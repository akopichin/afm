package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseGitfile covers the pure gitfile parser on every platform (RED/GREEN
// on darwin): a valid `gitdir:` line, surrounding whitespace, and the absence
// of any gitdir line.
func TestParseGitfile(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{"simple", "gitdir: /a/b/.git/modules/x\n", "/a/b/.git/modules/x", true},
		{"leading and trailing space", "  gitdir:   ../.git/worktrees/w  \n", "../.git/worktrees/w", true},
		{"relative", "gitdir: sub/dir", "sub/dir", true},
		{"no gitdir line", "ref: refs/heads/main\n", "", false},
		{"empty target", "gitdir:   \n", "", false},
		{"empty file", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseGitfile(tc.in)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("parseGitfile(%q) = (%q,%v), want (%q,%v)", tc.in, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestGitDirFromEntry_Containment covers the containment gate with real temp
// dirs (runs on darwin): a directory `.git`, a gitfile pointing inside the
// root, a gitfile pointing outside the root (must be refused), a relative
// gitfile resolved against absDir, and a non-regular/non-dir entry.
func TestGitDirFromEntry_Containment(t *testing.T) {
	root := t.TempDir()
	absDir := filepath.Join(root, "sub")
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// (a) directory .git → <absDir>/.git, always contained.
	if gd, ok := gitDirFromEntry(root, absDir, true, false, nil); !ok || gd != filepath.Join(absDir, ".git") {
		t.Errorf("dir .git = (%q,%v), want (%q,true)", gd, ok, filepath.Join(absDir, ".git"))
	}

	// (b) gitfile pointing INSIDE the root (absolute) → accepted.
	inside := filepath.Join(root, ".git", "modules", "x")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	realInside, _ := filepath.EvalSymlinks(inside)
	if gd, ok := gitDirFromEntry(root, absDir, false, true, []byte("gitdir: "+inside+"\n")); !ok || gd != realInside {
		t.Errorf("inside gitfile = (%q,%v), want (%q,true)", gd, ok, realInside)
	}

	// (c) gitfile pointing OUTSIDE the root → refused (the security case).
	outside := t.TempDir() // a sibling temp dir, definitely not under root
	if gd, ok := gitDirFromEntry(root, absDir, false, true, []byte("gitdir: "+outside+"\n")); ok {
		t.Errorf("outside gitfile must be refused, got (%q,%v)", gd, ok)
	}

	// (d) relative gitfile resolved against absDir, staying inside root.
	rel := filepath.Join(absDir, "gd")
	if err := os.MkdirAll(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	realRel, _ := filepath.EvalSymlinks(rel)
	if gd, ok := gitDirFromEntry(root, absDir, false, true, []byte("gitdir: gd\n")); !ok || gd != realRel {
		t.Errorf("relative gitfile = (%q,%v), want (%q,true)", gd, ok, realRel)
	}

	// (e) neither dir nor regular → refused.
	if gd, ok := gitDirFromEntry(root, absDir, false, false, nil); ok {
		t.Errorf("non-dir non-regular must be refused, got (%q,%v)", gd, ok)
	}

	// (f) regular file without a gitdir line → refused.
	if gd, ok := gitDirFromEntry(root, absDir, false, true, []byte("garbage\n")); ok {
		t.Errorf("gitfile without gitdir line must be refused, got (%q,%v)", gd, ok)
	}
}

// TestRepoRelPath covers the work-tree re-basing on every platform.
func TestRepoRelPath(t *testing.T) {
	cases := []struct{ relDir, clean, want string }{
		{".", "a/b.go", "a/b.go"},
		{"a", "a/b.go", "b.go"},
		{"a/b", "a/b/c/d.go", "c/d.go"},
	}
	for _, tc := range cases {
		if got := repoRelPath(tc.relDir, tc.clean); got != tc.want {
			t.Errorf("repoRelPath(%q,%q) = %q, want %q", tc.relDir, tc.clean, got, tc.want)
		}
	}
}
