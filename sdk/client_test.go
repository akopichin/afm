package afmsdk

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNew_ResolvesBinaryFromPath(t *testing.T) {
	dir := t.TempDir()
	fakeAfm := filepath.Join(dir, "afm")
	if err := os.WriteFile(fakeAfm, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	c, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.binary != fakeAfm {
		t.Errorf("binary: got %q, want %q", c.binary, fakeAfm)
	}
}

func TestNew_MissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected error when afm is not on PATH")
	}
}

func TestNew_ExplicitBinaryIsNotLookedUp(t *testing.T) {
	c, err := New(Config{Binary: "/some/explicit/path/afm"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.binary != "/some/explicit/path/afm" {
		t.Errorf("binary: got %q, want explicit path", c.binary)
	}
}

func TestNew_DefaultsBaseDirToTempDir(t *testing.T) {
	c, err := New(Config{Binary: "/bin/true"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.baseDir != os.TempDir() {
		t.Errorf("baseDir: got %q, want %q", c.baseDir, os.TempDir())
	}
}

func TestNew_HonorsExplicitBaseDir(t *testing.T) {
	c, err := New(Config{Binary: "/bin/true", BaseDir: "/custom/base"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.baseDir != "/custom/base" {
		t.Errorf("baseDir: got %q, want %q", c.baseDir, "/custom/base")
	}
}

func TestNew_SetsHTTPClientTimeout(t *testing.T) {
	c, err := New(Config{Binary: "/bin/true"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.httpClient == nil || c.httpClient.Timeout != 10*time.Second {
		t.Errorf("httpClient timeout: got %v, want 10s", c.httpClient)
	}
}
