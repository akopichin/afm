package orchestrator

import (
	"context"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/state"
)

// Тест-сеймы для регрессий по гонкам completeStage/withBeforeHook/retryStage. Аналог уже существующего
// InterruptChanForTest.

// CompleteStageForTest вызывает приватный completeStage с переданным «снимком» статуса current — позволяет
// тесту смоделировать проигрыш CAS EvComplete.
func CompleteStageForTest(o *Orchestrator, stageID string, current state.StageStatus) {
	o.completeStage(context.Background(), stageID, current, "")
}

// PendingAfterHooksForTest возвращает текущее значение счётчика живых
// script_after-горутин (см. поле pendingAfterHooks).
func PendingAfterHooksForTest(o *Orchestrator) int32 {
	return o.pendingAfterHooks.Load()
}

// WaitAgentsForTest дожидается завершения всех агентских горутин (тот же WaitAgents, что Run вызывает на
// shutdown) — чтобы тест читал счётчики после того, как спавн отработал.
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

// SetVerifyOutcomeGuardHookForTest вставляет Pause/Revise непосредственно перед CAS EvVerifyFail.
func SetVerifyOutcomeGuardHookForTest(o *Orchestrator, fn func(stageID string)) {
	o.verifyOutcomeGuardHook = fn
}

// SetVerifyAgentRunnerForTest инъектирует фейковый раннер agent-шагов AI-verify для тестов из внешнего
// package orchestrator_test — тот же приём, что и остальные *ForTest выше.
func SetVerifyAgentRunnerForTest(o *Orchestrator, fn func(ctx context.Context, s flow.Stage, cmd, prompt, logFile, resultFile string) (verify.RunOutcome, error)) {
	o.runVerifyAgent = fn
}

// SetCommitAddStageBarrierForTest инъектирует хук, вызываемый в CommitAddStage сразу после planDraft
// (advisory preflight) и непосредственно перед Store.AddStageToGraph — см. поле commitAddStageBarrier
// (Этап 9, T9.4's TestCommit_NoLockHeldDuringModelWorkOrPreflight).
func SetCommitAddStageBarrierForTest(o *Orchestrator, fn func()) {
	o.commitAddStageBarrier = fn
}

// SetRunHelperAgentForTest инъектирует фейковый раннер одного хода помощника (mesh-задачи) для тестов из
// внешнего package orchestrator_test — тот же приём, что и SetVerifyAgentRunnerForTest, нужен Agent Mesh
// T6.3's run-level barrier tests, которым требуется реальный Orchestrator.Run(ctx) (через runOrchestratorAsync)
// с управляемой по времени "подпроцессом" mesh-задачей, а не продакшн-экзекьютор.
func SetRunHelperAgentForTest(o *Orchestrator, fn func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error)) {
	o.runHelperAgent = fn
}

// MeshClientForTest mints a usable *executor.MeshClient for participant through the SAME unexported
// meshClientFor the production run() path calls for every real stage/helper invocation (Agent Mesh Phase 3,
// T3.3) — exposed for Phase 9's T9.1 real-adapter smoke test
// (pkg/orchestrator/agentcomm_adapter_smoke_test.go). That test must also stand up a real *server.Server,
// and pkg/server already imports pkg/orchestrator (see agentcomm_integration_server_test.go's own doc
// comment on why that forces it into the external orchestrator_test package), so it cannot reach
// meshClientFor directly the way an internal-package test could.
func MeshClientForTest(o *Orchestrator, participant agentcomm.ParticipantID) *executor.MeshClient {
	return o.meshClientFor(participant)
}

// AgentCommInboxBlockForTest renders participant's pending inbox exactly as the unexported agentCommInboxFor
// does for a real resumed stage/helper invocation — exposed for the same T9.1 smoke test above, which must
// hand a REAL adapter process the SAME <inbox> block production code builds on resume, not a test-local
// reimplementation of its formatting.
func AgentCommInboxBlockForTest(o *Orchestrator, participant agentcomm.ParticipantID) (block string, onDelivered func()) {
	return o.agentCommInboxFor(participant)
}

// RunAgentParticipantIDForTest is the participant id AddRunAgent gives the run agent created by operationID
// ("run:<taskID>", the task id being derived from the operation id).
func RunAgentParticipantIDForTest(operationID string) agentcomm.ParticipantID {
	return meshRunParticipantID(deriveTaskID(operationID))
}

// AwaitReleaseForTest is awaitRelease for the external test package's fakes.
func AwaitReleaseForTest(t testing.TB, release <-chan struct{}) { awaitRelease(t, release) }

// ParkForQuotaForTest вызывает приватный parkForQuota — тест подставляет эпоху, в которой «якобы» стартовал ход,
// чтобы проверить фенс ExpectEpoch для устаревшего парка.
func ParkForQuotaForTest(o *Orchestrator, s flow.Stage, phase string, epoch uint64, limit *executor.UsageLimitError) {
	o.parkForQuota(context.Background(), s, phase, epoch, limit, false, false, func() {})
}

// WakeFromQuotaForTest вызывает приватный wakeFromQuota для парка с номером parkSeq — тест подставляет seq
// старого таймера, чтобы проверить фенс ExpectStageSeq.
func WakeFromQuotaForTest(o *Orchestrator, stageID string, parkSeq uint64) {
	o.wakeFromQuota(context.Background(), stageID, parkSeq)
}

// SetFingerprintFnForTest подменяет отпечаток рабочего дерева (review_patience) для внешних тестов.
func SetFingerprintFnForTest(o *Orchestrator, fn func(dir string) (string, bool)) {
	o.fingerprintFn = fn
}

// ParkVerifyForQuotaForTest вызывает приватный parkVerifyForQuota: тест подставляет эпоху, в которой «якобы»
// стартовал ход автора, чтобы проверить фенс ExpectEpoch для устаревшего verify-парка.
func ParkVerifyForQuotaForTest(o *Orchestrator, s flow.Stage, epoch uint64, limit *executor.UsageLimitError, verifyID string, step int) {
	o.parkVerifyForQuota(context.Background(), s, phaseImplementation, epoch, &verifyLimitError{Limit: limit, VerifyID: verifyID, Step: step}, verifyCounters{}, false, func() {})
}

// VerifyResumeCtxForTest возвращает ctx с приказом пробуждения verify-парка стадии — таким, как его строит
// wakeFromQuota: тест гоняет retry-цикл напрямую и проверяет, чья это работа.
func VerifyResumeCtxForTest(ctx context.Context, o *Orchestrator, stageID string) context.Context {
	st := o.opts.Store.Snapshot().Stages[stageID]
	return withVerifyResume(ctx, *st.Quota, st.Epoch)
}

// RunRetryLoopForTest запускает retry-цикл стадии напрямую — без раннера, lease и спавна.
func RunRetryLoopForTest(ctx context.Context, o *Orchestrator, s flow.Stage, phase string, agentFn func(retryContext string) error, completionCheck func() error, onUserInterrupted func()) {
	o.runRetryLoop(ctx, s, phase, false, agentFn, completionCheck, onUserInterrupted)
}

// SetLifecycleHooksForTest подставляет lifecycle-dispatcher до Run (внешние тесты собирают оркестратор через Options
// без Hooks).
func SetLifecycleHooksForTest(o *Orchestrator, d *lifecyclehooks.Dispatcher) { o.hooks = d }
