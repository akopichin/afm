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

// activateAutoStage переводит auto-стадию в Ready для автономного трека: создаёт stageDir, пишет
// autonomous.flag, фиксирует durable EvReady.
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

// shouldGateAutoRun reports whether s's first activation should pause instead of proceeding: auto_run is
// explicitly false AND this stage has never been through a pause cycle before.
func (o *Orchestrator) shouldGateAutoRun(s flow.Stage) bool {
	return s.AutoRunDisabled() && o.opts.Store.PausedFrom(s.ID) == ""
}

// activateScriptStage activates a script-only stage the same way activateAutoStage activates an auto
// stage: no plan.md, straight to Ready. Returns false (no-op) if s is not a script stage.
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
//
// Этап 9: re-fetches s from the registry before depsDone — a caller may have captured s moments earlier (e.g.
// StageByID in TryActivatePrePlanned's loop), and a concurrent AddStageToGraph commit could have added a new
// dependency to THIS stage in between. This does not fully close the race (there is still a zero-width window
// between the re-fetch below and the Trigger call), but it narrows it from "the whole scheduling pass" to "one
// in-memory read" — see dev-plan's T9.4 notes and TestCommit_SharedAdmissionSerialization.
func (o *Orchestrator) activatePrePlannedStage(s flow.Stage) {
	if fresh := o.reg().Stage(s.ID); fresh != nil {
		s = *fresh
	}
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
	for _, s := range o.reg().Stages() {
		if !s.NeedsPlanning() && o.opts.Store.Get(s.ID) == state.StatusPending {
			o.activatePrePlannedStage(s)
		}
	}

	// Newly activated stages may now be ready to run.
	o.startReadyStages(ctx)
}

// startPlanningForUnblocked starts planning for pending stages whose dependencies are all done. Stages
// with eager_planning start at flow start and are never gated here.
func (o *Orchestrator) startPlanningForUnblocked(ctx context.Context) {
	for _, s := range o.reg().Stages() {
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

	ready := o.reg().ReadyStages(statuses)
	for _, id := range ready {
		if stage := o.reg().Stage(id); stage != nil {
			o.startReadyStage(ctx, *stage)
		}
	}
}

// startReadyStage is shared by normal scheduling and manual retry. Only the winner of the durable ready ->
// running transition may spawn the stage. Re-fetches s from the registry first — see activatePrePlannedStage's
// doc comment (Этап 9) for why.
func (o *Orchestrator) startReadyStage(ctx context.Context, s flow.Stage) bool {
	if fresh := o.reg().Stage(s.ID); fresh != nil {
		s = *fresh
	}
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

// clearInteractiveSessions очищает сессии и stream-json всех диалоговых фаз перед manual retry, включая
// autonomous-фазу.
func clearInteractiveSessions(stageDir string) {
	for _, ph := range dialogPhases(stageDir) {
		_ = os.Remove(stagefiles.SessionFile(stageDir, ph))
		_ = os.Truncate(filepath.Join(stageDir, jsonlFileForPhase(ph)), 0)
	}
}

// retryStage долговечно переводит проваленную стадию из failed и перезапускает её.
func (o *Orchestrator) retryStage(ctx context.Context, stageID string) {
	current := o.currentStatus(stageID)

	if current != state.StatusFailed {
		return
	}

	stage := o.reg().Stage(stageID)
	if stage == nil {
		return
	}

	// Тест-сейм (nil в проде): точка, где тест может смоделировать проигрыш CAS, переведя стадию из failed на
	// другой горутине между проверкой статуса выше и CAS ниже.
	if o.retryCASBarrier != nil {
		o.retryCASBarrier(stageID)
	}

	// CAS ПЕРВЫМ: право на retry захватывает ровно один вызывающий.
	if _, ok := o.Trigger(stageID, bus.EvManualRetry, bus.GuardCtx{}, ""); !ok {
		return
	}

	// Только победитель CAS чистит. Для interactive-стадии чистим phase-сессии (leftover <phase>.session.json
	// сослался бы на несуществующий разговор → claude «No conversation found») и усекаем <phase>.jsonl
	// (иначе *.question.json из прошлого violating-рана мгновенно пере-сработал бы у поллера). Race-free
	// w.r.t. поллера: после EvManualRetry стадия pending (non-active), и поллер её пропускает.
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

	// Keep blocked retries pending so normal scheduling can activate them. A cached plan only skips planning;
	// even eager_planning never permits implementation before dependencies.
	if !o.depsDone(*stage) {
		return
	}
	o.Trigger(stageID, bus.EvReady, bus.GuardCtx{}, readyReason)
	if o.startReadyStage(ctx, *stage) {
		o.startReadyStages(ctx)
	}
}

// failBlockedStages marks pending stages as failed if any of their dependencies are in StatusFailed.
func (o *Orchestrator) failBlockedStages() {
	changed := true
	for changed {
		changed = false
		for _, s := range o.reg().Stages() {
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

// shouldExit reports whether the orchestrator loop should stop. Without a dashboard, any terminal state
// (done or failed) is final.
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
	// Agent Mesh T6.3: every STAGE is terminal, but a run-scoped standing role (no RootStageID at all) or an
	// advisory task whose root stage already finished are both invisible to the stage-status check above — see
	// state.AssistantsState.RunBlocker's doc comment for why this is a separate, wider check than the
	// per-stage RequiredBlocker gate.
	if o.opts.AssistantsEnabled {
		rs := o.opts.Store.Snapshot()
		if _, blocked := rs.Assistants.RunBlocker(); blocked || runAgentHasUnseenMessages(rs) {
			return false
		}
	}
	if o.opts.DashboardURL == "" {
		return true
	}
	snap := o.opts.Store.Snapshot()
	return snap.AllDone()
}
