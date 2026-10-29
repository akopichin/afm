package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// markerNoticeWait is one wait of a completion_marker_pending_wait notice.
type markerNoticeWait struct {
	WaitID    string                  `json:"wait_id"`
	Condition agentcomm.WaitCondition `json:"condition"`
	Status    string                  `json:"status"`
}

type markerNoticeData struct {
	Marker  string             `json:"marker"`
	Message string             `json:"message"`
	Waits   []markerNoticeWait `json:"waits"`
}

// markerNotice is one completion_marker_pending_wait line of notices.jsonl.
type markerNotice struct {
	Type    string           `json:"type"`
	StageID string           `json:"stage_id"`
	Data    markerNoticeData `json:"data"`
}

func readMarkerNotices(t *testing.T, runDir string) []markerNotice {
	t.Helper()
	f, err := os.Open(filepath.Join(runDir, "notices.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []markerNotice
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var head struct{ Type string }
		if err := json.Unmarshal(sc.Bytes(), &head); err != nil {
			t.Fatalf("notices.jsonl line %q: %v", sc.Text(), err)
		}
		if head.Type != string(eventCompletionMarkerPendingWait) {
			continue
		}
		var n markerNotice
		if err := json.Unmarshal(sc.Bytes(), &n); err != nil {
			t.Fatalf("notices.jsonl line %q: %v", sc.Text(), err)
		}
		out = append(out, n)
	}
	return out
}

// markerTurn is one author turn of stage s1: write plays the agent's file writes, registerWait makes it end on a
// pending wait. run reports whether the completion check (the gate that runs verify) was called.
type markerTurn struct {
	stage        flow.Stage // zero = a bare stage s1
	phase        string
	registerWait string // operation id of a wait registered during the turn; "" = none
	waitThread   string // thread that wait is on; "" = the shared test thread (an identical wait is deduplicated)
	write        func(stageDir string)
}

func (m markerTurn) run(t *testing.T, o *Orchestrator, runDir string) (completionChecked bool) {
	t.Helper()
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stage := m.stage
	if stage.ID == "" {
		stage.ID = "s1"
	}
	o.runWithRetry(context.Background(), stage, m.phase,
		func(string) error {
			if m.write != nil {
				m.write(stageDir)
			}
			if m.registerWait != "" {
				m.registerStageWait(t, o)
			}
			return nil
		},
		func() error { completionChecked = true; return nil },
		func() { t.Error("onUserInterrupted should not be called") },
	)
	return completionChecked
}

func (m markerTurn) registerStageWait(t *testing.T, o *Orchestrator) {
	t.Helper()
	if m.waitThread == "" {
		registerTestStageWait(t, o, "s1", m.registerWait)
		return
	}
	_, err := o.Wait(context.Background(), WaitRequest{
		OperationID: m.registerWait, Waiter: meshStageParticipantID("s1"), Deadline: testFarDeadline(),
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: agentcomm.ThreadID(m.waitThread)},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// sawLiveEvent drains what the UI bus has buffered and reports whether an event of the given type was in it.
func sawLiveEvent(live <-chan bus.Event, typ bus.EventType) bool {
	for {
		select {
		case ev := <-live:
			if ev.Type == typ {
				return true
			}
		default:
			return false
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A completion marker that appears during a turn which ends on a pending wait is a conflict: the stage parks
// (no completion, no verify) and the run learns about it through one durable notice.
func TestCompletionMarker_FreshMarkerWithPendingWaitPublishesOneNotice(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.absRunDir = runDir
	_, live := o.ui.Subscribe(64)

	checked := markerTurn{
		phase: phaseImplementation, registerWait: "op-marker-1",
		write: func(dir string) { writeFile(t, filepath.Join(dir, ".done"), "done") },
	}.run(t, o, runDir)

	if checked {
		t.Fatal("the completion check (which runs verify) must not run while the stage waits for peers")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusAwaitingPeer {
		t.Fatalf("status = %v, want awaiting_peer", got)
	}
	notices := readMarkerNotices(t, runDir)
	if len(notices) != 1 {
		t.Fatalf("got %d completion-marker notices, want exactly 1: %+v", len(notices), notices)
	}
	if !sawLiveEvent(live, eventCompletionMarkerPendingWait) {
		t.Error("the notice must also reach live clients")
	}
	n := notices[0]
	waitID := string(deriveMeshWaitID("op-marker-1"))
	if n.StageID != "s1" || n.Data.Marker != ".done" {
		t.Errorf("notice stage/marker = %q/%q, want s1/.done", n.StageID, n.Data.Marker)
	}
	if len(n.Data.Waits) != 1 || n.Data.Waits[0].WaitID != waitID || n.Data.Waits[0].Status != string(agentcomm.WaitPending) {
		t.Errorf("notice waits = %+v, want the one pending wait %s", n.Data.Waits, waitID)
	}
	if n.Data.Waits[0].Condition.Kind == "" || n.Data.Message == "" {
		t.Errorf("notice must name the wait condition and carry an action hint: %+v", n)
	}
}

// The autonomous track completes on execution_summary.md, so that is the marker it is diagnosed by.
func TestCompletionMarker_AutonomousTrackUsesExecutionSummary(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.absRunDir = runDir

	markerTurn{
		phase: phaseAutonomous, registerWait: "op-marker-auto",
		write: func(dir string) { writeFile(t, filepath.Join(dir, "execution_summary.md"), "# summary") },
	}.run(t, o, runDir)

	notices := readMarkerNotices(t, runDir)
	if len(notices) != 1 || notices[0].Data.Marker != "execution_summary.md" {
		t.Fatalf("notices = %+v, want one for execution_summary.md", notices)
	}
}

// An old marker is not news: a turn that only yields on a wait, with the marker untouched, gets no diagnostic.
func TestCompletionMarker_OldMarkerWithGenuineYieldIsQuiet(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.absRunDir = runDir
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(stageDir, ".done"), "left by an earlier turn")

	markerTurn{phase: phaseImplementation, registerWait: "op-marker-old"}.run(t, o, runDir)

	if got := o.opts.Store.Get("s1"); got != state.StatusAwaitingPeer {
		t.Fatalf("status = %v, want awaiting_peer", got)
	}
	if n := readMarkerNotices(t, runDir); len(n) != 0 {
		t.Fatalf("an unchanged marker must not be reported, got %+v", n)
	}
}

// A marker the turn rewrote is a change, even though it existed before.
func TestCompletionMarker_RewrittenMarkerPublishesNotice(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.absRunDir = runDir
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(stageDir, ".done"), "v1")

	markerTurn{
		phase: phaseImplementation, registerWait: "op-marker-rewrite",
		write: func(dir string) { writeFile(t, filepath.Join(dir, ".done"), "version two, longer") },
	}.run(t, o, runDir)

	if n := readMarkerNotices(t, runDir); len(n) != 1 {
		t.Fatalf("a rewritten marker must be reported once, got %+v", n)
	}
}

// A marker the completion probe rejects (empty) is not a completion signal, so there is nothing to diagnose.
func TestCompletionMarker_EmptyMarkerIsNotASignal(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.absRunDir = runDir

	markerTurn{
		phase: phaseImplementation, registerWait: "op-marker-empty",
		write: func(dir string) { writeFile(t, filepath.Join(dir, ".done"), "  \n") },
	}.run(t, o, runDir)

	if n := readMarkerNotices(t, runDir); len(n) != 0 {
		t.Fatalf("an empty marker must not be reported, got %+v", n)
	}
}

// Without a pending wait a fresh marker is the ordinary completion path: no notice, the check runs.
func TestCompletionMarker_FreshMarkerWithoutWaitCompletesQuietly(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.absRunDir = runDir

	checked := markerTurn{
		phase: phaseImplementation,
		write: func(dir string) { writeFile(t, filepath.Join(dir, ".done"), "done") },
	}.run(t, o, runDir)

	if !checked {
		t.Fatal("no wait: the completion check must run")
	}
	if n := readMarkerNotices(t, runDir); len(n) != 0 {
		t.Fatalf("no wait, no diagnostic, got %+v", n)
	}
}

// A turn that resumes after the park (restart or a woken wait) finds the marker unchanged and stays quiet: the
// conflict is reported once, when the marker appeared.
func TestCompletionMarker_ResumedTurnDoesNotReportTheSameMarkerAgain(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.absRunDir = runDir

	markerTurn{
		phase: phaseImplementation, registerWait: "op-marker-resume",
		write: func(dir string) { writeFile(t, filepath.Join(dir, ".done"), "done") },
	}.run(t, o, runDir)
	// The park left the stage in awaiting_peer; a second turn of the same stage (the wait still pending) yields again.
	if err := o.opts.Store.Apply(&state.Transition{StageID: "s1", From: state.StatusAwaitingPeer, To: state.StatusRunning, Event: "test_resume"}); err != nil {
		t.Fatal(err)
	}
	markerTurn{phase: phaseImplementation}.run(t, o, runDir)

	notices := readMarkerNotices(t, runDir)
	if len(notices) != 1 {
		t.Fatalf("got %d notices, want 1 (only the turn that wrote the marker)", len(notices))
	}
}

// Marker identity is its content and mtime, not its size: a same-size rewrite is a change whether the agent's
// write moved the mtime or restored it.
func TestCompletionMarker_SameSizeRewriteIsDetectedWhateverTheMtime(t *testing.T) {
	cases := map[string]func(path string, old os.FileInfo){
		"mtime moved":    func(path string, old os.FileInfo) { chtimes(t, path, old.ModTime().Add(time.Hour)) },
		"mtime restored": func(path string, old os.FileInfo) { chtimes(t, path, old.ModTime()) },
	}
	for name, touch := range cases {
		t.Run(name, func(t *testing.T) {
			o, runDir := setupHookOrch(t, "s1")
			o.absRunDir = runDir
			stageDir := filepath.Join(runDir, "s1")
			if err := os.MkdirAll(stageDir, 0o755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(stageDir, ".done")
			writeFile(t, marker, "aaaa")
			old, err := os.Stat(marker)
			if err != nil {
				t.Fatal(err)
			}

			markerTurn{
				phase: phaseImplementation, registerWait: "op-marker-samesize",
				write: func(string) { writeFile(t, marker, "bbbb"); touch(marker, old) },
			}.run(t, o, runDir)

			if n := readMarkerNotices(t, runDir); len(n) != 1 {
				t.Fatalf("a same-size rewrite must be reported once, got %+v", n)
			}
		})
	}
}

// Identical content written again with a new mtime is still a write the agent made this turn.
func TestCompletionMarker_RewriteWithTheSameContentAndANewMtimeIsDetected(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.absRunDir = runDir
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(stageDir, ".done")
	writeFile(t, marker, "done")
	old, _ := os.Stat(marker)

	markerTurn{
		phase: phaseImplementation, registerWait: "op-marker-same-content",
		write: func(string) { writeFile(t, marker, "done"); chtimes(t, marker, old.ModTime().Add(time.Hour)) },
	}.run(t, o, runDir)

	if n := readMarkerNotices(t, runDir); len(n) != 1 {
		t.Fatalf("got %+v, want one notice", n)
	}
}

// A marker that was already there and did not change stays quiet even when something else (a declared artifact)
// makes it pass the completion probe during the turn: validity is not freshness.
func TestCompletionMarker_UnchangedMarkerThatBecomesValidIsNotFresh(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.absRunDir = runDir
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(stageDir, ".done"), "left by an earlier turn")
	stage := flow.Stage{ID: "s1", Artifacts: []flow.Artifact{{Name: "report", Path: "./report.md"}}}

	markerTurn{
		stage: stage, phase: phaseImplementation, registerWait: "op-marker-artifact",
		write: func(dir string) { writeFile(t, filepath.Join(dir, "report.md"), "# report") },
	}.run(t, o, runDir)

	if n := readMarkerNotices(t, runDir); len(n) != 0 {
		t.Fatalf("the marker did not change this turn, got %+v", n)
	}
}

// The callback turn of a stage still parked on the same wait rewrites the marker again: the conflict is one
// episode, so one notice — and a restart (nothing but notices.jsonl survives) sees the same.
func TestCompletionMarker_OneNoticePerPendingEpisode(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.absRunDir = runDir
	resume := func() {
		if err := o.opts.Store.Apply(&state.Transition{StageID: "s1", From: state.StatusAwaitingPeer, To: state.StatusRunning, Event: "test_resume"}); err != nil {
			t.Fatal(err)
		}
	}

	markerTurn{
		phase: phaseImplementation, registerWait: "op-episode-1",
		write: func(dir string) { writeFile(t, filepath.Join(dir, ".done"), "first") },
	}.run(t, o, runDir)
	resume()
	markerTurn{ // the callback turn: the wait is still pending, the marker is written again
		phase: phaseImplementation,
		write: func(dir string) { writeFile(t, filepath.Join(dir, ".done"), "second, longer") },
	}.run(t, o, runDir)
	if n := readMarkerNotices(t, runDir); len(n) != 1 {
		t.Fatalf("the same pending wait must be reported once, got %d: %+v", len(n), n)
	}

	resume()
	markerTurn{ // a new wait makes a new episode
		phase: phaseImplementation, registerWait: "op-episode-2", waitThread: "t-second",
		write: func(dir string) { writeFile(t, filepath.Join(dir, ".done"), "third, even longer") },
	}.run(t, o, runDir)
	if n := readMarkerNotices(t, runDir); len(n) != 2 {
		t.Fatalf("a new pending episode needs its own notice, got %d: %+v", len(n), n)
	}
}

func chtimes(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}
