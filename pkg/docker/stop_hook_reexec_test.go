package docker_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/docker"
)

// The container's afm builds its own Stop hook bridge and descriptor, so nothing of the hook is carried through
// `docker run`: an ambient hook variable on the host is not forwarded and no hook secret is in the argv.
func TestReExec_DoesNotForwardTheStopHookTransport(t *testing.T) {
	var gotArgv []string
	docker.SetExecFunc(func(_ string, argv []string, _ []string) error {
		gotArgv = argv
		return nil
	})
	defer docker.ResetExecFunc()

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("AFM_STOP_HOOK_DESCRIPTOR", "/host/tmp/descriptor.json")
	t.Setenv("AFM_STOP_HOOK_CODEX_STATE", "{trusted}")

	if err := docker.ReExec(docker.ReExecConfig{Image: "img", ProjectDir: "/project with space"}); err != nil {
		t.Fatalf("ReExec: %v", err)
	}
	if joined := strings.Join(gotArgv, "\n"); strings.Contains(joined, "AFM_STOP_HOOK") || strings.Contains(joined, "descriptor.json") {
		t.Fatalf("docker argv carries the Stop hook transport:\n%s", joined)
	}
}
