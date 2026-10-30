// Package state (this file, Этап 7 T7.2/T7.4/T7.5): the durable, pure (no file I/O) computation of "which
// required helper task currently blocks stageID's completion barrier, if any" — the single source of truth
// consulted both by the live orchestrator's barrier (pkg/orchestrator) before it is allowed to mark a stage
// done, and by this package's own isIdle/accountIdleAndBackoff (T7.7) so live and replay classification never
// diverge. See pkg/assistants.RequiredGateOpen for the pure tree-walk this wraps with durable state lookups.
package state

import "github.com/akopichin/afm/pkg/assistants"

// currentRootChildren returns stageID's direct (ParentTaskID == nil) helper tasks belonging to the CURRENT
// root generation (RootGenerations[stageID], 0 if never bumped) — a stage-level mirror of
// pkg/orchestrator's currentChildren one level up. MINOR, intentional duplication: pkg/state cannot import
// pkg/orchestrator (forbidden cycle), and introducing a third, lower-level package for six lines of map
// filtering is not worth it — see dev-plan.md's Review decisions.
func (as AssistantsState) currentRootChildren(stageID string) []assistants.TaskID {
	gen := as.RootGenerations[stageID]
	var out []assistants.TaskID
	for _, id := range as.ByRootStage[stageID] {
		if t, ok := as.Tasks[id]; ok && t.ParentTaskID == nil && t.RootGeneration == gen {
			out = append(out, id)
		}
	}
	return out
}

// currentChildrenOf mirrors pkg/orchestrator's currentChildren (ParentGeneration-filtered direct children) —
// same MINOR-duplication rationale as currentRootChildren above.
func (as AssistantsState) currentChildrenOf(parentID assistants.TaskID, parentGeneration uint64) []assistants.TaskID {
	var out []assistants.TaskID
	for _, id := range as.ByParent[parentID] {
		if t, ok := as.Tasks[id]; ok && t.ParentGeneration == parentGeneration {
			out = append(out, id)
		}
	}
	return out
}

// tasksFor resolves a slice of ids to their current assistants.Task values, silently skipping any id that is
// no longer present (defensive only — ByRootStage/ByParent indexes are kept in sync with Tasks by construction).
func (as AssistantsState) tasksFor(ids []assistants.TaskID) []assistants.Task {
	out := make([]assistants.Task, 0, len(ids))
	for _, id := range ids {
		if t, ok := as.Tasks[id]; ok {
			out = append(out, t.Task)
		}
	}
	return out
}

// taskGateClosed reports whether task id has durably closed its own required gate (assistants.TaskGateClosed),
// reading LastVerdict from the projection — never a file. A WAIVED task (Этап 7, T7.6 — a separate, audited
// human decision) always counts as closed, regardless of its actual Status/LastVerdict: that is the whole
// point of a waiver — accepting the risk of a task that will never otherwise close its gate.
func (as AssistantsState) taskGateClosed(id assistants.TaskID) bool {
	t, ok := as.Tasks[id]
	if !ok {
		return false
	}
	if t.Waived {
		return true
	}
	return assistants.TaskGateClosed(t.Status, t.LastVerdict, t.LastVerdict != "")
}

// RequiredBlocker walks stageID's CURRENT required-chain task subtree (pkg/assistants.RequiredGateOpen,
// starting from the stage's current-generation direct children) and returns the first helper task that has
// not yet closed its required gate. ok=false means the barrier is currently satisfied for stageID — either
// there are no required tasks at all, or every one of them (recursively, through required edges only) has
// closed its gate. An advisory task's subtree is never descended into (§4.3) — a required task nested under
// an advisory one blocks only that advisory task, never the root stage.
func (as AssistantsState) RequiredBlocker(stageID string) (TaskState, bool) {
	roots := as.tasksFor(as.currentRootChildren(stageID))
	children := func(t assistants.Task) []assistants.Task {
		return as.tasksFor(as.currentChildrenOf(t.ID, t.TaskGeneration))
	}
	closed := func(t assistants.Task) bool { return as.taskGateClosed(t.ID) }

	open, blocker := assistants.RequiredGateOpen(roots, children, closed)
	if !open || blocker == nil {
		return TaskState{}, false
	}
	ts, ok := as.Tasks[blocker.ID]
	if !ok {
		return TaskState{}, false
	}
	return *ts, true
}

// RunBlocker reports whether any task ANYWHERE in the run — regardless of scope or root stage — currently
// holds accepted-but-unfinished work or a registered mesh wait with a continuation. Agent Mesh T6.3's
// RUN-level completion barrier, consulted by the orchestrator's shouldExit (pkg/orchestrator/scheduling.go)
// ONLY once every stage has already reached a terminal status. It is deliberately DIFFERENT and WIDER than
// RequiredBlocker above: RequiredBlocker only walks ONE stage's required-chain subtree, so it is blind both to
// a run-scoped standing role (T6.1/T6.2's AttachAndSend, which has no RootStageID at all) and to an advisory
// stage-scoped task whose root stage has already finished (advisory outcomes never gate their root stage,
// §4.3 — the stage can reach Done while the task is still mid-flight). Once every stage is terminal, which
// stage (if any) a task is rooted at no longer matters; only whether ANY task in the whole run still has live
// work does.
//
// A task sitting in StatusQueued is deliberately NOT a blocker: a run-scoped role attached with no first
// message (T6.2) must stay addressable forever without ever preventing the run from finishing — that is
// exactly "an idle run-scoped role does not hold the run open" (dev-plan.md T6.3). Every OTHER non-terminal
// status (running, waiting_parent, waiting_children, waiting_peer, watching, paused) means the task has been
// ACCEPTED and has durable state riding on it finishing cleanly — including waiting_peer, which a task reaches
// by registering a mesh Wait with a continuation and cooperatively releasing its own concurrency slot (T5.4):
// by the time it is sitting there, its own goroutine has already exited, so concurrency.Manager's agentWG no
// longer tracks it at all — this check is the only thing left that still does. An open-but-empty thread is not
// a task at all, so it was never a blocker here either way.
//
// ok=false means nothing currently blocks a run-level completion decision. Map iteration order is irrelevant:
// only whether SOME blocker exists matters to the caller, never which one.
func (as AssistantsState) RunBlocker() (TaskState, bool) {
	for _, t := range as.Tasks {
		if t == nil || t.Status.Terminal() || t.Status == assistants.StatusQueued {
			continue
		}
		return *t, true
	}
	return TaskState{}, false
}

// NeedsHumanDecision reports whether stageID's CURRENT required-completion blocker (if any) requires a human
// decision rather than just more machine work (T7.7, §5.6): a failed/cancelled/held task, or one that
// completed with a non-pass verdict (needs_changes/inconclusive), needs a Revise/Retry/Waiver from a person.
// A task still actively working (waiting_parent/queued/running/waiting_children/watching) does not. ok=false
// from RequiredBlocker (gate already satisfied, or nothing required attached) is "no decision pending" — a
// race between the stage's status and this read, not a durable state. Used both by isIdle (state.go) and by
// pkg/server's StageView (Этап 7, T7.9) to decide whether awaiting_assistants counts as dashboard attention.
func (as AssistantsState) NeedsHumanDecision(stageID string) bool {
	blocker, ok := as.RequiredBlocker(stageID)
	if !ok {
		return false
	}
	switch blocker.Status {
	case assistants.StatusFailed, assistants.StatusCancelled, assistants.StatusPaused:
		return true
	case assistants.StatusCompleted:
		return blocker.LastVerdict != assistants.VerdictPass
	default:
		return false
	}
}

// DeadRequiredTask returns the first failed or cancelled, non-waived required task anywhere in stageID's current
// required tree. Unlike RequiredBlocker it keeps walking below a task that is still open, so a dead task is
// found even when an earlier blocker is alive or waits for a decision. Waived and advisory tasks never count.
func (as AssistantsState) DeadRequiredTask(stageID string) (TaskState, bool) {
	return as.deadRequiredIn(as.currentRootChildren(stageID))
}

func (as AssistantsState) deadRequiredIn(ids []assistants.TaskID) (TaskState, bool) {
	for _, id := range ids {
		t, ok := as.Tasks[id]
		if !ok || t.Mode != assistants.ModeRequired {
			continue
		}
		if !t.Waived && (t.Status == assistants.StatusFailed || t.Status == assistants.StatusCancelled) {
			return *t, true
		}
		if dead, found := as.deadRequiredIn(as.currentChildrenOf(t.ID, t.TaskGeneration)); found {
			return dead, true
		}
	}
	return TaskState{}, false
}
