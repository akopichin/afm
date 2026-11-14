//go:build !windows

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func writable(path string, _ os.FileInfo) bool { return unix.Access(path, unix.W_OK) == nil }

func writableDir(dir string) bool { return unix.Access(dir, unix.W_OK) == nil }
