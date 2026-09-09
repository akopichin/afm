package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryRebuildCmd_Registered(t *testing.T) {
	c, _, err := newRootCmd().Find([]string{"memory", "rebuild"})
	if err != nil || c.Name() != "rebuild" {
		t.Fatalf("got %v %v", c, err)
	}
}

func TestMemoryRebuildCmd_CommitFlagsMutuallyExclusive(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"memory", "rebuild", "flow.yaml", "--commit", "--no-commit"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "exclusive") {
		t.Fatalf("want mutual-exclusion error, got %v", err)
	}
}

func TestResolveExplicitRunDir(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "flow-20260101-000000-aaaa"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveExplicitRunDir(base, "flow", "flow-20260101-000000-aaaa"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"../escape", "sub/flow-x", `flow-2026\escape`, "flow\\x", ".", "..", "/abs",
		"other-20260101-000000-aaaa", "flow-20260101-000000-zzzz",
	} {
		if _, err := resolveExplicitRunDir(base, "flow", bad); err == nil {
			t.Errorf("want error for %q", bad)
		}
	}
}

func TestResolveExplicitRunDir_RejectsSymlink(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, "flow-20260101-000000-aaaa")); err != nil {
		t.Skip("symlink unsupported")
	}
	if _, err := resolveExplicitRunDir(base, "flow", "flow-20260101-000000-aaaa"); err == nil {
		t.Fatal("symlinked run id must be rejected")
	}
}
