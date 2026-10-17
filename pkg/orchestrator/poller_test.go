package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// newPollerOrch builds an orchestrator whose stage starts in StatusRunning, returning the store and a
// cancellable context for the Run loop. pollerClock is the poller's clock — tests that only need ticks to
// resolve fast pass newFakeClockWithPump(t); tests that need to count exact ticks pass their own
// clock.FakeClock and drive it with BlockUntil/Advance.
func newPollerOrch(t *testing.T, runDir, stageID string, pollerClock clock.Clock) (*orchestrator.Orchestrator, *state.Store, context.Context, context.CancelFunc) {
	t.Helper()
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	// plan.md must exist so runImplementationAgent does not fail fast. With the plan present, the blocking
	// script holds the stage in StatusRunning for the poller to observe.
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# plan"), 0644); err != nil {
		t.Fatal(err)
	}
	// A plain script — mirrors the blocking-agent pattern already used by integration_interactive_test.go's
	// misplaced-question tests.
	scriptPath := filepath.Join(stageDir, "block.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(runDir, []string{stageID})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: stageID, From: state.StatusPending, To: state.StatusRunning, Event: "test"}); err != nil {
		t.Fatal(err)
	}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir,
		Stages: []flow.Stage{{ID: stageID, Name: stageID, Interactive: true, Command: scriptPath}},
		Store:  store,
		Config: config.Default(),
		Clock:  pollerClock,
	})
	ctx, cancel := context.WithCancel(context.Background())
	return orch, store, ctx, cancel
}

func TestPollQuestions_DetectsNewQuestion(t *testing.T) {
	runDir := t.TempDir()
	stageID := "poll-test"
	orch, store, ctx, cancel := newPollerOrch(t, runDir, stageID, newFakeClockWithPump(t))
	t.Cleanup(func() { store.Close() })

	// Write the question file BEFORE starting Run.
	stageDir := filepath.Join(runDir, stageID)
	qPath := filepath.Join(stageDir, "implementation.q1.question.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"go?"}`), 0644); err != nil {
		t.Fatal(err)
	}

	uiBus := orch.UIBus()
	subID, sub := uiBus.Subscribe(64)

	runOrchestratorAsync(ctx, t, orch, cancel)

	// The polling goroutine ticks every second; allow a comfortable margin.
	timeout := time.After(3 * time.Second)
	for {
		select {
		case ev := <-sub:
			if ev.Type == bus.EventAskUser && ev.StageID == stageID {
				uiBus.Unsubscribe(subID)
				return // success
			}
		case <-timeout:
			uiBus.Unsubscribe(subID)
			t.Fatal("timeout: EventAskUser not received within 3s")
		}
	}
}

// TestPollQuestions_Idempotent verifies that the same question file triggers EventAskUser exactly once
// across multiple poller ticks — driven explicitly via Advance instead of sleeping out several real 1s
// ticks (dev-plan.md, T7).
func TestPollQuestions_Idempotent(t *testing.T) {
	runDir := t.TempDir()
	stageID := "idem-test"
	fc := clock.NewFake(time.Unix(0, 0))
	orch, store, ctx, cancel := newPollerOrch(t, runDir, stageID, fc)
	t.Cleanup(func() { store.Close() })

	stageDir := filepath.Join(runDir, stageID)
	qPath := filepath.Join(stageDir, "implementation.q1.question.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"go?"}`), 0644); err != nil {
		t.Fatal(err)
	}

	uiBus := orch.UIBus()
	subID, sub := uiBus.Subscribe(64)

	runOrchestratorAsync(ctx, t, orch, cancel)

	askCount := 0
	drainAsks := func() {
		for {
			select {
			case ev := <-sub:
				if ev.Type == bus.EventAskUser && ev.StageID == stageID {
					askCount++
				}
			case <-time.After(100 * time.Millisecond):
				return // settle window for the poller goroutine to process this tick
			}
		}
	}

	// Advance 3 ticks explicitly — the same question file must surface EventAskUser on the first tick only.
	for i := 0; i < 3; i++ {
		fc.BlockUntil(1)
		fc.Advance(time.Second)
		drainAsks()
	}
	uiBus.Unsubscribe(subID)
	if askCount != 1 {
		t.Errorf("EventAskUser published %d times, want exactly 1", askCount)
	}
}
