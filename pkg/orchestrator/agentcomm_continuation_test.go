package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// A continuation turn of a non-interactive stage is a fresh agent session: what earlier turns asked and got back
// reaches it only through the prompt. Live run (examples/agent-team-review): every continuation started the
// review procedure over and asked both reviewers again, eight rounds of "no findings".

// readInbox is what `afm agent inbox` does for the stage: hand over every new message.
func readInbox(t *testing.T, o *Orchestrator, reader agentcomm.ParticipantID) {
	t.Helper()
	page, err := o.ReadInbox(context.Background(), ReadInboxRequest{Participant: reader, OnlyNew: true})
	if err != nil {
		t.Fatalf("ReadInbox: %v", err)
	}
	for _, m := range page.Messages {
		if err := o.MarkMessageDelivered(context.Background(), m.ID, reader); err != nil {
			t.Fatalf("MarkMessageDelivered: %v", err)
		}
	}
}

// The live scenario: the stage asks two reviewers and yields; the first answer resumes it; the second answer
// arrives during that continuation and the agent reads it there. The stage must finish after that turn — and the
// continuation's prompt must list both requests: one answered, one still waiting.
func TestPeerContinuation_SecondAnswerReadInTheContinuationFinishes(t *testing.T) {
	runner := &scriptedStageRunner{}
	o, store := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
	releases := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var calls atomic.Int32
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		if n := calls.Add(1); int(n) <= len(releases) {
			awaitRelease(t, releases[n-1])
		}
		return passResultOutcome(), nil
	}
	members := []agentcomm.ParticipantID{unreadTestStage}
	var revs []agentcomm.ParticipantID
	for i := range 2 {
		res, err := o.AddRunAgent(context.Background(), AddRunAgentRequest{
			OperationID: fmt.Sprintf("op-reviewer-%d", i), ProfileID: "qa", Title: fmt.Sprintf("Reviewer %d", i), Actor: state.Actor{Kind: "user"},
		})
		if err != nil {
			t.Fatalf("AddRunAgent: %v", err)
		}
		revs = append(revs, res.ParticipantID)
		members = append(members, res.ParticipantID)
	}
	th, err := o.OpenThread(context.Background(), OpenThreadRequest{OperationID: "op-review-thread", Members: members, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	var waits []agentcomm.WaitID
	runner.turn = func(n int32) error {
		switch n {
		case 1:
			waits = askReviewers(t, o, unreadTestStage, th.ID, revs)
			// The turn's own stream log, as the executor writes it: what a fresh session can be told it did.
			line := `{"type":"assistant","message":{"content":[{"type":"text","text":"Asked both reviewers."}]}}` + "\n"
			if err := os.WriteFile(filepath.Join(o.stageDir(unreadTestStageID), flow.PhaseJSONL(flow.PhaseAutonomous)), []byte(line), 0o644); err != nil {
				t.Fatal(err)
			}
			close(releases[0])
		case 2:
			close(releases[1])
			waitUntilReady(t, store, waits...)
			readInbox(t, o, unreadTestStage)
		default:
		}
		return nil
	}

	runToEnd(t, o)

	if got := runner.turns.Load(); got != 2 {
		t.Fatalf("stage turns = %d, want 2 (the asking turn and one continuation)", got)
	}
	if got := store.Get(unreadTestStageID); got != state.StatusDone {
		t.Fatalf("stage status = %s, want done", got)
	}
	prompt := runner.prompt(2)
	_, block, found := strings.Cut(prompt, "<your_requests>")
	if !found || !strings.Contains(block, "still waiting") || !strings.Contains(block, "looks fine") {
		t.Fatalf("continuation prompt must list both requests, one answered and one still waiting:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Previously completed actions") || !strings.Contains(prompt, "Asked both reviewers.") {
		t.Fatalf("a fresh continuation session must get the earlier turns' actions:\n%s", prompt)
	}
}
