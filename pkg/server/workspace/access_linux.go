//go:build linux

package workspace

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// resolveFlags is the openat2 resolve policy that makes this the security
// boundary of the file browser:
//
//   - RESOLVE_BENEATH       — every path component must stay at or below the
//     root dir fd; any ".." that would escape fails with EXDEV.
//   - RESOLVE_NO_MAGICLINKS — no /proc-style magic-link traversal.
//   - RESOLVE_NO_SYMLINKS   — no symlink is ever followed; a symlink component
//     (including the final one) fails with ELOOP.
const resolveFlags = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS

// Open-flag combinations for the read paths, defined per platform because
// O_DIRECTORY / O_NONBLOCK are not portable — Windows' syscall package lacks
// them, so referencing syscall.O_DIRECTORY from a build-tag-free file broke the
// windows cross-build. list.go/content.go use these package constants instead.
const (
	openDirReadOnly      = unix.O_RDONLY | unix.O_DIRECTORY // List: open a directory fd
	openFileReadNonblock = unix.O_RDONLY | unix.O_NONBLOCK  // Read/Reference: never block on a FIFO
)

// rootHandle pins one browsable root by an open directory fd. All access to
// that root goes through openat, so the kernel — not string math — enforces
// containment.
type rootHandle struct {
	root Root
	dirf int // O_DIRECTORY|O_PATH fd of the root directory
}

// openRootDir opens the root directory itself as an O_PATH handle. O_PATH means
// we hold the directory only as an anchor for openat2, without read/traverse
// intent of its own.
func openRootDir(path string) (int, error) {
	return unix.Open(path, unix.O_DIRECTORY|unix.O_PATH|unix.O_CLOEXEC, 0)
}

// openat opens relPath relative to the root dir fd under resolveFlags. It maps
// the kernel's containment/symlink refusals onto the package's sentinel errors;
// any other errno is returned raw for the caller to classify.
func (r *rootHandle) openat(relPath string, flags int) (int, error) {
	how := &unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC),
		Resolve: resolveFlags,
	}
	fd, err := unix.Openat2(r.dirf, relPath, how)
	if err != nil {
		switch {
		case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV):
			// ELOOP: a symlink component under RESOLVE_NO_SYMLINKS.
			// EXDEV: a ".." / mount escape under RESOLVE_BENEATH.
			return -1, ErrSymlink
		case errors.Is(err, unix.ENOENT):
			return -1, ErrNotFound
		default:
			return -1, err
		}
	}
	return fd, nil
}

// probeOpenat2Supported checks that openat2 exists on the running kernel by
// opening "." beneath the given root dir fd. On kernels < 5.6 openat2 is not
// wired up and this returns ENOSYS, which New uses to degrade gracefully.
func probeOpenat2Supported(dirf int) error {
	how := &unix.OpenHow{
		Flags:   uint64(unix.O_PATH | unix.O_CLOEXEC),
		Resolve: resolveFlags,
	}
	fd, err := unix.Openat2(dirf, ".", how)
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

// closeFD closes a file descriptor.
func closeFD(fd int) error {
	return unix.Close(fd)
}

// maxGitfileBytes bounds the read of a `.git` gitfile. A real gitfile holds a
// single short `gitdir: <path>` line; anything larger is not a gitfile.
const maxGitfileBytes = 4 << 10

// gitEntry classifies the `.git` entry at relGit (root-relative, slash-path)
// opened through the secure openat2 fd, so containment/symlink policy is the
// kernel's job, not string math. It first opens O_PATH — which never follows a
// symlink (RESOLVE_NO_SYMLINKS → ErrSymlink) and never blocks on a fifo/device
// — to fstat the kind. Only a confirmed regular file is reopened O_RDONLY and
// read (bounded), so a gitfile's target can be parsed. A directory reports
// isDir; anything that is neither dir nor regular reports both false.
func (r *rootHandle) gitEntry(relGit string) (isDir, isRegular bool, content []byte, err error) {
	fd, err := r.openat(relGit, unix.O_PATH)
	if err != nil {
		return false, false, nil, err // ErrSymlink / ErrNotFound / raw errno
	}
	pf := os.NewFile(uintptr(fd), relGit)
	st, serr := pf.Stat()
	_ = pf.Close()
	if serr != nil {
		return false, false, nil, serr
	}
	mode := st.Mode()
	switch {
	case mode.IsDir():
		return true, false, nil, nil
	case mode.IsRegular():
		rfd, rerr := r.openat(relGit, unix.O_RDONLY)
		if rerr != nil {
			return false, true, nil, rerr
		}
		rf := os.NewFile(uintptr(rfd), relGit)
		data, derr := io.ReadAll(io.LimitReader(rf, maxGitfileBytes))
		_ = rf.Close()
		if derr != nil {
			return false, true, nil, derr
		}
		return false, true, data, nil
	default:
		return false, false, nil, nil
	}
}
