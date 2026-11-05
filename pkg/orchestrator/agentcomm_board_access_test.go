package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/state"
)

func TestBoardAvailable(t *testing.T) {
	cases := []struct {
		name string
		mode assistants.CollaborationMode
		want bool
	}{
		{"zero value means team", "", true},
		{"team", assistants.CollaborationModeTeam, true},
		{"assistants", assistants.CollaborationModeAssistants, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := &Orchestrator{opts: Options{CollaborationMode: tc.mode}}
			if got := o.boardAvailable(); got != tc.want {
				t.Errorf("boardAvailable() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestErrBoardUnavailable(t *testing.T) {
	var rejected *state.OperationRejectedError
	if !errors.As(errBoardUnavailable(), &rejected) {
		t.Fatalf("errBoardUnavailable() is not *state.OperationRejectedError: %v", errBoardUnavailable())
	}
	if rejected.Code != assistants.CodeFeatureDisabled {
		t.Errorf("Code = %q, want %q", rejected.Code, assistants.CodeFeatureDisabled)
	}
	if want := "Board is unavailable in assistants mode"; rejected.Reason != want {
		t.Errorf("Reason = %q, want %q", rejected.Reason, want)
	}
}

// boardGateFixture is one run store driven by two orchestrators: team commits the Board history, assistants
// is the same run seen in assistants mode, which must refuse every Board operation without touching it.
type boardGateFixture struct {
	team, assistants *Orchestrator
	store            *state.Store
	helper           agentcomm.ParticipantID
	peer             agentcomm.ParticipantID
}

func newBoardGateFixture(t *testing.T) boardGateFixture {
	t.Helper()
	team, store := newBoardTaskTestOrchestrator(t)
	activateStage(t, team, assistantsTestStageID)
	helper, peer := attachBlockedHelpers(t, team, store)
	opts := team.opts
	opts.CollaborationMode = assistants.CollaborationModeAssistants
	return boardGateFixture{team: team, assistants: assembleAssistantsTestOrchestrator(t, opts), store: store, helper: helper, peer: peer}
}

// attachBlockedHelpers attaches two helpers behind one fake runner and returns once both are inside it: start_turn
// is committed before the runner is called, so no late helper write can move the run state under a test.
func attachBlockedHelpers(t *testing.T, o *Orchestrator, store *state.Store) (helper, peer agentcomm.ParticipantID) {
	t.Helper()
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		entered <- struct{}{}
		awaitRelease(t, release)
		return passResultOutcome(), nil
	}
	ids := []assistants.TaskID{
		attachTestTask(context.Background(), t, o, "gate-helper"),
		attachTestTask(context.Background(), t, o, "gate-peer"),
	}
	t.Cleanup(func() {
		close(release)
		for _, id := range ids {
			waitForTaskStatus(t, store, id, assistants.StatusCompleted, 2*time.Second)
		}
	})
	for range ids {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("a helper did not reach the blocked runner")
		}
	}
	return meshHelperParticipantID(ids[0]), meshHelperParticipantID(ids[1])
}

// frozenRun is the run state a refused Board operation must leave untouched, as JSON for a deep comparison.
func frozenRun(t *testing.T, store *state.Store) string {
	t.Helper()
	rs := store.Snapshot()
	raw, err := json.Marshal(struct {
		LastSeq    uint64
		Assistants state.AssistantsState
	}{rs.LastSeq, rs.Assistants})
	if err != nil {
		t.Fatalf("marshal run state: %v", err)
	}
	return string(raw)
}

func requireRunUnchanged(t *testing.T, store *state.Store, before string) {
	t.Helper()
	if after := frozenRun(t, store); after != before {
		t.Fatalf("refused Board operations changed the run state (%d bytes before, %d after)", len(before), len(after))
	}
}

func requireBoardUnavailable(t *testing.T, err error) {
	t.Helper()
	var rejected *state.OperationRejectedError
	if !errors.As(err, &rejected) || rejected.Code != assistants.CodeFeatureDisabled || rejected.Reason != "Board is unavailable in assistants mode" {
		t.Fatalf("error = %v, want a feature_disabled rejection \"Board is unavailable in assistants mode\"", err)
	}
}

// boardOperations lists every Board entry point of the orchestrator for one actor, each under its own request id.
func boardOperations(o *Orchestrator, actor agentcomm.ParticipantID, tag string) map[string]func() error {
	ctx := context.Background()
	deadline := time.Now().Add(time.Hour)
	return map[string]func() error{
		"ReadBoard all": func() error { _, err := o.ReadBoard(ctx, ReadBoardRequest{}); return err },
		"ReadBoard key": func() error { _, err := o.ReadBoard(ctx, ReadBoardRequest{Key: "notes"}); return err },
		"Publish": func() error {
			_, err := o.Publish(ctx, PublishRequest{OperationID: "pub-" + tag, Author: actor, Key: "notes-" + tag, Status: "ready"})
			return err
		},
		"BoardTasks all": func() error { _, err := o.BoardTasks(ctx, ""); return err },
		"BoardTasks key": func() error { _, err := o.BoardTasks(ctx, "task/gate"); return err },
		"BoardTask create": func() error {
			_, err := o.BoardTask(ctx, BoardTaskRequest{OperationID: "task-" + tag, Action: BoardTaskCreate, Actor: actor, Key: "task/new-" + tag, Title: new("New")})
			return err
		},
		"Wait board_key": func() error {
			_, err := o.Wait(ctx, WaitRequest{OperationID: "wait-" + tag, Waiter: actor, Deadline: &deadline, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnBoardKey, Key: "notes", Status: "ready"}})
			return err
		},
	}
}

func TestBoardOperationsRefusedInAssistantsMode(t *testing.T) {
	f := newBoardGateFixture(t)
	actors := map[string]agentcomm.ParticipantID{
		"user":      agentcomm.UserParticipantID,
		"stage":     meshStageParticipantID(assistantsTestStageID),
		"helper":    f.helper,
		"run agent": meshRunParticipantID("run-agent-task"),
		"assistant": f.assistants.SideAgentParticipantID(),
	}
	before := frozenRun(t, f.store)
	for actorName, actor := range actors {
		for opName, op := range boardOperations(f.assistants, actor, strings.ReplaceAll(actorName, " ", "-")) {
			t.Run(actorName+"/"+opName, func(t *testing.T) {
				requireBoardUnavailable(t, op())
			})
		}
	}
	requireRunUnchanged(t, f.store, before)
}

// A request id committed while the run was in team mode must not answer from its stored receipt in assistants mode.
func TestBoardRetryOfCommittedRequestRefusedInAssistantsMode(t *testing.T) {
	f := newBoardGateFixture(t)
	ctx := context.Background()
	author := meshStageParticipantID(assistantsTestStageID)

	publish := PublishRequest{OperationID: "committed-pub", Author: author, Key: "notes", Status: "ready"}
	if _, err := f.team.Publish(ctx, publish); err != nil {
		t.Fatalf("team Publish: %v", err)
	}
	create := BoardTaskRequest{OperationID: "committed-task", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: "task/gate", Title: new("Gate")}
	mustBoardTask(t, f.team, create)
	deadline := time.Now().Add(time.Hour)
	wait := WaitRequest{OperationID: "committed-wait", Waiter: agentcomm.UserParticipantID, Actor: state.Actor{Kind: "user"}, Deadline: &deadline, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnBoardKey, Key: "later", Status: "ready"}}
	if _, err := f.team.Wait(ctx, wait); err != nil {
		t.Fatalf("team Wait: %v", err)
	}
	before := frozenRun(t, f.store)

	_, err := f.assistants.Publish(ctx, publish)
	requireBoardUnavailable(t, err)
	_, err = f.assistants.BoardTask(ctx, create)
	requireBoardUnavailable(t, err)
	_, err = f.assistants.Wait(ctx, wait)
	requireBoardUnavailable(t, err)
	requireRunUnchanged(t, f.store, before)
}

// Only the board_key wait is refused; the other conditions register in assistants mode as before.
func TestNonBoardWaitsStillRegisterInAssistantsMode(t *testing.T) {
	f := newBoardGateFixture(t)
	ctx := context.Background()
	thread, err := f.team.OpenThread(ctx, OpenThreadRequest{OperationID: "gate-open", Members: []agentcomm.ParticipantID{f.helper, f.peer}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	sent, err := f.team.Send(ctx, SendRequest{OperationID: "gate-send", ThreadID: thread.ID, Sender: f.helper, Recipients: []agentcomm.ParticipantID{f.peer}, Kind: agentcomm.MessageKindRequest, Body: "ping"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	peerTask := assistants.TaskID(strings.TrimPrefix(string(f.peer), meshHelperPrefix))

	cases := []struct {
		name   string
		waiter agentcomm.ParticipantID
		cond   agentcomm.WaitCondition
	}{
		{"reply_to", f.helper, agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: sent.MessageID}},
		{"addressed_message", f.peer, agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread.ID}},
		{"task_result", f.helper, agentcomm.WaitCondition{Kind: agentcomm.WaitOnTaskResult, TaskID: string(peerTask)}},
		{"stage_status", agentcomm.UserParticipantID, agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: assistantsTestStageID, Status: string(state.StatusDone)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			receipt, err := f.assistants.Wait(ctx, WaitRequest{OperationID: "gate-wait-" + tc.name, Waiter: tc.waiter, Condition: tc.cond})
			if err != nil {
				t.Fatalf("Wait: %v", err)
			}
			if _, ok := f.store.Snapshot().Assistants.Mesh.Waits[receipt.WaitID]; !ok {
				t.Fatalf("wait %s was not registered", receipt.WaitID)
			}
		})
	}
}

// Without the mode (the zero value) and in team mode the same operations go through.
func TestBoardOperationsWorkInTeamMode(t *testing.T) {
	for _, mode := range []assistants.CollaborationMode{"", assistants.CollaborationModeTeam} {
		t.Run("mode="+string(mode), func(t *testing.T) {
			f := newBoardGateFixture(t)
			opts := f.team.opts
			opts.CollaborationMode = mode
			o := assembleAssistantsTestOrchestrator(t, opts)
			ctx := context.Background()
			deadline := time.Now().Add(time.Hour)

			if _, err := o.Publish(ctx, PublishRequest{OperationID: "team-pub", Author: meshStageParticipantID(assistantsTestStageID), Key: "notes", Status: "ready"}); err != nil {
				t.Fatalf("Publish: %v", err)
			}
			if page, err := o.ReadBoard(ctx, ReadBoardRequest{Key: "notes"}); err != nil || len(page.Entries) != 1 {
				t.Fatalf("ReadBoard = %+v, %v, want the published entry", page, err)
			}
			mustBoardTask(t, o, BoardTaskRequest{OperationID: "team-task", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: "task/gate", Title: new("Gate")})
			if views, err := o.BoardTasks(ctx, "task/gate"); err != nil || len(views) != 1 {
				t.Fatalf("BoardTasks = %+v, %v, want the created task", views, err)
			}
			receipt, err := o.Wait(ctx, WaitRequest{OperationID: "team-wait", Waiter: agentcomm.UserParticipantID, Actor: state.Actor{Kind: "user"}, Deadline: &deadline, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnBoardKey, Key: "later", Status: "ready"}})
			if err != nil || receipt.Status != agentcomm.WaitPending {
				t.Fatalf("Wait board_key = %+v, %v, want a pending wait", receipt, err)
			}
		})
	}
}
