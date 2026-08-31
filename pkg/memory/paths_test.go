package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectAndStageFile(t *testing.T) {
	if ProjectFile("/m") != filepath.Join("/m", "memory.md") {
		t.Error("ProjectFile")
	}
	if StageFile("/m", "sub/b.md") != filepath.Join("/m", "sub/b.md") {
		t.Error("StageFile")
	}
}

func TestAtomicWrite_CreatesDirsAndFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a/b/c.md")
	if err := AtomicWrite(p, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "hi" {
		t.Errorf("content = %q", b)
	}
}
