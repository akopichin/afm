//go:build !windows

package executor

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"syscall"
)

// ProcessDead reports whether pid no longer runs: it is gone, or it is a zombie. A killed orphan stays a zombie
// until init reaps it, and a container started without an init never does; for these tests it is dead.
// Exported for the executor_test package.
func ProcessDead(pid int) bool {
	if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return true
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat") // Linux only; macOS has no /proc
	if err != nil {
		return false
	}
	// The state follows the command name in parentheses, which may itself contain ") ".
	end := bytes.LastIndexByte(stat, ')')
	return end >= 0 && end+2 < len(stat) && stat[end+2] == 'Z'
}
