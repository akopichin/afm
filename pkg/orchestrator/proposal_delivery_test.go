package orchestrator

// Этап 10 (T10.1/T10.2/T10.4): delivery for assistants.ProposalAddStage/ProposalAttachAssistant
// (proposal_delivery.go) — the unified policy that makes a graph/task-tree mutation proposed BY an assistant
// go through the EXACT SAME validated entry points (CommitAddStage, Attach, admitChildren) the manual
// dashboard and side-agent draft-confirm paths already use.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// newProposalDeliveryTestOrchestrator builds a real Orchestrator with BOTH collaboration enabled (one
// "reviewer" profile with OS read-only enforcement, one "unrestricted" profile with none — needed by the
// no-self-elevation tests) AND a real, safe-to-exec default agent command (mirrors graph_mutate_test.go's
// newGraphMutateTestOrchestrator/newSafeAgentCommand/autoStage, same package) — Этап 10 proposal delivery
// exercises both CommitAddStage and Attach/admitChildren through the SAME orchestrator instance.
func newProposalDeliveryTestOrchestrator(t *testing.T, cmd string, stages []flow.Stage) (*Orchestrator, *state.Store) {
	t.Helper()
	runDir := t.TempDir()
	ids := make([]string, 0, len(stages))
	for _, s := range stages {
		ids = append(ids, s.ID)
	}
	store, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-1"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}
	reg, err := assistants.BuildRegistry(nil, []assistants.Profile{
		{ID: "reviewer", Title: "Reviewer", Command: "helper-cmd-os", Capabilities: assistants.Capabilities{Delegate: true}},
		{ID: "unrestricted", Title: "Unrestricted", Command: "helper-cmd-none", Capabilities: assistants.Capabilities{Delegate: true}},
	}, nil, "claude", func(c string) (bool, string, assistants.ReadonlyEnforcement) {
		if c == "helper-cmd-none" {
			return true, "", assistants.ReadonlyEnforcementNone
		}
		return true, "", assistants.ReadonlyEnforcementOS
	})
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	cfg := config.Default()
	cfg.Client.Command = cmd
	orch := New(Options{RunDir: runDir, Stages: stages, Store: store, Config: cfg, Prompts: DefaultPrompts(), AssistantsEnabled: true, AssistantsRegistry: reg})
	// CommitAddStage wakes the scheduler via tryActivatePrePlanned, which spawns agent goroutines fire-and-
	// forget — registered AFTER the store-close cleanup above so LIFO runs it FIRST (same ordering rationale
	// as newGraphMutateTestOrchestrator).
	t.Cleanup(func() { orch.concurrency.WaitAgents() })
	return orch, store
}

func addStagePayloadJSON(t *testing.T, id, cmd string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(AddStageDraft{Kind: AddStageKindAppend, Stage: graph.StageDraft{ID: id, Name: "New stage", Command: cmd, Prompt: "do it"}})
	if err != nil {
		t.Fatalf("marshal AddStageDraft: %v", err)
	}
	return raw
}

func seedAddStageProposal(ctx context.Context, t *testing.T, o *Orchestrator, opID, stageID, newStageID, cmd string) string {
	t.Helper()
	id, err := o.CreateProposal(ctx, CreateProposalRequest{
		OperationID: opID, StageID: stageID, Type: assistants.ProposalAddStage, Title: "add a stage",
		Payload: addStagePayloadJSON(t, newStageID, cmd),
		Authors: []assistants.MessageAuthor{{Kind: assistants.AuthorAssistant, ID: "planner"}},
	})
	if err != nil {
		t.Fatalf("CreateProposal(add_stage): %v", err)
	}
	return id
}

// ---- T10.1 ----

// TestAction_AddStageDraftValidatedLikeManual proves AcceptProposal's add_stage delivery is the SAME
// validator the manual dashboard path (CommitAddStage, called directly here to stand in for the HTTP route
// that calls it verbatim — pkg/server/graph.go) uses: an identical malformed draft (duplicate stage id) is
// rejected with the exact same *graph.ValidationError code through BOTH paths, and an identical valid draft
// is accepted through both.
func TestAction_AddStageDraftValidatedLikeManual(t *testing.T) {
	cmd := newSafeAgentCommand(t)
	o, _ := newProposalDeliveryTestOrchestrator(t, cmd, []flow.Stage{autoStage("impl", cmd)})
	ctx := context.Background()

	// Invalid: duplicate stage id "impl" — manual path.
	_, manualErr := o.CommitAddStage(ctx, AddStageDraft{Kind: AddStageKindAppend, RequestID: "manual-dup", Stage: graph.StageDraft{ID: "impl", Command: cmd, Prompt: "x"}})
	var manualVE *graph.ValidationError
	if !errors.As(manualErr, &manualVE) || manualVE.Code != graph.CodeDuplicateStageID {
		t.Fatalf("manual CommitAddStage(dup id): err=%v, want *graph.ValidationError{Code:duplicate_stage_id}", manualErr)
	}

	// Invalid: duplicate stage id "impl" — proposal delivery path.
	propID := seedAddStageProposal(ctx, t, o, "op-dup", "impl", "impl", cmd)
	deliverErr := o.AcceptProposal(ctx, propID, "accept-dup", state.Actor{Kind: "user"})
	var deliverVE *graph.ValidationError
	if !errors.As(deliverErr, &deliverVE) || deliverVE.Code != graph.CodeDuplicateStageID {
		t.Fatalf("AcceptProposal(add_stage, dup id): err=%v, want *graph.ValidationError{Code:duplicate_stage_id} (SAME validator as manual path)", deliverErr)
	}

	// Valid: a genuinely new stage id, delivered through the proposal path, must actually land in the
	// registry — proving the "same validator" also accepts what the manual path would accept.
	propID2 := seedAddStageProposal(ctx, t, o, "op-new", "impl", "new-stage", cmd)
	if err := o.AcceptProposal(ctx, propID2, "accept-new", state.Actor{Kind: "user"}); err != nil {
		t.Fatalf("AcceptProposal(add_stage, valid): %v", err)
	}
	if _, ok := o.reg().StageByID("new-stage"); !ok {
		t.Fatal("accepted add_stage proposal did not install the new stage into the registry")
	}
}

// TestAction_AssistantCanProposeNotApplyGraphChange — §12.5: CreateProposal (the assistant "proposing") must
// NEVER itself mutate the flow graph. Only AcceptProposal (a human's explicit confirmation) does.
func TestAction_AssistantCanProposeNotApplyGraphChange(t *testing.T) {
	cmd := newSafeAgentCommand(t)
	o, _ := newProposalDeliveryTestOrchestrator(t, cmd, []flow.Stage{autoStage("impl", cmd)})
	ctx := context.Background()
	revBefore := o.reg().Revision()

	propID := seedAddStageProposal(ctx, t, o, "op-propose", "impl", "new-stage", cmd)
	if _, ok := o.reg().StageByID("new-stage"); ok {
		t.Fatal("CreateProposal must not apply the graph change — new-stage must not exist yet")
	}
	if o.reg().Revision() != revBefore {
		t.Fatalf("CreateProposal must not bump the graph revision: before=%d after=%d", revBefore, o.reg().Revision())
	}

	if err := o.AcceptProposal(ctx, propID, "accept-propose", state.Actor{Kind: "user"}); err != nil {
		t.Fatalf("AcceptProposal: %v", err)
	}
	if _, ok := o.reg().StageByID("new-stage"); !ok {
		t.Fatal("AcceptProposal (explicit confirmation) must apply the graph change")
	}
}

// TestAction_InternalDelegationReusesStage4aChecks proves that delivering a ProposalAttachAssistant with
// Proposal.TaskID set (attach as a CHILD of an existing task) goes through admitChildren — Этап 4a's SHARED
// depth-admission gate — rather than a second, hand-rolled check: a child that would exceed the run's
// delegation-depth policy is rejected with the SAME CodeDelegationDepthExceeded admitChildren's own direct
// callers already get (assistants_delegate_test.go's TestAdmission_DepthBoundary).
func TestAction_InternalDelegationReusesStage4aChecks(t *testing.T) {
	cmd := newSafeAgentCommand(t)
	o, store := newProposalDeliveryTestOrchestrator(t, cmd, []flow.Stage{{ID: assistantsTestStageIDForProposals, Command: "claude"}})
	// MaxDepth=1: a top-level task (depth 1) is already at the ceiling — any child of it is depth 2, blocked.
	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 1, Source: assistants.DepthSourceDefault, Revision: "rev-depth-1"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageIDForProposals)
	// A fake runHelperAgent stands in for a real subprocess (same seam as assistants_delegate_test.go's
	// runningParentTask) — the parent task's own profile command ("helper-cmd-os") is not a real executable,
	// and this test only cares about the CHILD admission decision, not actually running the parent's turn.
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}
	parentID, err := o.Attach(ctx, AttachRequest{OperationID: "op-parent", StageID: assistantsTestStageIDForProposals, ProfileID: "reviewer", Instruction: "x", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	payload, _ := json.Marshal(AttachAssistantProposalPayload{ProfileID: "reviewer", Title: "child", Instruction: "do it"})
	propID, err := o.CreateProposal(ctx, CreateProposalRequest{
		OperationID: "op-child-proposal", StageID: assistantsTestStageIDForProposals, TaskID: string(parentID),
		Type: assistants.ProposalAttachAssistant, Title: "delegate a child", Payload: payload,
		Authors: []assistants.MessageAuthor{{Kind: assistants.AuthorAssistant, ID: string(parentID)}},
	})
	if err != nil {
		t.Fatalf("CreateProposal(attach_assistant, child): %v", err)
	}

	err = o.AcceptProposal(ctx, propID, "accept-child", state.Actor{Kind: "user"})
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeDelegationDepthExceeded {
		t.Fatalf("AcceptProposal(attach_assistant, child beyond max_depth): err=%v, want CodeDelegationDepthExceeded (admitChildren reused)", err)
	}
}

const assistantsTestStageIDForProposals = "impl-proposals"

// TestAddStageDisabled_AcceptProposalKeepsProposalPending — при выключенном add_stage принятие предложения
// отвечает feature_disabled ДО смены статуса: предложение остаётся pending, журнал и граф не меняются.
func TestAddStageDisabled_AcceptProposalKeepsProposalPending(t *testing.T) {
	cmd := newSafeAgentCommand(t)
	o, store := newProposalDeliveryTestOrchestrator(t, cmd, []flow.Stage{autoStage("impl", cmd)})
	ctx := context.Background()
	propID := seedAddStageProposal(ctx, t, o, "op-disabled", "impl", "new-stage", cmd)
	o.opts.AddStageDisabled = true
	seqBefore := store.Snapshot().LastSeq
	revBefore := o.reg().Revision()

	requireAddStageDisabled(t, o.AcceptProposal(ctx, propID, "accept-disabled", state.Actor{Kind: "user"}))

	p, ok := o.Proposal(propID)
	if !ok || p.Status != assistants.ProposalPending {
		t.Fatalf("proposal after rejected accept = %+v (found=%v), want status pending", p, ok)
	}
	if got := store.Snapshot().LastSeq; got != seqBefore {
		t.Fatalf("journal advanced by rejected accept: LastSeq %d -> %d", seqBefore, got)
	}
	if got := o.reg().Revision(); got != revBefore {
		t.Fatalf("registry revision changed by rejected accept: %d -> %d", revBefore, got)
	}
}
