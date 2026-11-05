package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
)

func TestAssistantActivity_TypedEventsKeepTheirFields(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	taskID := attachTestTask(context.Background(), t, o, "op-typed")
	emit := activityEmitOf(t, o, taskID)
	exit := 2
	started := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	finished := started.Add(3 * time.Second)

	emit(executor.ActivityEvent{Kind: executor.KindMessage, InvocationID: "inv-1", ItemID: "msg:1", Revision: 2, Partial: true, Detail: "thinking"})
	emit(executor.ActivityEvent{Kind: executor.KindMessage, InvocationID: "inv-1", ItemID: "msg:1", Revision: 3, Final: true, Detail: "done"})
	emit(executor.ActivityEvent{Kind: executor.KindToolFinished, InvocationID: "inv-1", ItemID: "tool:c1", Revision: 2, Tool: "Bash", Detail: "make test",
		Outcome: executor.OutcomeFailed, ExitCode: &exit, StartedAt: started, FinishedAt: finished})
	emit(executor.ActivityEvent{Kind: executor.KindInvocationFinished, InvocationID: "inv-1", Outcome: executor.OutcomeInterrupted})

	got := noticesOf(t, o, activityNoticeType)
	if len(got) != 4 {
		t.Fatalf("notices = %d, want 4: %v", len(got), got)
	}
	msg := got[0]
	if msg["kind"] != "message" || msg["invocation_id"] != "inv-1" || msg["item_id"] != "msg:1" || msg["revision"] != float64(2) ||
		!jsonTrue(msg["partial"]) || msg["tool"] != "text" || msg["detail"] != "thinking" {
		t.Errorf("partial message = %v", msg)
	}
	if !jsonTrue(got[1]["final"]) || got[1]["partial"] != nil {
		t.Errorf("final message = %v", got[1])
	}
	tool := got[2]
	if tool["kind"] != "tool_finished" || tool["tool"] != "Bash" || tool["detail"] != "make test" || tool["outcome"] != "failed" ||
		tool["exit_code"] != float64(2) || tool["started_at"] != started.Format(time.RFC3339Nano) || tool["finished_at"] != finished.Format(time.RFC3339Nano) {
		t.Errorf("tool_finished = %v", tool)
	}
	end := got[3]
	if end["kind"] != "invocation_finished" || end["outcome"] != "interrupted" || end["item_id"] != nil {
		t.Errorf("invocation_finished = %v", end)
	}
	seen := map[any]bool{}
	for _, d := range got {
		if seen[d["event_id"]] {
			t.Errorf("event_id %v repeats", d["event_id"])
		}
		seen[d["event_id"]] = true
	}
}

func TestAssistantActivity_LiveOnlyPartialIsNotSaved(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	taskID := attachTestTask(context.Background(), t, o, "op-live")
	_, sub := o.ui.Subscribe(16)
	activityEmitOf(t, o, taskID)(executor.ActivityEvent{Kind: executor.KindMessage, InvocationID: "inv-1", ItemID: "msg:1", Revision: 1, Partial: true, LiveOnly: true, Detail: "hel"})

	select {
	case ev := <-sub:
		live, _ := ev.Data.(stagefiles.ActivityData)
		if ev.Type != bus.EventAssistantActivity || live.Detail != "hel" || !live.Partial || live.EventID == "" {
			t.Errorf("live event = %+v", ev)
		}
	default:
		t.Fatal("a live-only partial must still reach subscribers")
	}
	if got := noticesOf(t, o, activityNoticeType); len(got) != 0 {
		t.Fatalf("a live-only partial was saved: %v", got)
	}
}

func TestAssistantActivity_CapsEveryTextFieldAtRuneBoundary(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	taskID := attachTestTask(context.Background(), t, o, "op-caps")
	long := "x" + strings.Repeat("я", 20<<10)
	activityEmitOf(t, o, taskID)(executor.ActivityEvent{Kind: executor.KindMessage, InvocationID: long, ItemID: "msg:" + long, Revision: 1, Detail: long})

	page, err := stagefiles.ReadTaskActivity(o.opts.RunDir, string(taskID), stagefiles.ActivityQuery{})
	if err != nil || len(page.Entries) != 1 {
		t.Fatalf("history = %d entries, err %v; want the capped entry to read back", len(page.Entries), err)
	}
	e := page.Entries[0]
	for name, c := range map[string]struct {
		got   string
		limit int
	}{
		"invocation_id": {e.InvocationID, stagefiles.MaxActivityInvocationBytes},
		"item_id":       {e.ItemID, stagefiles.MaxActivityItemBytes},
		"detail":        {e.Detail, stagefiles.MaxActivityDetailBytes},
	} {
		if len(c.got) == 0 || len(c.got) > c.limit || !utf8.ValidString(c.got) {
			t.Errorf("%s: len=%d valid=%v, want a non-empty valid prefix of at most %d bytes", name, len(c.got), utf8.ValidString(c.got), c.limit)
		}
	}
	if !e.Truncated {
		t.Error("a capped entry must be marked truncated")
	}
}

func TestAssistantActivity_OversizedIDsSharingAPrefixStayDistinct(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	taskID := attachTestTask(context.Background(), t, o, "op-ids")
	emit := activityEmitOf(t, o, taskID)
	common := "tool:" + strings.Repeat("p", stagefiles.MaxActivityItemBytes)
	for _, suffix := range []string{"-one", "-two"} {
		emit(executor.ActivityEvent{Kind: executor.KindToolStarted, InvocationID: "inv", ItemID: common + suffix, Revision: 1, Tool: "Bash"})
	}
	page, err := stagefiles.ReadTaskActivity(o.opts.RunDir, string(taskID), stagefiles.ActivityQuery{})
	if err != nil || len(page.Entries) != 2 {
		t.Fatalf("history = %+v, err %v", page.Entries, err)
	}
	a, b := page.Entries[0].ItemID, page.Entries[1].ItemID
	if a == b || len(a) > stagefiles.MaxActivityItemBytes || len(b) > stagefiles.MaxActivityItemBytes {
		t.Errorf("item ids %q / %q: want distinct ids within the cap", a, b)
	}
}

// fakeTurnAgent is a claude-family helper that shows the same provider tool id in every call. A native run is
// refused (so each turn also runs prompt-only); the first prompt-only call stays open until the test pauses the
// task, and the next one answers.
func fakeTurnAgent(t *testing.T, dir string) (cmd, blocked string) {
	t.Helper()
	cmd, blocked = filepath.Join(dir, "fake-claude"), filepath.Join(dir, "blocked")
	answer := strconv.Quote(`{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok"}`)
	script := "#!/bin/sh\ncat >/dev/null\n" +
		`echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"c1","name":"Bash","input":{"command":"ls"}}]}}'` + "\n" +
		`echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"c1","is_error":false}]}}'` + "\n" +
		`for a in "$@"; do [ "$a" = "--json-schema" ] && exit 1; done` + "\n" +
		"if [ ! -e " + blocked + " ]; then\n  : > " + blocked + "\n  until [ -e " + filepath.Join(dir, "never") + " ]; do sleep 0.02; done\nfi\n" +
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":` + answer + `}]}}'` + "\n" +
		`echo '{"type":"result","subtype":"success","is_error":false}'` + "\n"
	if err := os.WriteFile(cmd, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture: an executable fake agent
		t.Fatal(err)
	}
	return cmd, blocked
}

// A task that is paused and continued runs two turns, each with a refused native call and a prompt-only call, all
// showing the same provider tool id. The saved history keeps the four calls apart and puts each in its own turn.
func TestAssistantActivity_PauseContinueKeepsInvocationsAndTurnsApart(t *testing.T) {
	dir := t.TempDir()
	cmd, blocked := fakeTurnAgent(t, dir)
	// Format repair runs the stage command; a stub keeps a stray repair from starting a real agent.
	stubs := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubs, "claude"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	t.Setenv("PATH", stubs+string(os.PathListSeparator)+os.Getenv("PATH"))

	o, store := newAssistantsTestOrchestrator(t, true)
	reg, err := assistants.BuildRegistry(nil, []assistants.Profile{{ID: "reviewer", Title: "Reviewer", Command: cmd}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	o.opts.AssistantsRegistry = reg
	o.opts.Config.Docker.Agents = map[string]config.AgentRecipe{cmd: {Type: config.ClaudeCommand}}
	o.runHelperAgent = o.execHelperAgent
	activateStage(t, o, assistantsTestStageID)

	taskID := attachTestTask(context.Background(), t, o, "op-turns")
	waitUntil(t, "the first turn's prompt-only call is open", func() bool {
		if _, err := os.Stat(blocked); err != nil {
			return false
		}
		return store.Snapshot().Assistants.Tasks[taskID].Turn == 1
	})
	if err := o.PauseTask(context.Background(), taskID); err != nil {
		t.Fatalf("PauseTask: %v", err)
	}
	waitForTaskStatus(t, store, taskID, assistants.StatusQueued, 5*time.Second)
	if err := o.ContinueTask(context.Background(), taskID); err != nil {
		t.Fatalf("ContinueTask: %v", err)
	}
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 5*time.Second)

	page, err := stagefiles.ReadTaskActivity(o.opts.RunDir, string(taskID), stagefiles.ActivityQuery{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	type call struct {
		turn                     uint64
		starts, ends             int
		outcome                  string
		sameTurn, attributedToIt bool
	}
	var order []string
	calls := map[string]*call{}
	participant := string(o.taskMeshParticipant(taskID))
	for _, e := range page.Entries {
		if e.InvocationID == "" {
			t.Fatalf("entry without an invocation id: %+v", e.ActivityData)
		}
		c := calls[e.InvocationID]
		if c == nil {
			c = &call{turn: e.Turn, sameTurn: true, attributedToIt: true}
			calls[e.InvocationID] = c
			order = append(order, e.InvocationID)
		}
		c.sameTurn = c.sameTurn && e.Turn == c.turn
		c.attributedToIt = c.attributedToIt && e.TaskID == string(taskID) && e.ParticipantID == participant
		switch {
		case e.Kind == "tool_started" && e.ItemID == "tool:c1":
			c.starts++
		case e.Kind == "invocation_finished":
			c.ends++
			c.outcome = e.Outcome
		default:
		}
	}
	if len(order) != 4 {
		t.Fatalf("invocations = %v, want 4 distinct calls", order)
	}
	for i, id := range order {
		c := calls[id]
		wantTurn := uint64(i/2 + 1)
		if c.turn != wantTurn || !c.sameTurn || c.starts != 1 || c.ends != 1 || !c.attributedToIt {
			t.Errorf("call %d (%s) = %+v, want turn %d, one start, one end, task and participant on every entry", i, id, *c, wantTurn)
		}
	}
	if got := calls[order[1]].outcome; got == "completed" {
		t.Errorf("the paused prompt-only call ended %q, want it not completed", got)
	}
	if got := calls[order[3]].outcome; got != "completed" {
		t.Errorf("the continued prompt-only call ended %q, want completed", got)
	}
	if turn := store.Snapshot().Assistants.Tasks[taskID].Turn; turn != 2 {
		t.Errorf("task turn = %d, want 2", turn)
	}
}

func TestAssistantActivity_ContinuedTaskKeepsInvocationsApart(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	taskID := attachTestTask(context.Background(), t, o, "op-inv")
	emit := activityEmitOf(t, o, taskID)
	for _, inv := range []string{"inv-a", "inv-b"} {
		emit(executor.ActivityEvent{Kind: executor.KindToolStarted, InvocationID: inv, ItemID: "tool:c1", Revision: 1, Tool: "Bash", Detail: "ls"})
	}
	page, err := stagefiles.ReadTaskActivity(o.opts.RunDir, string(taskID), stagefiles.ActivityQuery{})
	if err != nil || len(page.Entries) != 2 || page.Entries[0].InvocationID != "inv-a" || page.Entries[1].InvocationID != "inv-b" ||
		page.Entries[0].ItemID != page.Entries[1].ItemID {
		t.Fatalf("history = %+v, err %v; want two invocations with the same provider item id", page.Entries, err)
	}
}

func TestOutputFormatterHasNoActivity(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	if cfg := o.outputFormatterConfig("claude", assistantsTestStageID); cfg.OnActivity != nil || cfg.OnAction != nil {
		t.Fatal("format repair must not publish helper activity")
	}
}

// jsonTrue reports whether a decoded JSON value is the boolean true.
func jsonTrue(v any) bool {
	b, _ := v.(bool)
	return b
}
