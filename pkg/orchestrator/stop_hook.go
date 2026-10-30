package orchestrator

import (
	"fmt"
	"sync/atomic"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/state"
)

// maxSummaryHook is how many reminders the Stop hook gives the live session of the autonomous author per
// correction cycle; the post-exit continuations (maxSummaryPostExit) are a separate budget.
const maxSummaryHook = 1

// eventSummaryHookReminder is announced (live + notices.jsonl, non-FSM) when the Stop hook keeps a session going to
// write its missing execution_summary.md.
const eventSummaryHookReminder bus.EventType = "summary_hook_reminder"

// eventSummaryHookUnavailable is announced (live + notices.jsonl, non-FSM) once per stage and reason when the Stop
// hook cannot work for the author's CLI; AFM's recovery after the session is then the only guard.
const eventSummaryHookUnavailable bus.EventType = "summary_hook_unavailable"

// stopHookRuntime is how the CLI behind cmd takes a Stop hook: Claude-family commands (claude and its recipe
// wrappers, GLM among them) and the Codex adapter. Any other command — OpenAI, Cursor, an unknown one — has no
// hook protocol AFM knows, so it gets none.
func (o *Orchestrator) stopHookRuntime(cmd string) (executor.StopHookRuntime, bool) {
	cfg := o.opts.Config
	switch {
	case cfg.IsCodexCommand(cmd):
		return executor.StopHookCodex, true
	case cfg.BrowserSupportedFor(cmd): // claude-family: the same commands that run the Claude CLI
		return executor.StopHookClaude, true
	}
	return "", false
}

// stopHookFor is the Stop hook capability for the author process of stage s: only the main autonomous author of a
// CLI that takes the hook gets one, bound to the live turn it is taken from. The turn is the stage's current run
// loop (runRetryLoop), so a capability built outside one — or by a helper, verifier or planner — does not exist.
func (o *Orchestrator) stopHookFor(s flow.Stage, phase, cmd string) *executor.StopHookClient {
	runtime, supported := o.stopHookRuntime(cmd)
	if phase != phaseAutonomous || !supported || o.opts.Store == nil {
		return nil
	}
	live, ok := o.liveTurns.Load(s.ID)
	if !ok {
		return nil
	}
	turn := live.(*turnRecord)
	return &executor.StopHookClient{
		Runtime:  runtime,
		Launcher: browserLauncherPath(),
		OnUnavailable: func(reason executor.StopHookUnavailable) {
			o.noteSummaryHookUnavailable(s.ID, runtime, reason)
		},
		Mint: func() (func(executor.StopRequest) executor.StopDecision, func()) {
			inv := o.newStopInvocation(s, turn)
			return inv.decide, inv.revoke
		},
	}
}

// noteSummaryHookUnavailable announces why the hook does not work, once per stage and reason: notices.jsonl is the
// memory, so retries, repeated callbacks and a restart do not repeat it. The payload is a fixed vocabulary.
func (o *Orchestrator) noteSummaryHookUnavailable(stageID string, runtime executor.StopHookRuntime, reason executor.StopHookUnavailable) {
	if stagefiles.HasNotice(o.opts.RunDir, stageID, string(eventSummaryHookUnavailable), "reason", string(reason)) {
		return
	}
	o.publishNotice(bus.Event{Type: eventSummaryHookUnavailable, StageID: stageID, Data: map[string]string{
		"reason":  string(reason),
		"runtime": string(runtime),
		"message": summaryHookUnavailableMessage(runtime, reason),
	}})
}

func summaryHookUnavailableMessage(runtime executor.StopHookRuntime, reason executor.StopHookUnavailable) string {
	const fallback = " AFM's summary recovery after the session still applies."
	switch reason {
	case executor.StopHookSettingsConflict:
		return "AFM could not add its Stop hook: the agent command already passes --settings, and a second one would replace it." + fallback
	case executor.StopHookBridgeFailed:
		return "AFM could not open its Stop hook bridge for this session." + fallback
	}
	if runtime == executor.StopHookCodex {
		return "Codex ended without running AFM's Stop hook: hooks are disabled, managed hooks only are allowed, or this Codex version does not accept AFM's trust of the hook." + fallback
	}
	return "The Claude CLI ended without running AFM's Stop hook (for example --bare, disabled hooks or managed policy)." + fallback
}

// stopInvocation answers the Stop requests of one author process. It owns nothing durable: the decision reads the
// live turn and the journal, and the reservation goes through the Store like any other transition.
type stopInvocation struct {
	o       *Orchestrator
	stage   flow.Stage
	turn    *turnRecord
	revoked atomic.Bool

	// beforeReserve is a test seam: it runs between the policy's reads and the reservation.
	beforeReserve func()
}

func (o *Orchestrator) newStopInvocation(s flow.Stage, turn *turnRecord) *stopInvocation {
	return &stopInvocation{o: o, stage: s, turn: turn}
}

// revoke makes the capability inert once its process is gone; a request still in flight is allowed to stop.
func (inv *stopInvocation) revoke() { inv.revoked.Store(true) }

// decide is the Stop hook policy. It blocks the session once per cycle for a missing or empty summary and lets it
// stop in every other case: the usual completion path (verify, barriers, post-exit corrections) decides from there.
func (inv *stopInvocation) decide(req executor.StopRequest) executor.StopDecision {
	o, id := inv.o, inv.stage.ID
	if inv.revoked.Load() || req.HookActive || !inv.turnIsCurrent() {
		return executor.StopDecision{}
	}
	stageDir := o.stageDirFor(id)
	failure, ok := stagefiles.MarkerFailureOf(stagefiles.CheckAutonomousCompletion(stageDir))
	// An unreadable file is AFM's exact file error, not something the author can be reminded of.
	if !ok || failure.Problem == stagefiles.MarkerUnreadable {
		return executor.StopDecision{}
	}
	// A yield continues through the peer path and an open question through the dialog: neither needs the file yet.
	yielded := len(o.turnEpisode(meshStageParticipantID(id), inv.turn.currentWaitsBefore())) > 0
	if yielded || o.hasOpenQuestion(id, phaseAutonomous) {
		return executor.StopDecision{}
	}
	if spent := o.summaryCorrections(id); spent != nil && spent.Hook >= maxSummaryHook {
		return executor.StopDecision{}
	}
	if inv.beforeReserve != nil {
		inv.beforeReserve()
	}
	if !o.reserveSummaryHook(id, inv.turn.epoch, failure.Problem) {
		return executor.StopDecision{}
	}
	// The file appeared while the reminder was being reserved: nothing left to remind of.
	if summaryProbePasses(stageDir) {
		return executor.StopDecision{}
	}
	o.noteSummaryHookReminder(id, failure)
	return executor.StopDecision{Block: true, Reason: prompts.SummaryReminder(failure.Path, string(failure.Problem))}
}

// turnIsCurrent reports a turn that is still the stage's live one, still running and still owning its epoch:
// anything else (Pause, Continue, Revise, a user question, a park, the turn's end) makes the request stale.
func (inv *stopInvocation) turnIsCurrent() bool {
	o, id := inv.o, inv.stage.ID
	live, ok := o.liveTurns.Load(id)
	return ok && live == inv.turn && o.currentStatus(id) == state.StatusRunning && o.turnOwnsStage(id, inv.turn.epoch)
}

// reserveSummaryHook durably records the reminder before it is given.
func (o *Orchestrator) reserveSummaryHook(stageID string, epoch uint64, problem stagefiles.MarkerProblem) bool {
	step := &state.SummaryStep{Source: state.SummarySourceStopHook, Reason: string(problem)}
	return o.recordSummaryStep(stageID, epoch, step, "stop hook reminder")
}

// noteSummaryHookReminder announces the reminder that is being given: once per reservation, so neither a repeated
// request nor a restart announces it twice.
func (o *Orchestrator) noteSummaryHookReminder(stageID string, failure *stagefiles.MarkerFailure) {
	spent := o.summaryCorrections(stageID)
	if spent == nil {
		return
	}
	o.publishSummaryNotice(eventSummaryHookReminder, stageID, spent.ReservedSeq, map[string]string{
		"reason":  string(failure.Problem),
		"path":    failure.Path,
		"message": fmt.Sprintf("execution_summary.md is %s: the agent was reminded to write it before ending the session", failure.Problem),
	})
}
