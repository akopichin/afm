package orchestrator

// Этап 10 (T10.2): one policy, three sources. UI (a manual dashboard action), side-agent (an action draft the
// side agent proposed and a human confirmed), and assistant (a helper task delegating to its own task tree)
// all funnel through the SAME admission functions — admitChildren for child-task creation, CommitAddStage for
// graph mutation — with no source-based branch inside the check itself (the only branch admitChildren has,
// the actor.Kind=="assistant" self-match/no-self-elevation carve-outs, is deliberately asymmetric and is
// covered by its own tests below, not by the "same decision" table).

import (
	"context"
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// policySources enumerates the three request origins T10.2 requires to share one verdict — ID is filled in
// per test case (an assistant actor's ID must equal the delegating task for the self-match rule elsewhere to
// even let the request reach the checks under test here).
func policySources(parentID assistants.TaskID) []state.Actor {
	return []state.Actor{
		{Kind: "user", ID: "dashboard-operator"},
		{Kind: "side_agent", ID: "side"},
		{Kind: "assistant", ID: string(parentID)},
	}
}

// TestPolicy_SameDecisionForUiSideAgentAndAssistant — a table test: the SAME logical child-admission request,
// driven through admitChildren with each of the three actor kinds in turn, must reach the SAME verdict for
// every invariant that is NOT actor-specific by design (depth ceiling, unknown profile, quota). All three
// literally call (*Orchestrator).admitChildren — the point of T10.2 is that there is no second, laxer check
// anywhere else a UI button or a side-agent confirm could instead be wired to.
func TestPolicy_SameDecisionForUiSideAgentAndAssistant(t *testing.T) {
	cases := []struct {
		name       string
		children   []assistants.DelegateRequest
		maxDepth   int
		wantCode   assistants.ErrorCode
		wantAccept bool
	}{
		{name: "unknown profile rejected for every source", children: []assistants.DelegateRequest{{ProfileID: "ghost", Instruction: "x"}}, maxDepth: 5, wantCode: assistants.CodeCapabilityUnavailable},
		{name: "depth exceeded rejected for every source", children: oneChild(), maxDepth: 1, wantCode: assistants.CodeDelegationDepthExceeded},
		{name: "valid request accepted for every source", children: oneChild(), maxDepth: 5, wantAccept: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			activateStage(t, o, assistantsTestStageID)
			if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: c.maxDepth, Source: assistants.DepthSourceDefault, Revision: "rev-" + c.name}, state.Actor{Kind: "system"}); err != nil {
				t.Fatalf("EnsurePolicyRecorded: %v", err)
			}
			release := make(chan struct{})
			defer func() {
				close(release)
				o.concurrency.WaitAgents()
			}()
			parentID := runningParentTask(t, o, store, release)

			for i, actor := range policySources(parentID) {
				_, err := o.admitChildren(context.Background(), c.name+"-op-"+actor.Kind, parentID, c.children, actor, "", nil)
				if c.wantAccept {
					if err != nil {
						t.Fatalf("source %s (%d): admitChildren = %v, want success", actor.Kind, i, err)
					}
					continue
				}
				var rej *state.OperationRejectedError
				if !errors.As(err, &rej) || rej.Code != c.wantCode {
					t.Fatalf("source %s (%d): admitChildren = %v, want OperationRejectedError{Code:%s}", actor.Kind, i, err, c.wantCode)
				}
			}
		})
	}
}

// TestPolicy_ChildCapabilitiesNeverWiderThanParent — regardless of WHICH source created it, a child task's
// depth/root-stage/policy fields are ALWAYS derived from the parent/the run's current policy, never taken
// from the request (there is no field in assistants.DelegateRequest for any of them to begin with).
func TestPolicy_ChildCapabilitiesNeverWiderThanParent(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	release := make(chan struct{})
	defer func() {
		close(release)
		o.concurrency.WaitAgents()
	}()
	parentID := runningParentTask(t, o, store, release)
	rsBefore := store.Snapshot()
	parent := rsBefore.Assistants.Tasks[parentID]

	for _, actor := range policySources(parentID) {
		childIDs, err := o.admitChildren(context.Background(), "cap-op-"+actor.Kind, parentID, oneChild(), actor, "", nil)
		if err != nil {
			t.Fatalf("source %s: admitChildren: %v", actor.Kind, err)
		}
		rs := store.Snapshot()
		child := rs.Assistants.Tasks[childIDs[0]]
		if child.RootStageID != parent.RootStageID {
			t.Fatalf("source %s: child.RootStageID = %q, want parent's %q", actor.Kind, child.RootStageID, parent.RootStageID)
		}
		if child.MaxDepthAtCreate != rs.Assistants.Policy.MaxDepth {
			t.Fatalf("source %s: child.MaxDepthAtCreate = %d, want the run's own policy MaxDepth %d", actor.Kind, child.MaxDepthAtCreate, rs.Assistants.Policy.MaxDepth)
		}
		if child.Depth != parent.Depth+1 {
			t.Fatalf("source %s: child.Depth = %d, want parent.Depth+1 = %d", actor.Kind, child.Depth, parent.Depth+1)
		}
	}
}

// TestPolicy_DirectBackendRequestCheckedLikeModelAction — a "direct backend request" (what a manual HTTP
// handler calls, simulated here by calling CommitAddStage directly with actor-irrelevant content, since
// CommitAddStage takes no actor at all) must be checked with the SAME strictness as a model-originated
// delivery (AcceptProposal's add_stage path) — neither gets a "trusted because it's the API" bypass of the
// command-prepared check (T8.5).
func TestPolicy_DirectBackendRequestCheckedLikeModelAction(t *testing.T) {
	cmd := newSafeAgentCommand(t)
	o, _ := newProposalDeliveryTestOrchestrator(t, cmd, []flow.Stage{autoStage("impl", cmd)})
	ctx := context.Background()
	notPrepared := draftN("x", "/bin/not-a-prepared-command")

	// "Direct backend request": CommitAddStage called the same way the manual HTTP route calls it.
	_, directErr := o.CommitAddStage(ctx, AddStageDraft{Kind: AddStageKindAppend, RequestID: "direct", Stage: notPrepared})

	// "Model action": the identical draft, delivered through an assistant's accepted proposal.
	propID := seedAddStageProposal(ctx, t, o, "op-model", "impl", "x", "/bin/not-a-prepared-command")
	modelErr := o.AcceptProposal(ctx, propID, "accept-model", state.Actor{Kind: "user"})

	var directVE, modelVE *graph.ValidationError
	if !errors.As(directErr, &directVE) || directVE.Code != graph.CodeCommandNotPrepared {
		t.Fatalf("direct backend request: err=%v, want *graph.ValidationError{Code:command_not_prepared}", directErr)
	}
	if !errors.As(modelErr, &modelVE) || modelVE.Code != graph.CodeCommandNotPrepared {
		t.Fatalf("model-originated action: err=%v, want *graph.ValidationError{Code:command_not_prepared} (SAME check as the direct backend request)", modelErr)
	}
}

// TestPolicy_NoSelfElevation — a helper can NEVER get a WIDER max_depth, a different root_stage_id, or a
// less-restricted executor than what its parent/caller already has. max_depth/root_stage_id are proven
// impossible-by-construction (DelegateRequest has no field for either — TestPolicy_ChildCapabilitiesNeverWiderThanParent
// is the regression lock for that). The executor/profile axis is the one genuine gap this stage closes: an
// assistant delegating to ITS OWN task must not be able to pick a child profile with a WEAKER read-only
// guarantee than its own — but an explicit, already-confirmed human/side-agent choice is NOT self-elevation
// and remains allowed (same carve-out admitChildren's self-match check already documents).
func TestPolicy_NoSelfElevation(t *testing.T) {
	cmd := newSafeAgentCommand(t)
	o, store := newProposalDeliveryTestOrchestrator(t, cmd, []flow.Stage{{ID: assistantsTestStageIDForProposals, Command: "claude"}})
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageIDForProposals)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}
	parentID, err := o.Attach(ctx, AttachRequest{OperationID: "op-parent-elev", StageID: assistantsTestStageIDForProposals, ProfileID: "reviewer", Instruction: "x", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	rs := store.Snapshot()
	if rs.Assistants.Tasks[parentID].ProfileSnapshot.ReadonlyEnforcement != assistants.ReadonlyEnforcementOS {
		t.Fatalf("test setup: parent profile must be OS-enforced, got %s", rs.Assistants.Tasks[parentID].ProfileSnapshot.ReadonlyEnforcement)
	}

	weakerChild := []assistants.DelegateRequest{{ProfileID: "unrestricted", Instruction: "x"}}
	sameChild := []assistants.DelegateRequest{{ProfileID: "reviewer", Instruction: "x"}}

	// The assistant delegating to ITS OWN task may not downgrade to a less-restricted profile.
	_, err = o.admitChildren(ctx, "elev-self", parentID, weakerChild, state.Actor{Kind: "assistant", ID: string(parentID)}, "", nil)
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeExecutorNotPermitted {
		t.Fatalf("assistant self-delegation to a weaker-enforced profile: err=%v, want CodeExecutorNotPermitted", err)
	}

	// The SAME downgrade, chosen explicitly by a human, is not self-elevation — allowed.
	if _, err := o.admitChildren(ctx, "elev-user", parentID, weakerChild, state.Actor{Kind: "user", ID: "dashboard-operator"}, "", nil); err != nil {
		t.Fatalf("human-initiated child with a different profile must be allowed: %v", err)
	}

	// A lateral (equally-restricted) profile choice by the assistant itself is not a downgrade — allowed.
	if _, err := o.admitChildren(ctx, "elev-lateral", parentID, sameChild, state.Actor{Kind: "assistant", ID: string(parentID)}, "", nil); err != nil {
		t.Fatalf("assistant self-delegation to an EQUALLY-enforced profile must be allowed: %v", err)
	}
}
