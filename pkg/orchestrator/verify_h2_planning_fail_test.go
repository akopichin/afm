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

// H2 (7-е код-ревью): после H1 (commitVerifyFailure/EvVerifyFail) ЛЮБАЯ
// терминальная ошибка completionCheck — не только verify-исход — стала
// уходить через commitVerifyFailure, чей EvVerifyFail имеет ОГРАНИЧЕННЫЙ From
// (Running/AwaitingApproval/Retrying/AwaitingUserInput — БЕЗ StatusPlanning,
// см. bus/fsm.go). Оба планировочных раннера (runPlanningAgent/
// runPlanningWithFeedback, agents.go) гонят CheckPlanCompletionFor через тот
// же самый generic runWithRetry — а planning вообще не проходит verify
// (gateWithVerify никогда не оборачивает planning-completionCheck, см.
// verify.go). Итог: обычный сбой completion-проверки planning-стадии
// (отсутствующий/пустой plan.md, вторая подряд неполная секция) бил в
// EvVerifyFail, CAS проигрывал (текущий статус — planning, его нет в
// From-наборе), commitVerifyFailure возвращал false, runWithRetry молча
// возвращался БЕЗ Trigger(EvFail) — стадия зависала в "planning" НАВСЕГДА,
// хотя агент уже завершился и никто её больше не перезапустит.
//
// Этот тест воспроизводит ИМЕННО такой сценарий: планировочная стадия,
// interactive:true (чтобы первая неполная попытка ушла в штатный
// "один бесплатный retry" — IncompleteWorkError/attempt==0), вторая попытка
// снова не проходит проверку — это и есть "терминальная ошибка
// completionCheck", которую раньше отправляли через commitVerifyFailure.
func TestRunWithRetry_PlanningCompletionExhausted_ReachesFailed(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = 15
	// setupHookOrch стартует стадию в running — planning-раннеры сами делают
	// переход в planning (EvStartPlanning) до входа в runWithRetry, здесь
	// эмулируем то же самое напрямую через Store, не через FSM-событие,
	// чтобы тест не зависел от деталей runPlanningAgent.
	if err := o.opts.Store.Apply(&state.Transition{
		StageID: "s1", From: state.StatusRunning, To: state.StatusPlanning, Event: "test_setup",
	}); err != nil {
		t.Fatal(err)
	}

	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// plan.md существует, но без обязательных секций (Tasks/Assumptions/
	// Acceptance Criteria) — CheckPlanCompletionFor(interactive=true) вернёт
	// IncompleteWorkError КАЖДЫЙ раз, потому что тестовый agentFn ничего не
	// правит между попытками.
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
	// Планировочный сбой — НЕ verify-исход, он обязан идти через ОБЫЧНЫЙ
	// EvFail ("fail"), а не через атомарный EvVerifyFail ("verify_fail"),
	// чей ограниченный From и стал причиной H2.
	if !strings.Contains(string(data), `"event":"fail"`) {
		t.Errorf("expected plain EvFail (\"fail\") in events.jsonl, got: %s", data)
	}
	if strings.Contains(string(data), `"event":"verify_fail"`) {
		t.Errorf("a planning completion failure must never use the atomic EvVerifyFail (\"verify_fail\"): %s", data)
	}
}

// TestRunWithRetry_MissingArtifact_FailsViaPlainEvFail — H2: обычная (не
// verify-исход) терминальная ошибка completion-проверки на ИСПОЛНИТЕЛЬСКОЙ
// стадии (implementation/review/autonomous) тоже обязана идти через
// обычный EvFail, а не через commitVerifyFailure/EvVerifyFail — поведение
// не изменилось относительно кода ДО добавления AI-verify.
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
