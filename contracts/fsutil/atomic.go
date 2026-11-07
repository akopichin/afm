package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// AtomicWriteFile atomically replaces path with data: it writes and fsyncs a same-directory temp file with
// permissions perm (set before the rename, regardless of umask), renames it over path, then fsyncs the parent
// directory (skipped on Windows, where a directory cannot be fsynced). Missing parent directories are created
// with 0755.
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dirName := filepath.Dir(path)
	if err := os.MkdirAll(dirName, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dirName, err)
	}
	tmp, err := os.CreateTemp(dirName, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp: %w", err)
	}
	// На Windows fsync папки невозможен: FlushFileBuffers требует права записи, а os.Open открывает
	// папку только на чтение.
	if runtime.GOOS == "windows" { //nolint:goconst // второе вхождение — в тесте, константа не нужна
		return nil
	}
	dir, err := os.Open(dirName)
	if err != nil {
		return fmt.Errorf("open dir for fsync: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("fsync dir: %w", err)
	}
	return nil
}
