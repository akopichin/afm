package orchestrator

import (
	"context"

	"github.com/akopichin/afm/pkg/state"
)

// Тест-сеймы для регрессий по гонкам completeStage/withBeforeHook/retryStage.
// Живут в package orchestrator (не _test), поэтому имеют доступ к приватным
// completeStage/retryStage/pendingAfterHooks, но вызываются из внешнего
// package orchestrator_test через эти экспортированные обёртки. Аналог уже
// существующего InterruptChanForTest.

// CompleteStageForTest вызывает приватный completeStage с переданным «снимком»
// статуса current — позволяет тесту смоделировать проигрыш CAS EvComplete
// (реальный статус стадии уже paused, а current устарел = running).
func CompleteStageForTest(o *Orchestrator, stageID string, current state.StageStatus) {
	o.completeStage(context.Background(), stageID, current, "")
}

// PendingAfterHooksForTest возвращает текущее значение счётчика живых
// script_after-горутин (см. поле pendingAfterHooks).
func PendingAfterHooksForTest(o *Orchestrator) int32 {
	return o.pendingAfterHooks.Load()
}

// WaitAgentsForTest дожидается завершения всех агентских горутин (тот же
// WaitAgents, что Run вызывает на shutdown) — чтобы тест читал счётчики после
// того, как спавн отработал.
func WaitAgentsForTest(o *Orchestrator) {
	o.concurrency.WaitAgents()
}

// RetryStageForTest вызывает приватный retryStage напрямую (в обход
// runContext), чтобы тест мог драйвить его без активного Run.
func RetryStageForTest(o *Orchestrator, stageID string) {
	o.retryStage(context.Background(), stageID)
}

// SetRetryCASBarrierForTest инъектирует хук, вызываемый в retryStage между
// проверкой статуса failed и CAS EvManualRetry (см. поле retryCASBarrier).
func SetRetryCASBarrierForTest(o *Orchestrator, fn func(stageID string)) {
	o.retryCASBarrier = fn
}
