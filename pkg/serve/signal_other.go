//go:build !unix

package serve

import (
	"errors"
	"syscall"
)

var errIdentityChanged = errors.New("the process is not the one that was started")

func signalProcess(int, string, syscall.Signal) error {
	return errors.New("signalling managed runs is not supported on this platform")
}
