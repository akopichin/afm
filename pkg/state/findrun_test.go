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
