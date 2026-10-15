package state

import (
	"errors"
	"testing"
)

func feedbackIntentCreatedControl(t *testing.T, opID, intentID, stageID string) *Control {
	t.Helper()
	return &Control{
		Kind: kindFeedbackIntentCreated, SchemaVersion: ControlSchemaVersion, OperationID: opID,
		Target:  Target{StageID: stageID, IntentID: intentID},
		Payload: mustJSON(t, FeedbackIntentCreatedPayload{StageID: stageID, Feedback: "please fix X", Origin: "user"}),
	}
}

func feedbackIntentConsumedControl(t *testing.T, opID, intentID string, p FeedbackIntentConsumedPayload) *Control {
	t.Helper()
	return &Control{
		Kind: kindFeedbackIntentConsumed, SchemaVersion: ControlSchemaVersion, OperationID: opID,
		Target: Target{IntentID: intentID}, Payload: mustJSON(t, p),
	}
}

func TestFeedbackIntentProjection_RoundTripSurvivesReplay(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.AppendControl(feedbackIntentCreatedControl(t, "op-1", "fi-1", "a")); err != nil {
		t.Fatalf("AppendControl(created): %v", err)
	}
	if err := store.AppendControl(feedbackIntentConsumedControl(t, "op-2", "fi-1", FeedbackIntentConsumedPayload{Status: "delivered"})); err != nil {
		t.Fatalf("AppendControl(consumed): %v", err)
	}
	store.Close()

	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	fi, ok := rs.Assistants.FeedbackIntents["fi-1"]
	if !ok {
		t.Fatal("intent fi-1 not restored")
	}
	if fi.Status != FeedbackIntentDelivered {
		t.Fatalf("Status = %s, want delivered", fi.Status)
	}
}

func TestFeedbackIntentProjection_DoubleConsumeRejected(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	if err := store.AppendControl(feedbackIntentCreatedControl(t, "op-1", "fi-1", "a")); err != nil {
		t.Fatalf("AppendControl(created): %v", err)
	}
	if err := store.AppendControl(feedbackIntentConsumedControl(t, "op-2", "fi-1", FeedbackIntentConsumedPayload{Status: "delivered"})); err != nil {
		t.Fatalf("AppendControl(consumed): %v", err)
	}
	// A second, DIFFERENT consume attempt (e.g. a buggy double-fire) must be rejected — delivery happens
	// exactly once (T6.6's TestFeedbackNotAppendedTwice is the orchestrator-level version of this guarantee).
	err = store.AppendControl(feedbackIntentConsumedControl(t, "op-3", "fi-1", FeedbackIntentConsumedPayload{Status: "rejected", Reason: "late"}))
	if !errors.Is(err, ErrFeedbackIntentAlreadyConsumed) {
		t.Fatalf("expected ErrFeedbackIntentAlreadyConsumed, got %v", err)
	}
}

func TestStageGeneration_TracksStartRunTransitions(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if g := store.StageGeneration("a"); g != 0 {
		t.Fatalf("a never-started stage must have generation 0, got %d", g)
	}
	if err := store.Apply(&Transition{StageID: "a", From: StatusPending, To: StatusReady, Event: "ready"}); err != nil {
		t.Fatalf("Apply(ready): %v", err)
	}
	tr := &Transition{StageID: "a", From: StatusReady, To: StatusRunning, Event: "start_run"}
	if err := store.Apply(tr); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	firstGen := store.StageGeneration("a")
	if firstGen == 0 {
		t.Fatal("generation must be nonzero after the first start_run")
	}

	// A second start_run (a later attempt — e.g. after a Revise cycle) must bump the generation further.
	if err := store.Apply(&Transition{StageID: "a", From: StatusRunning, To: StatusRevising, Event: "revise"}); err != nil {
		t.Fatalf("Apply(revise): %v", err)
	}
	tr2 := &Transition{StageID: "a", From: StatusRevising, To: StatusRunning, Event: "start_run"}
	if err := store.Apply(tr2); err != nil {
		t.Fatalf("Apply(2): %v", err)
	}
	secondGen := store.StageGeneration("a")
	if secondGen <= firstGen {
		t.Fatalf("second start_run must produce a strictly higher generation: first=%d second=%d", firstGen, secondGen)
	}
}
