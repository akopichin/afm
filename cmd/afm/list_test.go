package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestListShowsFlows seeds a couple of flow files under .afm/flows/ and
// verifies `afm list` enumerates them (both .yaml and .yml extensions),
// while ignoring non-flow files.
func TestListShowsFlows(t *testing.T) {
	chdirTemp(t)

	dir := filepath.Join(".afm", "flows")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"foo.yaml", "bar.yml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("name: x\nstages: []\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// non-flow file must be ignored
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a flow"), 0644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		cmd := newListCmd()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		if err := cmd.Execute(); err != nil {
			t.Fatalf("list: %v", err)
		}
	})

	if !strings.Contains(out, "foo.yaml") {
		t.Errorf("expected output to list foo.yaml, got:\n%s", out)
	}
	if !strings.Contains(out, "bar.yml") {
		t.Errorf("expected output to list bar.yml, got:\n%s", out)
	}
	if strings.Contains(out, "notes.txt") {
		t.Errorf("output should not list non-yaml file notes.txt, got:\n%s", out)
	}
}

// TestListNoFlowsFound verifies the friendly message when .afm/flows/ is
// empty or missing (no error, just a hint to run `afm init`).
func TestListNoFlowsFound(t *testing.T) {
	chdirTemp(t)

	out := captureStdout(t, func() {
		cmd := newListCmd()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		if err := cmd.Execute(); err != nil {
			t.Fatalf("list: %v", err)
		}
	})

	if !strings.Contains(out, "No flows found") {
		t.Errorf("expected 'No flows found' message, got:\n%s", out)
	}
}
