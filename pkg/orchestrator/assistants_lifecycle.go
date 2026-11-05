package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/jsonstrict"
	"github.com/akopichin/afm/pkg/memory"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
	"github.com/akopichin/afm/schema"
)

// helperAgentRunner is the subsituteable seam for one helper-agent turn — mirrors verifyAgentRunner.
// Production is execHelperAgent; tests inject a fake (see assistants_test.go).
type helperAgentRunner func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error)

// helperExecutorConfig builds the executor.Config for one helper task's run — pulled out of execHelperAgent so
// a test can check it without spawning a process. Dir is the project root, exactly like the stage agent's and
// the verifier's (o.opts.RootDir, already worktree-aware; empty = afm's own CWD): a helper reviews code, so it
// must be able to read it where it lives. Its own scratch/ is handed over as AFM_SCRATCH_DIR, a place for
// notes. Whether the helper can also write to the project is up to its environment (a host codex sandbox is
// read-only, a container is not) — this config promises nothing about it. The task's executor binding sets its
// model arguments, reasoning and runtime context.
func (o *Orchestrator) helperExecutorConfig(taskID assistants.TaskID, cmd string) executor.Config {
	cfg := o.promptOnlyHelperConfig(taskID, cmd)
	// Delegation's alternatives have no native form, so only a helper that cannot delegate runs in native mode.
	if task, ok := o.opts.Store.Snapshot().Assistants.Tasks[taskID]; ok && !task.ProfileSnapshot.Capabilities.Delegate {
		cfg.NativeSchema = o.nativeSchemaFor(cmd, helperContract(task.Task))
	}
	if cfg.NativeSchema != nil && cfg.Job != nil {
		cfg.Job.Yielded = o.turnYieldCheck(o.taskMeshParticipant(taskID), true)
	}
	return cfg
}

// promptOnlyHelperConfig is helperExecutorConfig with the schema in the prompt alone.
func (o *Orchestrator) promptOnlyHelperConfig(taskID assistants.TaskID, cmd string) executor.Config {
	participant := o.taskMeshParticipant(taskID)
	return withExecutionBinding(executor.Config{
		Command:        cmd,
		IdleTimeout:    o.opts.Config.Executor.IdleTimeout,
		TruncateOutput: o.opts.Config.Executor.TruncateOutput,
		WrapperDir:     executor.WrapperDirFor(cmd, o.opts.WrapperDir, o.opts.GeneratedAgents),
		Dir:            o.opts.RootDir,
		ScratchDir:     o.taskScratchDir(taskID),
		Debug:          o.opts.Debug,
		RunDir:         o.opts.RunDir,
		Phase:          "helper",
		UsageHint:      o.usageHintFor(cmd),
		Provider:       o.opts.Config.UsageLimitProvider(cmd),
		OnLimitWarning: o.limitWarningSink("", taskID),
		OnAction:       o.helperActivitySink(taskID),
		Redact:         o.RedactSecrets,
		Secrets:        o.secretValues,
		MeshClient:     o.meshClientFor(participant),
		OnDelivered:    o.deliverRenderedInbox(participant),
		Job:            o.jobConfig(o.taskJobMeta(o.opts.Store.Snapshot(), taskID, cmd), participant),
	}, o.taskExecutionByID(taskID, cmd).Binding)
}

// helperContract is the answer contract of a helper's turn: the delegate form only for a profile that may delegate,
// and a verdict only a required task must give.
func helperContract(task assistants.Task) schema.Contract {
	required := task.Mode == assistants.ModeRequired
	switch {
	case task.ProfileSnapshot.Capabilities.Delegate && required:
		return schema.AssistantResultRequired
	case task.ProfileSnapshot.Capabilities.Delegate:
		return schema.AssistantResult
	case required:
		return schema.AssistantResultOnlyRequired
	default:
		return schema.AssistantResultOnly
	}
}

// taskContract is helperContract of a task in the projection; a task that is gone gets the most common one.
func (o *Orchestrator) taskContract(taskID assistants.TaskID) schema.Contract {
	if task, ok := o.opts.Store.Snapshot().Assistants.Tasks[taskID]; ok {
		return helperContract(task.Task)
	}
	return schema.AssistantResultOnly
}

// verdictRequired reports whether the contract is the answer of a required task, which only a verdict can close.
func verdictRequired(contract schema.Contract) bool {
	return contract == schema.AssistantResultRequired || contract == schema.AssistantResultOnlyRequired
}

// execHelperAgent is the production implementation of o.runHelperAgent: builds a fresh executor.Executor
// (mirrors runnerForVerify/execVerifyAgent) and delegates to RunHelperAgent. Cost accounting attribution
// (T3.8) is wired here via OnUsage/AppendTask — task identity (root stage, parent task) is read back from
// the current projection rather than widening the helperAgentRunner seam signature, which tests (T3.4)
// already depend on.
func (o *Orchestrator) execHelperAgent(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
	id := assistants.TaskID(taskID)
	run := func(cfg executor.Config) (executor.HelperOutcome, error) {
		cfg.OnUsage = o.recordTaskUsage(id, assistants.ProfileID(profileID))
		return executor.New(cfg).RunHelperAgent(ctx, taskID, profileID, prompt, logFile, resultFile)
	}
	yielded := o.turnYieldCheck(o.taskMeshParticipant(id), true)
	outcome, err := run(o.helperExecutorConfig(id, cmd))
	if err != nil || ctx.Err() != nil {
		return outcome, err
	}
	fallback := helperNativeRerun(outcome, yielded, o.taskContract(id))
	if fallback == "" {
		return outcome, nil
	}
	// One prompt-only rerun of the same turn; the task's log collects both runs like all its turns.
	if keepErr := keepNativeEvidence("", resultFile); keepErr != nil {
		return outcome, keepErr
	}
	cfg := o.promptOnlyHelperConfig(id, cmd)
	if hold := o.admitAgent(cfg.Provider, "", id); hold != nil {
		return executor.HelperOutcome{ProcessErr: hold}, hold
	}
	enforced := outcome.NativeEnforced
	outcome, err = run(cfg)
	// The provenance records what the native run did, as for verify.
	outcome.NativeFallback, outcome.NativeEnforced = executor.NativeSchemaFallback(fallback), enforced
	return outcome, err
}

// helperNativeRerun is nativeRerunReason for a helper turn. A turn that yielded gave no answer, so a payload the CLI
// forced into it decides nothing. A forced pass that repeats the model's own final answer is not made up, and a
// forced answer without the verdict a required task needs does not decode.
func helperNativeRerun(outcome executor.HelperOutcome, yielded func(string) bool, contract schema.Contract) string {
	failure := runFailure(outcome.Interrupted, outcome.TimedOut, outcome.ProcessOK, outcome.ProcessErr)
	forcedMayPass := false
	if failure == nil && outcome.NativeEnforced && !yielded(outcome.RawText) {
		env, decodeErr := assistants.DecodeResultFor(nativeAnswer(contract, []byte(outcome.RawText)), verdictRequired(contract))
		forcedMayPass = decodeErr != nil || env.Result == nil ||
			(env.Result.Verdict.ClosesGate() && !givesVerdict(outcome.TextBeforeEnforcement, env.Result.Verdict))
	}
	return nativeRerunReason(outcome.NativeStarted, failure, outcome.NativeEnforced, forcedMayPass)
}

// givesVerdict reports whether text is a result with verdict, accepted as a prompt-only answer would be.
func givesVerdict(text string, verdict assistants.Verdict) bool {
	env, _, _, err := assistants.RecoverResult([]byte(text))
	return err == nil && env.Kind == assistants.KindResult && env.Result != nil && env.Result.Verdict == verdict
}

// recordTaskUsage returns the OnUsage callback for one helper task invocation — nil (accounting disabled)
// when o.opts.Accounting is nil, exactly like recordUsage does for stage runners.
func (o *Orchestrator) recordTaskUsage(taskID assistants.TaskID, profileID assistants.ProfileID) func(accounting.Observation) {
	if o.opts.Accounting == nil {
		return nil
	}
	return func(obs accounting.Observation) {
		meta := accounting.TaskMeta{
			TaskID:      string(taskID),
			RootStageID: o.taskRootStage(taskID),
			ProfileID:   string(profileID),
			ExecutionID: randomOpSuffix(),
		}
		rs := o.opts.Store.Snapshot()
		if t, ok := rs.Assistants.Tasks[taskID]; ok && t.ParentTaskID != nil {
			meta.ParentTaskID = string(*t.ParentTaskID)
		}
		if err := o.opts.Accounting.AppendTask(obs, meta, "check", ""); err != nil {
			log.Printf("WARN: assistants: append task usage (task=%s): %v", taskID, err)
		}
	}
}

// persistTaskResult makes the latest result visible before completion and returns a rollback for rejection.
// Even a failed atomic write may have renamed the file before its directory sync failed.
func (o *Orchestrator) persistTaskResult(dir string, raw []byte) (func() error, error) {
	path := filepath.Join(dir, "result.json")
	previous, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return nil, readErr
	}
	restore := func() error {
		if readErr == nil {
			return memory.AtomicWrite(path, previous)
		}
		if err := os.Remove(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		parent, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer parent.Close()
		return parent.Sync()
	}
	return restore, memory.AtomicWrite(path, raw)
}

// taskCacheView is task.json's shape — a CACHE of a few identity fields for quick inspection, never the
// source of truth (events.jsonl/the state projection is — Инвариант №1). Best-effort: a write failure here
// must never fail the task itself.
type taskCacheView struct {
	TaskID      assistants.TaskID    `json:"task_id"`
	RootStageID string               `json:"root_stage_id"`
	ProfileID   assistants.ProfileID `json:"profile_id"`
	Mode        assistants.Mode      `json:"mode"`
	Trigger     assistants.Trigger   `json:"trigger"`
}

// persistTaskCache best-effort writes task.json — never fails the caller if it can't.
func (o *Orchestrator) persistTaskCache(dir string, task assistants.Task) {
	raw, err := json.MarshalIndent(taskCacheView{
		TaskID: task.ID, RootStageID: task.RootStageID, ProfileID: task.ProfileID,
		Mode: task.Mode, Trigger: task.Trigger,
	}, "", "  ")
	if err != nil {
		return
	}
	_ = memory.AtomicWrite(filepath.Join(dir, "task.json"), raw)
}

// taskCancelEntry is one attempt's own identity in o.taskCancels — see that field's doc comment (orchestrator.go)
// for why a pointer (compared by identity via CompareAndDelete) is required instead of a bare context.CancelFunc.
type taskCancelEntry struct {
	cancel context.CancelFunc
}

// runHelperTask is the body of one task's turn: build the prompt, fingerprint the tree before/after (reusing
// the EXISTING o.fingerprintFn/o.projectDir seam — never a second implementation), run the helper agent,
// and classify its outcome into a terminal task status. Never calls Trigger/completeStage — a helper task's
// outcome never changes its root stage's FSM status (Этап 3's acceptance criterion).
func (o *Orchestrator) runHelperTask(ctx context.Context, taskID assistants.TaskID, task assistants.Task) {
	// A CancelTask can commit "cancelled" before this goroutine ever gets scheduled — the durable
	// queued->running status write (maybeScheduleTask) and this goroutine's own first instruction are not
	// synchronized with each other. Bail out before touching the filesystem AT ALL in that case: every write
	// below (workspace dirs, task.json, context.md, agent.log, result files) happens under the task's own
	// directory, and a write that starts only because this goroutine was scheduled late would otherwise race
	// a caller that reasonably treats "task already terminal" as "nothing more will touch its directory"
	// (e.g. a test's TempDir cleanup, or — in production — a later CancelTask/cleanup of the same task dir).
	if o.taskTerminal(taskID) {
		return
	}

	runCtx, cancel := context.WithCancel(ctx)
	// entry is THIS attempt's own identity in the map — CompareAndDelete below only ever removes it if it is
	// still the current value, never whatever a newer, already-rescheduled attempt stored in the meantime
	// (see taskCancels's doc comment on orchestrator.go for the race this closes).
	entry := &taskCancelEntry{cancel: cancel}
	o.taskCancels.Store(taskID, entry)
	defer func() {
		o.taskCancels.CompareAndDelete(taskID, entry)
		o.taskPausePending.Delete(taskID) // in case this attempt finished via a path that never consumed it
		cancel()
		// Этап 5: settle this watcher's binding regardless of which exit path was taken (success→watching,
		// success→completed, failed, cancelled, or requeued) — a no-op for a plain one_shot task, which was
		// never bound in the first place (see bindWatcher's isWatcherTrigger guard).
		o.settleWatcherBinding(taskID)
	}()

	turn, ok := o.prepareHelperTurn(ctx, taskID, task)
	if !ok {
		return
	}

	end := helperTurnEnd{waiter: o.taskMeshParticipant(taskID)}
	end.waitsBefore = o.meshWaitStatusesOf(end.waiter)
	beforeFP, beforeOK := o.fingerprintFn(o.projectDir())
	if !o.startHelperTurn(ctx, taskID, task, &turn) {
		return
	}
	end.outcome, end.err = o.runHelperAgent(runCtx, string(taskID), string(task.ProfileID), turn.exec.Command, turn.ctx.Prompt, turn.logFile, turn.resultFile)
	// Marking delivered here also covers a runner that never calls OnDelivered. Only a turn that ran to its end
	// consumed its input (committed with its outcome); a cut-short turn is retried with the same messages.
	if end.err == nil && end.outcome.ProcessOK {
		if turn.ctx.markInboxDelivered != nil {
			turn.ctx.markInboxDelivered()
		}
		end.consumed = turn.number
	}
	if afterFP, afterOK := o.fingerprintFn(o.projectDir()); beforeOK && afterOK && beforeFP != afterFP {
		// Observation only — never changes the task's verdict (mirrors verify_tree_modified). Not an accusation
		// either: the tree may have been changed by anybody while the helper ran, so the notice only says when.
		agent := task.Title
		if agent == "" {
			agent = string(task.ProfileID)
		}
		agent = o.RedactSecrets(agent) // a title journaled before titles were redacted
		o.publishNotice(bus.Event{Type: bus.EventHelperTreeModified, StageID: task.RootStageID, Data: map[string]any{
			"task_id": string(taskID), "agent": agent,
		}})
	}

	// CancelTask commits "cancelled" right after cancelling runCtx. The projected status, not runCtx.Err(), tells
	// that apart from a run shutdown, which has no other writer and must still commit its own terminal status.
	end.finalizedByOther = o.taskTerminal(taskID)

	// Only final classification/commit outlive task cancellation; formatting belongs to this turn.
	if o.classifyHelperFailure(ctx, taskID, task, turn, end) {
		return
	}
	turn.runCtx = runCtx
	end.recovery, end.err = o.recoverHelperOutput(runCtx, taskID, task, turn, end.outcome)
	if end.err == nil && end.recovery.Provenance.Attempts == 0 {
		turn.runCtx = nil // A locally complete answer retains the existing shutdown/hold completion semantics.
	}
	if !o.helperOutputCurrent(taskID, task, turn) {
		o.interruptHelperOutput(ctx, taskID, task, turn)
		return
	}
	var runErr *outputRunError
	var limit *executor.UsageLimitError
	if errors.As(end.err, &runErr) && !errors.As(runErr.err, &limit) {
		// The helper did answer; only its repair failed, so it is the answer's protocol error, not a crash.
		end.err = fmt.Errorf("%w (%v)", runErr.rejected, runErr)
	} else if runErr != nil {
		if err := persistOutputRecovery(turn.dir, fmt.Sprintf("turn-%d-output", turn.number), end.recovery); err != nil {
			if o.helperOutputCommitHook != nil {
				o.helperOutputCommitHook()
			}
			finishErr := o.failHelperTurn(ctx, taskID, task, turn, end.consumed, o.helperTurnFailure("assistants: persist recovery", task, turn.exec, err.Error()), nil, o.helperTurnGuard(taskID, task, turn))
			if finishErr != nil && !o.helperOutputCurrent(taskID, task, turn) {
				o.interruptHelperOutput(ctx, taskID, task, turn)
			}
			return
		}
		end.outcome = executor.HelperOutcome{ProcessErr: runErr.err}
		end.err = runErr.err
		end.consumed = 0
		o.classifyHelperFailure(ctx, taskID, task, turn, end)
		return
	}
	o.commitHelperResult(ctx, taskID, task, turn, end)
}

// helperTurn is what a helper turn needs once its preparation succeeded.
type helperTurn struct {
	runCtx     context.Context
	exec       assistants.Execution // the executor of the turn and of its format repair
	dir        string
	ctx        TaskContext
	number     uint64
	holdEpoch  uint64
	logFile    string
	resultFile string
	request    agentcomm.MessageID // the on_message request the turn works on; "" for any other task
}

// resultPayload is the task_result payload of the turn's result: an on_message turn binds it to its request.
func (t helperTurn) resultPayload(result assistants.Result) state.TaskResultPayload {
	if t.request == "" {
		return state.TaskResultPayload{Result: result}
	}
	return state.TaskResultPayload{Result: result, RequestID: string(t.request), Turn: t.number}
}

// helperTurnEnd is what the entry method observed around the agent call; classification and commit read it.
type helperTurnEnd struct {
	recovery         outputRecovery
	outcome          executor.HelperOutcome
	err              error
	consumed         uint64 // the turn number when the turn ran to its end, else 0
	finalizedByOther bool
	waiter           agentcomm.ParticipantID
	waitsBefore      map[agentcomm.WaitID]agentcomm.WaitStatus
}

// prepareHelperTurn creates the task's workspace and context for its next turn, which startHelperTurn then starts. It
// reports false when the turn must not run: the task already failed here (finishTaskFrom) or rests again.
func (o *Orchestrator) prepareHelperTurn(ctx context.Context, taskID assistants.TaskID, task assistants.Task) (helperTurn, bool) {
	exec := o.taskExecution(o.opts.Store.Snapshot(), task)
	fail := func(what, detail string) {
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, o.helperTurnFailure(what, task, exec, detail))
	}
	// An on_message turn works on one request; with none due it does not start, and the helper rests again.
	var request agentcomm.MessageID
	if current, ok := o.opts.Store.Snapshot().Assistants.Tasks[taskID]; ok {
		request = onMessageTurnRequest(current)
	}
	if state.IsOnMessageHelper(task) && request == "" {
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusWatching, errNoRequestDue.Error())
		return helperTurn{}, false
	}
	dir := o.taskDir(taskID)
	if err := ensureTaskWorkspace(dir, o.taskScratchDir(taskID)); err != nil {
		fail("assistants: create task workspace", err.Error())
		return helperTurn{}, false
	}
	o.persistTaskCache(dir, task)

	// A scope=run task (Agent Mesh T6.1/T6.2) has no RootStageID at all — a standing role is not rooted at any
	// stage, so there is no stage to resolve or to fail on. buildTaskContext already degrades gracefully for a
	// zero-value flow.Stage (empty stage.ID short-circuits CollectArtifacts/stageGeneration/plan.md reads to
	// nothing, see its own callees) — this is the one place that previously assumed EVERY task has a real root
	// stage and must be taught the ScopeRun exception, otherwise AttachAndSend's own "always starts queued"
	// scope=run task (agentcomm_attach.go) would fail its very first turn before doing any work at all.
	var stage flow.Stage
	if task.RootStageID != "" {
		st := o.reg().Stage(task.RootStageID)
		if st == nil {
			fail("assistants: root stage no longer exists", "")
			return helperTurn{}, false
		}
		stage = *st
	}
	tc, cerr := o.buildTaskContext(task, stage)
	if cerr != nil {
		fail("assistants: build context", cerr.Error())
		return helperTurn{}, false
	}
	if werr := memory.AtomicWrite(filepath.Join(dir, "context.md"), []byte(tc.Prompt)); werr != nil {
		fail("assistants: persist context", werr.Error())
		return helperTurn{}, false
	}
	return helperTurn{
		exec: exec,
		dir:  dir, ctx: tc,
		logFile: filepath.Join(dir, "agent.log"), resultFile: filepath.Join(dir, rawResultFileName), request: request,
	}, true
}

// startHelperTurn records the start of turn right before its process, only the stale answer is cleared in between. An
// on_message helper whose stage paused or moved on since its command slot was granted does not start: its task goes
// back to the queue with the request, which the stage's Continue starts.
func (o *Orchestrator) startHelperTurn(ctx context.Context, taskID assistants.TaskID, task assistants.Task, turn *helperTurn) bool {
	number, holdEpoch, err := o.startTurn(taskID, turn.ctx.inboxIDs, turn.request)
	if errors.Is(err, errOnMessageGated) {
		o.requeueBeforeStart(ctx, taskID, onMessageStagePausedReason)
	}
	if err != nil {
		return false
	}
	turn.number, turn.holdEpoch = number, holdEpoch
	// The path is per-task, but a watch/continuation task re-runs under the SAME id (fireWatcherTask ->
	// maybeScheduleTask -> runHelperTask), and the executor only overwrites resultFile when the turn produced
	// nonempty text (RunHelperAgent, pkg/executor/hardening.go). Without clearing it first, a textless turn
	// would silently serve an EARLIER turn's answer as if it were this one's.
	if rerr := os.Remove(turn.resultFile); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		// The turn already started, so a Pause in this window supersedes it: requeue instead of failing.
		if o.helperOutputCommitHook != nil {
			o.helperOutputCommitHook()
		}
		err := o.finishTaskTurnReplyingGuarded(taskID, 0, assistants.StatusRunning, assistants.StatusFailed,
			o.helperTurnFailure("assistants: clear stale raw answer", task, turn.exec, rerr.Error()), nil, o.helperTurnGuard(taskID, task, *turn), nil)
		if err != nil && !o.helperOutputCurrent(taskID, task, *turn) {
			o.interruptHelperOutput(ctx, taskID, task, *turn)
		}
		return false
	}
	return true
}

// classifyHelperFailure ends, parks or hands over a turn without an acceptable answer and reports whether it did.
// The order is the precedence; false means the turn has a plain answer for commitHelperResult.
func (o *Orchestrator) classifyHelperFailure(ctx context.Context, taskID assistants.TaskID, task assistants.Task, turn helperTurn, end helperTurnEnd) bool {
	outcome, err, consumed := end.outcome, end.err, end.consumed
	guard := o.helperTurnGuard(taskID, task, turn)
	beforeCommit := func() {
		if o.helperOutputCommitHook != nil {
			o.helperOutputCommitHook()
		}
	}
	interruptIfSuperseded := func(err error) {
		if err != nil && !o.helperOutputCurrent(taskID, task, turn) {
			o.interruptHelperOutput(ctx, taskID, task, turn)
		}
	}
	fail := func(what, detail string, cause error) {
		beforeCommit()
		reason := o.helperTurnFailure(what, task, turn.exec, detail)
		interruptIfSuperseded(o.failHelperTurn(ctx, taskID, task, turn, consumed, reason, cause, guard))
	}
	failProcess := func(err error) {
		what, detail := processFailure(err)
		fail(what, detail, err)
	}

	// The run itself is shutting down (Ctrl+C): an invocation it cut short did not fail. The task stays
	// running, exactly like an interrupted stage, and recoverAssistants re-queues it when the run is resumed.
	// A turn that still finished with a result is recorded as usual below.
	if ctx.Err() != nil && (err != nil || !outcome.ProcessOK) {
		log.Printf("assistants: task %s interrupted by run shutdown — it is retried when the run resumes", taskID)
		return true
	}

	// A provider usage limit is no failure: the task waits for the reset in queued (parkTaskForQuota) instead of
	// ending, so its waits and mesh identity stay. Checked before the generic error: RunHelperAgent returns the
	// limit as the error and as outcome.ProcessErr. The turn did not run to its end, so its input stays.
	var limit *executor.UsageLimitError
	if errors.As(err, &limit) || errors.As(outcome.ProcessErr, &limit) {
		if !end.finalizedByOther {
			beforeCommit()
			interruptIfSuperseded(o.parkTaskForQuota(ctx, taskID, limit, guard))
		}
		return true
	}
	if err != nil {
		if end.finalizedByOther {
			return true
		}
		failProcess(err)
		return true
	}
	switch {
	case outcome.Interrupted || end.finalizedByOther:
		// PauseTask/CancelTask already recorded (or will record) their own status transition — a stopped
		// subprocess reaching here must not clobber it with a second, contradictory status.
		return true
	case outcome.TimedOut:
		fail("helper agent timed out", "", nil)
		return true
	case !outcome.ProcessOK:
		// Pause/clarify cancels runCtx just like a crash; its marker survives a concurrent Continue.
		// Requeue consumes the markers and retries scheduling to close Continue's lost-wakeup window.
		_, pausedBeforeClassification := o.taskPausePending.Load(taskID)
		_, clarifying := o.taskClarifyPending.Load(taskID)
		if pausedBeforeClassification || clarifying || o.taskHeld(taskID) {
			o.requeuePausedTurn(ctx, taskID)
			return true
		}
		failProcess(outcome.ProcessErr)
		return true
	}

	// Agent Mesh Phase 5, T5.4: the subprocess registered a mesh Wait on a colleague (another helper, a
	// stage, or the human) and exited cooperatively — mirrors retry.go's stage-level T5.2 branch. This is NOT a
	// false completion and NOT a protocol error: a yielding agent prints no final answer, so the yield is
	// classified BEFORE the executor's "no final answer" protocol error is looked at. This task's own
	// concurrency lease is released simply by runHelperTask returning next.
	if o.turnYielded(end.waiter, end.waitsBefore, outcome.RawText, outcome.NativeStarted) {
		beforeCommit()
		interruptIfSuperseded(o.parkYieldedTurn(ctx, taskID, consumed, end.waiter, end.waitsBefore, guard))
		return true
	}
	if outcome.ProtocolErr != nil {
		beforeCommit()
		interruptIfSuperseded(o.failAnsweredTurn(ctx, taskID, task, turn, consumed, o.helperProtocolReason(task, turn.exec, outcome.ProtocolErr.Error()), unacceptedAnswer{}, guard))
		return true
	}
	return false
}

func (o *Orchestrator) recoverHelperOutput(ctx context.Context, taskID assistants.TaskID, task assistants.Task, turn helperTurn, outcome executor.HelperOutcome) (outputRecovery, error) {
	contract := helperContract(task)
	origin := nativeOrigin{started: outcome.NativeStarted, fallback: string(outcome.NativeFallback), enforced: outcome.NativeEnforced}
	raw := []byte(outcome.RawText)
	validator := nativeValidator(contract, origin, func(raw []byte) ([]byte, jsonstrict.RepairKind, error) {
		env, candidate, kind, err := assistants.RecoverResult(raw)
		if err != nil {
			return nil, "", err
		}
		if verdictRequired(contract) {
			if _, err := assistants.DecodeResultFor(candidate, true); err != nil {
				return nil, "", err
			}
		}
		if env.Kind == assistants.KindDelegate && !task.ProfileSnapshot.Capabilities.Delegate {
			return nil, "", jsonstrict.NewIssue(jsonstrict.DomainIssue, errors.New("profile cannot delegate"))
		}
		if env.Result != nil && env.Result.Verdict == assistants.VerdictPass && o.requiredChildOpen(taskID) {
			return nil, "", jsonstrict.NewIssue(jsonstrict.DomainIssue, errors.New("verdict pass with an unresolved required child"))
		}
		return candidate, kind, nil
	})
	cmd := turn.exec.Command
	cfg := withExecutionBinding(o.outputFormatterConfig(cmd, task.RootStageID), turn.exec.Binding)
	cfg.OnUsage = o.recordTaskUsage(taskID, task.ProfileID)
	cfg.Job = o.repairJob(o.taskJobMeta(o.opts.Store.Snapshot(), taskID, cmd), cmd)
	cfg.OnLimitWarning = o.limitWarningSink(task.RootStageID, taskID)
	prefix := fmt.Sprintf("turn-%d-output", turn.number)
	if err := memory.AtomicWrite(filepath.Join(turn.dir, prefix+".raw.json"), raw); err != nil {
		return outputRecovery{}, fmt.Errorf("persist immutable helper output: %w", err)
	}
	owner := fmt.Sprintf("task/%s/generation/%d/turn/%d", taskID, task.TaskGeneration, turn.number)
	recovered, err := recoverOutput(ctx, contract, owner, raw, validator, outputFormatAttempts, o.outputFormatter(cfg, turn.dir, prefix, taskID))
	recovered.noteNative(origin)
	return recovered, err
}

func (o *Orchestrator) helperOutputCurrent(taskID assistants.TaskID, task assistants.Task, turn helperTurn) bool {
	if turn.runCtx != nil && turn.runCtx.Err() != nil {
		return false
	}
	rs := o.opts.Store.Snapshot()
	return helperOutputOwned(&rs, taskID, task, turn)
}

var errHelperOutputSuperseded = errors.New("helper output no longer owns the turn")

func helperOutputOwned(rs *state.RunState, taskID assistants.TaskID, task assistants.Task, turn helperTurn) bool {
	current := rs.Assistants.Tasks[taskID]
	if current == nil {
		return false
	}
	held := len(current.Holds) > 0
	// Ancestor holds stop new work; a produced local result can finish unless its own task was paused.
	if turn.runCtx != nil {
		held, _ = rs.Assistants.EffectivelyHeld(taskID)
	}
	return current.Status == assistants.StatusRunning && current.Turn == turn.number && current.HoldEpoch == turn.holdEpoch &&
		current.TaskGeneration == task.TaskGeneration && current.RootGeneration == task.RootGeneration &&
		rs.Assistants.RootGenerations[task.RootStageID] == task.RootGeneration && !held
}

func (o *Orchestrator) helperTurnGuard(taskID assistants.TaskID, task assistants.Task, turn helperTurn) func(*state.RunState) error {
	return func(rs *state.RunState) error {
		if !helperOutputOwned(rs, taskID, task, turn) || (turn.runCtx != nil && turn.runCtx.Err() != nil) {
			return errHelperOutputSuperseded
		}
		return nil
	}
}

func (o *Orchestrator) interruptHelperOutput(ctx context.Context, taskID assistants.TaskID, task assistants.Task, turn helperTurn) {
	rs := o.opts.Store.Snapshot()
	current := rs.Assistants.Tasks[taskID]
	if current == nil || current.Turn != turn.number || current.TaskGeneration != task.TaskGeneration || current.RootGeneration != task.RootGeneration || rs.Assistants.RootGenerations[task.RootStageID] != task.RootGeneration {
		return
	}
	if current.Status.Terminal() || ctx.Err() != nil {
		return
	}
	// A rejected ownership guard proves takeover even before PauseTask signals the live process.
	o.requeuePausedTurn(ctx, taskID)
}

func (o *Orchestrator) requeuePausedTurn(ctx context.Context, taskID assistants.TaskID) {
	o.taskPausePending.LoadAndDelete(taskID)
	o.taskClarifyPending.LoadAndDelete(taskID)
	o.finishTaskTurn(taskID, 0, assistants.StatusRunning, assistants.StatusQueued, "re-queued: paused while running")
	// Even while held: it returns on the hold, but first ends the pass of a stage that failed while the turn ran.
	o.maybeScheduleTask(ctx, taskID)
	if o.taskRequeuePreDeferHook != nil {
		o.taskRequeuePreDeferHook(taskID)
	}
}

// commitHelperResult runs after recovery under the original turn's ownership.
func (o *Orchestrator) commitHelperResult(ctx context.Context, taskID assistants.TaskID, task assistants.Task, turn helperTurn, end helperTurnEnd) {
	consumed := end.consumed
	guard := o.helperTurnGuard(taskID, task, turn)
	interruptIfSuperseded := func(err error) {
		if err != nil && !o.helperOutputCurrent(taskID, task, turn) {
			o.interruptHelperOutput(ctx, taskID, task, turn)
		}
	}
	fail := func(what, detail string, answered bool) {
		if o.helperOutputCommitHook != nil {
			o.helperOutputCommitHook()
		}
		reason := o.helperTurnFailure(what, task, turn.exec, detail)
		if answered {
			answer := unacceptedAnswer{text: end.outcome.RawText, path: filepath.Join(turn.dir, fmt.Sprintf("turn-%d-output.raw.json", turn.number))}
			interruptIfSuperseded(o.failAnsweredTurn(ctx, taskID, task, turn, consumed, reason, answer, guard))
		} else {
			interruptIfSuperseded(o.failHelperTurn(ctx, taskID, task, turn, consumed, reason, nil, guard))
		}
	}
	if !o.helperOutputCurrent(taskID, task, turn) {
		o.interruptHelperOutput(ctx, taskID, task, turn)
		return
	}
	if err := persistOutputRecovery(turn.dir, fmt.Sprintf("turn-%d-output", turn.number), end.recovery); err != nil {
		fail("assistants: persist recovery", err.Error(), false)
		return
	}
	if end.err != nil {
		fail(contractNotMet, end.err.Error(), true)
		return
	}
	env, decErr := assistants.DecodeResultFor(end.recovery.Candidate, verdictRequired(helperContract(task)))
	if decErr != nil {
		fail(contractNotMet, decErr.Error(), true)
		return
	}
	repaired := end.recovery.Provenance.Source == sourceRepair || end.recovery.Provenance.Source == sourceFormatAgent
	if repaired {
		o.publishNotice(bus.Event{Type: bus.EventHelperResultRepaired, StageID: task.RootStageID, Data: map[string]any{
			"task_id": string(taskID), "source": end.recovery.Provenance.Source,
			"contract": end.recovery.Provenance.Contract, "owner": end.recovery.Provenance.Owner, "validation": validationAccepted,
		}})
	}
	if env.Kind == assistants.KindDelegate {
		// T4a.3: admit the requested children and move this task to waiting_children — never treated as a
		// completed check (assistants.Envelope.ClosesRequiredGate is false for kind:"delegate" by
		// construction, T4a.1).
		if o.helperOutputCommitHook != nil {
			o.helperOutputCommitHook()
		}
		if err := o.handleDelegate(ctx, taskID, consumed, turn.ctx.Version, *env.Delegate, guard); errors.Is(err, errHelperOutputSuperseded) {
			o.interruptHelperOutput(ctx, taskID, task, turn)
		}
		return
	}
	if env.Kind != assistants.KindResult || env.Result == nil {
		fail(contractNotMet, "unsupported kind", true)
		return
	}
	if env.Result.Verdict == assistants.VerdictPass && o.requiredChildOpen(taskID) {
		// dev-plan.md's TestResult_ConsistencyWithRequiredChildren: a required child that has not (yet, or
		// ever will) reached verdict:"pass" keeps this gate closed — the model's own claimed "pass" text does
		// not override it.
		fail(contractNotMet, "verdict:\"pass\" with an unresolved required child", true)
		return
	}

	// Stamp the SERVER's own ContextVersion rather than trusting whatever (if anything) the model echoed
	// back — a result must always reference the actual context it was computed against, not a model's
	// possibly-stale or fabricated claim about it.
	result := o.redactResult(*env.Result)
	result.ContextVersion = turn.ctx.Version
	result.Repaired = repaired
	if !o.helperOutputCurrent(taskID, task, turn) {
		o.interruptHelperOutput(ctx, taskID, task, turn)
		return
	}
	resultBytes, marshalErr := json.MarshalIndent(result, "", "  ")
	if marshalErr != nil {
		fail("assistants: marshal result", marshalErr.Error(), false)
		return
	}
	if werr := memory.AtomicWrite(filepath.Join(turn.dir, fmt.Sprintf("turn-%d-result.json", turn.number)), resultBytes); werr != nil {
		fail("assistants: persist result", werr.Error(), false)
		return
	}
	// The decoded result is attached as a SECOND effect of the SAME finish operation — never a separate
	// ApplyRunOperation call — so the journal never has a window where status says "completed" but the
	// result event does not exist yet (see finishTaskFrom's extra doc comment).
	resultEff, eerr := taskResultEffect(taskID, turn.resultPayload(result))
	if eerr != nil {
		fail("assistants: marshal result event", eerr.Error(), false)
		return
	}

	if o.helperOutputCommitHook != nil {
		o.helperOutputCommitHook()
	}
	restore, writeErr := o.persistTaskResult(turn.dir, resultBytes)
	restoreLatest := func() bool {
		if restore != nil {
			if err := restore(); err != nil {
				o.setFatal(fmt.Errorf("assistants: task %s: restore latest result: %w", taskID, err))
				return false
			}
		}
		return true
	}
	if writeErr != nil {
		if restoreLatest() {
			fail("assistants: persist latest result", writeErr.Error(), false)
		}
		return
	}
	_, finalPass := o.taskFinalPass.Load(taskID)
	// A completion helper starts only once its stage has its result, so each of its passes is its last.
	finalPass = finalPass || isCompletionHelper(task)
	committed := func(state.OperationResult) {
		if o.helperOutputCommittedHook != nil {
			o.helperOutputCommittedHook()
		}
		if isWatcherTrigger(task.Trigger) && finalPass {
			o.taskFinalPass.Delete(taskID)
		}
	}
	finished := func(err error) {
		if err != nil && restoreLatest() {
			interruptIfSuperseded(err)
		}
	}
	finish := func(status assistants.TaskStatus, reason string, replies []plannedMessage) {
		finished(o.finishTaskTurnReplyingGuarded(taskID, consumed, assistants.StatusRunning, status, reason, replies, guard, committed, resultEff))
	}

	// An on_message helper answers the request it worked on, in the commit of its result, and rests for the next one.
	if turn.request != "" {
		replies := o.planTurnReplies(taskID, turn.number, turnReplyBody(taskID, turn.number, result))
		finished(o.finishOnMessageRequest(ctx, taskID, turn, state.RequestOutcomeResult, "", replies, guard, committed, resultEff))
		return
	}

	// A watcher's successful turn rests in "watching" unless it is its final pass: always for a stage's own
	// on_completion helper (isCompletionHelper), otherwise as taskFinalPass marks it (required last word, MaxPasses).
	if isWatcherTrigger(task.Trigger) && !finalPass {
		finish(assistants.StatusWatching, "", nil)
		return
	}
	// A run agent is a standing role: its turn ends idle (queued, which never holds the run open — see
	// state.RunBlocker) rather than completed, and the next message addressed to it starts its next turn
	// (Send → maybeScheduleTask). A message that arrived during this turn starts it right away. Its result also
	// answers the message that woke the turn: a passive reply in the same commit (agentcomm_turn_reply.go).
	if task.Scope == assistants.ScopeRun {
		replies := o.planTurnReplies(taskID, consumed, turnReplyBody(taskID, consumed, result))
		finish(assistants.StatusQueued, runAgentIdleReason, replies)
		o.maybeScheduleTask(ctx, taskID)
		return
	}
	// A helper a stage agent spawned answers the request it was spawned for in the commit of its result: a
	// passive reply, so the stage's reply_to wait on that request ends with the answer.
	if task.CreatedBy == assistants.OriginStageAgent {
		replies := o.planTurnReplies(taskID, consumed, turnReplyBody(taskID, consumed, result))
		finish(assistants.StatusCompleted, "", replies)
		return
	}
	finish(assistants.StatusCompleted, "", nil)
}

// parkTaskForQuota parks a helper turn that hit a provider usage limit: the task goes back to queued — not
// terminal, so its mesh identity, TurnInbox and the waits on its answer stay — carrying the wait in
// TaskState.Quota, and its process and lease end with the turn. The wait comes from the stage's shared
// primitives (quotaWaits, planQuotaWait); the wake timer is the same run-scoped one (retryTaskAt). A limit that
// cannot be waited out also puts PauseTask's manual hold on the task in the same commit: it stays paused with
// the exact reason, and ContinueTask probes it once.
func (o *Orchestrator) parkTaskForQuota(ctx context.Context, taskID assistants.TaskID, limit *executor.UsageLimitError, guard func(*state.RunState) error) error {
	t, ok := o.opts.Store.Snapshot().Assistants.Tasks[taskID]
	if !ok {
		return errHelperOutputSuperseded
	}
	fail := func(err error) error {
		reason := o.taskTurnFailure(taskID, "assistants: record usage limit", err.Error())
		return o.finishTaskTurnReplyingGuarded(taskID, 0, assistants.StatusRunning, assistants.StatusFailed, reason, nil, guard, nil)
	}
	waits := quotaWaits(t.Quota, state.QuotaTargetHelper, false, limit)
	q := planQuotaWait(o.opts.Config.UsageLimit, limit, state.QuotaTargetHelper, waits, o.clk().Now())
	reason := o.sanitizeTaskReason(taskQuotaReason(q))
	parkEff, err := taskStatusChangeEffect(taskID, state.TaskStatusChangedPayload{Status: string(assistants.StatusQueued), Reason: reason, Quota: &q})
	if err != nil {
		return fail(err)
	}
	effects := []state.Effect{parkEff}
	if q.PauseReason != "" {
		holdEff, err := taskHoldEffect("task_hold_set", taskID, holdOwnerManual, reason)
		if err != nil {
			return fail(err)
		}
		effects = append(effects, holdEff)
	}
	op := state.Operation{
		ID:      "task-quota-park:" + string(taskID) + ":" + randomOpSuffix(),
		Kind:    "park_task_quota",
		Expect:  state.Preconditions{TaskStatus: map[string]assistants.TaskStatus{string(taskID): assistants.StatusRunning}, Check: guard},
		Effects: effects,
	}
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventAssistantTaskStatus, StageID: t.RootStageID, Data: map[string]any{
			"task_id": string(taskID), "status": string(assistants.StatusQueued), "reason": reason,
		}})
		o.publishQuotaNotice(eventQuotaWait, t.RootStageID, taskID, quotaNoticeData(&q, QuotaWaitMessage(&q)))
		// Parked idle: the waits on what the cut-short turn read and left end now, not at the next start.
		o.releaseUnanswerableWaitsAfterFinish()
		// The pass of a stage that failed while the turn ran never starts again: it ends now, not at q.Until.
		if o.cancelCompletionPassOfFailedStage(taskID) {
			return
		}
		if q.PauseReason == "" {
			o.retryTaskAt(ctx, taskID, q.Until)
		}
	})
	o.handleTaskOperationErr(err)
	return err
}

// taskQuotaReason is the status reason of a parked task: until when it waits, or why it is paused.
func taskQuotaReason(q state.QuotaWait) string {
	if q.PauseReason != "" {
		return "usage limit: paused (" + q.PauseReason + ")"
	}
	return "usage limit: waiting until " + q.Until.UTC().Format(time.RFC3339)
}

// failAnsweredTurn ends a turn whose answer cannot be accepted (consumed: the turn's number if it ran to its end). A
// helper task fails, a run agent goes idle and an on_message helper fails only the turn's request; the requester
// gets a passive reply with why and the unvalidated answer, so nobody waits on an agent that will not write again.
func (o *Orchestrator) failAnsweredTurn(ctx context.Context, taskID assistants.TaskID, task assistants.Task, turn helperTurn, consumed uint64, reason string, answer unacceptedAnswer, guard func(*state.RunState) error) error {
	if turn.request != "" {
		return o.failOnMessageRequest(ctx, taskID, task, turn, reason, answer, guard)
	}
	reason = o.sanitizeTaskReason(reason) // the reply and the idle reason below use the same recorded value
	standing := task.Scope == assistants.ScopeRun
	// turn 0 consumed nothing: going idle would hand the same input straight back to a new turn.
	if consumed == 0 || (!standing && task.CreatedBy != assistants.OriginStageAgent) {
		return o.finishTaskTurnReplyingGuarded(taskID, consumed, assistants.StatusRunning, assistants.StatusFailed, reason, nil, guard, nil)
	}
	replies := o.failureReplies(taskID, task, consumed, reason, standing, answer)
	if !standing {
		return o.finishTaskTurnReplyingGuarded(taskID, consumed, assistants.StatusRunning, assistants.StatusFailed, reason, replies, guard, nil)
	}
	if err := o.finishTaskTurnReplyingGuarded(taskID, consumed, assistants.StatusRunning, assistants.StatusQueued, runAgentIdleAfterFailure(reason), replies, guard, nil); err != nil {
		return err
	}
	o.maybeScheduleTask(ctx, taskID)
	return nil
}

// failHelperTurn ends turn, which failed for reason with no answer to show (cause, when the process failed). An
// on_message helper fails only the turn's request and takes the next one; a process that never started fails the
// task as before, as does a failure of any other task — the helper keeps its requests for the Retry.
func (o *Orchestrator) failHelperTurn(ctx context.Context, taskID assistants.TaskID, task assistants.Task, turn helperTurn, consumed uint64, reason string, cause error, guard func(*state.RunState) error) error {
	if turn.request != "" && !errors.Is(cause, executor.ErrAgentStart) {
		return o.failOnMessageRequest(ctx, taskID, task, turn, reason, unacceptedAnswer{}, guard)
	}
	return o.finishTaskTurnReplyingGuarded(taskID, consumed, assistants.StatusRunning, assistants.StatusFailed, reason, nil, guard, nil)
}

// failureReplies plans the passive replies that end turn of taskID with why it failed and its unvalidated answer;
// standing says whether writing again starts another turn.
func (o *Orchestrator) failureReplies(taskID assistants.TaskID, task assistants.Task, turn uint64, reason string, standing bool, answer unacceptedAnswer) []plannedMessage {
	agent := task.Title
	if agent == "" {
		agent = string(task.ProfileID)
	}
	answer.text = o.RedactSecrets(answer.text)
	body := turnFailureBody(taskID, turn, o.RedactSecrets(agent), reason, standing, answer)
	return o.planTurnReplies(taskID, turn, body)
}

// parkYieldedTurn parks a yielded task in waiting_peer — ending turn, which consumed its input — and closes the ready-before-park window: a reply (or a
// counter-question) may have landed while the process was finishing, when the task was still running and the
// resume Send attempted was a no-op. Nobody else will resume it now, so it re-checks through the same
// CAS-protected resumes Send uses — a resume that already won makes them no-ops. A wait the run closed
// (closeRemainingMeshWaits) never resumes anybody.
func (o *Orchestrator) parkYieldedTurn(ctx context.Context, taskID assistants.TaskID, turn uint64, waiter agentcomm.ParticipantID, waitsBefore map[agentcomm.WaitID]agentcomm.WaitStatus, guard func(*state.RunState) error) error {
	if err := o.finishTaskTurnReplyingGuarded(taskID, turn, assistants.StatusRunning, assistants.StatusWaitingPeer, "", nil, guard, nil); err != nil {
		return err
	}
	for id, w := range o.opts.Store.Snapshot().Assistants.Mesh.Waits {
		if _, existed := waitsBefore[id]; w.Waiter == waiter && !existed && w.Status == agentcomm.WaitRunClosed {
			return nil
		}
	}
	o.tryResumePeer(ctx, taskID)
	o.maybeResumeAwaitingPeerQuestion(ctx, waiter)
	return nil
}

// turnYielded reports whether a helper turn yielded into a mesh wait: one of waiter is still pending, or the turn
// left no final text after a wait ended that it registered or found pending (an already_pending receipt names
// one). A non-empty answer is never a yield: if it does not decode it is a protocol error, not a rerun.
// byWaits decides by the turn's waits alone (turnEpisode), as for a stage author: native mode forces a payload
// even into a turn that yielded, so its final text tells nothing.
func (o *Orchestrator) turnYielded(waiter agentcomm.ParticipantID, waitsBefore map[agentcomm.WaitID]agentcomm.WaitStatus, rawText string, byWaits bool) bool {
	if byWaits {
		return len(o.turnEpisode(waiter, waitsBefore)) > 0
	}
	if o.hasOpenMeshWaitFor(waiter) {
		return true
	}
	if strings.TrimSpace(rawText) != "" {
		return false
	}
	for id := range o.meshWaitStatusesOf(waiter) {
		if before, existed := waitsBefore[id]; !existed || before == agentcomm.WaitPending {
			return true
		}
	}
	return false
}

// maxFailureReasonLen (in characters, ellipsis included) keeps a failure reason short enough for the task list
// and the event log.
const maxFailureReasonLen = 300

// contractNotMet is what failed in a turn whose answer broke the result contract. protocolErrorPrefix stays first:
// it marks an invalid answer, whose raw text the dashboard can show.
const contractNotMet = protocolErrorPrefix + "result contract not met"

// helperProtocolReason is the reason of a turn whose answer broke the result contract.
func (o *Orchestrator) helperProtocolReason(task assistants.Task, exec assistants.Execution, detail string) string {
	return o.helperTurnFailure(contractNotMet, task, exec, detail)
}

// helperTurnFailure is the reason of every failed helper turn: what went wrong and with which executor — role,
// command and its source, never the argv — then the detail, so the dashboard says why. The detail is redacted
// before the reason is cut to length: a secret cut in half would no longer be recognised.
func (o *Orchestrator) helperTurnFailure(what string, task assistants.Task, exec assistants.Execution, detail string) string {
	reason := what + " (role " + string(task.ProfileID) + ", " + executorLabel(exec) + ")"
	if detail != "" {
		reason += ": " + o.RedactSecrets(detail)
	}
	if r := []rune(reason); len(r) > maxFailureReasonLen {
		return string(r[:maxFailureReasonLen-1]) + "…"
	}
	return reason
}

// taskTurnFailure is helperTurnFailure for a task known by id, with the executor it runs.
func (o *Orchestrator) taskTurnFailure(taskID assistants.TaskID, what, detail string) string {
	rs := o.opts.Store.Snapshot()
	var task assistants.Task
	if t, ok := rs.Assistants.Tasks[taskID]; ok {
		task = t.Task
	}
	return o.helperTurnFailure(what, task, o.taskExecution(rs, task), detail)
}

// processFailure says how a turn's process failed — the executable did not start, the CLI reported an error itself
// (such as "Not logged in") or the process just failed — and the error's text.
func processFailure(err error) (what, detail string) {
	if err != nil {
		detail = err.Error()
	}
	var cliErr *executor.CLIReportedError
	switch {
	case errors.Is(err, executor.ErrAgentStart):
		return "helper executable did not start", detail
	case errors.As(err, &cliErr):
		return "helper CLI reported an error", detail
	default:
		return "helper execution failed", detail
	}
}
