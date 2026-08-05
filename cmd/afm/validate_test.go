package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFlow(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "flow.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write flow.yaml: %v", err)
	}
	return path
}

func TestValidateFlowFile_ValidFlowReturnsOK(t *testing.T) {
	path := writeTestFlow(t, `name: test-flow
description: "a test flow"
stages:
  - id: implementation
    name: "Implementation"
    description: "do the thing"
    agents: [planning, implementation, review]
`)
	msg, err := validateFlowFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(msg, "test-flow") || !strings.Contains(msg, "1 stage") {
		t.Errorf("unexpected message: %q", msg)
	}
}

func TestValidateFlowFile_CycleReturnsError(t *testing.T) {
	path := writeTestFlow(t, `name: cyclic-flow
stages:
  - id: a
    description: "a"
    agents: [planning, implementation]
    depends_on: [b]
  - id: b
    description: "b"
    agents: [planning, implementation]
    depends_on: [a]
`)
	_, err := validateFlowFile(path)
	if err == nil {
		t.Fatal("expected error for cyclic depends_on")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected cycle error, got: %v", err)
	}
}

func TestValidateFlowFile_UnknownDependsOnReturnsError(t *testing.T) {
	path := writeTestFlow(t, `name: bad-flow
stages:
  - id: a
    description: "a"
    agents: [planning, implementation]
    depends_on: [nonexistent]
`)
	_, err := validateFlowFile(path)
	if err == nil {
		t.Fatal("expected error for unknown depends_on")
	}
	if !strings.Contains(err.Error(), "unknown stage") {
		t.Errorf("expected unknown-stage error, got: %v", err)
	}
}
