package pipeline

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func openPackageRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root
}

// Once skills is open, replacing its name with a link cannot redirect the rest of the path: it is resolved from the
// opened directory.
func TestOpenNoFollowReadsThroughAlreadyOpenedAncestor(t *testing.T) {
	dir, _ := newPackage(t)
	writeFile(t, dir, "private/a/SKILL.md", "undeclared-secret")
	root := openPackageRoot(t, dir)
	linked := false
	onOpenComponent(t, func(rel, prefix string) {
		if prefix == "skills/a" && !linked {
			linkSkills(t, dir, "private")
			linked = true
		}
	})
	f, err := openNoFollow(root, "skills/a/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := io.ReadAll(f)
	if !linked || err != nil || string(got) != "skill" {
		t.Fatalf("linked = %t, read %q (%v), want the declared bytes", linked, got, err)
	}
}

// Followed literally, "." and ".." would reach existing files, one of them outside the root.
func TestOpenNoFollowRejectsInvalidPaths(t *testing.T) {
	parent := t.TempDir()
	writeFile(t, parent, "outside.txt", "outside")
	writeFile(t, parent, "pkg/flow.yaml", "flow")
	writeFile(t, parent, "pkg/sub/x", "x")
	root := openPackageRoot(t, filepath.Join(parent, "pkg"))
	for _, rel := range []string{"", "../outside.txt", "./flow.yaml", "sub/../flow.yaml", "/flow.yaml"} {
		if f, err := openNoFollow(root, rel); err == nil {
			f.Close()
			t.Errorf("openNoFollow(%q) succeeded", rel)
		}
	}
}
