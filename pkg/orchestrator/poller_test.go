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

// blockingRunner holds RunPlanning/RunAgent open until ctx is cancelled. It keeps
// a stage in its active status while the file-based question poller runs, so the
// poller has a chance to detect *.question.json files. This avoids the real agent
// binary and any fast-fail status transitions during the test.
type blockingRunner struct{}

func (blockingRunner) RunPlanning(ctx context.Context, _, _, _, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

func (blockingRunner) RunAgent(ctx context.Context, _, _, _, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

// newPollerOrch builds an orchestrator whose stage starts in StatusRunning with a
// blocking runner, returning the store and a cancellable context for the Run loop.
func newPollerOrch(t *testing.T, runDir, stageID string) (*orchestrator.Orchestrator, *state.Store, context.Context, context.CancelFunc) {
	t.Helper()
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	// plan.md must exist so runImplementationAgent does not fail fast (it reads
	// the plan before invoking the runner). With the plan present, the blocking
	// runner holds the stage in StatusRunning for the poller to observe.
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# plan"), 0644); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(runDir, []string{stageID})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(state.Transition{StageID: stageID, From: state.StatusPending, To: state.StatusRunning, Event: "test"}); err != nil {
		t.Fatal(err)
	}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir,
		Stages: []flow.Stage{{ID: stageID, Name: stageID}},
		Store:  store,
		Config: config.Default(),
		Runner: blockingRunner{},
	})
	ctx, cancel := context.WithCancel(context.Background())
	return orch, store, ctx, cancel
}

// settleAndCancel cancels the Run context and waits until the orchestrator's
// launched agent goroutine reaches a terminal status. This must happen before
// the store is closed: the agent goroutine applies an EvFail transition on
// cancellation, and closing the store first would nil its events log and panic.
func settleAndCancel(t *testing.T, store *state.Store, stageID string, cancel context.CancelFunc) {
	t.Helper()
	cancel()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		switch store.Get(stageID) {
		case state.StatusDone, state.StatusFailed:
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestPollQuestions_DetectsNewQuestion verifies that the 1-second question
// poller, started by Run, detects a *.question.json file and publishes
// EventAskUser so the UI and stage status reflect the open question.
func TestPollQuestions_DetectsNewQuestion(t *testing.T) {
	runDir := t.TempDir()
	stageID := "poll-test"
	orch, store, ctx, cancel := newPollerOrch(t, runDir, stageID)
	defer store.Close()
	defer settleAndCancel(t, store, stageID, cancel)

	// Write the question file BEFORE starting Run.
	stageDir := filepath.Join(runDir, stageID)
	qPath := filepath.Join(stageDir, "implementation.q1.question.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"go?"}`), 0644); err != nil {
		t.Fatal(err)
	}

	uiBus := orch.UIBus()
	subID, sub := uiBus.Subscribe(64)

	go func() { _ = orch.Run(ctx) }()

	// The polling goroutine ticks every second; allow a comfortable margin.
	timeout := time.After(3 * time.Second)
	for {
		select {
		case ev := <-sub:
			if ev.Type == orchestrator.EventAskUser && ev.StageID == stageID {
				uiBus.Unsubscribe(subID)
				return // success
			}
		case <-timeout:
			uiBus.Unsubscribe(subID)
			t.Fatal("timeout: EventAskUser not received within 3s")
		}
	}
}

// TestPollQuestions_Idempotent verifies that the same question file triggers
// EventAskUser exactly once across multiple poller ticks.
func TestPollQuestions_Idempotent(t *testing.T) {
	runDir := t.TempDir()
	stageID := "idem-test"
	orch, store, ctx, cancel := newPollerOrch(t, runDir, stageID)
	defer store.Close()
	defer settleAndCancel(t, store, stageID, cancel)

	stageDir := filepath.Join(runDir, stageID)
	qPath := filepath.Join(stageDir, "implementation.q1.question.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"go?"}`), 0644); err != nil {
		t.Fatal(err)
	}

	uiBus := orch.UIBus()
	subID, sub := uiBus.Subscribe(64)

	go func() { _ = orch.Run(ctx) }()

	// Observe for long enough that the 1-second poller ticks at least twice.
	askCount := 0
	timer := time.After(2500 * time.Millisecond)
	done := false
	for !done {
		select {
		case ev := <-sub:
			if ev.Type == orchestrator.EventAskUser && ev.StageID == stageID {
				askCount++
			}
		case <-timer:
			done = true
		}
	}
	uiBus.Unsubscribe(subID)
	if askCount != 1 {
		t.Errorf("EventAskUser published %d times, want exactly 1", askCount)
	}
}
