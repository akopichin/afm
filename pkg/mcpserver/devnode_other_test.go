//go:build !linux && !darwin

package mcpserver

import "errors"

func mknodNull(string) error { return errors.ErrUnsupported }
