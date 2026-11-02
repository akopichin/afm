package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
)

// A relative afm-root (the default ".") must not leak into agent-facing paths: the agent's CWD is the project
// root or a worktree, where a relative root_dir or memory path would point somewhere else.
func TestPrepareRunEnvironment_RelativeAfmRootGivesAbsolutePaths(t *testing.T) {
	chdirTemp(t)
	dir, err := os.Getwd() // the resolved CWD (macOS reports /var temp dirs as /private/var)
	if err != nil {
		t.Fatal(err)
	}
	f := &flow.Flow{Name: "flow", RootDir: "src"}
	f.Memory.Path = "memory"

	env := prepareTestRunEnvironment(t, f, "")
	if want := filepath.Join(dir, "src"); env.RootDir != want {
		t.Errorf("RootDir = %q, want %q", env.RootDir, want)
	}
	if want := filepath.Join(dir, "src", "memory"); env.MemoryDir != want {
		t.Errorf("MemoryDir = %q, want %q", env.MemoryDir, want)
	}

	wt := prepareTestRunEnvironment(t, &flow.Flow{Name: "flow"}, filepath.Join(".worktrees", "feature"))
	if want := filepath.Join(dir, ".worktrees", "feature"); wt.RootDir != want {
		t.Errorf("worktree RootDir = %q, want %q", wt.RootDir, want)
	}
}

// prepareTestRunEnvironment is prepareRunEnvironment for a test: when it ends, PATH is restored and the afm-bin
// shim removed, so no later test resolves a bare `afm` to this test binary.
func prepareTestRunEnvironment(t *testing.T, f *flow.Flow, worktree string) runEnvironment {
	t.Helper()
	t.Setenv("PATH", os.Getenv("PATH"))
	env, err := prepareRunEnvironment(".", f, config.Default(), worktree)
	if err != nil {
		t.Fatalf("prepareRunEnvironment: %v", err)
	}
	t.Cleanup(env.Close)
	return env
}

func skipWithoutSymlinks(t *testing.T) {
	t.Helper()
	if err := os.Symlink(t.TempDir(), filepath.Join(t.TempDir(), "probe")); err != nil {
		t.Skipf("PATH shim needs symlinks: %v", err)
	}
}

// Agents run the afm of this process, not whichever afm the user's PATH finds first (a stale brew copy
// lacks the commands the prompt tells the agent to use).
func TestPrepareRunEnvironment_PutsThisAfmFirstOnPath(t *testing.T) {
	skipWithoutSymlinks(t)
	chdirTemp(t)
	oldDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(oldDir, "afm"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	originalPath := oldDir + string(os.PathListSeparator) + os.Getenv("PATH")
	t.Setenv("PATH", originalPath)

	env, err := prepareRunEnvironment(".", &flow.Flow{Name: "flow"}, config.Default(), "")
	if err != nil {
		t.Fatal(err)
	}
	dir, _, _ := strings.Cut(os.Getenv("PATH"), string(os.PathListSeparator))
	if !strings.HasPrefix(filepath.Base(dir), "afm-bin-") || env.AfmBinDir != dir {
		t.Fatalf("PATH starts with %q (AfmBinDir %q), want the afm-bin-* directory", dir, env.AfmBinDir)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wantTarget, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatal(err)
	}
	gotTarget, err := os.Readlink(filepath.Join(dir, "afm"))
	if err != nil || gotTarget != wantTarget {
		t.Fatalf("afm link = %q (%v), want %q", gotTarget, err, wantTarget)
	}

	env.Close()
	if got := os.Getenv("PATH"); got != originalPath {
		t.Errorf("PATH after Close = %q, want %q", got, originalPath)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("afm-bin directory survived Close: %v", err)
	}
}

// The same afm already first on PATH (Docker image, or a second prepare in one process) needs no shim.
func TestPrepareRunEnvironment_SkipsShimWhenPathAlreadyHasThisAfm(t *testing.T) {
	skipWithoutSymlinks(t)
	chdirTemp(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(binDir, "afm")); err != nil {
		t.Fatal(err)
	}
	originalPath := binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	t.Setenv("PATH", originalPath)

	env, err := prepareRunEnvironment(".", &flow.Flow{Name: "flow"}, config.Default(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	if env.AfmBinDir != "" || os.Getenv("PATH") != originalPath {
		t.Fatalf("shim installed although PATH already resolves afm to this binary: dir=%q PATH=%q", env.AfmBinDir, os.Getenv("PATH"))
	}
}
