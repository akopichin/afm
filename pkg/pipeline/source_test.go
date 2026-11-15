package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSource(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "pipelines", "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		arg  string
		want Source
	}{
		{"./pipelines/dev", Source{Kind: SourceLocal, Location: filepath.Join(cwd, "pipelines", "dev")}},
		{"pipelines/dev", Source{Kind: SourceLocal, Location: filepath.Join(cwd, "pipelines", "dev")}},
		{"/abs/pkg", Source{Kind: SourceLocal, Location: "/abs/pkg"}},
		{"git@host.example:o/r.git//p@v1", Source{Kind: SourceGit, Location: "git@host.example:o/r.git", Subdir: "p", Ref: "v1"}},
		{"git@host.example:o/r.git", Source{Kind: SourceGit, Location: "git@host.example:o/r.git"}},
		{"github.com/akopichin/afm-pipelines//pipelines/dev-pipeline@abc123", Source{Kind: SourceGit, Location: "https://github.com/akopichin/afm-pipelines", Subdir: "pipelines/dev-pipeline", Ref: "abc123"}},
		{"https://example.com/o/r.git@main", Source{Kind: SourceGit, Location: "https://example.com/o/r.git", Ref: "main"}},
		{"file:///tmp/repo.git//pipelines/livepkg@v1", Source{Kind: SourceGit, Location: "file:///tmp/repo.git", Subdir: "pipelines/livepkg", Ref: "v1"}},
		{"ssh://git@example.com/o/r.git//a/b@feature/x", Source{Kind: SourceGit, Location: "ssh://git@example.com/o/r.git", Subdir: "a/b", Ref: "feature/x"}},
	}
	for _, tc := range cases {
		t.Run(tc.arg, func(t *testing.T) {
			got, err := ParseSource(tc.arg, cwd)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseSourceRelativeFromOtherCWD(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	s1, err := ParseSource("pkg", a)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := ParseSource(filepath.Join(a, "pkg"), b)
	if err != nil {
		t.Fatal(err)
	}
	if s1.Identity() != s2.Identity() || !filepath.IsAbs(s1.Location) {
		t.Fatalf("identities differ: %q vs %q", s1.Identity(), s2.Identity())
	}
}

func TestParseSourceRejectsCredentialsWithoutLeakingThem(t *testing.T) {
	for _, arg := range []string{"https://u:tok123@example.invalid/r.git//p@v1", "u:tok123@example.invalid/r.git//p@v1", "http://tok123@example.invalid/r", "ssh://git:tok123@example.invalid/r.git//pkg@main", "ssh://u:@example.invalid/r.git", "file://u:tok123@/tmp/r.git", "file://tok123@host/tmp/r.git"} {
		_, err := ParseSource(arg, t.TempDir())
		if err == nil || strings.Contains(err.Error(), "tok123") {
			t.Fatalf("%s: err = %v", arg, err)
		}
	}
}

func TestParseSourceRejectsUnsafe(t *testing.T) {
	for _, arg := range []string{"https://example.com/r.git//../x@v1", "ftp://example.com/r.git", "https://example.com/r.git@-oops"} {
		if _, err := ParseSource(arg, t.TempDir()); err == nil {
			t.Errorf("%s: expected an error", arg)
		}
	}
}

func TestSourceIdentityIgnoresRef(t *testing.T) {
	a, _ := ParseSource("github.com/o/r//p@v1", "/")
	b, _ := ParseSource("github.com/o/r//p@v2", "/")
	c, _ := ParseSource("github.com/o/r//q@v1", "/")
	if a.Identity() != b.Identity() || a.Identity() == c.Identity() {
		t.Fatalf("identity: %q %q %q", a.Identity(), b.Identity(), c.Identity())
	}
}
