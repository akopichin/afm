package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGenerate_StageStatusTsInSync is the Go-side counterpart of `make generate-check` (Makefile): it fails
// `go test ./...` directly if someone adds/removes a state.StageStatus const and forgets to run
// `make generate` before committing — catching the drift even for a contributor who never runs `make lint-ci`
// locally. repoRoot resolves OutPath (repo-root-relative) from this test's own package directory
// (tools/genstagestatus), since `go test` runs with the package dir as cwd, not the repo root.
func TestGenerate_StageStatusTsInSync(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	want := Generate()
	got, err := os.ReadFile(filepath.Join(repoRoot, OutPath))
	if err != nil {
		t.Fatalf("read %s: %v", OutPath, err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s is stale — run `make generate` and commit the result.\n--- committed ---\n%s\n--- regenerated ---\n%s", OutPath, got, want)
	}
}
