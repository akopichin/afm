//go:build !windows

package main

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// adoptOwnerTokenFD accepts fd only if it is an open pipe and takes it over: close-on-exec at once, and an
// *os.File so the number stays reserved. A number nobody inherited (afm's own files would reuse it) or a regular
// file is refused before a byte is written.
func adoptOwnerTokenFD(fd int) (*os.File, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, errors.New("is not an open descriptor")
	}
	if st.Mode&unix.S_IFMT != unix.S_IFIFO {
		return nil, errors.New("is not a pipe")
	}
	unix.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), "owner-token"), nil
}
