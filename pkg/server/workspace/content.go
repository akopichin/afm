package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"unicode/utf8"
)

// maxContentBytes caps the size of a file Read returns inline. Larger files
// are rejected with ErrTooLarge — the caller should use Reference instead and
// let the agent read the file itself with its own tool.
const maxContentBytes = 2 << 20

// buildMarker returns the "[AFM file: ...]" reference string embedded in
// agent prompts. abs is JSON-string-encoded so quotes, backslashes, and
// newlines in the path are all safe to splice verbatim into a prompt.
func buildMarker(abs string) string {
	encoded, _ := json.Marshal(abs) // Marshal of a string never errors.
	return "[AFM file: " + string(encoded) + "]"
}

// Read returns the full content of relPath inside rootID, along with the
// metadata needed to render and diff it. Symlinks and directories are
// rejected (via resolve/openat and the Stat check below); files over
// maxContentBytes are rejected with ErrTooLarge; files containing a NUL byte
// or invalid UTF-8 are rejected with ErrBinary — Reference is the fallback
// for both large and binary files.
func (fs *fsImpl) Read(ctx context.Context, rootID, relPath string) (File, error) {
	if err := ctx.Err(); err != nil {
		return File{}, err
	}

	rh, clean, err := fs.resolve(rootID, relPath)
	if err != nil {
		return File{}, err
	}

	fd, err := rh.openat(clean, syscall.O_RDONLY)
	if err != nil {
		return File{}, err
	}
	f := os.NewFile(uintptr(fd), clean)
	defer f.Close()

	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return File{}, ErrNotFound
	}
	if st.Size() > maxContentBytes {
		return File{}, ErrTooLarge
	}

	data, err := io.ReadAll(io.LimitReader(f, maxContentBytes+1))
	if err != nil {
		return File{}, ErrReadFailed
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return File{}, ErrBinary
	}

	abs := filepath.Join(rh.root.Path, clean)
	return File{
		Path:        clean,
		DisplayPath: rh.root.Label + "/" + clean,
		Reference:   buildMarker(abs),
		Language:    detectLanguage(clean),
		Size:        st.Size(),
		ModifiedAt:  st.ModTime(),
		Content:     string(data),
		ETag:        fmt.Sprintf("\"%x-%x\"", st.ModTime().UnixNano(), st.Size()),
	}, nil
}

// Reference returns a lightweight identifier for relPath — its display path
// and the "[AFM file: ...]" marker used in agent prompts — without reading
// the content. Unlike Read it has no size or binary gate: a large or binary
// file can still be referenced, since the agent reads it with its own tool
// rather than through afm's JSON API. It still rejects a directory or a
// symlink (via resolve/openat and the Stat check below), since a marker only
// makes sense for a real regular file.
func (fs *fsImpl) Reference(ctx context.Context, rootID, relPath string) (Reference, error) {
	if err := ctx.Err(); err != nil {
		return Reference{}, err
	}

	rh, clean, err := fs.resolve(rootID, relPath)
	if err != nil {
		return Reference{}, err
	}

	fd, err := rh.openat(clean, syscall.O_RDONLY)
	if err != nil {
		return Reference{}, err
	}
	f := os.NewFile(uintptr(fd), clean)
	defer f.Close()

	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return Reference{}, ErrNotFound
	}

	abs := filepath.Join(rh.root.Path, clean)
	return Reference{
		Path:        clean,
		DisplayPath: rh.root.Label + "/" + clean,
		Reference:   buildMarker(abs),
	}, nil
}
