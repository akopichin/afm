//go:build !unix

package mcpserver

import "errors"

func mkfifo(string) error { return errors.New("unsupported") }
