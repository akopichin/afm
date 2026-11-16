//go:build !unix

package mcpserver

import (
	"errors"
	"os"
)

// openAt fails closed: without a descriptor-relative no-follow open, a swapped path could redirect the read.
func openAt(*os.File, string, bool) (*os.File, error) {
	return nil, errors.New("artifacts cannot be read safely on this platform")
}
