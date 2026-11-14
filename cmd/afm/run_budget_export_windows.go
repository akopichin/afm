//go:build windows

package main

import "os"

// Windows has no access(2); the mode bits are what the read-only attribute maps to.
func writable(_ string, info os.FileInfo) bool { return info.Mode().Perm()&0o200 != 0 }

func writableDir(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}
