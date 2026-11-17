package runregistry

import (
	"fmt"
	"strconv"

	"golang.org/x/sys/unix"
)

// ProcessStart returns an opaque marker of when pid started: the same PID with another marker is another
// process.
func ProcessStart(pid int) (string, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if int(kp.Proc.P_pid) != pid {
		return "", fmt.Errorf("runregistry: process %d not found", pid)
	}
	start := kp.Proc.P_starttime
	return strconv.FormatInt(int64(start.Sec), 10) + "." + strconv.FormatInt(int64(start.Usec), 10), nil
}
