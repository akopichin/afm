package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

func TestRunVerification_ShellStep_PreSignalledInterrupt_NeverStarts(t *testing.T) {
	stage := flow.Stage{
		ID:     verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyShell, Run: "true"}}},
	}
	o, _ := newVerifyTestOrchestrator(t, stage)

	interruptCh := make(chan struct{}, 1)
	interruptCh <- struct{}{} // сигнал уже лежит в канале ДО вызова RunVerification
	o.interruptChans.Store(stage.ID, interruptCh)

	var calls int
	o.runVerifyShell = func(context.Context, string, string) (string, error) {
		calls++
		return "", nil
	}

	err := o.RunVerification(context.Background(), stage, phaseImplementation)
	if !errors.Is(err, executor.ErrUserInterrupted) {
		t.Fatalf("expected executor.ErrUserInterrupted, got %v (%T)", err, err)
	}
	if calls != 0 {
		t.Errorf("shell verify command must never start, got %d calls", calls)
	}
}

func TestRunVerification_ShellStep_AlreadyPausedStatus_NeverStarts(t *testing.T) {
	stage := flow.Stage{
		ID:     verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyShell, Run: "true"}}},
	}
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	o := &Orchestrator{
		opts: Options{RunDir: runDir, Stages: []flow.Stage{stage}, RunID: "run1", Store: store},
		ui:   bus.NewUIBus(),
	}

	var calls int
	o.runVerifyShell = func(context.Context, string, string) (string, error) {
		calls++
		return "", nil
	}

	gotErr := o.RunVerification(context.Background(), stage, phaseImplementation)
	if !errors.Is(gotErr, executor.ErrUserInterrupted) {
		t.Fatalf("expected executor.ErrUserInterrupted, got %v (%T)", gotErr, gotErr)
	}
	if calls != 0 {
		t.Errorf("shell verify command must never start, got %d calls", calls)
	}
}
