package memorypipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/akopichin/afm/pkg/progress"
)

// MemoryLockPath returns the fixed path of the interprocess lock file that
// guards a memory directory. The path is derived from the memory dir's
// canonical absolute location (symlinks of the deepest EXISTING ancestor
// resolved, review #14 — the memory dir itself may not exist yet), so two
// different-looking paths that alias the same real directory (e.g. via a
// symlink) hash to the same lock file. The lock lives under
// ~/.afm/locks, not inside memoryDir itself — the memory dir may not exist
// yet, and the lock must not become part of the content it's protecting.
func MemoryLockPath(memoryDir string) (string, error) {
	abs, err := filepath.Abs(memoryDir)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	canon := canonicalizeExistingAncestor(abs)

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

// AcquireMemoryLock blocks (polling every 100ms) until it acquires the
// exclusive lock for memoryDir, ctx is cancelled, or a genuine (non-busy)
// I/O error occurs. Contention (progress.ErrLockBusy) is retried; any other
// error from TryLock propagates immediately — it is not lock contention and
// retrying it would just spin (review #8).
//
// The lock is acquired by the CALLER (CLI / orchestrator), not inside
// Finalize — Finalize itself assumes the memory dir is already guarded.
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
