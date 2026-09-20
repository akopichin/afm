package orchestrator

import (
	"context"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
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

// SetVerifyOutcomeGuardHookForTest инъектирует хук, вызываемый в runWithRetry
// НЕПОСРЕДСТВЕННО перед verify-driven переходами (needs_changes
// incomplete-retry / финальный EvFail, см. поле verifyOutcomeGuardHook,
// retry.go's verifyOutcomeStillOwned) — тест может внутри него смоделировать
// конкурентный Pause()/Revise(), приземлившийся в узком окне между F1-
// проверкой статуса и этим переходом.
func SetVerifyOutcomeGuardHookForTest(o *Orchestrator, fn func(stageID string)) {
	o.verifyOutcomeGuardHook = fn
}

// SetVerifyAgentRunnerForTest инъектирует фейковый раннер agent-шагов
// AI-verify (см. поле runVerifyAgent, verify.go) для тестов из внешнего
// package orchestrator_test — тот же приём, что и остальные *ForTest выше.
// Позволяет собрать полноценный *Orchestrator через New() (реальные FSM/
// Store/раннеры основного агента) и при этом не запускать настоящий
// verify-subprocess.
func SetVerifyAgentRunnerForTest(o *Orchestrator, fn func(ctx context.Context, s flow.Stage, cmd, prompt, logFile, resultFile string) (verify.RunOutcome, error)) {
	o.runVerifyAgent = fn
}
