//go:build linux || darwin

package mcpserver

import "golang.org/x/sys/unix"

// mknodNull creates a character device with the numbers of /dev/null, so opening it touches no real hardware.
func mknodNull(path string) error {
	var st unix.Stat_t
	if err := unix.Stat("/dev/null", &st); err != nil {
		return err
	}
	return unix.Mknod(path, unix.S_IFCHR|0o644, int(st.Rdev))
}
