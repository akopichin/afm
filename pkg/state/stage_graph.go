package state

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
)

// DynamicStageAdded is the durable, replay-restored projection of ONE stage_graph_added control record
// (Этап 9, T9.4/T9.5): a new regular stage's full definition, plus whatever additional dependency it gives
// an EXISTING stage. Insert-before semantics (§11.3) never REMOVE a dependency from the target — they only
// ADD the new stage as one more dependency alongside whatever the target already depended on — so ExtraDeps
// is purely additive and this type never needs to express a removal.
type DynamicStageAdded struct {
	// OperationID is the idempotency key this addition was committed under (Store.AddStageToGraph's op.ID).
	OperationID string `json:"operation_id"`
	// Seq is the stage_graph_added control record's own journal sequence number — lets a caller order this
	// addition relative to stage FSM transitions (Store.History) without a second journal scan.
	Seq uint64 `json:"seq"`
	// Stage is the new stage's full definition, exactly as it will appear in the reconstructed registry.
	Stage flow.Stage `json:"stage"`
	// ExtraDeps maps an EXISTING stage id -> the dependency ids appended to its DependsOn by this addition
	// (today always exactly {targetID: [newStageID]} for insert-before, empty for append).
	ExtraDeps map[string][]string `json:"extra_deps,omitempty"`
}

// StageGraphPayload is stage_graph_added's Control.Payload shape — the single durable write that bundles the
// new stage's definition, the target's updated dependency, and the resulting stage display order together
// (T9.4's "ONE durable operation... не две отдельных journal writes").
type StageGraphPayload struct {
	Stage     flow.Stage          `json:"stage"`
	ExtraDeps map[string][]string `json:"extra_deps,omitempty"`
	// Order is the FULL StageOrder after this addition (frozen stages + every prior dynamic addition + this
	// one) — overwriting rs.StageOrder wholesale on replay is simpler and just as correct as diffing, since
	// this is the only thing that ever changes it after NewRunState.
	Order []string `json:"order"`
}

// StageGraphExpect are the preconditions Store.AddStageToGraph re-checks under s.mu IMMEDIATELY before
// commit — the same "precondition under the same lock as the write" discipline operation.go's
// checkPreconditionsLocked uses for assistants operations (T2.4), applied here to the stage graph instead.
type StageGraphExpect struct {
	// GraphRevision, if non-zero, must equal 1+len(current DynamicStages) — the runtime registry revision the
	// caller's preview was computed against (len(DynamicStages)+1 IS that revision — see DynamicStages' doc
	// comment). Mismatch -> stale_revision: another graph edit landed in between preview and commit.
	GraphRevision int64
	// TargetPending, if non-empty, names a stage that must currently be StatusPending AND have
	// StageGeneration()==0 — insert-before's "target truly hasn't started" condition (a bare status=pending
	// check alone is not enough: EvManualRetry also returns a FAILED stage to StatusPending, and eager_planning
	// can move a pending stage into StatusPlanning while its own generation is still 0).
	TargetPending string
	// RunNotFinalizing rejects with run_finalizing if the run has already started winding down — the same
	// signal operation.go's "Step 1" checks for assistants operations (o.finalizing/Store.SetRunFinalizing).
	RunNotFinalizing bool
}

// StageGraphOperation is Store.AddStageToGraph's caller-intent — the stage-graph analogue of Operation
// (operation.go), minus the generic Effects list: a stage-graph addition is always exactly one payload, so it
// needs no bundling mechanism of its own.
type StageGraphOperation struct {
	// ID is the idempotency key (T2.5's convention): the SAME ID with the SAME Payload repeated any number of
	// times (including after a restart) returns the original StageGraphResult; the same ID with DIFFERENT
	// content is rejected (ErrOperationIDConflict).
	ID      string
	Actor   Actor
	Expect  StageGraphExpect
	Payload StageGraphPayload
}

// StageGraphResult is what AddStageToGraph returns on success — including an idempotent replay, in which case
// Seq is the ORIGINAL commit's seq.
type StageGraphResult struct {
	OperationID string
	Seq         uint64
}

// AddStageToGraph durably commits one dynamically-added regular stage (Этап 9, T9.4). It takes the SAME s.mu
// as Store.Apply (stage FSM transitions) and Store.ApplyRunOperation (assistants operations) — not a second,
// independent lock — so a scheduler admission decision for op.Expect.TargetPending and this commit can never
// interleave: whichever of the two reaches s.mu first is strictly serialized ahead of the other
// (TestCommit_SharedAdmissionSerialization).
func (s *Store) AddStageToGraph(op StageGraphOperation) (StageGraphResult, error) {
	if op.ID == "" {
		return StageGraphResult{}, errors.New("state: stage graph operation id is required")
	}
	payload, err := json.Marshal(op.Payload)
	if err != nil {
		return StageGraphResult{}, fmt.Errorf("marshal stage graph payload: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.eventsLog == nil {
		return StageGraphResult{}, ErrStoreClosed
	}

	// T2.5 idempotency: checked FIRST, before any precondition — a repeat of an already-committed operation
	// returns the ORIGINAL outcome, never re-validated against preconditions that may have changed since.
	if seq, known := s.snapshot.Assistants.OperationIndex[op.ID]; known {
		res, rerr := s.reconcileIdempotentRepeatLocked(op.ID, seq, payload)
		return StageGraphResult(res), rerr
	}

	if op.Expect.RunNotFinalizing && s.runFinalizing {
		return StageGraphResult{}, &OperationRejectedError{Code: assistants.CodeRunFinalizing, Reason: "run is finalizing"}
	}
	if rev := op.Expect.GraphRevision; rev != 0 {
		if cur := int64(len(s.snapshot.DynamicStages)) + 1; cur != rev {
			return StageGraphResult{}, &OperationRejectedError{Code: assistants.CodeStaleRevision, Reason: "graph revision changed"}
		}
	}
	if tid := op.Expect.TargetPending; tid != "" {
		if st := s.snapshot.Stages[tid].Status; st != StatusPending {
			return StageGraphResult{}, &OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: fmt.Sprintf("stage %s already %s", tid, st)}
		}
		if gen := s.stageGenerationLocked(tid); gen != 0 {
			return StageGraphResult{}, &OperationRejectedError{Code: assistants.CodeStaleRevision, Reason: fmt.Sprintf("stage %s already started (generation %d)", tid, gen)}
		}
	}

	c := &Control{
		Kind:          kindStageGraphAdded,
		SchemaVersion: ControlSchemaVersion,
		OperationID:   op.ID,
		Actor:         op.Actor,
		Target:        Target{StageID: op.Payload.Stage.ID},
		Payload:       payload,
	}
	if err := s.appendControlLocked(c); err != nil {
		return StageGraphResult{}, err
	}
	s.opCache.put(op.ID, opCacheEntry{seq: c.Seq, payloadJSON: payload})
	return StageGraphResult{OperationID: op.ID, Seq: c.Seq}, nil
}

// DynamicStageByOperationID returns the DynamicStageAdded record committed under operationID, if any. This
// lets a caller (orchestrator.CommitAddStage, T9.4) resolve an idempotent-replay result directly from the
// durable record instead of re-deriving it from the original request against a registry the FIRST commit may
// have already mutated (which would otherwise spuriously fail duplicate_stage_id on a legitimate retry of the
// exact same request).
func (s *Store) DynamicStageByOperationID(operationID string) (DynamicStageAdded, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.snapshot.DynamicStages {
		if d.OperationID == operationID {
			return d, true
		}
	}
	return DynamicStageAdded{}, false
}

// applyStageGraphAdded projects one stage_graph_added control record onto rs — see dispatchControl's
// doc comment for why this is a top-level branch rather than a dispatchControlKind case. Like every other
// projection handler in this package, it fully validates before mutating anything, and mutates nothing at all
// when dryRun is true (validateControlForRunState's contract).
func applyStageGraphAdded(rs *RunState, c *Control, dryRun bool) error {
	var p StageGraphPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("decode stage_graph_added payload: %w", err)
	}
	if p.Stage.ID == "" {
		return errors.New("stage_graph_added: empty stage id")
	}
	if _, exists := rs.Stages[p.Stage.ID]; exists {
		return fmt.Errorf("stage_graph_added: stage %q already exists", p.Stage.ID)
	}
	if dryRun {
		return nil
	}
	if rs.Stages == nil {
		rs.Stages = map[string]StageState{}
	}
	rs.Stages[p.Stage.ID] = StageState{Status: StatusPending}
	rs.StageOrder = append([]string(nil), p.Order...)
	rs.DynamicStages = append(rs.DynamicStages, DynamicStageAdded{
		OperationID: c.OperationID,
		Seq:         c.Seq,
		Stage:       p.Stage,
		ExtraDeps:   p.ExtraDeps,
	})
	return nil
}
