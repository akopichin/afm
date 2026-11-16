//go:build unix

package mcpserver

import (
	"os"

	"golang.org/x/sys/unix"
)

// openAt opens the single component name inside dir, never through a symlink. O_NONBLOCK keeps a FIFO put in place
// of a file from blocking the open; the caller rejects whatever is not a regular file.
func openAt(dir *os.File, name string, wantDir bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	if wantDir {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(int(dir.Fd()), name, flags, 0)
	if err != nil {
		return nil, &os.PathError{Op: "openat", Path: name, Err: err}
	}
	return os.NewFile(uintptr(fd), name), nil
}
