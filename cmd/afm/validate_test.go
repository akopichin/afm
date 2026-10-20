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

// writeValidateConfig points afm validate at an empty HOME and a project .afm/config.yaml with body.
func writeValidateConfig(t *testing.T, body string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AFM_COLLABORATION", "")
	root := t.TempDir()
	prev := rootDir
	rootDir = root
	t.Cleanup(func() { rootDir = prev })
	if err := os.MkdirAll(filepath.Join(root, ".afm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".afm", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const validateTeamFlow = `name: team-flow
team:
  - id: arch
    profile: rev-arch
    title: Architecture
    instruction: Review when asked.
stages:
  - id: s
    agents: [auto]
`

func TestValidateFlowFile_TeamWithKnownProfile(t *testing.T) {
	writeValidateConfig(t, "collaboration:\n  profiles:\n    rev-arch:\n      command: codex-as-claude\n")
	if _, err := validateFlowFile(writeTestFlow(t, validateTeamFlow)); err != nil {
		t.Fatalf("validateFlowFile: %v", err)
	}
}

func TestValidateFlowFile_TeamUnknownProfile(t *testing.T) {
	writeValidateConfig(t, "collaboration:\n  profiles:\n    qa:\n      command: codex-as-claude\n")
	_, err := validateFlowFile(writeTestFlow(t, validateTeamFlow))
	if err == nil || !strings.Contains(err.Error(), `team member "arch"`) || !strings.Contains(err.Error(), "rev-arch") {
		t.Fatalf("validateFlowFile = %v, want an unknown-profile error for arch", err)
	}
}

func TestValidateFlowFile_TeamDuplicateID(t *testing.T) {
	writeValidateConfig(t, "")
	path := writeTestFlow(t, `name: team-flow
team:
  - id: qa
    profile: qa
    title: QA
  - id: qa
    profile: qa
    title: QA two
stages:
  - id: s
    agents: [auto]
`)
	_, err := validateFlowFile(path)
	if err == nil || !strings.Contains(err.Error(), `duplicate team member id "qa"`) {
		t.Fatalf("validateFlowFile = %v, want a duplicate id error", err)
	}
}

func TestValidateFlowFile_TeamWithDisabledCollaborationWarns(t *testing.T) {
	writeValidateConfig(t, "collaboration:\n  enabled: false\n")
	var err error
	stderr := captureStderr(t, func() { _, err = validateFlowFile(writeTestFlow(t, validateTeamFlow)) })
	if err != nil {
		t.Fatalf("validateFlowFile: %v, want OK with a warning", err)
	}
	if !strings.Contains(stderr, "collaboration is disabled") {
		t.Fatalf("stderr = %q, want the disabled-collaboration warning", stderr)
	}
}

// validate builds the same profile registry as afm run: a malformed profile that would turn collaboration
// off at run time fails validation even when the team's own profile is fine.
func TestValidateFlowFile_TeamWithMalformedProfileRegistry(t *testing.T) {
	writeValidateConfig(t, "collaboration:\n  profiles:\n    rev-arch:\n      command: codex-as-claude\n    \"bad/id\":\n      command: codex-as-claude\n")
	_, err := validateFlowFile(writeTestFlow(t, validateTeamFlow))
	if err == nil || !strings.Contains(err.Error(), "bad/id") {
		t.Fatalf("validateFlowFile = %v, want the registry error for bad/id", err)
	}
}
