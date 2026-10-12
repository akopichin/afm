//go:build !linux

package workspace

import "errors"

// errUnsupported marks the whole secure-open mechanism as unavailable off Linux. New treats it exactly
// like the ENOSYS degradation path: zero roots, no error, capability off.
var errUnsupported = errors.New("workspace: secure file browser is only supported on Linux (openat2)")

// Parity stubs for the Linux open-flag constants so list.go/content.go compile cross-platform.
const (
	openDirReadOnly      = 0
	openFileReadNonblock = 0
)

// rootHandle mirrors the Linux type so workspace.go compiles cross-platform.
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

// gitEntry is a non-Linux stub; secure workspace access is unavailable.
//
//nolint:unused // parity stub for the Linux gitEntry; called by Diff's repo walk
func (r *rootHandle) gitEntry(string) (isDir, isRegular bool, content []byte, err error) {
	return false, false, nil, errUnsupported
}
