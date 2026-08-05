package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
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

func TestGenerateAndValidateFlow_ValidFlowWritesAndValidates(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "flow.yaml")
	f := &flow.Flow{
		Name:        "test-flow",
		Description: "desc",
		Stages: []flow.Stage{
			{ID: "implementation", Name: "Implementation", Description: "do it",
				Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
		},
	}
	if _, err := generateAndValidateFlow(f, outPath); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(outPath); statErr != nil {
		t.Errorf("file not written: %v", statErr)
	}
}

func TestGenerateAndValidateFlow_InvalidDependsOnReturnsErrorButStillWritesFile(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "flow.yaml")
	f := &flow.Flow{
		Name: "test-flow",
		Stages: []flow.Stage{
			{ID: "a", Description: "d", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
				DependsOn: []string{"nonexistent"}},
		},
	}
	_, err := generateAndValidateFlow(f, outPath)
	if err == nil {
		t.Fatal("expected error for bad depends_on")
	}
	if !strings.Contains(err.Error(), "unknown stage") {
		t.Errorf("error = %v", err)
	}
	if _, statErr := os.Stat(outPath); statErr != nil {
		t.Errorf("file should still be written even when invalid, for manual repair: %v", statErr)
	}
}
