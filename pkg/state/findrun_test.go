package state

import (
	"os"
	"path/filepath"
	"testing"
)

// Префикс "foo-" не должен матчить run флоу "foo-bar".
func TestFindLatestRunDir_AnchorsPrefix(t *testing.T) {
	base := t.TempDir()
	// runs: foo-bar-20240101-000000 и foo-20240102-000000
	os.MkdirAll(filepath.Join(base, "foo-bar-20240101-000000"), 0755)
	os.MkdirAll(filepath.Join(base, "foo-20240102-000000"), 0755)

	dir, err := FindLatestRunDir(base, "foo")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dir) != "foo-20240102-000000" {
		t.Fatalf("want foo-20240102-000000, got %s", filepath.Base(dir))
	}
}

// A numeric-suffixed sibling name ("sprint-2") must not be mistaken for a run of "sprint": the character
// right after "sprint-" is a digit ('2'), so a bare "does the next byte look like a digit" check passes,
// even though the rest of the name ("2-20260101-100000-aaaa") is not a timestamp at all. --worktree makes
// this realistic — users commonly pick sibling branch names like "sprint"/"sprint-2".
func TestFindLatestRunDir_AnchorsPrefix_RejectsNumericSuffixSibling(t *testing.T) {
	base := t.TempDir()
	os.MkdirAll(filepath.Join(base, "sprint-2-20260101-100000-aaaa"), 0755)

	_, err := FindLatestRunDir(base, "sprint")
	if err == nil {
		t.Fatal("FindLatestRunDir(\"sprint\") matched a \"sprint-2\" run dir, want no match")
	}
}
