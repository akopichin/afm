package state

import (
	"errors"
	"fmt"
	"slices"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
)

// MeshState is the normalized, replay-restored Agent Mesh projection for one run (Agent Mesh Phase 1,
// T1.1-T1.4): participants, threads, messages+receipts, waits (mesh_waits.go) and the shared board
// (mesh_board.go). Entities are held by reference (id -> *value) with ordered id indexes — the same
// "map + ordered id slice" shape AssistantsState's other sub-projections already use (DiscussionState,
// ProposalBoard): order is always restored from the id list (append/commit order), never re-derived from any
// record's own timestamp.
type MeshState struct {
	Participants     map[agentcomm.ParticipantID]*agentcomm.Participant `json:"participants,omitempty"`
	ParticipantOrder []agentcomm.ParticipantID                          `json:"participant_order,omitempty"`

	Threads     map[agentcomm.ThreadID]*agentcomm.Thread `json:"threads,omitempty"`
	ThreadOrder []agentcomm.ThreadID                     `json:"thread_order,omitempty"`

	Messages map[agentcomm.MessageID]*agentcomm.Message `json:"messages,omitempty"`
	// MessagesByThread preserves each thread's commit order — the same role Discussions.ByStage plays for
	// discussion messages.
	MessagesByThread map[agentcomm.ThreadID][]agentcomm.MessageID `json:"messages_by_thread,omitempty"`
	// Inbox maps a recipient to the ordered ids of every message ever addressed to it (read AND unread —
	// Receipts is what tells a reader which of them are still unread).
	Inbox map[agentcomm.ParticipantID][]agentcomm.MessageID `json:"inbox,omitempty"`
	// Receipts maps messageID -> recipientID -> that recipient's current DeliveryStatus. "answered" is
	// deliberately never a value stored here — see MessageAnswered's doc comment.
	Receipts map[agentcomm.MessageID]map[agentcomm.ParticipantID]agentcomm.DeliveryStatus `json:"receipts,omitempty"`

	Waits     map[agentcomm.WaitID]*MeshWait `json:"waits,omitempty"`
	WaitOrder []agentcomm.WaitID             `json:"wait_order,omitempty"`

	Board map[string]*MeshBoardEntry `json:"board,omitempty"`
}

func newMeshState() MeshState {
	return MeshState{
		Participants:     map[agentcomm.ParticipantID]*agentcomm.Participant{},
		Threads:          map[agentcomm.ThreadID]*agentcomm.Thread{},
		Messages:         map[agentcomm.MessageID]*agentcomm.Message{},
		MessagesByThread: map[agentcomm.ThreadID][]agentcomm.MessageID{},
		Inbox:            map[agentcomm.ParticipantID][]agentcomm.MessageID{},
		Receipts:         map[agentcomm.MessageID]map[agentcomm.ParticipantID]agentcomm.DeliveryStatus{},
		Waits:            map[agentcomm.WaitID]*MeshWait{},
		Board:            map[string]*MeshBoardEntry{},
	}
}

// clone returns a ONE-LEVEL-DEEP copy of ms: every top-level map/slice is independent (mutating the clone's
// maps/slices never affects the original), but the VALUES (participant/thread/message/wait/board-entry
// pointers, and each message's receipt sub-map) are shared — exactly the convention
// AssistantsState.Clone documents for Tasks/Holds, used for Store.Snapshot()'s external read-only copies.
func (ms MeshState) clone() MeshState {
	out := newMeshState()
	for id, p := range ms.Participants {
		out.Participants[id] = p
	}
	out.ParticipantOrder = append([]agentcomm.ParticipantID(nil), ms.ParticipantOrder...)
	for id, th := range ms.Threads {
		out.Threads[id] = th
	}
	out.ThreadOrder = append([]agentcomm.ThreadID(nil), ms.ThreadOrder...)
	for id, m := range ms.Messages {
		out.Messages[id] = m
	}
	for tid, ids := range ms.MessagesByThread {
		out.MessagesByThread[tid] = append([]agentcomm.MessageID(nil), ids...)
	}
	for pid, ids := range ms.Inbox {
		out.Inbox[pid] = append([]agentcomm.MessageID(nil), ids...)
	}
	for mid, inner := range ms.Receipts {
		out.Receipts[mid] = inner
	}
	for id, w := range ms.Waits {
		out.Waits[id] = w
	}
	out.WaitOrder = append([]agentcomm.WaitID(nil), ms.WaitOrder...)
	for key, e := range ms.Board {
		out.Board[key] = e
	}
	return out
}

// deepClone returns a FULL copy of ms, used only by applyOperationCommitted's transactional batch apply (see
// AssistantsState.deepClone's doc comment): every value this package's handlers mutate IN PLACE (as opposed to
// replacing wholesale with a new pointer) gets its own independent copy, so a later effect in the same batch
// can fail and leave the real state completely untouched. Threads (Status), Waits (Status) and each message's
// Receipts sub-map are mutated in place; Participants/Messages/Board entries are always replaced wholesale by
// their handlers, so clone()'s shared pointers already suffice for them.
func (ms MeshState) deepClone() MeshState {
	out := ms.clone()
	for id, th := range ms.Threads {
		nt := *th
		nt.Members = append([]agentcomm.ParticipantID(nil), th.Members...)
		out.Threads[id] = &nt
	}
	for id, w := range ms.Waits {
		nw := *w
		out.Waits[id] = &nw
	}
	for mid, inner := range ms.Receipts {
		ni := make(map[agentcomm.ParticipantID]agentcomm.DeliveryStatus, len(inner))
		for k, v := range inner {
			ni[k] = v
		}
		out.Receipts[mid] = ni
	}
	return out
}

// MessageAnswered reports whether SOME message in ms has InReplyTo equal to id — "answered" is deliberately
// never a stored DeliveryStatus value (unlike accepted/delivered/acknowledged/undeliverable, each of which has
// its own control kind below): it is always computed fresh from the current message set, the same
// "derived, not stored" discipline ReadyContinuations/ReadyWaits use elsewhere in this package.
func (ms MeshState) MessageAnswered(id agentcomm.MessageID) bool {
	for _, m := range ms.Messages {
		if m.InReplyTo != nil && *m.InReplyTo == id {
			return true
		}
	}
	return false
}

// HasWakingUnreadFor is THE "waking unread" rule: whether recipient has a message it has not seen yet (still
// only accepted, not delivered to it through <inbox> or `afm agent inbox`) that should wake it. A passive turn
// result (Message.AutoResult) never does: it waits in the inbox for the recipient's next turn. Everything that
// decides whether an agent is woken goes through it — a thread-less addressed_message wait, an idle run agent's
// next turn, the run-finalization blocker, a stage's extra turns for unread messages and the counter-question
// resume.
func (ms MeshState) HasWakingUnreadFor(recipient agentcomm.ParticipantID) bool {
	for _, id := range ms.Inbox[recipient] {
		if m := ms.Messages[id]; m != nil && !m.AutoResult && ms.Receipts[id][recipient] == agentcomm.DeliveryAccepted {
			return true
		}
	}
	return false
}

// CheckInboxCapacity rejects a message to recipients when one of them already has agentcomm.MaxInboxUnread
// messages it has not been handed yet (still accepted) — explicit rejection, never a silent truncation (T0.2).
// The limit bounds what is still waiting to reach the recipient: a message handed over in a turn's <inbox> or
// by `afm agent inbox` (delivered) no longer occupies it, so a recipient that keeps reading never fills up.
// Passive turn results (passive=true, Message.AutoResult) are counted against their own
// agentcomm.MaxInboxPassive instead: an idle recipient collects them without being woken, and they must never
// crowd out the waking message that would start its next turn. The human is not limited: the dashboard shows
// every thread in full, there is no queue to hand over. Exported so a caller can check it before taking a step
// it would otherwise leave behind.
func (ms MeshState) CheckInboxCapacity(recipients []agentcomm.ParticipantID, passive bool) error {
	limit, what := agentcomm.MaxInboxUnread, "messages waiting to be read (MaxInboxUnread)"
	if passive {
		limit, what = agentcomm.MaxInboxPassive, "turn results waiting to be read (MaxInboxPassive)"
	}
	for _, r := range recipients {
		if p, ok := ms.Participants[r]; ok && p.Kind == agentcomm.KindUser {
			continue
		}
		waiting := 0
		for _, mid := range ms.Inbox[r] {
			if m := ms.Messages[mid]; m != nil && m.AutoResult == passive && ms.Receipts[mid][r] == agentcomm.DeliveryAccepted {
				waiting++
			}
		}
		if waiting >= limit {
			return meshRejected(assistants.CodeBudgetExceeded, "recipient %s already has %d %s", r, limit, what)
		}
	}
	return nil
}

// appendToInbox adds id to recipient's inbox index. The human's index keeps only its newest
// agentcomm.MaxHumanInbox messages — its sends are not capacity-checked, so without this bound a long run would
// grow the index, and every snapshot copy of it, without limit. Decided the same way live and on replay.
func (ms *MeshState) appendToInbox(recipient agentcomm.ParticipantID, id agentcomm.MessageID) {
	ids := append(ms.Inbox[recipient], id)
	if p, ok := ms.Participants[recipient]; ok && p.Kind == agentcomm.KindUser && len(ids) > agentcomm.MaxHumanInbox {
		ids = slices.Clone(ids[len(ids)-agentcomm.MaxHumanInbox:])
	}
	ms.Inbox[recipient] = ids
}

// hasMessageAddressedTo reports whether some message already in threadID, other than a passive turn result,
// names recipient among its Recipients — used by mesh_wait_registered (mesh_waits.go) to decide whether a WaitOnAddressedMessage
// condition is already satisfied at registration time.
func (ms MeshState) hasMessageAddressedTo(threadID agentcomm.ThreadID, recipient agentcomm.ParticipantID) bool {
	for _, id := range ms.MessagesByThread[threadID] {
		m := ms.Messages[id]
		if m.AutoResult {
			continue // a passive turn result never wakes anybody
		}
		for _, r := range m.Recipients {
			if r == recipient {
				return true
			}
		}
	}
	return false
}

// ---- T1.1: participants and threads ----

// MeshParticipantAddedPayload is the mesh_participant_added control record's payload — decoded here by
// applyMeshParticipantAdded. The new participant's id is carried in Target.ParticipantID, not in this payload
// (same "Target names the entity this record creates" convention TaskCreatedPayload follows via
// Target.TaskID).
type MeshParticipantAddedPayload struct {
	Kind        string `json:"kind"`
	Scope       string `json:"scope"`
	Alias       string `json:"alias,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	StageID     string `json:"stage_id,omitempty"`
}

// applyMeshParticipantAdded projects one mesh_participant_added control record. A participant is always
// created Reachable (UnreachableReason empty) — this phase adds no kind that later flips it, so nothing
// mutates a Participant in place after creation.
//
// A record naming an id that already exists with the SAME descriptor is a no-op, not an error: OpenThread
// (pkg/orchestrator/agentcomm_roster.go, ensureParticipantEffects) re-derives a stage/helper/user member's
// added-effect from live run state on EVERY call rather than gating it on "already known", so that a retry of
// the exact same operation id produces the exact same effects (required for ApplyRunOperation's idempotency
// check) and so that naming an already-registered member in a later, different thread-open is harmless. Only
// a genuinely conflicting descriptor for the same id is rejected.
func applyMeshParticipantAdded(as *AssistantsState, c *Control, dryRun bool) error {
	id := agentcomm.ParticipantID(c.Target.ParticipantID)
	if id == "" {
		return errors.New("mesh_participant_added: empty target participant_id")
	}
	var p MeshParticipantAddedPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("decode mesh_participant_added payload: %w", err)
	}
	participant := agentcomm.Participant{
		ID: id, Kind: agentcomm.ParticipantKind(p.Kind), Scope: agentcomm.Scope(p.Scope),
		Alias: p.Alias, DisplayName: p.DisplayName, StageID: p.StageID, Reachable: true,
	}
	if !participant.Valid() {
		return fmt.Errorf("mesh_participant_added %s: invalid participant", id)
	}
	if existing, exists := as.Mesh.Participants[id]; exists {
		if existing.Kind != participant.Kind || existing.Scope != participant.Scope || existing.Alias != participant.Alias ||
			existing.DisplayName != participant.DisplayName || existing.StageID != participant.StageID {
			return fmt.Errorf("mesh_participant_added: id %s already exists with a different descriptor", id)
		}
		return nil
	}
	if dryRun {
		return nil
	}
	as.Mesh.Participants[id] = &participant
	as.Mesh.ParticipantOrder = append(as.Mesh.ParticipantOrder, id)
	return nil
}

// MeshThreadOpenedPayload is the mesh_thread_opened control record's payload — decoded here by
// applyMeshThreadOpened. Members is the thread's FIXED member set (agentcomm.Thread's own doc comment: adding
// a colleague means opening a new thread, never mutating this one's Members).
type MeshThreadOpenedPayload struct {
	Title       string   `json:"title,omitempty"`
	Scope       string   `json:"scope"`
	Members     []string `json:"members"`
	MaxMessages int      `json:"max_messages,omitempty"`
	// OriginStage — agentcomm.Thread.OriginStage. Not part of the descriptor a re-open compares: the first
	// open decides it, so two agents of different stages opening the same derived conversation at once, or a
	// post re-opening a discussion thread journaled before the field existed, stay no-ops.
	OriginStage string `json:"origin_stage,omitempty"`
}

// applyMeshThreadOpened projects one mesh_thread_opened control record. Every named member must already be a
// known participant — a thread naming a nonexistent member is rejected before anything is mutated, the same
// fail-closed posture applyDiscussionMessage uses for an unknown parent_message_id.
//
// A record naming an id that already exists with the SAME descriptor (Title/Scope/Members/MaxMessages — NOT
// Status, which a later mesh_thread_closed may have changed independently, nor OriginStage, which the first
// open decides) is a no-op, not an error — the
// SAME "re-derive from scratch on every call, let the projection dedupe" discipline
// applyMeshParticipantAdded already documents. Agent Mesh Phase 6, T6.4 relies on exactly this: a stage's
// discussion thread uses one FIXED id+descriptor derived from the stage id alone (not the posting operation's
// id), so every post after the first re-submits the identical mesh_thread_opened effect. Only a genuinely
// conflicting descriptor for the same id is rejected.
func applyMeshThreadOpened(as *AssistantsState, c *Control, dryRun bool) error {
	id := agentcomm.ThreadID(c.Target.ThreadID)
	if id == "" {
		return errors.New("mesh_thread_opened: empty target thread_id")
	}
	var p MeshThreadOpenedPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("decode mesh_thread_opened payload: %w", err)
	}
	members := make([]agentcomm.ParticipantID, 0, len(p.Members))
	agents := 0
	for _, m := range p.Members {
		pid := agentcomm.ParticipantID(m)
		participant, ok := as.Mesh.Participants[pid]
		if !ok {
			return meshRejected(assistants.CodeNotFound, "unknown participant %q — `afm agent list` shows who is in this run", m)
		}
		if participant.Kind != agentcomm.KindUser {
			agents++
		}
		members = append(members, pid)
	}
	// The human joins every conversation, so only agents count toward the limit.
	if agents > agentcomm.MaxThreadMembers {
		return meshRejected(assistants.CodeInvalidRequest, "a conversation can have at most %d agents (MaxThreadMembers), got %d", agentcomm.MaxThreadMembers, agents)
	}
	thread := agentcomm.Thread{
		ID: id, Title: p.Title, Scope: agentcomm.Scope(p.Scope), Members: members, Status: agentcomm.ThreadOpen,
		Budgets: agentcomm.ThreadBudgets{MaxMessages: p.MaxMessages}, OriginStage: p.OriginStage,
	}
	if !thread.Valid() {
		return fmt.Errorf("mesh_thread_opened %s: invalid thread", id)
	}
	if existing, exists := as.Mesh.Threads[id]; exists {
		if existing.Title != thread.Title || existing.Scope != thread.Scope || existing.Budgets != thread.Budgets ||
			!slices.Equal(existing.Members, thread.Members) {
			return fmt.Errorf("mesh_thread_opened: id %s already exists with a different descriptor", id)
		}
		return nil
	}
	if dryRun {
		return nil
	}
	as.Mesh.Threads[id] = &thread
	as.Mesh.ThreadOrder = append(as.Mesh.ThreadOrder, id)
	return nil
}

// applyMeshThreadClosed projects one mesh_thread_closed control record. Closing an already-closed thread is
// REJECTED, not a silent no-op: a second close is always a caller bug (there is exactly one legitimate closer
// per thread in the design this phase supports), and silently swallowing it would hide that bug.
//
// Every still-pending wait whose condition names this thread (an addressed_message wait on it, or a
// reply_to wait on one of its messages) is cancelled in the SAME commit — decided here, against the live
// Mesh state, rather than by the orchestrator from a pre-close snapshot (same race/idempotency reasoning as
// resolvePendingWaits's doc comment): closure is never passed off as the answer those waits were registered
// for (tmp/agent-mesh.md §5.4/§6.6).
func applyMeshThreadClosed(as *AssistantsState, c *Control, dryRun bool) error {
	id := agentcomm.ThreadID(c.Target.ThreadID)
	th, ok := as.Mesh.Threads[id]
	if !ok {
		return meshRejected(assistants.CodeNotFound, "unknown thread %s", id)
	}
	if th.Status == agentcomm.ThreadClosed {
		return meshRejected(assistants.CodeInvalidStatus, "thread %s is already closed", id)
	}
	if dryRun {
		return nil
	}
	th.Status = agentcomm.ThreadClosed

	threadMessages := make(map[agentcomm.MessageID]bool, len(as.Mesh.MessagesByThread[id]))
	for _, mid := range as.Mesh.MessagesByThread[id] {
		threadMessages[mid] = true
	}
	as.resolvePendingWaits(agentcomm.WaitCancelled, func(w *MeshWait) bool {
		return (w.Condition.Kind == agentcomm.WaitOnAddressedMessage && w.Condition.ThreadID == id) ||
			(w.Condition.Kind == agentcomm.WaitOnReplyTo && threadMessages[w.Condition.MessageID])
	})
	return nil
}

// ---- T1.2: messages, inbox references and receipts ----

// MeshMessageSentPayload is the mesh_message_sent control record's payload — decoded here by
// applyMeshMessageSent. The new message's id is Target.MessageID, its thread Target.ThreadID (same
// Target-names-the-entity convention as T1.1).
type MeshMessageSentPayload struct {
	Sender       string   `json:"sender_id"`
	Recipients   []string `json:"recipient_ids"`
	Kind         string   `json:"kind"`
	InReplyTo    *string  `json:"in_reply_to,omitempty"`
	Body         string   `json:"body"`
	ArtifactRefs []string `json:"artifact_refs,omitempty"`
	// AutoResult — afm posted an agent's turn result as this reply (see agentcomm.Message.AutoResult). Absent in
	// journals written before it existed.
	AutoResult bool `json:"auto_result,omitempty"`
}

// meshRejected is a typed rejection for a mesh operation the caller can fix (unknown thread, bad recipients,
// exceeded limits): the API turns it into a 4xx with the reason instead of a bare 500.
func meshRejected(code assistants.ErrorCode, format string, args ...any) error {
	return &OperationRejectedError{Code: code, Reason: fmt.Sprintf(format, args...)}
}

// applyMeshMessageSent projects one mesh_message_sent control record: validates the message's shape and
// thread-membership (agentcomm.ValidateMessage) BEFORE mutating anything, then records one Message plus one
// agentcomm.DeliveryAccepted receipt and one Inbox entry per recipient, all in the same call.
func applyMeshMessageSent(as *AssistantsState, c *Control, dryRun bool) error {
	id := agentcomm.MessageID(c.Target.MessageID)
	if id == "" {
		return errors.New("mesh_message_sent: empty target message_id")
	}
	threadID := agentcomm.ThreadID(c.Target.ThreadID)
	if threadID == "" {
		return errors.New("mesh_message_sent: empty target thread_id")
	}
	if _, exists := as.Mesh.Messages[id]; exists {
		return fmt.Errorf("mesh_message_sent: id %s already exists", id)
	}
	thread, ok := as.Mesh.Threads[threadID]
	if !ok {
		return meshRejected(assistants.CodeNotFound, "unknown thread %s — send without a thread to start a conversation", threadID)
	}
	if thread.Status == agentcomm.ThreadClosed {
		return meshRejected(assistants.CodeInvalidStatus, "thread %s is closed", threadID)
	}

	var p MeshMessageSentPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("decode mesh_message_sent payload: %w", err)
	}

	recipients := make([]agentcomm.ParticipantID, 0, len(p.Recipients))
	for _, r := range p.Recipients {
		recipients = append(recipients, agentcomm.ParticipantID(r))
	}
	var inReplyTo *agentcomm.MessageID
	if p.InReplyTo != nil {
		mid := agentcomm.MessageID(*p.InReplyTo)
		inReplyTo = &mid
	}
	msg := agentcomm.Message{
		ID: id, ThreadID: threadID, Sender: agentcomm.ParticipantID(p.Sender), Recipients: recipients,
		Kind: agentcomm.MessageKind(p.Kind), InReplyTo: inReplyTo, Body: p.Body, ArtifactRefs: p.ArtifactRefs,
		AutoResult: p.AutoResult, Seq: c.Seq, CreatedAt: c.Time,
	}

	known := make(map[agentcomm.MessageID]bool, len(as.Mesh.MessagesByThread[threadID]))
	for _, mid := range as.Mesh.MessagesByThread[threadID] {
		known[mid] = true
	}
	if err := agentcomm.ValidateMessage(msg, thread.Members, known); err != nil {
		if errors.Is(err, agentcomm.ErrSenderNotThreadMember) {
			// Never reveal who is in a thread to someone outside it.
			return meshRejected(assistants.CodeActorNotAllowed, "%s is not a member of thread %s", msg.Sender, threadID)
		}
		return meshRejected(assistants.CodeInvalidRequest, "%v (thread %s members: %v)", err, threadID, thread.Members)
	}
	// The thread's message limit and MaxInboxUnread: explicit rejection, never a silent truncation (T0.2). A
	// thread's own budget binds new messages only — older journals hold threads filled past it up to the global
	// limit, and they must still replay.
	limit := agentcomm.MaxThreadMessages
	if as.admittingNewRecord(dryRun) {
		limit = threadMessageLimit(thread)
	}
	if len(as.Mesh.MessagesByThread[threadID]) >= limit {
		return meshRejected(assistants.CodeBudgetExceeded, "thread %s has reached its limit of %d messages", threadID, limit)
	}
	if err := as.Mesh.CheckInboxCapacity(recipients, msg.AutoResult); err != nil {
		return err
	}

	if dryRun {
		return nil
	}

	as.Mesh.Messages[id] = &msg
	as.Mesh.MessagesByThread[threadID] = append(as.Mesh.MessagesByThread[threadID], id)
	receipts := make(map[agentcomm.ParticipantID]agentcomm.DeliveryStatus, len(recipients))
	for _, r := range recipients {
		receipts[r] = agentcomm.DeliveryAccepted
		as.Mesh.appendToInbox(r, id)
	}
	as.Mesh.Receipts[id] = receipts

	// A reply arriving makes exactly one continuation (Agent Mesh Phase 5, T5.3): resolve every PENDING wait
	// this message satisfies — reply_to the message it answers, or addressed_message in its thread for one of
	// its recipients — in the SAME commit as the message itself. See resolvePendingWaits's doc comment for why
	// this cannot be decided by the orchestrator from a pre-commit snapshot.
	as.resolvePendingWaits(agentcomm.WaitReady, func(w *MeshWait) bool {
		return as.meshWaitSatisfied(w)
	})
	return nil
}

// ThreadFull reports whether thread id holds as many messages as a new one may add up to — a sender that can
// choose not to write (a board notice) checks it instead of having its whole operation rejected.
func (ms MeshState) ThreadFull(id agentcomm.ThreadID) bool {
	th, ok := ms.Threads[id]
	return ok && len(ms.MessagesByThread[id]) >= threadMessageLimit(th)
}

// threadMessageLimit is how many messages thread may hold: its own Budgets.MaxMessages when set, capped by the
// global agentcomm.MaxThreadMessages.
func threadMessageLimit(thread *agentcomm.Thread) int {
	if b := thread.Budgets.MaxMessages; b > 0 && b < agentcomm.MaxThreadMessages {
		return b
	}
	return agentcomm.MaxThreadMessages
}

// deliveryRank orders DeliveryStatus so receipt transitions can be checked for monotonicity: a status may
// only move to a STRICTLY higher rank (tmp/agent-mesh.md §6.1 — "no status alone proves ..." plus the
// durability rule that a receipt can never silently regress). "answered" never appears here as a STORED
// value (see MessageAnswered), but is still ranked for completeness/documentation.
func deliveryRank(s agentcomm.DeliveryStatus) int {
	switch s {
	case agentcomm.DeliveryAccepted:
		return 0
	case agentcomm.DeliveryDelivered:
		return 1
	case agentcomm.DeliveryAcknowledged:
		return 2
	case agentcomm.DeliveryAnswered:
		return 3
	case agentcomm.DeliveryUndeliverable:
		return 4
	default:
		return -1
	}
}

// MeshMessageReceiptPayload is the shared payload shape for mesh_message_delivered/mesh_message_acked/
// mesh_message_undeliverable — only a human-readable Reason varies; the target status is implied by the kind
// itself (applyMeshMessageReceipt's newStatus parameter), not carried in the payload.
type MeshMessageReceiptPayload struct {
	Reason string `json:"reason,omitempty"`
}

// applyMeshMessageReceipt projects one recipient's receipt transition for one message (Target.MessageID,
// Target.ParticipantID). Monotonic: if newStatus's rank is not STRICTLY greater than the recipient's current
// rank, this is a no-op, never an error — this is what makes two concurrent deliveries/acks of the same
// message/recipient resolve to exactly one forward-moving outcome instead of a corrupted or regressed status.
func applyMeshMessageReceipt(as *AssistantsState, c *Control, newStatus agentcomm.DeliveryStatus, dryRun bool) error {
	id := agentcomm.MessageID(c.Target.MessageID)
	if id == "" {
		return fmt.Errorf("%s: empty target message_id", c.Kind)
	}
	recipient := agentcomm.ParticipantID(c.Target.ParticipantID)
	if recipient == "" {
		return fmt.Errorf("%s: empty target participant_id", c.Kind)
	}
	receipts, ok := as.Mesh.Receipts[id]
	if !ok {
		return meshRejected(assistants.CodeNotFound, "unknown message %s", id)
	}
	current, ok := receipts[recipient]
	if !ok {
		return meshRejected(assistants.CodeInvalidRequest, "%s is not a recipient of message %s", recipient, id)
	}
	if dryRun {
		return nil
	}
	if deliveryRank(newStatus) <= deliveryRank(current) {
		return nil
	}
	receipts[recipient] = newStatus
	return nil
}

func applyMeshMessageDelivered(as *AssistantsState, c *Control, dryRun bool) error {
	return applyMeshMessageReceipt(as, c, agentcomm.DeliveryDelivered, dryRun)
}

func applyMeshMessageAcked(as *AssistantsState, c *Control, dryRun bool) error {
	return applyMeshMessageReceipt(as, c, agentcomm.DeliveryAcknowledged, dryRun)
}

func applyMeshMessageUndeliverable(as *AssistantsState, c *Control, dryRun bool) error {
	return applyMeshMessageReceipt(as, c, agentcomm.DeliveryUndeliverable, dryRun)
}
