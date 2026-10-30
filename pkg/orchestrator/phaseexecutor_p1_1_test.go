package orchestrator

// Характеризационные тесты P1.1. Производственный код в этом PR не менялся.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/state"
)

// newP11Orch запускает entrypoint с моками; нулевой retry-бюджет даёт одну попытку без backoff.
func newP11Orch(t *testing.T, stageID string, initialStatus state.StageStatus, runner *p11CapturingRunner) (*Orchestrator, string) {
	t.Helper()
	rootDir := t.TempDir()
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{stageID})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if initialStatus != state.StatusPending {
		if err := store.Apply(&state.Transition{StageID: stageID, From: state.StatusPending, To: initialStatus, Event: "test_setup"}); err != nil {
			t.Fatal(err)
		}
	}
	stages := []flow.Stage{{ID: stageID}}
	o := &Orchestrator{
		opts:     Options{RootDir: rootDir, RunDir: runDir, Store: store, Stages: stages, Prompts: DefaultPrompts()},
		ui:       bus.NewUIBus(),
		critical: bus.NewCriticalBus(16),
		fsm:      bus.NewFSM(store),
		graph:    graph.NewGraph(stages),
		runner:   runner,
	}
	o.opts.Runner = runner
	return o, runDir
}

// p11Call — одно наблюдённое обращение к RunAgent/RunPlanning: тип агента, итоговый промпт и logFile — имя
// файла лога пиновать отдельно важно: план фиксирует его как byte-identity инвариант.
type p11Call struct {
	agentType string
	prompt    string
	logFile   string
}

// p11CapturingRunner записывает RunAgent, имитирует fail/.done и вызывает onCall между implementation и
// embedded review.
type p11CapturingRunner struct {
	mu     sync.Mutex
	calls  []p11Call
	failOn string
	doneOn string
	onCall func(agentType string)
}

// planningStub — валидный plan.md, чтобы runPlanningAgent's post-RunPlanning валидация проходила чисто и
// НЕ уходила в rePromptMissingSections.
const planningStub = "## Tasks\n- t\n\n## Assumptions\n- a\n\n## Acceptance Criteria\n- c\n"

// p11PlanningAgentType — синтетическая метка agentType для p11Call-записей, рождённых RunPlanning —
// используется только там, где тест сверяет agentType, а не только logFile.
const p11PlanningAgentType = "planning"

func (r *p11CapturingRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	r.mu.Lock()
	r.calls = append(r.calls, p11Call{agentType: p11PlanningAgentType, prompt: prompt, logFile: logFile})
	r.mu.Unlock()
	return os.WriteFile(outFile, []byte(planningStub), 0644)
}

func (r *p11CapturingRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	r.mu.Lock()
	r.calls = append(r.calls, p11Call{agentType: agentType, prompt: prompt, logFile: logFile})
	r.mu.Unlock()
	if r.onCall != nil {
		r.onCall(agentType)
	}
	if r.failOn != "" && agentType == r.failOn {
		return errors.New("p11: forced failure for " + agentType)
	}
	if r.doneOn != "" && agentType == r.doneOn {
		_ = os.WriteFile(filepath.Join(filepath.Dir(logFile), ".done"), []byte("done"), 0644)
	}
	return nil
}

func (r *p11CapturingRunner) RunJSONQuery(ctx context.Context, prompt string) ([]byte, error) {
	return nil, nil
}

func (r *p11CapturingRunner) snapshot() []p11Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]p11Call{}, r.calls...)
}

// assertNoteOrder проверяет, что каждый маркер из markersInOrder найден в prompt и что их позиции строго
// возрастают в заданном порядке — то есть пинит порядок конкатенации кусков RetryContext.
func assertNoteOrder(t *testing.T, prompt string, markersInOrder ...string) {
	t.Helper()
	prevIdx, prevMarker := -1, "<start>"
	for _, m := range markersInOrder {
		idx := strings.Index(prompt, m)
		if idx < 0 {
			t.Fatalf("expected marker %q in prompt, not found:\n%s", m, prompt)
		}
		if idx <= prevIdx {
			t.Errorf("note ordering broken: marker %q (at %d) must come AFTER %q (at %d):\n%s", m, idx, prevMarker, prevIdx, prompt)
		}
		prevIdx, prevMarker = idx, m
	}
}

// assertNoMarker проверяет, что маркер НЕ встречается в prompt — используется
// пинить отрицательный контракт (напр. embedded-review не получает preNote).
func assertNoMarker(t *testing.T, prompt, marker string) {
	t.Helper()
	if strings.Contains(prompt, marker) {
		t.Errorf("expected marker %q to be ABSENT from prompt, but found it:\n%s", marker, prompt)
	}
}

// countContextWarnings считает сборы dependency context: отсутствующий plan.md даёт одно предупреждение на
// сбор.
func countContextWarnings(o *Orchestrator, fn func()) int {
	subID, events := o.ui.Subscribe(64)
	defer o.ui.Unsubscribe(subID)
	fn()
	n := 0
	for {
		select {
		case ev := <-events:
			if ev.Type == bus.EventContextWarning {
				n++
			}
		default:
			return n
		}
	}
}

// seedActiveVerifyFeedback пишет blocking finding с заданным title для различения версий feedback.
func seedActiveVerifyFeedback(t *testing.T, stageDir, verID, title string) {
	t.Helper()
	result := verify.ModelResult{
		SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictNeedsChanges, Summary: "s",
		Findings: []verify.Finding{{Blocking: true, Title: title, Requirement: "req", Evidence: "ev", MinimalFix: "fix"}},
	}
	if err := stagefiles.WriteActiveFeedback(stageDir, verID, result, "codex", 1); err != nil {
		t.Fatal(err)
	}
}

// --- runPlanningAgent (fresh) ---------------------------------------------

func TestP11_RunPlanningAgent_Fresh_ClearsStaleAutonomousFlag(t *testing.T) {
	stageID := "s1"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, state.StatusPending, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	flagPath := filepath.Join(stageDir, "autonomous.flag")
	if err := os.WriteFile(flagPath, nil, 0644); err != nil {
		t.Fatal(err)
	}

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentPlanning}}
	o.runPlanningAgent(context.Background(), s)

	if _, err := os.Stat(flagPath); !os.IsNotExist(err) {
		t.Errorf("expected fresh planning to clear the stale autonomous.flag, stat err = %v", err)
	}
}

// --- runImplementationAgent (fresh) ---------------------------------------

func TestP11_RunImplementationAgent_Fresh_NoteOrder(t *testing.T) {
	stageID := "impl1"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, state.StatusPending, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := state.SavePreNote(stageDir, "PRENOTE-MARKER"); err != nil {
		t.Fatal(err)
	}
	seedActiveVerifyFeedback(t, stageDir, "ver1", "verify-marker")

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentImplementation}}
	o.runImplementationAgent(context.Background(), s)

	calls := runner.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 RunAgent call (no review agent declared), got %d: %+v", len(calls), calls)
	}
	assertNoteOrder(t, calls[0].prompt,
		"Stage directory for .done file",
		"added before this stage started",
		verifyFeedbackHeading,
	)
}

func TestP11_RunImplementationAgent_EmbeddedReview_OrderAndNoPreNoteInReviewPrompt(t *testing.T) {
	stageID := "implrev"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, state.StatusPending, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := state.SavePreNote(stageDir, "PRENOTE-MARKER"); err != nil {
		t.Fatal(err)
	}
	seedActiveVerifyFeedback(t, stageDir, "ver1", "verify-marker")

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentImplementation, flow.AgentReview}}
	o.runImplementationAgent(context.Background(), s)

	calls := runner.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected implementation + embedded review (2 calls), got %d: %+v", len(calls), calls)
	}
	if calls[0].agentType != phaseImplementation {
		t.Errorf("expected call 0 to be implementation, got %q", calls[0].agentType)
	}
	if calls[1].agentType != phaseReview {
		t.Errorf("expected call 1 (embedded review) to be phaseReview, got %q", calls[1].agentType)
	}
	if !strings.Contains(calls[1].prompt, verifyFeedbackHeading) {
		t.Errorf("expected embedded review prompt to carry verifyNote, got:\n%s", calls[1].prompt)
	}
	assertNoMarker(t, calls[1].prompt, "added before this stage started")
}

func TestP11_RunImplementationAgent_EmbeddedReview_ReusesCapturedVerifyNote(t *testing.T) {
	stageID := "implreuse"
	var stageDir string
	runner := &p11CapturingRunner{
		onCall: func(agentType string) {
			if agentType == phaseImplementation {
				seedActiveVerifyFeedback(t, stageDir, "ver2", "AFTER-marker")
			}
		},
	}
	o, runDir := newP11Orch(t, stageID, state.StatusPending, runner)
	stageDir = filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
		t.Fatal(err)
	}
	seedActiveVerifyFeedback(t, stageDir, "ver1", "BEFORE-marker")

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentImplementation, flow.AgentReview}}
	o.runImplementationAgent(context.Background(), s)

	calls := runner.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected implementation + embedded review (2 calls), got %d: %+v", len(calls), calls)
	}
	reviewPrompt := calls[1].prompt
	if !strings.Contains(reviewPrompt, "BEFORE-marker") {
		t.Errorf("expected embedded review to reuse the verifyNote captured BEFORE implementation ran (BEFORE-marker), got:\n%s", reviewPrompt)
	}
	if strings.Contains(reviewPrompt, "AFTER-marker") {
		t.Errorf("embedded review must NOT re-read verify/feedback.md after implementation — it reused a stale AFTER-marker:\n%s", reviewPrompt)
	}
}

func TestP11_RunImplementationAgent_EmbeddedReview_ReusesCapturedDepPlans(t *testing.T) {
	stageID := "implreusedeps"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, state.StatusPending, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentImplementation, flow.AgentReview}, DependsOn: []string{"dep1"}}
	o.opts.Stages = append(o.opts.Stages, flow.Stage{ID: "dep1", Name: "Dep1"})

	warnings := countContextWarnings(o, func() {
		o.runImplementationAgent(context.Background(), s)
	})
	if warnings != 1 {
		t.Errorf("expected exactly 1 EventContextWarning (depPlans collected ONCE and reused for embedded review), got %d — embedded review likely re-collected depPlans", warnings)
	}

	calls := runner.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected implementation + embedded review (2 calls), got %d: %+v", len(calls), calls)
	}
}

func TestP11_RunImplementationAgent_EmbeddedReviewSkippedOnImplementationFailure(t *testing.T) {
	stageID := "implfail"
	runner := &p11CapturingRunner{failOn: phaseImplementation}
	o, runDir := newP11Orch(t, stageID, state.StatusPending, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentImplementation, flow.AgentReview}}
	o.runImplementationAgent(context.Background(), s)

	calls := runner.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected embedded review to be SKIPPED when implementation itself fails, got %d calls: %+v", len(calls), calls)
	}
	if calls[0].agentType != phaseImplementation {
		t.Errorf("expected the single recorded call to be the failed implementation attempt, got %q", calls[0].agentType)
	}
}

// --- runImplementationWithFeedback ----------------------------------------

// TestP11_RunImplementationWithFeedback_NoteOrder пинит порядок RetryContext
// = retryContext + stageDirNote + feedbackNote + verifyNote (agents.go:505).
func TestP11_RunImplementationWithFeedback_NoteOrder(t *testing.T) {
	stageID := "impl2"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, state.StatusRevising, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("FEEDBACK-MARKER"), 0644); err != nil {
		t.Fatal(err)
	}
	seedActiveVerifyFeedback(t, stageDir, "ver1", "verify-marker")

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentImplementation}}
	o.runImplementationWithFeedback(context.Background(), s)

	calls := runner.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 RunAgent call (no review agent declared), got %d: %+v", len(calls), calls)
	}
	assertNoteOrder(t, calls[0].prompt,
		"Stage directory for .done file",
		"added while this stage was running",
		verifyFeedbackHeading,
	)
	assertNoMarker(t, calls[0].prompt, "added before this stage started")
}

func TestP11_RunImplementationWithFeedback_EmbeddedReview_OrderAndNoNoteInReviewPrompt(t *testing.T) {
	stageID := "implrevfb"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, state.StatusRevising, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("FEEDBACK-MARKER"), 0644); err != nil {
		t.Fatal(err)
	}
	seedActiveVerifyFeedback(t, stageDir, "ver1", "verify-marker")

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentImplementation, flow.AgentReview}}
	o.runImplementationWithFeedback(context.Background(), s)

	calls := runner.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected feedback-implementation + embedded review (2 calls), got %d: %+v", len(calls), calls)
	}
	if calls[0].agentType != phaseImplementation {
		t.Errorf("expected call 0 to be implementation, got %q", calls[0].agentType)
	}
	if calls[1].agentType != phaseReview {
		t.Errorf("expected call 1 (embedded review) to be phaseReview, got %q", calls[1].agentType)
	}
	if !strings.Contains(calls[1].prompt, verifyFeedbackHeading) {
		t.Errorf("expected embedded review prompt to carry verifyNote, got:\n%s", calls[1].prompt)
	}
	assertNoMarker(t, calls[1].prompt, "added before this stage started")
	assertNoMarker(t, calls[1].prompt, "added while this stage was running")
}

func TestP11_RunImplementationWithFeedback_EmbeddedReviewSkippedOnImplementationFailure(t *testing.T) {
	stageID := "implfailfb"
	runner := &p11CapturingRunner{failOn: phaseImplementation}
	o, runDir := newP11Orch(t, stageID, state.StatusRevising, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("FEEDBACK-MARKER"), 0644); err != nil {
		t.Fatal(err)
	}

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentImplementation, flow.AgentReview}}
	o.runImplementationWithFeedback(context.Background(), s)

	calls := runner.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected embedded review to be SKIPPED when feedback-implementation itself fails, got %d calls: %+v", len(calls), calls)
	}
	if calls[0].agentType != phaseImplementation {
		t.Errorf("expected the single recorded call to be the failed implementation attempt, got %q", calls[0].agentType)
	}
}

func TestP11_RunImplementationWithFeedback_EmbeddedReview_ReusesCapturedContext(t *testing.T) {
	stageID := "implreusefb"
	var stageDir string
	runner := &p11CapturingRunner{
		onCall: func(agentType string) {
			if agentType == phaseImplementation {
				seedActiveVerifyFeedback(t, stageDir, "ver2", "AFTER-marker")
			}
		},
	}
	o, runDir := newP11Orch(t, stageID, state.StatusRevising, runner)
	stageDir = filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("FEEDBACK-MARKER"), 0644); err != nil {
		t.Fatal(err)
	}
	seedActiveVerifyFeedback(t, stageDir, "ver1", "BEFORE-marker")

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentImplementation, flow.AgentReview}, DependsOn: []string{"dep1"}}
	o.opts.Stages = append(o.opts.Stages, flow.Stage{ID: "dep1", Name: "Dep1"})

	warnings := countContextWarnings(o, func() {
		o.runImplementationWithFeedback(context.Background(), s)
	})
	if warnings != 1 {
		t.Errorf("expected exactly 1 EventContextWarning (depPlans collected ONCE and reused for embedded review), got %d — embedded review likely re-collected depPlans", warnings)
	}

	calls := runner.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected feedback-implementation + embedded review (2 calls), got %d: %+v", len(calls), calls)
	}
	reviewPrompt := calls[1].prompt
	if !strings.Contains(reviewPrompt, "BEFORE-marker") {
		t.Errorf("expected embedded review to reuse the verifyNote captured BEFORE the feedback-implementation attempt ran (BEFORE-marker), got:\n%s", reviewPrompt)
	}
	if strings.Contains(reviewPrompt, "AFTER-marker") {
		t.Errorf("embedded review (feedback variant) must NOT re-read verify/feedback.md after implementation — it reused a stale AFTER-marker:\n%s", reviewPrompt)
	}
}

// --- runReviewAgent (standalone, fresh) -----------------------------------

func TestP11_RunReviewAgent_Fresh_NoteOrder(t *testing.T) {
	stageID := "rev1"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, state.StatusPending, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := state.SavePreNote(stageDir, "PRENOTE-MARKER"); err != nil {
		t.Fatal(err)
	}
	seedActiveVerifyFeedback(t, stageDir, "ver1", "verify-marker")

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentReview}}
	o.runReviewAgent(context.Background(), s)

	calls := runner.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 RunAgent call, got %d: %+v", len(calls), calls)
	}
	assertNoteOrder(t, calls[0].prompt, "added before this stage started", verifyFeedbackHeading)
}

func TestP11_RunReviewAgent_Fresh_VerifyGateInvoked(t *testing.T) {
	stageID := "revv"
	runner := &p11CapturingRunner{doneOn: phaseReview}
	o, runDir := newP11Orch(t, stageID, state.StatusPending, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}

	called := false
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		called = true
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	}

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentReview},
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
	o.runReviewAgent(context.Background(), s)

	if !called {
		t.Error("expected runReviewAgent's completionCheck to be gateWithVerify-wrapped and invoke RunVerification once the .done probe passed")
	}
}

// --- runReviewWithFeedback -------------------------------------------------

// TestP11_RunReviewWithFeedback_NoteOrder пинит порядок RetryContext =
// retryContext + feedbackNote + verifyNote (agents.go:574).
func TestP11_RunReviewWithFeedback_NoteOrder(t *testing.T) {
	stageID := "rev2"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, state.StatusRevising, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("FEEDBACK-MARKER"), 0644); err != nil {
		t.Fatal(err)
	}
	seedActiveVerifyFeedback(t, stageDir, "ver1", "verify-marker")

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentReview}}
	o.runReviewWithFeedback(context.Background(), s)

	calls := runner.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 RunAgent call, got %d: %+v", len(calls), calls)
	}
	assertNoteOrder(t, calls[0].prompt, "added while this stage was running", verifyFeedbackHeading)
	assertNoMarker(t, calls[0].prompt, "added before this stage started")
}

// --- runAutonomousAgent (fresh) --------------------------------------------

func TestP11_RunAutonomousAgent_Fresh_NoteOrder_AndFlagWritten(t *testing.T) {
	stageID := "auto1"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, state.StatusPending, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := state.SavePreNote(stageDir, "PRENOTE-MARKER"); err != nil {
		t.Fatal(err)
	}
	seedActiveVerifyFeedback(t, stageDir, "ver1", "verify-marker")

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentAuto}}
	o.runAutonomousAgent(context.Background(), s)

	if _, err := os.Stat(filepath.Join(stageDir, "autonomous.flag")); err != nil {
		t.Errorf("expected fresh autonomous start to write autonomous.flag, stat err = %v", err)
	}

	calls := runner.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 RunAgent call, got %d: %+v", len(calls), calls)
	}
	assertNoteOrder(t, calls[0].prompt,
		"Write execution_summary.md here when done.",
		"added before this stage started",
		verifyFeedbackHeading,
	)
}

// --- runAutonomousWithFeedback ----------------------------------------------

func TestP11_RunAutonomousWithFeedback_NoteOrder(t *testing.T) {
	stageID := "auto2"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, state.StatusRevising, runner)
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("FEEDBACK-MARKER"), 0644); err != nil {
		t.Fatal(err)
	}
	seedActiveVerifyFeedback(t, stageDir, "ver1", "verify-marker")

	s := flow.Stage{ID: stageID, Agents: []flow.AgentType{flow.AgentAuto}}
	o.runAutonomousWithFeedback(context.Background(), s)

	// Раннер summary не пишет, поэтому за первым вызовом следуют ровно два summary-продолжения: порядок
	// заметок проверяем у первого вызова.
	calls := runner.snapshot()
	if len(calls) != 3 {
		t.Fatalf("expected 3 RunAgent calls (run + 2 summary recoveries), got %d", len(calls))
	}
	assertNoteOrder(t, calls[0].prompt,
		"Write execution_summary.md here when done.",
		"added while this stage was running",
		verifyFeedbackHeading,
	)
	assertNoMarker(t, calls[0].prompt, "added before this stage started")

	if _, err := os.Stat(filepath.Join(stageDir, "autonomous.flag")); !os.IsNotExist(err) {
		t.Errorf("expected runAutonomousWithFeedback NOT to write autonomous.flag itself, stat err = %v", err)
	}
}

// --- Log filename identity (IMPORTANT gap) ---------------------------------

func TestP11_LogFileNames_PinnedPerVariant(t *testing.T) {
	cases := []struct {
		name     string
		stageID  string
		status   state.StageStatus
		agents   []flow.AgentType
		setup    func(t *testing.T, stageDir string)
		call     func(o *Orchestrator, s flow.Stage)
		wantLogs []string // expected p11Call.logFile per call, in order
	}{
		{
			name:    "planning fresh",
			stageID: "logs-plan-fresh",
			status:  state.StatusPending,
			agents:  []flow.AgentType{flow.AgentPlanning},
			setup:   func(t *testing.T, stageDir string) {},
			call:    func(o *Orchestrator, s flow.Stage) { o.runPlanningAgent(context.Background(), s) },
			wantLogs: []string{
				flow.PhaseLogFile(flow.PhasePlanning), // "planning.log"
			},
		},
		{
			name:    "planning feedback (revision)",
			stageID: "logs-plan-fb",
			status:  state.StatusRevising,
			agents:  []flow.AgentType{flow.AgentPlanning},
			setup: func(t *testing.T, stageDir string) {
				if err := os.MkdirAll(stageDir, 0755); err != nil {
					t.Fatal(err)
				}
			},
			call:     func(o *Orchestrator, s flow.Stage) { o.runPlanningWithFeedback(context.Background(), s) },
			wantLogs: []string{"planning-revision.log"},
		},
		{
			name:    "implementation fresh (no review)",
			stageID: "logs-impl-fresh",
			status:  state.StatusPending,
			agents:  []flow.AgentType{flow.AgentImplementation},
			setup: func(t *testing.T, stageDir string) {
				if err := os.MkdirAll(stageDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			call: func(o *Orchestrator, s flow.Stage) { o.runImplementationAgent(context.Background(), s) },
			wantLogs: []string{
				flow.PhaseLogFile(flow.PhaseImplementation), // "implementation.log"
			},
		},
		{
			name:    "implementation feedback (no review)",
			stageID: "logs-impl-fb",
			status:  state.StatusRevising,
			agents:  []flow.AgentType{flow.AgentImplementation},
			setup: func(t *testing.T, stageDir string) {
				if err := os.MkdirAll(stageDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			call:     func(o *Orchestrator, s flow.Stage) { o.runImplementationWithFeedback(context.Background(), s) },
			wantLogs: []string{"implementation-feedback.log"},
		},
		{
			// Embedded review, fresh implementation: the review log is the
			// SAME name as standalone runReviewAgent's own log — see below.
			name:    "implementation fresh + embedded review",
			stageID: "logs-impl-fresh-rev",
			status:  state.StatusPending,
			agents:  []flow.AgentType{flow.AgentImplementation, flow.AgentReview},
			setup: func(t *testing.T, stageDir string) {
				if err := os.MkdirAll(stageDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			call: func(o *Orchestrator, s flow.Stage) { o.runImplementationAgent(context.Background(), s) },
			wantLogs: []string{
				flow.PhaseLogFile(flow.PhaseImplementation),
				flow.PhaseLogFile(flow.PhaseReview), // "review.log" — SAME as standalone
			},
		},
		{
			// Embedded review uses the plain review log even when outer implementation uses its feedback log.
			name:    "implementation feedback + embedded review",
			stageID: "logs-impl-fb-rev",
			status:  state.StatusRevising,
			agents:  []flow.AgentType{flow.AgentImplementation, flow.AgentReview},
			setup: func(t *testing.T, stageDir string) {
				if err := os.MkdirAll(stageDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			call: func(o *Orchestrator, s flow.Stage) { o.runImplementationWithFeedback(context.Background(), s) },
			wantLogs: []string{
				"implementation-feedback.log",
				flow.PhaseLogFile(flow.PhaseReview), // "review.log" — NOT "review-feedback.log"
			},
		},
		{
			name:     "review fresh (standalone)",
			stageID:  "logs-rev-fresh",
			status:   state.StatusPending,
			agents:   []flow.AgentType{flow.AgentReview},
			setup:    func(t *testing.T, stageDir string) {},
			call:     func(o *Orchestrator, s flow.Stage) { o.runReviewAgent(context.Background(), s) },
			wantLogs: []string{flow.PhaseLogFile(flow.PhaseReview)}, // "review.log"
		},
		{
			name:    "review feedback (standalone)",
			stageID: "logs-rev-fb",
			status:  state.StatusRevising,
			agents:  []flow.AgentType{flow.AgentReview},
			setup: func(t *testing.T, stageDir string) {
				if err := os.MkdirAll(stageDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("FEEDBACK-MARKER"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			call:     func(o *Orchestrator, s flow.Stage) { o.runReviewWithFeedback(context.Background(), s) },
			wantLogs: []string{"review-feedback.log"},
		},
		{
			name:     "autonomous fresh",
			stageID:  "logs-auto-fresh",
			status:   state.StatusPending,
			agents:   []flow.AgentType{flow.AgentAuto},
			setup:    func(t *testing.T, stageDir string) {},
			call:     func(o *Orchestrator, s flow.Stage) { o.runAutonomousAgent(context.Background(), s) },
			wantLogs: []string{flow.PhaseLogFile(flow.PhaseAutonomous)}, // "autonomous.log"
		},
		{
			name:    "autonomous feedback",
			stageID: "logs-auto-fb",
			status:  state.StatusRevising,
			agents:  []flow.AgentType{flow.AgentAuto},
			setup: func(t *testing.T, stageDir string) {
				if err := os.MkdirAll(stageDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("FEEDBACK-MARKER"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			call: func(o *Orchestrator, s flow.Stage) { o.runAutonomousWithFeedback(context.Background(), s) },
			// Раннер summary не пишет: первый вызов и два summary-продолжения идут в один и тот же лог.
			wantLogs: []string{"autonomous-feedback.log", "autonomous-feedback.log", "autonomous-feedback.log"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &p11CapturingRunner{}
			o, runDir := newP11Orch(t, tc.stageID, tc.status, runner)
			stageDir := filepath.Join(runDir, tc.stageID)
			tc.setup(t, stageDir)

			s := flow.Stage{ID: tc.stageID, Agents: tc.agents}
			tc.call(o, s)

			calls := runner.snapshot()
			if len(calls) != len(tc.wantLogs) {
				t.Fatalf("expected %d call(s), got %d: %+v", len(tc.wantLogs), len(calls), calls)
			}
			for i, want := range tc.wantLogs {
				gotBase := filepath.Base(calls[i].logFile)
				if gotBase != want {
					t.Errorf("call %d: logFile base = %q, want %q (full: %q)", i, gotBase, want, calls[i].logFile)
				}
			}
		})
	}
}
