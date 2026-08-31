package memory

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCommit_AddsAndCommitsOnlyOnChange(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	memDir := filepath.Join(repo, "m")
	os.MkdirAll(memDir, 0755)
	if err := os.WriteFile(filepath.Join(memDir, "memory.md"), []byte("# Project rules\n"), 0644); err != nil {
		t.Fatal(err)
	}

	committed, err := Commit(memDir, "chore(memory): update project memory")
	if err != nil || !committed {
		t.Fatalf("first commit: committed=%v err=%v", committed, err)
	}
	// no change → committed=false
	committed, err = Commit(memDir, "chore(memory): update project memory")
	if err != nil || committed {
		t.Fatalf("second commit should be no-op: committed=%v err=%v", committed, err)
	}
}
