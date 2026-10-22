// Package fsutil содержит небольшие общие помощники для файловой системы.
package fsutil

import (
	"os"
	"path/filepath"
)

// CanonicalPath возвращает абсолютный путь p, в котором раскрыты симлинки ближайшего существующего
// предка; отсутствующий хвост добавляется как есть. Ошибка — если предок не раскрывается
// (например, битый симлинк).
func CanonicalPath(p string) (string, error) {
	existing, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	var missing []string
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		missing = append([]string{filepath.Base(existing)}, missing...)
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{resolved}, missing...)...), nil
}
