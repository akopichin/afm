package memorypipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/akopichin/afm/pkg/fsutil"
	"github.com/akopichin/afm/pkg/progress"
)

// MemoryLockPath returns the fixed path of the interprocess lock file that guards a memory directory.
func MemoryLockPath(memoryDir string) (string, error) {
	abs, err := filepath.Abs(memoryDir)
	if err != nil {
		return "", err
	}
	canon, err := fsutil.CanonicalPath(abs)
	if err != nil {
		canon = abs
	}

	sum := sha256.Sum256([]byte(canon))

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	locks := filepath.Join(home, ".afm", "locks")
	if err := os.MkdirAll(locks, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(locks, "memory-"+hex.EncodeToString(sum[:])+".lock"), nil
}

// MemoryLock is a held interprocess lock on a memory directory, acquired via
// AcquireMemoryLock. It must be released with Close.
type MemoryLock struct {
	l *progress.Lock
}

// AcquireMemoryLock blocks until it acquires the exclusive lock for memoryDir, ctx is cancelled, or a
// genuine I/O error occurs.
func AcquireMemoryLock(ctx context.Context, memoryDir string) (*MemoryLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	path, err := MemoryLockPath(memoryDir)
	if err != nil {
		return nil, err
	}

	l, err := progress.NewLock(path)
	if err != nil {
		return nil, err
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		err := l.TryLock()
		if err == nil {
			return &MemoryLock{l: l}, nil
		}
		if !errors.Is(err, progress.ErrLockBusy) {
			return nil, err
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Close releases the lock. Safe to call multiple times.
func (m *MemoryLock) Close() error {
	if m.l != nil {
		m.l.Unlock()
		m.l = nil
	}
	return nil
}
