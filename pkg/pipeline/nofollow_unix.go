//go:build unix

package pipeline

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// openComponent opens the single path element name inside dir without following a symlink. O_NONBLOCK keeps a FIFO
// put in place of a package file from blocking the open; callers check the type of what they opened.
func openComponent(dir *os.File, name string) (*os.File, error) {
	for {
		fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.ELOOP) {
			return nil, errLink
		}
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), name), nil
	}
}
