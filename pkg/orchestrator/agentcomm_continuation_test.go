package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
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

// A restart between the resume out of awaiting_peer and the continuation agent's start: the restarted turn is
// still a continuation and gets the earlier turns' actions — the journal says so, not process memory.
func TestPeerContinuation_CrashBeforeTheAgentStartedKeepsTheContext(t *testing.T) {
	runDir := t.TempDir()
	o, store := newUnreadTestOrchestratorAt(t, runDir, flow.AgentAuto, &scriptedStageRunner{turn: func(int32) error { return nil }})
	thread, question := yieldedStage(t, o)
	epoch := store.Snapshot().Stages[unreadTestStageID].Epoch
	o.parkForPeers(context.Background(), unreadTestStageID, phaseAutonomous, epoch, o.turnEpisode(unreadTestStage, nil), func() { t.Error("no revise here") })
	answerAsUser(t, o, thread, question)
	park := store.Snapshot().Stages[unreadTestStageID].Seq
	if _, ok := o.Trigger(unreadTestStageID, bus.EvPeerReplied, bus.GuardCtx{Phase: phaseAutonomous, ExpectStageSeq: &park}, ""); !ok {
		t.Fatal("the resume transition did not commit")
	}
	stageDir := filepath.Join(runDir, unreadTestStageID)
	for _, name := range []string{".done", "execution_summary.md"} {
		if err := os.Remove(filepath.Join(stageDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"Asked the human."}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(stageDir, flow.PhaseJSONL(flow.PhaseAutonomous)), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	runner := &scriptedStageRunner{turn: func(int32) error { return nil }}
	restartAndRecover(t, runDir, runner)
	deadline := time.Now().Add(5 * time.Second)
	for runner.turns.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no turn after recovery")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if prompt := runner.prompt(1); !strings.Contains(prompt, "Previously completed actions") || !strings.Contains(prompt, "Asked the human.") {
		t.Fatalf("the recovered continuation must get the earlier turns' actions:\n%s", prompt)
	}
}

// requestsRoster is the roster the requests of requestsMesh are labelled from.
var requestsRoster = []agentcomm.Participant{{ID: "run:qa", Alias: "qa"}}

// requestsMesh is a mesh in which the stage sent n review requests (seq 1..n, ids mm-1..mm-n) to qa.
func requestsMesh(n int) state.MeshState {
	mesh := state.MeshState{
		Participants: map[agentcomm.ParticipantID]*agentcomm.Participant{"run:qa": {ID: "run:qa", Alias: "qa"}},
		Messages:     map[agentcomm.MessageID]*agentcomm.Message{},
		Waits:        map[agentcomm.WaitID]*state.MeshWait{},
	}
	for i := 1; i <= n; i++ {
		id := agentcomm.MessageID(fmt.Sprintf("mm-%d", i))
		mesh.Messages[id] = &agentcomm.Message{
			ID: id, Sender: unreadTestStage, Recipients: []agentcomm.ParticipantID{"run:qa"}, Kind: agentcomm.MessageKindRequest,
			Body: "please review", Seq: uint64(i),
		}
	}
	return mesh
}

func addReply(mesh state.MeshState, id string, to agentcomm.MessageID, seq uint64, body string) {
	mesh.Messages[agentcomm.MessageID(id)] = &agentcomm.Message{
		ID: agentcomm.MessageID(id), Sender: "run:qa", Recipients: []agentcomm.ParticipantID{unreadTestStage},
		Kind: agentcomm.MessageKindReply, InReplyTo: &to, Body: body, Seq: seq,
	}
}

func addWait(mesh state.MeshState, id string, on agentcomm.MessageID, status agentcomm.WaitStatus, reason string) {
	mesh.Waits[agentcomm.WaitID(id)] = &state.MeshWait{Wait: agentcomm.Wait{
		ID: agentcomm.WaitID(id), Waiter: unreadTestStage, Status: status, Reason: reason,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: on},
	}}
}

// An old request whose wait is still open stays in the block behind any number of newer answered ones, and the
// block says how many requests it left out.
func TestRequestsBlock_KeepsOpenRequestsFirst(t *testing.T) {
	mesh := requestsMesh(maxListedRequests + 6)
	addWait(mesh, "mw-old", "mm-1", agentcomm.WaitPending, "")
	for i := 2; i <= maxListedRequests+6; i++ {
		addReply(mesh, fmt.Sprintf("mm-r%d", i), agentcomm.MessageID(fmt.Sprintf("mm-%d", i)), uint64(100+i), "no findings")
	}
	block := agentCommRequestsBlock(mesh, requestsRoster, unreadTestStage)
	if !strings.Contains(block, "[mm-1] to qa") || !strings.Contains(block, "still waiting") {
		t.Fatalf("the open request was evicted:\n%s", block)
	}
	if strings.Contains(block, "[mm-2] to qa") || !strings.Contains(block, "6 more requests") {
		t.Fatalf("want the oldest answered requests left out and counted:\n%s", block)
	}
}

// Many long answers stay within the block's total budget; the answers left out are counted.
func TestRequestsBlock_StaysWithinItsBudget(t *testing.T) {
	mesh := requestsMesh(maxListedRequests)
	long := strings.Repeat("x", 4*answerExcerptBytes)
	seq := uint64(1000)
	for i := 1; i <= maxListedRequests; i++ {
		for j := range 5 {
			seq++
			addReply(mesh, fmt.Sprintf("mm-r%d-%d", i, j), agentcomm.MessageID(fmt.Sprintf("mm-%d", i)), seq, long)
		}
	}
	block := agentCommRequestsBlock(mesh, requestsRoster, unreadTestStage)
	if len(block) > requestsBlockBudget+2048 {
		t.Fatalf("block is %d bytes, budget %d", len(block), requestsBlockBudget)
	}
	if !strings.Contains(block, "more answers are not shown") {
		t.Fatalf("the left-out answers must be counted:\n%.2000s", block)
	}
}

// A wait that ended without an answer (cancelled, expired) shows its status and reason.
func TestRequestsBlock_ShowsAWaitThatEnded(t *testing.T) {
	mesh := requestsMesh(1)
	addWait(mesh, "mw-1", "mm-1", agentcomm.WaitCancelled, "no recipient of mm-1 can answer it")
	block := agentCommRequestsBlock(mesh, requestsRoster, unreadTestStage)
	if !strings.Contains(block, "cancelled: no recipient of mm-1 can answer it") || strings.Contains(block, "no wait registered") {
		t.Fatalf("an ended wait must show its status and reason:\n%s", block)
	}
}

// An ended wait's reason is shown cut to its start, and what is shown counts against the block's budget: a
// long cancellation reason on every request cannot push the block past it.
func TestRequestsBlock_EndedWaitReasonIsBoundedAndCharged(t *testing.T) {
	const requests = maxListedRequests
	mesh := requestsMesh(requests)
	long := strings.Repeat("r", 4<<10)
	for i := 1; i <= requests; i++ {
		addWait(mesh, fmt.Sprintf("mw-%d", i), agentcomm.MessageID(fmt.Sprintf("mm-%d", i)), agentcomm.WaitCancelled, long)
	}
	block := agentCommRequestsBlock(mesh, requestsRoster, unreadTestStage)
	if strings.Contains(block, strings.Repeat("r", waitReasonExcerptBytes+1)) {
		t.Fatalf("an ended wait's reason must be cut to %d bytes:\n%.1000s", waitReasonExcerptBytes, block)
	}
	if !strings.Contains(block, "cancelled: rrr") {
		t.Fatalf("the ended wait must still show its status and the start of its reason:\n%.1000s", block)
	}
	if len(block) > requestsBlockBudget+2048 {
		t.Fatalf("block is %d bytes, budget %d", len(block), requestsBlockBudget)
	}
	view := requestView(requestsRoster, sentRequests(mesh, unreadTestStage)[0])
	if got, want := requestViewCost(view), len(view.Body)+len(view.WaitEnded)+requestEntryOverhead; got != want {
		t.Fatalf("an entry's cost = %d, want its body, its ended-wait text and the overhead (%d)", got, want)
	}
}
