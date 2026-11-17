//go:build !unix

package serve

import (
	"errors"
	"os"
)

// lockFile is unsupported off unix: the host relies on flock for its single-writer guarantee.
func lockFile(string) (*os.File, error) {
	return nil, errors.New("afm serve requires a unix host")
}
