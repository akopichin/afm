package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureGitignoreEntry(t *testing.T) {
	dir := t.TempDir()
	if err := ensureGitignoreEntry(dir, ".afm/secrets.env"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.Contains(string(data), ".afm/secrets.env") {
		t.Errorf("entry not written: %s", data)
	}
	// idempotent
	if err := ensureGitignoreEntry(dir, ".afm/secrets.env"); err != nil {
		t.Fatal(err)
	}
	data2, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if got := strings.Count(string(data2), ".afm/secrets.env"); got != 1 {
		t.Errorf("expected 1 entry, got %d: %s", got, data2)
	}
}
