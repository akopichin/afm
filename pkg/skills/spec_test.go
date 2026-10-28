package skills_test

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/akopichin/afm/pkg/skills"
)

func parseNode(t *testing.T, doc string) *yaml.Node {
	t.Helper()
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(doc), &root); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	return root.Content[0]
}

func TestParseNames(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		want    skills.Names
		wantErr string
	}{
		{"scalar", "tdd", skills.Names{"tdd"}, ""},
		{"quoted number is a string", `"42"`, skills.Names{"42"}, ""},
		{"list keeps order", "[b, a, c]", skills.Names{"b", "a", "c"}, ""},
		{"exact repeats dropped", "[a, b, a]", skills.Names{"a", "b"}, ""},
		{"case differs", "[Tdd, tdd]", skills.Names{"Tdd", "tdd"}, ""},
		{"namespace kept", "superpowers:test-driven-development", skills.Names{"superpowers:test-driven-development"}, ""},
		{"empty list", "[]", skills.Names{}, ""},
		{"empty name", `""`, nil, "must not be empty"},
		{"blank name in list", `[a, "  "]`, nil, "must not be empty"},
		{"null scalar", "null", nil, "each skill name must be a string"},
		{"null in list", "[a, null]", nil, "each skill name must be a string"},
		{"number", "5", nil, "each skill name must be a string"},
		{"bool", "true", nil, "each skill name must be a string"},
		{"mixed list", "[a, 1]", nil, "each skill name must be a string"},
		{"nested list", "[[a]]", nil, "each skill name must be a string"},
		{"object", "{a: b}", nil, "skill name or a list"},
		{"list of objects", "[{a: b}]", nil, "each skill name must be a string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := skills.ParseNames(parseNode(t, tt.doc))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseNames: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseNames_Alias(t *testing.T) {
	var doc struct {
		A skills.Names `yaml:"a"`
		B skills.Names `yaml:"b"`
	}
	if err := yaml.Unmarshal([]byte("a: &x [one, two]\nb: *x\n"), &doc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(doc.B, skills.Names{"one", "two"}) {
		t.Errorf("alias: got %#v", doc.B)
	}
}

func TestParseDir(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		want    skills.Dir
		wantErr string
	}{
		{"relative", "./skills", "./skills", ""},
		{"absolute", "/opt/skills", "/opt/skills", ""},
		{"empty", `""`, "", "must not be empty"},
		{"blank", `"  "`, "", "must not be empty"},
		{"null", "null", "", "must be a string"},
		{"number", "5", "", "must be a string"},
		{"list", "[a]", "", "must be a string"},
		{"tilde", "~/skills", "", "not expanded"},
		{"bare tilde", `"~"`, "", "not expanded"},
		{"tilde user", "~bob/skills", "", "not expanded"},
		{"tilde inside is fine", "a/~b", "a/~b", ""},
		{"newline", `"a\nb"`, "", "control characters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := skills.ParseDir(parseNode(t, tt.doc))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestDirResolve(t *testing.T) {
	tests := []struct {
		dir  skills.Dir
		want string
	}{
		{"", ""},
		{"./skills", "/root/skills"},
		{"skills/../lib", "/root/lib"},
		{"/abs/lib/", "/abs/lib"},
	}
	for _, tt := range tests {
		if got := tt.dir.Resolve("/root"); got != tt.want {
			t.Errorf("Resolve(%q) = %q, want %q", tt.dir, got, tt.want)
		}
	}
}

func TestFirstDirNearestWins(t *testing.T) {
	if got := skills.FirstDir("", "flow", "config"); got != "flow" {
		t.Errorf("got %q, want flow", got)
	}
	if got := skills.FirstDir("stage", "flow", "config"); got != "stage" {
		t.Errorf("got %q, want stage", got)
	}
	if got := skills.FirstDir("", "", ""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestRejectNullKeys(t *testing.T) {
	for _, doc := range []string{"skills_dir:\n", "skills_dir: null\n", "skills_dir: ~\n"} {
		if err := skills.RejectNullKeys(parseNode(t, doc), "skills_dir"); err == nil {
			t.Errorf("%q: want error", doc)
		}
	}
	for _, doc := range []string{"other:\n", "skills_dir: x\n", "x: 1\n"} {
		if err := skills.RejectNullKeys(parseNode(t, doc), "skills_dir"); err != nil {
			t.Errorf("%q: unexpected error %v", doc, err)
		}
	}
	if err := skills.RejectNullKeys(nil, "skills_dir"); err != nil {
		t.Errorf("nil node: %v", err)
	}
}
