package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
)

// decodePayload unmarshals raw into v, treating an absent/empty payload as "all zero values" rather than a
// decode error — a control record with no payload key at all (e.g. a minimal policy_set) is syntactically
// fine, just carrying zero values for whatever this kind's payload would have set.
func decodePayload(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, v)
}

// ErrUnknownTask — a control record addresses a task_id that does not exist in the projection (yet).
// Distinct from assistants.ErrMissingParent, which is about a PARENT reference, not the record's own
// target task.
var ErrUnknownTask = errors.New("assistants: unknown task_id")

// Hold is one independent reason a task is held from advancing: either the task was paused directly, or one
// of its ancestors was. Holds are keyed by an opaque owner string chosen by the caller (e.g. "manual",
// "ancestor:<id>") so distinct holds on the SAME task never collide or clobber each other — clearing one
// leaves the others in place.
type Hold struct {
	Reason string `json:"reason,omitempty"`
}

// TaskState is one node of the task tree projected from the control-record stream: assistants.Task (frozen
// identity/profile/mode fields) plus the orchestration-only state the journal restores on replay — holds and
// a saved continuation.
type TaskState struct {
	assistants.Task
	// Holds — independent reasons this task is currently held, keyed by owner. Paused reports len(Holds)>0;
	// EffectivelyHeld (AssistantsState) additionally walks the parent chain.
	Holds map[string]Hold `json:"holds,omitempty"`
	// Continuation — set when the task is waiting_children with a saved continuation to resume with. Its
	// mere presence does NOT imply a concurrency slot is held: pkg/state has no notion of slots at all
	// (those live in pkg/orchestrator/concurrency and are always empty right after a restart).
	Continuation *assistants.Continuation `json:"continuation,omitempty"`
	// LastVerdict (Этап 7, T7.2/T7.4/T7.7) is the Verdict from the most recently projected task_result control
	// record for this task — "" if none was ever recorded. Unlike the rest of the result (summary/findings/
	// evidence), the verdict alone is promoted into the durable, replay-restored projection because the
	// required-completion gate (AssistantsState.RequiredBlocker) and the Idle/Backoff classification
	// (state.go's isIdle) both need it WITHOUT file I/O, identically live and after a restart — see
	// applyTaskResult's doc comment for why the rest of Result still lives only in the control stream.
	LastVerdict assistants.Verdict `json:"last_verdict,omitempty"`
	// ResultSeq is the seq of the most recently projected task_result record (0 if none) — tells a result
	// recorded after a task_result wait was registered from an earlier one (MeshWait.ResultBaseline).
	ResultSeq uint64 `json:"result_seq,omitempty"`
	// Turn is the number of the task's latest started turn (0 before the first one, and in a journal written
	// before turns were recorded). TurnInbox is the inventory of that turn: the ids of the mesh messages handed
	// to it, kept while the turn has not finished so a retry after Pause or a crash is handed them again, and
	// cleared by the status change that commits the turn's outcome. Both come from task_status_changed
	// records (TaskStatusChangedPayload.Turn/TurnInbox).
	Turn      uint64                `json:"turn,omitempty"`
	TurnInbox []agentcomm.MessageID `json:"turn_inbox,omitempty"`
	// OpenRequests are the requests (not replies, not passive results) handed to earlier turns which ended
	// waiting (a yield, or a delegation) instead of answering: the request those turns were working on, still owed an answer once a later turn —
	// possibly woken only by a passive result — produces one. A turn that ends any other way clears them; its
	// finish answers them. Derived from the same task_status_changed records as TurnInbox, live and on replay.
	OpenRequests []agentcomm.MessageID `json:"open_requests,omitempty"`
	// TurnReadonlyEnforcement is the read-only guarantee the latest turn actually ran under — the environment's
	// at its start (TaskStatusChangedPayload.ReadonlyEnforcement), which can differ from the admission-time
	// ProfileSnapshot.ReadonlyEnforcement after a resume in another environment. "" before the first recorded
	// turn and in an older journal.
	TurnReadonlyEnforcement assistants.ReadonlyEnforcement `json:"turn_readonly_enforcement,omitempty"`
	// Waived/WaiverReason/WaivedBy/WaivedAt (Этап 7, T7.6) record a human's explicit decision to accept the
	// risk of this required task never closing its own gate — a SEPARATE, audited operation (kindTaskWaived),
	// never a side effect of cancel/retry/any task_result. Once Waived, RequiredBlocker treats this task's
	// gate as closed regardless of its actual Status/LastVerdict (see taskGateClosed, assistants_barrier.go).
	Waived       bool      `json:"waived,omitempty"`
	WaiverReason string    `json:"waiver_reason,omitempty"`
	WaivedBy     Actor     `json:"waived_by,omitempty"`
	WaivedAt     time.Time `json:"waived_at,omitempty"`
}

// Paused reports whether this task itself carries at least one hold (does not look at ancestors — see
// AssistantsState.EffectivelyHeld for that).
func (t TaskState) Paused() bool { return len(t.Holds) > 0 }

// AssistantsState is the normalized, replay-restored task-tree projection for one run. Tasks are held by
// reference (TaskID), with byRootStage/byParent indexes — a tree of pointers/ids, not nested structs, so
// reading a subtree never requires rebuilding it.
type AssistantsState struct {
	Tasks       map[assistants.TaskID]*TaskState          `json:"tasks,omitempty"`
	ByRootStage map[string][]assistants.TaskID            `json:"by_root_stage,omitempty"`
	ByParent    map[assistants.TaskID][]assistants.TaskID `json:"by_parent,omitempty"`
	// OperationIndex maps operation_id -> the seq of the control record that committed it. NEVER evicted —
	// it lives for the whole run and is restored by replaying the ENTIRE log, not just its tail (T2.5). This
	// is the exactly-once boundary for ApplyRunOperation; a separate, bounded cache of recent response
	// bodies (kept outside RunState, see operation.go) handles the fast path on top of it.
	OperationIndex map[string]uint64 `json:"operation_index,omitempty"`
	// Policy is the durably-recorded effective delegation-depth policy (T2.6) — recorded once before the
	// first admission, and again (as its own operation) whenever it changes on resume, so a restart never
	// re-derives it silently from config files alone.
	Policy PolicyState `json:"policy,omitempty"`
	// QuotaReserved is the whole-tree reservation total (T2.6): a budget belongs to the whole delegation
	// tree, not to any one level, so it is a single running total rather than per-depth counters.
	QuotaReserved int `json:"quota_reserved,omitempty"`
	// RootGenerations (T4a.6) counts how many times EACH root stage's whole task tree has been invalidated
	// (keyed by root stage id, absent/0 = never bumped). A task's own RootGeneration field is stamped ONCE
	// from this counter's CURRENT value at task_created time and never rewritten — see
	// kindRootGenerationBumped's doc comment in record.go.
	RootGenerations map[string]uint64 `json:"root_generations,omitempty"`
	// Discussions (T6.1) is the durable per-stage discussion log projection.
	Discussions DiscussionState `json:"discussions,omitempty"`
	// Proposals (T6.4) is the durable proposal board projection.
	Proposals ProposalBoard `json:"proposals,omitempty"`
	// FeedbackIntents (T6.5/T6.6) is the durable idempotent-delivery pipeline state, keyed by intent id.
	FeedbackIntents map[string]*FeedbackIntent `json:"feedback_intents,omitempty"`
	// Mesh (Agent Mesh Phase 1, T1.1-T1.4) is the durable projection of pkg/agentcomm's shared vocabulary:
	// participants, threads, messages+receipts, waits and the shared board. See mesh_projection.go/
	// mesh_waits.go/mesh_board.go.
	Mesh MeshState `json:"mesh,omitempty"`

	// admitting is true only on the scratch copy that validates a NEW record before it is written
	// (applyOperationCommitted with dryRun). Never persisted. See admittingNewRecord.
	admitting bool
}

// admittingNewRecord reports whether a handler is deciding admission of a record being written NOW, as opposed
// to applying it after a successful validation or replaying it from the journal. A rule added after records
// were already journaled (a key's sole ownership, a thread's own message budget) is enforced only here, so a
// journal written under the older, looser rule still replays.
func (as *AssistantsState) admittingNewRecord(dryRun bool) bool {
	return dryRun || as.admitting
}

// DiscussionState is the normalized, replay-restored discussion-log projection for one run (T6.1). Messages
// are held by reference (id), with a per-stage ordered id index — same "map + ordered id slice" shape as
// Tasks/ByRootStage, so order is always restored from the id list (append/commit order), never re-derived
// from any message's own timestamp.
type DiscussionState struct {
	Messages map[string]*assistants.DiscussionMessage `json:"messages,omitempty"`
	ByStage  map[string][]string                      `json:"by_stage,omitempty"`
}

// ProposalBoard is the normalized, replay-restored proposal-lifecycle projection for one run (T6.4).
type ProposalBoard struct {
	Proposals map[string]*assistants.Proposal `json:"proposals,omitempty"`
	ByStage   map[string][]string             `json:"by_stage,omitempty"`
}

// FeedbackIntentOrigin names who/what is behind one FeedbackIntent (T6.5) — carried all the way to the feed so
// a forwarded proposal is never rendered as if the human typed it directly (TestFeedUI_ShowsUserForwardedProposal).
type FeedbackIntentOrigin string

const (
	FeedbackOriginUser     FeedbackIntentOrigin = "user"
	FeedbackOriginProposal FeedbackIntentOrigin = "assistant_proposal"
)

// FeedbackIntentStatus is one state of the idempotent delivery pipeline (T6.6).
type FeedbackIntentStatus string

const (
	// FeedbackIntentAccepted — durably recorded, not yet delivered (or re-delivery is still pending because the
	// parent stage was not in an immediately-interruptible status).
	FeedbackIntentAccepted FeedbackIntentStatus = "accepted"
	// FeedbackIntentDelivered — the feedback was durably projected into the stage's real execution path
	// (feedback.md / pre-note) and, where applicable, the stage was signaled/spawned. This does NOT claim the
	// underlying issue was fixed — only that AFM committed the correction into the right path exactly once.
	FeedbackIntentDelivered FeedbackIntentStatus = "delivered"
	// FeedbackIntentRejected — delivery will never happen automatically (stage reached done/failed, or the
	// intent went stale before it could be delivered). Requires an explicit human action (Retry/new stage) to
	// reopen, never a silent reinterpretation.
	FeedbackIntentRejected FeedbackIntentStatus = "rejected"
)

// FeedbackIntent is the durable record of "this feedback must reach the stage's agent exactly once" (T6.5/T6.6).
type FeedbackIntent struct {
	ID       string               `json:"id"`
	StageID  string               `json:"stage_id"`
	Feedback string               `json:"feedback"`
	Origin   FeedbackIntentOrigin `json:"origin"`
	// ProposalID is set when Origin == FeedbackOriginProposal.
	ProposalID string `json:"proposal_id,omitempty"`
	// Participants names who stands behind this feedback for feed-provenance purposes (e.g. "@qaEngineer") —
	// never collapsed into a plain user message.
	Participants []string `json:"participants,omitempty"`
	// ExpectedStageGeneration is the stage attempt this intent was computed against — re-checked immediately
	// before every delivery attempt, not just once at accept time (TestDeferredIntent_RevalidatedBeforeExecution).
	ExpectedStageGeneration uint64               `json:"expected_stage_generation"`
	Status                  FeedbackIntentStatus `json:"status"`
	Reason                  string               `json:"reason,omitempty"`
	CreatedAt               time.Time            `json:"created_at"`
	ConsumedAt              time.Time            `json:"consumed_at,omitempty"`
}

// PolicyState is the durable record of the effective delegation-depth policy for this run (T2.6) — mirrors
// assistants.DepthPolicy's fields in a journal/snapshot-friendly shape.
type PolicyState struct {
	MaxDepth int    `json:"max_depth"`
	Source   string `json:"source"`
	Revision string `json:"revision"`
}

// newAssistantsState returns an AssistantsState with initialized (non-nil) maps.
func newAssistantsState() AssistantsState {
	return AssistantsState{
		Tasks:           map[assistants.TaskID]*TaskState{},
		ByRootStage:     map[string][]assistants.TaskID{},
		ByParent:        map[assistants.TaskID][]assistants.TaskID{},
		OperationIndex:  map[string]uint64{},
		RootGenerations: map[string]uint64{},
		Discussions: DiscussionState{
			Messages: map[string]*assistants.DiscussionMessage{},
			ByStage:  map[string][]string{},
		},
		Proposals: ProposalBoard{
			Proposals: map[string]*assistants.Proposal{},
			ByStage:   map[string][]string{},
		},
		FeedbackIntents: map[string]*FeedbackIntent{},
		Mesh:            newMeshState(),
	}
}

// Clone returns a shallow copy of as: the top-level maps and index slices are copied (mutating the clone's
// maps/slices never affects the original), but *TaskState values themselves are shared pointers — mirrors
// the existing Store.Snapshot() convention of copying one level deep (see StageNames via maps.Clone).
func (as AssistantsState) Clone() AssistantsState {
	out := newAssistantsState()
	for id, t := range as.Tasks {
		out.Tasks[id] = t
	}
	for stage, ids := range as.ByRootStage {
		out.ByRootStage[stage] = append([]assistants.TaskID(nil), ids...)
	}
	for parent, ids := range as.ByParent {
		out.ByParent[parent] = append([]assistants.TaskID(nil), ids...)
	}
	for op, seq := range as.OperationIndex {
		out.OperationIndex[op] = seq
	}
	for stage, gen := range as.RootGenerations {
		out.RootGenerations[stage] = gen
	}
	for id, m := range as.Discussions.Messages {
		out.Discussions.Messages[id] = m
	}
	for stage, ids := range as.Discussions.ByStage {
		out.Discussions.ByStage[stage] = append([]string(nil), ids...)
	}
	for id, p := range as.Proposals.Proposals {
		out.Proposals.Proposals[id] = p
	}
	for stage, ids := range as.Proposals.ByStage {
		out.Proposals.ByStage[stage] = append([]string(nil), ids...)
	}
	for id, fi := range as.FeedbackIntents {
		out.FeedbackIntents[id] = fi
	}
	out.Policy = as.Policy
	out.QuotaReserved = as.QuotaReserved
	out.Mesh = as.Mesh.clone()
	return out
}

// deepClone returns a FULL copy of as: every *TaskState is a new pointer with its own copy of Holds/
// Continuation, not shared with the original. Used only by applyOperationCommitted's transactional batch
// apply (T2.4) — unlike the shallow Clone() above (Store.Snapshot's external-read-only-copy contract), a
// multi-effect operation may need to mutate several tasks speculatively (including ones created earlier in
// the SAME batch) and throw the whole batch away if a later effect fails, which requires genuinely
// independent copies, not shared pointers.
func (as AssistantsState) deepClone() AssistantsState {
	out := as.Clone()
	for id, t := range as.Tasks {
		nt := *t
		if t.Holds != nil {
			nt.Holds = make(map[string]Hold, len(t.Holds))
			for k, v := range t.Holds {
				nt.Holds[k] = v
			}
		}
		if t.Continuation != nil {
			cont := *t.Continuation
			nt.Continuation = &cont
		}
		out.Tasks[id] = &nt
	}
	// Proposals/FeedbackIntents mutate Status/History/Reason IN PLACE (same convention as TaskState.Status
	// above) — a batched operation that touches one of these needs a genuinely independent copy too, not a
	// shared pointer. DiscussionMessage is immutable once created (no in-place mutation path exists), so
	// Clone()'s shallow pointer copy already suffices for it.
	for id, p := range as.Proposals.Proposals {
		np := *p
		np.History = append([]assistants.ProposalDecision(nil), p.History...)
		out.Proposals.Proposals[id] = &np
	}
	for id, fi := range as.FeedbackIntents {
		nfi := *fi
		out.FeedbackIntents[id] = &nfi
	}
	out.Mesh = as.Mesh.deepClone()
	return out
}

// applyOperationCommitted applies every Effect bundled into one ApplyRunOperation call (T2.4) as a single
// all-or-nothing batch: it mutates a deepClone() scratch copy effect-by-effect (so a LATER effect can see an
// EARLIER one's result within the same batch — e.g. creating a parent and its children in one operation),
// and only replaces the real *as with the scratch copy if every effect succeeded. If any effect fails, *as
// is left completely untouched — TestOperation_AllEffectsCommittedTogether's in-memory half (the durable
// half is that appendControlLocked never even reaches the journal write unless validateControlForRunState,
// which calls this with dryRun, already proved the whole batch succeeds).
func applyOperationCommitted(as *AssistantsState, c *Control, dryRun bool) error {
	var p operationCommittedPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("decode operation_committed payload: %w", err)
	}

	scratch := as.deepClone()
	scratch.admitting = dryRun
	for i, e := range p.Effects {
		if e.Kind == kindOperationCommitted {
			return fmt.Errorf("operation effect %d: nested operation_committed is not allowed", i)
		}
		// Actor is carried through from the outer operation_committed record (Этап 7, T7.6: applyTaskWaived is
		// the first per-effect handler that actually reads it — WaivedBy must record who durably accepted the
		// risk, not a zero-value Actor). Seq is carried through too (Agent Mesh T6.7 fix): every effect
		// committed in this batch shares the outer record's own durable seq — applyMeshMessageSent/
		// applyMeshBoardPublished stamp it onto the Message/BoardEntry they create, and ReadInbox/ReadThread/
		// ReadBoard page by comparing THAT stored Seq against a cursor. Leaving it zero (as before this fix)
		// made every mesh entity created via ApplyRunOperation — i.e. every one of them, since that is the
		// ONLY way the mesh ever creates them — silently indistinguishable from "already read" at the default
		// cursor=0, so ReadInbox/ReadThread always returned an empty page regardless of what was actually
		// delivered (TestSideAgent_CanAddressAndBeAddressed is what first exercised a real read-back and
		// caught it; no earlier test asserted on a Read call's actual message contents, only its error).
		synthetic := &Control{Kind: e.Kind, Seq: c.Seq, Time: c.Time, Actor: c.Actor, Target: e.Target, Payload: e.Payload}
		if err := dispatchControlKind(&scratch, synthetic, false); err != nil {
			return fmt.Errorf("operation effect %d (%s): %w", i, e.Kind, err)
		}
	}

	if dryRun {
		return nil
	}
	*as = scratch
	return nil
}

// PolicyBlocked reports whether task id is blocked by the CURRENT policy (its stored Depth exceeds the
// current Policy.MaxDepth) — a read-time computed view, never stored: lowering max_depth must never rewrite
// an existing task's Depth/ParentTaskID (T2.6's TestPolicy_LoweredLimitKeepsExistingTree). Returns false for
// an unknown task id (nothing to block).
func (as AssistantsState) PolicyBlocked(id assistants.TaskID) bool {
	t, ok := as.Tasks[id]
	if !ok {
		return false
	}
	return as.Policy.Blocks(t.Depth)
}

// ReadyContinuations returns, in deterministic (sorted) order, every task currently in waiting_children with
// a saved continuation whose children are ALL terminal — i.e. recovery should resume it. This is a pure
// read computed fresh from the current projection every time, not a persisted queue: calling it any number
// of times (including once per retry of a crashed recovery, or again after a restart) returns the same
// answer for the same state, so there is no separate "already queued" bit to get out of sync — "queue
// continuation once" falls out of determinism rather than needing its own bookkeeping (T2.7).
func (as AssistantsState) ReadyContinuations() []assistants.TaskID {
	ids := make([]string, 0, len(as.Tasks))
	for id := range as.Tasks {
		ids = append(ids, string(id))
	}
	slices.Sort(ids)

	var ready []assistants.TaskID
	for _, idStr := range ids {
		id := assistants.TaskID(idStr)
		t := as.Tasks[id]
		if t.Status != assistants.StatusWaitingChildren || t.Continuation == nil {
			continue
		}
		children := as.ByParent[id]
		if len(children) == 0 {
			continue
		}
		allTerminal := true
		for _, cid := range children {
			if c, ok := as.Tasks[cid]; !ok || !c.Status.Terminal() {
				allTerminal = false
				break
			}
		}
		if allTerminal {
			ready = append(ready, id)
		}
	}
	return ready
}

// EffectivelyHeld reports whether id is blocked from advancing right now: either it carries a hold itself,
// or any ancestor on its parent chain does. Returns the holds found (own first, then ancestors innermost
// first) for diagnostics; a task with no holds anywhere on the chain returns (false, nil).
func (as AssistantsState) EffectivelyHeld(id assistants.TaskID) (bool, []Hold) {
	var found []Hold
	cur, ok := as.Tasks[id]
	for ok {
		for _, h := range cur.Holds {
			found = append(found, h)
		}
		if cur.ParentTaskID == nil {
			break
		}
		cur, ok = as.Tasks[*cur.ParentTaskID]
	}
	return len(found) > 0, found
}

// ---- control-record payload shapes (T2.3) ----
//
// These three are exported (unlike the other, smaller payload shapes further down this file) because
// pkg/orchestrator's task-admission/lifecycle code (assistants.go's taskCreatedEffect/taskStatusEffect/
// taskHoldEffect) builds Effect.Payload values of exactly these shapes — it imports pkg/state already, so
// sharing the type here is a plain rename, not a new dependency, and keeps the json tags defined in exactly
// one place instead of two hand-kept-in-sync copies.

// TaskCreatedPayload is the task_created control record's payload — decoded here by applyTaskCreated,
// encoded by pkg/orchestrator's taskCreatedEffect.
type TaskCreatedPayload struct {
	ParentTaskID *string `json:"parent_task_id,omitempty"`
	// Scope (Agent Mesh, T6.1) — "stage" or "run"; omitted/empty means assistants.ScopeStage
	// (every task_created record written before T6.1 has no "scope" field at all, and must keep
	// decoding to stage-scoped with its original RootStageID — see applyTaskCreated below).
	Scope            string             `json:"scope,omitempty"`
	ProfileID        string             `json:"profile_id"`
	ProfileSnapshot  assistants.Profile `json:"profile_snapshot"`
	Title            string             `json:"title"`
	Instruction      string             `json:"instruction"`
	Mode             string             `json:"mode"`
	Trigger          string             `json:"trigger"`
	PolicyRev        string             `json:"policy_rev"`
	MaxDepthAtCreate int                `json:"max_depth_at_create"`
	CreatedBy        string             `json:"created_by"`
	// ParentGenerationAtCreate/RootGenerationAtCreate (T4a.6) freeze the parent task's TaskGeneration and the
	// root stage's RootGenerations counter at the moment this task was created — same "stamp once, never
	// recompute" convention as PolicyRev/MaxDepthAtCreate above. A task with no parent gets
	// ParentGenerationAtCreate 0 (meaningless/unused: a top-level task is never "stale relative to its
	// parent").
	ParentGenerationAtCreate uint64 `json:"parent_generation_at_create,omitempty"`
	RootGenerationAtCreate   uint64 `json:"root_generation_at_create,omitempty"`
}

// TaskStatusChangedPayload is the task_status_changed control record's payload — decoded here by
// applyTaskStatusChanged, encoded by pkg/orchestrator's taskStatusEffect.
type TaskStatusChangedPayload struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	// Turn and TurnInbox record a helper turn's inventory on the record kind that already marks a task's
	// progress, so a journal stays readable by an older binary (it ignores both). A "running" record with Turn
	// starts that turn, handed the messages TurnInbox lists; a record with any other status and the same Turn
	// ends it — the turn ran to its end and its outcome is committed with this record. A record without Turn
	// ends no turn.
	Turn      uint64   `json:"turn,omitempty"`
	TurnInbox []string `json:"turn_inbox,omitempty"`
	// ReadonlyEnforcement — on a turn start only: the read-only guarantee the environment gives the turn
	// ("os"/"none"), recorded because the admission snapshot no longer says it after a resume elsewhere.
	ReadonlyEnforcement string `json:"readonly_enforcement,omitempty"`
}

// TaskHoldPayload is the task_hold_set/task_hold_cleared control records' shared payload — decoded here by
// applyTaskHoldSet/applyTaskHoldCleared, encoded by pkg/orchestrator's taskHoldEffect.
type TaskHoldPayload struct {
	Owner  string `json:"owner"`
	Reason string `json:"reason,omitempty"`
}

// applyControlToRunState projects a single control record onto rs.Assistants, mutating it. Called from BOTH
// the live write path (Store.AppendControl, AFTER the durable write succeeds) and replay (parseEventLog) —
// see record.go's doc comment. A non-nil error means the control STREAM is inconsistent (cycle, missing
// parent, unknown task) — the caller (parseEventLog) treats it like a corrupted log line, not like
// ErrUnsupportedRecord: the record decoded fine, but what it asserts about the task tree is impossible.
//
// Every handler below fully validates before mutating anything (task_created validates the complete
// candidate tree via a read-only slice before ever touching the map), so a single call either mutates
// everything it needs to or mutates nothing at all. validateControlForRunState relies on exactly this
// property to let AppendControl check a record is admissible WITHOUT mutating the live snapshot, before the
// record ever reaches the durable journal — a record that fails projection must never be written, or every
// future replay of this run would fail forever.
func applyControlToRunState(rs *RunState, c *Control) error {
	return dispatchControl(rs, c, false)
}

// validateControlForRunState reports whether applyControlToRunState would succeed for c against rs's
// CURRENT projection, without mutating rs. Used by Store.AppendControl to reject an inadmissible record
// before it is ever written to events.jsonl.
func validateControlForRunState(rs *RunState, c *Control) error {
	return dispatchControl(rs, c, true)
}

func dispatchControl(rs *RunState, c *Control, dryRun bool) error {
	if rs.Assistants.Tasks == nil {
		rs.Assistants = newAssistantsState()
	}
	as := &rs.Assistants

	// kindStageGraphAdded (Этап 9, T9.4) mutates rs.Stages/rs.StageOrder/rs.DynamicStages directly — it has
	// nothing to do with the task-tree/policy/discussion domain dispatchControlKind below projects onto
	// AssistantsState, so it gets its own top-level branch instead of a case inside that switch.
	if c.Kind == kindStageGraphAdded {
		if err := applyStageGraphAdded(rs, c, dryRun); err != nil {
			return err
		}
	} else if err := dispatchControlKind(as, c, dryRun); err != nil {
		return err
	}
	if !dryRun {
		rs.resolveStageStatusWaits()
	}

	// Idempotency index (T2.5): ANY control record that carries an operation_id contributes to the
	// never-evicted index, regardless of kind. Restored by replaying the WHOLE log (every call site of
	// applyControlToRunState, live or replay), not just the tail.
	if !dryRun && c.OperationID != "" {
		as.OperationIndex[c.OperationID] = c.Seq
	}
	return nil
}

// dispatchControlKind applies a single kind's handler to as, WITHOUT touching the idempotency index — the
// index belongs to the outer record only (dispatchControl), not to each inner Effect of an
// operation_committed batch (applyOperationCommitted calls this directly, once per effect, with the SAME
// outer operation_id implicitly handled once at the top level).
func dispatchControlKind(as *AssistantsState, c *Control, dryRun bool) error {
	switch c.Kind {
	case kindTaskCreated:
		return applyTaskCreated(as, c, dryRun)
	case kindTaskStatusChanged:
		return applyTaskStatusChanged(as, c, dryRun)
	case kindTaskHoldSet:
		return applyTaskHoldSet(as, c, dryRun)
	case kindTaskHoldCleared:
		return applyTaskHoldCleared(as, c, dryRun)
	case kindTaskContinuationSaved:
		return applyTaskContinuationSaved(as, c, dryRun)
	case kindPolicySet:
		return applyPolicySet(as, c, dryRun)
	case kindQuotaReserved:
		return applyQuotaReserved(as, c, dryRun)
	case kindTaskResult:
		return applyTaskResult(as, c, dryRun)
	case kindTaskGenerationBumped:
		return applyTaskGenerationBumped(as, c, dryRun)
	case kindRootGenerationBumped:
		return applyRootGenerationBumped(as, c, dryRun)
	case kindOperationCommitted:
		return applyOperationCommitted(as, c, dryRun)
	case kindDiscussionMessage:
		return applyDiscussionMessage(as, c, dryRun)
	case kindProposalCreated:
		return applyProposalCreated(as, c, dryRun)
	case kindProposalStatusChanged:
		return applyProposalStatusChanged(as, c, dryRun)
	case kindFeedbackIntentCreated:
		return applyFeedbackIntentCreated(as, c, dryRun)
	case kindFeedbackIntentConsumed:
		return applyFeedbackIntentConsumed(as, c, dryRun)
	case kindTaskWaived:
		return applyTaskWaived(as, c, dryRun)
	case kindMeshParticipantAdded:
		return applyMeshParticipantAdded(as, c, dryRun)
	case kindMeshThreadOpened:
		return applyMeshThreadOpened(as, c, dryRun)
	case kindMeshThreadClosed:
		return applyMeshThreadClosed(as, c, dryRun)
	case kindMeshMessageSent:
		return applyMeshMessageSent(as, c, dryRun)
	case kindMeshMessageDelivered:
		return applyMeshMessageDelivered(as, c, dryRun)
	case kindMeshMessageAcked:
		return applyMeshMessageAcked(as, c, dryRun)
	case kindMeshMessageUndeliverable:
		return applyMeshMessageUndeliverable(as, c, dryRun)
	case kindMeshWaitRegistered:
		return applyMeshWaitRegistered(as, c, dryRun)
	case kindMeshWaitResolved:
		return applyMeshWaitResolved(as, c, dryRun)
	case kindMeshBoardPublished:
		return applyMeshBoardPublished(as, c, dryRun)
	default:
		// controlKinds already rejected anything not listed above during decodeLine — reaching here would
		// be a registry/switch drift, not a journal problem.
		return fmt.Errorf("assistants: no projection handler registered for control kind %q", c.Kind)
	}
}

func applyTaskCreated(as *AssistantsState, c *Control, dryRun bool) error {
	var p TaskCreatedPayload
	if len(c.Payload) > 0 {
		if err := decodePayload(c.Payload, &p); err != nil {
			return fmt.Errorf("task %s: decode task_created payload: %w", c.Target.TaskID, err)
		}
	}
	id := assistants.TaskID(c.Target.TaskID)
	if id == "" {
		return errors.New("task_created: empty target task_id")
	}
	if _, exists := as.Tasks[id]; exists {
		return fmt.Errorf("task %s: task_created for an id that already exists", id)
	}

	// Scope normalizes the empty/absent payload value to ScopeStage (Agent Mesh, T6.1) — this is
	// what makes a pre-T6.1 journal (no "scope" key at all) replay byte-identically: p.Scope decodes
	// to "", which means exactly the same thing as an explicit "stage". RootStageID is taken
	// verbatim from c.Target.StageID either way — empty for a ScopeRun task, unchanged for every
	// existing stage-scoped record.
	scope := assistants.Scope(p.Scope)
	if scope == "" {
		scope = assistants.ScopeStage
	}

	t := assistants.Task{
		ID:               id,
		Scope:            scope,
		RootStageID:      c.Target.StageID,
		ProfileID:        assistants.ProfileID(p.ProfileID),
		ProfileSnapshot:  p.ProfileSnapshot,
		Title:            p.Title,
		Instruction:      p.Instruction,
		Mode:             assistants.Mode(p.Mode),
		Trigger:          assistants.Trigger(p.Trigger),
		Status:           assistants.StatusWaitingParent,
		PolicyRev:        p.PolicyRev,
		MaxDepthAtCreate: p.MaxDepthAtCreate,
		CreatedBy:        assistants.Origin(p.CreatedBy),
		CreatedAt:        c.Time,
		ParentGeneration: p.ParentGenerationAtCreate,
		RootGeneration:   p.RootGenerationAtCreate,
	}
	if p.ParentTaskID != nil {
		pid := assistants.TaskID(*p.ParentTaskID)
		t.ParentTaskID = &pid
	}

	// Validate the WHOLE resulting tree against a READ-ONLY candidate slice — existing tasks plus this
	// tentative one — WITHOUT inserting it into the real map first. This is what lets
	// assistants.ValidateParent's self-reference check (parent.ID == child.ID) catch a genuine cycle: the
	// tentative child is already present in the candidate slice when its own declared parent is looked up by
	// id. A plain missing reference (parent never existed, including under any other id) surfaces as
	// ErrMissingParent instead — sequential task_created application can never construct a longer cycle,
	// since every declared parent must already exist in the committed set before the child referencing it
	// can be created. Crucially, nothing is mutated here even on success — this function is side-effect-free
	// up to this point, which is what makes dryRun safe.
	candidates := make([]assistants.Task, 0, len(as.Tasks)+1)
	for _, existing := range as.Tasks {
		candidates = append(candidates, existing.Task)
	}
	candidates = append(candidates, t)
	if err := assistants.ValidateTree(candidates, ""); err != nil {
		return fmt.Errorf("task %s: %w", id, err)
	}

	if dryRun {
		return nil
	}

	// Depth is COMPUTED from the parent chain, never trusted from payload (model-authored content is
	// untrusted, §T2.3).
	var parent *assistants.Task
	if t.ParentTaskID != nil {
		parent = &as.Tasks[*t.ParentTaskID].Task
	}
	t.Depth = assistants.ComputeDepth(parent)

	as.Tasks[id] = &TaskState{Task: t}
	as.ByRootStage[c.Target.StageID] = append(as.ByRootStage[c.Target.StageID], id)
	if t.ParentTaskID != nil {
		as.ByParent[*t.ParentTaskID] = append(as.ByParent[*t.ParentTaskID], id)
	}
	return nil
}

func applyTaskStatusChanged(as *AssistantsState, c *Control, dryRun bool) error {
	id := assistants.TaskID(c.Target.TaskID)
	t, ok := as.Tasks[id]
	if !ok {
		return fmt.Errorf("task %s: %w", id, ErrUnknownTask)
	}
	var p TaskStatusChangedPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("task %s: decode task_status_changed payload: %w", id, err)
	}
	if dryRun {
		return nil
	}
	status := assistants.TaskStatus(p.Status)
	t.Status = status
	t.StatusReason = p.Reason
	if status == assistants.StatusRunning && t.StartedAt.IsZero() {
		t.StartedAt = c.Time
	}
	if status.Terminal() {
		t.FinishedAt = c.Time
	}
	switch {
	case p.Turn == 0:
	case status == assistants.StatusRunning:
		t.Turn = p.Turn
		t.TurnReadonlyEnforcement = assistants.ReadonlyEnforcement(p.ReadonlyEnforcement)
		t.TurnInbox = nil
		for _, m := range p.TurnInbox {
			t.TurnInbox = append(t.TurnInbox, agentcomm.MessageID(m))
		}
	case p.Turn == t.Turn:
		t.OpenRequests = carriedRequests(as.Mesh, t, status)
		t.TurnInbox = nil
	default: // a stale turn number ends nothing
	}
	as.resolveTaskResultWaits(id)
	return nil
}

// carriedRequests is t's OpenRequests once its current turn ends in status: a turn that ends waiting for a
// colleague or for its children keeps them and adds the requests among the turn's own messages — neither
// passive results nor replies, which answer something rather than ask; any other end clears them. Always a new
// slice — a snapshot may share the task's current one.
func carriedRequests(ms MeshState, t *TaskState, status assistants.TaskStatus) []agentcomm.MessageID {
	if status != assistants.StatusWaitingPeer && status != assistants.StatusWaitingChildren {
		return nil
	}
	out := slices.Clone(t.OpenRequests)
	for _, id := range t.TurnInbox {
		if m := ms.Messages[id]; m != nil && !m.AutoResult && m.Kind != agentcomm.MessageKindReply && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func applyTaskHoldSet(as *AssistantsState, c *Control, dryRun bool) error {
	id := assistants.TaskID(c.Target.TaskID)
	t, ok := as.Tasks[id]
	if !ok {
		return fmt.Errorf("task %s: %w", id, ErrUnknownTask)
	}
	var p TaskHoldPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("task %s: decode task_hold_set payload: %w", id, err)
	}
	if p.Owner == "" {
		return fmt.Errorf("task %s: task_hold_set with empty owner", id)
	}
	if dryRun {
		return nil
	}
	if t.Holds == nil {
		t.Holds = map[string]Hold{}
	}
	t.Holds[p.Owner] = Hold{Reason: p.Reason}
	return nil
}

func applyTaskHoldCleared(as *AssistantsState, c *Control, dryRun bool) error {
	id := assistants.TaskID(c.Target.TaskID)
	t, ok := as.Tasks[id]
	if !ok {
		return fmt.Errorf("task %s: %w", id, ErrUnknownTask)
	}
	var p TaskHoldPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("task %s: decode task_hold_cleared payload: %w", id, err)
	}
	if dryRun {
		return nil
	}
	delete(t.Holds, p.Owner)
	return nil
}

// applyTaskResult validates a task_result effect's target task exists and its payload decodes as an
// assistants.Result. Unlike every other task_* kind it does not mutate the bulk of TaskState — the full
// result's content (summary/findings/evidence/...) has nowhere to live in the projection; it is carried
// durably only in the control stream itself, which Store.TaskEvents already returns verbatim per target
// task_id — exactly what the dashboard's per-task event-history page (GET /api/assistants/{id}/events) reads.
// It DOES project one field, TaskState.LastVerdict (Этап 7) — see that field's doc comment for why the
// verdict specifically needs a durable, file-I/O-free home.
func applyTaskResult(as *AssistantsState, c *Control, dryRun bool) error {
	id := assistants.TaskID(c.Target.TaskID)
	t, ok := as.Tasks[id]
	if !ok {
		return fmt.Errorf("task %s: %w", id, ErrUnknownTask)
	}
	var p assistants.Result
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("task %s: decode task_result payload: %w", id, err)
	}
	if dryRun {
		return nil
	}
	t.LastVerdict = p.Verdict
	t.ResultSeq = c.Seq
	as.resolveTaskResultWaits(id)
	return nil
}

// TaskWaivedPayload is the task_waived control record's payload (Этап 7, T7.6) — decoded here by
// applyTaskWaived, encoded by pkg/orchestrator's taskWaivedEffect.
type TaskWaivedPayload struct {
	Reason string `json:"reason"`
}

// applyTaskWaived projects a human's explicit decision to accept the risk of this required task never
// closing its own gate. Idempotent at the projection level (re-applying only refreshes Reason/At) — the
// actual "only once meaningfully" guarantee is WaiveTask's own early return for an already-waived task, same
// posture as every other task_* kind here.
func applyTaskWaived(as *AssistantsState, c *Control, dryRun bool) error {
	id := assistants.TaskID(c.Target.TaskID)
	t, ok := as.Tasks[id]
	if !ok {
		return fmt.Errorf("task %s: %w", id, ErrUnknownTask)
	}
	var p TaskWaivedPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("task %s: decode task_waived payload: %w", id, err)
	}
	if dryRun {
		return nil
	}
	t.Waived = true
	t.WaiverReason = p.Reason
	t.WaivedBy = c.Actor
	t.WaivedAt = c.Time
	return nil
}

type policySetPayload struct {
	MaxDepth int    `json:"max_depth"`
	Source   string `json:"source"`
	Revision string `json:"revision"`
}

// applyPolicySet projects a durable "the effective policy is now X" record (T2.6). Lowering max_depth never
// rewrites existing tasks' Depth/ParentTaskID — PolicyBlocked (computed on read, not stored) is what reports
// a task deeper than the new limit; see PolicyState.Blocks.
func applyPolicySet(as *AssistantsState, c *Control, dryRun bool) error {
	var p policySetPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("decode policy_set payload: %w", err)
	}
	if dryRun {
		return nil
	}
	as.Policy = PolicyState(p)
	return nil
}

// QuotaReservedPayload is the quota_reserved control record's payload — decoded here by applyQuotaReserved,
// encoded by pkg/orchestrator's quotaReservedEffect (T4a.2). Exported for the same reason
// TaskCreatedPayload/TaskStatusChangedPayload/TaskHoldPayload above are: pkg/orchestrator builds this shape
// too (a delegation group's admission reserves quota atomically with creating its children), and sharing the
// type keeps the json tag defined in exactly one place.
type QuotaReservedPayload struct {
	Amount int `json:"amount"`
}

// applyQuotaReserved projects a durable quota reservation. The budget is whole-tree (QuotaReserved is a
// single running total across the whole run), not reset per delegation level — T2.6's
// TestQuota_NotResetPerDelegation.
func applyQuotaReserved(as *AssistantsState, c *Control, dryRun bool) error {
	var p QuotaReservedPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("decode quota_reserved payload: %w", err)
	}
	if dryRun {
		return nil
	}
	as.QuotaReserved += p.Amount
	return nil
}

// Blocks reports whether a task at the given depth is blocked by the CURRENT policy (depth exceeds
// MaxDepth) — used to mark existing deep tasks policy_blocked after a lowered limit, without touching their
// stored Depth/ParentTaskID (TestPolicy_LoweredLimitKeepsExistingTree).
func (p PolicyState) Blocks(depth int) bool { return depth > p.MaxDepth }

// applyTaskGenerationBumped increments taskID's own TaskGeneration by one (T4a.6) — RetryTask's effect.
// Existing descendants' ParentGeneration (stamped at THEIR creation time, never rewritten) is what becomes
// stale relative to this new value; this function itself never touches any other task.
func applyTaskGenerationBumped(as *AssistantsState, c *Control, dryRun bool) error {
	id := assistants.TaskID(c.Target.TaskID)
	t, ok := as.Tasks[id]
	if !ok {
		return fmt.Errorf("task %s: %w", id, ErrUnknownTask)
	}
	if dryRun {
		return nil
	}
	t.TaskGeneration++
	return nil
}

// applyRootGenerationBumped increments one root stage's whole-tree RootGenerations counter by one (T4a.6).
// Existing tasks' RootGeneration (stamped at task_created time) is left untouched — exactly like
// applyPolicySet's lowered max_depth, invalidation is a READ-time comparison (a task's own RootGeneration
// vs. this counter's CURRENT value), never a rewrite of history.
func applyRootGenerationBumped(as *AssistantsState, c *Control, dryRun bool) error {
	if c.Target.StageID == "" {
		return errors.New("root_generation_bumped: empty target stage_id")
	}
	if dryRun {
		return nil
	}
	if as.RootGenerations == nil {
		as.RootGenerations = map[string]uint64{}
	}
	as.RootGenerations[c.Target.StageID]++
	return nil
}

func applyTaskContinuationSaved(as *AssistantsState, c *Control, dryRun bool) error {
	id := assistants.TaskID(c.Target.TaskID)
	t, ok := as.Tasks[id]
	if !ok {
		return fmt.Errorf("task %s: %w", id, ErrUnknownTask)
	}
	if len(c.Payload) == 0 {
		if dryRun {
			return nil
		}
		t.Continuation = nil
		return nil
	}
	var cont assistants.Continuation
	if err := decodePayload(c.Payload, &cont); err != nil {
		return fmt.Errorf("task %s: decode task_continuation_saved payload: %w", id, err)
	}
	if dryRun {
		return nil
	}
	t.Continuation = &cont
	return nil
}
