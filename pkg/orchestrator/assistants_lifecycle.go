package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/memory"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
)

// helperAgentRunner is the subsituteable seam for one helper-agent turn — mirrors verifyAgentRunner.
// Production is execHelperAgent; tests inject a fake (see assistants_test.go).
type helperAgentRunner func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error)

// helperExecutorConfig builds the executor.Config for one helper task's run — pulled out of execHelperAgent so
// a test can check it without spawning a process. Dir is the project root, exactly like the stage agent's and
// the verifier's (o.opts.RootDir, already worktree-aware; empty = afm's own CWD): a helper reviews code, so it
// must be able to read it where it lives. Its own scratch/ is handed over as AFM_SCRATCH_DIR, a place for
// notes. Whether the helper can also write to the project is up to its environment (a host codex sandbox is
// read-only, a container is not) — this config promises nothing about it.
func (o *Orchestrator) helperExecutorConfig(taskID assistants.TaskID, cmd string) executor.Config {
	participant := o.taskMeshParticipant(taskID)
	return executor.Config{
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
		Redact:         o.RedactSecrets,
		MeshClient:     o.meshClientFor(participant),
		OnDelivered:    o.deliverRenderedInbox(participant),
	}
}

// execHelperAgent is the production implementation of o.runHelperAgent: builds a fresh executor.Executor
// (mirrors runnerForVerify/execVerifyAgent) and delegates to RunHelperAgent. Cost accounting attribution
// (T3.8) is wired here via OnUsage/AppendTask — task identity (root stage, parent task) is read back from
// the current projection rather than widening the helperAgentRunner seam signature, which tests (T3.4)
// already depend on.
func (o *Orchestrator) execHelperAgent(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
	cfg := o.helperExecutorConfig(assistants.TaskID(taskID), cmd)
	cfg.OnUsage = o.recordTaskUsage(assistants.TaskID(taskID), assistants.ProfileID(profileID))
	return executor.New(cfg).RunHelperAgent(ctx, taskID, profileID, prompt, logFile, resultFile)
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

// persistTaskResult durably (atomic write) saves an accepted assistants.Result under the task's own
// directory (T3.6's result.json).
func (o *Orchestrator) persistTaskResult(dir string, result assistants.Result) error {
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return memory.AtomicWrite(filepath.Join(dir, "result.json"), raw)
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
// the EXISTING o.fingerprintFn/o.fingerprintDir seam — never a second implementation), run the helper agent,
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

	dir := o.taskDir(taskID)
	if err := ensureTaskWorkspace(dir, o.taskScratchDir(taskID)); err != nil {
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "assistants: create task workspace: "+err.Error())
		return
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
			o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "assistants: root stage no longer exists")
			return
		}
		stage = *st
	}
	tc, cerr := o.buildTaskContext(task, stage)
	if cerr != nil {
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "assistants: build context: "+cerr.Error())
		return
	}
	if werr := memory.AtomicWrite(filepath.Join(dir, "context.md"), []byte(tc.Prompt)); werr != nil {
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "assistants: persist context: "+werr.Error())
		return
	}
	if werr := o.saveTurnInbox(taskID, tc.inboxIDs); werr != nil {
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "assistants: persist turn inbox: "+werr.Error())
		return
	}

	logFile := filepath.Join(dir, "agent.log")
	resultFile := filepath.Join(dir, rawResultFileName)

	// The path is per-task, but a watch/continuation task re-runs under the SAME id (fireWatcherTask ->
	// maybeScheduleTask -> runHelperTask), and the executor only overwrites resultFile when the turn produced
	// nonempty text (RunHelperAgent, pkg/executor/hardening.go). Without clearing it first, a textless turn
	// would silently serve an EARLIER turn's answer as if it were this one's.
	if rerr := os.Remove(resultFile); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "assistants: clear stale raw answer: "+rerr.Error())
		return
	}

	waiter := o.taskMeshParticipant(taskID)
	waitsBefore := o.meshWaitIDsOf(waiter)
	beforeFP, beforeOK := o.fingerprintFn(o.fingerprintDir())
	outcome, err := o.runHelperAgent(runCtx, string(taskID), string(task.ProfileID), task.ProfileSnapshot.Command, tc.Prompt, logFile, resultFile)
	// The <inbox> messages of this turn's prompt are delivered once its process has run. The executor already
	// marks them at process start (OnDelivered); marking the same set here (idempotent per message) also covers
	// a helperAgentRunner that never calls OnDelivered — otherwise a run agent would see them as unseen forever
	// and restart itself after every turn. A process that never started leaves them accepted for the next turn.
	// Only a turn that ran to its end consumed its input: a cut-short one (run shutdown, Pause) keeps the
	// turn-inbox record, so its retry is handed the same messages again.
	if err == nil && outcome.ProcessOK {
		if tc.markInboxDelivered != nil {
			tc.markInboxDelivered()
		}
		if cerr := o.clearTurnInbox(taskID); cerr != nil {
			log.Printf("assistants: task %s: forget finished turn input: %v", taskID, cerr)
		}
	}
	if afterFP, afterOK := o.fingerprintFn(o.fingerprintDir()); beforeOK && afterOK && beforeFP != afterFP {
		// Observation only — never changes the task's verdict (mirrors verify_tree_modified). Not an accusation
		// either: the tree may have been changed by anybody while the helper ran, so the notice only says when.
		agent := task.Title
		if agent == "" {
			agent = string(task.ProfileID)
		}
		o.publishNotice(bus.Event{Type: bus.EventHelperTreeModified, StageID: task.RootStageID, Data: map[string]any{
			"task_id": string(taskID), "agent": agent,
		}})
	}

	// finalizedByOther is true when ANOTHER writer (CancelTask) has ALREADY committed a terminal status for
	// this task while the agent call above was in flight. CancelTask cancels this exact task's runCtx (via
	// o.taskCancels) and then, in the SAME goroutine with no intervening blocking call, immediately commits
	// "cancelled" itself — so by the time this subprocess call notices ctx.Done(), unwinds, and reaches here,
	// that commit has, in practice, already landed. Checking the task's CURRENT projected status (o.taskTerminal,
	// rather than runCtx.Err()) is deliberate: runCtx is a CHILD of the whole run's ctx (o.runContext), so a
	// plain "was runCtx cancelled" check cannot tell a targeted CancelTask apart from an unrelated whole-run
	// shutdown — which has no other writer and MUST still let this goroutine commit the task's terminal
	// status (see TestShutdown_NoWritesAfterStoreClose). Without this check, any outcome surfacing after a
	// CancelTask (including a stale "success" from a subprocess that happened to finish right as it was
	// killed) would either race a second, contradictory status onto "cancelled", or wrongly persist/attach a
	// result.json + task_result event to a task the caller already told to stop.
	finalizedByOther := o.taskTerminal(taskID)

	// The run itself is shutting down (Ctrl+C): an invocation it cut short did not fail. The task stays
	// running, exactly like an interrupted stage, and recoverAssistants re-queues it when the run is resumed.
	// A turn that still finished with a result is recorded as usual below.
	if ctx.Err() != nil && (err != nil || !outcome.ProcessOK) {
		log.Printf("assistants: task %s interrupted by run shutdown — it is retried when the run resumes", taskID)
		return
	}

	if err != nil {
		if finalizedByOther {
			return
		}
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "assistants: helper execution failed: "+err.Error())
		return
	}
	switch {
	case outcome.Interrupted || finalizedByOther:
		// PauseTask/CancelTask already recorded (or will record) their own status transition — a stopped
		// subprocess reaching here must not clobber it with a second, contradictory status.
		return
	case outcome.TimedOut:
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "helper agent timed out")
		return
	case !outcome.ProcessOK:
		// A PauseTask (or StopAndClarify, T4.7) on an ALREADY-RUNNING task cancels runCtx directly (not via
		// executor's InterruptCh), which RunHelperAgent's default branch (pkg/executor/hardening.go) surfaces
		// as a bare !ProcessOK outcome — indistinguishable at this point from a genuine crash. CancelTask
		// already returned above via finalizedByOther, so reaching here with ANY signal set below can only mean
		// a deliberate pause/clarify, never a real crash:
		//   - taskPausePending (LoadAndDelete, consumed exactly once HERE): PauseTask's signal, set BEFORE
		//     cancel() — NOT the live hold state, which an immediate ContinueTask can already have cleared by
		//     the time this goroutine reaches this check (see its doc comment on orchestrator.go).
		//   - taskClarifyPending (LoadAndDelete, consumed exactly once HERE): StopAndClarify's signal — it has
		//     no separate "clear" step precisely to avoid racing THIS check (see its doc comment).
		//   - taskHeld: belt-and-suspenders fallback for the (non-racy) case where Continue has not run yet.
		// Requeue either way, with the same post-commit lost-wakeup retry as spawnTask's pre-start pause path
		// (a concurrent ContinueTask may have cleared the hold and tried to reschedule before this requeue
		// committed, in which case it saw status still "running" and silently no-op'd).
		_, pausedBeforeClassification := o.taskPausePending.LoadAndDelete(taskID)
		_, clarifying := o.taskClarifyPending.LoadAndDelete(taskID)
		if pausedBeforeClassification || clarifying || o.taskHeld(taskID) {
			o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusQueued, "re-queued: paused while running")
			if !o.taskHeld(taskID) {
				o.maybeScheduleTask(ctx, taskID)
			}
			if o.taskRequeuePreDeferHook != nil {
				o.taskRequeuePreDeferHook(taskID)
			}
			return
		}
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, o.helperFailureReason(outcome.ProcessErr))
		return
	}

	// Agent Mesh Phase 5, T5.4: the subprocess registered a mesh Wait on a colleague (another helper, a
	// stage, or the human) and exited cooperatively — mirrors retry.go's stage-level T5.2 branch. This is NOT a
	// false completion and NOT a protocol error: a yielding agent prints no final answer, so the yield is
	// classified BEFORE the executor's "no final answer" protocol error is looked at. This task's own
	// concurrency lease is released simply by this function returning next.
	if o.turnYielded(waiter, waitsBefore, outcome.RawText) {
		o.parkYieldedTurn(ctx, taskID, waiter, waitsBefore)
		return
	}
	if outcome.ProtocolErr != nil {
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "protocol_error: "+outcome.ProtocolErr.Error())
		return
	}

	env, repaired, decErr := assistants.DecodeResultWithRepair([]byte(outcome.RawText))
	if decErr != nil {
		// A malformed/invalid machine answer is a protocol error — NEVER treated as a "pass" (dev-plan's
		// §9.3 acceptance criterion), regardless of how plausible the raw text looks. Mechanical repair has
		// already been tried and failed — decErr is the ORIGINAL strict decode error.
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "protocol_error: "+decErr.Error())
		return
	}
	if repaired {
		// Observational only — repair changes syntax, never the model's own verdict text.
		o.publishNotice(bus.Event{Type: bus.EventHelperResultRepaired, StageID: task.RootStageID, Data: map[string]any{
			"task_id": string(taskID),
		}})
	}
	if env.Kind == assistants.KindDelegate {
		// T4a.3: admit the requested children and move this task to waiting_children — never treated as a
		// completed check (assistants.Envelope.ClosesRequiredGate is false for kind:"delegate" by
		// construction, T4a.1).
		o.handleDelegate(ctx, taskID, tc.Version, *env.Delegate)
		return
	}
	if env.Kind != assistants.KindResult || env.Result == nil {
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "protocol_error: unsupported kind")
		return
	}
	if env.Result.Verdict == assistants.VerdictPass && o.requiredChildOpen(taskID) {
		// dev-plan.md's TestResult_ConsistencyWithRequiredChildren: a required child that has not (yet, or
		// ever will) reached verdict:"pass" keeps this gate closed — the model's own claimed "pass" text does
		// not override it.
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "protocol_error: verdict:\"pass\" with an unresolved required child")
		return
	}

	// Stamp the SERVER's own ContextVersion rather than trusting whatever (if anything) the model echoed
	// back — a result must always reference the actual context it was computed against, not a model's
	// possibly-stale or fabricated claim about it.
	result := o.redactResult(*env.Result)
	result.ContextVersion = tc.Version
	result.Repaired = repaired
	if werr := o.persistTaskResult(dir, result); werr != nil {
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "assistants: persist result: "+werr.Error())
		return
	}
	// The decoded result is attached as a SECOND effect of the SAME finish operation — never a separate
	// ApplyRunOperation call — so the journal never has a window where status says "completed" but the
	// result event does not exist yet (see finishTaskFrom's extra doc comment).
	resultEff, eerr := taskResultEffect(taskID, result)
	if eerr != nil {
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "assistants: marshal result event: "+eerr.Error())
		return
	}

	// Этап 5 (T5.4): a checkpoints/on_completion watcher's successful turn lands in the non-terminal
	// "watching" resting state — unless this was explicitly marked as its FINAL pass (on_completion's single
	// turn, a required watcher's guaranteed last word once its root stage is done, or the collector itself
	// reporting MaxPasses reached — see watch_tasks.go's commitAndFire/finalizeWatcherOnCompletion, both of
	// which Store the taskFinalPass signal strictly BEFORE this turn's own completion can observe it). A plain
	// one_shot task never reaches this branch non-final (isWatcherTrigger is false for it).
	if isWatcherTrigger(task.Trigger) {
		_, final := o.taskFinalPass.LoadAndDelete(taskID)
		if !final {
			o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusWatching, "", resultEff)
			return
		}
	}
	// A run agent is a standing role: its turn ends idle (queued, which never holds the run open — see
	// state.RunBlocker) rather than completed, and the next message addressed to it starts its next turn
	// (Send → maybeScheduleTask). A message that arrived during this turn starts it right away.
	if task.Scope == assistants.ScopeRun {
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusQueued, runAgentIdleReason, resultEff)
		o.maybeScheduleTask(ctx, taskID)
		return
	}
	o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusCompleted, "", resultEff)
}

// meshWaitIDsOf returns the ids of every mesh wait waiter has registered so far, whatever their status.
func (o *Orchestrator) meshWaitIDsOf(waiter agentcomm.ParticipantID) map[agentcomm.WaitID]bool {
	ids := map[agentcomm.WaitID]bool{}
	for id, w := range o.opts.Store.Snapshot().Assistants.Mesh.Waits {
		if w.Waiter == waiter {
			ids[id] = true
		}
	}
	return ids
}

// parkYieldedTurn parks a yielded task in waiting_peer and closes the ready-before-park window: a reply (or a
// counter-question) may have landed while the process was finishing, when the task was still running and the
// resume Send attempted was a no-op. Nobody else will resume it now, so it re-checks through the same
// CAS-protected resumes Send uses — a resume that already won makes them no-ops. A wait the run closed
// (closeRemainingMeshWaits) never resumes anybody.
func (o *Orchestrator) parkYieldedTurn(ctx context.Context, taskID assistants.TaskID, waiter agentcomm.ParticipantID, waitsBefore map[agentcomm.WaitID]bool) {
	o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusWaitingPeer, "")
	for id, w := range o.opts.Store.Snapshot().Assistants.Mesh.Waits {
		if w.Waiter == waiter && !waitsBefore[id] && w.Status == agentcomm.WaitRunClosed {
			return
		}
	}
	o.tryResumePeer(ctx, taskID)
	o.maybeResumeAwaitingPeerQuestion(ctx, waiter)
}

// turnYielded reports whether a helper turn that ran to its end yielded into a mesh wait instead of answering:
// a wait of waiter is still pending, or the turn registered a new wait (one not in waitsBefore) and left no
// usable answer — the awaited reply arrived before the turn could park, so that wait is already ready.
func (o *Orchestrator) turnYielded(waiter agentcomm.ParticipantID, waitsBefore map[agentcomm.WaitID]bool, rawText string) bool {
	if o.hasOpenMeshWaitFor(waiter) {
		return true
	}
	registered := false
	for id := range o.meshWaitIDsOf(waiter) {
		registered = registered || !waitsBefore[id]
	}
	if !registered {
		return false
	}
	_, _, err := assistants.DecodeResultWithRepair([]byte(rawText))
	return err != nil
}

// redactResult masks secrets in every free-text field of a helper's result before it is written to result.json
// or the journal's task_result: summary, findings, evidence, suggested change and limitations.
func (o *Orchestrator) redactResult(r assistants.Result) assistants.Result {
	r.Summary = o.RedactSecrets(r.Summary)
	r.Evidence = o.redactAll(r.Evidence)
	r.SuggestedChange = o.RedactSecrets(r.SuggestedChange)
	r.Limitations = o.RedactSecrets(r.Limitations)
	findings := make([]assistants.Finding, len(r.Findings))
	for i, f := range r.Findings {
		f.ID, f.Severity, f.Detail = o.RedactSecrets(f.ID), o.RedactSecrets(f.Severity), o.RedactSecrets(f.Detail)
		if f.Path != nil {
			path := o.RedactSecrets(*f.Path)
			f.Path = &path
		}
		findings[i] = f
	}
	if r.Findings != nil {
		r.Findings = findings
	}
	return r
}

// maxFailureReasonLen (in characters, ellipsis included) keeps a failure reason short enough for the task list
// and the event log.
const maxFailureReasonLen = 300

// helperFailureReason is the failed task's reason: the process error when there is one (secrets redacted,
// length-capped), so the dashboard says why the helper failed — not only that it did.
func (o *Orchestrator) helperFailureReason(processErr error) string {
	const prefix = "helper execution failed"
	if processErr == nil {
		return prefix
	}
	reason := []rune(prefix + ": " + o.RedactSecrets(processErr.Error()))
	if len(reason) > maxFailureReasonLen {
		return string(reason[:maxFailureReasonLen-1]) + "…"
	}
	return string(reason)
}
