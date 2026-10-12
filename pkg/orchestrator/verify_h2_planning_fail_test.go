package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// H2: после H1 ЛЮБАЯ терминальная ошибка completionCheck — не только verify-исход — стала уходить через
// commitVerifyFailure, чей EvVerifyFail имеет ОГРАНИЧЕННЫЙ From.
func TestRunWithRetry_PlanningCompletionExhausted_ReachesFailed(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = 15
	// Переводим fixture из running в planning через Store, не завязывая тест на planning-entrypoint.
	if err := o.opts.Store.Apply(&state.Transition{
		StageID: "s1", From: state.StatusRunning, To: state.StatusPlanning, Event: "test_setup",
	}); err != nil {
		t.Fatal(err)
	}

	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// plan.md существует, но без обязательных секций — CheckPlanCompletionFor вернёт IncompleteWorkError
	// КАЖДЫЙ раз, потому что тестовый agentFn ничего не правит между попытками.
	planPath := filepath.Join(stageDir, "plan.md")
	if err := os.WriteFile(planPath, []byte("# stub plan\n\nnothing useful here\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := flow.Stage{ID: "s1", Interactive: true}
	var attempts int
	o.runWithRetry(context.Background(), s, phasePlanning,
		func(retryContext string) error { attempts++; return nil },
		func() error { return stagefiles.CheckPlanCompletionFor(stageDir, true) },
		func() {
			t.Fatal("onUserInterrupted must not be called — there is no concurrent pause/revise in this test")
		},
	)

	if attempts != 2 {
		t.Fatalf("expected exactly 2 attempts (one free incomplete-retry, then the terminal failure), got %d", attempts)
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusFailed {
		t.Fatalf("status = %v, want failed — H2 regression: a planning-only completion failure must not strand the stage in planning forever (EvVerifyFail's From excludes StatusPlanning)", got)
	}

	data, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// Планировочный сбой — НЕ verify-исход, он обязан идти через ОБЫЧНЫЙ EvFail ("fail"), а не через
	// атомарный EvVerifyFail ("verify_fail"), чей ограниченный From и стал причиной H2.
	if !strings.Contains(string(data), `"event":"fail"`) {
		t.Errorf("expected plain EvFail (\"fail\") in events.jsonl, got: %s", data)
	}
	if strings.Contains(string(data), `"event":"verify_fail"`) {
		t.Errorf("a planning completion failure must never use the atomic EvVerifyFail (\"verify_fail\"): %s", data)
	}
}

func TestRunWithRetry_MissingArtifact_FailsViaPlainEvFail(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = 15

	missing := &MissingArtifactError{Name: "api-contract"}

	var attempts int
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(retryContext string) error { attempts++; return nil },
		func() error { return missing },
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if attempts != 1 {
		t.Fatalf("missing artifact is not retryable, got %d attempts", attempts)
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusFailed {
		t.Fatalf("status = %v, want failed", got)
	}

	data, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"event":"fail"`) {
		t.Errorf("expected plain EvFail (\"fail\") in events.jsonl, got: %s", data)
	}
	if strings.Contains(string(data), `"event":"verify_fail"`) {
		t.Errorf("missing-artifact failure must not use the atomic EvVerifyFail (\"verify_fail\"): %s", data)
	}
}
