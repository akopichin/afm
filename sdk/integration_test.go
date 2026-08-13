package afmsdk

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// resolveAfmBinary locates a pre-built afm binary for integration tests. It's
// a real, separately-built binary (not something `go test` builds itself,
// since cmd/afm embeds the dashboard's compiled assets via go:embed and
// building those requires npm/node — see the repo root Makefile's `web`
// target). Set AFM_SDK_TEST_BINARY to override, or run `make build` at the
// repo root first (produces ../bin/afm relative to this module).
func resolveAfmBinary(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("AFM_SDK_TEST_BINARY"); bin != "" {
		if _, err := os.Stat(bin); err != nil {
			t.Fatalf("AFM_SDK_TEST_BINARY=%q: %v", bin, err)
		}
		return bin
	}
	bin := filepath.Join("..", "bin", "afm")
	if abs, err := filepath.Abs(bin); err == nil {
		bin = abs
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("no afm binary at %s (run `make build` at the repo root, or set AFM_SDK_TEST_BINARY): %v", bin, err)
	}
	return bin
}

func writeFlow(t *testing.T, dir, yaml string) string {
	t.Helper()
	path := filepath.Join(dir, "flow.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatalf("write flow.yaml: %v", err)
	}
	return path
}

// waitForStageStatus polls run.Status until stageID reports want, or fails
// the test after timeout.
func waitForStageStatus(ctx context.Context, t *testing.T, run *Run, stageID string, want StageStatus, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		status, err := run.Status(ctx)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status.Stages[stageID] == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stage %q did not reach %q in time, last status: %+v", stageID, want, status.Stages)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestIntegration_HappyPath(t *testing.T) {
	bin := resolveAfmBinary(t)
	c, err := New(Config{Binary: bin, BaseDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	workDir := t.TempDir()
	flowPath := writeFlow(t, workDir, `name: sdk-happy
stages:
  - id: notify
    name: Notify
    script: "echo done-output"
`)

	// Isolate the afm subprocess from the developer's real global
	// ~/.afm/config.yaml: afm resolves it via os.UserHomeDir() (== $HOME) and
	// merges it before parsing the flow, regardless of what the flow's stages
	// actually need. On a machine with docker.enabled: true set there (e.g.
	// for unrelated Docker-mode development on this same repo), this
	// script-only flow would otherwise get redirected into a Docker re-exec
	// that fails validating claude auth for an agent it never uses. Pointing
	// HOME at an empty temp dir keeps this test hermetic.
	t.Setenv("HOME", t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	run, err := c.Start(ctx, flowPath, workDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitForStageStatus(ctx, t, run, "notify", StageDone, 30*time.Second)

	deadline := time.Now().Add(30 * time.Second)
	for {
		status, err := run.Status(ctx)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status.Done {
			break
		}
		if status.Failed {
			t.Fatalf("run failed unexpectedly: %+v", status.Stages)
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not finish in time: %+v", status.Stages)
		}
		time.Sleep(200 * time.Millisecond)
	}

	if err := run.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if err := run.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(run.Dir()); !os.IsNotExist(err) {
		t.Errorf("run dir %q still exists after Cleanup", run.Dir())
	}
}
