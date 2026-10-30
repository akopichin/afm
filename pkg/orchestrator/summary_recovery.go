package orchestrator

import (
	"errors"
	"fmt"
	"log"
	"strconv"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/state"
)

// maxSummaryPostExit is how many times AFM continues an autonomous author whose process exited without a usable
// execution_summary.md, per correction cycle. The budget is durable (state.StageState.Summary) and separate from
// the transport and verify budgets.
const maxSummaryPostExit = 2

// Summary recovery notices (live + notices.jsonl, non-FSM). Each carries "reservation" — the seq of the
// transition that reserved the correction or reminder — so a restart does not announce the same event twice.
const (
	eventSummaryCorrectionStarted   bus.EventType = "summary_correction_started"   // a post-exit correction begins (1/2, 2/2)
	eventSummaryRecovered           bus.EventType = "summary_recovered"            // the file passes the probe after a correction
	eventSummaryCorrectionExhausted bus.EventType = "summary_correction_exhausted" // the corrections are spent, the stage fails
	eventAgentResultError           bus.EventType = "agent_result_error"           // the CLI reported an error, the summary stands
)

// summaryVerdict is what the retry loop does about a failed summary probe.
type summaryVerdict int

const (
	summaryUnhandled summaryVerdict = iota // not a summary probe failure: the generic completion handling decides
	summaryCorrect                         // a correction is reserved: the author runs again
	summarySettled                         // the turn is over: the stage failed or someone else took it over
)

func (o *Orchestrator) summaryCorrections(stageID string) *state.SummaryCorrections {
	if o.opts.Store == nil {
		return nil
	}
	return o.opts.Store.Snapshot().Stages[stageID].Summary
}

// summaryContinuation is the instruction of a turn that continues a correction reserved before a restart, Pause
// or park and not (or no longer) launched. Its reservation is already spent, so it is that correction, not an extra
// one; the file is probed again, since the author may have written it meanwhile.
func (o *Orchestrator) summaryContinuation(s flow.Stage, stageDir, phase string) string {
	spent := o.summaryCorrections(s.ID)
	if phase != phaseAutonomous || spent == nil || spent.PostExit == 0 || spent.Issued {
		return ""
	}
	failure, ok := stagefiles.MarkerFailureOf(stagefiles.CheckAutonomousCompletion(stageDir))
	if !ok || failure.Problem == stagefiles.MarkerUnreadable {
		return ""
	}
	return prompts.SummaryCorrection(failure.Path, string(failure.Problem), spent.PostExit, maxSummaryPostExit)
}

// summaryOutcomeUnknown reports a stage whose latest post-exit correction was launched and whose file is still not
// usable: the crash hid how that run ended, so it is not repeated.
func (o *Orchestrator) summaryOutcomeUnknown(stageID, stageDir, phase string) bool {
	spent := o.summaryCorrections(stageID)
	if phase != phaseAutonomous || spent == nil || !spent.Issued {
		return false
	}
	_, unusable := stagefiles.MarkerFailureOf(stagefiles.CheckAutonomousCompletion(stageDir))
	return unusable
}

// markSummaryCorrectionIssued durably records, before the author runs it, that the latest post-exit correction is
// launched; a transport retry of the same correction records nothing more. false — the turn lost the stage.
func (o *Orchestrator) markSummaryCorrectionIssued(stageID string, epoch uint64) bool {
	if spent := o.summaryCorrections(stageID); spent != nil && spent.Issued {
		return true
	}
	step := &state.SummaryStep{Source: state.SummarySourcePostExitIssued}
	return o.recordSummaryStep(stageID, epoch, step, "summary correction issued")
}

// recordSummaryStep durably records a summary step of the running stage, fenced by the turn's epoch: a takeover
// since the turn started leaves the stage alone. It is a bare store transition, not Trigger — Trigger would announce
// a running→running status change and wake the waiters of a stage that did not move.
func (o *Orchestrator) recordSummaryStep(stageID string, epoch uint64, step *state.SummaryStep, reason string) bool {
	guard := bus.GuardCtx{Phase: phaseAutonomous, ExpectEpoch: &epoch, SummaryStep: step}
	_, _, _, ok, err := o.fsm.Apply(stageID, bus.EvReserveSummaryHook, guard, reason)
	if err != nil {
		var storage *bus.StorageError
		if errors.As(err, &storage) {
			log.Printf("FATAL: storage failure recording %q of %s: %v", reason, stageID, err)
			o.setFatal(err)
		} else {
			log.Printf("CRITICAL: FSM Apply %s/%s: %v", stageID, bus.EvReserveSummaryHook, err)
		}
		return false
	}
	return ok
}

// decideSummaryFailure handles a probe that failed on execution_summary.md: a missing or empty file spends a durable
// post-exit correction (EvScheduleRetry records it before the author runs), an unreadable one fails with the exact
// file error, and spent corrections fail the stage. cliOutcome is the CLI's last error, shown as a cause.
func (o *Orchestrator) decideSummaryFailure(s flow.Stage, phase, stageDir string, epoch uint64, checkErr error, cliOutcome string, onUserInterrupted func()) (verdict summaryVerdict, instruction string) {
	failure, ok := stagefiles.MarkerFailureOf(checkErr)
	if !ok || phase != phaseAutonomous {
		return summaryUnhandled, ""
	}
	if failure.Problem == stagefiles.MarkerUnreadable {
		o.failTurn(s.ID, epoch, withCLIOutcome(checkErr.Error(), cliOutcome), onUserInterrupted)
		return summarySettled, ""
	}
	spent := 0
	if corrections := o.summaryCorrections(s.ID); corrections != nil {
		spent = corrections.PostExit
	}
	if spent >= maxSummaryPostExit {
		reason := fmt.Sprintf("%s (summary corrections used: %d of %d)", checkErr.Error(), spent, maxSummaryPostExit)
		if _, failed := o.failTurn(s.ID, epoch, withCLIOutcome(reason, cliOutcome), onUserInterrupted); failed {
			o.publishSummaryExhausted(s.ID, failure, spent, cliOutcome)
		}
		return summarySettled, ""
	}

	msg := fmt.Sprintf("execution_summary.md is %s: correction %d/%d", failure.Problem, spent+1, maxSummaryPostExit)
	step := &state.SummaryStep{Source: state.SummarySourcePostExit, Reason: string(failure.Problem)}
	_, seq, ok := o.triggerWithSeq(s.ID, bus.EvScheduleRetry, bus.GuardCtx{Phase: phase, ExpectEpoch: &epoch, SummaryStep: step}, msg)
	if !ok {
		o.handOffRevise(s.ID, epoch, onUserInterrupted)
		return summarySettled, ""
	}
	o.ui.Publish(bus.Event{Type: bus.EventRetryScheduled, StageID: s.ID, Data: msg, Seq: seq})
	o.Trigger(s.ID, bus.EvResumeAfterRetry, bus.GuardCtx{Phase: phase, ExpectEpoch: &epoch}, "")
	return summaryCorrect, prompts.SummaryCorrection(failure.Path, string(failure.Problem), spent+1, maxSummaryPostExit)
}

func withCLIOutcome(reason, cliOutcome string) string {
	if cliOutcome == "" {
		return reason
	}
	return reason + "; last CLI outcome: " + cliOutcome
}

// summaryProbePasses is the pure file probe: no model, verify or process.
func summaryProbePasses(stageDir string) bool {
	return stagefiles.CheckAutonomousCompletion(stageDir) == nil
}

// cliOutcomeOf names a terminal CLI result error for a notice or a fail reason: its subtype and API status, never
// the CLI's own text, which may echo a secret. "" — err is not a terminal result error.
func cliOutcomeOf(err error) string {
	var terminal *executor.TerminalResultError
	if !errors.As(err, &terminal) {
		return ""
	}
	outcome := terminal.Subtype
	if outcome == "" {
		outcome = "error result"
	}
	if terminal.APIErrorStatus != 0 {
		outcome += " (API status " + strconv.Itoa(terminal.APIErrorStatus) + ")"
	}
	return outcome
}

// finishedDespiteResultError reports a turn whose CLI ended with an error result although the turn itself wrote a
// complete summary. The summary stands: the usual verify, barrier and completion run, and the error is announced.
// A summary the turn did not write (an earlier one) is no proof the work was finished.
func (o *Orchestrator) finishedDespiteResultError(s flow.Stage, stageDir, phase string, markerBefore completionMarker, err error) bool {
	outcome := cliOutcomeOf(err)
	if outcome == "" || phase != phaseAutonomous || !o.readCompletionMarker(s, stageDir, phase).freshSince(markerBefore) {
		return false
	}
	o.publishNotice(bus.Event{Type: eventAgentResultError, StageID: s.ID, Data: map[string]string{
		"cli_outcome": outcome,
		"message":     "The agent CLI reported an error (" + outcome + ") but execution_summary.md is complete; continuing with the usual checks",
	}})
	return true
}

// noteSummaryRecovered announces, before the usual checks, that a correction got the file through the probe.
func (o *Orchestrator) noteSummaryRecovered(stageID, stageDir string) {
	spent := o.summaryCorrections(stageID)
	if spent == nil || spent.PostExit+spent.Hook == 0 || !summaryProbePasses(stageDir) {
		return
	}
	o.publishSummaryNotice(eventSummaryRecovered, stageID, spent.ReservedSeq, map[string]string{
		"message": "execution_summary.md is complete after a correction; running the usual checks",
	})
}

// noteSummaryCorrectionStarted announces the correction the author is about to run. The correction is named by its
// own reservation: a Stop hook reminder inside it moves ReservedSeq, and its transport retry must not announce twice.
func (o *Orchestrator) noteSummaryCorrectionStarted(stageID, stageDir string) {
	spent := o.summaryCorrections(stageID)
	failure, ok := stagefiles.MarkerFailureOf(stagefiles.CheckAutonomousCompletion(stageDir))
	if spent == nil || !ok {
		return
	}
	attempt := fmt.Sprintf("%d/%d", spent.PostExit, maxSummaryPostExit)
	o.publishSummaryNotice(eventSummaryCorrectionStarted, stageID, spent.PostExitSeq, map[string]string{
		"attempt": attempt,
		"reason":  string(failure.Problem),
		"path":    failure.Path,
		"message": fmt.Sprintf("execution_summary.md is %s: correction %s started", failure.Problem, attempt),
	})
}

func (o *Orchestrator) publishSummaryExhausted(stageID string, failure *stagefiles.MarkerFailure, spent int, cliOutcome string) {
	data := map[string]string{
		"attempt": fmt.Sprintf("%d/%d", spent, maxSummaryPostExit),
		"reason":  string(failure.Problem),
		"path":    failure.Path,
		"message": fmt.Sprintf("execution_summary.md is still %s after %d corrections", failure.Problem, spent),
	}
	if cliOutcome != "" {
		data["cli_outcome"] = cliOutcome
	}
	o.publishSummaryNotice(eventSummaryCorrectionExhausted, stageID, o.summaryCorrections(stageID).ReservedSeq, data)
}

// publishSummaryNotice publishes a notice once per reservation: notices.jsonl remembers what a restart forgets.
func (o *Orchestrator) publishSummaryNotice(typ bus.EventType, stageID string, reservedSeq uint64, data map[string]string) {
	reservation := strconv.FormatUint(reservedSeq, 10)
	if stagefiles.HasNotice(o.opts.RunDir, stageID, string(typ), "reservation", reservation) {
		return
	}
	data["reservation"] = reservation
	o.publishNotice(bus.Event{Type: typ, StageID: stageID, Data: data})
}
