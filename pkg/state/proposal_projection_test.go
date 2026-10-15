package state

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
)

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func proposalCreatedControl(t *testing.T, opID, propID, stageID string) *Control {
	t.Helper()
	return &Control{
		Kind: kindProposalCreated, SchemaVersion: ControlSchemaVersion, OperationID: opID,
		Actor: Actor{Kind: "afm"}, Target: Target{StageID: stageID, ProposalID: propID},
		Payload: mustJSON(t, ProposalCreatedPayload{Type: "feedback", Title: "bound the retry loop"}),
	}
}

func proposalStatusControl(t *testing.T, propID string, p ProposalStatusChangedPayload) *Control {
	t.Helper()
	return &Control{
		Kind: kindProposalStatusChanged, SchemaVersion: ControlSchemaVersion,
		Target: Target{ProposalID: propID}, Payload: mustJSON(t, p),
	}
}

func TestProposalProjection_RoundTripSurvivesReplay(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.AppendControl(proposalCreatedControl(t, "op-1", "p-1", "a")); err != nil {
		t.Fatalf("AppendControl(created): %v", err)
	}
	if err := store.AppendControl(proposalStatusControl(t, "p-1", ProposalStatusChangedPayload{Status: "accepted", OperationID: "accept-1"})); err != nil {
		t.Fatalf("AppendControl(accepted): %v", err)
	}
	store.Close()

	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	p, ok := rs.Assistants.Proposals.Proposals["p-1"]
	if !ok {
		t.Fatal("proposal p-1 not restored")
	}
	if p.Status != assistants.ProposalAccepted {
		t.Fatalf("Status = %s, want accepted", p.Status)
	}
	if p.OperationID != "accept-1" {
		t.Fatalf("OperationID = %q, want accept-1", p.OperationID)
	}
	if len(p.History) != 2 {
		t.Fatalf("expected 2 history entries (pending, accepted), got %+v", p.History)
	}
}

func TestProposalProjection_IllegalTransitionRejected(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	if err := store.AppendControl(proposalCreatedControl(t, "op-1", "p-1", "a")); err != nil {
		t.Fatalf("AppendControl(created): %v", err)
	}
	// pending -> delivered is not a legal direct transition.
	err = store.AppendControl(proposalStatusControl(t, "p-1", ProposalStatusChangedPayload{Status: "delivered"}))
	if !errors.Is(err, ErrIllegalProposalTransition) {
		t.Fatalf("expected ErrIllegalProposalTransition, got %v", err)
	}
}

func TestProposalProjection_UnknownProposalRejected(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	err = store.AppendControl(proposalStatusControl(t, "does-not-exist", ProposalStatusChangedPayload{Status: "accepted"}))
	if !errors.Is(err, ErrUnknownProposal) {
		t.Fatalf("expected ErrUnknownProposal, got %v", err)
	}
}
