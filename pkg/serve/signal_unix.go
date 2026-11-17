//go:build unix

package serve

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// errIdentityChanged means the PID now belongs to another process (or its identity cannot be proven).
var errIdentityChanged = errors.New("the process is not the one that was started")

// signalProcess sends sig to pid only when its start marker is the recorded one. The process handle is taken
// before the check, so on Linux (pidfd) the signal cannot reach a process that reused the PID afterwards.
func signalProcess(pid int, marker string, sig syscall.Signal) error {
	if marker == "" {
		return fmt.Errorf("pid %d: %w: no start marker recorded", pid, errIdentityChanged)
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	defer func() { _ = p.Release() }()
	got, err := processStart(pid)
	if err != nil {
		return fmt.Errorf("pid %d: %w", pid, err)
	}
	if got != marker {
		return fmt.Errorf("pid %d: %w", pid, errIdentityChanged)
	}
	return p.Signal(sig)
}
