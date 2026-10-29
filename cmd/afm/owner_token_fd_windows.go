//go:build windows

package main

import (
	"errors"
	"os"
)

func adoptOwnerTokenFD(int) (*os.File, error) {
	return nil, errors.New("is not supported on Windows")
}
