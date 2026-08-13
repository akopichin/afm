package afmsdk

import (
	"context"
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

func TestPickFreePort_ReturnsDistinctPorts(t *testing.T) {
	p1, err := pickFreePort()
	if err != nil {
		t.Fatalf("pickFreePort: %v", err)
	}
	if p1 <= 0 {
		t.Fatalf("port: got %d, want positive", p1)
	}
	p2, err := pickFreePort()
	if err != nil {
		t.Fatalf("pickFreePort: %v", err)
	}
	if p1 == p2 {
		t.Errorf("expected two different ports, got %d twice", p1)
	}
}

func TestNewRunDir_CreatesDirectoryUnderBase(t *testing.T) {
	base := t.TempDir()
	dir, err := newRunDir(base)
	if err != nil {
		t.Fatalf("newRunDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %q: %v", dir, err)
	}
	if !info.IsDir() {
		t.Errorf("%q is not a directory", dir)
	}
	if filepath.Dir(dir) != base {
		t.Errorf("dir parent: got %q, want %q", filepath.Dir(dir), base)
	}
}

func TestNewRunDir_TwoCallsProduceDistinctDirs(t *testing.T) {
	base := t.TempDir()
	d1, err := newRunDir(base)
	if err != nil {
		t.Fatalf("newRunDir: %v", err)
	}
	d2, err := newRunDir(base)
	if err != nil {
		t.Fatalf("newRunDir: %v", err)
	}
	if d1 == d2 {
		t.Errorf("expected distinct run dirs, got %q twice", d1)
	}
}

func TestAcquire_Unlimited_NeverBlocks(t *testing.T) {
	c := &Client{}
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		release, err := c.acquire(ctx)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		release()
	}
}

func TestAcquire_RespectsLimit(t *testing.T) {
	c := &Client{sem: make(chan struct{}, 1)}
	ctx := context.Background()

	release1, err := c.acquire(ctx)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		release2, err := c.acquire(ctx)
		if err != nil {
			t.Errorf("second acquire: %v", err)
			return
		}
		close(acquired)
		release2()
	}()

	select {
	case <-acquired:
		t.Fatal("second acquire succeeded before first release")
	case <-time.After(100 * time.Millisecond):
	}

	release1()
	select {
	case <-acquired:
	case <-time.After(1 * time.Second):
		t.Fatal("second acquire did not unblock after release")
	}
}

func TestAcquire_RespectsContextCancellation(t *testing.T) {
	c := &Client{sem: make(chan struct{}, 1)}
	release1, err := c.acquire(context.Background())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release1()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.acquire(ctx); err == nil {
		t.Fatal("expected error from cancelled context")
	}
}
