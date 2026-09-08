package orchestrator_test

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
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// pauseInjectRunner drives TestIntegration_PauseInjectResume's two stages
// through a REAL orchestrator run (real FSM, real scheduling, real
// concurrency.Manager) without a real AI client subprocess:
//
//   - RunPlanning always succeeds with a minimal valid plan.md, for any
//     stage — both s1 and s2 go through an ordinary planning phase, and
//     headless auto-approve (Options.RequireApproval defaults to false)
//     carries each straight from awaiting_approval to running, exactly the
//     way blockingRunner does in pause_continue_test.go.
//   - RunAgent for the target stage (targetStage, "s1") blocks on the SAME
//     interruptChans channel PauseFlow signals (via
//     orchestrator.InterruptChanForTest — the same test seam
//     blockingRunner/blockingThenFeedbackRunner already use elsewhere in
//     this package) on its FIRST call, returning
//     executor.ErrUserInterrupted — simulating a real subprocess reacting to
//     SIGINT. Every later call (i.e. the resumed runImplementationWithFeedback
//     call InjectNotesAndResume triggers) captures the prompt it received (so
//     the test can assert the rendered review note actually reached the real
//     prompt) and completes normally by writing .done.
//   - RunAgent for any OTHER stage (s2) always succeeds immediately.
//
// planCalls/agentCalls are keyed by stage name so the test can assert s1 was
// planned exactly once — a SECOND RunPlanning call for s1 would mean the
// resume incorrectly went through the planning (not implementation)
// WithFeedback runner.
type pauseInjectRunner struct {
	orch        *orchestrator.Orchestrator
	targetStage string

	mu            sync.Mutex
	planCalls     map[string]int
	agentCalls    map[string]int
	resumedPrompt string
}

func (r *pauseInjectRunner) RunPlanning(_ context.Context, stageName, _, outFile, _ string) error {
	r.mu.Lock()
	if r.planCalls == nil {
		r.planCalls = map[string]int{}
	}
	r.planCalls[stageName]++
	r.mu.Unlock()

	const plan = "## Tasks\n\n- [ ] implement feature\n\n## Assumptions\n\n- none\n\n## Acceptance Criteria\n\n- [ ] feature works\n"
	return os.WriteFile(outFile, []byte(plan), 0644)
}

func (r *pauseInjectRunner) RunAgent(ctx context.Context, _, stageName, prompt, logFile string) error {
	stageDir := filepath.Dir(logFile)
	if stageName != r.targetStage {
		return os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done\n"), 0644)
	}

	r.mu.Lock()
	if r.agentCalls == nil {
		r.agentCalls = map[string]int{}
	}
	r.agentCalls[stageName]++
	n := r.agentCalls[stageName]
	r.mu.Unlock()

	if n == 1 {
		// First call: this is the stage's ORIGINAL run, still in flight when
		// PauseFlow fires. Block on the real interrupt channel and return
		// exactly what a SIGINT'd subprocess would.
		ch, ok := orchestrator.InterruptChanForTest(r.orch, r.targetStage)
		if !ok {
			<-ctx.Done()
			return ctx.Err()
		}
		select {
		case <-ch:
			return executor.ErrUserInterrupted
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Second (and any later) call: the resumed run. Capture the prompt so the
	// test can assert the rendered review note actually reached it.
	r.mu.Lock()
	r.resumedPrompt = prompt
	r.mu.Unlock()
	return os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done\n"), 0644)
}

func (r *pauseInjectRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("not used in this test")
}

var _ executor.Runner = (*pauseInjectRunner)(nil)

func (r *pauseInjectRunner) planCallCount(stageName string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.planCalls[stageName]
}

func (r *pauseInjectRunner) getResumedPrompt() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.resumedPrompt
}

// pauseInjectResolveFile is a stub Options.ResolveFile that always resolves
// successfully with a fixed content_sha — AddNote below drives staleness
// purely through the clientSHA it's called with, not through this stub (same
// pattern as reviewpause_test.go's stubResolveFile, duplicated here because
// that helper lives in the internal orchestrator package, not
// orchestrator_test).
func pauseInjectResolveFile(root, path string, _ *int) (orchestrator.ResolvedFile, bool) {
	return orchestrator.ResolvedFile{
		Abs:         "/w/" + path,
		DisplayPath: root + "/" + path,
		Reference:   `[AFM file: "/w/` + path + `"]`,
		ContentSHA:  "sha256:current",
		InRange:     true,
	}, true
}

// pollUntil polls cond every 20ms until it returns true or the deadline
// passes, failing the test on timeout. Local equivalent of reviewpause_test.go's
// waitFor (unexported, internal-package helper) — duplicated here for the same
// reason as pauseInjectResolveFile above.
func pollUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !cond() {
		t.Fatal("pollUntil: condition not met within timeout")
	}
}

// TestIntegration_PauseInjectResume drives a REAL orchestrator (real FSM,
// real scheduling, a scripted mock Runner — no real AI client) through the
// full review-pause round trip end-to-end: PauseFlow pauses the one active
// stage, a review note is added via the real AddNote path, InjectNotesAndResume
// delivers it and resumes, and the dependent stage that was held behind it
// eventually completes too.
//
// Chosen approach: a full runOrchestratorAsync run (Option A from the task
// brief) rather than the testRunnerHook-stubbed focused variant. The focused
// variant lives in package orchestrator (reviewpause_test.go) and stubs
// resumeOwner's own Trigger+SpawnAgent entirely — it can prove WHICH runner
// kind gets picked, but it can't prove that s2 (whose activation is gated by
// s1 actually reaching StatusDone through the real graph, not just by
// activationHeld) really unblocks, because testRunnerHook short-circuits
// before any real FSM transition happens. Only a real run exercises the
// production code path far enough to observe that end-to-end.
func TestIntegration_PauseInjectResume(t *testing.T) {
	stages := []flow.Stage{
		{ID: "s1", Name: "s1", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
		{ID: "s2", Name: "s2", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, DependsOn: []string{"s1"}},
	}

	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"s1", "s2"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	runner := &pauseInjectRunner{targetStage: "s1"}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(), Runner: runner,
		ResolveFile: pauseInjectResolveFile,
	})
	runner.orch = orch

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	// --- Step 1: PauseFlow while s1 is running, s2 still pending on s1. ---
	waitForStatus(t, stateFile, "s1", state.StatusRunning, 10*time.Second)

	owners, err := orch.PauseFlow(ctx)
	if err != nil {
		t.Fatalf("PauseFlow: %v", err)
	}
	if len(owners) != 1 || owners[0] != "s1" {
		t.Fatalf("owners = %v, want [s1]", owners)
	}
	waitForStatus(t, stateFile, "s1", state.StatusPaused, 5*time.Second)

	if got := loadStateJSON(t, stateFile).Stages["s2"].Status; got != state.StatusPending {
		t.Fatalf("s2 status = %v, want pending (held behind s1)", got)
	}

	if rs, paused := orch.ReviewState(); rs != "paused" || len(paused) != 1 || paused[0] != "s1" {
		t.Fatalf("ReviewState = (%q, %v), want (paused, [s1])", rs, paused)
	}

	// --- Step 2: add a review note through the real AddNote path. ---
	const noteText = "please handle errors here"
	note, rev, err := orch.AddNote("project", "main.go", nil, noteText, "sha256:current", 0)
	if err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	if note.ID == "" || rev != 1 {
		t.Fatalf("AddNote returned note=%+v rev=%d, want a non-empty id and rev=1", note, rev)
	}

	// --- Step 3: InjectNotesAndResume. ---
	if err := orch.InjectNotesAndResume(ctx, "s1"); err != nil {
		t.Fatalf("InjectNotesAndResume: %v", err)
	}

	// feedback.md is written SYNCHRONOUSLY before InjectNotesAndResume
	// returns (see its doc comment) — readable immediately, no polling needed.
	feedbackBody, err := os.ReadFile(filepath.Join(runDir, "s1", "feedback.md"))
	if err != nil {
		t.Fatalf("read feedback.md: %v", err)
	}
	if strings.Count(string(feedbackBody), noteText) != 1 {
		t.Fatalf("feedback.md does not contain the note exactly once:\n%s", feedbackBody)
	}
	if !strings.Contains(string(feedbackBody), "## Review notes") {
		t.Fatalf("feedback.md missing the rendered review-notes header:\n%s", feedbackBody)
	}

	// --- Step 4: s1 resumes via the IMPLEMENTATION WithFeedback runner (not
	// planning), the note reaches its real prompt, and s2 — held behind s1 —
	// eventually completes too. ---
	waitForStatus(t, stateFile, "s1", state.StatusDone, 10*time.Second)
	waitForStatus(t, stateFile, "s2", state.StatusDone, 10*time.Second)

	// s1 was planned exactly once: a second RunPlanning call would mean the
	// resume mistakenly went through the PLANNING WithFeedback runner instead
	// of the implementation one.
	if n := runner.planCallCount("s1"); n != 1 {
		t.Fatalf("s1 RunPlanning call count = %d, want 1 (no re-planning on resume)", n)
	}
	resumedPrompt := runner.getResumedPrompt()
	if !strings.Contains(resumedPrompt, noteText) || !strings.Contains(resumedPrompt, "## Review notes") {
		t.Fatalf("resumed implementation prompt missing the injected review note:\n%s", resumedPrompt)
	}

	// --- Cleanup: marker + notes file are gone, review state is back to none. ---
	pollUntil(t, 2*time.Second, func() bool {
		_, found, ferr := state.ReadNotesPauseMarker(runDir)
		return ferr == nil && !found
	})
	notes, err := state.LoadReviewNotes(runDir)
	if err != nil {
		t.Fatalf("LoadReviewNotes: %v", err)
	}
	if len(notes.Notes) != 0 {
		t.Fatalf("review notes should be deleted after resume, got %+v", notes.Notes)
	}
	if rs, paused := orch.ReviewState(); rs != "none" || paused != nil {
		t.Fatalf("ReviewState after resume = (%q, %v), want (none, nil)", rs, paused)
	}

	// --- A second inject against the same (now closed) round must not
	// duplicate feedback — there's no active review-pause left to act on. ---
	if err := orch.InjectNotesAndResume(ctx, "s1"); !errors.Is(err, orchestrator.ErrNoReviewPause) {
		t.Fatalf("second InjectNotesAndResume: err = %v, want ErrNoReviewPause", err)
	}
	feedbackBody2, err := os.ReadFile(filepath.Join(runDir, "s1", "feedback.md"))
	if err != nil {
		t.Fatalf("re-read feedback.md: %v", err)
	}
	if strings.Count(string(feedbackBody2), noteText) != 1 {
		t.Fatalf("feedback.md must still contain the note exactly once after the rejected second inject:\n%s", feedbackBody2)
	}
}
