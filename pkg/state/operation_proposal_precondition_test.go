package state

import (
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
)

// TestOperation_ProposalStatusPreconditionRejectsStaleAccept exercises the ApplyRunOperation-level guard
// AcceptProposal relies on (T6.6/T6.7): accepting is only valid while the proposal is still "pending", checked
// under the SAME lock as the write — the primitive behind TestDoubleClick/TestAcceptFromTwoTabs.
func TestOperation_ProposalStatusPreconditionRejectsStaleAccept(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if err := store.AppendControl(proposalCreatedControl(t, "op-create", "p-1", "a")); err != nil {
		t.Fatalf("create proposal: %v", err)
	}

	acceptEffect := Effect{Kind: kindProposalStatusChanged, Target: Target{ProposalID: "p-1"},
		Payload: mustJSON(t, ProposalStatusChangedPayload{Status: "accepted", OperationID: "accept-1"})}
	op := Operation{
		ID: "accept-1", Kind: "accept_proposal",
		Expect:  Preconditions{ProposalStatus: map[string]assistants.ProposalStatus{"p-1": assistants.ProposalPending}},
		Effects: []Effect{acceptEffect},
	}
	if _, err := store.ApplyRunOperation(op, nil); err != nil {
		t.Fatalf("first accept must succeed: %v", err)
	}

	// A second, DIFFERENT accept attempt (two tabs / a retry with a fresh id) must be rejected: the proposal is
	// no longer "pending".
	op2 := op
	op2.ID = "accept-2"
	_, err = store.ApplyRunOperation(op2, nil)
	var rej *OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeStaleRevision {
		t.Fatalf("expected a stale_revision rejection for a second accept, got %v", err)
	}

	// The SAME accept (same operation id) replayed is an idempotent no-op success, not an error — TestDoubleClick/
	// TestRetryAfterTimeout/TestAccept_IdempotentByRequestId's underlying primitive.
	res, err := store.ApplyRunOperation(op, nil)
	if err != nil {
		t.Fatalf("replaying the SAME operation id must succeed idempotently, got %v", err)
	}
	if res.OperationID != "accept-1" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestOperation_StageGenerationPreconditionRejectsStale(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if err := store.AppendControl(proposalCreatedControl(t, "op-create", "p-1", "a")); err != nil {
		t.Fatalf("create proposal: %v", err)
	}

	op := Operation{
		ID:     "accept-1",
		Kind:   "accept_proposal",
		Expect: Preconditions{StageGeneration: map[string]uint64{"a": 0}},
		Effects: []Effect{{Kind: kindProposalStatusChanged, Target: Target{ProposalID: "p-1"},
			Payload: mustJSON(t, ProposalStatusChangedPayload{Status: "accepted"})}},
	}
	if _, err := store.ApplyRunOperation(op, nil); err != nil {
		t.Fatalf("accept against matching generation 0 must succeed: %v", err)
	}

	// Bump the stage's generation (a fresh start_run, as if the stage moved on to a new attempt) and confirm a
	// subsequent operation expecting the OLD generation is rejected.
	if err := store.Apply(&Transition{StageID: "a", From: StatusPending, To: StatusReady, Event: "ready"}); err != nil {
		t.Fatalf("Apply(ready): %v", err)
	}
	if err := store.Apply(&Transition{StageID: "a", From: StatusReady, To: StatusRunning, Event: "start_run"}); err != nil {
		t.Fatalf("Apply(start_run): %v", err)
	}

	op2 := Operation{
		ID:     "accept-2",
		Kind:   "accept_proposal",
		Expect: Preconditions{StageGeneration: map[string]uint64{"a": 0}},
		Effects: []Effect{{Kind: kindFeedbackIntentCreated, Target: Target{StageID: "a", IntentID: "fi-1"},
			Payload: mustJSON(t, FeedbackIntentCreatedPayload{StageID: "a", Feedback: "x"})}},
	}
	_, err = store.ApplyRunOperation(op2, nil)
	var rej *OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeStaleRevision {
		t.Fatalf("expected stale_revision after the stage generation moved on, got %v", err)
	}
}
