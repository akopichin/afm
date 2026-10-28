package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

const activityNoticeType = "assistant_activity"

// activityOf is the OnAction a production helper run of taskID would get.
func activityOf(t *testing.T, o *Orchestrator, taskID assistants.TaskID) func(tool, detail string) {
	t.Helper()
	cfg := o.helperExecutorConfig(taskID, "helper-cmd")
	if cfg.OnAction == nil {
		t.Fatal("helperExecutorConfig must wire OnAction to the activity publisher")
	}
	return cfg.OnAction
}

func TestAssistantActivity_PublishesRootStageParticipantTurnAndIdentity(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	release := make(chan struct{})
	defer func() {
		close(release)
		o.concurrency.WaitAgents()
	}()
	taskID := runningParentTask(t, o, store, release)
	waitUntil(t, "the turn to be recorded", func() bool { return store.Snapshot().Assistants.Tasks[taskID].Turn == 1 })

	_, sub := o.ui.Subscribe(16)
	start := time.Now().UTC().Add(-time.Second)
	activityOf(t, o, taskID)("text", "looking at the diff")

	got := noticesOf(t, o, activityNoticeType)
	if len(got) != 1 {
		t.Fatalf("activity notices = %d, want 1: %v", len(got), got)
	}
	d := got[0]
	if d["_stage"] != assistantsTestStageID {
		t.Errorf("stage_id = %v, want the task's root stage", d["_stage"])
	}
	if d["task_id"] != string(taskID) || d["participant_id"] != string(o.taskMeshParticipant(taskID)) {
		t.Errorf("task/participant = %v/%v", d["task_id"], d["participant_id"])
	}
	if d["tool"] != "text" || d["detail"] != "looking at the diff" {
		t.Errorf("tool/detail = %v/%v", d["tool"], d["detail"])
	}
	if turn, _ := d["turn"].(float64); turn != 1 {
		t.Errorf("turn = %v, want 1 (captured from the running turn)", d["turn"])
	}
	if id, _ := d["event_id"].(string); id == "" {
		t.Errorf("event_id is empty: %v", d)
	}
	at, err := time.Parse(time.RFC3339Nano, d["occurred_at"].(string))
	if err != nil || at.Location() != time.UTC || at.Before(start) {
		t.Errorf("occurred_at = %v (%v), want a UTC time", d["occurred_at"], err)
	}
	if _, has := d["truncated"]; has {
		t.Errorf("a short detail must not carry truncated: %v", d)
	}

	// The live delivery is the same payload — same event_id — as the saved one.
	select {
	case ev := <-sub:
		live, _ := ev.Data.(stagefiles.ActivityData)
		if ev.Type != bus.EventAssistantActivity || ev.StageID != assistantsTestStageID || live.EventID != d["event_id"] {
			t.Errorf("live event = %+v, want the saved payload", ev)
		}
	default:
		t.Fatal("no live delivery")
	}
}

func TestAssistantActivity_IdenticalCallsKeepDistinctIDsInOrder(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	taskID := attachTestTask(context.Background(), t, o, "op-dup")
	emit := activityOf(t, o, taskID)
	emit("Bash", "ls")
	emit("Bash", "ls")
	emit("text", "done")

	got := noticesOf(t, o, activityNoticeType)
	if len(got) != 3 {
		t.Fatalf("activity notices = %d, want 3 (identical calls are not deduped): %v", len(got), got)
	}
	if got[0]["event_id"] == got[1]["event_id"] || got[1]["event_id"] == got[2]["event_id"] {
		t.Errorf("event ids repeat: %v", got)
	}
	if got[2]["tool"] != "text" {
		t.Errorf("file order lost: %v", got)
	}
}

func TestAssistantActivity_ParallelTasksAreNotMixed(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	a := attachTestTask(context.Background(), t, o, "op-a")
	b := attachTestTask(context.Background(), t, o, "op-b")
	const perTask = 40

	var wg sync.WaitGroup
	for id, name := range map[assistants.TaskID]string{a: "a", b: "b"} {
		emit := activityOf(t, o, id)
		wg.Go(func() {
			for i := range perTask {
				emit("text", fmt.Sprintf("%s-%d", name, i))
			}
		})
	}
	wg.Wait()

	perTaskCount := map[any]int{}
	for _, d := range noticesOf(t, o, activityNoticeType) {
		want := map[string]assistants.TaskID{"a": a, "b": b}[strings.SplitN(d["detail"].(string), "-", 2)[0]]
		if d["task_id"] != string(want) {
			t.Errorf("%v published under task %v, want %v", d["detail"], d["task_id"], want)
		}
		perTaskCount[d["task_id"]]++
	}
	if perTaskCount[string(a)] != perTask || perTaskCount[string(b)] != perTask {
		t.Errorf("notices per task = %v, want %d each", perTaskCount, perTask)
	}
}

func TestAssistantActivity_DelegatedChildUsesRootStageOfItsParent(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	release := make(chan struct{})
	defer func() {
		close(release)
		o.concurrency.WaitAgents()
	}()
	parent := runningParentTask(t, o, store, release)
	children, err := o.admitChildren(context.Background(), "op-child", parent, oneChild(), state.Actor{Kind: "assistant", ID: string(parent)}, "", nil)
	if err != nil {
		t.Fatalf("admitChildren: %v", err)
	}
	activityOf(t, o, children[0])("text", "child speaking")

	got := noticesOf(t, o, activityNoticeType)
	if len(got) != 1 || got[0]["task_id"] != string(children[0]) || got[0]["_stage"] != assistantsTestStageID {
		t.Fatalf("child activity = %v, want task %s under stage %s", got, children[0], assistantsTestStageID)
	}
}

func TestAssistantActivity_RunScopedTaskHasEmptyStage(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	res := addTestRunAgent(t, o, "op-run", nil)
	activityOf(t, o, res.TaskID)("text", "standing role")

	got := noticesOf(t, o, activityNoticeType)
	if len(got) != 1 || got[0]["_stage"] != "" || got[0]["task_id"] != string(res.TaskID) {
		t.Fatalf("run-scoped activity = %v, want task %s with an empty stage_id", got, res.TaskID)
	}
}

func TestAssistantActivity_TurnIncreasesAcrossContinuedTask(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		o.helperExecutorConfig(assistants.TaskID(taskID), cmd).OnAction("text", "turn output")
		return passResultOutcome(), nil
	}
	res := addTestRunAgent(t, o, "op-turns", nil)
	for i, id := range []string{"op-go-1", "op-go-2"} {
		if _, err := o.Send(context.Background(), SendRequest{
			OperationID: id, ThreadID: res.ThreadID, Sender: meshUserParticipantID,
			Recipients: []agentcomm.ParticipantID{res.ParticipantID}, Kind: agentcomm.MessageKindMessage, Body: "go", Actor: state.Actor{Kind: "user"},
		}); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
		waitUntil(t, "the turn to finish", func() bool { return len(noticesOf(t, o, activityNoticeType)) == i+1 })
		waitForRunAgentTurn(t, store, res.TaskID, 2*time.Second)
	}
	got := noticesOf(t, o, activityNoticeType)
	if got[0]["turn"] != float64(1) || got[1]["turn"] != float64(2) || got[0]["task_id"] != got[1]["task_id"] {
		t.Fatalf("turns of one task = %v, %v, want 1 then 2 under the same task", got[0]["turn"], got[1]["turn"])
	}
}

func TestAssistantActivity_CapsDetailAtRuneBoundary(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	taskID := attachTestTask(context.Background(), t, o, "op-cap")
	const limit = 16 * 1024
	// "я" is two bytes, so an odd limit lands inside a rune after the leading ASCII byte.
	detail := "x" + strings.Repeat("я", limit)
	activityOf(t, o, taskID)("Bash", detail)

	got := noticesOf(t, o, activityNoticeType)[0]
	capped, _ := got["detail"].(string)
	if len(capped) > limit || !utf8.ValidString(capped) || !strings.HasPrefix(detail, capped) {
		t.Fatalf("detail: len=%d valid=%v — want a valid UTF-8 prefix of at most %d bytes", len(capped), utf8.ValidString(capped), limit)
	}
	if truncated, _ := got["truncated"].(bool); !truncated {
		t.Errorf("truncated = %v, want true", got["truncated"])
	}
	if len(capped) < limit-utf8.UTFMax {
		t.Errorf("detail cut too early: %d bytes", len(capped))
	}
}

func TestCapUTF8_Boundaries(t *testing.T) {
	tests := []struct {
		name          string
		in            string
		limit         int
		want          string
		wantTruncated bool
	}{
		{"shorter than limit", "abc", 4, "abc", false},
		{"exactly limit", "abcd", 4, "abcd", false},
		{"limit plus one", "abcde", 4, "abcd", true},
		{"cut lands inside a rune", "abя", 3, "ab", true},
		{"rune ends exactly at limit", "abя", 4, "abя", false},
		{"rune straddles limit plus one", "abяc", 4, "abя", true},
		{"nothing fits", "я", 1, "", true},
		{"empty", "", 0, "", false},
	}
	for _, tc := range tests {
		got, truncated := capUTF8(tc.in, tc.limit)
		if got != tc.want || truncated != tc.wantTruncated {
			t.Errorf("%s: capUTF8(%q, %d) = %q, %v; want %q, %v", tc.name, tc.in, tc.limit, got, truncated, tc.want, tc.wantTruncated)
		}
	}
}

// The tool name comes from agent output uncapped; a huge one must not make a saved line longer than the
// reader accepts, or the action disappears after a reload.
func TestAssistantActivity_CapsToolSoTheSavedEntryReadsBack(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	taskID := attachTestTask(context.Background(), t, o, "op-tool")
	hugeTool := "я" + strings.Repeat("<", 300<<10)
	activityOf(t, o, taskID)(hugeTool, "ls")

	page, err := stagefiles.ReadTaskActivity(o.opts.RunDir, string(taskID), stagefiles.ActivityQuery{})
	if err != nil || len(page.Entries) != 1 {
		t.Fatalf("history after reload = %d entries, err %v; want the action kept", len(page.Entries), err)
	}
	tool := page.Entries[0].Tool
	if len(tool) > stagefiles.MaxActivityToolBytes || !utf8.ValidString(tool) || !strings.HasPrefix(hugeTool, tool) || tool == "" {
		t.Errorf("tool: len=%d valid=%v — want a non-empty valid UTF-8 prefix of at most %d bytes", len(tool), utf8.ValidString(tool), stagefiles.MaxActivityToolBytes)
	}
	if !page.Entries[0].Truncated {
		t.Error("a capped tool must mark the entry truncated")
	}
}

// OnAction content is redacted by the executor, so a real helper run never hands the publisher a secret; this
// drives the production executor with a known secret in its output to prove it never reaches the journal.
func TestAssistantActivity_RealExecutorNeverJournalsKnownSecret(t *testing.T) {
	const secret = "sk-live-activity-secret" //nolint:gosec // test fixture value, not a real credential
	t.Setenv("ANTHROPIC_API_KEY", secret)
	o, _ := newAssistantsTestOrchestrator(t, true)
	taskID := attachTestTask(context.Background(), t, o, "op-secret")
	dir := t.TempDir()
	cmd := filepath.Join(dir, "fake-claude")
	script := "#!/bin/sh\ncat >/dev/null\n" +
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"key=` + secret + `"},{"type":"tool_use","name":"Bash","input":{"command":"echo ` + secret + `"}}]}}'` + "\n" +
		`echo '{"type":"result","subtype":"success","is_error":false,"result":"ok"}'` + "\n"
	if err := os.WriteFile(cmd, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture: an executable fake agent
		t.Fatal(err)
	}
	cfg := o.helperExecutorConfig(taskID, cmd)
	_, _ = executor.New(cfg).RunHelperAgent(context.Background(), string(taskID), "reviewer", "go", filepath.Join(dir, "h.log"), filepath.Join(dir, "r.json"))

	raw, err := os.ReadFile(filepath.Join(o.opts.RunDir, "notices.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), activityNoticeType) {
		t.Fatalf("the scripted agent produced no activity notice:\n%s", raw)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("a known secret reached notices.jsonl:\n%s", raw)
	}
}

func TestAssistantActivity_DoesNotTouchControlJournalOrTaskStatus(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	taskID := attachTestTask(context.Background(), t, o, "op-iso")
	before := store.Snapshot()
	statusBefore := before.Assistants.Tasks[taskID].Status
	stageBefore := before.Stages[assistantsTestStageID].Status
	_, blockedBefore := before.Assistants.RequiredBlocker(assistantsTestStageID)
	historyBefore, _ := store.History()
	taskEventsBefore := len(store.TaskEvents(taskID))
	journalBefore := fileSize(t, filepath.Join(o.opts.RunDir, "events.jsonl"))

	for range 50 {
		activityOf(t, o, taskID)("Bash", "ls")
	}
	after := store.Snapshot()
	historyAfter, _ := store.History()
	if after.Assistants.Tasks[taskID].Status != statusBefore || after.Stages[assistantsTestStageID].Status != stageBefore {
		t.Error("activity changed a task or stage status")
	}
	if _, blocked := after.Assistants.RequiredBlocker(assistantsTestStageID); blocked != blockedBefore {
		t.Errorf("required gate blocked = %v, was %v", blocked, blockedBefore)
	}
	if len(historyAfter) != len(historyBefore) {
		t.Errorf("activity wrote %d FSM transitions", len(historyAfter)-len(historyBefore))
	}
	if got := len(store.TaskEvents(taskID)); got != taskEventsBefore {
		t.Errorf("task control events = %d, was %d", got, taskEventsBefore)
	}
	if got := fileSize(t, filepath.Join(o.opts.RunDir, "events.jsonl")); got != journalBefore {
		t.Errorf("events.jsonl grew from %d to %d bytes", journalBefore, got)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}
