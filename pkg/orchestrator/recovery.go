package orchestrator

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// autoRecoverFailedStages resets every stage currently in StatusFailed back
// to Pending when auto_recover is enabled (default true), so a run
// interrupted by a killed process/container resumes automatically instead of
// requiring manual `afm retry` on each failed stage. All failed stages are
// reset regardless of failure reason (context canceled vs a genuine bug are
// treated identically — see the design doc for why). Order does not matter
// here: the reset stages re-enter the Pending flow in startPlanningForPending
// below, which already gates on depsDone(), so depends_on order falls out on
// its own without any extra bookkeeping.
func (o *Orchestrator) autoRecoverFailedStages() {
	if !o.opts.Config.IsAutoRecover() {
		return
	}
	for _, s := range o.opts.Stages {
		if o.opts.Store.Get(s.ID) != state.StatusFailed {
			continue
		}
		if s.Interactive {
			clearInteractiveSessions(filepath.Join(o.opts.RunDir, s.ID))
		}
		if _, ok := o.Trigger(s.ID, bus.EvManualRetry, bus.GuardCtx{}, "auto_recover"); ok {
			log.Printf("auto_recover: stage %q failed -> pending", s.ID)
		}
	}
}

// startPlanningForPending starts or resumes stages based on their saved status.
// Terminal states done and awaiting_approval are left untouched. Failed is
// only "terminal" when auto_recover is disabled: by default (auto_recover
// enabled) the call to autoRecoverFailedStages above resets every failed
// stage to pending first, so by the time the switches below run, a stage
// that failed no longer has StatusFailed at all — it re-enters the same
// pending flow as a stage that never ran.
// Interrupted transient states (planning, running, revising) are restarted.
// Pending stages start planning for the first time.
func (o *Orchestrator) startPlanningForPending(ctx context.Context) {
	o.autoRecoverFailedStages()
	for _, s := range o.opts.Stages {
		// A crashed script_after resume is invisible to the status-based
		// switches below: after-hooks never touch the FSM, so a stage stuck
		// waiting on a retry/skip decision for its after-hook is still
		// StatusDone on disk. Detect it directly via hook_pending.json
		// (Hook == "after") and resume the wait BEFORE the normal
		// status-based dispatch, regardless of the stage's current status.
		if pending, ok := readHookPending(filepath.Join(o.opts.RunDir, s.ID)); ok && pending.Hook == "after" {
			o.resumeAfterHook(ctx, s)
			continue
		}

		if !s.NeedsPlanning() {
			current := o.opts.Store.Get(s.ID)

			switch current {
			case state.StatusDone, state.StatusFailed, state.StatusAwaitingApproval:
				continue
			case state.StatusRunning, state.StatusReady, state.StatusAwaitingUserInput, state.StatusRevising, state.StatusHookFailed:
				// Let these fall through to the normal resume logic below. Revising
				// must fall through too (not hit default→activateAutoStage below):
				// an auto stage revised mid-run has no plan.md and isn't Pending, so
				// treating it as a fresh activation candidate would fire a no-op
				// EvReady (invalid from Revising, silently dropped) and strand the
				// stage in Revising forever instead of resuming via
				// runAutonomousWithFeedback below. HookFailed must fall through for
				// the same reason: it's not Pending, so the default branch below
				// would fire an invalid, silently-dropped EvReady instead of
				// resuming the pending before-hook decision via the second switch.
			case state.StatusRetrying:
				stageDir := filepath.Join(o.opts.RunDir, s.ID)
				if err := stagefiles.CheckCompletion(stageDir, ".", s); err == nil {
					o.Trigger(s.ID, bus.EvComplete, bus.GuardCtx{}, "recovered .done")
					o.maybeRunAfterHook(ctx, s.ID)
					continue
				}
				o.Trigger(s.ID, bus.EvReady, bus.GuardCtx{}, "retry recovery")
			default:
				if !o.depsDone(s) {
					continue
				}

				if o.activateAutoStage(s) {
					continue
				}

				stageDir := filepath.Join(o.opts.RunDir, s.ID)
				if err := os.MkdirAll(stageDir, 0755); err != nil {
					o.Trigger(s.ID, bus.EvFail, bus.GuardCtx{}, "mkdir failed")
					continue
				}
				dst := filepath.Join(stageDir, "plan.md")
				if s.Plan != "" {
					if err := copyFile(resolvePlanSource(o.opts.RunDir, s), dst); err != nil {
						o.Trigger(s.ID, bus.EvFail, bus.GuardCtx{}, "copy plan failed")
						continue
					}
				} else if s.Interactive {
					if err := os.WriteFile(dst, []byte(s.Description), 0644); err != nil {
						o.Trigger(s.ID, bus.EvFail, bus.GuardCtx{}, "write plan failed")
						continue
					}
				}
				o.Trigger(s.ID, bus.EvReady, bus.GuardCtx{}, "")
				continue
			}
		}

		current := o.opts.Store.Get(s.ID)

		switch current {
		case state.StatusDone, state.StatusFailed, state.StatusAwaitingApproval, state.StatusReady:
			continue
		case state.StatusAwaitingUserInput:
			o.spawnAgent(ctx, s, o.resumeInteractiveAgent)
		case state.StatusHookFailed:
			// Crashed while blocked on a before-hook retry/skip decision.
			// Re-enter the wait (not a silent retry) — see resumeHookFailedWait.
			o.spawnAgent(ctx, s, o.resumeHookFailedWait)
		case state.StatusRetrying:
			// Interrupted retry — check completion or restart
			stageDir := filepath.Join(o.opts.RunDir, s.ID)
			if err := stagefiles.CheckCompletion(stageDir, ".", s); err == nil {
				o.Trigger(s.ID, bus.EvComplete, bus.GuardCtx{}, "recovered .done")
				o.maybeRunAfterHook(ctx, s.ID)
				continue
			}
			if stagefiles.CheckPlanCompletion(stageDir) == nil && s.NeedsPlanning() {
				o.Trigger(s.ID, bus.EvPlanReady, bus.GuardCtx{}, "recovered plan.md")
				o.autoApproveIfConfigured(ctx, s)
				continue
			}
			// Restart planning from scratch
			o.Trigger(s.ID, bus.EvStartPlanning, bus.GuardCtx{}, "restart after retry")
			o.spawnAgent(ctx, s, o.runPlanningAgent)
		case state.StatusRevising:
			// Interrupted revision — restart with feedback, using whichever phase
			// was actually interrupted (agent_suggest can revise any active phase,
			// not only planning — detectInterruptedPhase looks at *.session.json
			// mtimes, same helper resumeInteractiveAgent already uses).
			stageDir := filepath.Join(o.opts.RunDir, s.ID)
			switch o.detectInterruptedPhase(stageDir) {
			case phaseImplementation:
				o.spawnAgent(ctx, s, o.runImplementationWithFeedback)
			case phaseReview:
				o.spawnAgent(ctx, s, o.runReviewWithFeedback)
			case phaseAutonomous:
				o.spawnAgent(ctx, s, o.runAutonomousWithFeedback)
			default:
				o.spawnAgent(ctx, s, o.runPlanningWithFeedback)
			}
		case state.StatusRunning:
			// Check if .done exists (agent completed but orchestrator missed the event)
			stageDir := filepath.Join(o.opts.RunDir, s.ID)
			// Script-стадия (Stage.IsScript()): нет ни plan.md, ни агента —
			// перезапускаем сам скрипт напрямую, тем же способом, что и
			// retryStage (scheduling.go) для вручную ретраенной failed
			// script-стадии. Без этой проверки код ниже безусловно падал бы в
			// runImplementationAgent, который ищет plan.md — файл, которого у
			// script-стадии никогда нет — и стадия падала бы с левой ошибкой
			// "no such file or directory" вместо перезапуска скрипта.
			// Проверяется ДО autonomous-ветки — script-стадия никогда не
			// бывает autonomous.
			if s.IsScript() {
				if err := stagefiles.CheckCompletion(stageDir, ".", s); err == nil {
					o.Trigger(s.ID, bus.EvComplete, bus.GuardCtx{}, "recovered .done")
					o.maybeRunAfterHook(ctx, s.ID)
					continue
				}
				o.spawnAgent(ctx, s, o.withBeforeHook(o.runScriptStage))
				continue
			}
			// Autonomous track resume: if this is an autonomous stage, look for
			// execution_summary.md instead of .done, and restart the autonomous
			// agent rather than the standard implementation agent.
			if isAutonomousStage(stageDir) || s.IsAuto() {
				if stagefiles.CheckAutonomousCompletion(stageDir) == nil {
					o.Trigger(s.ID, bus.EvComplete, bus.GuardCtx{}, "recovered execution_summary.md")
					o.maybeRunAfterHook(ctx, s.ID)
					continue
				}
				o.spawnAgent(ctx, s, o.runAutonomousAgent)
				continue
			}
			if err := stagefiles.CheckCompletion(stageDir, ".", s); err == nil {
				o.Trigger(s.ID, bus.EvComplete, bus.GuardCtx{}, "recovered .done")
				o.maybeRunAfterHook(ctx, s.ID)
				continue
			}
			// Interrupted implementation — restart with existing plan
			o.spawnAgent(ctx, s, o.runImplementationAgent)
		default:
			// Pending, planning, or unknown — check if planning already completed
			if s.NeedsPlanning() {
				stageDir := filepath.Join(o.opts.RunDir, s.ID)
				if stagefiles.CheckPlanCompletion(stageDir) == nil {
					o.Trigger(s.ID, bus.EvPlanReady, bus.GuardCtx{}, "recovered plan.md")
					o.autoApproveIfConfigured(ctx, s)
					continue
				}
			}
			// Pending stages wait for depends_on unless eager_planning is set.
			// Interrupted planning (status "planning") always resumes.
			if current == state.StatusPending && !s.EagerPlanning && !o.depsDone(s) {
				continue
			}
			o.Trigger(s.ID, bus.EvStartPlanning, bus.GuardCtx{}, "")
			o.spawnAgent(ctx, s, o.startWithSupervisor)
		}
	}

	// Cascade failures to stages blocked by failed dependencies.
	o.failBlockedStages()

	// Start planning for stages whose dependencies are already done
	// (covers recovery where a dependency was recovered as done above).
	o.startPlanningForUnblocked(ctx)

	// Start implementation for stages that are ready.
	o.startReadyStages(ctx)

	// Activate pre-planned stages whose deps just became satisfied.
	o.tryActivatePrePlanned(ctx)
}

// resumeInteractiveAgent re-runs the agent of the phase whose
// session.json exists most recently. The phase is detected by looking at
// mtimes of <phase>.session.json files in the stage directory.
func (o *Orchestrator) resumeInteractiveAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	phase := o.detectInterruptedPhase(stageDir)

	switch phase {
	case phasePlanning:
		o.Trigger(s.ID, bus.EvUserAnswered, bus.GuardCtx{Phase: phasePlanning}, "resume interactive")
		o.runPlanningAgent(ctx, s)
	case phaseReview:
		// Review runs after implementation completes; if we see review session
		// open, the stage was paused inside review. Fall through to implementation
		// agent which will re-trigger review at the end.
		fallthrough
	default:
		o.Trigger(s.ID, bus.EvUserAnswered, bus.GuardCtx{Phase: phaseImplementation}, "resume interactive")
		o.runImplementationAgent(ctx, s)
	}
}

func (o *Orchestrator) detectInterruptedPhase(stageDir string) string {
	var latestPhase string
	var latestMtime time.Time
	for _, p := range flow.Phases() {
		phase := string(p)
		fi, err := os.Stat(stagefiles.SessionFile(stageDir, phase))
		if err != nil {
			continue
		}
		if fi.ModTime().After(latestMtime) {
			latestMtime = fi.ModTime()
			latestPhase = phase
		}
	}
	return latestPhase
}
