package afmsdk

import (
	"context"
	"fmt"
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
	c, err := New(Config{Binary: bin})
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

	// notify is the only stage in this flow, so reaching StageDone here
	// already proves the whole run is done (RunStatus.Done requires every
	// stage to be done) — no need for a separate poll loop on status.Done.
	waitForStageStatus(ctx, t, run, "notify", StageDone, 30*time.Second)

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

func TestIntegration_StartUsesWorkDirAsAfmDir(t *testing.T) {
	bin := resolveAfmBinary(t)
	c, err := New(Config{Binary: bin})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	workDir := t.TempDir()
	flowPath := writeFlow(t, workDir, `name: sdk-dir-unify
stages:
  - id: notify
    name: Notify
    script: "echo done-output"
`)
	t.Setenv("HOME", t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	run, err := c.Start(ctx, flowPath, workDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		_ = run.Wait(ctx)
		_ = run.Cleanup()
	}()

	if run.Dir() != workDir {
		t.Errorf("Dir(): got %q, want %q (the workDir passed to Start)", run.Dir(), workDir)
	}
	matches, err := filepath.Glob(filepath.Join(workDir, ".afm", "runs", "*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one afm run dir under %s/.afm/runs, got %d: %v", workDir, len(matches), matches)
	}
}

const fakePlanningAgentScript = `#!/bin/bash
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"## Tasks\n\n- [ ] Step 1: implement feature\n- [ ] Step 2: write tests\n\n## Assumptions\n\n- none\n\n## Acceptance Criteria\n\n- [ ] feature works\n"}]}}'
echo '{"type":"result","subtype":"success"}'
`

// writeFakePlanningAgent writes an executable stand-in for a real AI agent
// that always produces the same valid plan (Tasks/Assumptions/Acceptance
// Criteria sections, required by prompts.ValidatePlan) on its first and only
// invocation, so a stage with `agents: [planning]` deterministically reaches
// awaiting_approval.
func writeFakePlanningAgent(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-plan-agent.sh")
	if err := os.WriteFile(path, []byte(fakePlanningAgentScript), 0755); err != nil {
		t.Fatalf("write fake planning agent: %v", err)
	}
	return path
}

// waitForStageStatusChange polls until stageID's status differs from from,
// or fails the test after timeout.
func waitForStageStatusChange(ctx context.Context, t *testing.T, run *Run, stageID string, from StageStatus, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		status, err := run.Status(ctx)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status.Stages[stageID] != from {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stage %q did not move off %q in time", stageID, from)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestIntegration_Approve(t *testing.T) {
	bin := resolveAfmBinary(t)
	c, err := New(Config{Binary: bin})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	workDir := t.TempDir()
	agentPath := writeFakePlanningAgent(t, workDir)
	flowPath := writeFlow(t, workDir, fmt.Sprintf(`name: sdk-approve
stages:
  - id: plan-me
    name: Plan Me
    agents: [planning]
    command: %s
`, agentPath))

	// See TestIntegration_HappyPath for why this is needed: isolates the afm
	// subprocess from the developer's real global ~/.afm/config.yaml, which
	// may have docker.enabled: true and would otherwise redirect this run
	// into an unrelated Docker re-exec.
	t.Setenv("HOME", t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	run, err := c.Start(ctx, flowPath, workDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		_ = run.Wait(ctx)
		_ = run.Cleanup()
	}()

	waitForStageStatus(ctx, t, run, "plan-me", StageAwaitingApproval, 30*time.Second)

	if err := run.Approve(ctx, "plan-me"); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	waitForStageStatusChange(ctx, t, run, "plan-me", StageAwaitingApproval, 10*time.Second)
}

func TestIntegration_Retry(t *testing.T) {
	bin := resolveAfmBinary(t)
	c, err := New(Config{Binary: bin})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	workDir := t.TempDir()
	gateFile := filepath.Join(t.TempDir(), "gate")
	flowPath := writeFlow(t, workDir, fmt.Sprintf(`name: sdk-retry
stages:
  - id: notify
    name: Notify
    script: "test -f %s && echo done-output || exit 1"
`, gateFile))

	// See TestIntegration_HappyPath for why this is needed: isolates the afm
	// subprocess from the developer's real global ~/.afm/config.yaml, which
	// may have docker.enabled: true and would otherwise redirect this run
	// into an unrelated Docker re-exec.
	t.Setenv("HOME", t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	run, err := c.Start(ctx, flowPath, workDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		_ = run.Wait(ctx)
		_ = run.Cleanup()
	}()

	waitForStageStatus(ctx, t, run, "notify", StageFailed, 30*time.Second)

	if err := os.WriteFile(gateFile, nil, 0644); err != nil {
		t.Fatalf("write gate file: %v", err)
	}

	if err := run.Retry(ctx, "notify"); err != nil {
		t.Fatalf("Retry: %v", err)
	}

	waitForStageStatus(ctx, t, run, "notify", StageDone, 30*time.Second)
}

func TestIntegration_Revise(t *testing.T) {
	bin := resolveAfmBinary(t)
	c, err := New(Config{Binary: bin})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	workDir := t.TempDir()
	agentPath := writeFakePlanningAgent(t, workDir)
	flowPath := writeFlow(t, workDir, fmt.Sprintf(`name: sdk-revise
stages:
  - id: plan-me
    name: Plan Me
    agents: [planning]
    command: %s
`, agentPath))

	// See TestIntegration_HappyPath for why this is needed: isolates the afm
	// subprocess from the developer's real global ~/.afm/config.yaml, which
	// may have docker.enabled: true and would otherwise redirect this run
	// into an unrelated Docker re-exec.
	t.Setenv("HOME", t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	run, err := c.Start(ctx, flowPath, workDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		_ = run.Wait(ctx)
		_ = run.Cleanup()
	}()

	waitForStageStatus(ctx, t, run, "plan-me", StageAwaitingApproval, 30*time.Second)

	if err := run.Revise(ctx, "plan-me", "please add more tests"); err != nil {
		t.Fatalf("Revise: %v", err)
	}

	// StageRevising is real (confirmed in afm's durable event log) but too
	// transient to catch over HTTP: afm's re-plan goroutine races the HTTP
	// response for this very POST and reliably wins on real hardware — the
	// whole revising->planning->awaiting_approval cycle can resolve in
	// under 150ms, faster than a normal 200ms poll interval samples. Poll
	// tightly and briefly right after Revise() returns to catch ANY
	// departure from awaiting_approval — proving Revise() had a real
	// effect — without asserting which specific intermediate status was
	// observed (a vacuous no-op Revise() would never leave
	// awaiting_approval at all, so this still rules out the failure mode
	// that matters).
	{
		deadline := time.Now().Add(2 * time.Second)
		left := false
		for time.Now().Before(deadline) {
			status, err := run.Status(ctx)
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if status.Stages["plan-me"] != StageAwaitingApproval {
				left = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !left {
			t.Fatal("plan-me never left awaiting_approval after Revise()")
		}
	}

	// The fake agent produces the same valid plan every time, so afm
	// re-plans and lands back on awaiting_approval without human input.
	waitForStageStatus(ctx, t, run, "plan-me", StageAwaitingApproval, 30*time.Second)
}

func TestIntegration_MaxConcurrentBlocksExtraStarts(t *testing.T) {
	bin := resolveAfmBinary(t)
	c, err := New(Config{Binary: bin, MaxConcurrent: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	newSlowFlow := func(t *testing.T) (flowPath, workDir string) {
		workDir = t.TempDir()
		flowPath = writeFlow(t, workDir, `name: sdk-slow
stages:
  - id: notify
    name: Notify
    script: "sleep 2; echo done-output"
`)
		return
	}

	// See TestIntegration_HappyPath for why this is needed: isolates the afm
	// subprocess from the developer's real global ~/.afm/config.yaml, which
	// may have docker.enabled: true and would otherwise redirect this run
	// into an unrelated Docker re-exec.
	t.Setenv("HOME", t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	flow1, work1 := newSlowFlow(t)
	run1, err := c.Start(ctx, flow1, work1)
	if err != nil {
		t.Fatalf("Start run1: %v", err)
	}
	defer func() { _ = run1.Wait(ctx); _ = run1.Cleanup() }()

	flow2, work2 := newSlowFlow(t)
	started2 := make(chan *Run, 1)
	errs2 := make(chan error, 1)
	go func() {
		run2, err := c.Start(ctx, flow2, work2)
		if err != nil {
			errs2 <- err
			return
		}
		started2 <- run2
	}()

	select {
	case <-started2:
		t.Fatal("second Start returned before first run finished, MaxConcurrent not enforced")
	case err := <-errs2:
		t.Fatalf("second Start failed: %v", err)
	case <-time.After(500 * time.Millisecond):
		// expected: still blocked waiting for the semaphore
	}

	if err := run1.Wait(ctx); err != nil {
		t.Fatalf("Wait run1: %v", err)
	}

	select {
	case run2 := <-started2:
		defer func() { _ = run2.Wait(ctx); _ = run2.Cleanup() }()
	case err := <-errs2:
		t.Fatalf("second Start failed after first finished: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("second Start did not unblock after first run finished")
	}
}
