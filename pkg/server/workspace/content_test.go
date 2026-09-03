//go:build linux

package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestRead_And_Reference exercises the real Read/Reference implementations
// end to end on a live temp root: a small text file is read with the right
// language/displayPath/marker, an oversized file is rejected with
// ErrTooLarge, a file with a NUL byte is rejected with ErrBinary, and
// Reference (unlike Read) still succeeds for the oversized file — it never
// reads the content, only builds the marker.
func TestRead_And_Reference(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "a.go"), "package a\n")
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), make([]byte, (2<<20)+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin"), []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}

	fs, err := New([]Root{{ID: "project", Label: "afm", Path: dir, Kind: "project"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs.Roots()) == 0 {
		t.Skip("secure file browser unavailable on this kernel (no openat2)")
	}
	defer fs.Close()

	f, err := fs.Read(context.Background(), "project", "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if f.Language != "go" || f.Content != "package a\n" || f.DisplayPath != "afm/a.go" {
		t.Errorf("bad file: %+v", f)
	}
	wantMarker := buildMarker(filepath.Join(dir, "a.go"))
	if f.Reference != wantMarker {
		t.Errorf("marker: got %q want %q", f.Reference, wantMarker)
	}
	if f.ETag == "" {
		t.Error("ETag must not be empty")
	}

	if _, err := fs.Read(context.Background(), "project", "big.txt"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("big → ErrTooLarge, got %v", err)
	}
	if _, err := fs.Read(context.Background(), "project", "bin"); !errors.Is(err, ErrBinary) {
		t.Errorf("bin → ErrBinary, got %v", err)
	}

	// Reference is still allowed on a big/binary file — it never reads the
	// content, only opens it securely and builds the marker.
	ref, err := fs.Reference(context.Background(), "project", "big.txt")
	if err != nil {
		t.Errorf("reference on big must be allowed: %v", err)
	}
	if ref.DisplayPath != "afm/big.txt" {
		t.Errorf("ref.DisplayPath = %q", ref.DisplayPath)
	}

	// Reference on a directory is rejected.
	mustMkdirAll(t, filepath.Join(dir, "sub"))
	if _, err := fs.Reference(context.Background(), "project", "sub"); !errors.Is(err, ErrNotFound) {
		t.Errorf("reference on dir → ErrNotFound, got %v", err)
	}
}
