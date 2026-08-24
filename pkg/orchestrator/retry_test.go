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

// TestIsRetryableError: перенесён из completion_test.go при выносе
// completion.go в pkg/orchestrator/stagefiles (Task 3 orchestrator-split) —
// isRetryableError и константы match* остаются в этом пакете (retry.go,
// errors.go), поэтому тест здесь, а не в stagefiles.
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

// TestRunWithRetry_CompletionMarkerOverridesStaleOpenQuestion is a regression
// test for a bug found analyzing a real production log: a stage's
// completion marker (execution_summary.md) already existed — the agent had
// genuinely finished all its real work and its process exited cleanly (err
// == nil) — but an entirely unrelated, permanently-abandoned question.json
// from much earlier in the stage's life (in the real incident: the
// reused-id bug's orphaned second-round question, which the user had
// already worked around via a manual Revise and moved past) was still
// sitting on disk with no answer.json. runWithRetry's hasOpenQuestion check
// doesn't distinguish "genuinely waiting on this answer" from "long-dead
// artifact nobody will ever answer" — it held the stage in
// awaiting_user_input forever, even though the real work was already done
// and verifiable on disk.
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

	// Симулирует реальный прод-сценарий: независимый поллер вопросов уже
	// перевёл стадию в awaiting_user_input, ПОКА агент ещё выполнялся —
	// раньше, чем он успел вернуться и записать execution_summary.md. Без
	// этого перехода тест не отличим от случая "вопрос только что написан
	// этим же вызовом" (см. TestIntegration_PlanningWithOpenQuestionWaits),
	// где completion не должен побеждать открытый вопрос.
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

	// runWithRetry only publishes the event — advancing the FSM from it is
	// the live event loop's job (Run's onAgentCompleted dispatch). Simulate
	// that one step so the assertion below reflects the real end-to-end
	// outcome, not just "was the event published".
	if err := o.onAgentCompleted(context.Background(), completedEvent); err != nil {
		t.Fatalf("onAgentCompleted: %v", err)
	}

	if got := o.opts.Store.Get("s1"); got == state.StatusAwaitingUserInput {
		t.Error("stage must not be held in awaiting_user_input when completion is already satisfied")
	}
}

// TestRunWithRetry_ScheduleRetryTransitionCarriesReason закрывает Finding #7:
// EventRetryScheduled публикуется живьём с Data="attempt X/Y in Zs", но сама
// FSM-transition EvScheduleRetry создавалась с reason="" — а на reload
// transitionToFeedEvents реконструирует retry_scheduled именно из reason
// (Data: t.Reason). Итог: после перезагрузки строка ленты показывала "retry:"
// без attempt/backoff. Теперь reason == сообщению.
func TestRunWithRetry_ScheduleRetryTransitionCarriesReason(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	// setupHookOrch строит Orchestrator вручную (не через New), поэтому
	// maxRetries/retryBackoff нулевые — выставляем явно, иначе retry вообще не
	// планируется (attempt < 0 == false → сразу EvFail).
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
