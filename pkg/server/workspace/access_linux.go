//go:build linux

package workspace

import (
	"errors"

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
