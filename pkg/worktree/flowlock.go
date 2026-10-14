package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/akopichin/afm/pkg/progress"
)

// FlowLock is a per-worktree advisory lock serializing a flow's run lifetime against a destructive
// worktree clear. A run holds it for as long as its agents may be using the worktree; a clear
// operation holds it only through Ensure's destructive git calls.
//
// It is keyed by the worktree's name (the flow name by default, or an explicit --worktree override),
// not by any specific run directory: resolveRun (cmd/afm/run.go) starts a brand-new run directory
// whenever the latest one is locked, so a run can restart into a fresh directory mid-flight, and a
// snapshot of "which run directories currently exist" can go stale the moment it's taken. A lock keyed
// by the worktree name itself has no such snapshot to go stale.
type FlowLock struct {
	l *progress.Lock
}

// ErrFlowLockBusy is returned by TryLockFlow when another process already holds name's lock.
var ErrFlowLockBusy = errors.New("worktree: flow lock busy")

func flowLockPath(locksDir, name string) string {
	return filepath.Join(locksDir, name+".worktree.lock")
}

// TryLockFlow attempts to exclusively, non-blockingly lock name's worktree. Returns
// ErrFlowLockBusy (not a sentinel wrapped in a generic error) when another process already holds it.
func TryLockFlow(locksDir, name string) (*FlowLock, error) {
	if err := os.MkdirAll(locksDir, 0o755); err != nil {
		return nil, fmt.Errorf("worktree: create lock dir %s: %w", locksDir, err)
	}
	l, err := progress.NewLock(flowLockPath(locksDir, name))
	if err != nil {
		return nil, err
	}
	if err := l.TryLock(); err != nil {
		if errors.Is(err, progress.ErrLockBusy) {
			return nil, ErrFlowLockBusy
		}
		return nil, err
	}
	return &FlowLock{l: l}, nil
}

// Close releases the lock. Safe to call on a nil *FlowLock (nothing to release).
func (l *FlowLock) Close() error {
	if l == nil {
		return nil
	}
	l.l.Unlock()
	return nil
}
