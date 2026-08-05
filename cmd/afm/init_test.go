package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureGitignoreEntry(t *testing.T) {
	dir := t.TempDir()
	if err := ensureGitignoreEntry(dir, ".afm/secrets.env"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.Contains(string(data), ".afm/secrets.env") {
		t.Errorf("entry not written: %s", data)
	}
	// idempotent
	if err := ensureGitignoreEntry(dir, ".afm/secrets.env"); err != nil {
		t.Fatal(err)
	}
	data2, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if got := strings.Count(string(data2), ".afm/secrets.env"); got != 1 {
		t.Errorf("expected 1 entry, got %d: %s", got, data2)
	}
}

func TestRunInitWizard_SingleChangeArchetype(t *testing.T) {
	lines := []string{
		"my-feature",   // flow name
		"does a thing", // flow description
		"",             // archetype -> default (single change)
		"",             // stage id -> default "implementation"
		"",             // stage name -> default
		"",             // mode -> default standard
		"ship it",      // description
		"",             // plan mode -> default agent
		"",             // phases -> default implementation
		"n",            // advanced -> no
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var out bytes.Buffer
	f := runInitWizard(scanner, &out)

	if f.Name != "my-feature" || f.Description != "does a thing" {
		t.Errorf("got name=%q description=%q", f.Name, f.Description)
	}
	if len(f.Stages) != 1 || f.Stages[0].ID != "implementation" {
		t.Fatalf("got stages: %+v", f.Stages)
	}
	parseGeneratedFlow(t, f)
}
