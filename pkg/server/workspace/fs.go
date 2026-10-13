package workspace

import (
	"context"
	"errors"
)

// ChangeMode selects the baseline for a changed-files listing.
type ChangeMode string

const (
	ChangeModeIndex ChangeMode = "index" // worktree vs index (+ untracked)
	ChangeModeHead  ChangeMode = "head"  // worktree vs HEAD (+ untracked)
	ChangeModeBase  ChangeMode = "base"  // linked worktree branches vs their fork point (committed work)
)

// FS is the secure-filesystem backend for the Docker project file browser.
type FS interface {
	Roots() []RootView
	// HasWorktrees reports whether rootID physically contains at least one linked git worktree. It runs a
	// git subprocess, so it takes a context and is called from the roots handler, never the hot path.
	HasWorktrees(ctx context.Context, rootID string) bool
	List(ctx context.Context, rootID, relPath, cursor string) (Page, error)
	Reference(ctx context.Context, rootID, relPath string) (Reference, error)
	Read(ctx context.Context, rootID, relPath string) (File, error)
	Diff(ctx context.Context, rootID, relPath string) (Diff, error)
	Changes(ctx context.Context, rootID string, mode ChangeMode) (ChangeList, error)
	Search(ctx context.Context, rootID, query string) (SearchResult, error)
	Close() error
}

// Sentinel errors mapped to HTTP status codes by the handler layer.
var (
	ErrInvalidRootOrPath = errors.New("workspace: invalid root or path")
	ErrNotFound          = errors.New("workspace: not found")
	ErrDiffUnavailable   = errors.New("workspace: diff unavailable")
	ErrTooLarge          = errors.New("workspace: file too large")
	ErrBinary            = errors.New("workspace: binary file")
	ErrSymlink           = errors.New("workspace: symlink not supported")
	ErrReadFailed        = errors.New("workspace: read failed")
)
