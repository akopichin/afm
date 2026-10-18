package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
)

// OperationRejectedError is a typed, non-storage rejection of an Operation: the write never reached the
// journal at all (as opposed to StorageError, where it did and the write/fsync itself failed). Code reuses
// assistants.ErrorCode so the same machine-readable strings already defined for the collaboration HTTP API
// (pkg/assistants/errors.go) flow straight through from the storage layer.
type OperationRejectedError struct {
	Code   assistants.ErrorCode
	Reason string
	// Cause — an optional sentinel this rejection stands for, so callers can still errors.Is it.
	Cause error
}

func (e *OperationRejectedError) Error() string {
	return fmt.Sprintf("operation rejected (%s): %s", e.Code, e.Reason)
}

func (e *OperationRejectedError) Unwrap() error { return e.Cause }

// ErrOperationIDConflict is wrapped by OperationRejectedError{Code: CodeInvalidStatus} — kept as a sentinel
// so callers can errors.Is it without string-matching Reason. Reusing CodeInvalidStatus rather than
// inventing a new ErrorCode: pkg/assistants/errors.go's catalog is the single source of machine-readable
// rejection codes for this feature, and "the same operation_id was already used for something else" is, from
// the caller's point of view, a request that is not valid to retry as given — the same shape of problem
// CodeInvalidStatus already names.
var ErrOperationIDConflict = errors.New("assistants: operation_id already used with different content")

// OperationIDConflictError is the typed rejection for a request id that was already used with different
// content — what both the store's idempotency check and a caller-level replay check (e.g. the agent send's
// own replay of an already-committed message) return, so the API answers 409, not 500.
func OperationIDConflictError(opID string) error {
	return &OperationRejectedError{
		Code:   assistants.CodeInvalidStatus,
		Reason: fmt.Sprintf("request id %s was already used for a different request — retry with a new request id", opID),
		Cause:  ErrOperationIDConflict,
	}
}

// Effect is one state mutation bundled into an Operation. Reuses the SAME per-kind payload shapes as
// standalone control records (task_created, task_status_changed, policy_set, quota_reserved, ...) — see
// controlKinds in record.go and dispatchControlKind in assistants_projection.go.
type Effect struct {
	Kind    string          `json:"kind"`
	Target  Target          `json:"target"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// operationCommittedPayload is the Control.Payload shape for the "operation_committed" wrapper kind: the
// caller's semantic op.Kind plus every bundled Effect, applied together (applyOperationCommitted).
type operationCommittedPayload struct {
	OpKind  string   `json:"op_kind"`
	Effects []Effect `json:"effects"`
}

// Preconditions are checked against the CURRENT snapshot under the SAME lock as the write (T2.4 step 2) —
// never against a read taken earlier. Zero-value fields mean "no constraint".
type Preconditions struct {
	// PolicyRevision, if non-empty, must match the run's current effective policy revision
	// (rs.Assistants.Policy.Revision) — stale_revision otherwise.
	PolicyRevision string
	// TaskStatus, if non-empty, maps taskID -> the status that task is expected to currently hold —
	// stale_revision on any mismatch or missing task. Optimistic-concurrency guard for operations that act
	// on an existing task (e.g. "only pause it if it's still running").
	TaskStatus map[string]assistants.TaskStatus
	// MaxQuota, if non-nil, is the whole-tree quota ceiling this operation's own quota_reserved effects (if
	// any) must not push the running total past — budget_exceeded otherwise. See T2.6's QuotaReserved.
	MaxQuota *int
	// ProposalStatus, if non-empty, maps proposalID -> the assistants.ProposalStatus that proposal is expected
	// to currently hold — stale_revision otherwise. Optimistic-concurrency guard for AcceptProposal/
	// DismissProposal (T6.6/T6.7): the SAME precondition-under-lock discipline as TaskStatus above is what
	// makes a double-click/two-tabs race on the same proposal resolve to exactly one winner.
	ProposalStatus map[string]assistants.ProposalStatus
	// StageGeneration, if non-empty, maps stageID -> the Store.StageGeneration that stage is expected to
	// currently be at — stale_revision otherwise (T6.4/T6.5: "ожидаемые stage_generation"). Checked against
	// the store's OWN history under the SAME lock as the write, never a value read earlier.
	StageGeneration map[string]uint64
	// StageNotTerminal, if non-empty, lists stage ids that must NOT currently be done/failed — invalid_status
	// otherwise. Этап 7 (T7.3): closes the TOCTOU window between an early, non-authoritative status read (e.g.
	// Attach's own convenience check) and the actual commit — admitting a new required task AFTER its root
	// stage has already reached done/failed would create a requirement nothing will ever re-evaluate again
	// (TestAttachRequiredRacesWithComplete). Checked under the SAME lock as the write, like every other
	// precondition here.
	StageNotTerminal []string
	// Check, if non-nil, is a caller-supplied admission check run against the CURRENT projection under the
	// SAME lock as the write — for a rule that needs the caller's own knowledge of the run (e.g. whether a
	// message recipient has finished) but must not be decided from an earlier snapshot. It must only read rs.
	// Like every precondition, it is skipped for an idempotent repeat of an already-committed operation.
	Check func(rs *RunState) error
}

// Operation is a single caller-intent: ApplyRunOperation either durably commits ALL of its Effects as one
// Control record, or none of them — see dev-plan.md T2.4's 7-step ordering.
type Operation struct {
	// ID is the idempotency key (T2.5) — required. The SAME ID with the SAME Effects repeated any number of
	// times (including after a restart) returns the original OperationResult without creating anything new;
	// the same ID with DIFFERENT content is rejected.
	ID      string
	Kind    string
	Actor   Actor
	Expect  Preconditions
	Effects []Effect
}

// OperationResult is what ApplyRunOperation returns on success — including when the call is a replay of an
// already-committed operation_id (T2.5), in which case Seq is the ORIGINAL commit's seq, not a new one.
// commitTime — the one form of a commit's timestamp in an OperationResult: UTC, without the monotonic clock
// reading, exactly as it reads back from the journal — so a result is equal live, from cache and after a
// restart, whatever the machine's local time zone.
func commitTime(t time.Time) time.Time { return t.UTC().Round(0) }

type OperationResult struct {
	OperationID string
	Seq         uint64
	// Time is the commit's own timestamp (the record's Time) — also the ORIGINAL one on a replay.
	Time time.Time
}

// ApplyRunOperation is the single mutation path for new (assistants/task) state (§6.3, dev-plan T2.4). The
// durable commit (steps 1-6) happens entirely under s.mu; externalWork — if non-nil — runs strictly AFTER
// the lock is released (step 7: spawning an agent or any other external side effect must never happen while
// the lock is held, since it may block for an arbitrary time). externalWork is only invoked when the
// operation was actually admitted (including an idempotent replay of a prior success); it is never called
// for a rejected operation.
func (s *Store) ApplyRunOperation(op Operation, externalWork func(OperationResult)) (OperationResult, error) {
	res, err := s.applyRunOperationLocked(op)
	if err != nil {
		return OperationResult{}, err
	}
	if externalWork != nil {
		externalWork(res)
	}
	return res, nil
}

func (s *Store) applyRunOperationLocked(op Operation) (OperationResult, error) {
	if op.ID == "" {
		return OperationResult{}, errors.New("assistants: operation id is required")
	}

	payload, err := json.Marshal(operationCommittedPayload{OpKind: op.Kind, Effects: op.Effects})
	if err != nil {
		return OperationResult{}, fmt.Errorf("marshal operation effects: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.eventsLog == nil {
		return OperationResult{}, ErrStoreClosed
	}

	// T2.5 idempotency: the never-evicted operationIndex is checked FIRST, before any lifecycle/precondition
	// check — a repeat of an already-committed operation returns the ORIGINAL outcome, it is not re-validated
	// against preconditions that may have changed since (that would make a successful repeat retroactively
	// fail, which defeats the point of idempotency).
	if seq, known := s.snapshot.Assistants.OperationIndex[op.ID]; known {
		return s.reconcileIdempotentRepeatLocked(op.ID, seq, payload)
	}

	// Step 1: run lifecycle not finalizing. (Flow-wide pause is a stage-FSM concept with no equivalent here
	// yet — pkg/state has no notion of "the whole flow is paused" independent of individual stage statuses;
	// a later stage wires that precondition in via Expect once it exists.)
	if s.runFinalizing {
		return OperationResult{}, &OperationRejectedError{Code: assistants.CodeRunFinalizing, Reason: "run is finalizing"}
	}

	// T2.6: no task may be created before SOME policy has been durably recorded — depth/MaxDepthAtCreate
	// only mean anything relative to a recorded policy. This gate lives here (the one production admission
	// path), not in the raw projection (applyTaskCreated/AppendControl stay usable by lower-level callers,
	// including recording the very first policy_set itself, without needing a policy to already exist).
	if requiresPolicy(op.Effects) && s.snapshot.Assistants.Policy.Revision == "" {
		return OperationResult{}, fmt.Errorf("%w: operation %s", ErrPolicyNotRecorded, op.ID)
	}

	// Step 2: check Expect against the snapshot, UNDER LOCK.
	if err := s.checkPreconditionsLocked(s.snapshot, op); err != nil {
		return OperationResult{}, err
	}

	// Step 3: build ONE Control record with ALL effects.
	c := &Control{
		Kind:          kindOperationCommitted,
		SchemaVersion: ControlSchemaVersion,
		OperationID:   op.ID,
		Actor:         op.Actor,
		Payload:       payload,
	}

	// Steps 4-5: write+fsync, then update projections — appendControlLocked validates (dry-run) before ever
	// touching the journal, exactly like AppendControl, so a batch that would fail projection never gets
	// durably written at all.
	if err := s.appendControlLocked(c); err != nil {
		return OperationResult{}, err
	}

	// Round(0) drops the monotonic clock reading: a replay (decoded from the journal) carries none, and the
	// result must be identical either way.
	res := OperationResult{OperationID: op.ID, Seq: c.Seq, Time: commitTime(c.Time)}
	s.opCache.put(op.ID, opCacheEntry{seq: c.Seq, time: res.Time, payloadJSON: payload})

	// Step 6: publish a notice. pkg/state has no notice bus (that lives in pkg/orchestrator) — left as the
	// caller's responsibility; ApplyRunOperation's return value (and the durable record itself) is what a
	// pkg/orchestrator wrapper observes to publish one.
	return res, nil
}

// checkPreconditionsLocked validates op.Expect against rs's CURRENT projection and, for MaxQuota, against
// this SAME operation's own quota_reserved effects (so the ceiling check and the reservation are atomic under
// one lock acquisition — see TestQuota_ReservedAtomicallyForChildGroup). Method on *Store (not a free
// function) ONLY so StageGeneration's check can reach s.stageGenerationLocked without re-acquiring s.mu —
// caller MUST already hold s.mu.
func (s *Store) checkPreconditionsLocked(rs *RunState, op Operation) error {
	exp := op.Expect
	if exp.PolicyRevision != "" && rs.Assistants.Policy.Revision != exp.PolicyRevision {
		return &OperationRejectedError{Code: assistants.CodeStaleRevision, Reason: "policy revision changed"}
	}
	for id, want := range exp.TaskStatus {
		t, ok := rs.Assistants.Tasks[assistants.TaskID(id)]
		if !ok || t.Status != want {
			return &OperationRejectedError{Code: assistants.CodeStaleRevision, Reason: fmt.Sprintf("task %s status changed", id)}
		}
	}
	for id, want := range exp.ProposalStatus {
		p, ok := rs.Assistants.Proposals.Proposals[id]
		if !ok || p.Status != want {
			return &OperationRejectedError{Code: assistants.CodeStaleRevision, Reason: fmt.Sprintf("proposal %s status changed", id)}
		}
	}
	for stageID, want := range exp.StageGeneration {
		if got := s.stageGenerationLocked(stageID); got != want {
			return &OperationRejectedError{Code: assistants.CodeStaleRevision, Reason: fmt.Sprintf("stage %s generation changed (expected %d, got %d)", stageID, want, got)}
		}
	}
	for _, stageID := range exp.StageNotTerminal {
		if st := rs.Stages[stageID].Status; st == StatusDone || st == StatusFailed {
			return &OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: fmt.Sprintf("stage %s already %s", stageID, st)}
		}
	}
	if exp.MaxQuota != nil {
		requested := 0
		for _, e := range op.Effects {
			if e.Kind != kindQuotaReserved {
				continue
			}
			var p QuotaReservedPayload
			if err := decodePayload(e.Payload, &p); err != nil {
				return fmt.Errorf("decode quota_reserved effect: %w", err)
			}
			requested += p.Amount
		}
		if rs.Assistants.QuotaReserved+requested > *exp.MaxQuota {
			return &OperationRejectedError{Code: assistants.CodeBudgetExceeded, Reason: fmt.Sprintf("quota %d + %d exceeds max %d", rs.Assistants.QuotaReserved, requested, *exp.MaxQuota)}
		}
	}
	if exp.Check != nil {
		return exp.Check(rs)
	}
	return nil
}

// reconcileIdempotentRepeatLocked handles a request whose operation_id is already in operationIndex. Caller
// holds s.mu. Fast path: the bounded opCache. Slow path (cache miss, index hit): a direct single read of the
// journal at the known seq — no scan of the rest of the log.
func (s *Store) reconcileIdempotentRepeatLocked(opID string, seq uint64, newPayload []byte) (OperationResult, error) {
	if e, ok := s.opCache.get(opID); ok {
		if !bytes.Equal(e.payloadJSON, newPayload) {
			return OperationResult{}, OperationIDConflictError(opID)
		}
		return OperationResult{OperationID: opID, Seq: e.seq, Time: e.time}, nil
	}

	c, err := s.readControlAtSeq(seq)
	if err != nil {
		return OperationResult{}, &StorageError{Inner: fmt.Errorf("re-read committed operation %s at seq %d: %w", opID, seq, err)}
	}
	if !bytes.Equal(c.Payload, newPayload) {
		return OperationResult{}, OperationIDConflictError(opID)
	}
	// Repopulate the cache so a THIRD repeat of the same operation_id hits the fast path again.
	s.opCache.put(opID, opCacheEntry{seq: seq, time: commitTime(c.Time), payloadJSON: c.Payload})
	return OperationResult{OperationID: opID, Seq: seq, Time: commitTime(c.Time)}, nil
}

// CommittedOperation is an already committed operation exactly as its journal record holds it — what a retry
// of the same request id is answered from, never from the current projection, which may have moved on.
type CommittedOperation struct {
	Result OperationResult
	// Kind is the committed operation's kind (Operation.Kind); "" for a legacy single control record, which
	// carries no operation kind.
	Kind    string
	Effects []Effect
}

// Effect decodes the first effect of kind into payload and returns its target; ok=false when the operation
// has no such effect.
func (c CommittedOperation) Effect(kind string, payload any) (target Target, ok bool, err error) {
	for _, e := range c.Effects {
		if e.Kind != kind {
			continue
		}
		if err := decodePayload(e.Payload, payload); err != nil {
			return Target{}, false, fmt.Errorf("decode committed %s: %w", kind, err)
		}
		return e.Target, true, nil
	}
	return Target{}, false, nil
}

// CommittedOperation returns the operation committed under opID; ok=false when none was. kind is the kind of
// operation the caller retries: an operation of another kind committed under the same id is a request id
// conflict (ErrOperationIDConflict), even when its effects look alike. It reads the bounded cache or, on a
// miss, the one journal record at the operation's seq.
func (s *Store) CommittedOperation(opID, kind string) (CommittedOperation, bool, error) {
	op, ok, err := s.readCommittedOperation(opID)
	if err != nil || !ok {
		return op, ok, err
	}
	if op.Kind != "" && op.Kind != kind {
		return CommittedOperation{}, true, OperationIDConflictError(opID)
	}
	return op, true, nil
}

func (s *Store) readCommittedOperation(opID string) (CommittedOperation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, known := s.snapshot.Assistants.OperationIndex[opID]
	if !known {
		return CommittedOperation{}, false, nil
	}
	if e, ok := s.opCache.get(opID); ok {
		var p operationCommittedPayload
		if err := json.Unmarshal(e.payloadJSON, &p); err != nil {
			return CommittedOperation{}, false, fmt.Errorf("decode committed operation %s: %w", opID, err)
		}
		return CommittedOperation{Result: OperationResult{OperationID: opID, Seq: e.seq, Time: e.time}, Kind: p.OpKind, Effects: p.Effects}, true, nil
	}
	c, err := s.readControlAtSeq(seq)
	if err != nil {
		return CommittedOperation{}, false, &StorageError{Inner: fmt.Errorf("re-read committed operation %s at seq %d: %w", opID, seq, err)}
	}
	op := CommittedOperation{Result: OperationResult{OperationID: opID, Seq: seq, Time: commitTime(c.Time)}}
	if c.Kind != kindOperationCommitted {
		op.Effects = []Effect{{Kind: c.Kind, Target: c.Target, Payload: c.Payload}}
		return op, true, nil
	}
	var p operationCommittedPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return CommittedOperation{}, false, fmt.Errorf("decode committed operation %s: %w", opID, err)
	}
	op.Kind, op.Effects = p.OpKind, p.Effects
	return op, true, nil
}

// ---- T2.5: bounded response-body cache ----
//
// operationIndex (AssistantsState.OperationIndex) is the UNBOUNDED, never-evicted half of the idempotency
// boundary — see its doc comment and dev-plan.md T2.5's "Почему не кольцо ключей" for why a bounded ring of
// KEYS was rejected. opResultCache below is the bounded half: a small cache of recent response BODIES, pure
// optimization on top of the always-correct index+journal-read path.

const defaultOpResultCacheCapacity = 1024

// opResultCacheCapacityOverride lets tests configure a tiny capacity so eviction is fast and deterministic
// without depending on (or hardcoding assumptions about) the production constant. Set it before Open(); each
// Store captures the value at construction time (see newOpResultCache's call site).
var opResultCacheCapacityOverride int

// SetOperationResultCacheCapacityForTest overrides the capacity used by Stores opened AFTER this call.
// Production code MUST NOT call this. n<=0 restores the default.
func SetOperationResultCacheCapacityForTest(n int) {
	opResultCacheCapacityOverride = n
}

func opResultCacheCapacity() int {
	if opResultCacheCapacityOverride > 0 {
		return opResultCacheCapacityOverride
	}
	return defaultOpResultCacheCapacity
}

type opCacheEntry struct {
	seq         uint64
	time        time.Time
	payloadJSON []byte
}

// opResultCache is a small, evicting (oldest-first) cache of recent operation results keyed by operation_id.
// Guarded by its own mutex (not s.mu) since the fast-path get/put is also useful for future read-only
// callers that don't want to take the store-wide lock — today it is only ever used from under s.mu, but
// there is no reason to couple it to that lock.
type opResultCache struct {
	mu       sync.Mutex
	capacity int
	order    []string // oldest first
	entries  map[string]opCacheEntry
}

func newOpResultCache(capacity int) *opResultCache {
	if capacity <= 0 {
		capacity = defaultOpResultCacheCapacity
	}
	return &opResultCache{capacity: capacity, entries: map[string]opCacheEntry{}}
}

func (c *opResultCache) get(id string) (opCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	return e, ok
}

func (c *opResultCache) put(id string, e opCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[id]; !exists {
		c.order = append(c.order, id)
		if len(c.order) > c.capacity {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.entries, oldest)
		}
	}
	c.entries[id] = e
}
