package state

import (
	"errors"
	"path/filepath"

	"github.com/akopichin/afm/pkg/progress"
)

// RunLock — самостоятельная, только для чтения ручка на flock run-директории.
type RunLock struct {
	l *progress.Lock
}

// TryLockRun пытается неблокирующе захватить flock run-директории.
func TryLockRun(runDir string) (*RunLock, error) {
	l, err := acquireRunLock(runDir)
	if err != nil {
		return nil, err
	}
	return &RunLock{l: l}, nil
}

// Close снимает flock. Безопасно вызывать повторно.
func (r *RunLock) Close() error {
	if r.l != nil {
		r.l.Unlock()
		r.l = nil
	}
	return nil
}

// acquireRunLock — общая точка захвата flock run-директории, разделяемая Store.Open и TryLockRun.
func acquireRunLock(runDir string) (*progress.Lock, error) {
	l, err := progress.NewLock(filepath.Join(runDir, ".lock"))
	if err != nil {
		return nil, err
	}
	if err := l.TryLock(); err != nil {
		if errors.Is(err, progress.ErrLockBusy) {
			return nil, ErrRunLocked
		}
		return nil, err
	}
	return l, nil
}
