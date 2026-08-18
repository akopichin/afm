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
			case state.StatusDone, state.StatusFailed, state.StatusAwaitingApproval, state.StatusPaused:
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

				if o.shouldGateAutoRun(s) {
					o.Trigger(s.ID, bus.EvPause, bus.GuardCtx{}, "auto_run: false")
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
		case state.StatusDone, state.StatusFailed, state.StatusAwaitingApproval, state.StatusReady, state.StatusPaused:
			continue
		case state.StatusAwaitingUserInput:
			o.concurrency.SpawnAgent(ctx, s, o.resumeInteractiveAgent)
		case state.StatusHookFailed:
			// Crashed while blocked on a before-hook retry/skip decision.
			// Re-enter the wait (not a silent retry) — see resumeHookFailedWait.
			o.concurrency.SpawnAgent(ctx, s, o.resumeHookFailedWait)
		case state.StatusRetrying:
			o.resumeStageAtStatus(ctx, s, state.StatusRetrying)
		case state.StatusRevising:
			o.resumeStageAtStatus(ctx, s, state.StatusRevising)
		case state.StatusRunning:
			o.resumeStageAtStatus(ctx, s, state.StatusRunning)
		default:
			stageDir := filepath.Join(o.opts.RunDir, s.ID)
			if stagefiles.CheckPlanCompletion(stageDir) == nil {
				o.Trigger(s.ID, bus.EvPlanReady, bus.GuardCtx{}, "recovered plan.md")
				o.autoApproveIfConfigured(ctx, s)
				continue
			}
			if current == state.StatusPending {
				if !s.EagerPlanning && !o.depsDone(s) {
					continue
				}
				if o.shouldGateAutoRun(s) {
					o.Trigger(s.ID, bus.EvPause, bus.GuardCtx{}, "auto_run: false")
					continue
				}
			}
			o.Trigger(s.ID, bus.EvStartPlanning, bus.GuardCtx{}, "")
			o.concurrency.SpawnAgent(ctx, s, o.startWithSupervisor)
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

// resumePlanningStage (re)starts planning for a stage whose recorded status
// says planning should be in progress (or complete on disk) — used both by
// startPlanningForPending's default branch (afm restarted mid-planning) and
// by resumeStageAtStatus below (Continue after a manual pause during
// planning).
func (o *Orchestrator) resumePlanningStage(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if stagefiles.CheckPlanCompletion(stageDir) == nil {
		o.Trigger(s.ID, bus.EvPlanReady, bus.GuardCtx{}, "recovered plan.md")
		o.autoApproveIfConfigured(ctx, s)
		return
	}
	o.Trigger(s.ID, bus.EvStartPlanning, bus.GuardCtx{}, "")
	o.concurrency.SpawnAgent(ctx, s, o.startWithSupervisor)
}

// resumeStageAtStatus (re)spawns whatever goroutine a stage recorded as
// running/planning/revising/retrying needs to make progress again — used at
// afm startup (startPlanningForPending, when the recorded status survived a
// crash) and by Continue (Task 7, when a user resumes a stage from paused).
// Both situations reduce to the same question: "the process this status
// implies isn't running right now — start it."
func (o *Orchestrator) resumeStageAtStatus(ctx context.Context, s flow.Stage, status state.StageStatus) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	switch status {
	case state.StatusPlanning:
		o.resumePlanningStage(ctx, s)
	case state.StatusRetrying:
		// Autonomous stages never go through planning — same check the
		// StatusRunning branch below already does. Without it, a retrying
		// autonomous stage falls through to the generic plan-based fallback
		// and gets routed into EvStartPlanning + runPlanningAgent, a real
		// planning agent that has no plan.md to produce for a stage that's
		// never supposed to have one.
		if isAutonomousStage(stageDir) || s.IsAuto() {
			if stagefiles.CheckAutonomousCompletion(stageDir) == nil {
				o.Trigger(s.ID, bus.EvComplete, bus.GuardCtx{}, "recovered execution_summary.md")
				o.maybeRunAfterHook(ctx, s.ID)
				return
			}
			o.concurrency.SpawnAgent(ctx, s, o.runAutonomousAgent)
			return
		}
		if err := stagefiles.CheckCompletion(stageDir, ".", s); err == nil {
			o.Trigger(s.ID, bus.EvComplete, bus.GuardCtx{}, "recovered .done")
			o.maybeRunAfterHook(ctx, s.ID)
			return
		}
		if stagefiles.CheckPlanCompletion(stageDir) == nil && s.NeedsPlanning() {
			o.Trigger(s.ID, bus.EvPlanReady, bus.GuardCtx{}, "recovered plan.md")
			o.autoApproveIfConfigured(ctx, s)
			return
		}
		o.Trigger(s.ID, bus.EvStartPlanning, bus.GuardCtx{}, "restart after retry")
		o.concurrency.SpawnAgent(ctx, s, o.runPlanningAgent)
	case state.StatusRevising:
		switch o.detectInterruptedPhase(stageDir) {
		case phaseImplementation:
			o.concurrency.SpawnAgent(ctx, s, o.runImplementationWithFeedback)
		case phaseReview:
			o.concurrency.SpawnAgent(ctx, s, o.runReviewWithFeedback)
		case phaseAutonomous:
			o.concurrency.SpawnAgent(ctx, s, o.runAutonomousWithFeedback)
		default:
			o.concurrency.SpawnAgent(ctx, s, o.runPlanningWithFeedback)
		}
	case state.StatusRunning:
		if s.IsScript() {
			if err := stagefiles.CheckCompletion(stageDir, ".", s); err == nil {
				o.Trigger(s.ID, bus.EvComplete, bus.GuardCtx{}, "recovered .done")
				o.maybeRunAfterHook(ctx, s.ID)
				return
			}
			o.concurrency.SpawnAgent(ctx, s, o.withBeforeHook(o.runScriptStage))
			return
		}
		if isAutonomousStage(stageDir) || s.IsAuto() {
			if stagefiles.CheckAutonomousCompletion(stageDir) == nil {
				o.Trigger(s.ID, bus.EvComplete, bus.GuardCtx{}, "recovered execution_summary.md")
				o.maybeRunAfterHook(ctx, s.ID)
				return
			}
			o.concurrency.SpawnAgent(ctx, s, o.runAutonomousAgent)
			return
		}
		if err := stagefiles.CheckCompletion(stageDir, ".", s); err == nil {
			o.Trigger(s.ID, bus.EvComplete, bus.GuardCtx{}, "recovered .done")
			o.maybeRunAfterHook(ctx, s.ID)
			return
		}
		o.concurrency.SpawnAgent(ctx, s, o.runImplementationAgent)
	default:
		// Unreachable in practice: callers only ever pass a status they just
		// observed on a stage that needs resuming (Planning/Retrying/Revising/
		// Running). Kept explicit to satisfy the lint rule requiring switches
		// to have a default case.
	}
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
