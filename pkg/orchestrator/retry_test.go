package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

func TestBuildRetryContext_FullActionNotTruncated(t *testing.T) {
	stageDir := t.TempDir()
	longOutput := strings.Repeat("output-line ", 100) // far longer than any sane truncate_output limit
	line := fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"%s"}}]}}`, longOutput)
	if err := os.WriteFile(filepath.Join(stageDir, "implementation.jsonl"), []byte(line+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got := buildRetryContext(stageDir, phaseImplementation)
	if !strings.Contains(got, longOutput) {
		t.Errorf("retry context does not contain full action text — got truncated/missing content: %q", got)
	}
}

func TestBuildRetryContext_MissingLogReturnsEmpty(t *testing.T) {
	stageDir := t.TempDir()
	if got := buildRetryContext(stageDir, phaseImplementation); got != "" {
		t.Errorf("expected empty context for missing jsonl, got %q", got)
	}
}

func TestIsRetryableError(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"You've hit your limit", true},
		{matchRateLimit + " exceeded", true},
		{matchTooManyRequests, true},
		{matchOverloaded, true},
		{matchAtCapacity, true},
		{"500 Internal Server Error", true},
		{matchInternalServerError, true},
		{"something went wrong", false},
		{"", false},
	}
	for _, c := range cases {
		var err error
		if c.msg != "" {
			err = fmt.Errorf("%s", c.msg)
		}
		if got := isRetryableError(err); got != c.want {
			t.Errorf("isRetryableError(%q) = %v, want %v", c.msg, got, c.want)
		}
	}

	if isRetryableError(nil) {
		t.Error("nil should not be retryable")
	}
}

func TestRunWithRetry_CompletionMarkerOverridesStaleOpenQuestion(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}

	stalePath := filepath.Join(stageDir, "autonomous_execution.q2.question.json")
	if err := os.WriteFile(stalePath, []byte(`{"id":"q2","question":"long-abandoned question"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Поллер уже перевёл живого агента в awaiting_user_input до записи completion; это отличает stale-вопрос
	// от нового.
	events := o.critical.Recv()
	o.Trigger("s1", bus.EvAskUser, bus.GuardCtx{Phase: phaseAutonomous}, "")
	<-events // drain this Trigger's own stage_status_changed notification

	o.runWithRetry(context.Background(), flow.Stage{ID: "s1", Interactive: true}, phaseAutonomous,
		func(retryContext string) error { return nil }, // agent exited cleanly, already wrote its artifact
		func() error { return stagefiles.CheckAutonomousCompletion(stageDir) },
		func() { t.Error("onUserInterrupted should not be called") },
	)

	var completedEvent bus.Event
	select {
	case ev := <-events:
		completedEvent = ev
		if ev.Type != bus.EventAgentCompleted {
			t.Errorf("got event %s, want %s", ev.Type, bus.EventAgentCompleted)
		}
	default:
		t.Fatal("expected EventAgentCompleted to be published — completion marker was already satisfied")
	}

	// runWithRetry only publishes the event — advancing the FSM from it is the live event loop's job.
	if err := o.onAgentCompleted(context.Background(), completedEvent); err != nil {
		t.Fatalf("onAgentCompleted: %v", err)
	}

	if got := o.opts.Store.Get("s1"); got == state.StatusAwaitingUserInput {
		t.Error("stage must not be held in awaiting_user_input when completion is already satisfied")
	}
}

func TestRunWithRetry_ScheduleRetryTransitionCarriesReason(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	// setupHookOrch строит Orchestrator вручную (не через New), поэтому maxRetries/retryBackoff нулевые —
	// выставляем явно, иначе retry вообще не планируется (attempt < 0 == false → сразу EvFail).
	o.maxRetries = 1
	o.retryBackoff = time.Millisecond
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}

	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(string) error { return errors.New("overloaded") }, // retryable → планирует ровно один retry
		nil,
		func() {},
	)

	data, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var tr struct {
			Event  string `json:"event"`
			Reason string `json:"reason"`
		}
		if json.Unmarshal([]byte(line), &tr) != nil {
			continue
		}
		if tr.Event == string(bus.EvScheduleRetry) {
			found = true
			if !strings.Contains(tr.Reason, "attempt") {
				t.Errorf("schedule_retry reason = %q, want непустой 'attempt X/Y in Zs' — иначе retry_scheduled пуст на reload", tr.Reason)
			}
		}
	}
	if !found {
		t.Fatal("не найдено ни одной schedule_retry-transition в events.jsonl")
	}
}

func TestRunWithRetry_LateCompletionDoesNotCompleteRevisingStage(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	s := flow.Stage{ID: "s1"}

	var interruptedCalled bool
	o.runWithRetry(context.Background(), s, phaseReview,
		func(string) error {
			if _, ok := o.Trigger(s.ID, bus.EvRevise, bus.GuardCtx{}, "concurrent revise"); !ok {
				t.Fatal("EvRevise: CAS rejected — running->revising must be legal here")
			}
			return nil // агент "успел" вернуться естественно, уже после Revise()
		},
		nil, // completionCheck==nil → checkErr==nil сразу, как после успешного verify
		func() { interruptedCalled = true },
	)

	if !interruptedCalled {
		t.Fatal("expected onUserInterrupted to be invoked for a stage that moved to revising during completion — the standalone respawn-with-feedback path")
	}
	if got := o.opts.Store.Get(s.ID); got != state.StatusRevising {
		t.Fatalf("status = %v, want revising (untouched — the respawn callback owns the next transition)", got)
	}
	// Trigger itself legitimately wakes the event loop with a stage_status_changed critical event — drain
	// that and assert specifically that EventAgentCompleted was never published alongside it.
	for {
		select {
		case ev := <-o.critical.Recv():
			if ev.Type == bus.EventAgentCompleted {
				t.Fatalf("EventAgentCompleted must not be published for a stage that left running for revising, got %+v", ev)
			}
		default:
			return
		}
	}
}

func TestRunWithRetry_ConcurrentReviseWinsOverNeedsChangesVerifyOutcome(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	s := flow.Stage{ID: "s1"}

	rejected := &VerifyRejectedError{
		IncompleteWorkError: &stagefiles.IncompleteWorkError{Reason: "verify needs changes"},
		ReportID:            "ver1",
		Step:                1,
	}

	var interruptedCalled bool
	o.runWithRetry(context.Background(), s, phaseReview,
		func(string) error {
			if _, ok := o.Trigger(s.ID, bus.EvRevise, bus.GuardCtx{}, "concurrent revise"); !ok {
				t.Fatal("EvRevise: CAS rejected — running->revising must be legal here")
			}
			return nil // агент "успел" вернуться естественно, уже после Revise()
		},
		func() error { return rejected }, // stale needs_changes verify outcome
		func() { interruptedCalled = true },
	)

	if !interruptedCalled {
		t.Fatal("expected onUserInterrupted for a stale needs_changes verify outcome on a stage that moved to revising")
	}
	if got := o.opts.Store.Get(s.ID); got != state.StatusRevising {
		t.Fatalf("status = %v, want revising (untouched — the respawn callback owns the next transition, not an old-generation retry)", got)
	}
}

func TestRunWithRetry_ConcurrentReviseWinsOverVerifyExecError(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	s := flow.Stage{ID: "s1"}

	execErr := &VerifyExecError{Reason: "verify timed out", Step: 1}

	var interruptedCalled bool
	o.runWithRetry(context.Background(), s, phaseReview,
		func(string) error {
			if _, ok := o.Trigger(s.ID, bus.EvRevise, bus.GuardCtx{}, "concurrent revise"); !ok {
				t.Fatal("EvRevise: CAS rejected — running->revising must be legal here")
			}
			return nil
		},
		func() error { return execErr }, // stale verify exec-error
		func() { interruptedCalled = true },
	)

	if !interruptedCalled {
		t.Fatal("expected onUserInterrupted for a stale verify exec-error on a stage that moved to revising")
	}
	if got := o.opts.Store.Get(s.ID); got != state.StatusRevising {
		t.Fatalf("status = %v, want revising (untouched) — EvFail must not override a concurrent revise", got)
	}
}
