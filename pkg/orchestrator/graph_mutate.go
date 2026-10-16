// Этап 9 (T9.1, T9.4): the preview+commit API for dynamically adding a regular stage to a live run. Preview
// (PreviewAddStage) validates a draft WITHOUT mutating anything. Commit (CommitAddStage) re-validates
// everything under pkg/state.Store's own lock (the SAME lock the scheduler's own admission decisions use —
// Store.Apply/Store.AddStageToGraph share s.mu, see stage_graph.go's doc comment) and, on success, publishes a
// new registry revision and wakes the scheduler through the EXISTING activation paths — no bespoke spawn path.
package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// Insertion kinds for AddStageDraft.Kind.
const (
	AddStageKindInsertBefore = "insert_before"
	AddStageKindAppend       = "append"
)

// AddStageDraft is the request shape shared by PreviewAddStage and CommitAddStage — the HTTP layer decodes
// the wire request into this, unchanged, for both calls (T9.6's "re-fetch a fresh preview" sends the exact
// same shape again).
type AddStageDraft struct {
	Kind     string // AddStageKindInsertBefore | AddStageKindAppend
	Stage    graph.StageDraft
	TargetID string // insert_before only
	// ExplicitDeps, for append, overrides the sinks default. nil (not empty-non-nil) means "use sinks" —
	// callers that want an EXPLICIT empty dependency set must not be expressible here (T9.3 never needs it:
	// an appended stage with zero dependencies would run immediately, alongside the very first stages, which
	// is not "the end of the flow"); Preview/Commit always fall back to sinks when ExplicitDeps is nil.
	ExplicitDeps []string
	// ExpectedRevision, if non-zero, must match the registry's CURRENT revision — stale_revision otherwise
	// (optimistic concurrency against a concurrent graph change landing between two round trips).
	ExpectedRevision int64
	// RequestID is CommitAddStage's idempotency key (ignored by PreviewAddStage, which never mutates anything
	// to be idempotent about) — the SAME convention as ProposalActions.Accept/Dismiss's requestID: a repeated
	// commit with the SAME RequestID returns the ORIGINAL result instead of attempting a second insertion
	// (TestCommit_DuplicateOperationIgnored). Required (CommitAddStage rejects an empty RequestID).
	RequestID string
}

// AddStagePreview is PreviewAddStage's result — everything T9.6's dialog needs to render the dependency diff
// (and, for append, what the new stage will wait for) without any mutation having happened.
type AddStagePreview struct {
	Plan          graph.InsertionPlan
	GraphRevision int64
}

// ErrUnknownInsertionKind is returned for an AddStageDraft.Kind that is neither insert_before nor append.
var ErrUnknownInsertionKind = errors.New("add-stage: unknown insertion kind")

// stageNotYetStarted reports whether id "truly hasn't started yet" (§11.3): StatusPending alone is not
// enough — EvManualRetry also returns a FAILED stage to StatusPending, and the stage's StageGeneration (the
// Seq of its latest EvStartRun) tells the two apart. Reading status and generation here is a best-effort,
// UNLOCKED check for Preview/advisory purposes only — the AUTHORITATIVE recheck happens inside
// Store.AddStageToGraph, under the SAME lock as the write (Expect.TargetPending).
func (o *Orchestrator) stageNotYetStarted(id string) bool {
	return o.opts.Store.Get(id) == state.StatusPending && o.opts.Store.StageGeneration(id) == 0
}

// isStageFailed reports whether id is currently failed — this codebase has no separate "blocked" StageStatus
// (failBlockedStages transitions a pending stage straight to StatusFailed via EvBlockedByDep), so §11.4's
// "failed/blocked" dependency warning collapses to this one check.
func (o *Orchestrator) isStageFailed(id string) bool {
	return o.opts.Store.Get(id) == state.StatusFailed
}

// planDraft resolves req against the CURRENT registry — the one computation PreviewAddStage and
// CommitAddStage both call, so the two can never disagree about what a given draft means.
func (o *Orchestrator) planDraft(reg *graph.Registry, req AddStageDraft) (graph.InsertionPlan, error) {
	if req.ExpectedRevision != 0 && req.ExpectedRevision != reg.Revision() {
		return graph.InsertionPlan{}, &graph.ValidationError{Code: "stale_revision", Reason: "graph revision changed since this draft was prepared"}
	}
	switch req.Kind {
	case AddStageKindInsertBefore:
		if _, ok := reg.StageByID(req.TargetID); !ok {
			return graph.InsertionPlan{}, &graph.ValidationError{Code: graph.CodeTargetNotFound, Reason: fmt.Sprintf("target stage %q not found", req.TargetID)}
		}
		if !o.stageNotYetStarted(req.TargetID) {
			return graph.InsertionPlan{}, &graph.ValidationError{Code: "invalid_status", Reason: fmt.Sprintf("target stage %q has already started", req.TargetID)}
		}
		return reg.PlanInsertBefore(req.Stage, req.TargetID, o.IsCommandPrepared)
	case AddStageKindAppend:
		return reg.PlanAppend(req.Stage, req.ExplicitDeps, o.IsCommandPrepared, o.isStageFailed)
	default:
		return graph.InsertionPlan{}, fmt.Errorf("%w: %q", ErrUnknownInsertionKind, req.Kind)
	}
}

// PreviewAddStage validates req against the CURRENT registry and returns the resolved dependency diff —
// WITHOUT touching the journal or bumping the registry revision (TestPreview_NoMutation).
func (o *Orchestrator) PreviewAddStage(req AddStageDraft) (AddStagePreview, error) {
	if err := o.rejectIfReviewPaused(); err != nil {
		return AddStagePreview{}, err
	}
	reg := o.reg()
	plan, err := o.planDraft(reg, req)
	if err != nil {
		return AddStagePreview{}, err
	}
	return AddStagePreview{Plan: plan, GraphRevision: reg.Revision()}, nil
}

// AddStageResult is CommitAddStage's success result.
type AddStageResult struct {
	Stage         flow.Stage
	GraphRevision int64
}

// ErrRequestIDRequired is returned by CommitAddStage when req.RequestID is empty.
var ErrRequestIDRequired = errors.New("add-stage: request id is required")

// CommitAddStage is T9.4's atomic transaction: re-validate (revision + activation conditions) under
// Store.AddStageToGraph's lock, commit ONE durable record (definition + dependency + display order
// together), publish the new registry revision, then wake the scheduler through the EXISTING
// activatePrePlanned/startReadyStage paths. ctx is used only for the scheduler wake-up call (spawning the new
// stage's agent if it is immediately ready) — never held while any lock is taken. A repeat call with the SAME
// req.RequestID (TestCommit_DuplicateOperationIgnored) returns the original result without a second
// insertion; CommitAddStage still recomputes plan fresh every call, so the repeat's OWN structural shape is
// validated the same way, but Store.AddStageToGraph's idempotency index is what actually short-circuits the
// durable write.
func (o *Orchestrator) CommitAddStage(ctx context.Context, req AddStageDraft) (AddStageResult, error) {
	if err := o.rejectIfReviewPaused(); err != nil {
		return AddStageResult{}, err
	}
	if req.RequestID == "" {
		return AddStageResult{}, ErrRequestIDRequired
	}

	// Idempotent replay (TestCommit_DuplicateOperationIgnored): resolve directly from the durable record
	// instead of re-planning against a registry the ORIGINAL commit may have already mutated — re-planning
	// here would otherwise spuriously fail duplicate_stage_id on a legitimate retry of the exact same request
	// (e.g. a lost HTTP response).
	if prior, ok := o.opts.Store.DynamicStageByOperationID(req.RequestID); ok {
		return AddStageResult{Stage: prior.Stage, GraphRevision: o.reg().Revision()}, nil
	}

	// Advisory re-validation against the current in-memory registry — cheap, lock-free, and exactly the same
	// computation Preview used. The AUTHORITATIVE recheck (revision + target-pending) happens one line below,
	// inside AddStageToGraph, under pkg/state.Store's own lock — this call can still race and fail there, and
	// that is by design (TestCommit_SharedAdmissionSerialization). NoLockHeldDuringModelWorkOrPreflight: this
	// whole block (and everything above it) runs with no lock held at all.
	reg := o.reg()
	plan, err := o.planDraft(reg, req)
	if err != nil {
		return AddStageResult{}, err
	}

	if o.commitAddStageBarrier != nil {
		o.commitAddStageBarrier()
	}

	op := state.StageGraphOperation{
		ID: req.RequestID,
		Payload: state.StageGraphPayload{
			Stage:     plan.Stage,
			ExtraDeps: plan.ExtraDeps,
			Order:     plan.Order,
		},
		Expect: state.StageGraphExpect{
			GraphRevision:    reg.Revision(),
			RunNotFinalizing: true,
		},
	}
	if req.Kind == AddStageKindInsertBefore {
		op.Expect.TargetPending = req.TargetID
	}

	if _, err := o.opts.Store.AddStageToGraph(op); err != nil {
		return AddStageResult{}, err
	}

	// o.graph.Replace is safe here WITHOUT re-reading o.reg(): AddStageToGraph's own Expect.GraphRevision
	// check just proved reg (read above) is not stale relative to the durable commit that succeeded — no
	// OTHER successful commit could have landed in between (every successful commit strictly advances
	// len(DynamicStages), and this one's precondition check would have failed had that happened). See
	// graph_mutate.go's package doc comment / dev-plan's T9.4 notes for the full argument.
	//
	// The one exception is a true concurrent double-submit of the SAME RequestID racing past the idempotent-
	// replay check above on two goroutines at once: AddStageToGraph's own idempotency index (keyed by
	// RequestID) still lets only one of them durably commit, but both would otherwise reach this line. The
	// StageByID check below makes the loser's Replace a no-op instead of installing a redundant extra
	// revision with identical content.
	installed := o.reg()
	if _, exists := installed.StageByID(plan.Stage.ID); !exists {
		installed = o.graph.Replace(reg.WithPlan(plan).Stages())
	}

	// Wake the scheduler through the EXISTING activation paths (no bespoke spawn path,
	// TestCommit_NewStageStartsViaExistingPaths): a fresh agents:[auto] stage never needs planning, so this
	// alone starts it immediately if its dependencies are already done.
	o.tryActivatePrePlanned(o.runContext(ctx))

	return AddStageResult{Stage: plan.Stage, GraphRevision: installed.Revision()}, nil
}
