//go:build !linux

package workspace

import "errors"

// errUnsupported marks the whole secure-open mechanism as unavailable off
// Linux. New treats it exactly like the ENOSYS degradation path: zero roots,
// no error, capability off.
var errUnsupported = errors.New("workspace: secure file browser is only supported on Linux (openat2)")

// rootHandle mirrors the Linux type so workspace.go compiles cross-platform.
// dirf is never populated here — openRootDir always errors, so New skips every
// root and no handle is ever constructed off Linux.
type rootHandle struct {
	root Root
	dirf int
}

func openRootDir(string) (int, error) { return -1, errUnsupported }

//nolint:unused // parity stub for the Linux openat; called by Tasks 7-9 read paths
func (r *rootHandle) openat(string, int) (int, error) { return -1, errUnsupported }

func probeOpenat2Supported(int) error { return errUnsupported }

// closeFD is a no-op off Linux — no descriptor was ever opened.
func closeFD(int) error { return nil }
