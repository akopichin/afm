package main

import (
	"os"
	"path/filepath"
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

	env, err := prepareRunEnvironment(".", f, config.Default(), "")
	if err != nil {
		t.Fatalf("prepareRunEnvironment: %v", err)
	}
	if want := filepath.Join(dir, "src"); env.RootDir != want {
		t.Errorf("RootDir = %q, want %q", env.RootDir, want)
	}
	if want := filepath.Join(dir, "src", "memory"); env.MemoryDir != want {
		t.Errorf("MemoryDir = %q, want %q", env.MemoryDir, want)
	}

	wt, err := prepareRunEnvironment(".", &flow.Flow{Name: "flow"}, config.Default(), filepath.Join(".worktrees", "feature"))
	if err != nil {
		t.Fatalf("prepareRunEnvironment(worktree): %v", err)
	}
	if want := filepath.Join(dir, ".worktrees", "feature"); wt.RootDir != want {
		t.Errorf("worktree RootDir = %q, want %q", wt.RootDir, want)
	}
}
