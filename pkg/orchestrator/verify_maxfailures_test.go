package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// newVerifyRejected собирает *VerifyRejectedError — verify-отклонение
// (needs_changes), которое тратит бюджет max_failures.
func newVerifyRejected(step int) *VerifyRejectedError {
	return &VerifyRejectedError{
		IncompleteWorkError: &stagefiles.IncompleteWorkError{Reason: "verify needs changes"},
		ReportID:            "ver-report",
		Step:                step,
	}
}

// countRetryNotices считает записи retry_scheduled в notices.jsonl — детерминированно
// (AppendNotice синхронна), в отличие от асинхронной UI-шины.
func countRetryNotices(t *testing.T, runDir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.Contains(line, `"`+string(bus.EventRetryScheduled)+`"`) {
			n++
		}
	}
	return n
}

// TestVerifyMaxFailures_DefaultOneCorrection — N=1 (дефолт): ровно одна
// коррекция, второе отклонение проваливает стадию. Совпадает с прежним
// однократным поведением.
func TestVerifyMaxFailures_DefaultOneCorrection(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.maxRetries = 15

	var attempts int
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(string) error { attempts++; return nil },
		func() error { return newVerifyRejected(1) },
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if attempts != 2 {
		t.Fatalf("N=1: expected 2 attempts (1 correction), got %d", attempts)
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusFailed {
		t.Fatalf("status = %v, want failed", got)
	}
}

// TestVerifyMaxFailures_ZeroStrict — N=0: первое отклонение проваливает стадию
// немедленно, без единой коррекции; переход — verify-driven fail.
func TestVerifyMaxFailures_ZeroStrict(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.maxRetries = 15
	zero := 0
	o.opts.Config.Verify = config.VerifyConfig{MaxFailures: &zero}

	var attempts int
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(string) error { attempts++; return nil },
		func() error { return newVerifyRejected(1) },
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if attempts != 1 {
		t.Fatalf("N=0: expected 1 attempt (no correction), got %d", attempts)
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusFailed {
		t.Fatalf("status = %v, want failed", got)
	}
}

// TestVerifyMaxFailures_ThreeCorrections — N=3: три коррекции, четвёртое
// отклонение проваливает стадию.
func TestVerifyMaxFailures_ThreeCorrections(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.maxRetries = 15
	three := 3
	o.opts.Config.Verify = config.VerifyConfig{MaxFailures: &three}

	var attempts int
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(string) error { attempts++; return nil },
		func() error { return newVerifyRejected(1) },
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if attempts != 4 {
		t.Fatalf("N=3: expected 4 attempts (3 corrections), got %d", attempts)
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusFailed {
		t.Fatalf("status = %v, want failed", got)
	}
}

// TestVerifyMaxFailures_StageOverrideBeatsGlobal — stage override (verify:
// max_failures) побеждает глобальный конфиг: global 1, stage 3 → 3 коррекции.
func TestVerifyMaxFailures_StageOverrideBeatsGlobal(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.maxRetries = 15
	one := 1
	o.opts.Config.Verify = config.VerifyConfig{MaxFailures: &one}

	three := 3
	stage := flow.Stage{ID: "s1", Verify: flow.VerifySpec{MaxFailures: &three}}

	var attempts int
	o.runWithRetry(context.Background(), stage, phaseImplementation,
		func(string) error { attempts++; return nil },
		func() error { return newVerifyRejected(1) },
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if attempts != 4 {
		t.Fatalf("stage override 3: expected 4 attempts (3 corrections), got %d", attempts)
	}
}

// TestVerifyMaxFailures_TransportRetryDoesNotConsumeBudget — транспортный ретрай
// (retryable-ошибка) тратит внешний attempt, но НЕ verify-бюджет: отклонение
// после транспортного ретрая всё равно получает свою коррекцию.
func TestVerifyMaxFailures_TransportRetryDoesNotConsumeBudget(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.maxRetries = 15
	o.retryBackoff = 0 // не спим в тесте

	var attempts int
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(string) error {
			attempts++
			// Попытка 1 — транспортная ошибка (retryable, съедает attempt,
			// НЕ verify-бюджет). Дальше агент завершается штатно, вердикт
			// решает completionCheck.
			if attempts == 1 {
				return errors.New("overloaded")
			}
			return nil
		},
		func() error { return newVerifyRejected(1) },
		func() { t.Error("onUserInterrupted should not be called") },
	)

	// attempt 1: transport error → retry. attempt 2: verify reject → коррекция
	// (verifyFailures 0<1). attempt 3: verify reject → бюджет исчерпан, fail.
	if attempts != 3 {
		t.Fatalf("expected 3 attempts (transport retry + 1 verify correction), got %d", attempts)
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusFailed {
		t.Fatalf("status = %v, want failed", got)
	}
}

// TestVerifyMaxFailures_PlanIncompleteAndVerifyIndependent — план-незавершёнка
// на attempt 0 получает свою однократную коррекцию, а последующее
// verify-отклонение — свою: политики независимы.
func TestVerifyMaxFailures_PlanIncompleteAndVerifyIndependent(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.maxRetries = 15

	planIncomplete := &stagefiles.IncompleteWorkError{Reason: "plan not finished"}

	var attempts int
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(string) error { attempts++; return nil },
		func() error {
			// attempt 0: обычная план-незавершёнка (не verify) → однократный
			// retry. attempt 1+: verify-отклонения (свой бюджет N=1).
			if attempts == 1 {
				return planIncomplete
			}
			return newVerifyRejected(1)
		},
		func() { t.Error("onUserInterrupted should not be called") },
	)

	// attempt 1: plan-incomplete → retry (attempt==0). attempt 2: verify reject
	// → коррекция (verifyFailures 0<1). attempt 3: verify reject → бюджет
	// исчерпан, fail. Итого 3 вызова агента.
	if attempts != 3 {
		t.Fatalf("expected 3 attempts (plan correction + verify correction), got %d", attempts)
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusFailed {
		t.Fatalf("status = %v, want failed", got)
	}
}

// TestVerifyMaxFailures_ExecErrorNotRetried — *VerifyExecError не проходит через
// бюджет коррекций: он не IncompleteWorkError, стадия падает на attempt 0.
func TestVerifyMaxFailures_ExecErrorNotRetried(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.maxRetries = 15
	three := 3
	o.opts.Config.Verify = config.VerifyConfig{MaxFailures: &three}

	var attempts int
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(string) error { attempts++; return nil },
		func() error { return &VerifyExecError{Reason: "verify timed out", Step: 2} },
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if attempts != 1 {
		t.Fatalf("VerifyExecError must not be retried, got %d attempts", attempts)
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusFailed {
		t.Fatalf("status = %v, want failed", got)
	}
}

// TestVerifyMaxFailures_RetryEventsPerCorrection — на КАЖДУЮ разрешённую
// коррекцию публикуется ровно один EventRetryScheduled; после исчерпания
// бюджета — один финальный verify-fail без лишнего retry-события.
func TestVerifyMaxFailures_RetryEventsPerCorrection(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = 15
	two := 2
	o.opts.Config.Verify = config.VerifyConfig{MaxFailures: &two}

	var attempts int
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(string) error { attempts++; return nil },
		func() error { return newVerifyRejected(1) },
		func() { t.Error("onUserInterrupted should not be called") },
	)

	// N=2 → две коррекции (attempt 0,1), третье отклонение проваливает.
	if attempts != 3 {
		t.Fatalf("N=2: expected 3 attempts, got %d", attempts)
	}
	// Ровно одно retry-событие на разрешённую коррекцию; финальный fail —
	// verify-driven, без лишнего retry-события.
	if got := countRetryNotices(t, runDir); got != 2 {
		t.Fatalf("expected exactly 2 retry_scheduled notices (one per correction), got %d", got)
	}

	if got := o.opts.Store.Get("s1"); got != state.StatusFailed {
		t.Fatalf("status = %v, want failed", got)
	}
}

// TestVerifyMaxFailures_ResolutionHelper — verifyMaxFailures: stage override
// побеждает глобальный конфиг, тот — дефолт 1.
func TestVerifyMaxFailures_ResolutionHelper(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")

	if got := o.verifyMaxFailures(flow.Stage{ID: "s1"}); got != 1 {
		t.Errorf("no config, no stage override: got %d, want default 1", got)
	}

	five := 5
	o.opts.Config.Verify = config.VerifyConfig{MaxFailures: &five}
	if got := o.verifyMaxFailures(flow.Stage{ID: "s1"}); got != 5 {
		t.Errorf("global config: got %d, want 5", got)
	}

	zero := 0
	if got := o.verifyMaxFailures(flow.Stage{ID: "s1", Verify: flow.VerifySpec{MaxFailures: &zero}}); got != 0 {
		t.Errorf("stage override 0 must beat global 5: got %d, want 0", got)
	}
}
