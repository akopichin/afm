package memory

import (
	"fmt"
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

// AtomicWrite fsyncs a temp file, renames it, then fsyncs the parent. Parent-sync failure leaves the
// rename applied; do not retry it.
func AtomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, "tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open parent for fsync: %w", err)
	}
	if err := d.Sync(); err != nil {
		d.Close()
		return fmt.Errorf("fsync parent: %w", err)
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("close parent after fsync: %w", err)
	}
	return nil
}
