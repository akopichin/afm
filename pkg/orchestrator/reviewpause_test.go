package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

func TestRunnerKind_SetGetClear(t *testing.T) {
	o := &Orchestrator{}
	if o.runnerKindOf("s1") != "" {
		t.Fatal("empty default")
	}
	o.setRunnerKind("s1", kindImplementation)
	if o.runnerKindOf("s1") != kindImplementation {
		t.Fatal("set/get")
	}
	o.clearRunnerKind("s1")
	if o.runnerKindOf("s1") != "" {
		t.Fatal("clear")
	}
}

func TestRunWithRetry_Attempt0ResumeContext(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Валидная stream-json строка позволяет отличить отсутствие retry-контекста от отсутствия данных для
	// него.
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"echo prior work"}}]}}`
	if err := os.WriteFile(filepath.Join(stageDir, "implementation.jsonl"), []byte(line+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Baseline: без arm первая (и единственная — agentFn успешен) попытка НЕ
	// получает retry-контекст.
	var unarmedCtx string
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(retryContext string) error { unarmedCtx = retryContext; return nil },
		nil, func() {})
	if unarmedCtx != "" {
		t.Fatalf("unarmed attempt-0 context should be empty, got %q", unarmedCtx)
	}

	o.armResumeContext("s1")

	var armedCtx string
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(retryContext string) error { armedCtx = retryContext; return nil },
		nil, func() {})
	if armedCtx == "" {
		t.Fatal("armed attempt-0 context should be non-empty")
	}

	// Флаг однократный: следующий вызов той же стадии без повторного arm
	// снова не должен получить контекст на attempt 0.
	var secondCtx string
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(retryContext string) error { secondCtx = retryContext; return nil },
		nil, func() {})
	if secondCtx != "" {
		t.Fatalf("resume-context flag must be consumed after first use, got %q on next call", secondCtx)
	}
}

func TestRunWithRetry_SelfAbortsWhenAlreadyPaused(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	if err := o.opts.Store.Apply(&state.Transition{StageID: "s1", From: state.StatusRunning, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	called := false
	agentFn := func(string) error { called = true; return nil }
	done := make(chan struct{})
	go func() {
		o.runWithRetry(context.Background(), flow.Stage{ID: "s1", Name: "s1"}, phaseImplementation,
			agentFn, func() error { return nil }, func() {})
		close(done)
	}()
	<-done

	if called {
		t.Fatal("agentFn must not run when stage is already paused")
	}
}

func TestActivationHold_BlocksNewActivation(t *testing.T) {
	dir := t.TempDir()
	stages := []flow.Stage{{ID: "s1", Agents: []flow.AgentType{flow.AgentImplementation}}}
	store, err := state.Open(dir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Стадия без зависимостей, уже в Ready — единственное, что мешает
	// startReadyStages её забрать, должен быть activationHeld.
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusPending, To: state.StatusReady, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	cb := bus.NewCriticalBus(16)
	o := &Orchestrator{
		opts:        Options{RunDir: dir, Stages: stages, Store: store},
		graph:       graph.NewRegistry(stages),
		fsm:         bus.NewFSM(store),
		ui:          bus.NewUIBus(),
		critical:    cb,
		concurrency: concurrency.NewWithSemaphores(cb, map[string]concurrency.Semaphore{}, ""),
	}
	o.activationHeld.Store(true)

	o.startReadyStages(context.Background())

	if got := o.currentStatus("s1"); got != state.StatusReady {
		t.Fatalf("held stage should stay ready, got %v", got)
	}
}

// newPauseFlowTestOrch создаёт реальные Store/Orchestrator с несколькими стадиями; script-тип задаётся до
// построения графа.
func newPauseFlowTestOrch(t *testing.T, stages []flow.Stage) *Orchestrator {
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
	t.Cleanup(func() { store.Close() })
	return New(Options{RunDir: runDir, Stages: stages, Store: store, Config: config.Default()})
}

// seedStageStatus переводит стадию id из state.StatusPending прямо в status одной транзакцией.
func seedStageStatus(t *testing.T, store *state.Store, id string, status state.StageStatus) {
	t.Helper()
	if err := store.Apply(&state.Transition{StageID: id, From: state.StatusPending, To: status, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
}

// seedScriptStage seeds a declared script stage that PauseFlow must skip.
func seedScriptStage(t *testing.T, o *Orchestrator, id string, status state.StageStatus) {
	t.Helper()
	stage := o.graph.Stage(id)
	if stage == nil || !stage.IsScript() {
		t.Fatalf("seedScriptStage: %q is not declared as a script stage in the graph", id)
	}
	seedStageStatus(t, o.opts.Store, id, status)
}

// testActiveRelease хранит idempotent release по stageID для проверки drain прямо во время теста.
var testActiveRelease sync.Map // stageID -> func() (закрывает release-канал не более раза)

// markActiveForTest делает concurrency.Manager.IsActive истинным по- настоящему, через реальный
// SpawnAgent.
func markActiveForTest(t *testing.T, o *Orchestrator, stageID string) {
	t.Helper()
	ready := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	releaseFn := func() { once.Do(func() { close(release) }) }
	testActiveRelease.Store(stageID, releaseFn)
	o.concurrency.SpawnAgent(context.Background(), flow.Stage{ID: stageID}, func(context.Context, flow.Stage) {
		close(ready)
		<-release
	})
	t.Cleanup(func() {
		releaseFn()
		testActiveRelease.Delete(stageID)
	})
	<-ready
}

// markDoneForTest releases an active stage and waits until IsActive becomes false.
func markDoneForTest(t *testing.T, o *Orchestrator, stageID string) {
	t.Helper()
	v, ok := testActiveRelease.Load(stageID)
	if !ok {
		t.Fatalf("markDoneForTest(%q): not marked active (call markActiveForTest first)", stageID)
	}
	v.(func())()
	for o.concurrency.IsActive(stageID) {
		time.Sleep(time.Millisecond)
	}
}

// setPausedFrom derives PausedFrom through real pending → from → paused transitions; requires pending and
// from != paused.
func setPausedFrom(t *testing.T, store *state.Store, id string, from state.StageStatus) {
	t.Helper()
	if from != state.StatusPending {
		if err := store.Apply(&state.Transition{StageID: id, From: state.StatusPending, To: from, Event: "test_setup"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Apply(&state.Transition{StageID: id, From: from, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
}

// boolStr formats observer results for assertions such as implementation/true.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestPauseFlow_OwnsOnlyActiveWinners(t *testing.T) {
	stages := []flow.Stage{
		{ID: "s1", Agents: []flow.AgentType{flow.AgentImplementation}},
		{ID: "s2", Agents: []flow.AgentType{flow.AgentImplementation}},
		{ID: "s3", Script: "echo hi"},
	}
	o := newPauseFlowTestOrch(t, stages)

	// s1: running + IsActive -> owned as implementation.
	seedStageStatus(t, o.opts.Store, "s1", state.StatusRunning)
	o.setRunnerKind("s1", kindImplementation)
	markActiveForTest(t, o, "s1") // helper making IsActive("s1")==true

	// s2: running but NOT active (queued) -> not owned.
	seedStageStatus(t, o.opts.Store, "s2", state.StatusRunning)

	// s3: script running -> not owned.
	seedScriptStage(t, o, "s3", state.StatusRunning)

	owners, err := o.PauseFlow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 1 || owners[0] != "s1" {
		t.Fatalf("owners = %v, want [s1]", owners)
	}
	if o.currentStatus("s1") != state.StatusPaused {
		t.Fatal("s1 not paused")
	}
	if o.currentStatus("s2") != state.StatusRunning {
		t.Fatalf("s2 must be left running (not owned), got %v", o.currentStatus("s2"))
	}
	if o.currentStatus("s3") != state.StatusRunning {
		t.Fatalf("s3 (script) must be left running, got %v", o.currentStatus("s3"))
	}

	m := o.reviewMarker.Load()
	if m == nil || m.State != state.PauseStatePaused || len(m.Owned) != 1 || m.Owned[0].ResumeKind != kindImplementation {
		t.Fatalf("marker wrong: %+v", m)
	}
	if !o.activationHeld.Load() {
		t.Fatal("activationHeld must be set")
	}
}

// TestPauseFlow_RejectedDuringFinalization закрывает Task 10: PauseFlow не
// должен запускать новый ревью-раунд поверх ещё финализирующегося.
func TestPauseFlow_RejectedDuringFinalization(t *testing.T) {
	o := newPauseFlowTestOrch(t, []flow.Stage{{ID: "s1", Agents: []flow.AgentType{flow.AgentImplementation}}})
	o.finalizing.Store(true)
	if _, err := o.PauseFlow(context.Background()); !errors.Is(err, ErrRunFinalizing) {
		t.Fatalf("want ErrRunFinalizing, got %v", err)
	}
}

func TestRenderReviewFeedback_AlwaysCarriesOriginal(t *testing.T) {
	l42 := 42
	orig := "\tif err != nil {"
	notes := []state.ReviewNote{{
		Root: "project", Path: "a.go", DisplayPath: "project/a.go",
		Reference: `[AFM file: "/w/a.go"]`, Line: &l42, OrigLineText: &orig,
		ContentSHA: "sha256:OLD", Text: "handle this",
	}}
	// current sha differs -> changed-at-injection warning, but original still present.
	out := renderReviewFeedback(notes, func(root, path string) (string, bool) {
		return "sha256:NEW", true
	})
	if !strings.Contains(out, `original: "\tif err != nil {"`) {
		t.Fatalf("original line text missing:\n%s", out)
	}
	if !strings.Contains(out, "content differed at injection") {
		t.Fatalf("drift warning missing:\n%s", out)
	}
	if !strings.Contains(out, `[AFM file: "/w/a.go"]`) {
		t.Fatal("reference marker missing")
	}
}

func TestRenderReviewFeedback_UnavailableFile(t *testing.T) {
	l5 := 5
	orig := "package gone"
	notes := []state.ReviewNote{{DisplayPath: "project/gone.go", Reference: `[AFM file: "/w/gone.go"]`,
		Line: &l5, OrigLineText: &orig, ContentSHA: "sha256:X", Text: "note"}}
	out := renderReviewFeedback(notes, func(root, path string) (string, bool) { return "", false })
	if !strings.Contains(out, "file unavailable at injection") {
		t.Fatalf("unavailable marker missing:\n%s", out)
	}
}

func TestRenderReviewFeedback_TwoFileLevelNotesStableOrder(t *testing.T) {
	l10 := 10
	lineOrig := "var x int"

	// Три замечания: одно на строке 10, два на файле (Line==nil)
	notes := []state.ReviewNote{
		{
			Root: "proj", Path: "a.go", DisplayPath: "proj/a.go",
			Reference: `[AFM file: "/w/a.go"]`, Line: &l10, OrigLineText: &lineOrig,
			ContentSHA: "sha256:ABC", Text: "add type annotation",
		},
		{
			Root: "proj", Path: "a.go", DisplayPath: "proj/a.go",
			Reference: `[AFM file: "/w/a.go"]`, Line: nil, OrigLineText: nil,
			ContentSHA: "sha256:ABC", Text: "file-level comment 1",
		},
		{
			Root: "proj", Path: "a.go", DisplayPath: "proj/a.go",
			Reference: `[AFM file: "/w/a.go"]`, Line: nil, OrigLineText: nil,
			ContentSHA: "sha256:ABC", Text: "file-level comment 2",
		},
	}
	out := renderReviewFeedback(notes, func(root, path string) (string, bool) {
		return "sha256:ABC", true
	})

	// Проверяем, что выход содержит оба file-level замечания.
	if !strings.Contains(out, "file-level comment 1") {
		t.Fatalf("file-level comment 1 missing:\n%s", out)
	}
	if !strings.Contains(out, "file-level comment 2") {
		t.Fatalf("file-level comment 2 missing:\n%s", out)
	}

	// Проверяем порядок: line note (Line 10) должен быть ПЕРЕД file-level нотами.
	pos1 := strings.Index(out, "Line 10")
	pos2 := strings.Index(out, "file-level comment 1")
	pos3 := strings.Index(out, "file-level comment 2")

	if pos1 < 0 || pos2 < 0 || pos3 < 0 {
		t.Fatalf("missing required text in output:\n%s", out)
	}
	if pos1 >= pos2 || pos1 >= pos3 {
		t.Fatalf("line note must come before file-level notes, got positions: line=%d, file1=%d, file2=%d", pos1, pos2, pos3)
	}
}

func TestResumeOwner_TargetImplementationUsesWithFeedback(t *testing.T) {
	o := newTestOrchestrator(t)
	setPausedFrom(t, o.opts.Store, "s1", state.StatusRunning)

	var chosen string
	o.testRunnerHook = func(kind string, withFeedback bool) { chosen = kind + "/" + boolStr(withFeedback) }

	o.resumeOwner(context.Background(), state.PauseOwner{ID: "s1", ResumeKind: kindImplementation}, true)

	if chosen != "implementation/true" {
		t.Fatalf("target should use implementation WithFeedback, got %q", chosen)
	}
}

func TestResumeOwner_WaitsForDrain(t *testing.T) {
	o := newTestOrchestrator(t)
	o.testRunnerHook = func(string, bool) {}
	seedStageStatus(t, o.opts.Store, "s1", state.StatusRunning)
	markActiveForTest(t, o, "s1")
	if err := o.opts.Store.Apply(&state.Transition{StageID: "s1", From: state.StatusRunning, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	go func() {
		o.resumeOwner(context.Background(), state.PauseOwner{ID: "s1", ResumeKind: kindImplementation}, false)
		close(started)
	}()

	select {
	case <-started:
		t.Fatal("resume must block until IsActive(s1)==false")
	case <-time.After(50 * time.Millisecond):
	}

	markDoneForTest(t, o, "s1") // IsActive -> false

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("resume should proceed after drain")
	}
}

// waitFor polls a condition every 10ms, failing after 2s.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !cond() {
		t.Fatal("waitFor: condition not met within timeout")
	}
}

func TestInject_WritesFeedbackOnceAndResumes(t *testing.T) {
	o := newTestOrchestrator(t)
	seedStageStatus(t, o.opts.Store, "s1", state.StatusPaused)
	m := state.PauseMarker{Version: 1, OperationID: "op1", State: state.PauseStatePaused,
		Owned: []state.PauseOwner{{ID: "s1", ResumeKind: kindImplementation}}}
	if err := state.WriteNotesPauseMarker(o.opts.RunDir, m); err != nil {
		t.Fatal(err)
	}
	o.reviewMarker.Store(&m)
	o.activationHeld.Store(true)

	l := 1
	orig := "x"
	if err := state.SaveReviewNotes(o.opts.RunDir, state.ReviewNotes{Version: 1, NextID: 2,
		Notes: []state.ReviewNote{{ID: "n1", Root: "project", Path: "a.go", DisplayPath: "project/a.go",
			Reference: `[AFM file: "/w/a.go"]`, Line: &l, OrigLineText: &orig, ContentSHA: "sha256:x", Text: "fix"}}}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var resumed []string
	o.testRunnerHook = func(kind string, _ bool) {
		mu.Lock()
		resumed = append(resumed, kind)
		mu.Unlock()
	}

	if err := o.InjectNotesAndResume(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}

	// Synchronous effects: assertable immediately, before the async worker runs.
	if o.activationHeld.Load() {
		t.Fatal("activationHeld should be cleared synchronously")
	}
	body, err := os.ReadFile(filepath.Join(o.opts.RunDir, "s1", "feedback.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(body), "fix") != 1 {
		t.Fatalf("feedback not written once:\n%s", body)
	}

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(resumed) == 1
	}) // async worker ran
	mu.Lock()
	if resumed[0] != kindImplementation {
		t.Fatalf("resumed kind = %q, want %q", resumed[0], kindImplementation)
	}
	mu.Unlock()

	waitFor(t, func() bool { return !o.reviewTxnActive() })
	if _, found, _ := state.ReadNotesPauseMarker(o.opts.RunDir); found {
		t.Fatal("marker should be deleted after resume")
	}
	if notes, err := state.LoadReviewNotes(o.opts.RunDir); err != nil || len(notes.Notes) != 0 {
		t.Fatalf("notes should be deleted after resume, got %+v (err=%v)", notes, err)
	}
}

func TestInject_RejectsUnpausedTargetAndEmptyNotes(t *testing.T) {
	o := newTestOrchestrator(t)

	// No review-pause marker at all.
	if err := o.InjectNotesAndResume(context.Background(), "s1"); !errors.Is(err, ErrNoReviewPause) {
		t.Fatalf("want ErrNoReviewPause, got %v", err)
	}

	// Marker present, s1 owned, but target not (yet) paused -> ErrTargetNotPaused.
	m := state.PauseMarker{Version: 1, OperationID: "op1", State: state.PauseStatePaused,
		Owned: []state.PauseOwner{{ID: "s1", ResumeKind: kindImplementation}}}
	o.reviewMarker.Store(&m)
	if err := o.InjectNotesAndResume(context.Background(), "s1"); !errors.Is(err, ErrTargetNotPaused) {
		t.Fatalf("want ErrTargetNotPaused, got %v", err)
	}

	// Target not owned by the marker at all -> ErrNotOwned.
	seedStageStatus(t, o.opts.Store, "s1", state.StatusPaused)
	unowned := state.PauseMarker{Version: 1, OperationID: "op1", State: state.PauseStatePaused}
	o.reviewMarker.Store(&unowned)
	if err := o.InjectNotesAndResume(context.Background(), "s1"); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("want ErrNotOwned, got %v", err)
	}

	// Owned and paused, but no review notes on disk -> ErrNoNotes.
	o.reviewMarker.Store(&m)
	if err := o.InjectNotesAndResume(context.Background(), "s1"); !errors.Is(err, ErrNoNotes) {
		t.Fatalf("want ErrNoNotes, got %v", err)
	}

	// None of the rejected paths should have written a feedback.md.
	if _, err := os.Stat(filepath.Join(o.opts.RunDir, "s1", "feedback.md")); !os.IsNotExist(err) {
		t.Fatalf("feedback.md should not exist after rejected injections, stat err=%v", err)
	}
}

// makeAllTerminal moves every stage of o's flow directly to StatusDone so that allTerminal — and therefore
// shouldExit, absent any other gate — would report true.
func makeAllTerminal(t *testing.T, o *Orchestrator) {
	t.Helper()
	for _, s := range o.opts.Stages {
		seedStageStatus(t, o.opts.Store, s.ID, state.StatusDone)
	}
}

func TestControlPolicy_RejectsWhileReviewPaused(t *testing.T) {
	o := newTestOrchestrator(t)
	m := state.PauseMarker{Version: 1, State: state.PauseStatePaused}
	o.reviewMarker.Store(&m)
	if err := o.Continue(context.Background(), "s1"); !errors.Is(err, ErrFlowPaused) {
		t.Fatalf("Continue want ErrFlowPaused, got %v", err)
	}
	if err := o.Retry(context.Background(), "s1"); !errors.Is(err, ErrFlowPaused) {
		t.Fatalf("Retry want ErrFlowPaused, got %v", err)
	}
}

func TestShouldExit_HeldByReviewMarker(t *testing.T) {
	o := newTestOrchestrator(t)
	makeAllTerminal(t, o) // so shouldExit would otherwise be true
	m := state.PauseMarker{Version: 1, State: state.PauseStateResuming}
	o.reviewMarker.Store(&m)
	if o.shouldExit() {
		t.Fatal("shouldExit must be false while review marker exists")
	}
}

func TestRecoverReviewPause_PausedReestablishesHold(t *testing.T) {
	o := newTestOrchestrator(t)
	m := state.PauseMarker{Version: 1, State: state.PauseStatePaused,
		Owned: []state.PauseOwner{{ID: "s1", ResumeKind: kindImplementation}}}
	if err := state.WriteNotesPauseMarker(o.opts.RunDir, m); err != nil {
		t.Fatal(err)
	}
	if err := o.recoverReviewPause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !o.activationHeld.Load() || !o.reviewTxnActive() {
		t.Fatal("paused recovery must re-establish the hold + marker")
	}
}

func TestRecoverReviewPause_ResumingFinishes(t *testing.T) {
	o := newTestOrchestrator(t)
	seedStageStatus(t, o.opts.Store, "s1", state.StatusPaused)
	m := state.PauseMarker{Version: 1, OperationID: "op1", State: state.PauseStateResuming,
		Mode: state.PauseModeCancel, Owned: []state.PauseOwner{{ID: "s1", ResumeKind: kindImplementation}}}
	if err := state.WriteNotesPauseMarker(o.opts.RunDir, m); err != nil {
		t.Fatal(err)
	}
	o.testRunnerHook = func(string, bool) {}
	if err := o.recoverReviewPause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := state.ReadNotesPauseMarker(o.opts.RunDir); found {
		t.Fatal("resuming recovery must finish and delete the marker")
	}
}

// countingBlockingRunner считает запуски по стадиям и блокируется до cancel, чтобы повторный spawn был
// наблюдаем.
type countingBlockingRunner struct {
	mu    sync.Mutex
	calls map[string]int
}

func (r *countingBlockingRunner) RunAgent(ctx context.Context, _, stageName, _, _ string) error {
	r.mu.Lock()
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	r.calls[stageName]++
	r.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

func (r *countingBlockingRunner) RunPlanning(context.Context, string, string, string, string) error {
	return nil
}

func (r *countingBlockingRunner) RunJSONQuery(context.Context, string) ([]byte, error) {
	return nil, nil
}

func (r *countingBlockingRunner) count(stageName string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[stageName]
}

var _ executor.Runner = (*countingBlockingRunner)(nil)

func TestRecoverReviewPause_ResumingDoesNotDoubleSpawn(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "s1", Agents: []flow.AgentType{flow.AgentAuto}}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	runner := &countingBlockingRunner{}
	o := New(Options{
		RunDir: runDir, Stages: []flow.Stage{stage}, Store: store,
		Config: config.Default(), Prompts: DefaultPrompts(), Runner: runner,
	})

	// s1 — durably paused-from-running (как его оставил бы PauseFlow до краха);
	// маркер на диске говорит, что resume уже был в полёте (Mode=cancel).
	setPausedFrom(t, store, "s1", state.StatusRunning)
	m := state.PauseMarker{
		Version: 1, OperationID: "op1", State: state.PauseStateResuming,
		Mode:  state.PauseModeCancel,
		Owned: []state.PauseOwner{{ID: "s1", ResumeKind: kindAutonomous}},
	}
	if err := state.WriteNotesPauseMarker(runDir, m); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	// LIFO cleanup отменяет и ждёт агентов до store.Close, исключая запись в закрытый eventsLog.
	t.Cleanup(func() {
		cancel()
		o.concurrency.WaitAgents()
	})

	// Recovery доводит resume-транзакцию до конца: s1 выходит из paused, его
	// autonomous-раннер спавнится РОВНО ОДИН раз, маркер удаляется.
	if err := o.recoverReviewPause(ctx); err != nil {
		t.Fatalf("recoverReviewPause: %v", err)
	}
	waitFor(t, func() bool { return runner.count("s1") == 1 })

	// Bootstrap — ровно как Run() зовёт его сразу после recovery. Без guard'а reviewResumed он увидел бы s1 в
	// Running и resumeStageAtStatus заспавнил бы ВТОРОГО автономного агента в той же стадии.
	o.startPlanningForPending(ctx)

	// Даём возможному ошибочному второму спавну время дойти до RunAgent и
	// утверждаем ровно один спавн на s1 за recovery+bootstrap.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if n := runner.count("s1"); n > 1 {
			t.Fatalf("s1 spawned %d times: bootstrap double-spawned an owner already resumed by review recovery", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := runner.count("s1"); n != 1 {
		t.Fatalf("s1 spawn count = %d, want exactly 1", n)
	}
}

func TestRecoverReviewPause_Absent(t *testing.T) {
	o := newTestOrchestrator(t)
	if err := o.recoverReviewPause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if o.activationHeld.Load() || o.reviewTxnActive() {
		t.Fatal("no marker on disk: recovery must be a no-op")
	}
}

func TestRecoverReviewPause_CorruptResumingFailsClosed(t *testing.T) {
	o := newTestOrchestrator(t)
	path := filepath.Join(o.opts.RunDir, "notes-pause.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"state":"resuming",`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := o.recoverReviewPause(context.Background()); err == nil {
		t.Fatal("corrupt resuming marker must fail closed with a non-nil error")
	}
	if !o.activationHeld.Load() {
		t.Fatal("corrupt resuming marker must keep activationHeld set")
	}
	// P2 #1: a real in-memory poison marker must be cached so control policy,
	// /api/status, and shouldExit all keep treating the review round as active.
	if !o.reviewTxnActive() {
		t.Fatal("corrupt resuming marker must cache a poison reviewMarker (reviewTxnActive)")
	}
	if st, _ := o.ReviewState(); st != state.PauseStateResuming {
		t.Fatalf("ReviewState = %q, want resuming (poison state visible in /api/status)", st)
	}
	if o.shouldExit() {
		t.Fatal("shouldExit must stay false while the poison marker is present")
	}
	if err := o.Continue(context.Background(), "s1"); !errors.Is(err, ErrFlowPaused) {
		t.Fatalf("controls must be rejected under the poison marker, got %v", err)
	}
	// P2 (re-review): the poison must be recorded DURABLY so it survives restarts.
	if !state.HasPoisonMarker(o.opts.RunDir) {
		t.Fatal("corrupt resuming marker must write a durable poison breadcrumb")
	}
}

func TestRecoverReviewPause_PoisonSurvivesRestart(t *testing.T) {
	o := newTestOrchestrator(t)
	if err := state.WritePoisonMarker(o.opts.RunDir, "prior corrupt resuming round"); err != nil {
		t.Fatal(err)
	}
	err := o.recoverReviewPause(context.Background())
	if !errors.Is(err, errReviewPausePoisoned) {
		t.Fatalf("poison breadcrumb must re-poison on restart, got %v", err)
	}
	if !o.activationHeld.Load() || !o.reviewTxnActive() {
		t.Fatal("poison breadcrumb must re-establish hold + in-memory poison marker")
	}
	if st, _ := o.ReviewState(); st != state.PauseStateResuming {
		t.Fatalf("ReviewState = %q, want resuming after restart-poison", st)
	}
	// Operator clears the breadcrumb → a fresh recovery on the same run dir is
	// back to normal (no poison, no error).
	if err := state.ClearPoisonMarker(o.opts.RunDir); err != nil {
		t.Fatal(err)
	}
	if state.HasPoisonMarker(o.opts.RunDir) {
		t.Fatal("ClearPoisonMarker must remove the breadcrumb")
	}
	clean := New(Options{RunDir: o.opts.RunDir, Stages: o.opts.Stages, Store: o.opts.Store, Config: config.Default()})
	if err := clean.recoverReviewPause(context.Background()); err != nil {
		t.Fatalf("recovery after clearing the poison must be clean, got %v", err)
	}
	if clean.reviewTxnActive() {
		t.Fatal("no poison should remain after the breadcrumb is cleared")
	}
}

func TestRecoverReviewPause_CorruptPausedFailsOpen(t *testing.T) {
	o := newTestOrchestrator(t)
	path := filepath.Join(o.opts.RunDir, "notes-pause.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"state":"paused",`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := o.recoverReviewPause(context.Background()); err != nil {
		t.Fatalf("corrupt paused marker must fail open (nil error), got %v", err)
	}
	if o.activationHeld.Load() || o.reviewTxnActive() {
		t.Fatal("corrupt paused marker must not set activationHeld/reviewMarker")
	}
}

// stubResolveFile returns fixed file metadata; caller SHA/line controls stale-note cases.
func stubResolveFile(sha, lineText string, inRange bool) func(root, path string, line *int) (ResolvedFile, bool) {
	return func(root, path string, line *int) (ResolvedFile, bool) {
		return ResolvedFile{
			DisplayPath: root + "/" + path,
			Reference:   `[AFM file: "/w/` + path + `"]`,
			ContentSHA:  sha,
			LineText:    lineText,
			InRange:     inRange,
		}, true
	}
}

// pausedNotesTestOrch builds a single-stage orchestrator with a live PauseStatePaused review marker and
// the given ResolveFile stub wired in.
func pausedNotesTestOrch(t *testing.T, resolve func(root, path string, line *int) (ResolvedFile, bool)) *Orchestrator {
	t.Helper()
	o := newPauseFlowTestOrch(t, []flow.Stage{{ID: "s1", Agents: []flow.AgentType{flow.AgentImplementation}}})
	o.reviewMarker.Store(&state.PauseMarker{Version: 1, State: state.PauseStatePaused})
	o.opts.ResolveFile = resolve
	return o
}

func TestAddNote_RejectedWhenNotPaused(t *testing.T) {
	o := newPauseFlowTestOrch(t, []flow.Stage{{ID: "s1", Agents: []flow.AgentType{flow.AgentImplementation}}})
	o.opts.ResolveFile = stubResolveFile("sha256:x", "orig", true)
	if _, _, err := o.AddNote("project", "a.go", nil, "fix", "sha256:x", 0); !errors.Is(err, ErrNoReviewPause) {
		t.Fatalf("err=%v, want ErrNoReviewPause", err)
	}
}

// TestAddNote_RevMismatch закрывает Task 18: expectedRev != текущий Rev
// (0 при первой ноте) должен вернуть ErrRevConflict.
func TestAddNote_RevMismatch(t *testing.T) {
	o := pausedNotesTestOrch(t, stubResolveFile("sha256:x", "orig", true))
	if _, _, err := o.AddNote("project", "a.go", nil, "fix", "sha256:x", 7); !errors.Is(err, ErrRevConflict) {
		t.Fatalf("err=%v, want ErrRevConflict", err)
	}
}

// TestAddNote_EmptyText закрывает Task 18: пустой/whitespace-only text
// должен вернуть ErrEmptyText.
func TestAddNote_EmptyText(t *testing.T) {
	o := pausedNotesTestOrch(t, stubResolveFile("sha256:x", "orig", true))
	if _, _, err := o.AddNote("project", "a.go", nil, "   ", "sha256:x", 0); !errors.Is(err, ErrEmptyText) {
		t.Fatalf("err=%v, want ErrEmptyText", err)
	}
}

// TestAddNote_StaleContent закрывает Task 18: клиентский content_sha не
// совпадает с тем, что резолвит ResolveFile сейчас — ErrStaleContent.
func TestAddNote_StaleContent(t *testing.T) {
	o := pausedNotesTestOrch(t, stubResolveFile("sha256:current", "orig", true))
	if _, _, err := o.AddNote("project", "a.go", nil, "fix", "sha256:stale", 0); !errors.Is(err, ErrStaleContent) {
		t.Fatalf("err=%v, want ErrStaleContent", err)
	}
}

func TestAddNote_UnresolvableFileIsStaleContent(t *testing.T) {
	o := pausedNotesTestOrch(t, func(root, path string, line *int) (ResolvedFile, bool) { return ResolvedFile{}, false })
	if _, _, err := o.AddNote("project", "a.go", nil, "fix", "sha256:x", 0); !errors.Is(err, ErrStaleContent) {
		t.Fatalf("err=%v, want ErrStaleContent", err)
	}
}

// TestAddNote_StaleLine закрывает Task 18: построчная нота с InRange=false
// (номер строки за пределами текущего файла) должна вернуть ErrStaleLine.
func TestAddNote_StaleLine(t *testing.T) {
	o := pausedNotesTestOrch(t, stubResolveFile("sha256:x", "orig", false))
	line := 999
	if _, _, err := o.AddNote("project", "a.go", &line, "fix", "sha256:x", 0); !errors.Is(err, ErrStaleLine) {
		t.Fatalf("err=%v, want ErrStaleLine", err)
	}
}

func TestAddNote_HappyPath(t *testing.T) {
	o := pausedNotesTestOrch(t, stubResolveFile("sha256:x", "original line", true))
	line := 42
	note, rev, err := o.AddNote("project", "a.go", &line, "fix this", "sha256:x", 0)
	if err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	if note.ID != "n1" {
		t.Fatalf("id=%q, want n1", note.ID)
	}
	if rev != 1 {
		t.Fatalf("rev=%d, want 1", rev)
	}
	if note.DisplayPath != "project/a.go" || note.ContentSHA != "sha256:x" {
		t.Fatalf("note=%+v", note)
	}
	if note.OrigLineText == nil || *note.OrigLineText != "original line" {
		t.Fatalf("orig_line_text=%v", note.OrigLineText)
	}

	onDisk, err := state.LoadReviewNotes(o.opts.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Rev != 1 || len(onDisk.Notes) != 1 || onDisk.Notes[0].ID != "n1" {
		t.Fatalf("on disk: %+v", onDisk)
	}
}

func TestAddNote_DedupReplacesSameKey(t *testing.T) {
	o := pausedNotesTestOrch(t, stubResolveFile("sha256:x", "original line", true))
	line := 42
	first, rev1, err := o.AddNote("project", "a.go", &line, "first text", "sha256:x", 0)
	if err != nil {
		t.Fatalf("first AddNote: %v", err)
	}
	if first.ID != "n1" || rev1 != 1 {
		t.Fatalf("first=%+v rev=%d", first, rev1)
	}

	second, rev2, err := o.AddNote("project", "a.go", &line, "second text", "sha256:x", rev1)
	if err != nil {
		t.Fatalf("second AddNote: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("dedup must keep the same id: first=%q second=%q", first.ID, second.ID)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("dedup must keep created_at: first=%v second=%v", first.CreatedAt, second.CreatedAt)
	}
	if second.Text != "second text" {
		t.Fatalf("dedup must refresh text: %q", second.Text)
	}
	if rev2 != 2 {
		t.Fatalf("rev2=%d, want 2", rev2)
	}

	onDisk, err := state.LoadReviewNotes(o.opts.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(onDisk.Notes) != 1 {
		t.Fatalf("dedup must not create a second note: %+v", onDisk.Notes)
	}
	if onDisk.NextID != 2 {
		t.Fatalf("NextID must only bump for a genuinely new note: %d", onDisk.NextID)
	}
}

// TestUpdateNote_RevConflictAndSuccess закрывает Task 18: UpdateNote
// отклоняет мисматч ревизии и, при успехе, меняет только text.
func TestUpdateNote_RevConflictAndSuccess(t *testing.T) {
	o := pausedNotesTestOrch(t, stubResolveFile("sha256:x", "orig", true))
	note, rev, err := o.AddNote("project", "a.go", nil, "first", "sha256:x", 0)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := o.UpdateNote(note.ID, "updated", rev+1); !errors.Is(err, ErrRevConflict) {
		t.Fatalf("err=%v, want ErrRevConflict", err)
	}

	newRev, err := o.UpdateNote(note.ID, "updated", rev)
	if err != nil {
		t.Fatalf("UpdateNote: %v", err)
	}
	if newRev != rev+1 {
		t.Fatalf("newRev=%d, want %d", newRev, rev+1)
	}

	onDisk, err := state.LoadReviewNotes(o.opts.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Notes[0].Text != "updated" {
		t.Fatalf("text not updated: %+v", onDisk.Notes[0])
	}
}

// TestUpdateNote_NotFound закрывает Task 18: обновление несуществующего id
// возвращает ErrNoteNotFound, а не тихий no-op.
func TestUpdateNote_NotFound(t *testing.T) {
	o := pausedNotesTestOrch(t, stubResolveFile("sha256:x", "orig", true))
	if _, err := o.UpdateNote("n404", "updated", 0); !errors.Is(err, ErrNoteNotFound) {
		t.Fatalf("err=%v, want ErrNoteNotFound", err)
	}
}

// TestDeleteNote_Success закрывает Task 18: DeleteNote убирает ноту и
// поднимает Rev; повторное удаление того же id — ErrNoteNotFound.
func TestDeleteNote_Success(t *testing.T) {
	o := pausedNotesTestOrch(t, stubResolveFile("sha256:x", "orig", true))
	note, rev, err := o.AddNote("project", "a.go", nil, "first", "sha256:x", 0)
	if err != nil {
		t.Fatal(err)
	}

	newRev, err := o.DeleteNote(note.ID, rev)
	if err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	if newRev != rev+1 {
		t.Fatalf("newRev=%d, want %d", newRev, rev+1)
	}

	onDisk, err := state.LoadReviewNotes(o.opts.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(onDisk.Notes) != 0 {
		t.Fatalf("note not deleted: %+v", onDisk.Notes)
	}

	if _, err := o.DeleteNote(note.ID, newRev); !errors.Is(err, ErrNoteNotFound) {
		t.Fatalf("second delete err=%v, want ErrNoteNotFound", err)
	}
}

func TestListNotes_NoPauseGate(t *testing.T) {
	o := newPauseFlowTestOrch(t, []flow.Stage{{ID: "s1", Agents: []flow.AgentType{flow.AgentImplementation}}})
	notes, err := o.ListNotes()
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if notes.Rev != 0 || len(notes.Notes) != 0 {
		t.Fatalf("expected empty notes, got %+v", notes)
	}
}
