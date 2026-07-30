package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

func TestRecovery_ResumesHookFailed(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	mainMarker := filepath.Join(rootDir, "main-ran.marker")

	stages := []flow.Stage{{
		ID:           "notify",
		Name:         "Notify",
		Script:       "touch " + mainMarker,
		ScriptBefore: "exit 1",
	}}

	// Simulate a prior crash: stage already in hook_failed with a pending
	// before-hook recorded on disk (as runBeforeHook would have left it).
	store, err := state.Open(runDir, []string{"notify"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := store.Apply(&state.Transition{StageID: "notify", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "notify", From: state.StatusRunning, To: state.StatusHookFailed, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(runDir, "notify")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	// runBeforeHook always writes hook_pending.json BEFORE transitioning to
	// hook_failed (hooks.go) — reproduce that invariant here so resume finds
	// the same on-disk record a real crash would have left. Written raw
	// (matching hookPending's JSON shape) since this is an external test
	// package and can't call the unexported writeHookPending.
	hookPendingJSON := `{"hook":"before","script":"exit 1","timeout":0}`
	if err := os.WriteFile(filepath.Join(stageDir, "hook_pending.json"), []byte(hookPendingJSON), 0644); err != nil {
		t.Fatal(err)
	}
	store.Close() // simulate process exit

	// Reopen (simulating `afm run` restart) and re-run with a fresh Orchestrator.
	store2, err := state.Open(runDir, []string{"notify"})
	if err != nil {
		t.Fatalf("state.Open (reopen): %v", err)
	}
	t.Cleanup(func() { store2.Close() })

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		RootDir: rootDir,
		Stages:  stages,
		Store:   store2,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	// The stage should still be waiting in hook_failed (not silently retried).
	time.Sleep(500 * time.Millisecond)
	if got := orchestrator.StoreFromOrch(orch).Get("notify"); got != state.StatusHookFailed {
		t.Fatalf("status = %v, want hook_failed (resumed, not auto-retried)", got)
	}

	if err := orch.SkipHook("notify"); err != nil {
		t.Fatalf("SkipHook: %v", err)
	}

	stateFile := filepath.Join(runDir, "state.json")
	waitForStatus(t, stateFile, "notify", state.StatusDone, 20*time.Second)
	if _, err := os.Stat(mainMarker); err != nil {
		t.Error("main script should have run after skip")
	}

	cancel()
	<-runDone
}

// TestRecovery_ResumesPendingAfterHook mirrors
// TestRecovery_ResumesHookFailed for the other crash-recovery gap: a stage
// that has already reached `done` (script_after never touches the FSM —
// hooks.go) but crashed while its after-hook was blocked on a retry/skip
// decision. That pending decision is invisible via stage status (still
// `done` before AND after the crash) — the only on-disk trace is
// hook_pending.json (Hook == "after"), which recovery.go must scan for
// regardless of status.
func TestRecovery_ResumesPendingAfterHook(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()

	stages := []flow.Stage{{
		ID:          "notify",
		Name:        "Notify",
		Script:      "echo main-ok",
		ScriptAfter: "exit 1",
	}}

	// First run: get the stage genuinely to done with a genuinely failed
	// after-hook (same pattern as
	// TestIntegration_ScriptAfter_FailsThenSkip_StageStaysDone), then crash
	// it (cancel ctx) WITHOUT ever resolving the hook — hook_pending.json
	// is left on disk, exactly as a real process kill would leave it.
	store, err := state.Open(runDir, []string{"notify"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}

	orch1 := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		RootDir: rootDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx1, cancel1 := context.WithTimeout(context.Background(), 30*time.Second)
	runDone1 := make(chan error, 1)
	go func() { runDone1 <- orch1.Run(ctx1) }()

	stateFile := filepath.Join(runDir, "state.json")
	waitForStatus(t, stateFile, "notify", state.StatusDone, 20*time.Second)

	stageDir := filepath.Join(runDir, "notify")
	pendingPath := filepath.Join(stageDir, "hook_pending.json")
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(pendingPath); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(pendingPath); err != nil {
		t.Fatal("expected hook_pending.json for the failed after-hook before simulating the crash")
	}

	// Simulate the crash: cancel without ever calling RetryHook/SkipHook, so
	// the after-hook goroutine's wait is abandoned mid-flight and
	// hook_pending.json is left behind, unresolved.
	cancel1()
	<-runDone1
	store.Close()

	// Reopen (simulating `afm run` restart) and re-run with a fresh Orchestrator.
	store2, err := state.Open(runDir, []string{"notify"})
	if err != nil {
		t.Fatalf("state.Open (reopen): %v", err)
	}
	t.Cleanup(func() { store2.Close() })

	orch2 := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		RootDir: rootDir,
		Stages:  stages,
		Store:   store2,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()
	runDone2 := make(chan error, 1)
	go func() { runDone2 <- orch2.Run(ctx2) }()

	// Status must stay done throughout resume (after-hooks never touch the FSM).
	time.Sleep(500 * time.Millisecond)
	if got := orchestrator.StoreFromOrch(orch2).Get("notify"); got != state.StatusDone {
		t.Fatalf("status = %v, want done (after-hook resume must never touch the FSM)", got)
	}

	// SkipHook only succeeds if the recovery scan actually re-registered a
	// waiter for this stage's pending after-hook — proving the resume path
	// ran, not just that the code compiles unexercised.
	if err := orch2.SkipHook("notify"); err != nil {
		t.Fatalf("SkipHook: %v (recovery did not resume the pending after-hook wait)", err)
	}

	// The resumed wait must actually process the decision: hook_pending.json
	// gets cleared once resolved (resumeAfterHookWait -> clearHookPending).
	deadline = time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(pendingPath); os.IsNotExist(err) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(pendingPath); err == nil {
		t.Error("expected hook_pending.json to be cleared after SkipHook resolved the resumed wait")
	}

	if got := orchestrator.StoreFromOrch(orch2).Get("notify"); got != state.StatusDone {
		t.Errorf("status = %v, want done (still unaffected after resolving the resumed after-hook)", got)
	}

	cancel2()
	<-runDone2
}

// TestRecovery_FiresAfterHookOnRecoveredDone covers the crash scenario where
// a stage's real work finished (its .done marker is on disk) but afm crashed
// BEFORE the live completion path (completeStage in orchestrator.go) ever
// ran — so script_after was never even attempted, not just interrupted
// mid-flight. On restart, recovery.go detects the StatusRunning stage's
// .done file and fires EvComplete directly (recovery.go's "recovered .done"
// site under the StatusRunning case). Without a maybeRunAfterHook call right
// after that Trigger, the stage's script_after would never run at all. This
// differs from TestRecovery_ResumesPendingAfterHook, which resumes a hook
// that had ALREADY started (hook_pending.json already on disk from a first
// real run) — here the hook has not started at all before the simulated
// crash, so hook_pending.json does not exist yet.
func TestRecovery_FiresAfterHookOnRecoveredDone(t *testing.T) {
	rootDir := t.TempDir()
	runDir := t.TempDir()
	afterMarker := filepath.Join(rootDir, "after-ran.marker")

	stages := []flow.Stage{{
		ID:          "notify",
		Name:        "Notify",
		ScriptAfter: "touch " + afterMarker,
	}}

	// Simulate a crash: the stage's real work already finished (.done
	// written, as an agent/script would leave it) and the store recorded it
	// as StatusRunning, but the process died before completeStage ever ran
	// — so EvComplete was never fired and script_after was never attempted.
	store, err := state.Open(runDir, []string{"notify"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := store.Apply(&state.Transition{StageID: "notify", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(runDir, "notify")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done\n"), 0644); err != nil {
		t.Fatal(err)
	}
	store.Close() // simulate process exit

	// Reopen (simulating `afm run` restart) and re-run with a fresh Orchestrator.
	store2, err := state.Open(runDir, []string{"notify"})
	if err != nil {
		t.Fatalf("state.Open (reopen): %v", err)
	}
	t.Cleanup(func() { store2.Close() })

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		RootDir: rootDir,
		Stages:  stages,
		Store:   store2,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	stateFile := filepath.Join(runDir, "state.json")
	waitForStatus(t, stateFile, "notify", state.StatusDone, 20*time.Second)

	// The key assertion: recovery must have actually given script_after a
	// chance to run, not silently skipped it because EvComplete was fired
	// via recovery.go instead of the live completeStage path.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(afterMarker); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(afterMarker); err != nil {
		t.Error("expected script_after to have run after recovery fired EvComplete for the recovered .done stage")
	}

	cancel()
	<-runDone
}
