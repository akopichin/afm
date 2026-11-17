//go:build !unix

package serve

import "os/exec"

// detach has nothing to do where sessions do not exist; managed runs are only verified on Linux.
func detach(*exec.Cmd) {}
