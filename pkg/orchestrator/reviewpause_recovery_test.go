package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// newBlockingRunnerOrch builds an orchestrator over a real Store + a countingBlockingRunner, wired for the
// crash/recovery review-pause tests that must observe real agent spawns.
func newBlockingRunnerOrch(t *testing.T, stages []flow.Stage) (*Orchestrator, *countingBlockingRunner, context.Context) {
	t.Helper()
	runDir := t.TempDir()
	ids := make([]string, len(stages))
	for i, s := range stages {
		ids[i] = s.ID
	}
	store, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatal(err)
	}
	runner := &countingBlockingRunner{}
	o := New(Options{
		RunDir: runDir, Stages: stages, Store: store,
		Config: config.Default(), Prompts: DefaultPrompts(), Runner: runner,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		o.concurrency.WaitAgents()
		store.Close()
	})
	return o, runner, ctx
}

func TestRunResumeTransaction_InjectRedeliversFeedbackOnRecovery(t *testing.T) {
	o := newTestOrchestrator(t)
	seedStageStatus(t, o.opts.Store, "s1", state.StatusPaused)
	o.testRunnerHook = func(string, bool) {}

	line := 7
	orig := "\treturn nil"
	if err := state.SaveReviewNotes(o.opts.RunDir, state.ReviewNotes{
		Version: 1, NextID: 2, Rev: 1,
		Notes: []state.ReviewNote{{
			ID: "n1", Root: "project", Path: "a.go", DisplayPath: "project/a.go",
			Reference: `[AFM file: "/w/a.go"]`, Line: &line, OrigLineText: &orig,
			ContentSHA: "sha256:x", Text: "handle the error here",
		}},
	}); err != nil {
		t.Fatal(err)
	}

	m := state.PauseMarker{
		Version: 1, OperationID: "opCrash", State: state.PauseStateResuming,
		Mode: state.PauseModeInject, TargetStage: "s1",
		Owned: []state.PauseOwner{{ID: "s1", ResumeKind: kindImplementation}},
	}
	o.reviewMarker.Store(&m)

	fbPath := filepath.Join(o.opts.RunDir, "s1", "feedback.md")
	if _, err := os.Stat(fbPath); !os.IsNotExist(err) {
		t.Fatalf("precondition: feedback.md must not exist yet, stat err=%v", err)
	}

	if err := o.runResumeTransaction(context.Background(), m); err != nil {
		t.Fatalf("runResumeTransaction: %v", err)
	}

	body, err := os.ReadFile(fbPath)
	if err != nil {
		t.Fatalf("feedback.md must be written on recovery: %v", err)
	}
	if !strings.Contains(string(body), "Review notes") || !strings.Contains(string(body), "handle the error here") {
		t.Fatalf("feedback.md missing the review block:\n%s", body)
	}
}

func TestResumeOwner_ReSpawnsRunningOwnerWithoutTransition(t *testing.T) {
	o, runner, ctx := newBlockingRunnerOrch(t, []flow.Stage{
		{ID: "s1", Name: "s1", Agents: []flow.AgentType{flow.AgentAuto}},
	})
	// s1 durably in running (as a crash after the resume transition would leave it) — NOT paused.
	// autonomous.flag makes runAutonomousAgent the resolved runner path.
	seedStageStatus(t, o.opts.Store, "s1", state.StatusRunning)
	if err := os.MkdirAll(filepath.Join(o.opts.RunDir, "s1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.opts.RunDir, "s1", "autonomous.flag"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	o.resumeOwner(ctx, state.PauseOwner{ID: "s1", ResumeKind: kindAutonomous}, false)

	waitFor(t, func() bool { return runner.count("s1") == 1 })
	if got := o.currentStatus("s1"); got != state.StatusRunning {
		t.Fatalf("running owner must stay running (no extra transition), got %v", got)
	}
	// No second spawn.
	time.Sleep(100 * time.Millisecond)
	if n := runner.count("s1"); n != 1 {
		t.Fatalf("running owner spawned %d times, want exactly 1", n)
	}
}

func TestRunResumeTransaction_ReleasesHeldPendingDependent(t *testing.T) {
	o, runner, ctx := newBlockingRunnerOrch(t, []flow.Stage{
		{ID: "s_script", Name: "s_script", Script: "echo hi"},
		{ID: "s_dep", Name: "s_dep", Agents: []flow.AgentType{flow.AgentAuto}, DependsOn: []string{"s_script"}},
	})
	// The script finished during the review; its dependent was held in pending.
	seedStageStatus(t, o.opts.Store, "s_script", state.StatusDone)
	// activationHeld is already false by the time the async resume runs (Inject/
	// Cancel clear it synchronously) — mirror that precondition.
	o.activationHeld.Store(false)

	m := state.PauseMarker{
		Version: 1, OperationID: "opCancel", State: state.PauseStateResuming,
		Mode: state.PauseModeCancel, // no owners: scripts are never owned
	}
	o.reviewMarker.Store(&m)

	if err := o.runResumeTransaction(ctx, m); err != nil {
		t.Fatalf("runResumeTransaction: %v", err)
	}

	// The dependent must have been activated (autonomous agent spawned), not
	// left stranded in pending until an afm restart.
	waitFor(t, func() bool { return runner.count("s_dep") == 1 })
}

func TestSpawnKind_StampsKindSynchronously(t *testing.T) {
	o, _, ctx := newBlockingRunnerOrch(t, []flow.Stage{
		{ID: "s1", Name: "s1", Agents: []flow.AgentType{flow.AgentReview}},
	})
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })
	// Pre-seed a STALE kind to model the planning→implementation window.
	o.setRunnerKind("s1", kindPlanning)
	o.spawnKind(ctx, o.opts.Stages[0], kindImplementation, func(context.Context, flow.Stage) { <-blocked })

	if k := o.runnerKindOf("s1"); k != kindImplementation {
		t.Fatalf("spawnKind must stamp the kind synchronously (overriding stale), got %q", k)
	}
}

func TestPauseFlow_ActiveReviewOwnerResumesAsReview(t *testing.T) {
	o := newPauseFlowTestOrch(t, []flow.Stage{
		{ID: "s1", Agents: []flow.AgentType{flow.AgentReview}},
	})
	seedStageStatus(t, o.opts.Store, "s1", state.StatusRunning)
	o.setRunnerKind("s1", kindReview) // as spawnReview stamps it, before markActive
	markActiveForTest(t, o, "s1")

	if _, err := o.PauseFlow(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := o.reviewMarker.Load()
	if m == nil || len(m.Owned) != 1 || m.Owned[0].ResumeKind != kindReview {
		t.Fatalf("review owner must resume as review, got %+v", m)
	}
}

func TestAddNote_TooLongRejected(t *testing.T) {
	o := pausedNotesTestOrch(t, stubResolveFile("sha256:x", "orig", true))
	big := strings.Repeat("x", maxNoteTextBytes+1)
	if _, _, err := o.AddNote("project", "a.go", nil, big, "sha256:x", 0); !errors.Is(err, ErrNoteTooLong) {
		t.Fatalf("err=%v, want ErrNoteTooLong", err)
	}
	if onDisk, _ := state.LoadReviewNotes(o.opts.RunDir); len(onDisk.Notes) != 0 {
		t.Fatalf("oversized note must not be persisted: %+v", onDisk.Notes)
	}
}

func TestAddNote_TooManyRejected(t *testing.T) {
	o := pausedNotesTestOrch(t, stubResolveFile("sha256:x", "orig", true))
	// Seed maxNotes distinct file-level notes directly on disk.
	notes := state.ReviewNotes{Version: 1, NextID: maxNotes + 1, Rev: maxNotes}
	for i := 0; i < maxNotes; i++ {
		notes.Notes = append(notes.Notes, state.ReviewNote{
			ID: "n" + strconv.Itoa(i+1), Root: "project", Path: "f" + strconv.Itoa(i) + ".go",
			DisplayPath: "project/f" + strconv.Itoa(i) + ".go", ContentSHA: "sha256:x", Text: "n",
		})
	}
	if err := state.SaveReviewNotes(o.opts.RunDir, notes); err != nil {
		t.Fatal(err)
	}

	// A genuinely new note is rejected.
	if _, _, err := o.AddNote("project", "new.go", nil, "one more", "sha256:x", maxNotes); !errors.Is(err, ErrTooManyNotes) {
		t.Fatalf("err=%v, want ErrTooManyNotes", err)
	}
	// Replacing an existing key still works (no growth).
	if _, _, err := o.AddNote("project", "f0.go", nil, "updated", "sha256:x", maxNotes); err != nil {
		t.Fatalf("replacing an existing note at the cap must still succeed: %v", err)
	}
}

func TestRenderReviewFeedback_TruncatesLargeAggregate(t *testing.T) {
	var notes []state.ReviewNote
	// Each note ~1 KiB of text; enough of them to blow past the cap.
	body := strings.Repeat("y", 1024)
	for i := 0; i < (maxRenderedFeedbackBytes/1024)+50; i++ {
		notes = append(notes, state.ReviewNote{
			Root: "p", Path: "f.go", DisplayPath: "p/f.go", Reference: "[ref]",
			ContentSHA: "sha", Text: body,
		})
	}
	out := renderReviewFeedback(notes, func(string, string) (string, bool) { return "sha", true })
	if len(out) > maxRenderedFeedbackBytes+len("… (review notes truncated)\n") {
		t.Fatalf("rendered feedback not truncated: %d bytes", len(out))
	}
	if !strings.Contains(out, "review notes truncated") {
		t.Fatal("truncation marker missing")
	}
}

func TestResumeOwner_ReSpawnsRetryingOwnerWithoutTransition(t *testing.T) {
	o, runner, ctx := newBlockingRunnerOrch(t, []flow.Stage{
		{ID: "s1", Name: "s1", Agents: []flow.AgentType{flow.AgentAuto}},
	})
	seedStageStatus(t, o.opts.Store, "s1", state.StatusRetrying)
	if err := os.MkdirAll(filepath.Join(o.opts.RunDir, "s1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.opts.RunDir, "s1", "autonomous.flag"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	if handled := o.resumeOwner(ctx, state.PauseOwner{ID: "s1", ResumeKind: kindAutonomous}, false); !handled {
		t.Fatal("resumeOwner must report the retrying owner as handled")
	}
	waitFor(t, func() bool { return runner.count("s1") == 1 })
	if got := o.currentStatus("s1"); got != state.StatusRetrying {
		t.Fatalf("retrying owner must stay retrying (no extra transition), got %v", got)
	}
	time.Sleep(100 * time.Millisecond)
	if n := runner.count("s1"); n != 1 {
		t.Fatalf("retrying owner spawned %d times, want exactly 1", n)
	}
}

func TestRunResumeTransaction_CleanupFailureRetriesCleanupOnly(t *testing.T) {
	o, runner, ctx := newBlockingRunnerOrch(t, []flow.Stage{
		{ID: "s1", Name: "s1", Agents: []flow.AgentType{flow.AgentAuto}},
	})
	setPausedFrom(t, o.opts.Store, "s1", state.StatusRunning)
	if err := os.MkdirAll(filepath.Join(o.opts.RunDir, "s1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.opts.RunDir, "s1", "autonomous.flag"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	m := state.PauseMarker{
		Version: 1, OperationID: "op1", State: state.PauseStateResuming,
		Mode: state.PauseModeCancel, Owned: []state.PauseOwner{{ID: "s1", ResumeKind: kindAutonomous}},
	}
	o.reviewMarker.Store(&m)

	failing := errors.New("disk full")
	o.testClearReviewArtifacts = func() error { return failing }

	if err := o.runResumeTransaction(ctx, m); !errors.Is(err, failing) {
		t.Fatalf("want cleanup error surfaced, got %v", err)
	}
	waitFor(t, func() bool { return runner.count("s1") == 1 })
	if !o.reviewTxnActive() {
		t.Fatal("in-memory marker must be retained when durable cleanup fails (fail-closed)")
	}

	// Retry: cleanup now succeeds. The owner is already resumed — the retry must
	// NOT spawn it a second time.
	o.testClearReviewArtifacts = func() error { return nil }
	if err := o.runResumeTransaction(ctx, m); err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
	if o.reviewTxnActive() {
		t.Fatal("marker must be cleared once durable cleanup succeeds")
	}
	time.Sleep(100 * time.Millisecond)
	if n := runner.count("s1"); n != 1 {
		t.Fatalf("owner spawned %d times across failure+retry, want exactly 1 (no double spawn)", n)
	}
}

func TestInjectCancel_RejectedWhileResuming(t *testing.T) {
	o := newTestOrchestrator(t)
	seedStageStatus(t, o.opts.Store, "s1", state.StatusPaused)
	m := state.PauseMarker{
		Version: 1, OperationID: "op1", State: state.PauseStateResuming, Mode: state.PauseModeCancel,
		Owned: []state.PauseOwner{{ID: "s1", ResumeKind: kindImplementation}},
	}
	o.reviewMarker.Store(&m)

	if err := o.CancelNotesAndResume(context.Background()); !errors.Is(err, ErrResumeInProgress) {
		t.Fatalf("Cancel while resuming: err=%v, want ErrResumeInProgress", err)
	}
	if err := o.InjectNotesAndResume(context.Background(), "s1"); !errors.Is(err, ErrResumeInProgress) {
		t.Fatalf("Inject while resuming: err=%v, want ErrResumeInProgress", err)
	}
	// Marker untouched.
	if mm := o.reviewMarker.Load(); mm == nil || mm.State != state.PauseStateResuming {
		t.Fatalf("marker must be left in resuming, got %+v", mm)
	}
}
