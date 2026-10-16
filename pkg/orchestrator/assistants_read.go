// Package orchestrator (this file): the read-side query surface wiring needs — pkg/server's AssistantsReader
// (T3.7) has nothing to call against the real runtime without it. Этап 3 scope has no recursion (Attach never
// sets ParentTaskID, see assistants.go's doc comment), so TaskTree's walk and TaskEvents' per-task filter are
// both exercised today only at depth 0 — written generally so Этап 4a's recursive delegation does not need a
// second tree-walk written from scratch.
package orchestrator

import (
	"fmt"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// TaskTree returns every task rooted at stageID, in a stable deterministic order: each root-level task (in
// creation order, state.AssistantsState.ByRootStage) immediately followed by its own children (depth-first,
// pre-order) — a parent always appears before any of its descendants, and no task appears twice. Backs GET
// /api/stages/{id}/assistants (pkg/server/assistants.go's AssistantsReader.TaskTree) via a cmd/afm adapter.
func (o *Orchestrator) TaskTree(stageID string) ([]assistants.Task, error) {
	if o.reg().Stage(stageID) == nil {
		return nil, fmt.Errorf("assistants: unknown stage %q", stageID)
	}
	rs := o.opts.Store.Snapshot()
	var out []assistants.Task
	var walk func(id assistants.TaskID)
	walk = func(id assistants.TaskID) {
		t, ok := rs.Assistants.Tasks[id]
		if !ok {
			return // projection is internally consistent in practice; defensive, not expected
		}
		out = append(out, t.Task)
		for _, child := range rs.Assistants.ByParent[id] {
			walk(child)
		}
	}
	for _, id := range rs.Assistants.ByRootStage[stageID] {
		walk(id)
	}
	return out, nil
}

// TaskWaiver reports a task's waiver state (Этап 7, T7.6) — ok is false for an unknown task id. Mirrors
// WatcherStatus's pattern (watch_tasks.go): extra per-task info not carried by the bare assistants.Task struct
// TaskTree returns, read via a small side lookup rather than widening TaskTree's return type.
func (o *Orchestrator) TaskWaiver(taskID assistants.TaskID) (waived bool, reason string, ok bool) {
	rs := o.opts.Store.Snapshot()
	t, found := rs.Assistants.Tasks[taskID]
	if !found {
		return false, "", false
	}
	return t.Waived, t.WaiverReason, true
}

// AssistantsPolicyRevision returns the run's current effective collaboration depth-policy revision and max
// depth (T4.5: the live values an action draft's ExpectedRevision must still match before the draft-confirm
// HTTP path proceeds to Attach).
func (o *Orchestrator) AssistantsPolicyRevision() (revision string, maxDepth int) {
	rs := o.opts.Store.Snapshot()
	return rs.Assistants.Policy.Revision, rs.Assistants.Policy.MaxDepth
}

// TaskEvents returns a bounded page of taskID's durable event history strictly after cursor, in increasing
// Seq order, plus the cursor for the next page and whether more are available. Backs GET
// /api/assistants/{id}/events. An unknown taskID is not an error — it simply has no events (mirrors
// state.Store.TaskEvents, which has no notion of "does this task exist").
func (o *Orchestrator) TaskEvents(taskID assistants.TaskID, cursor uint64, limit int) ([]state.TaskEvent, uint64, bool, error) {
	all := o.opts.Store.TaskEvents(taskID)
	filtered := make([]state.TaskEvent, 0, len(all))
	for _, e := range all {
		if e.Seq > cursor {
			filtered = append(filtered, e)
		}
	}
	page := filtered
	hasMore := false
	if limit > 0 && len(filtered) > limit {
		// Never cut a page in the middle of a Seq group: one ApplyRunOperation can commit several TaskEvents
		// atomically (e.g. finishTaskFrom's status+result pair), and the cursor returned below IS a Seq value —
		// if the page ended between two events sharing that Seq, the sibling would be filtered out as "already
		// consumed" (e.Seq > cursor) on the very next page and lost forever. Grow the page past limit instead.
		cut := limit
		for cut < len(filtered) && filtered[cut].Seq == filtered[cut-1].Seq {
			cut++
		}
		page = filtered[:cut]
		hasMore = cut < len(filtered)
	}
	next := cursor
	if len(page) > 0 {
		next = page[len(page)-1].Seq
	}
	return page, next, hasMore, nil
}
