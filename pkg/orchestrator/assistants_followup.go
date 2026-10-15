// Package orchestrator (this file, T4.7): a message in a specific helper task's own feed, addressed ONLY to
// that task (dev-plan.md §9.2) — distinct from Attach (creates a new task) and from the side-agent
// conversation (whole-run, not task-scoped).
package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/memory"
	"github.com/akopichin/afm/pkg/state"
)

// followUpFileName is the per-task file a user's follow-up/clarify instruction is stored in — mirrors
// state.SavePreNote's stage-level prenote.md pattern, scoped to one task's own directory instead of a stage
// dir. Overwritten by each new call: "stored as the NEXT turn's input" means the latest text, not an
// accumulating log.
const followUpFileName = "followup.md"

// saveFollowUp durably (atomic write) stores text as taskID's pending follow-up instruction, read back by
// buildTaskContext on this task's next turn. Creates the task's own directory if it does not exist yet — a
// follow-up can arrive while the task is still queued, before runHelperTask ever calls ensureTaskWorkspace.
func (o *Orchestrator) saveFollowUp(taskID assistants.TaskID, text string) error {
	dir := o.taskDir(taskID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("assistants: create task dir for follow-up: %w", err)
	}
	return memory.AtomicWrite(filepath.Join(dir, followUpFileName), []byte(text))
}

// loadFollowUp reads taskID's pending follow-up instruction — "" if none was ever stored. A read error
// (including "file does not exist") is treated the same as "no follow-up": never a hard failure of the
// task's turn over a missing, optional file.
func (o *Orchestrator) loadFollowUp(taskID assistants.TaskID) string {
	raw, err := os.ReadFile(filepath.Join(o.taskDir(taskID), followUpFileName))
	if err != nil {
		return ""
	}
	return string(raw)
}

// currentTask is the shared lookup FollowUp/StopAndClarify need: the task's live projected state, or an
// error if it is not known at all.
func (o *Orchestrator) currentTask(taskID assistants.TaskID) (*state.TaskState, error) {
	rs := o.opts.Store.Snapshot()
	t, ok := rs.Assistants.Tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("assistants: unknown task %q", taskID)
	}
	return t, nil
}

// FollowUp stores text as taskID's pending follow-up instruction. It never touches an already-running
// subprocess — no live injection is promised, only that the text is read back the next time this task's
// context is built. Rejected for an already-terminal task — use StopAndClarify instead to actually end the
// current attempt and start a new one that will read it.
func (o *Orchestrator) FollowUp(ctx context.Context, taskID assistants.TaskID, text string) error {
	t, err := o.currentTask(taskID)
	if err != nil {
		return err
	}
	if t.Status.Terminal() {
		return &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: "task already finished"}
	}
	return o.saveFollowUp(taskID, text)
}

// StopAndClarify ends taskID's current attempt (cancelling its in-flight subprocess, if it is actually
// running) and arranges for it to restart with an updated follow-up instruction, for when a plain FollowUp
// would arrive too late (the current turn is already underway and would finish before any "next turn" a
// one-shot task would otherwise never take). Rejected for an already-terminal task, same as FollowUp. A task
// that is not yet running (queued/waiting_parent) has no attempt to end — saving the follow-up is enough, it
// will be read whenever that task's own turn naturally starts.
//
// Implementation: taskClarifyPending is set BEFORE cancelling — runHelperTask's own goroutine consumes it
// exactly once (LoadAndDelete) when classifying the resulting !ProcessOK outcome, and requeues instead of
// failing, then retries scheduling itself — the SAME running->queued requeue path (and lost-wakeup retry)
// spawnTask/runHelperTask already implement for a manual pause (see assistants.go's doc comments), without a
// second "clear" operation that would otherwise race that same classification (see taskClarifyPending's own
// doc comment on orchestrator.go for why that race is real).
func (o *Orchestrator) StopAndClarify(ctx context.Context, taskID assistants.TaskID, text string) error {
	t, err := o.currentTask(taskID)
	if err != nil {
		return err
	}
	if t.Status.Terminal() {
		return &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: "task already finished"}
	}
	if err := o.saveFollowUp(taskID, text); err != nil {
		return err
	}
	if t.Status != assistants.StatusRunning {
		return nil
	}
	v, ok := o.taskCancels.Load(taskID)
	if !ok {
		// Lost the race: the run already finished (or never truly started) between our status read above and
		// here — nothing to cancel, and the follow-up just saved is all a future turn will need.
		return nil
	}
	o.taskClarifyPending.Store(taskID, struct{}{})
	v.(*taskCancelEntry).cancel()
	return nil
}
