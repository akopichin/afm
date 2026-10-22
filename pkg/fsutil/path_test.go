package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalPath(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "real")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "gone"), filepath.Join(base, "dangling")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, in, want string
	}{
		{"existing path", target, target},
		{"symlink resolved", filepath.Join(base, "link"), target},
		{"missing tail kept", filepath.Join(base, "link", "a", "b.md"), filepath.Join(target, "a", "b.md")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CanonicalPath(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("CanonicalPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	t.Run("relative path becomes absolute", func(t *testing.T) {
		t.Chdir(target)
		got, err := CanonicalPath("x")
		if err != nil {
			t.Fatal(err)
		}
		if got != filepath.Join(target, "x") {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("dangling symlink is an error", func(t *testing.T) {
		if _, err := CanonicalPath(filepath.Join(base, "dangling", "f.md")); err == nil {
			t.Fatal("must fail")
		}
	})
}
