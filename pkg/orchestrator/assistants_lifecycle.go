package orchestrator

import (
	"context"
	"encoding/json"
	"log"
	"path/filepath"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/memory"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
)

// helperAgentRunner is the subsituteable seam for one helper-agent turn — mirrors verifyAgentRunner.
// Production is execHelperAgent; tests inject a fake (see assistants_test.go).
type helperAgentRunner func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error)

// helperExecutorConfig builds the executor.Config for one helper task's run — pulled out of execHelperAgent
// so T3.6's TestScratch_IsOnlyWritablePath can assert Dir == taskScratchDir without spawning a real process.
// Dir is the task's own scratch/ directory, NOT o.opts.RootDir: a helper's context (T3.5) already carries
// everything it is entitled to read as inline TEXT (plan/artifacts), so it needs no live write access to the
// project tree — scratch/ is the only directory it can write to.
func (o *Orchestrator) helperExecutorConfig(taskID assistants.TaskID, cmd string) executor.Config {
	return executor.Config{
		Command:        cmd,
		IdleTimeout:    o.opts.Config.Executor.IdleTimeout,
		TruncateOutput: o.opts.Config.Executor.TruncateOutput,
		WrapperDir:     executor.WrapperDirFor(cmd, o.opts.WrapperDir, o.opts.GeneratedAgents),
		Dir:            o.taskScratchDir(taskID),
		Debug:          o.opts.Debug,
		RunDir:         o.opts.RunDir,
		Phase:          "helper",
		UsageHint:      o.usageHintFor(cmd),
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
	o.taskCancels.Store(taskID, cancel)
	defer func() {
		o.taskCancels.Delete(taskID)
		cancel()
	}()

	dir := o.taskDir(taskID)
	if err := ensureTaskWorkspace(dir, o.taskScratchDir(taskID)); err != nil {
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "assistants: create task workspace: "+err.Error())
		return
	}
	o.persistTaskCache(dir, task)

	stage := o.graph.Stage(task.RootStageID)
	if stage == nil {
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "assistants: root stage no longer exists")
		return
	}
	tc, cerr := o.buildTaskContext(task, *stage)
	if cerr != nil {
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "assistants: build context: "+cerr.Error())
		return
	}
	if werr := memory.AtomicWrite(filepath.Join(dir, "context.md"), []byte(tc.Prompt)); werr != nil {
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "assistants: persist context: "+werr.Error())
		return
	}

	logFile := filepath.Join(dir, "agent.log")
	resultFile := filepath.Join(dir, "result.json.raw")

	beforeFP, beforeOK := o.fingerprintFn(o.fingerprintDir())
	outcome, err := o.runHelperAgent(runCtx, string(taskID), string(task.ProfileID), task.ProfileSnapshot.Command, tc.Prompt, logFile, resultFile)
	if afterFP, afterOK := o.fingerprintFn(o.fingerprintDir()); beforeOK && afterOK && beforeFP != afterFP {
		// Observation only — never changes the task's verdict (mirrors verify_tree_modified exactly).
		o.publishNotice(bus.Event{Type: bus.EventHelperTreeModified, StageID: task.RootStageID, Data: map[string]any{
			"task_id": string(taskID),
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
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "helper execution failed")
		return
	case outcome.ProtocolErr != nil:
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "protocol_error: "+outcome.ProtocolErr.Error())
		return
	}

	env, decErr := assistants.DecodeResult([]byte(outcome.RawText))
	if decErr != nil {
		// A malformed/invalid machine answer is a protocol error — NEVER treated as a "pass" (dev-plan's
		// §9.3 acceptance criterion), regardless of how plausible the raw text looks.
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "protocol_error: "+decErr.Error())
		return
	}
	if env.Kind != assistants.KindResult || env.Result == nil {
		// Этап 3 scope: kind:"delegate" (recursive delegation) lands in Этап 4a. Treat it as a protocol error
		// here rather than silently accepting a response shape this stage can't act on yet.
		o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusFailed, "protocol_error: unsupported kind in Этап 3 scope")
		return
	}

	// Stamp the SERVER's own ContextVersion rather than trusting whatever (if anything) the model echoed
	// back — a result must always reference the actual context it was computed against, not a model's
	// possibly-stale or fabricated claim about it.
	result := *env.Result
	result.ContextVersion = tc.Version
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
	o.finishTaskFrom(taskID, assistants.StatusRunning, assistants.StatusCompleted, "", resultEff)
}
