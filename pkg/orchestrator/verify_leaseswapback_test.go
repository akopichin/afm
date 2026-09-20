package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

// TestRunVerification_SwapBackToAuthorAbortsOnPause_ReturnsPromptly — F2
// (4-е код-ревью): деферренный своп lease обратно на команду автора
// (RunVerification, "verify.go" defer) раньше блокировался на
// Lease.SwapTo(ctx, ...) под ДОЛГОЖИВУЩИМ run-scoped ctx, который Pause() не
// отменяет (Pause только долговечно переводит FSM и сигналит
// o.interruptChans, см. control_api.go). Если слот команды автора в этот
// момент занят ДРУГОЙ стадией, verify не мог вернуть управление вызывающему
// коду — пауза переставала быть отзывчивой, per-step таймаут превышался.
//
// Барьер синхронизации — не time.Sleep: фейковый o.runVerifyAgent САМ, ещё
// до возврата из шага verify (пока lease держит слот верификатора, а слот
// автора уже физически свободен — SwapTo release-before-acquire), кладёт
// "чужой" токен в claudeSem (симулируя другую стадию, успевшую занять
// освободившийся слот автора) И сигналит на interruptCh. К моменту, когда
// RunVerification доходит до дефера (после того как шаг единственный раз
// пройден с pass), сигнал уже гарантированно лежит в буфере канала —
// pauseAwareVerifyCtx детерминированно подхватывает его (см. приоритетную
// неблокирующую проверку в его горутине-наблюдателе), никакой гонки с
// реальным временем не остаётся. Промптность возврата проверяется через
// select с таймаутом (channel-based), а не подсчётом прошедшего времени.
func TestRunVerification_SwapBackToAuthorAbortsOnPause_ReturnsPromptly(t *testing.T) {
	claudeSem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	codexSem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	mgr := concurrency.NewWithSemaphores(bus.NewCriticalBus(1), map[string]concurrency.Semaphore{
		"claude": claudeSem,
		"codex":  codexSem,
	}, "claude")

	lease, err := mgr.AcquireLease(context.Background(), "claude")
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}

	s := flow.Stage{ID: "s1", Command: "claude",
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
	o, _ := newVerifyTestOrchestrator(t, s)
	o.stageLeases.Store(s.ID, lease)

	interruptCh := make(chan struct{}, 1)
	o.interruptChans.Store(s.ID, interruptCh)

	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		// Слот "claude" (автора) в этот момент уже release'нут SwapTo при
		// переходе на верификатора — занимаем его чужим токеном И сигналим
		// прерывание ДО того, как шаг вернётся. Оба события гарантированно
		// произойдут раньше, чем RunVerification дойдёт до deferred swap-back.
		claudeSem <- struct{}{}
		interruptCh <- struct{}{}
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	}

	done := make(chan error, 1)
	go func() { done <- o.RunVerification(context.Background(), s, phaseImplementation) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunVerification: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunVerification did not return promptly — swap-back to the author slot blocked on the busy semaphore despite the pause signal")
	}

	// Верификаторский слот должен быть освобождён (Release случился ДО
	// попытки захватить занятый слот автора), а слот автора должен остаться
	// занятым ровно "чужим" токеном — lease не мог его захватить (буфер
	// полон) и не держит теперь НИЧЕГО.
	if len(codexSem) != 0 {
		t.Errorf("verifier slot must be released, len=%d", len(codexSem))
	}
	if len(claudeSem) != 1 {
		t.Errorf("author slot must remain held only by the external holder (lease must not have sneaked in), len=%d", len(claudeSem))
	}
}
