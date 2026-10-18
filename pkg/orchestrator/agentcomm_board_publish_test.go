package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

func publishAsUser(o *Orchestrator, opID, key, status string, expectedVersion int, notify ...agentcomm.ParticipantID) (agentcomm.BoardEntry, error) {
	return o.Publish(context.Background(), PublishRequest{
		OperationID: opID, Author: meshUserParticipantID, Key: key, Status: status, ExpectedVersion: expectedVersion,
		Notify: notify, Actor: state.Actor{Kind: "user"},
	})
}

// A board task with notify reaches an idle run agent: one message from the human starts its turn, and a
// retried request does not send a second one.
func TestPublish_NotifyWakesIdleRunAgent(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	var mu sync.Mutex
	var prompts []string
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		mu.Lock()
		defer mu.Unlock()
		prompts = append(prompts, prompt)
		return passResultOutcome(), nil
	}
	res := addTestRunAgent(t, o, "op-add", nil)

	for range 2 { // the second call is a retry of the same request
		if _, err := publishAsUser(o, "op-task", "task/csv-export", "CSV export", 0, res.ParticipantID); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
	waitForRunAgentTurn(t, store, res.TaskID, 2*time.Second)

	inbox := store.Snapshot().Assistants.Mesh.Inbox[res.ParticipantID]
	if len(inbox) != 1 {
		t.Fatalf("messages to the agent = %d, want exactly 1", len(inbox))
	}
	want := "New task on the board: task/csv-export — CSV export. Report under task/csv-export/<your alias>."
	if body := store.Snapshot().Assistants.Mesh.Messages[inbox[0]].Body; body != want {
		t.Fatalf("notification = %q, want %q", body, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "task/csv-export") {
		t.Fatalf("agent turns = %d, want one turn that sees the task", len(prompts))
	}
}

// Publish answers with the revision its own operation committed — also on a replay, after a newer revision.
func TestPublish_ReplayReturnsItsOwnRevision(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	first, err := publishAsUser(o, "op-v1", "decision", "draft", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publishAsUser(o, "op-v2", "decision", "final", 1); err != nil {
		t.Fatal(err)
	}
	replay, err := publishAsUser(o, "op-v1", "decision", "draft", 0)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.Version != 1 || replay.Status != "draft" || replay.Seq != first.Seq || !replay.Ts.Equal(first.Ts) {
		t.Fatalf("replay = %+v, want the first revision %+v", replay, first)
	}
}

// A board status is redacted like a message body: in the journal, the response and the notice.
func TestPublish_RedactsSecretsInStatus(t *testing.T) {
	const secret = "sk-test-board-secret-424242" //nolint:gosec // test fixture value, not a real credential
	t.Setenv("ANTHROPIC_API_KEY", secret)
	o, store := newAssistantsTestOrchestrator(t, true)
	_, events := o.ui.Subscribe(16)

	entry, err := publishAsUser(o, "op-secret", "creds", "token is "+secret, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(entry.Status, secret) || strings.Contains(store.Snapshot().Assistants.Mesh.Board["creds"].Status, secret) {
		t.Fatalf("secret leaked into the board entry: %q", entry.Status)
	}
	select {
	case ev := <-events:
		data, _ := ev.Data.(map[string]any)
		if s, _ := data["status"].(string); strings.Contains(s, secret) {
			t.Fatalf("secret leaked into the notice: %q", s)
		}
	case <-time.After(time.Second):
		t.Fatal("no board notice published")
	}
}

// TestPublish_NotifyNeverFailsThePublish: once a task is on the board, telling people about it cannot undo
// or fail the publish. A finished stage or an unknown name is skipped, a long status is quoted only in part,
// and every reachable recipient gets its own message.
func TestPublish_NotifyNeverFailsThePublish(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	stubPassingHelper(o)
	t.Cleanup(o.concurrency.WaitAgents) // the notified agents take a turn; let it end before TempDir cleanup
	activateStage(t, o, assistantsTestStageID)
	if _, ok := o.Trigger(assistantsTestStageID, bus.EvComplete, bus.GuardCtx{}, ""); !ok {
		t.Fatal("EvComplete failed")
	}
	first := addTestRunAgent(t, o, "op-add-1", nil)
	second := addTestRunAgent(t, o, "op-add-2", nil)
	status := strings.Repeat("long task description ", 1000) // ~22 KB, more than one message may carry

	entry, err := publishAsUser(o, "op-task", "task/big", status, 0,
		meshStageParticipantID(assistantsTestStageID), "nobody-by-this-name", first.ParticipantID, second.ParticipantID)
	if err != nil {
		t.Fatalf("Publish with unreachable and unknown recipients: %v", err)
	}
	if entry.Version != 1 || store.Snapshot().Assistants.Mesh.Board["task/big"] == nil {
		t.Fatalf("the task was not published: %+v", entry)
	}

	rs := store.Snapshot()
	for _, p := range []agentcomm.ParticipantID{first.ParticipantID, second.ParticipantID} {
		inbox := rs.Assistants.Mesh.Inbox[p]
		if len(inbox) != 1 {
			t.Fatalf("%s got %d messages, want its own notice", p, len(inbox))
		}
		if m := rs.Assistants.Mesh.Messages[inbox[0]]; len(m.Recipients) != 1 || !strings.HasPrefix(m.Body, "New task on the board: task/big") {
			t.Fatalf("notice to %s = %+v, want one message to it alone", p, m)
		}
	}
	if got := rs.Assistants.Mesh.Inbox[meshStageParticipantID(assistantsTestStageID)]; len(got) != 0 {
		t.Fatalf("the finished stage got %d messages, want none", len(got))
	}
}

// TestPublish_NoticeIsCommittedWithTheRevision: the notice is part of the publish's own operation, so a run
// that finishes right after it sees the notice unread and keeps going — the revision and its notices are
// never separated.
func TestPublish_NoticeIsCommittedWithTheRevision(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		<-release
		return passResultOutcome(), nil
	}
	t.Cleanup(func() { close(release); o.concurrency.WaitAgents() })
	activateStage(t, o, assistantsTestStageID)
	if _, ok := o.Trigger(assistantsTestStageID, bus.EvComplete, bus.GuardCtx{}, ""); !ok {
		t.Fatal("EvComplete failed")
	}
	agent := addTestRunAgent(t, o, "op-add", nil)

	if _, err := publishAsUser(o, "op-task", "task/report", "write the report", 0, agent.ParticipantID); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	op, committed, err := store.CommittedOperation("op-task")
	if err != nil || !committed {
		t.Fatalf("CommittedOperation: committed=%v err=%v", committed, err)
	}
	var board state.MeshBoardPublishedPayload
	var msg state.MeshMessageSentPayload
	_, hasBoard, _ := op.Effect("mesh_board_published", &board)
	_, hasMsg, _ := op.Effect("mesh_message_sent", &msg)
	if !hasBoard || !hasMsg || len(msg.Recipients) != 1 || msg.Recipients[0] != string(agent.ParticipantID) {
		t.Fatalf("publish operation effects = %+v, want the revision and the notice to %s together", op.Effects, agent.ParticipantID)
	}
	rs := store.Snapshot()
	if o.runStateAllowsExit(&rs) {
		t.Fatal("the run may finish with the notice unread, want it kept open until the agent reads it")
	}
}

// TestPublish_RetryWithAnotherNotifyListConflicts: a retry is compared with the committed request in full,
// the notify list included.
func TestPublish_RetryWithAnotherNotifyListConflicts(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	if _, err := publishAsUser(o, "op-task", "task/x", "do x", 0, "nobody"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if _, err := publishAsUser(o, "op-task", "task/x", "do x", 0); !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("retry without the notify list: err = %v, want a request id conflict", err)
	}
}

// TestPublish_LegacyRecordWithoutNotifyAnswersRetry: a publish journaled before the notify list was stored has
// no list to compare with — its retry, notify list included, is answered from that record, not a conflict.
func TestPublish_LegacyRecordWithoutNotifyAnswersRetry(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	legacy, err := marshalEffect("mesh_board_published",
		state.Target{BoardKey: "task/legacy", ParticipantID: string(meshUserParticipantID)},
		map[string]any{"status": "do the legacy thing", "expected_version": 0})
	if err != nil {
		t.Fatal(err)
	}
	committed, err := store.ApplyRunOperation(state.Operation{ID: "op-legacy", Kind: "mesh_publish", Actor: state.Actor{Kind: "user"}, Effects: []state.Effect{legacy}}, nil)
	if err != nil {
		t.Fatalf("commit legacy publish: %v", err)
	}

	entry, err := publishAsUser(o, "op-legacy", "task/legacy", "do the legacy thing", 0, "someone")
	if err != nil {
		t.Fatalf("retry of a legacy publish with a notify list: %v", err)
	}
	if entry.Version != 1 || entry.Seq != committed.Seq || entry.Status != "do the legacy thing" {
		t.Fatalf("retry = %+v, want the legacy revision (seq %d)", entry, committed.Seq)
	}
	if _, err := publishAsUser(o, "op-legacy", "task/legacy", "something else", 0, "someone"); !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("retry with another status: err = %v, want a request id conflict", err)
	}
}

// TestPublish_NotifyListIsCapped: one publish commits its notices in its own journal record, so the notify list
// is capped — an oversized one is an invalid request, and the largest allowed publish still answers its retry
// from the journal.
func TestPublish_NotifyListIsCapped(t *testing.T) {
	state.SetOperationResultCacheCapacityForTest(1) // a retry must read the record back from the journal
	t.Cleanup(func() { state.SetOperationResultCacheCapacityForTest(0) })
	o, store := newAssistantsTestOrchestrator(t, true)
	stubPassingHelper(o)
	t.Cleanup(o.concurrency.WaitAgents)

	tooMany := make([]agentcomm.ParticipantID, agentcomm.MaxBoardNotify+1)
	for i := range tooMany {
		tooMany[i] = agentcomm.ParticipantID(fmt.Sprintf("agent-%d", i))
	}
	_, err := publishAsUser(o, "op-too-many", "task/x", "do x", 0, tooMany...)
	var rejected *state.OperationRejectedError
	if !errors.As(err, &rejected) || rejected.Code != assistants.CodeInvalidRequest {
		t.Fatalf("publish notifying %d participants: err = %v, want invalid_request", len(tooMany), err)
	}

	notify := make([]agentcomm.ParticipantID, agentcomm.MaxBoardNotify)
	for i := range notify {
		notify[i] = addTestRunAgent(t, o, fmt.Sprintf("op-add-%d", i), nil).ParticipantID
	}
	// Every byte of key and status escapes to six in the journal's JSON: the worst case for the record size.
	key := strings.Repeat("<", 8*1024)
	status := strings.Repeat("<", 20*1024)
	first, err := publishAsUser(o, "op-big", key, status, 0, notify...)
	if err != nil {
		t.Fatalf("publish notifying %d participants: %v", len(notify), err)
	}
	if _, err := publishAsUser(o, "op-other", "task/other", "evicts op-big from the cache", 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CommittedOperation("op-big"); err != nil {
		t.Fatalf("read the largest allowed publish back from the journal: %v", err)
	}
	retry, err := publishAsUser(o, "op-big", key, status, 0, notify...)
	if err != nil || retry.Seq != first.Seq {
		t.Fatalf("retry = %+v, %v; want the committed revision (seq %d)", retry, err, first.Seq)
	}
	if size := largestJournalLine(t, o.opts.RunDir); size > 512*1024 {
		t.Fatalf("largest journal record = %d bytes, want a clear margin below the 1 MiB read limit", size)
	}
}

func largestJournalLine(t *testing.T, runDir string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	largest := 0
	for _, line := range bytes.Split(raw, []byte("\n")) {
		largest = max(largest, len(line))
	}
	return largest
}
