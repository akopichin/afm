package orchestrator

// Характеризационные тесты P1.1. Производственный код в этом PR не менялся.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/browser"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/memory"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/prompts"
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
		graph:    graph.NewStore(graph.NewRegistry(stages)),
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
	// T8/D5: completion contract is appended LAST (after preNote and verifyNote), not as a leading
	// "Stage directory for .done file" note anymore — see runImplementationAttempt (agents.go).
	doneMarker := filepath.Join(stageID, ".done") // o.stageDir(stageID) in the test harness (absRunDir unset)
	assertNoteOrder(t, calls[0].prompt,
		"added before this stage started",
		verifyFeedbackHeading,
		doneMarker,
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
	o.graph = graph.NewStore(graph.NewRegistry(o.opts.Stages)) // dependency-plan collection now reads o.graph, not o.opts.Stages

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
// = retryContext + feedbackNote + verifyNote + completion contract (T8/D5, agents.go's
// runImplementationAttempt) — "Stage directory for .done file" больше не ведущая заметка.
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
	doneMarker := filepath.Join(stageID, ".done") // o.stageDir(stageID) in the test harness (absRunDir unset)
	assertNoteOrder(t, calls[0].prompt,
		"added while this stage was running",
		verifyFeedbackHeading,
		doneMarker,
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
	o.graph = graph.NewStore(graph.NewRegistry(o.opts.Stages)) // dependency-plan collection now reads o.graph, not o.opts.Stages

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
	// T8/D5: completion contract is appended LAST (after preNote and verifyNote), not as a leading
	// "Stage directory: ... / Write execution_summary.md here when done." note anymore.
	summaryMarker := filepath.Join(stageID, "execution_summary.md") // o.stageDir(stageID) in the test harness (absRunDir unset)
	assertNoteOrder(t, calls[0].prompt,
		"added before this stage started",
		verifyFeedbackHeading,
		summaryMarker,
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

	calls := runner.snapshot()
	// The fake author writes no summary: the run and the two post-exit corrections.
	if len(calls) != 1+maxSummaryPostExit {
		t.Fatalf("expected the run and %d corrections, got %d calls: %+v", maxSummaryPostExit, len(calls), calls)
	}
	summaryMarker := filepath.Join(stageID, "execution_summary.md") // o.stageDir(stageID) in the test harness (absRunDir unset)
	assertNoteOrder(t, calls[0].prompt,
		"added while this stage was running",
		verifyFeedbackHeading,
		summaryMarker,
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
			// The fake author writes no summary: the run and the post-exit corrections share the log.
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

// --- Общие prompts.Inputs стадийных фаз --------------------------------------

// stagePromptCase — путь, строящий стадийный промпт. wantRules — выдаёт ли фаза <interactive_rules> для
// не-интерактивной стадии; promptCall — индекс вызова runner, чей промпт проверяется. Фейковый автор не пишет
// execution_summary.md, поэтому прогон autonomous feedback — это запуск и maxSummaryPostExit коррекций.
type stagePromptCase struct {
	name       string
	status     state.StageStatus
	agents     []flow.AgentType
	call       func(o *Orchestrator, s flow.Stage)
	wantRules  bool
	wantCalls  int
	promptCall int
}

// stagePromptEntrypoints — fresh и feedback каждой фазы плюс встроенный review.
func stagePromptEntrypoints() []stagePromptCase {
	ctx := context.Background()
	return []stagePromptCase{
		{"planning", state.StatusPending, []flow.AgentType{flow.AgentPlanning}, func(o *Orchestrator, s flow.Stage) { o.runPlanningAgent(ctx, s) }, false, 1, 0},
		{"planning feedback", state.StatusRevising, []flow.AgentType{flow.AgentPlanning}, func(o *Orchestrator, s flow.Stage) { o.runPlanningWithFeedback(ctx, s) }, false, 1, 0},
		{"implementation", state.StatusPending, []flow.AgentType{flow.AgentImplementation}, func(o *Orchestrator, s flow.Stage) { o.runImplementationAgent(ctx, s) }, false, 1, 0},
		{"implementation feedback", state.StatusRevising, []flow.AgentType{flow.AgentImplementation}, func(o *Orchestrator, s flow.Stage) { o.runImplementationWithFeedback(ctx, s) }, false, 1, 0},
		{"embedded review", state.StatusPending, []flow.AgentType{flow.AgentImplementation, flow.AgentReview}, func(o *Orchestrator, s flow.Stage) { o.runImplementationAgent(ctx, s) }, false, 2, 1},
		{"review", state.StatusPending, []flow.AgentType{flow.AgentReview}, func(o *Orchestrator, s flow.Stage) { o.runReviewAgent(ctx, s) }, false, 1, 0},
		{"review feedback", state.StatusRevising, []flow.AgentType{flow.AgentReview}, func(o *Orchestrator, s flow.Stage) { o.runReviewWithFeedback(ctx, s) }, false, 1, 0},
		{"autonomous", state.StatusPending, []flow.AgentType{flow.AgentAuto}, func(o *Orchestrator, s flow.Stage) { o.runAutonomousAgent(ctx, s) }, true, 1, 0},
		{"autonomous feedback", state.StatusRevising, []flow.AgentType{flow.AgentAuto}, func(o *Orchestrator, s flow.Stage) { o.runAutonomousWithFeedback(ctx, s) }, true, 1 + maxSummaryPostExit, 0},
	}
}

// Каждый стадийный промпт несёт общие поля: global prompt, память, browser, mesh-контракт. Диалоговый протокол
// <interactive_rules> зависит от фазы: autonomous получает его всегда, остальные — только у интерактивной стадии.
func TestP11_StagePrompts_CarryCommonInputs(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		for _, tc := range stagePromptEntrypoints() {
			t.Run(fmt.Sprintf("%s/interactive=%t", tc.name, interactive), func(t *testing.T) {
				stageID := "base"
				runner := &p11CapturingRunner{}
				o, runDir := newP11Orch(t, stageID, tc.status, runner)
				stageDir := filepath.Join(runDir, stageID)
				if err := os.MkdirAll(stageDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Plan\n- step\n"), 0644); err != nil {
					t.Fatal(err)
				}
				memoryDir := t.TempDir()
				projectMemory := memory.ProjectFile(memoryDir)
				if err := os.WriteFile(projectMemory, []byte("# Rules\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				o.opts.GlobalPrompt = "GLOBAL-PROMPT-MARKER"
				o.opts.MemoryDir = memoryDir
				o.opts.Memory = flow.MemoryConfig{Mode: flow.ReflectModeRW, MemoryUse: true}
				o.opts.BrowserDescriptor = &browser.Descriptor{Endpoint: "127.0.0.1:1", Epoch: "ep1"}
				// Инъектированный runner подменяет только дефолтную команду; managed browser нужен claude.
				o.opts.Config.Client.Command = "claude"

				s := flow.Stage{ID: stageID, Agents: tc.agents, Interactive: interactive}
				tc.call(o, s)

				calls := runner.snapshot()
				if len(calls) != tc.wantCalls {
					t.Fatalf("runner calls = %d, want %d: %+v", len(calls), tc.wantCalls, calls)
				}
				prompt := calls[tc.promptCall].prompt
				for _, want := range []string{"GLOBAL-PROMPT-MARKER", projectMemory, "<browser_tools>", "<agent_communication>"} {
					if !strings.Contains(prompt, want) {
						t.Errorf("prompt lacks %q:\n%s", want, prompt)
					}
				}
				if got := strings.Contains(prompt, "<interactive_rules>"); got != (tc.wantRules || interactive) {
					t.Errorf("<interactive_rules> present = %t, want %t", got, tc.wantRules || interactive)
				}
			})
		}
	}
}

// Уникальные маркеры входов, которые каждая фаза добавляет к общим полям промпта.
const (
	markerPlanningTemplate = "TEMPLATE-PLANNING-MARKER"
	markerImplTemplate     = "TEMPLATE-IMPLEMENTATION-MARKER"
	markerReviewTemplate   = "TEMPLATE-REVIEW-MARKER"
	markerAutoTemplate     = "TEMPLATE-AUTONOMOUS-MARKER"
	markerOwnPlan          = "OWN-PLAN-MARKER"
	markerPreviousPlan     = "PREVIOUS-PLAN-MARKER"
	markerDependencyPlan   = "DEPENDENCY-PLAN-MARKER"
	markerInputArtifact    = "INPUT-ARTIFACT-MARKER"
	markerOutputArtifact   = "OUTPUT-ARTIFACT-MARKER"
	markerPreNote          = "PRE-NOTE-MARKER"
	markerFeedbackNote     = "FEEDBACK-NOTE-MARKER"
	markerVerifyFeedback   = "VERIFY-FEEDBACK-MARKER"
	markerVerifyCommand    = "VERIFY-COMMAND-MARKER"
)

// newPhasePromptOrch готовит стадию с зависимостью, входным и выходным артефактом, plan.md, prenote, feedback и
// verify-feedback. Стадия `up` лежит в реестре, чтобы входы собирались так же, как в рабочем ране.
func newPhasePromptOrch(t *testing.T, tc stagePromptCase) (*Orchestrator, *p11CapturingRunner, flow.Stage) {
	t.Helper()
	stageID := "phase"
	runner := &p11CapturingRunner{}
	o, runDir := newP11Orch(t, stageID, tc.status, runner)
	// absRunDir пуст: сессия интерактивной стадии не создаётся, и runnerFor отдаёт инжектированный runner.
	o.opts.Prompts = Prompts{
		Planning:       markerPlanningTemplate,
		Implementation: markerImplTemplate,
		Review:         markerReviewTemplate,
		Autonomous:     markerAutoTemplate,
	}

	upDir := filepath.Join(runDir, "up")
	stageDir := filepath.Join(runDir, stageID)
	files := map[string]string{
		filepath.Join(upDir, "plan.md"):                              markerDependencyPlan,
		filepath.Join(upDir, "note.md"):                              markerInputArtifact,
		filepath.Join(stageDir, "plan.md"):                           markerOwnPlan,
		filepath.Join(stageDir, "plan.v1.md"):                        markerPreviousPlan,
		filepath.Join(stageDir, "prenote.md"):                        markerPreNote,
		filepath.Join(stageDir, "feedback.md"):                       markerFeedbackNote,
		filepath.Join(stagefiles.VerifyDir(stageDir), "feedback.md"): markerVerifyFeedback,
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	up := flow.Stage{ID: "up", Artifacts: []flow.Artifact{{Name: "note", Path: "./note.md"}}}
	s := flow.Stage{
		ID:          stageID,
		Agents:      tc.agents,
		Interactive: true,
		DependsOn:   []string{"up"},
		Inputs:      []flow.Input{{Ref: "up.note"}},
		Artifacts:   []flow.Artifact{{Name: markerOutputArtifact, Path: "./out.md"}},
		Verify:      flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyShell, Run: markerVerifyCommand}}},
	}
	o.opts.Stages = []flow.Stage{up, s}
	o.graph = graph.NewStore(graph.NewRegistry(o.opts.Stages))
	return o, runner, s
}

// Каждая фаза кладёт в промпт свои входы: шаблон, план, контекст зависимостей, артефакты, заметки и контракт
// завершения. Остальные маркеры в этом промпте отсутствуют, поэтому и лишний, и потерянный вход ловятся.
func TestP11_StagePrompts_CarryPhaseInputs(t *testing.T) {
	type wantPrompt struct {
		phase   flow.Phase
		present []string
	}
	const (
		contractPlanning = "planning contract"
		contractDone     = "done contract"
		contractSummary  = "summary contract"
	)
	wants := map[string]wantPrompt{
		"planning": {flow.PhasePlanning, []string{markerPlanningTemplate, markerDependencyPlan, markerInputArtifact, markerPreNote, contractPlanning}},
		"planning feedback": {flow.PhasePlanning, []string{markerPlanningTemplate, markerDependencyPlan, markerInputArtifact,
			markerPreviousPlan, markerFeedbackNote, contractPlanning}},
		"implementation": {flow.PhaseImplementation, []string{markerImplTemplate, markerOwnPlan, markerDependencyPlan, markerInputArtifact,
			markerOutputArtifact, markerPreNote, markerVerifyFeedback, markerVerifyCommand, contractDone}},
		"implementation feedback": {flow.PhaseImplementation, []string{markerImplTemplate, markerOwnPlan, markerDependencyPlan, markerInputArtifact,
			markerOutputArtifact, markerFeedbackNote, markerVerifyFeedback, markerVerifyCommand, contractDone}},
		"embedded review": {flow.PhaseReview, []string{markerReviewTemplate, markerDependencyPlan, markerInputArtifact,
			markerOutputArtifact, markerVerifyFeedback, contractDone}},
		"review": {flow.PhaseReview, []string{markerReviewTemplate, markerDependencyPlan, markerInputArtifact,
			markerPreNote, markerVerifyFeedback, contractDone}},
		"review feedback": {flow.PhaseReview, []string{markerReviewTemplate, markerDependencyPlan, markerInputArtifact,
			markerFeedbackNote, markerVerifyFeedback, contractDone}},
		"autonomous": {flow.PhaseAutonomous, []string{markerAutoTemplate, markerDependencyPlan, markerInputArtifact,
			markerPreNote, markerVerifyFeedback, contractSummary}},
		"autonomous feedback": {flow.PhaseAutonomous, []string{markerAutoTemplate, markerDependencyPlan, markerInputArtifact,
			markerFeedbackNote, markerVerifyFeedback, contractSummary}},
	}

	for _, tc := range stagePromptEntrypoints() {
		t.Run(tc.name, func(t *testing.T) {
			o, runner, s := newPhasePromptOrch(t, tc)
			// Текст контрактов зависит от каталога стадии, остальные входы ищутся по самому маркеру.
			texts := map[string]string{
				contractPlanning: planningContract,
				contractDone:     prompts.CompletionContract(flow.PhaseImplementation, o.stageDir(s.ID)),
				contractSummary:  prompts.CompletionContract(flow.PhaseAutonomous, o.stageDir(s.ID)),
			}
			for _, marker := range []string{markerPlanningTemplate, markerImplTemplate, markerReviewTemplate, markerAutoTemplate,
				markerOwnPlan, markerPreviousPlan, markerDependencyPlan, markerInputArtifact, markerOutputArtifact,
				markerPreNote, markerFeedbackNote, markerVerifyFeedback, markerVerifyCommand} {
				texts[marker] = marker
			}

			tc.call(o, s)

			calls := runner.snapshot()
			if len(calls) != tc.wantCalls {
				t.Fatalf("runner calls = %d, want %d: %+v", len(calls), tc.wantCalls, calls)
			}
			prompt := calls[tc.promptCall].prompt
			want := wants[tc.name]
			present := make(map[string]bool, len(want.present))
			for _, name := range want.present {
				present[name] = true
			}
			for name, text := range texts {
				if got := strings.Contains(prompt, text); got != present[name] {
					t.Errorf("prompt contains %q = %t, want %t", name, got, present[name])
				}
			}
			// Имя фазы видно только в префиксе файлов диалога: стадия интерактивна, блок есть в каждой фазе.
			if dialogFile := string(want.phase) + ".q<N>.question.json"; !strings.Contains(prompt, dialogFile) {
				t.Errorf("prompt lacks the dialog prefix %q", dialogFile)
			}
		})
	}
}

// Без autonomous-шаблона автономная фаза берёт implementation-шаблон; с ним — только свой.
func TestP11_AutonomousPrompt_FallsBackToImplementationTemplate(t *testing.T) {
	var autonomous stagePromptCase
	for _, tc := range stagePromptEntrypoints() {
		if tc.name == "autonomous" {
			autonomous = tc
		}
	}
	for _, tc := range []struct {
		name         string
		autonomous   string
		want, absent string
	}{
		{"own template", markerAutoTemplate, markerAutoTemplate, markerImplTemplate},
		{"fallback", "", markerImplTemplate, markerAutoTemplate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, runner, s := newPhasePromptOrch(t, autonomous)
			o.opts.Prompts.Autonomous = tc.autonomous

			autonomous.call(o, s)

			calls := runner.snapshot()
			if len(calls) != 1 {
				t.Fatalf("runner calls = %d, want 1: %+v", len(calls), calls)
			}
			prompt := calls[0].prompt
			if !strings.Contains(prompt, tc.want) || strings.Contains(prompt, tc.absent) {
				t.Errorf("want template %q without %q:\n%s", tc.want, tc.absent, prompt)
			}
		})
	}
}
