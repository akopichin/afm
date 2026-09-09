package state

import (
	"errors"
	"path/filepath"

	"github.com/akopichin/afm/pkg/progress"
)

// RunLock — самостоятельная, только для чтения ручка на flock run-директории
// (без открытия events.jsonl/state.json и без побочных эффектов на диске,
// кроме самого файла .lock). Используется командами вроде `afm memory rebuild`,
// которым нужно убедиться, что run сейчас не пишет живой afm-процесс, но не
// нужно открывать Store целиком.
type RunLock struct {
	l *progress.Lock
}

// TryLockRun пытается неблокирующе захватить flock run-директории.
// ErrRunLocked — run уже открыт другим процессом afm (см. acquireRunLock);
// любая другая ошибка (недоступный каталог, нехватка дескрипторов и т.п.)
// пробрасывается как есть — это не contention.
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

// acquireRunLock — общая точка захвата flock run-директории, разделяемая
// Store.Open и TryLockRun. Отличает реальную занятость лока (ErrRunLocked)
// от сбоя ввода-вывода (EACCES, отсутствующий каталог, исчерпание
// дескрипторов и т.п.), который пробрасывается вызывающему как есть —
// раньше любая ошибка TryLock молча превращалась в ErrRunLocked (review #8).
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
