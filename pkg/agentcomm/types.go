package agentcomm

import "time"

// ProtocolVersion is the single version of the Agent Mesh wire/journal contract this build
// understands. Every durable mesh record carries it so an older build can refuse to
// misinterpret a newer journal — the same fail-closed posture as state.ControlSchemaVersion.
const ProtocolVersion = 1

// ParticipantID is a server-assigned id, stable and unique within one run. Higher layers mint it
// in one of a few conventional forms — "user", "stage:<stageID>", "helper:<taskID>",
// "run:<profileID>", "side:<runID>" — but this package treats it as an opaque string: parsing or
// constructing that convention is the orchestrator's job, not this package's.
type ParticipantID string

// ParticipantKind says what role a participant plays. It never changes for a given
// ParticipantID's lifetime.
type ParticipantKind string

const (
	KindUser       ParticipantKind = "user"
	KindStageAgent ParticipantKind = "stage_agent"
	KindHelper     ParticipantKind = "helper"
	KindRunAgent   ParticipantKind = "run_agent"
	KindSideAgent  ParticipantKind = "side_agent"
)

// Valid reports whether k is one of the recognized kinds. Unlike assistants.Mode/Trigger, a
// participant's kind has no meaningful default — the zero value is never valid.
func (k ParticipantKind) Valid() bool {
	switch k {
	case KindUser, KindStageAgent, KindHelper, KindRunAgent, KindSideAgent:
		return true
	default:
		return false
	}
}

// Scope says whether a participant or thread lives and dies with one stage, or stands for the
// whole run.
type Scope string

const (
	ScopeStage Scope = "stage"
	ScopeRun   Scope = "run"
)

// Valid reports whether s is a recognized Scope. The zero value is never valid.
func (s Scope) Valid() bool {
	switch s {
	case ScopeStage, ScopeRun:
		return true
	default:
		return false
	}
}

// Participant is one addressable member of a run's mesh: the human, a stage's main agent, a
// helper task at any depth, a run-scoped standing role, or the bound Side agent.
type Participant struct {
	ID    ParticipantID   `json:"id"`
	Kind  ParticipantKind `json:"kind"`
	Scope Scope           `json:"scope"`
	// Alias is the short, human-typed mention name (e.g. "qa") used by ResolveAlias. May be empty
	// for a participant that is never @-mentioned.
	Alias string `json:"alias,omitempty"`
	// DisplayName is the full label shown in the UI (e.g. "QA reviewer"). A renderer falls back
	// to Alias or ID when this is empty — this package does not pick that default itself.
	DisplayName string `json:"display_name,omitempty"`
	// StageID binds a stage-scoped participant to the stage it belongs to. Empty for run-scoped
	// participants (user, run_agent, side_agent).
	StageID string `json:"stage_id,omitempty"`
	// Reachable reports whether this participant can currently receive a message — false once its
	// stage/task has gone terminal, or its executor lacks mesh capability. A terminal participant
	// stays in the roster, never erased, with Reachable=false and UnreachableReason set.
	Reachable bool `json:"reachable"`
	// UnreachableReason explains why Reachable is false. Empty when Reachable is true.
	UnreachableReason string `json:"unreachable_reason,omitempty"`
}

// Valid reports whether p has the minimum identity a Participant needs: a non-empty ID and a
// recognized Kind and Scope. The zero Participant is never valid.
func (p Participant) Valid() bool {
	return p.ID != "" && p.Kind.Valid() && p.Scope.Valid()
}

// ThreadID identifies one conversation, unique within a run.
type ThreadID string

// ThreadStatus is a Thread's lifecycle state.
type ThreadStatus string

const (
	ThreadOpen   ThreadStatus = "open"
	ThreadClosed ThreadStatus = "closed"
)

// Valid reports whether s is a recognized ThreadStatus. The zero value is never valid.
func (s ThreadStatus) Valid() bool {
	switch s {
	case ThreadOpen, ThreadClosed:
		return true
	default:
		return false
	}
}

// ThreadBudgets caps how far one conversation can run, so a thread can never grow or churn
// without bound ("конечные лимиты сообщений/автоматических ходов/времени" in the design doc).
type ThreadBudgets struct {
	// MaxMessages is the most messages this thread may ever hold. Zero means "use the package
	// default" (MaxThreadMessages) — never "unlimited".
	MaxMessages int `json:"max_messages,omitempty"`
}

// Thread is a conversation with a fixed member set decided at open time: adding a colleague
// means opening a new thread, never mutating this one's Members.
type Thread struct {
	ID      ThreadID        `json:"id"`
	Title   string          `json:"title,omitempty"`
	Scope   Scope           `json:"scope"`
	Members []ParticipantID `json:"members"`
	Status  ThreadStatus    `json:"status"`
	Budgets ThreadBudgets   `json:"budgets,omitempty"`
}

// Valid reports whether t has the minimum shape a Thread needs: a non-empty ID, at least one
// member, and a recognized Status. The zero Thread is never valid.
func (t Thread) Valid() bool {
	return t.ID != "" && len(t.Members) > 0 && t.Status.Valid()
}

// MessageID identifies one durable message, unique within a run.
type MessageID string

// MessageKind determines how a message is delivered and linked — never authority, consent or
// task completion. Those stay explicit and are never inferred from a message's phrasing.
type MessageKind string

const (
	MessageKindMessage  MessageKind = "message"
	MessageKindRequest  MessageKind = "request"
	MessageKindReply    MessageKind = "reply"
	MessageKindResult   MessageKind = "result"
	MessageKindProposal MessageKind = "proposal"
)

// Valid reports whether k is a recognized MessageKind. An unrecognized kind is always rejected by
// ValidateMessage — never guessed from other fields.
func (k MessageKind) Valid() bool {
	switch k {
	case MessageKindMessage, MessageKindRequest, MessageKindReply, MessageKindResult, MessageKindProposal:
		return true
	default:
		return false
	}
}

// Message is one durable entry in a thread.
type Message struct {
	ID         MessageID       `json:"id"`
	ThreadID   ThreadID        `json:"thread_id"`
	Sender     ParticipantID   `json:"sender_id"`
	Recipients []ParticipantID `json:"recipient_ids"`
	Kind       MessageKind     `json:"kind"`
	// InReplyTo names the message this one answers. Required when Kind is MessageKindReply, and
	// must otherwise name a message already present in the same thread (see ValidateMessage).
	InReplyTo    *MessageID `json:"in_reply_to,omitempty"`
	Body         string     `json:"body"`
	ArtifactRefs []string   `json:"artifact_refs,omitempty"`
	// Seq is assigned by the journal writer, sharing the run's single event-log sequence.
	Seq uint64 `json:"seq"`
	// CreatedAt is stamped from the control record's own time, never time.Now(), so replay stays
	// deterministic (root AGENTS.md's clock seam rule).
	CreatedAt time.Time `json:"created_at"`
}

// Valid reports whether m has the minimum shape a Message needs: a non-empty ID, thread, sender,
// at least one recipient, and a recognized Kind. The zero Message is never valid. This only
// checks shape — ValidateMessage enforces the full rule set (limits, reply linkage, membership).
func (m Message) Valid() bool {
	return m.ID != "" && m.ThreadID != "" && m.Sender != "" && len(m.Recipients) > 0 && m.Kind.Valid()
}

// DeliveryStatus tracks one recipient's receipt of one message: accepted, delivered,
// acknowledged, answered, or the terminal undeliverable. No status alone proves understanding,
// agreement, or a completed fix (agent-mesh.md §6.1).
type DeliveryStatus string

const (
	DeliveryAccepted      DeliveryStatus = "accepted"
	DeliveryDelivered     DeliveryStatus = "delivered"
	DeliveryAcknowledged  DeliveryStatus = "acknowledged"
	DeliveryAnswered      DeliveryStatus = "answered"
	DeliveryUndeliverable DeliveryStatus = "undeliverable"
)

// Valid reports whether s is a recognized DeliveryStatus. The zero value is never valid.
func (s DeliveryStatus) Valid() bool {
	switch s {
	case DeliveryAccepted, DeliveryDelivered, DeliveryAcknowledged, DeliveryAnswered, DeliveryUndeliverable:
		return true
	default:
		return false
	}
}

// WaitID identifies one durable wait registration, unique within a run.
type WaitID string

// WaitConditionKind discriminates which of the five representable wait conditions a
// WaitCondition holds. This is a closed union — a free-text condition is never representable, so
// a condition the server cannot express is rejected at admission, not stored vaguely.
type WaitConditionKind string

const (
	// WaitOnReplyTo is satisfied when some message in the thread has InReplyTo equal to MessageID.
	WaitOnReplyTo WaitConditionKind = "reply_to"
	// WaitOnAddressedMessage is satisfied by any new message addressed to the waiter in ThreadID.
	WaitOnAddressedMessage WaitConditionKind = "addressed_message"
	// WaitOnTaskResult is satisfied when the assistants task named by TaskID reaches a terminal
	// status.
	WaitOnTaskResult WaitConditionKind = "task_result"
	// WaitOnBoardKey is satisfied when the board entry named Key reaches Status.
	WaitOnBoardKey WaitConditionKind = "board_key"
	// WaitOnStageStatus is satisfied when the stage named StageID reaches Status.
	WaitOnStageStatus WaitConditionKind = "stage_status"
)

// Valid reports whether k is a recognized WaitConditionKind. The zero value is never valid.
func (k WaitConditionKind) Valid() bool {
	switch k {
	case WaitOnReplyTo, WaitOnAddressedMessage, WaitOnTaskResult, WaitOnBoardKey, WaitOnStageStatus:
		return true
	default:
		return false
	}
}

// WaitCondition is a closed, typed union of the five conditions a Wait may be registered
// against. Kind selects which of the other fields is meaningful; the rest are left zero. A
// discriminated struct is simpler than an interface here: there is a fixed, small set of cases
// and no caller outside this package ever needs to add a new one.
type WaitCondition struct {
	Kind WaitConditionKind `json:"kind"`

	// MessageID is set for WaitOnReplyTo: the message the waiter expects an answer to.
	MessageID MessageID `json:"message_id,omitempty"`
	// ThreadID is set for WaitOnAddressedMessage: the thread to watch for a new message
	// addressed to the waiter. Empty means any thread: a message the waiter has not seen yet.
	ThreadID ThreadID `json:"thread_id,omitempty"`
	// TaskID is set for WaitOnTaskResult: the assistants task whose terminal result is awaited.
	// Kept as a plain string (not assistants.TaskID) so this field costs agentcomm no import of
	// pkg/assistants.
	TaskID string `json:"task_id,omitempty"`
	// Key is set for WaitOnBoardKey: the board entry key to watch.
	Key string `json:"key,omitempty"`
	// StageID is set for WaitOnStageStatus: the stage to watch.
	StageID string `json:"stage_id,omitempty"`
	// Status is set for WaitOnBoardKey (the board entry's expected status) and for
	// WaitOnStageStatus (the stage's expected status). The two conditions never co-occur, so
	// sharing one field does not conflate them.
	Status string `json:"status,omitempty"`
}

// Valid reports whether c names a recognized Kind and carries the field(s) that kind requires.
// The zero WaitCondition is never valid.
func (c WaitCondition) Valid() bool {
	switch c.Kind {
	case WaitOnReplyTo:
		return c.MessageID != ""
	case WaitOnAddressedMessage:
		return true
	case WaitOnTaskResult:
		return c.TaskID != ""
	case WaitOnBoardKey:
		return c.Key != "" && c.Status != ""
	case WaitOnStageStatus:
		return c.StageID != "" && c.Status != ""
	default:
		return false
	}
}

// WaitStatus is a Wait's own lifecycle, separate from the condition it watches.
type WaitStatus string

const (
	WaitPending   WaitStatus = "pending"
	WaitReady     WaitStatus = "ready"
	WaitCancelled WaitStatus = "cancelled"
	WaitExpired   WaitStatus = "expired"
	// WaitRunClosed (Agent Mesh Phase 5, T5.6) resolves a wait that was still WaitPending when the run itself
	// wound down — nobody will ever revisit it again, so it must not dangle in the journal forever with no
	// visible terminal outcome. Distinct from WaitCancelled (a deliberate, addressed decision by the waiter or
	// a human) and WaitExpired (its own deadline elapsed): this one is neither, it is simply "the run ended
	// while this was still open".
	WaitRunClosed WaitStatus = "run_closed"
)

// Valid reports whether s is a recognized WaitStatus. The zero value is never valid.
func (s WaitStatus) Valid() bool {
	switch s {
	case WaitPending, WaitReady, WaitCancelled, WaitExpired, WaitRunClosed:
		return true
	default:
		return false
	}
}

// Wait is a durable registration that a participant is waiting on some typed condition, with an
// optional deadline and a note for the continuation that resumes it once the condition is met.
type Wait struct {
	ID        WaitID        `json:"id"`
	Waiter    ParticipantID `json:"waiter_id"`
	Condition WaitCondition `json:"condition"`
	// Deadline is nil for "wait indefinitely". A board_key condition with no known producer
	// requires a non-nil Deadline — enforced by the orchestrator, not by this type.
	Deadline *time.Time `json:"deadline,omitempty"`
	// ContinuationNote carries context the waiter wants to see when it resumes (e.g. what it was
	// waiting for) — the same role assistants.Continuation.Note plays for delegation.
	ContinuationNote string     `json:"continuation_note,omitempty"`
	Status           WaitStatus `json:"status"`
	// Reason (Agent Mesh Phase 5, T5.6) is set once this wait resolves away from Pending without the
	// condition itself having been satisfied — "deadline exceeded", a human/agent's CancelWait reason, or the
	// run closing while it was still open. Empty for WaitPending and for a normal WaitReady satisfaction
	// (the condition itself is the "reason" there, with nothing extra to say). Stored durably so the outcome
	// is visible on replay, not just in the live notice stream.
	Reason string `json:"reason,omitempty"`
}

// Valid reports whether w has the minimum shape a Wait needs: a non-empty ID, waiter, a valid
// Condition, and a recognized Status. The zero Wait is never valid.
func (w Wait) Valid() bool {
	return w.ID != "" && w.Waiter != "" && w.Condition.Valid() && w.Status.Valid()
}

// BoardEntry is one key's current value on the shared board: "what is known/ready now", as
// opposed to a Thread's "who asked/answered/proposed what".
type BoardEntry struct {
	AuthorID ParticipantID `json:"author_id"`
	Key      string        `json:"key"`
	// Version increments on every publish to the same Key, guarding against a lost update via
	// expected-version CAS (enforced by the orchestrator, not by this type).
	Version      int       `json:"version"`
	Status       string    `json:"status"`
	ArtifactRefs []string  `json:"artifact_refs,omitempty"`
	Seq          uint64    `json:"seq"`
	Ts           time.Time `json:"ts"`
}

// Valid reports whether e has the minimum shape a BoardEntry needs: a non-empty AuthorID and Key,
// and a Version of at least 1 (the first publish of any key starts at 1). The zero BoardEntry is
// never valid.
func (e BoardEntry) Valid() bool {
	return e.AuthorID != "" && e.Key != "" && e.Version >= 1
}
