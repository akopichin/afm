// Package orchestrator (this file, Agent Mesh Phase 2, T2.1): the one Service implementation that every
// mesh client — the HTTP UI adapter, the agent-facing CLI, the Side agent and the prompt/inbox builder —
// calls. See dev-plan.md's "Architecture" section and tmp/agent-mesh.md §5.4 for why this is a single
// interface rather than one per client: a second implementation would be the second inbox/delivery path
// §5.1 forbids.
package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/state"
)

// Service is the Agent Mesh contract (tmp/agent-mesh.md §5.4). *Orchestrator is its only
// implementation (var _ Service assertion below) — an HTTP handler, a CLI command or the prompt
// builder calls these methods directly, never a second copy of the logic.
type Service interface {
	ListParticipants(context.Context, ListParticipantsRequest) (ParticipantPage, error)
	OpenThread(context.Context, OpenThreadRequest) (agentcomm.Thread, error)
	CloseThread(context.Context, CloseThreadRequest) error
	Send(context.Context, SendRequest) (SendReceipt, error)
	ReadInbox(context.Context, ReadInboxRequest) (MessagePage, error)
	ReadThread(context.Context, ReadThreadRequest) (MessagePage, error)
	Acknowledge(context.Context, AcknowledgeRequest) error
	Wait(context.Context, WaitRequest) (WaitReceipt, error)
	CancelWait(context.Context, CancelWaitRequest) error
	Publish(context.Context, PublishRequest) (agentcomm.BoardEntry, error)
	ReadBoard(context.Context, ReadBoardRequest) (BoardPage, error)
}

var _ Service = (*Orchestrator)(nil)

// ListParticipantsRequest carries only the caller's identity: ListParticipants has no filter beyond "what
// can Actor see" (today every participant — a run has no private roster — but the field exists so a later
// policy does not need a new method signature).
type ListParticipantsRequest struct {
	Actor state.Actor
}

// ParticipantPage is ListParticipants' result — a slice, not a cursor page: a run's roster is bounded by
// its own task/stage counts, never large enough to need paging (unlike MessagePage/BoardPage).
type ParticipantPage struct {
	Participants []agentcomm.Participant
}

// OpenThreadRequest's Members is the thread's FIXED member set (agentcomm.Thread's own doc comment).
// Members not yet present in the mesh roster (a stage agent or helper addressed for the first time) are
// registered as a side effect of opening the thread — see ensureParticipantEffects.
type OpenThreadRequest struct {
	OperationID string
	Title       string
	Scope       agentcomm.Scope
	Members     []agentcomm.ParticipantID
	MaxMessages int
	Actor       state.Actor
}

type CloseThreadRequest struct {
	OperationID string
	ThreadID    agentcomm.ThreadID
	Actor       state.Actor
}

// SendRequest.Sender is the message's author. It MUST be set by the authenticated caller — the Phase 3
// HTTP/CLI layer resolves it from the per-invocation bearer token (agent) or the human session, never from
// an attacker-controlled request body field — mirroring discussion.go's PostMessageRequest.AuthorID.
type SendRequest struct {
	OperationID  string
	ThreadID     agentcomm.ThreadID
	Sender       agentcomm.ParticipantID
	Recipients   []agentcomm.ParticipantID
	Kind         agentcomm.MessageKind
	InReplyTo    *agentcomm.MessageID
	Body         string
	ArtifactRefs []string
	Actor        state.Actor
}

type SendReceipt struct {
	MessageID agentcomm.MessageID
	Seq       uint64
}

type ReadInboxRequest struct {
	Participant agentcomm.ParticipantID
	Cursor      uint64
	Limit       int
	Actor       state.Actor
}

type ReadThreadRequest struct {
	ThreadID agentcomm.ThreadID
	Reader   agentcomm.ParticipantID
	Cursor   uint64
	Limit    int
	Actor    state.Actor
}

// MessagePage is a cursor page over a thread or an inbox: NextCursor is the Seq to pass as the next call's
// Cursor, zero when there is nothing further.
type MessagePage struct {
	Messages   []agentcomm.Message
	NextCursor uint64
}

type AcknowledgeRequest struct {
	OperationID string
	MessageID   agentcomm.MessageID
	Participant agentcomm.ParticipantID
	Actor       state.Actor
}

type WaitRequest struct {
	OperationID      string
	Waiter           agentcomm.ParticipantID
	Condition        agentcomm.WaitCondition
	Deadline         *time.Time
	ContinuationNote string
	Actor            state.Actor
}

type WaitReceipt struct {
	WaitID agentcomm.WaitID
	Status agentcomm.WaitStatus
}

type CancelWaitRequest struct {
	OperationID string
	WaitID      agentcomm.WaitID
	Reason      string
	Actor       state.Actor
}

type PublishRequest struct {
	OperationID     string
	Author          agentcomm.ParticipantID
	Key             string
	Status          string
	ArtifactRefs    []string
	ExpectedVersion int
	AuthorizedBy    []agentcomm.ParticipantID
	Actor           state.Actor
}

// ReadBoardRequest's Key, when empty, returns every entry on the board.
type ReadBoardRequest struct {
	Key   string
	Actor state.Actor
}

type BoardPage struct {
	Entries []agentcomm.BoardEntry
}

// deriveMeshID computes a deterministic, path-safe id from an operation id and a namespace — the same
// "SAME OperationID always yields the SAME id" rationale as discussion.go's deriveMessageID, kept as one
// shared helper (with a namespace argument) rather than one copy per entity kind.
func deriveMeshID(namespace, operationID string) string {
	sum := sha256.Sum256([]byte(namespace + "|" + operationID))
	return hex.EncodeToString(sum[:])[:20]
}

func deriveMeshThreadID(operationID string) agentcomm.ThreadID {
	return agentcomm.ThreadID("mt-" + deriveMeshID("mesh-thread", operationID))
}

func deriveMeshMessageID(operationID string) agentcomm.MessageID {
	return agentcomm.MessageID("mm-" + deriveMeshID("mesh-message", operationID))
}

func deriveMeshWaitID(operationID string) agentcomm.WaitID {
	return agentcomm.WaitID("mw-" + deriveMeshID("mesh-wait", operationID))
}

// marshalEffect builds one state.Effect from a kind, target and a JSON-serializable payload — the one place
// every agentcomm_*.go file turns a typed payload into the Effect shape ApplyRunOperation expects, instead
// of repeating json.Marshal+Effect{} at each call site (mirrors discussion.go's discussionMessageEffect,
// generalized across the several mesh effect kinds).
func marshalEffect(kind string, target state.Target, payload any) (state.Effect, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return state.Effect{}, fmt.Errorf("marshal %s payload: %w", kind, err)
	}
	return state.Effect{Kind: kind, Target: target, Payload: raw}, nil
}
