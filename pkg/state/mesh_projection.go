package state

import (
	"errors"
	"fmt"

	"github.com/akopichin/afm/pkg/agentcomm"
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

// hasMessageAddressedTo reports whether some message already in threadID names recipient among its
// Recipients — used by mesh_wait_registered (mesh_waits.go) to decide whether a WaitOnAddressedMessage
// condition is already satisfied at registration time.
func (ms MeshState) hasMessageAddressedTo(threadID agentcomm.ThreadID, recipient agentcomm.ParticipantID) bool {
	for _, id := range ms.MessagesByThread[threadID] {
		m := ms.Messages[id]
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
func applyMeshParticipantAdded(as *AssistantsState, c *Control, dryRun bool) error {
	id := agentcomm.ParticipantID(c.Target.ParticipantID)
	if id == "" {
		return errors.New("mesh_participant_added: empty target participant_id")
	}
	if _, exists := as.Mesh.Participants[id]; exists {
		return fmt.Errorf("mesh_participant_added: id %s already exists", id)
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
}

// applyMeshThreadOpened projects one mesh_thread_opened control record. Every named member must already be a
// known participant — a thread naming a nonexistent member is rejected before anything is mutated, the same
// fail-closed posture applyDiscussionMessage uses for an unknown parent_message_id.
func applyMeshThreadOpened(as *AssistantsState, c *Control, dryRun bool) error {
	id := agentcomm.ThreadID(c.Target.ThreadID)
	if id == "" {
		return errors.New("mesh_thread_opened: empty target thread_id")
	}
	if _, exists := as.Mesh.Threads[id]; exists {
		return fmt.Errorf("mesh_thread_opened: id %s already exists", id)
	}
	var p MeshThreadOpenedPayload
	if err := decodePayload(c.Payload, &p); err != nil {
		return fmt.Errorf("decode mesh_thread_opened payload: %w", err)
	}
	if len(p.Members) > agentcomm.MaxThreadMembers {
		return fmt.Errorf("mesh_thread_opened %s: %d members exceeds MaxThreadMembers %d", id, len(p.Members), agentcomm.MaxThreadMembers)
	}
	members := make([]agentcomm.ParticipantID, 0, len(p.Members))
	for _, m := range p.Members {
		pid := agentcomm.ParticipantID(m)
		if _, ok := as.Mesh.Participants[pid]; !ok {
			return fmt.Errorf("mesh_thread_opened %s: unknown member %q", id, m)
		}
		members = append(members, pid)
	}
	thread := agentcomm.Thread{
		ID: id, Title: p.Title, Scope: agentcomm.Scope(p.Scope), Members: members, Status: agentcomm.ThreadOpen,
		Budgets: agentcomm.ThreadBudgets{MaxMessages: p.MaxMessages},
	}
	if !thread.Valid() {
		return fmt.Errorf("mesh_thread_opened %s: invalid thread", id)
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
		return fmt.Errorf("mesh_thread_closed: unknown thread_id %s", id)
	}
	if th.Status == agentcomm.ThreadClosed {
		return fmt.Errorf("mesh_thread_closed: thread %s is already closed", id)
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
		return fmt.Errorf("mesh_message_sent %s: unknown thread %s", id, threadID)
	}
	if thread.Status == agentcomm.ThreadClosed {
		return fmt.Errorf("mesh_message_sent %s: thread %s is closed", id, threadID)
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
		Seq: c.Seq, CreatedAt: c.Time,
	}

	known := make(map[agentcomm.MessageID]bool, len(as.Mesh.MessagesByThread[threadID]))
	for _, mid := range as.Mesh.MessagesByThread[threadID] {
		known[mid] = true
	}
	if err := agentcomm.ValidateMessage(msg, thread.Members, known); err != nil {
		return fmt.Errorf("mesh_message_sent %s: %w", id, err)
	}
	// agentcomm.MaxThreadMessages/MaxInboxUnread: explicit rejection, never a silent truncation (T0.2). A
	// recipient's UNREAD count — accepted or delivered, not yet acknowledged — is what the limit bounds; an
	// already-acknowledged message no longer occupies that budget.
	if n := len(as.Mesh.MessagesByThread[threadID]); n >= agentcomm.MaxThreadMessages {
		return fmt.Errorf("mesh_message_sent %s: thread %s has reached MaxThreadMessages (%d)", id, threadID, agentcomm.MaxThreadMessages)
	}
	for _, r := range recipients {
		unread := 0
		for _, mid := range as.Mesh.Inbox[r] {
			switch as.Mesh.Receipts[mid][r] {
			case agentcomm.DeliveryAccepted, agentcomm.DeliveryDelivered:
				unread++
			default:
			}
		}
		if unread >= agentcomm.MaxInboxUnread {
			return fmt.Errorf("mesh_message_sent %s: recipient %s has reached MaxInboxUnread (%d)", id, r, agentcomm.MaxInboxUnread)
		}
	}

	if dryRun {
		return nil
	}

	as.Mesh.Messages[id] = &msg
	as.Mesh.MessagesByThread[threadID] = append(as.Mesh.MessagesByThread[threadID], id)
	receipts := make(map[agentcomm.ParticipantID]agentcomm.DeliveryStatus, len(recipients))
	for _, r := range recipients {
		receipts[r] = agentcomm.DeliveryAccepted
		as.Mesh.Inbox[r] = append(as.Mesh.Inbox[r], id)
	}
	as.Mesh.Receipts[id] = receipts

	// A reply arriving makes exactly one continuation (Agent Mesh Phase 5, T5.3): resolve every PENDING wait
	// this message satisfies — reply_to the message it answers, or addressed_message in its thread for one of
	// its recipients — in the SAME commit as the message itself. See resolvePendingWaits's doc comment for why
	// this cannot be decided by the orchestrator from a pre-commit snapshot.
	as.resolvePendingWaits(agentcomm.WaitReady, func(w *MeshWait) bool {
		return as.meshWaitConditionSatisfied(w.Condition, w.Waiter)
	})
	return nil
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
		return fmt.Errorf("%s: unknown message %s", c.Kind, id)
	}
	current, ok := receipts[recipient]
	if !ok {
		return fmt.Errorf("%s: %s is not a recipient of message %s", c.Kind, recipient, id)
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
