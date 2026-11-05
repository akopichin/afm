package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentjobs"
	"github.com/akopichin/afm/pkg/clock"
)

// activityCollector gathers ActivityEvents on the reader goroutine; the channel lets a test wait for the next
// event while the fake CLI is still running, so "visible before the process exits" needs no sleeps.
type activityCollector struct {
	mu     sync.Mutex
	events []ActivityEvent
	ch     chan ActivityEvent
}

func newActivityCollector() *activityCollector {
	return &activityCollector{ch: make(chan ActivityEvent, 256)}
}

func (c *activityCollector) sink(ev ActivityEvent) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
	c.ch <- ev
}

func (c *activityCollector) all() []ActivityEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ActivityEvent(nil), c.events...)
}

// next waits for the next not yet read event.
// skipTo drops events until one satisfies stop and returns it.
func (c *activityCollector) skipTo(t *testing.T, stop func(ActivityEvent) bool) ActivityEvent {
	t.Helper()
	for {
		if ev := c.next(t); stop(ev) {
			return ev
		}
	}
}

func (c *activityCollector) next(t *testing.T) ActivityEvent {
	t.Helper()
	select {
	case ev := <-c.ch:
		return ev
	case <-time.After(10 * time.Second):
		t.Fatal("no activity event within 10s")
		return ActivityEvent{}
	}
}

type activityRun struct {
	events []ActivityEvent
	out    HelperOutcome
	err    error
	dir    string
}

func (r activityRun) read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (r activityRun) byItem(itemID string) []ActivityEvent {
	var out []ActivityEvent
	for _, ev := range r.events {
		if ev.ItemID == itemID {
			out = append(out, ev)
		}
	}
	return out
}

func (r activityRun) last(t *testing.T) ActivityEvent {
	t.Helper()
	if len(r.events) == 0 {
		t.Fatal("no activity events")
	}
	return r.events[len(r.events)-1]
}

// runActivity runs a fake helper whose whole stdout is script's output.
func runActivity(t *testing.T, script string, mutate func(*Config)) activityRun {
	t.Helper()
	dir := t.TempDir()
	col := newActivityCollector()
	cfg := Config{Command: "bash", ExtraArgs: []string{"-c", script}, IdleTimeout: 10 * time.Second, OnActivity: col.sink}
	if mutate != nil {
		mutate(&cfg)
	}
	out, err := New(cfg).RunHelperAgent(context.Background(), "t1", "reviewer", "review",
		filepath.Join(dir, "helper.log"), filepath.Join(dir, "result.json"))
	return activityRun{events: col.all(), out: out, err: err, dir: dir}
}

func fixtureScript(name string) string {
	return "cat " + filepath.Join("testdata", "activity", name)
}

func runFixture(t *testing.T, name string) activityRun {
	t.Helper()
	return runActivity(t, fixtureScript(name), nil)
}

func linesScript(lines ...string) string {
	return emitScript("0", lines...)
}

// kindsOf is the shape of a stream: "message:msg:t1:r1".
func kindsOf(events []ActivityEvent) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, string(ev.Kind)+":"+ev.ItemID)
	}
	return out
}

func equalStrings(a, b []string) bool {
	return strings.Join(a, "\n") == strings.Join(b, "\n")
}

func TestActivity_ClaudeToolLifecycle(t *testing.T) {
	r := runFixture(t, "claude_tool_lifecycle.jsonl")
	if r.err != nil || !r.out.ProcessOK {
		t.Fatalf("run: err=%v outcome=%+v", r.err, r.out)
	}
	want := []string{
		"message:msg:t1", "tool_started:tool:toolu_1", "tool_finished:tool:toolu_1",
		"message:msg:t2", "message:msg:t2", "invocation_finished:",
	}
	if got := kindsOf(r.events); !equalStrings(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	start, finish := r.events[1], r.events[2]
	if start.Tool != "Bash" || start.Detail != "ls pkg" || start.Revision != 1 || start.StartedAt.IsZero() {
		t.Errorf("tool_started = %+v", start)
	}
	if finish.Outcome != OutcomeCompleted || finish.Revision != 2 || finish.Tool != "Bash" || finish.Detail != "ls pkg" ||
		finish.ExitCode != nil || finish.FinishedAt.IsZero() || finish.StartedAt.IsZero() {
		t.Errorf("tool_finished = %+v (a Claude tool result carries no exit code)", finish)
	}
	if msg := r.events[3]; msg.Detail != "All good." || msg.Revision != 1 || msg.Final || msg.Partial {
		t.Errorf("answer message = %+v", msg)
	}
	if fin := r.events[4]; !fin.Final || fin.Revision != 2 || fin.Detail != "All good." {
		t.Errorf("final revision = %+v, want the answer item re-published with Final", fin)
	}
	if end := r.last(t); end.Outcome != OutcomeCompleted || end.ItemID != "" {
		t.Errorf("invocation_finished = %+v", end)
	}
	id := r.events[0].InvocationID
	if !strings.HasPrefix(id, "inv-") {
		t.Errorf("invocation id %q: without a job it must be executor-local", id)
	}
	for _, ev := range r.events {
		if ev.InvocationID != id {
			t.Errorf("event %+v has another invocation id", ev)
		}
	}
	if r.out.RawText != "All good." {
		t.Errorf("RawText = %q", r.out.RawText)
	}
}

func TestActivity_InvocationIDIsTheJobID(t *testing.T) {
	sink := &recordingJobSink{}
	r := runActivity(t, fixtureScript("claude_tool_lifecycle.jsonl"), func(c *Config) {
		c.Job = &JobConfig{Sink: sink}
	})
	id := sink.jobID()
	if id == "" || r.events[0].InvocationID != id {
		t.Fatalf("invocation id %q, job id %q: a call's activity must be keyed by its job", r.events[0].InvocationID, id)
	}
}

type recordingJobSink struct {
	mu   sync.Mutex
	recs []agentjobs.Record
}

func (s *recordingJobSink) Append(r agentjobs.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, r)
	return nil
}

func (s *recordingJobSink) jobID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.recs) == 0 {
		return ""
	}
	return s.recs[0].JobID
}

func TestActivity_ToolErrorIsFailed(t *testing.T) {
	r := runFixture(t, "claude_tool_failed.jsonl")
	finish := r.byItem("tool:toolu_9")[1]
	if finish.Kind != KindToolFinished || finish.Outcome != OutcomeFailed {
		t.Errorf("tool_finished = %+v, want failed", finish)
	}
}

func TestActivity_TextBlocksOfOneClaudeMessageStayApart(t *testing.T) {
	r := runFixture(t, "claude_shared_message_id.jsonl")
	if got, want := kindsOf(r.events[:2]), []string{"message:msg:t1", "message:msg:t2"}; !equalStrings(got, want) {
		t.Fatalf("events = %v, want separate items for blocks sharing a message id", got)
	}
	if r.events[0].Detail != "First block." || r.events[1].Detail != "Second block." {
		t.Errorf("block texts overwrote each other: %+v", r.events[:2])
	}
}

func TestActivity_StructuredOutputToolIsHidden(t *testing.T) {
	r := runFixture(t, "claude_structured_output.jsonl")
	for _, ev := range r.events {
		if ev.Tool == "StructuredOutput" || strings.Contains(ev.ItemID, "toolu_so") {
			t.Errorf("the native payload tool leaked into activity: %+v", ev)
		}
	}
	if got, want := kindsOf(r.events), []string{"message:msg:t1", "message:msg:t1", "invocation_finished:"}; !equalStrings(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestActivity_ToolWithoutIDStaysALegacyLine(t *testing.T) {
	r := runFixture(t, "claude_tool_without_id.jsonl")
	legacy := r.events[0]
	if legacy.Kind != "" || legacy.Tool != "Bash" || legacy.Detail != "echo hi" || legacy.ItemID != "" {
		t.Errorf("legacy action = %+v, want a plain tool/detail line", legacy)
	}
}

func TestActivity_DuplicateStartIsIdempotent(t *testing.T) {
	start := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`
	r := runActivity(t, linesScript(start, start), nil)
	starts := 0
	for _, ev := range r.events {
		if ev.Kind == KindToolStarted {
			starts++
		}
	}
	if starts != 1 {
		t.Errorf("tool_started published %d times for one tool id", starts)
	}
}

func TestActivity_ExtensionToolFinishCarriesExitCode(t *testing.T) {
	r := runFixture(t, "ext_v1_complete.jsonl")
	finish := r.byItem("tool:call_1")[1]
	if finish.ExitCode == nil || *finish.ExitCode != 0 || finish.Outcome != OutcomeCompleted {
		t.Errorf("finish = %+v", finish)
	}
	r = runFixture(t, "ext_v1_failed_tool.jsonl")
	finish = r.byItem("tool:call_7")[1]
	if finish.ExitCode == nil || *finish.ExitCode != 2 || finish.Outcome != OutcomeFailed {
		t.Errorf("failed finish = %+v", finish)
	}
}

func TestActivity_FinishWithoutStartCarriesTheTool(t *testing.T) {
	r := runFixture(t, "ext_v1_finish_without_start.jsonl")
	items := r.byItem("tool:call_x")
	if len(items) != 1 {
		t.Fatalf("events for call_x = %+v, want the finish only (a late start must not reopen it)", items)
	}
	if f := items[0]; f.Kind != KindToolFinished || f.Tool != "Bash" || f.Detail != "git status" || f.Outcome != OutcomeCompleted {
		t.Errorf("finish without start = %+v", f)
	}
}

func TestActivity_ExtensionMessagesAndFinalRevision(t *testing.T) {
	r := runFixture(t, "ext_v1_complete.jsonl")
	if r.out.RawText != `{"verdict":"pass"}` || !r.out.ProcessOK || r.out.ProtocolErr != nil {
		t.Fatalf("outcome = %+v", r.out)
	}
	m1 := r.byItem("msg:m1")
	if len(m1) != 2 || !m1[0].Partial || m1[1].Partial || m1[1].Final {
		t.Errorf("progress item msg:m1 = %+v, want a partial then a complete revision, not final", m1)
	}
	m2 := r.byItem("msg:m2")
	if len(m2) != 2 || m2[0].Final || !m2[1].Final || m2[1].Revision != m2[0].Revision+1 {
		t.Errorf("answer item msg:m2 = %+v, want a complete revision then a Final one", m2)
	}
}

func TestActivity_DeclaredProtocolTakesTheFinalOnlyFromAfmFinal(t *testing.T) {
	r := runFixture(t, "ext_v1_no_final.jsonl")
	if r.out.ProtocolErr == nil || r.out.RawText != "" {
		t.Errorf("announced stream without afm_final: outcome = %+v, want ProtocolErr and no answer", r.out)
	}
	for _, ev := range r.events {
		if ev.Final {
			t.Errorf("progress text became final: %+v", ev)
		}
	}
	r = runFixture(t, "ext_v1_late_announce.jsonl")
	if r.out.RawText != "the declared answer" || r.out.ProtocolErr != nil {
		t.Errorf("late announcement: outcome = %+v", r.out)
	}
	r = runFixture(t, "ext_v1_failed_tool.jsonl")
	if r.out.RawText != "tests failed" {
		t.Errorf("afm_final without item id: RawText = %q", r.out.RawText)
	}
}

func TestActivity_AfmFinalWorksWithoutNativeSchemaAndBeforeErrorLines(t *testing.T) {
	// An afm_final text that looks like an error must still be taken as the answer, not as an error line.
	line := `{"type":"afm_final","version":1,"text":"Error: nothing to do"}`
	r := runActivity(t, linesScript(`{"type":"system","subtype":"afm_stream","version":1}`, line), nil)
	if r.out.RawText != "Error: nothing to do" {
		t.Errorf("RawText = %q", r.out.RawText)
	}
	if strings.Contains(r.read(t, "helper.log"), "helper-note") {
		t.Errorf("afm_final was reported as an error line:\n%s", r.read(t, "helper.log"))
	}
}

func TestActivity_UnknownProtocolVersionIsIgnored(t *testing.T) {
	r := runActivity(t, linesScript(
		`{"type":"system","subtype":"afm_stream","version":2}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"plain answer"}]}}`), nil)
	if r.out.RawText != "plain answer" {
		t.Errorf("RawText = %q: an unknown protocol version must leave the legacy rule in force", r.out.RawText)
	}
}

func TestActivity_LegacyStreamKeepsTheLastTextAsTheAnswer(t *testing.T) {
	r := runFixture(t, "claude_tool_lifecycle.jsonl")
	if r.out.RawText != "All good." || r.out.ProtocolErr != nil {
		t.Errorf("outcome = %+v", r.out)
	}
}

func TestActivity_OnActivityReplacesOnAction(t *testing.T) {
	var actions []string
	r := runActivity(t, fixtureScript("claude_tool_lifecycle.jsonl"), func(c *Config) {
		c.OnAction = func(tool, detail string) { actions = append(actions, tool+":"+detail) }
	})
	if len(actions) != 0 {
		t.Errorf("OnAction called %v although OnActivity is set", actions)
	}
	if len(r.events) == 0 {
		t.Fatal("no activity")
	}
}

func TestActivity_WithoutOnActivityOnActionIsUnchanged(t *testing.T) {
	var actions []string
	dir := t.TempDir()
	ex := New(Config{
		Command: "bash", ExtraArgs: []string{"-c", fixtureScript("claude_tool_lifecycle.jsonl")}, IdleTimeout: 10 * time.Second,
		OnAction: func(tool, detail string) { actions = append(actions, tool+":"+detail) },
	})
	if _, err := ex.RunHelperAgent(context.Background(), "t1", "reviewer", "p", filepath.Join(dir, "h.log"), filepath.Join(dir, "r.json")); err != nil {
		t.Fatal(err)
	}
	want := []string{"text:Checking the repository.", "Bash:ls pkg", "text:All good."}
	if !equalStrings(actions, want) {
		t.Errorf("actions = %v, want %v", actions, want)
	}
}

// A fake CLI that waits for the test between steps proves events are published while the process still runs.
func TestActivity_EventsArriveBeforeTheProcessExits(t *testing.T) {
	dir := t.TempDir()
	gate := func(name string) string {
		return "until [ -e " + filepath.Join(dir, name) + " ]; do sleep 0.02; done\n"
	}
	script := `echo '{"type":"assistant","message":{"content":[{"type":"text","text":"thinking out loud"}]}}'` + "\n" +
		gate("g1") +
		`echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"c1","name":"Bash","input":{"command":"ls"}}]}}'` + "\n" +
		gate("g2") +
		`echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"c1","is_error":false}]}}'` + "\n" +
		gate("g3") +
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}'` + "\n"
	col := newActivityCollector()
	ex := New(Config{Command: "bash", ExtraArgs: []string{"-c", script}, IdleTimeout: 30 * time.Second, OnActivity: col.sink})
	done := make(chan error, 1)
	go func() {
		_, err := ex.RunHelperAgent(context.Background(), "t1", "reviewer", "p", filepath.Join(dir, "h.log"), filepath.Join(dir, "r.json"))
		done <- err
	}()
	release := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if ev := col.next(t); ev.Kind != KindMessage || ev.Detail != "thinking out loud" {
		t.Fatalf("first event = %+v", ev)
	}
	release("g1")
	if ev := col.next(t); ev.Kind != KindToolStarted {
		t.Fatalf("second event = %+v, want tool_started while the process is alive", ev)
	}
	release("g2")
	if ev := col.next(t); ev.Kind != KindToolFinished {
		t.Fatalf("third event = %+v, want tool_finished", ev)
	}
	select {
	case err := <-done:
		t.Fatalf("process finished early: %v", err)
	default:
	}
	release("g3")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// partialLine is a snapshot revision of message item m1.
func partialLine(text string) string {
	return `{"type":"assistant","message":{"content":[{"type":"text","text":"` + text + `"}]},"afm":{"item_id":"m1","partial":true}}`
}

const (
	announceLine  = `{"type":"system","subtype":"afm_stream","version":1}`
	toolStartLine = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"c1","name":"Bash","input":{"command":"ls"}}]}}`
	toolDoneLine  = `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"c1","is_error":false}]}}`
)

// batchedRun feeds a fake helper one batch of lines at a time. Each batch ends with a tool_use line of its own: the
// reader publishes it after handling everything before it, so waiting for it proves the batch was processed. The fake
// clock is the only source of time, so the 1 s / 5 s limits are exercised exactly, without sleeping.
type batchedRun struct {
	t    *testing.T
	fc   *clock.FakeClock
	col  *activityCollector
	dir  string
	n    int
	done chan error
}

func startBatched(t *testing.T, batches int) *batchedRun {
	t.Helper()
	dir := t.TempDir()
	script := "for i in $(seq 1 " + strconv.Itoa(batches) + "); do until [ -e " + dir + "/b$i ]; do sleep 0.02; done; cat " + dir + "/b$i; done\n"
	r := &batchedRun{t: t, fc: clock.NewFake(time.Unix(1000, 0)), col: newActivityCollector(), dir: dir, done: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ex := New(Config{Command: "bash", ExtraArgs: []string{"-c", script}, IdleTimeout: time.Hour, Clock: r.fc, OnActivity: r.col.sink})
	go func() {
		_, err := ex.RunHelperAgent(ctx, "t1", "reviewer", "p", filepath.Join(dir, "h.log"), filepath.Join(dir, "r.json"))
		r.done <- err
	}()
	return r
}

// send advances the clock by after, then delivers lines and waits until the reader has handled them.
func (r *batchedRun) send(after time.Duration, lines ...string) {
	r.t.Helper()
	r.fc.Advance(after)
	r.n++
	sentinel := strings.Replace(toolStartLine, `"c1"`, `"s`+strconv.Itoa(r.n)+`"`, 1)
	tmp, final := filepath.Join(r.dir, "tmp"), filepath.Join(r.dir, "b"+strconv.Itoa(r.n))
	if err := os.WriteFile(tmp, []byte(strings.Join(append(lines, sentinel), "\n")+"\n"), 0o644); err != nil {
		r.t.Fatal(err)
	}
	if err := os.Rename(tmp, final); err != nil {
		r.t.Fatal(err)
	}
	r.col.skipTo(r.t, func(ev ActivityEvent) bool { return ev.ItemID == "tool:s"+strconv.Itoa(r.n) })
}

func (r *batchedRun) finish() []ActivityEvent {
	r.t.Helper()
	select {
	case err := <-r.done:
		if err != nil {
			r.t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		r.t.Fatal("the helper did not end")
	}
	return r.col.all()
}

func partials(events []ActivityEvent) []ActivityEvent {
	var out []ActivityEvent
	for _, ev := range events {
		if ev.Kind == KindMessage && ev.Partial {
			out = append(out, ev)
		}
	}
	return out
}

func TestActivity_PartialSnapshotsAreCoalescedAtTheOneSecondBoundary(t *testing.T) {
	r := startBatched(t, 12)
	// A burst of ten snapshots spread over 900 ms, then one at 999 ms and one at exactly 1 s after the first.
	r.send(0, partialLine("a"))
	text := "a"
	for i := 0; i < 9; i++ {
		text += string(rune('b' + i))
		r.send(100*time.Millisecond, partialLine(text))
	}
	if got := partials(r.col.all()); len(got) != 1 || got[0].Detail != "a" || !got[0].LiveOnly {
		t.Fatalf("after the burst partials = %+v, want only the first, live-only snapshot", got)
	}
	r.send(99*time.Millisecond, partialLine(text+"!")) // 999 ms after the first publication
	if got := partials(r.col.all()); len(got) != 1 {
		t.Fatalf("at 999 ms partials = %d, want still 1", len(got))
	}
	r.send(time.Millisecond, partialLine(text+"!!")) // exactly 1 s
	got := partials(r.col.all())
	if len(got) != 2 || got[1].Detail != text+"!!" || got[1].Revision != got[0].Revision+1 || !got[1].LiveOnly {
		t.Fatalf("at 1 s partials = %+v, want the latest snapshot as the next revision", got)
	}
	events := r.finish()
	if order := kindsOf(events); order[len(order)-1] != "invocation_finished:" {
		t.Errorf("events end with %v", order)
	}
}

func TestActivity_PartialBecomesDurableAtFiveSeconds(t *testing.T) {
	for name, c := range map[string]struct {
		after    time.Duration
		liveOnly bool
	}{
		"just below": {5*time.Second - time.Millisecond, true},
		"at":         {5 * time.Second, false},
	} {
		t.Run(name, func(t *testing.T) {
			r := startBatched(t, 2)
			r.send(0, announceLine, partialLine("a"))
			r.send(c.after, partialLine("ab"))
			got := partials(r.col.all())
			if len(got) != 2 || got[1].Detail != "ab" || got[1].LiveOnly != c.liveOnly {
				t.Errorf("partials = %+v, want the second snapshot with LiveOnly=%v", got, c.liveOnly)
			}
			r.finish()
		})
	}
}

func TestActivity_PendingSnapshotIsFlushedOnceWhenTheCallEnds(t *testing.T) {
	// Two snapshots in the same second: the second one is pending when the process ends. It must be published
	// once, durably and complete, before invocation_finished — and nothing after it.
	r := runActivity(t, linesScript(announceLine, partialLine("one"), partialLine("one two")), nil)
	got := partials(r.events)
	if len(got) != 1 || got[0].Detail != "one" {
		t.Fatalf("partials = %+v", got)
	}
	flushed := r.events[len(r.events)-2]
	if flushed.Kind != KindMessage || flushed.Detail != "one two" || flushed.LiveOnly || flushed.Partial {
		t.Errorf("pending snapshot flush = %+v", flushed)
	}
	if end := r.last(t); end.Kind != KindInvocationFinished {
		t.Errorf("last event = %+v", end)
	}
}

func TestActivity_PartialLinesStayOutOfTheLogs(t *testing.T) {
	r := runActivity(t, linesScript(announceLine, partialLine("PARTIAL-ONLY"),
		`{"type":"assistant","message":{"content":[{"type":"text","text":"whole"}]},"afm":{"item_id":"m1","partial":false}}`,
		`{"type":"afm_final","version":1,"text":"whole","item_id":"m1"}`), nil)
	for _, name := range []string{"helper.jsonl", "helper.log"} {
		if strings.Contains(r.read(t, name), "PARTIAL-ONLY") {
			t.Errorf("%s holds a partial snapshot", name)
		}
	}
	if !strings.Contains(r.read(t, "helper.jsonl"), `"partial":false`) {
		t.Errorf("complete revisions must stay in the jsonl:\n%s", r.read(t, "helper.jsonl"))
	}
}

const activitySecret = "sk-live-secret-value-123" //nolint:gosec // test fixture, not a real credential

func secretConfig(c *Config) {
	c.Secrets = func() []string { return []string{activitySecret} }
	c.Redact = func(s string) string { return strings.ReplaceAll(s, activitySecret, "[REDACTED]") }
}

func TestActivity_SecretPrefixAtTheEndOfASnapshotIsHeldBack(t *testing.T) {
	r := runActivity(t, linesScript(announceLine,
		partialLine("token sk-live-secr"),
		`{"type":"assistant","message":{"content":[{"type":"text","text":"token `+activitySecret+` done"}]},"afm":{"item_id":"m1","partial":false}}`,
		`{"type":"afm_final","version":1,"text":"ok"}`), secretConfig)
	for _, ev := range r.events {
		if strings.Contains(ev.Detail, "sk-live") {
			t.Errorf("secret or its prefix reached a sink: %+v", ev)
		}
	}
	got := partials(r.events)
	if len(got) != 1 || got[0].Detail != "token " {
		t.Errorf("partials = %+v, want the snapshot without the secret-like tail", got)
	}
	for _, name := range []string{"helper.jsonl", "helper.log", "result.json"} {
		if _, err := os.Stat(filepath.Join(r.dir, name)); err != nil {
			continue
		}
		if strings.Contains(r.read(t, name), "sk-live") {
			t.Errorf("%s contains the secret or its prefix", name)
		}
	}
}

func TestActivity_HugeSecretDoesNotHoldBackPlainText(t *testing.T) {
	huge := strings.Repeat("Q", 8*1024)
	r := runActivity(t, linesScript(announceLine, partialLine("plain progress text")), func(c *Config) {
		c.Secrets = func() []string { return []string{huge} }
		c.Redact = func(s string) string { return strings.ReplaceAll(s, huge, "[REDACTED]") }
	})
	got := partials(r.events)
	if len(got) != 1 || got[0].Detail != "plain progress text" {
		t.Errorf("partials = %+v: only a possible secret start may be held back", got)
	}
}

func TestActivity_PartialsAreNotPublishedWhenRedactionHasNoSecretList(t *testing.T) {
	r := runActivity(t, linesScript(announceLine, partialLine("maybe a secret"),
		`{"type":"assistant","message":{"content":[{"type":"text","text":"complete text"}]},"afm":{"item_id":"m1","partial":false}}`,
		`{"type":"afm_final","version":1,"text":"x"}`), func(c *Config) {
		c.Redact = func(s string) string { return s }
	})
	if got := partials(r.events); len(got) != 0 {
		t.Errorf("snapshots published without a way to tell where a secret could start: %+v", got)
	}
	if len(r.byItem("msg:m1")) == 0 {
		t.Error("the complete revision must still be published")
	}
}

func TestActivity_UnchangedTrimmedSnapshotIsNotRepublished(t *testing.T) {
	// The only growth between snapshots is inside the held-back tail: nothing new is visible.
	fc := clock.NewFake(time.Unix(1000, 0))
	dir := t.TempDir()
	gate := filepath.Join(dir, "g")
	script := "cat <<'EOF'\n" + strings.Join([]string{announceLine, partialLine("text sk-li"), partialLine("text sk-live-"), toolStartLine}, "\n") + "\nEOF\n" +
		"until [ -e " + gate + " ]; do sleep 0.02; done\n"
	col := newActivityCollector()
	cfg := Config{Command: "bash", ExtraArgs: []string{"-c", script}, IdleTimeout: time.Hour, Clock: fc, OnActivity: col.sink}
	secretConfig(&cfg)
	done := make(chan error, 1)
	go func() {
		_, err := New(cfg).RunHelperAgent(context.Background(), "t1", "reviewer", "p", filepath.Join(dir, "h.log"), filepath.Join(dir, "r.json"))
		done <- err
	}()
	col.skipTo(t, func(ev ActivityEvent) bool { return ev.Kind == KindToolStarted })
	fc.Advance(2 * time.Second)
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, p := range partials(col.all()) {
		if p.Detail != "text " {
			t.Errorf("published partial %q, want only the held-back view \"text \"", p.Detail)
		}
	}
	if n := len(partials(col.all())); n != 1 {
		t.Errorf("partial publications = %d, want 1", n)
	}
}

func TestActivity_InvocationFinishedOnEveryExit(t *testing.T) {
	msg := `echo '{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}'` + "\n"
	count := func(events []ActivityEvent) int {
		n := 0
		for _, ev := range events {
			if ev.Kind == KindInvocationFinished {
				n++
			}
		}
		return n
	}
	t.Run("non-zero exit is failed", func(t *testing.T) {
		r := runActivity(t, msg+"exit 3", nil)
		if end := r.last(t); end.Kind != KindInvocationFinished || end.Outcome != OutcomeFailed || count(r.events) != 1 {
			t.Errorf("events = %v", kindsOf(r.events))
		}
	})
	t.Run("usage limit", func(t *testing.T) {
		col := newActivityCollector()
		ex := limitExecutor(t, emitScript("1", rejectedEventSec, limitAssistant, limitResult))
		ex.cfg.OnActivity = col.sink
		dir := t.TempDir()
		_, err := ex.RunHelperAgent(context.Background(), "t1", "reviewer", "p", filepath.Join(dir, "h.log"), filepath.Join(dir, "r.json"))
		var limit *UsageLimitError
		if !errors.As(err, &limit) {
			t.Fatalf("err = %v", err)
		}
		events := col.all()
		if end := events[len(events)-1]; end.Kind != KindInvocationFinished || end.Outcome != OutcomeUsageLimit || count(events) != 1 {
			t.Errorf("events = %v", kindsOf(events))
		}
	})
	t.Run("result file write error", func(t *testing.T) {
		col := newActivityCollector()
		ex := New(Config{Command: "bash", ExtraArgs: []string{"-c", fixtureScript("claude_tool_lifecycle.jsonl")}, IdleTimeout: 10 * time.Second, OnActivity: col.sink})
		dir := t.TempDir()
		_, err := ex.RunHelperAgent(context.Background(), "t1", "reviewer", "p", filepath.Join(dir, "h.log"), filepath.Join(dir, "no-such-dir", "r.json"))
		if err == nil {
			t.Fatal("want the result file error")
		}
		events := col.all()
		if end := events[len(events)-1]; end.Kind != KindInvocationFinished || end.Outcome != OutcomeCompleted || count(events) != 1 {
			t.Errorf("events = %v", kindsOf(events))
		}
	})
	t.Run("interrupt flushes the pending snapshot once", func(t *testing.T) {
		dir := t.TempDir()
		interrupt := make(chan struct{}, 1)
		script := "cat <<'EOF'\n" + strings.Join([]string{announceLine, partialLine("one"), partialLine("one two"), toolStartLine}, "\n") + "\nEOF\nsleep 30\n"
		col := newActivityCollector()
		ex := New(Config{Command: "bash", ExtraArgs: []string{"-c", script}, IdleTimeout: time.Hour, InterruptCh: interrupt, OnActivity: col.sink})
		done := make(chan HelperOutcome, 1)
		go func() {
			out, _ := ex.RunHelperAgent(context.Background(), "t1", "reviewer", "p", filepath.Join(dir, "h.log"), filepath.Join(dir, "r.json"))
			done <- out
		}()
		col.skipTo(t, func(ev ActivityEvent) bool { return ev.Kind == KindToolStarted })
		interrupt <- struct{}{}
		if out := <-done; !out.Interrupted {
			t.Fatalf("outcome = %+v, want Interrupted", out)
		}
		events := col.all()
		n := len(events)
		if events[n-1].Kind != KindInvocationFinished || events[n-1].Outcome != OutcomeInterrupted || count(events) != 1 {
			t.Fatalf("events = %v", kindsOf(events))
		}
		if flushed := events[n-2]; flushed.Detail != "one two" || flushed.LiveOnly {
			t.Errorf("last snapshot = %+v, want it published once, durably", flushed)
		}
		time.Sleep(50 * time.Millisecond)
		if after := col.all(); len(after) != n {
			t.Errorf("%d events published after the call returned", len(after)-n)
		}
	})
	t.Run("idle timeout", func(t *testing.T) {
		dir := t.TempDir()
		fc := clock.NewFake(time.Unix(1000, 0))
		rc := &resetSignalClock{FakeClock: fc, resets: make(chan struct{}, 16)}
		script := "cat <<'EOF'\n" + toolStartLine + "\nEOF\nsleep 30\n"
		col := newActivityCollector()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel) // a lost wake-up must not leave the fake helper sleeping
		ex := New(Config{Command: "bash", ExtraArgs: []string{"-c", script}, IdleTimeout: time.Minute, Clock: rc, OnActivity: col.sink})
		done := make(chan error, 1)
		go func() {
			_, err := ex.RunHelperAgent(ctx, "t1", "reviewer", "p", filepath.Join(dir, "h.log"), filepath.Join(dir, "r.json"))
			done <- err
		}()
		col.next(t)
		// The idle timer is restarted after the line callback returns; advancing before that would be undone by it.
		select {
		case <-rc.resets:
		case <-time.After(10 * time.Second):
			t.Fatal("the idle timer was not restarted after the line")
		}
		fc.BlockUntil(1)
		fc.Advance(time.Minute)
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("err = %v (a timeout is reported through the outcome)", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the idle timeout did not end the call")
		}
		events := col.all()
		if end := events[len(events)-1]; end.Kind != KindInvocationFinished || end.Outcome != OutcomeTimedOut || count(events) != 1 {
			t.Errorf("events = %v", kindsOf(events))
		}
	})
}

// resetSignalClock tells the test when an executor timer was restarted, so a test can advance the clock only after
// the executor has re-armed its idle timeout.
type resetSignalClock struct {
	*clock.FakeClock
	resets chan struct{}
}

func (c *resetSignalClock) NewTimer(d time.Duration) clock.Timer {
	return resetSignalTimer{Timer: c.FakeClock.NewTimer(d), resets: c.resets}
}

type resetSignalTimer struct {
	clock.Timer
	resets chan<- struct{}
}

func (t resetSignalTimer) Reset(d time.Duration) bool {
	was := t.Timer.Reset(d)
	select {
	case t.resets <- struct{}{}:
	default:
	}
	return was
}

func TestActivity_LiveOnlySnapshotIsPersistedWhenTheCallEnds(t *testing.T) {
	// The only snapshot was shown live-only; nothing durable exists until the call ends.
	r := runActivity(t, linesScript(announceLine, partialLine("shown live only")), nil)
	var durable []ActivityEvent
	for _, ev := range r.events {
		if ev.Kind == KindMessage && !ev.LiveOnly {
			durable = append(durable, ev)
		}
	}
	if len(durable) != 1 || durable[0].Detail != "shown live only" || durable[0].Partial {
		t.Errorf("durable message events = %+v, want the last displayed progress persisted once", durable)
	}
}

func TestActivity_UnfinishedSnapshotKeepsTheSecretPrefixOutOfEveryEvent(t *testing.T) {
	r := runActivity(t, linesScript(announceLine, partialLine("token sk-live-secr")), secretConfig)
	for _, ev := range r.events {
		if strings.Contains(ev.Detail, "sk-live") {
			t.Errorf("secret prefix reached a sink: %+v", ev)
		}
	}
	for _, name := range []string{"helper.jsonl", "helper.log"} {
		if strings.Contains(r.read(t, name), "sk-live") {
			t.Errorf("%s contains the secret prefix", name)
		}
	}
}

func TestActivity_UnfinishedSnapshotIsDroppedWithoutASecretList(t *testing.T) {
	r := runActivity(t, linesScript(announceLine, partialLine("maybe a secret")), func(c *Config) {
		c.Redact = func(s string) string { return s }
	})
	for _, ev := range r.events {
		if ev.Kind == KindMessage {
			t.Errorf("unfinished snapshot published without a secret list: %+v", ev)
		}
	}
}

func TestActivity_FinalWithoutItemIDLinksNoMessage(t *testing.T) {
	r := runFixture(t, "ext_v1_late_announce.jsonl")
	for _, ev := range r.events {
		if ev.Final {
			t.Errorf("unrelated progress marked as the final answer: %+v", ev)
		}
	}
}

func TestActivity_BlockedConsumerDoesNotBlockTheEndOfTheCall(t *testing.T) {
	old := activityFinishWait
	activityFinishWait = 200 * time.Millisecond
	t.Cleanup(func() { activityFinishWait = old })

	var inSink, maxInSink atomic.Int32
	release := make(chan struct{})
	blocked := make(chan struct{}, 1)
	sink := func(ev ActivityEvent) {
		n := inSink.Add(1)
		defer inSink.Add(-1)
		for {
			m := maxInSink.Load()
			if n <= m || maxInSink.CompareAndSwap(m, n) {
				break
			}
		}
		select {
		case blocked <- struct{}{}:
		default:
		}
		<-release
	}
	t.Cleanup(func() { close(release) })

	dir := t.TempDir()
	script := "cat <<'EOF'\n" + toolStartLine + "\nEOF\nsleep 30\n"
	ctx, cancel := context.WithCancel(context.Background())
	ex := New(Config{Command: "bash", ExtraArgs: []string{"-c", script}, IdleTimeout: time.Hour, OnActivity: sink})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = ex.RunHelperAgent(ctx, "t1", "reviewer", "p", filepath.Join(dir, "h.log"), filepath.Join(dir, "r.json"))
	}()
	<-blocked
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("RunHelperAgent stayed blocked behind the stuck activity consumer")
	}
	if got := maxInSink.Load(); got != 1 {
		t.Errorf("sink ran %d calls at once, want it never re-entered", got)
	}
}

func TestActivity_FinishDetailIsRedactedBeforeItIsCapped(t *testing.T) {
	cmd := "#" + strings.Repeat("x", 1986) + activitySecret + " tail"
	line := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"c1","is_error":false}]},"afm":{"exit_code":0,"tool":"Bash","detail":"` + cmd + `"}}`
	r := runActivity(t, linesScript(announceLine, line), secretConfig)
	var finish *ActivityEvent
	for i, ev := range r.events {
		if ev.Kind == KindToolFinished {
			finish = &r.events[i]
		}
	}
	if finish == nil {
		t.Fatalf("no tool_finished: %v", kindsOf(r.events))
	}
	if strings.Contains(finish.Detail, "sk-live") || len(finish.Detail) > finishDetailLimit {
		t.Errorf("detail len %d = %.60q…%q, want it redacted and capped at %d", len(finish.Detail), finish.Detail, finish.Detail[max(0, len(finish.Detail)-30):], finishDetailLimit)
	}
}
