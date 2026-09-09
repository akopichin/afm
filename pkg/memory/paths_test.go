package memory

import (
	"os"
	"path/filepath"
	"strings"
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

func TestAtomicWrite_ReplacesExistingAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.md")
	if err := AtomicWrite(p, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(p, []byte("second")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "second" {
		t.Errorf("content = %q, want %q", b, "second")
	}
	assertNoLeftoverTemp(t, dir)
}

func TestAtomicWrite_TempCreateFailureSurfacesError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permission bits")
	}
	dir := t.TempDir()
	// Делаем директорию нерасширяемой: os.CreateTemp внутри неё должен
	// упасть с permission denied, и эта ошибка обязана дойти до вызывающего
	// кода (review #minor.1 — AtomicWrite больше не best-effort).
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	p := filepath.Join(dir, "f.md")
	err := AtomicWrite(p, []byte("data"))
	if err == nil {
		t.Fatal("expected an error when the parent directory forbids file creation")
	}
	if _, statErr := os.Stat(p); statErr == nil {
		t.Error("target file must not have been created on failure")
	}
}

// assertNoLeftoverTemp проверяет, что после успешной или неуспешной записи
// в dir не остаётся файлов вида tmp-* (os.CreateTemp с этим префиксом) —
// иначе временные файлы будут копиться при каждом падении на середине.
func assertNoLeftoverTemp(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "tmp-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}
