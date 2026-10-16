package docker_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/flow"
)

// TestPrepare_DockerMountsAndCredentialsReady is T8.5's Docker-side proof: a command that exists ONLY as an
// assistant (helper-agent) profile — never as a flow stage's command — still gets a Docker mount prepared,
// via ScanCommandsWithExtra's extra parameter. Before T8.5, ScanCommands only ever scanned f.Stages, so a
// collaboration profile's command silently never got mounted even with Docker enabled.
func TestPrepare_DockerMountsAndCredentialsReady(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"frontend-reviewer", "glm-build"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	f := &flow.Flow{
		Name:   "test",
		Stages: []flow.Stage{{ID: "build", Command: "glm-build"}},
	}

	// Only the flow-stage command is "ready" without T8.5's extra parameter — the profile command is absent.
	before := docker.ScanCommands(f, "claude", nil)
	if len(before) != 1 || before[0].ContainerName != "glm-build" {
		t.Fatalf("ScanCommands baseline = %+v, want exactly the flow-stage command", before)
	}

	// With the assistant profile's command passed as extra, BOTH are prepared before any dynamic launch
	// attempt — this is what "pre-prepared" means for T8.5.
	after := docker.ScanCommandsWithExtra(f, "claude", nil, []string{"frontend-reviewer"})
	names := make(map[string]bool, len(after))
	for _, m := range after {
		names[m.ContainerName] = true
	}
	if !names["glm-build"] {
		t.Error("flow-stage command missing from ScanCommandsWithExtra result")
	}
	if !names["frontend-reviewer"] {
		t.Error("assistant profile command missing from ScanCommandsWithExtra result — not pre-prepared for Docker")
	}
}

// TestScanCommandsWithExtra_DedupesAgainstStageCommands: a command appearing both as a flow stage's command
// AND in extra is mounted exactly once.
func TestScanCommandsWithExtra_DedupesAgainstStageCommands(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "shared-cmd")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	f := &flow.Flow{Name: "test", Stages: []flow.Stage{{ID: "s1", Command: "shared-cmd"}}}
	mounts := docker.ScanCommandsWithExtra(f, "claude", nil, []string{"shared-cmd"})
	if len(mounts) != 1 {
		t.Fatalf("expected exactly 1 mount (deduped), got %d: %+v", len(mounts), mounts)
	}
}
