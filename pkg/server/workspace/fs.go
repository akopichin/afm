package workspace

import (
	"context"
	"errors"
)

// FS is the secure-filesystem backend for the Docker project file browser.
// Implementations resolve a root ID + relative path pair to real files on
// disk, keeping every access confined to the declared roots.
type FS interface {
	Roots() []RootView
	List(ctx context.Context, rootID, relPath, cursor string) (Page, error)
	Reference(ctx context.Context, rootID, relPath string) (Reference, error)
	Read(ctx context.Context, rootID, relPath string) (File, error)
	Diff(ctx context.Context, rootID, relPath string) (Diff, error)
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
