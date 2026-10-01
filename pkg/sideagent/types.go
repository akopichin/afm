// Package sideagent реализует «боковой» разговор с агентом, изолированный от
// основной FSM потока: у него собственный журнал (chat.jsonl), собственная
// нумерация seq и собственный учёт стоимости. Один разговор на run_id, один
// активный запрос за раз.
//
// Этот файл (A1) описывает схему событий журнала, типизированные нагрузки,
// коды ошибок и конечный автомат состояний одного «хода» (turn).
package sideagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SchemaVersion — текущая версия схемы события. Любое другое значение в журнале
// считается нераспознанным и приводит к явной ошибке декодирования (не к тихому
// zero-value).
const SchemaVersion = 1

// EventType — тип записи журнала разговора.
type EventType string

const (
	EventTurnAccepted    EventType = "turn_accepted"
	EventTurnStarted     EventType = "turn_started"
	EventAssistantText   EventType = "assistant_text"
	EventToolAction      EventType = "tool_action"
	EventTurnCompleted   EventType = "turn_completed"
	EventTurnFailed      EventType = "turn_failed"
	EventTurnInterrupted EventType = "turn_interrupted"
)

// knownEventTypes — множество допустимых типов. Неизвестный тип → ошибка.
var knownEventTypes = map[EventType]struct{}{
	EventTurnAccepted:    {},
	EventTurnStarted:     {},
	EventAssistantText:   {},
	EventToolAction:      {},
	EventTurnCompleted:   {},
	EventTurnFailed:      {},
	EventTurnInterrupted: {},
}

// Valid сообщает, входит ли тип в известный каталог.
func (t EventType) Valid() bool {
	_, ok := knownEventTypes[t]
	return ok
}

// Event — конверт одной записи журнала. Форма фиксирована:
// {schema_version, seq, conversation_id, turn_id, type, timestamp, data}.
type Event struct {
	SchemaVersion  int             `json:"schema_version"`
	Seq            uint64          `json:"seq"`
	ConversationID string          `json:"conversation_id"`
	TurnID         string          `json:"turn_id"`
	Type           EventType       `json:"type"`
	Timestamp      time.Time       `json:"timestamp"`
	Data           json.RawMessage `json:"data,omitempty"`
}

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

// Encode сериализует событие в одну JSON-строку (без завершающего '\n').
func (e Event) Encode() ([]byte, error) {
	return json.Marshal(e)
}

// Ошибки декодирования конверта. Нераспознанные type/schema_version обязаны
// давать явную ошибку, а не молчаливый нулевой результат.
var (
	ErrUnknownEventType     = errors.New("sideagent: unknown event type")
	ErrUnknownSchemaVersion = errors.New("sideagent: unknown schema version")
)

// DecodeEvent разбирает одну строку журнала в Event, валидируя schema_version и
// type. Неизвестные значения → явная ошибка.
func DecodeEvent(line []byte) (Event, error) {
	dec := json.NewDecoder(bytes.NewReader(line))
	var e Event
	if err := dec.Decode(&e); err != nil {
		return Event{}, fmt.Errorf("sideagent: decode event: %w", err)
	}
	if e.SchemaVersion != SchemaVersion {
		return Event{}, fmt.Errorf("%w: %d", ErrUnknownSchemaVersion, e.SchemaVersion)
	}
	if !e.Type.Valid() {
		return Event{}, fmt.Errorf("%w: %q", ErrUnknownEventType, e.Type)
	}
	return e, nil
}

// --- Типизированные нагрузки событий ---

// TurnAcceptedData несёт исходный текст сообщения пользователя и клиентский id
// для дедупликации. Первое turn_accepted фиксирует начало разговора; его
// conversation_id живёт в конверте.
type TurnAcceptedData struct {
	Message         string `json:"message"`
	ClientMessageID string `json:"client_message_id"`
}

// TurnStartedData — запись запроса: фактически использованная команда агента и
// версия формата контекста, которым был собран промпт.
type TurnStartedData struct {
	Command              string `json:"command"`
	ContextFormatVersion string `json:"context_format_version,omitempty"`
}

// AssistantTextData — проза агента (видимый ответ).
type AssistantTextData struct {
	Text string `json:"text"`
}

// ToolActionData — компактная запись действия инструмента со ссылкой на
// подробный лог хода.
type ToolActionData struct {
	Tool   string `json:"tool"`
	Detail string `json:"detail,omitempty"`
	LogRef string `json:"log_ref,omitempty"`
}

// TurnCompletedData — успешное завершение хода (пока без полезной нагрузки).
type TurnCompletedData struct{}

// TurnFailedData — отказ хода с типизированным кодом.
type TurnFailedData struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message,omitempty"`
}

// TurnInterruptedData — прерывание хода (shutdown/recovery).
type TurnInterruptedData struct {
	Reason string `json:"reason,omitempty"`
}

// ErrorCode — стабильный код отказа запроса (часть контракта API).
type ErrorCode string

const (
	ErrCodeBusy               ErrorCode = "busy"
	ErrCodeInvalidMessage     ErrorCode = "invalid_message"
	ErrCodeMessageTooLarge    ErrorCode = "message_too_large"
	ErrCodeContextLimit       ErrorCode = "context_limit"
	ErrCodeShuttingDown       ErrorCode = "shutting_down"
	ErrCodeStorageUnavailable ErrorCode = "storage_unavailable"
	ErrCodeStaleTurn          ErrorCode = "stale_turn"
)

// --- Конечный автомат состояний хода ---

// TurnState — состояние одного хода разговора.
type TurnState string

const (
	TurnStateAccepted    TurnState = "accepted"
	TurnStateStarted     TurnState = "started"
	TurnStateCompleted   TurnState = "completed"
	TurnStateFailed      TurnState = "failed"
	TurnStateInterrupted TurnState = "interrupted"
)

// turnTransitions — разрешённые переходы. accepted→started→terminal — обычный
// путь; accepted→failed покрывает отказ при сборке контекста (например,
// context_limit) до старта агента; accepted→interrupted — recovery хода,
// принятого, но не стартовавшего до краха.
var turnTransitions = map[TurnState][]TurnState{
	TurnStateAccepted: {TurnStateStarted, TurnStateFailed, TurnStateInterrupted},
	TurnStateStarted:  {TurnStateCompleted, TurnStateFailed, TurnStateInterrupted},
}

// IsTerminal сообщает, является ли состояние конечным.
func (s TurnState) IsTerminal() bool {
	switch s {
	case TurnStateCompleted, TurnStateFailed, TurnStateInterrupted:
		return true
	default:
		return false
	}
}

// CanTransitionTo проверяет допустимость перехода s→to.
func (s TurnState) CanTransitionTo(to TurnState) bool {
	for _, allowed := range turnTransitions[s] {
		if allowed == to {
			return true
		}
	}
	return false
}

// TurnState возвращает состояние хода, в которое переводит FSM-событие. Для
// content-событий (assistant_text/tool_action) возвращает ok=false — они не
// меняют состояние хода.
func (t EventType) TurnState() (TurnState, bool) {
	switch t {
	case EventTurnAccepted:
		return TurnStateAccepted, true
	case EventTurnStarted:
		return TurnStateStarted, true
	case EventTurnCompleted:
		return TurnStateCompleted, true
	case EventTurnFailed:
		return TurnStateFailed, true
	case EventTurnInterrupted:
		return TurnStateInterrupted, true
	default:
		return "", false
	}
}
