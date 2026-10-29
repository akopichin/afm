package skills_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/fsutil"
	"github.com/akopichin/afm/pkg/skills"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newLibrary returns a canonical library root, so expected paths match LoadCatalog's.
func newLibrary(t *testing.T) string {
	t.Helper()
	root, err := fsutil.CanonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLoadCatalog_ReadsSortedEntries(t *testing.T) {
	root := newLibrary(t)
	writeFile(t, filepath.Join(root, "zeta", "SKILL.md"), "---\nname: zeta-skill\ndescription: Last one\n---\nbody\n")
	writeFile(t, filepath.Join(root, "alpha", "SKILL.md"), "no frontmatter at all\n")
	writeFile(t, filepath.Join(root, "alpha", "references", "notes.md"), "aux")
	writeFile(t, filepath.Join(root, "ns", "SKILL.md"), "---\nname: superpowers:tdd\ndescription: >\n  Folded\n  text\n---\n")
	writeFile(t, filepath.Join(root, "README.md"), "a file next to skill folders is ignored")
	writeFile(t, filepath.Join(root, ".git", "config"), "hidden directories are not skills")

	cat, err := skills.LoadCatalog(root)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	want := []skills.Entry{
		{Name: "alpha", Path: filepath.Join(root, "alpha", "SKILL.md")},
		{Name: "superpowers:tdd", Description: "Folded text", Path: filepath.Join(root, "ns", "SKILL.md")},
		{Name: "zeta-skill", Description: "Last one", Path: filepath.Join(root, "zeta", "SKILL.md")},
	}
	if cat.Root != root {
		t.Errorf("Root = %q, want %q", cat.Root, root)
	}
	if fmt.Sprint(cat.Entries) != fmt.Sprint(want) {
		t.Errorf("Entries = %+v, want %+v", cat.Entries, want)
	}
}

func TestLoadCatalog_Deterministic(t *testing.T) {
	root := newLibrary(t)
	for _, n := range []string{"c", "a", "b"} {
		writeFile(t, filepath.Join(root, n, "SKILL.md"), "x")
	}
	first, err := skills.LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := skills.LoadCatalog(root)
	if fmt.Sprint(first) != fmt.Sprint(second) {
		t.Errorf("catalog differs between loads:\n%v\n%v", first, second)
	}
}

func TestLoadCatalog_Errors(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, root string)
		wantErr string
	}{
		{"directory without SKILL.md", func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "empty", "notes.md"), "x")
		}, "has no SKILL.md"},
		{"unclosed frontmatter", func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "a", "SKILL.md"), "---\nname: a\n")
		}, "closing --- not found"},
		{"invalid frontmatter yaml", func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "a", "SKILL.md"), "---\nname: [unclosed\n---\n")
		}, "invalid frontmatter"},
		{"frontmatter is a list", func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "a", "SKILL.md"), "---\n- a\n---\n")
		}, "invalid frontmatter"},
		{"name not a string", func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "a", "SKILL.md"), "---\nname: 5\n---\n")
		}, "name must be a string"},
		{"empty name", func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "a", "SKILL.md"), "---\nname: \"\"\n---\n")
		}, "non-empty single-line"},
		{"null name", func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "a", "SKILL.md"), "---\nname:\n---\n")
		}, "non-empty single-line"},
		{"multi-line name", func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "a", "SKILL.md"), "---\nname: \"a\\nb\"\n---\n")
		}, "non-empty single-line"},
		{"description not a string", func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "a", "SKILL.md"), "---\ndescription: [x]\n---\n")
		}, "description must be a string"},
		{"duplicate name from frontmatter and dir", func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "a", "SKILL.md"), "x")
			writeFile(t, filepath.Join(root, "b", "SKILL.md"), "---\nname: a\n---\n")
		}, `duplicate skill name "a"`},
		{"SKILL.md is a directory", func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, "a", "SKILL.md"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "not a regular file"},
		{"unreadable SKILL.md", func(t *testing.T, root string) {
			if os.Geteuid() == 0 {
				t.Skip("root reads everything")
			}
			p := filepath.Join(root, "a", "SKILL.md")
			writeFile(t, p, "x")
			if err := os.Chmod(p, 0); err != nil {
				t.Fatal(err)
			}
		}, "permission denied"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newLibrary(t)
			tt.setup(t, root)
			_, err := skills.LoadCatalog(root)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadCatalog_BadRoot(t *testing.T) {
	root := newLibrary(t)
	if _, err := skills.LoadCatalog(filepath.Join(root, "missing")); err == nil {
		t.Error("missing library: want error")
	}
	file := filepath.Join(root, "file")
	writeFile(t, file, "x")
	if _, err := skills.LoadCatalog(file); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("file as library: error = %v", err)
	}
}

func TestLoadCatalog_EmptyLibraryIsValid(t *testing.T) {
	cat, err := skills.LoadCatalog(newLibrary(t))
	if err != nil || len(cat.Entries) != 0 {
		t.Fatalf("got %+v, %v", cat, err)
	}
}

func TestLoadCatalog_Symlinks(t *testing.T) {
	outside := newLibrary(t)
	writeFile(t, filepath.Join(outside, "secret.md"), "secret")

	t.Run("internal symlinks are allowed", func(t *testing.T) {
		root := newLibrary(t)
		writeFile(t, filepath.Join(root, "a", "SKILL.md"), "x")
		writeFile(t, filepath.Join(root, "b", "SKILL.md"), "x")
		mustSymlink(t, filepath.Join(root, "b"), filepath.Join(root, "a", "references"))
		mustSymlink(t, filepath.Join(root, "b", "SKILL.md"), filepath.Join(root, "a", "scripts.md"))
		cat, err := skills.LoadCatalog(root)
		if err != nil || len(cat.Entries) != 2 {
			t.Fatalf("got %+v, %v", cat, err)
		}
	})

	t.Run("internal symlink to a hidden helper dir", func(t *testing.T) {
		root := newLibrary(t)
		writeFile(t, filepath.Join(root, "a", "SKILL.md"), "x")
		writeFile(t, filepath.Join(root, ".shared", "common.md"), "shared")
		mustSymlink(t, filepath.Join(root, ".shared"), filepath.Join(root, "a", "references"))
		cat, err := skills.LoadCatalog(root)
		if err != nil || len(cat.Entries) != 1 {
			t.Fatalf("got %+v, %v", cat, err)
		}
	})

	t.Run("symlinked skill directory keeps the lexical path", func(t *testing.T) {
		root := newLibrary(t)
		writeFile(t, filepath.Join(root, ".src", "real", "SKILL.md"), "x")
		mustSymlink(t, filepath.Join(root, ".src", "real"), filepath.Join(root, "linked"))
		cat, err := skills.LoadCatalog(root)
		if err != nil || len(cat.Entries) != 1 {
			t.Fatalf("got %+v, %v", cat, err)
		}
		if want := filepath.Join(root, "linked", "SKILL.md"); cat.Entries[0].Path != want {
			t.Errorf("Path = %q, want %q", cat.Entries[0].Path, want)
		}
	})

	t.Run("a link chain through a hidden dir cannot reach outside", func(t *testing.T) {
		root := newLibrary(t)
		writeFile(t, filepath.Join(root, "a", "SKILL.md"), "x")
		mustSymlink(t, filepath.Join(root, ".hidden"), filepath.Join(root, "a", "references"))
		if err := os.MkdirAll(filepath.Join(root, ".hidden"), 0o755); err != nil {
			t.Fatal(err)
		}
		mustSymlink(t, filepath.Join(outside, "secret.md"), filepath.Join(root, ".hidden", "secret"))
		if _, err := skills.LoadCatalog(root); err == nil {
			t.Fatal("a link reachable only through another link must be checked too")
		}
	})

	t.Run("a link cycle inside the library is not followed forever", func(t *testing.T) {
		root := newLibrary(t)
		writeFile(t, filepath.Join(root, "a", "SKILL.md"), "x")
		mustSymlink(t, filepath.Join(root, "a"), filepath.Join(root, "a", "self"))
		cat, err := skills.LoadCatalog(root)
		if err != nil || len(cat.Entries) != 1 {
			t.Fatalf("got %+v, %v", cat, err)
		}
	})

	escapes := []struct {
		name string
		link func(root string) (target, link string)
	}{
		{"SKILL.md", func(root string) (string, string) {
			return filepath.Join(outside, "secret.md"), filepath.Join(root, "a", "SKILL.md")
		}},
		{"scripts directory", func(root string) (string, string) {
			return outside, filepath.Join(root, "a", "scripts")
		}},
		{"nested reference file", func(root string) (string, string) {
			return filepath.Join(outside, "secret.md"), filepath.Join(root, "a", "references", "deep", "link.md")
		}},
		{"skill directory", func(root string) (string, string) {
			writeFile(t, filepath.Join(outside, "ext", "SKILL.md"), "x")
			return filepath.Join(outside, "ext"), filepath.Join(root, "ext")
		}},
		{"broken link", func(root string) (string, string) {
			return filepath.Join(root, "nowhere"), filepath.Join(root, "a", "scripts")
		}},
	}
	for _, tt := range escapes {
		t.Run("rejects escape via "+tt.name, func(t *testing.T) {
			root := newLibrary(t)
			writeFile(t, filepath.Join(root, "a", "SKILL.md"), "x")
			writeFile(t, filepath.Join(root, "a", "references", "deep", "ok.md"), "x")
			target, link := tt.link(root)
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			_ = os.Remove(link)
			mustSymlink(t, target, link)
			if _, err := skills.LoadCatalog(root); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestLoadCatalog_EntryLimit(t *testing.T) {
	root := newLibrary(t)
	for i := range skills.MaxCatalogEntries + 1 {
		writeFile(t, filepath.Join(root, fmt.Sprintf("s%03d", i), "SKILL.md"), "x")
	}
	if _, err := skills.LoadCatalog(root); err == nil || !strings.Contains(err.Error(), "exceed the limit") {
		t.Fatalf("error = %v", err)
	}
}

func TestCatalogBlock(t *testing.T) {
	cat := skills.Catalog{Root: "/lib", Entries: []skills.Entry{
		{Name: "a", Description: "does a", Path: "/lib/a/SKILL.md"},
		{Name: "b", Path: "/lib/b/SKILL.md"},
	}}
	got, err := cat.Block()
	if err != nil {
		t.Fatal(err)
	}
	want := "- a: does a (/lib/a/SKILL.md)\n- b:  (/lib/b/SKILL.md)\n"
	if got != want {
		t.Errorf("Block() = %q, want %q", got, want)
	}

	big := skills.Catalog{Root: "/lib", Entries: []skills.Entry{{Name: "x", Description: strings.Repeat("é", skills.MaxCatalogBytes), Path: "/p"}}}
	if _, err := big.Block(); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("oversized block: error = %v", err)
	}
}

func TestLoadCatalog_RejectsControlCharactersInASkillName(t *testing.T) {
	root := newLibrary(t)
	writeFile(t, filepath.Join(root, "a\nb", "SKILL.md"), "no frontmatter, the directory name is the skill name")
	if _, err := skills.LoadCatalog(root); err == nil || !strings.Contains(err.Error(), "control") {
		t.Fatalf("error = %v, want one about control characters", err)
	}
}

func TestLoadCatalog_RejectsRootLevelSymlinkOutside(t *testing.T) {
	outside := newLibrary(t)
	writeFile(t, filepath.Join(outside, "secret.md"), "secret")
	for _, name := range []string{"notes.md", ".hidden.md"} {
		t.Run(name, func(t *testing.T) {
			root := newLibrary(t)
			writeFile(t, filepath.Join(root, "a", "SKILL.md"), "x")
			mustSymlink(t, filepath.Join(outside, "secret.md"), filepath.Join(root, name))
			if _, err := skills.LoadCatalog(root); err == nil || !strings.Contains(err.Error(), "outside the library") {
				t.Fatalf("error = %v, want one about a symlink outside the library", err)
			}
		})
	}
}

func TestLoadCatalog_AllowsRootLevelSymlinkInside(t *testing.T) {
	root := newLibrary(t)
	writeFile(t, filepath.Join(root, "a", "SKILL.md"), "x")
	mustSymlink(t, filepath.Join(root, "a", "SKILL.md"), filepath.Join(root, "README.md"))
	if cat, err := skills.LoadCatalog(root); err != nil || len(cat.Entries) != 1 {
		t.Fatalf("got %+v, %v", cat, err)
	}
}

func TestLoadCatalog_RejectsControlCharactersInPaths(t *testing.T) {
	t.Run("entry directory with a frontmatter name", func(t *testing.T) {
		root := newLibrary(t)
		writeFile(t, filepath.Join(root, "a\nb", "SKILL.md"), "---\nname: ok\n---\n")
		if _, err := skills.LoadCatalog(root); err == nil || !strings.Contains(err.Error(), "control") {
			t.Fatalf("error = %v, want one about control characters", err)
		}
	})
	t.Run("library root", func(t *testing.T) {
		root := filepath.Join(newLibrary(t), "lib\nroot")
		writeFile(t, filepath.Join(root, "a", "SKILL.md"), "x")
		if _, err := skills.LoadCatalog(root); err == nil || !strings.Contains(err.Error(), "control") {
			t.Fatalf("error = %v, want one about control characters", err)
		}
	})
	t.Run("a plain file with a newline is not rendered and stays harmless", func(t *testing.T) {
		root := newLibrary(t)
		writeFile(t, filepath.Join(root, "a", "SKILL.md"), "x")
		writeFile(t, filepath.Join(root, "notes\n.md"), "x")
		if cat, err := skills.LoadCatalog(root); err != nil || len(cat.Entries) != 1 {
			t.Fatalf("got %+v, %v", cat, err)
		}
	})
}

func TestCatalogBlock_KeepsEachSkillOnOneLine(t *testing.T) {
	cat := skills.Catalog{Root: "/lib", Entries: []skills.Entry{
		{Name: "a\nb", Description: "x\r\n- injected: line", Path: "/lib/a\nb/SKILL.md"},
	}}
	got, err := cat.Block()
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(got, "\n"); n != 1 || !strings.HasSuffix(got, "\n") {
		t.Errorf("Block() = %q, want exactly one line", got)
	}
}
