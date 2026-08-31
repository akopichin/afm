package memory

import (
	"os"
	"path/filepath"
)

// ProjectFile returns the path to the project memory file in the given directory.
// Returns: <dir>/memory.md
func ProjectFile(dir string) string {
	return filepath.Join(dir, "memory.md")
}

// StageFile returns the path to a stage-relative file in the given directory.
// Returns: <dir>/<rel> (rel may be "sub/f.md" or other relative paths)
func StageFile(dir, rel string) string {
	return filepath.Join(dir, rel)
}

// AtomicWrite writes data to path atomically using temp+rename.
// It creates parent directories as needed with MkdirAll.
func AtomicWrite(path string, data []byte) error {
	// Create parent directories
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	// Write to a temporary file in the same directory (for atomic rename)
	tmpFile, err := os.CreateTemp(filepath.Dir(path), "tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()

	// Write data to temp file
	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		_ = os.Remove(tmpPath)
		return err
	}

	// Close before rename
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	// Atomically rename temp file to target path
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	return nil
}
