package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

// maxContentBytes caps the size of a file Read returns inline.
const maxContentBytes = 2 << 20

// buildMarker returns the "[AFM file: ...]" reference string embedded in agent prompts.
func buildMarker(abs string) string {
	encoded, _ := json.Marshal(abs) // Marshal of a string never errors.
	return "[AFM file: " + string(encoded) + "]"
}

// Read returns the full content of relPath inside rootID, along with the metadata needed to render and
// diff it.
func (fs *fsImpl) Read(ctx context.Context, rootID, relPath string) (File, error) {
	if err := ctx.Err(); err != nil {
		return File{}, err
	}

	rh, clean, err := fs.resolve(rootID, relPath)
	if err != nil {
		return File{}, err
	}

	// O_NONBLOCK matters only for a FIFO: without it, opening one O_RDONLY blocks until a writer shows up —
	// potentially forever, hanging this call.
	fd, err := rh.openat(clean, openFileReadNonblock)
	if err != nil {
		return File{}, err
	}
	f := os.NewFile(uintptr(fd), clean)
	defer f.Close()

	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		// Rejects directories along with any special file (FIFO, socket,
		// device) — the same sentinel as a plain "not a viewable file".
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

// Reference returns a lightweight identifier for relPath — its display path and the "[AFM file: ...]"
// marker used in agent prompts — without reading the content.
func (fs *fsImpl) Reference(ctx context.Context, rootID, relPath string) (Reference, error) {
	if err := ctx.Err(); err != nil {
		return Reference{}, err
	}

	rh, clean, err := fs.resolve(rootID, relPath)
	if err != nil {
		return Reference{}, err
	}

	// See the O_NONBLOCK note in Read: without it, opening a FIFO O_RDONLY
	// can block this call forever waiting for a writer.
	fd, err := rh.openat(clean, openFileReadNonblock)
	if err != nil {
		return Reference{}, err
	}
	f := os.NewFile(uintptr(fd), clean)
	defer f.Close()

	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return Reference{}, ErrNotFound
	}

	abs := filepath.Join(rh.root.Path, clean)
	return Reference{
		Path:        clean,
		DisplayPath: rh.root.Label + "/" + clean,
		Reference:   buildMarker(abs),
	}, nil
}
