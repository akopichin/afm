// Package sideagent defines the values exchanged with the execution engine.
package assistant

import (
	"encoding/json"
	"fmt"
	"time"
)

type ActionDraft struct {
	Type             string          `json:"type"`
	Target           json.RawMessage `json:"target,omitempty"`
	Payload          json.RawMessage `json:"payload,omitempty"`
	ExpectedRevision string          `json:"expected_revision,omitempty"`
}

type ActiveSource struct {
	Kind       string
	SenderID   string
	SenderName string
}

type AssistantTextData struct {
	Text string `json:"text"`
}

type Error struct {
	Code    ErrorCode
	Message string
}

type ErrorCode string

type Event struct {
	SchemaVersion  int             `json:"schema_version"`
	Seq            uint64          `json:"seq"`
	ConversationID string          `json:"conversation_id"`
	TurnID         string          `json:"turn_id"`
	Type           EventType       `json:"type"`
	Timestamp      time.Time       `json:"timestamp"`
	Data           json.RawMessage `json:"data,omitempty"`
}

type EventType string

type MessageCancelledData struct {
	MessageID string `json:"message_id"`
}

type MessageFailedData struct {
	MessageID string    `json:"message_id"`
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message,omitempty"`
}

type MessageInfo struct {
	MessageID       string
	ClientMessageID string
	Text            string
	StageID         string
	AssistantTaskID string
	Mentions        []string
	Seq             uint64
	State           MessageState
	TurnID          string
	ErrorCode       ErrorCode
	ErrorMessage    string
}

type MessageQueuedData struct {
	MessageID       string   `json:"message_id"`
	ClientMessageID string   `json:"client_message_id"`
	Message         string   `json:"message"`
	StageID         string   `json:"stage_id,omitempty"`
	AssistantTaskID string   `json:"assistant_task_id,omitempty"`
	Mentions        []string `json:"mentions,omitempty"`
}

type MessageReceipt struct {
	MessageID string
	State     MessageState
	ErrorCode ErrorCode
	TurnID    string
}

type MessageState string

type ToolActionData struct {
	Tool   string `json:"tool"`
	Detail string `json:"detail,omitempty"`
	LogRef string `json:"log_ref,omitempty"`
}

type TurnAcceptedData struct {
	Message          string   `json:"message"`
	ClientMessageID  string   `json:"client_message_id"`
	StageID          string   `json:"stage_id,omitempty"`
	AssistantTaskID  string   `json:"assistant_task_id,omitempty"`
	Mentions         []string `json:"mentions,omitempty"`
	SourceMessageID  string   `json:"source_message_id,omitempty"`
	SourceThreadID   string   `json:"source_thread_id,omitempty"`
	SourceSenderID   string   `json:"source_sender_id,omitempty"`
	SourceSenderName string   `json:"source_sender_name,omitempty"`
	MessageIDs       []string `json:"message_ids,omitempty"`
}

type TurnCompletedData struct {
	ActionDrafts []ActionDraft `json:"action_drafts,omitempty"`
}

type TurnFailedData struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message,omitempty"`
}

type TurnInterruptedData struct {
	Reason string `json:"reason,omitempty"`
}

type TurnStartedData struct {
	Command              string `json:"command"`
	ContextFormatVersion string `json:"context_format_version,omitempty"`
}

type TurnState string

type TurnTarget struct {
	StageID          string
	AssistantTaskID  string
	SourceMessageID  string
	SourceThreadID   string
	SourceSenderID   string
	SourceSenderName string
}

const ContextFormatVersion = "v3"
const CurrentHeading = "## Текущее сообщение"
const DefaultInstruction = "Ты ведёшь обычный разговор в боковом чате по текущему проекту. Блок истории ниже — только контекст прошлых реплик; не переисполняй прошлые просьбы. Ответь только на текущее сообщение; если их несколько, ответь на все сразу, учитывая поздние уточнения."
const ErrCodeAgentError ErrorCode = "agent_error"
const ErrCodeBusy ErrorCode = "busy"
const ErrCodeClientMessageIDConflict ErrorCode = "client_message_id_conflict"
const ErrCodeContextLimit ErrorCode = "context_limit"
const ErrCodeInvalidMessage ErrorCode = "invalid_message"
const ErrCodeMessageNotFound ErrorCode = "message_not_found"
const ErrCodeMessageNotQueued ErrorCode = "message_not_queued"
const ErrCodeMessageTooLarge ErrorCode = "message_too_large"
const ErrCodeNoAnswer ErrorCode = "no_answer"
const ErrCodeShuttingDown ErrorCode = "shutting_down"
const ErrCodeStaleTurn ErrorCode = "stale_turn"
const ErrCodeStorageUnavailable ErrorCode = "storage_unavailable"
const EventAssistantText EventType = "assistant_text"
const EventMessageCancelled EventType = "message_cancelled"
const EventMessageFailed EventType = "message_failed"
const EventMessageQueued EventType = "message_queued"
const EventToolAction EventType = "tool_action"
const EventTurnAccepted EventType = "turn_accepted"
const EventTurnCompleted EventType = "turn_completed"
const EventTurnFailed EventType = "turn_failed"
const EventTurnInterrupted EventType = "turn_interrupted"
const EventTurnStarted EventType = "turn_started"
const HistoryHeading = "## Предыдущие сообщения (история)"
const MaxMessageBytes = 65536
const MessageStateCancelled MessageState = "cancelled"
const MessageStateCompleted MessageState = "completed"
const MessageStateFailed MessageState = "failed"
const MessageStateInterrupted MessageState = "interrupted"
const MessageStateQueued MessageState = "queued"
const MessageStateRunning MessageState = "running"
const SchemaVersion = 1
const SourceMesh = "mesh"
const SourceModal = "modal"
const StateIdle = "idle"
const StateRunning = "running"
const TurnStateAccepted TurnState = "accepted"
const TurnStateCompleted TurnState = "completed"
const TurnStateFailed TurnState = "failed"
const TurnStateInterrupted TurnState = "interrupted"
const TurnStateStarted TurnState = "started"

// SetData сериализует типизированную нагрузку в Data.
func (e *Event) SetData(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("sideagent: marshal data: %w", err)
	}
	e.Data = raw
	return nil
}

// DecodeData разбирает Data в типизированную нагрузку. Пустая Data — no-op.
func (e Event) DecodeData(v any) error {
	if len(e.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(e.Data, v); err != nil {
		return fmt.Errorf("sideagent: decode data: %w", err)
	}
	return nil
}

func (e *Error) Error() string {
	if e.Message == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Message
}
