package orchestrator

// Этап 10 (T10.5): the action bridge (CommitAddStage/Attach/admitChildren — the SAME functions
// proposal_delivery.go and the manual/side-agent HTTP paths all call) must never write into a closed store,
// and a run already winding down must reject a late action cleanly (run_finalizing), not panic or silently
// no-op. Ordinary conversational history/replay has its own, separate policy (unaffected here) — this file
// only covers the WRITE side.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// TestSideAgentActionAfterRunClosed_ReturnsRunFinalizing — once the run has started winding down
// (Store.SetRunFinalizing(true), the same flag orchestrator.go's own shutdown sequence sets BEFORE
// store.Close(), per AGENTS.md's "Clean shutdown"), an action proposed by an assistant and confirmed by a
// human — the SAME delivery path a side-agent action-draft confirm uses — must be rejected with
// CodeRunFinalizing, not a bare "store is closed" error and not a panic.
func TestSideAgentActionAfterRunClosed_ReturnsRunFinalizing(t *testing.T) {
	cmd := newSafeAgentCommand(t)
	o, store := newProposalDeliveryTestOrchestrator(t, cmd, []flow.Stage{autoStage("impl", cmd)})
	ctx := context.Background()

	addStageID := seedAddStageProposal(ctx, t, o, "op-finalize-add", "impl", "new-stage", cmd)
	attachPayload, _ := json.Marshal(AttachAssistantProposalPayload{ProfileID: "reviewer", Title: "t", Instruction: "x"})
	attachID, err := o.CreateProposal(ctx, CreateProposalRequest{
		OperationID: "op-finalize-attach", StageID: "impl", Type: assistants.ProposalAttachAssistant,
		Title: "attach a helper", Payload: attachPayload,
		Authors: []assistants.MessageAuthor{{Kind: assistants.AuthorAssistant, ID: "planner"}},
	})
	if err != nil {
		t.Fatalf("CreateProposal(attach_assistant): %v", err)
	}

	store.SetRunFinalizing(true)

	addErr := o.AcceptProposal(ctx, addStageID, "accept-finalize-add", state.Actor{Kind: "user"})
	var rej *state.OperationRejectedError
	if !errors.As(addErr, &rej) || rej.Code != assistants.CodeRunFinalizing {
		t.Fatalf("AcceptProposal(add_stage) after SetRunFinalizing: err=%v, want CodeRunFinalizing", addErr)
	}

	attachErr := o.AcceptProposal(ctx, attachID, "accept-finalize-attach", state.Actor{Kind: "user"})
	rej = nil
	if !errors.As(attachErr, &rej) || rej.Code != assistants.CodeRunFinalizing {
		t.Fatalf("AcceptProposal(attach_assistant) after SetRunFinalizing: err=%v, want CodeRunFinalizing", attachErr)
	}
}

// TestNoWriteAfterStoreClose — run with -race: the action bridge (admitChildren here, standing in for every
// write path proposal_delivery.go/the manual and side-agent HTTP routes share — this is the SAME function
// Этап 4a's internal delegation and this stage's attach_assistant-with-TaskID delivery both call) racing a
// real Store.Close() must never panic and must never report success for a write that landed AFTER Close
// returned — distinct from an ordinary HTTP request-context cancellation (that path is covered elsewhere;
// this one is specifically about the store's own lifecycle).
//
// admitChildren (not CommitAddStage) is the right write path to hammer concurrently here: CommitAddStage's
// own optimistic-concurrency precondition (graph_mutate.go's GraphRevision check, T9.4) means concurrent
// CommitAddStage calls LEGITIMATELY reject each other with stale_revision regardless of Close() — a real,
// already-tested behavior (TestCommit_SharedAdmissionSerialization) that would otherwise drown out the one
// signal this test cares about. Every child created below is independent (distinct operation ids, no shared
// per-call precondition besides the parent's own status, which nothing here changes), so the ONLY thing that
// can turn a concurrent admitChildren call into a rejection is Store.Close() itself.
func TestNoWriteAfterStoreClose(t *testing.T) {
	cmd := newSafeAgentCommand(t)
	o, store := newProposalDeliveryTestOrchestrator(t, cmd, []flow.Stage{{ID: assistantsTestStageIDForProposals, Command: "claude"}})
	ctx := context.Background()
	// admitChildren (unlike Attach) always queues+schedules a new child immediately regardless of the root
	// stage's own FSM status (see proposal_confirmation_test.go's sibling-task test for the same note) — a
	// fake runHelperAgent stands in for the real subprocess the never-started "reviewer" profile command
	// would otherwise be, for every one of the (up to 8) children this test admits.
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}
	// The stage itself is left PENDING — Attach leaves the parent task in "waiting_parent" without scheduling
	// anything, and nothing here changes its status concurrently with the race below.
	parentID, err := o.Attach(ctx, AttachRequest{OperationID: "op-race-parent", StageID: assistantsTestStageIDForProposals, ProfileID: "reviewer", Instruction: "x", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]error, 8)
	for i := range results {
		wg.Go(func() {
			<-start
			_, err := o.admitChildren(ctx, fmt.Sprintf("race-child-%d", i), parentID, oneChild(), state.Actor{Kind: "user"}, "", nil)
			results[i] = err
		})
	}
	wg.Go(func() {
		<-start
		_ = store.Close()
	})
	close(start)
	wg.Wait()

	// Every outcome must be either a genuine success OR one of the two clean, typed closed/finalizing
	// rejections ApplyRunOperation can return — never anything else (a decode panic, a nil-pointer dereference,
	// or an unrecognized error would indicate the write path touched something outside s.mu's protection).
	for i, err := range results {
		if err == nil {
			continue
		}
		var rej *state.OperationRejectedError
		if errors.As(err, &rej) && rej.Code == assistants.CodeRunFinalizing {
			continue
		}
		if errors.Is(err, state.ErrStoreClosed) {
			continue
		}
		t.Fatalf("result[%d] = %v, want success, CodeRunFinalizing, or state.ErrStoreClosed", i, err)
	}
}
