// Package workspace defines the domain core for the Docker project file
// browser: value types shared between the filesystem backend (FS) and the
// HTTP handlers that serve them directly as JSON.
package workspace

import "time"

// Root is a browsable project root (e.g. the workspace root, an .afm run
// directory, ...). It is internal — never serialized — and holds the
// absolute path inside the container.
type Root struct {
	ID, Label, Path, Kind string
	MountReadOnly         bool
}

// RootView is the wire representation of a Root returned to the frontend.
type RootView struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	Kind          string `json:"kind"`
	MountReadOnly bool   `json:"mount_read_only"`
}

// Entry is one file/directory/symlink listed inside a directory listing.
type Entry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Kind       string `json:"kind"` // file | directory | symlink
	Size       int64  `json:"size,omitempty"`
	Language   string `json:"language,omitempty"`
	Selectable bool   `json:"selectable"`
}

// Page is a (possibly partial) directory listing with cursor-based pagination.
type Page struct {
	Entries    []Entry `json:"entries"`
	NextCursor string  `json:"next_cursor,omitempty"`
}

// File is the full content of a single file, along with metadata needed to
// render and diff it.
type File struct {
	Path        string    `json:"path"`
	DisplayPath string    `json:"display_path"`
	Reference   string    `json:"reference"`
	Language    string    `json:"language"`
	Size        int64     `json:"size"`
	ModifiedAt  time.Time `json:"modified_at"`
	Content     string    `json:"content"`
	ETag        string    `json:"-"` // sent as an HTTP header, never in the JSON body
}

// Reference is a lightweight identifier for a file, without its content.
type Reference struct {
	Path        string `json:"path"`
	DisplayPath string `json:"display_path"`
	Reference   string `json:"reference"`
}

// Diff is a unified diff of a file against its baseline.
type Diff struct {
	Path      string `json:"path"`
	Baseline  string `json:"baseline"`
	Status    string `json:"status"` // clean | modified | added
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	Diff      string `json:"diff"`
}
