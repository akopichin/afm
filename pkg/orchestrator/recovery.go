package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// startPlanningForPending starts or resumes stages based on their saved status.
// Terminal states (done, failed, awaiting_approval) are left untouched.
// Interrupted transient states (planning, running, revising) are restarted.
// Pending stages start planning for the first time.
func (o *Orchestrator) startPlanningForPending(ctx context.Context) {
	for _, s := range o.opts.Stages {
		if !s.NeedsPlanning() {
			current := o.opts.Store.Get(s.ID)

			switch current {
			case state.StatusDone, state.StatusFailed, state.StatusAwaitingApproval:
				continue
			case state.StatusRunning, state.StatusReady, state.StatusAwaitingUserInput:
				// Let these fall through to the normal resume logic below.
			case state.StatusRetrying:
				stageDir := filepath.Join(o.opts.RunDir, s.ID)
				if err := checkCompletion(stageDir, ".", s); err == nil {
					o.Trigger(s.ID, EvComplete, GuardCtx{}, "recovered .done")
					continue
				}
				o.Trigger(s.ID, EvReady, GuardCtx{}, "retry recovery")
			default:
				if !o.depsDone(s) {
					continue
				}

				stageDir := filepath.Join(o.opts.RunDir, s.ID)
				if err := os.MkdirAll(stageDir, 0755); err != nil {
					o.Trigger(s.ID, EvFail, GuardCtx{}, "mkdir failed")
					continue
				}
				dst := filepath.Join(stageDir, "plan.md")
				if s.Plan != "" {
					if err := copyFile(s.Plan, dst); err != nil {
						o.Trigger(s.ID, EvFail, GuardCtx{}, "copy plan failed")
						continue
					}
				} else if s.Interactive {
					if err := os.WriteFile(dst, []byte(s.Description), 0644); err != nil {
						o.Trigger(s.ID, EvFail, GuardCtx{}, "write plan failed")
						continue
					}
				}
				o.Trigger(s.ID, EvReady, GuardCtx{}, "")
				continue
			}
		}

		current := o.opts.Store.Get(s.ID)

		switch current {
		case state.StatusDone, state.StatusFailed, state.StatusAwaitingApproval, state.StatusReady:
			continue
		case state.StatusAwaitingUserInput:
			go func(st flow.Stage) {
				sem := o.semFor(st)
				sem.acquire()
				defer sem.release()
				o.resumeInteractiveAgent(ctx, st)
			}(s)
		case state.StatusRetrying:
			// Interrupted retry — check completion or restart
			stageDir := filepath.Join(o.opts.RunDir, s.ID)
			if err := checkCompletion(stageDir, ".", s); err == nil {
				o.Trigger(s.ID, EvComplete, GuardCtx{}, "recovered .done")
				continue
			}
			if checkPlanCompletion(stageDir) == nil && s.NeedsPlanning() {
				o.Trigger(s.ID, EvPlanReady, GuardCtx{}, "recovered plan.md")
				continue
			}
			// Restart planning from scratch
			o.Trigger(s.ID, EvStartPlanning, GuardCtx{}, "restart after retry")
			go func(stage flow.Stage) {
				sem := o.semFor(stage)
				sem.acquire()
				o.markAgentActive(stage.ID)
				defer func() {
					o.markAgentDone(stage.ID)
					sem.release()
				}()
				o.runPlanningAgent(ctx, stage)
			}(s)
		case state.StatusRevising:
			// Interrupted revision — restart with feedback
			go func() {
				sem := o.semFor(s)
				sem.acquire()
				defer sem.release()
				o.runPlanningWithFeedback(ctx, s)
			}()
		case state.StatusRunning:
			// Check if .done exists (agent completed but orchestrator missed the event)
			stageDir := filepath.Join(o.opts.RunDir, s.ID)
			if err := checkCompletion(stageDir, ".", s); err == nil {
				o.Trigger(s.ID, EvComplete, GuardCtx{}, "recovered .done")
				continue
			}
			// Interrupted implementation — restart with existing plan
			go func(st flow.Stage) {
				sem := o.semFor(st)
				sem.acquire()
				o.markAgentActive(st.ID)
				defer func() {
					o.markAgentDone(st.ID)
					sem.release()
				}()
				o.runImplementationAgent(ctx, st)
			}(s)
		default:
			// Pending, planning, or unknown — check if planning already completed
			if s.NeedsPlanning() {
				stageDir := filepath.Join(o.opts.RunDir, s.ID)
				if checkPlanCompletion(stageDir) == nil {
					o.Trigger(s.ID, EvPlanReady, GuardCtx{}, "recovered plan.md")
					continue
				}
			}
			// Pending stages wait for depends_on unless eager_planning is set.
			// Interrupted planning (status "planning") always resumes.
			if current == state.StatusPending && !s.EagerPlanning && !o.depsDone(s) {
				continue
			}
			o.Trigger(s.ID, EvStartPlanning, GuardCtx{}, "")
			go func(stage flow.Stage) {
				sem := o.semFor(stage)
				sem.acquire()
				o.markAgentActive(stage.ID)
				defer func() {
					o.markAgentDone(stage.ID)
					sem.release()
				}()
				o.runPlanningAgent(ctx, stage)
			}(s)
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
	o.markAgentActive(s.ID)
	defer o.markAgentDone(s.ID)

	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	phase := o.detectInterruptedPhase(stageDir)

	switch phase {
	case phasePlanning:
		o.Trigger(s.ID, EvUserAnswered, GuardCtx{Phase: phasePlanning}, "resume interactive")
		o.runPlanningAgent(ctx, s)
	case phaseReview:
		// Review runs after implementation completes; if we see review session
		// open, the stage was paused inside review. Fall through to implementation
		// agent which will re-trigger review at the end.
		fallthrough
	default:
		o.Trigger(s.ID, EvUserAnswered, GuardCtx{Phase: phaseImplementation}, "resume interactive")
		o.runImplementationAgent(ctx, s)
	}
}

func (o *Orchestrator) detectInterruptedPhase(stageDir string) string {
	var latestPhase string
	var latestMtime time.Time
	for _, phase := range []string{phasePlanning, phaseImplementation, phaseReview} {
		fi, err := os.Stat(sessionFile(stageDir, phase))
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
