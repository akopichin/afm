package orchestrator

import (
	"context"
	"os"
	"path/filepath"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// depsDone checks whether all dependencies of a stage are in StatusDone.
func (o *Orchestrator) depsDone(s flow.Stage) bool {
	for _, dep := range s.DependsOn {
		if o.opts.Store.Get(dep) != state.StatusDone {
			return false
		}
	}
	return true
}

// activateAutoStage переводит auto-стадию (agents: [auto]) в Ready для автономного
// трека: создаёт stageDir, пишет autonomous.flag (жёсткий автономный трек — без
// supervisor и без plan.md), фиксирует durable EvReady. Возвращает true, если стадия
// действительно auto (и обработана). Единая точка активации auto-стадии для обоих
// путей: tryActivatePrePlanned (scheduling) и startPlanningForPending (recovery).
func (o *Orchestrator) activateAutoStage(s flow.Stage) bool {
	if !s.IsAuto() {
		return false
	}
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		o.Trigger(s.ID, bus.EvFail, bus.GuardCtx{}, "mkdir failed")
		return true
	}
	_ = os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0644)
	o.Trigger(s.ID, bus.EvReady, bus.GuardCtx{}, "auto stage")
	return true
}

// shouldGateAutoRun reports whether s's first activation should pause
// instead of proceeding: auto_run is explicitly false AND this stage has
// never been through a pause cycle before (PausedFrom is the permanent
// marker — see state.StageState.PausedFrom). Without the second condition a
// stage retried after failing (which re-enters Pending via EvManualRetry)
// would re-pause on every retry instead of only the very first activation.
func (o *Orchestrator) shouldGateAutoRun(s flow.Stage) bool {
	return s.AutoRunDisabled() && o.opts.Store.PausedFrom(s.ID) == ""
}

// activateScriptStage activates a script-only stage (Stage.IsScript()) the
// same way activateAutoStage activates an auto stage: no plan.md, straight
// to Ready. Returns false (no-op) if s is not a script stage.
func (o *Orchestrator) activateScriptStage(s flow.Stage) bool {
	if !s.IsScript() {
		return false
	}
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		o.Trigger(s.ID, bus.EvFail, bus.GuardCtx{}, "mkdir failed")
		return true
	}
	o.Trigger(s.ID, bus.EvReady, bus.GuardCtx{}, "script stage")
	return true
}

// activatePrePlannedStage prepares a pending stage that needs no planning agent.
// Startup recovery and dependency-driven activation use the same gates and files.
func (o *Orchestrator) activatePrePlannedStage(s flow.Stage) {
	if !o.depsDone(s) {
		return
	}
	if o.shouldGateAutoRun(s) {
		o.Trigger(s.ID, bus.EvPause, bus.GuardCtx{}, "auto_run: false")
		return
	}
	if o.activationBlocked() {
		return
	}
	if o.activateAutoStage(s) || o.activateScriptStage(s) {
		return
	}

	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		o.Trigger(s.ID, bus.EvFail, bus.GuardCtx{}, "mkdir failed")
		return
	}
	dst := filepath.Join(stageDir, "plan.md")
	if s.Plan != "" {
		if err := copyFile(resolvePlanSource(o.opts.RunDir, s), dst); err != nil {
			o.Trigger(s.ID, bus.EvFail, bus.GuardCtx{}, "copy plan failed")
			return
		}
	} else if s.Interactive {
		if err := os.WriteFile(dst, []byte(s.Description), 0644); err != nil {
			o.Trigger(s.ID, bus.EvFail, bus.GuardCtx{}, "write plan failed")
			return
		}
	}
	o.Trigger(s.ID, bus.EvReady, bus.GuardCtx{}, "")
}

// tryActivatePrePlanned activates pending stages that need no planning agent.
func (o *Orchestrator) tryActivatePrePlanned(ctx context.Context) {
	for _, s := range o.opts.Stages {
		if !s.NeedsPlanning() && o.opts.Store.Get(s.ID) == state.StatusPending {
			o.activatePrePlannedStage(s)
		}
	}

	// Newly activated stages may now be ready to run.
	o.startReadyStages(ctx)
}

// startPlanningForUnblocked starts planning for pending stages whose
// dependencies are all done. Stages with eager_planning start at flow
// start and are never gated here.
func (o *Orchestrator) startPlanningForUnblocked(ctx context.Context) {
	for _, s := range o.opts.Stages {
		if !s.NeedsPlanning() {
			continue
		}
		if o.opts.Store.Get(s.ID) != state.StatusPending {
			continue
		}
		if !o.depsDone(s) {
			continue
		}
		if o.shouldGateAutoRun(s) {
			o.Trigger(s.ID, bus.EvPause, bus.GuardCtx{}, "auto_run: false")
			continue
		}
		if o.activationBlocked() {
			continue // review mode: hold new activations; the stage stays pending/ready
		}
		// Synchronous transition out of pending guards against double
		// start: a second call sees "planning" and skips the stage.
		if _, ok := o.Trigger(s.ID, bus.EvStartPlanning, bus.GuardCtx{Stage: s}, "deps done"); !ok {
			continue
		}
		o.spawnKind(ctx, s, kindPlanning, o.runPlanningAgent)
	}
}

// startReadyStages starts ready stages whose dependencies are done.
func (o *Orchestrator) startReadyStages(ctx context.Context) {
	snap := o.opts.Store.Snapshot()
	statuses := make(map[string]state.StageStatus, len(snap.Stages))
	for id, s := range snap.Stages {
		statuses[id] = s.Status
	}

	ready := o.graph.ReadyStages(statuses)
	for _, id := range ready {
		if stage := o.graph.Stage(id); stage != nil {
			o.startReadyStage(ctx, *stage)
		}
	}
}

// startReadyStage is shared by normal scheduling and manual retry. Only the
// winner of the durable ready -> running transition may spawn the stage.
// Recheck the gates here because the caller's ready-stage snapshot may be stale.
func (o *Orchestrator) startReadyStage(ctx context.Context, s flow.Stage) bool {
	if o.activationBlocked() || !o.depsDone(s) {
		return false
	}
	if _, ok := o.Trigger(s.ID, bus.EvStartRun, bus.GuardCtx{}, ""); !ok {
		return false
	}

	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	switch {
	case s.IsScript():
		o.concurrency.SpawnAgent(ctx, s, o.withBeforeHook(o.runScriptStage))
	default:
		kind := o.executionKind(s)
		// Publish the autonomous marker before spawning so the dialog poller
		// sees the same track as the runner, regardless of who won the CAS.
		if s.IsAuto() {
			_ = os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0644)
		}
		o.spawnKind(ctx, s, kind, o.withBeforeHook(o.plainRunner(kind)))
	}
	return true
}

// executionKind selects the agent track for a stage ready to execute its work.
// The on-disk flag preserves autonomous decisions made by older flow versions.
func (o *Orchestrator) executionKind(s flow.Stage) string {
	if s.IsAuto() || isAutonomousStage(filepath.Join(o.opts.RunDir, s.ID)) {
		return kindAutonomous
	}
	return kindImplementation
}

// clearInteractiveSessions удаляет claude-сессии и обнуляет stream-json логи всех
// диалоговых фаз стадии — вызывается при manual retry интерактивной стадии, чтобы
// не тянуть phantom-сессию и не перезапускать старый *.question.json. Использует
// dialogPhases (учитывает autonomous.flag) и jsonlFileForPhase (autonomous-фаза
// логируется в autonomous.jsonl, а не autonomous_execution.jsonl).
func clearInteractiveSessions(stageDir string) {
	for _, ph := range dialogPhases(stageDir) {
		_ = os.Remove(stagefiles.SessionFile(stageDir, ph))
		_ = os.Truncate(filepath.Join(stageDir, jsonlFileForPhase(ph)), 0)
	}
}

// retryStage долговечно переводит проваленную стадию из failed и перезапускает
// её (планирование или implementation, в зависимости от наличия plan.md).
// Вызывается СИНХРОННО из Retry (HTTP-обработчик) — переход в Store фиксируется
// до возврата, краш после Retry не теряет интент (recovery резюмит по логу).
func (o *Orchestrator) retryStage(ctx context.Context, stageID string) {
	current := o.currentStatus(stageID)

	if current != state.StatusFailed {
		return
	}

	stage := o.graph.Stage(stageID)
	if stage == nil {
		return
	}

	// Тест-сейм (nil в проде): точка, где тест может смоделировать проигрыш
	// CAS, переведя стадию из failed на другой горутине между проверкой статуса
	// выше и CAS ниже.
	if o.retryCASBarrier != nil {
		o.retryCASBarrier(stageID)
	}

	// CAS ПЕРВЫМ: право на retry захватывает ровно один вызывающий. Два
	// конкурентных Retry-запроса оба проходят устаревшую проверку failed выше,
	// но EvManualRetry (failed -> pending) применится только у одного —
	// проигравший выходит здесь, НЕ тронув файлы стадии. Раньше очистка
	// session/jsonl шла ДО этого CAS: проигравший успевал снести уже свежие
	// данные победителя, стартовавшего новый запуск.
	if _, ok := o.Trigger(stageID, bus.EvManualRetry, bus.GuardCtx{}, ""); !ok {
		return
	}

	// Только победитель CAS чистит. Manual retry of an interactive stage must
	// start a fresh Claude session: a leftover <phase>.session.json may
	// reference a conversation that was never created (phantom), which makes
	// claude fail with "No conversation found". Clear all phase sessions for
	// this stage.
	//
	// Also truncate <phase>.jsonl: detectDialogViolation re-scans the raw
	// stream-json log every poll tick, and a *.question.json Write from a
	// previous (violating) run would otherwise re-fire instantly and make the
	// stage un-retryable. Truncating here stays race-free w.r.t. the poller:
	// after EvManualRetry the stage is pending (non-active) and the poller
	// skips it just the same.
	if stage.Interactive {
		clearInteractiveSessions(filepath.Join(o.opts.RunDir, stageID))
	}

	stageDir := filepath.Join(o.opts.RunDir, stageID)
	readyReason := ""
	switch {
	case stage.IsScript():
		readyReason = "manual retry: script"
	case stage.IsAuto() || isAutonomousStage(stageDir):
		readyReason = "manual retry: autonomous"
	default:
		planPath := filepath.Join(stageDir, "plan.md")
		if _, err := os.Stat(planPath); err != nil {
			if stage.NeedsPlanning() {
				if !stage.EagerPlanning && !o.depsDone(*stage) {
					return
				}
				if _, ok := o.Trigger(stageID, bus.EvStartPlanning, bus.GuardCtx{Stage: *stage}, "manual retry"); !ok {
					return
				}
				o.spawnKind(ctx, *stage, kindPlanning, o.runPlanningAgent)
				return
			}

			// plan.md not yet on disk — try to copy it from stage.Plan source.
			if !o.depsDone(*stage) {
				return
			}
			if stage.Plan == "" {
				o.Trigger(stageID, bus.EvFail, bus.GuardCtx{}, "no plan.md and no plan source configured")
				return
			}
			if err := os.MkdirAll(stageDir, 0755); err != nil {
				o.Trigger(stageID, bus.EvFail, bus.GuardCtx{}, "mkdir failed")
				return
			}
			if err := copyFile(resolvePlanSource(o.opts.RunDir, *stage), planPath); err != nil {
				o.Trigger(stageID, bus.EvFail, bus.GuardCtx{}, "copy plan failed: "+err.Error())
				return
			}
		}
	}

	// Keep blocked retries pending so normal scheduling can activate them
	// (or cascade a dependency failure). A cached plan only skips planning;
	// even eager_planning never permits implementation before dependencies.
	if !o.depsDone(*stage) {
		return
	}
	o.Trigger(stageID, bus.EvReady, bus.GuardCtx{}, readyReason)
	if o.startReadyStage(ctx, *stage) {
		o.startReadyStages(ctx)
	}
}

// failBlockedStages marks pending stages as failed if any of their
// dependencies are in StatusFailed. This prevents the flow from hanging
// when a dependency fails and dependent stages can never start.
func (o *Orchestrator) failBlockedStages() {
	changed := true
	for changed {
		changed = false
		for _, s := range o.opts.Stages {
			current := o.opts.Store.Get(s.ID)

			if current != state.StatusPending {
				continue
			}

			for _, dep := range s.DependsOn {
				if o.opts.Store.Get(dep) == state.StatusFailed {
					o.Trigger(s.ID, bus.EvBlockedByDep, bus.GuardCtx{}, "dep failed")
					changed = true
					break
				}
			}
		}
	}
}

func (o *Orchestrator) allTerminal() bool {
	snap := o.opts.Store.Snapshot()
	if len(snap.Stages) == 0 {
		return true
	}
	for _, s := range snap.Stages {
		if !bus.IsTerminal(s.Status) {
			return false
		}
	}
	return true
}

// shouldExit reports whether the orchestrator loop should stop.
// Without a dashboard, any terminal state (done or failed) is final.
// With a dashboard, exit only when all stages are done — failed stages stay
// visible so the user can retry them without restarting the process.
//
// pendingAfterHooks guards a gap allTerminal() alone can't see: a stage's
// own status can already be "done" while its script_after hook (spawned
// from onAgentCompleted/approveStage, right as that same status flips) is
// still running or blocked waiting on a RetryHook/SkipHook decision —
// runAfterHook deliberately never touches the FSM (see its doc comment), so
// the stage stays "done" throughout. Without this check, Run() could cancel
// its ctx (shutdown) in the very same instant the hook goroutine was
// spawned, killing it before it ever gets to run. Scoped narrowly to
// after-hooks only (not a general "any agent in flight" counter, see
// SpawnAgent's doc comment) — every other agent type already moves its
// stage's FSM status, which allTerminal() below already accounts for.
// pendingReflections mirrors the same guard for the reflect→consolidator
// memory pipeline (maybeRunReflection, reflection.go): it, too,
// never touches the FSM, so a stage stays "done" while its pipeline is
// still running in the background.
//
// reviewTxnActive() is checked first and unconditionally: every owned stage
// a PauseFlow round holds sits in status paused, which allTerminal() below
// treats as non-terminal anyway — but the check is kept explicit and first so
// the intent ("never finalize a run mid review-pause") doesn't depend on that
// coincidence, and so it also covers the brief window where the marker exists
// but ownership/counting hasn't settled yet.
func (o *Orchestrator) shouldExit() bool {
	if o.reviewTxnActive() {
		return false // a review-pause round is in progress; stay alive for the reviewer
	}
	if o.pendingAfterHooks.Load() > 0 || o.pendingReflections.Load() > 0 {
		return false
	}
	if !o.allTerminal() {
		return false
	}
	if o.opts.DashboardURL == "" {
		return true
	}
	snap := o.opts.Store.Snapshot()
	return snap.AllDone()
}
