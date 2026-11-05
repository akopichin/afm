package sideagent

import (
	"errors"
	"fmt"
)

// Состояние разговора целиком (не хода и не сообщения).
const (
	StateIdle    = "idle"
	StateRunning = "running"
)

// MessageState — состояние одного сообщения очереди: queued → running → completed|failed|interrupted
// (по исходу хода) либо queued → cancelled|failed.
type MessageState string

const (
	MessageStateQueued      MessageState = "queued"
	MessageStateRunning     MessageState = "running"
	MessageStateCompleted   MessageState = "completed"
	MessageStateFailed      MessageState = "failed"
	MessageStateInterrupted MessageState = "interrupted"
	MessageStateCancelled   MessageState = "cancelled"
)

// Источник хода: пользователь из модалки или Mesh-запрос другого участника.
const (
	SourceModal = "modal"
	SourceMesh  = "mesh"
)

// Ошибки валидации событий очереди; оборачивают ErrInvalidTransition.
var (
	ErrMessageNotFound  = errors.New("sideagent: message not found")
	ErrMessageNotQueued = errors.New("sideagent: message is not queued")
)

// ActiveSource описывает, чей запрос выполняет активный ход.
type ActiveSource struct {
	Kind       string
	SenderID   string
	SenderName string
}

// MessageInfo — проекция одного сообщения очереди.
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

// ClientRecord — принятое ранее обращение с client_message_id: сообщение очереди (MessageID) или
// старый/Mesh-ход (TurnID). Нужен для идемпотентности повторов.
type ClientRecord struct {
	MessageID       string
	TurnID          string
	Text            string
	StageID         string
	AssistantTaskID string
	SourceMessageID string
	SourceThreadID  string
	SourceSenderID  string
}

type turnInfo struct {
	state      TurnState
	source     ActiveSource
	messageIDs []string
}

// Conversation — единственный fold журнала разговора: Store применяет его на replay и после fsync, он же
// проверяет новые события, а ProjectConversation даёт тот же результат для снимка и идемпотентности.
type Conversation struct {
	turns        map[string]*turnInfo
	turnOrder    []string
	messages     map[string]*MessageInfo
	messageOrder []string
	clients      map[string]ClientRecord
}

func newConversation() *Conversation {
	return &Conversation{
		turns:    make(map[string]*turnInfo),
		messages: make(map[string]*MessageInfo),
		clients:  make(map[string]ClientRecord),
	}
}

// ProjectConversation строит проекцию по срезу журнала.
func ProjectConversation(history []Event) *Conversation {
	c := newConversation()
	for _, e := range history {
		c.apply(e)
	}
	return c
}

// apply учитывает уже записанное событие. Некорректные данные пропускаются: replay не должен падать на
// записи, которую валидация не пропустила бы.
func (c *Conversation) apply(e Event) {
	switch e.Type {
	case EventMessageQueued:
		c.applyQueued(e)
	case EventMessageCancelled:
		var d MessageCancelledData
		if e.DecodeData(&d) == nil {
			c.finishQueued(d.MessageID, MessageStateCancelled, "", "")
		}
	case EventMessageFailed:
		var d MessageFailedData
		if e.DecodeData(&d) == nil {
			c.finishQueued(d.MessageID, MessageStateFailed, d.Code, d.Message)
		}
	case EventTurnAccepted:
		c.applyAccepted(e)
	case EventTurnStarted, EventTurnCompleted, EventTurnFailed, EventTurnInterrupted:
		c.applyTurnState(e)
	default:
		// content-события проекцию не меняют
	}
}

func (c *Conversation) applyQueued(e Event) {
	var d MessageQueuedData
	if e.DecodeData(&d) != nil || d.MessageID == "" {
		return
	}
	if _, dup := c.messages[d.MessageID]; dup {
		return
	}
	c.messages[d.MessageID] = &MessageInfo{
		MessageID: d.MessageID, ClientMessageID: d.ClientMessageID, Text: d.Message,
		StageID: d.StageID, AssistantTaskID: d.AssistantTaskID, Mentions: d.Mentions,
		Seq: e.Seq, State: MessageStateQueued,
	}
	c.messageOrder = append(c.messageOrder, d.MessageID)
	c.addClient(d.ClientMessageID, ClientRecord{
		MessageID: d.MessageID, Text: d.Message, StageID: d.StageID, AssistantTaskID: d.AssistantTaskID,
	})
}

// finishQueued переводит ожидающее сообщение в терминальное состояние; у уже решённого выигрывает первая запись.
func (c *Conversation) finishQueued(id string, to MessageState, code ErrorCode, msg string) {
	m, ok := c.messages[id]
	if !ok || m.State != MessageStateQueued {
		return
	}
	m.State, m.ErrorCode, m.ErrorMessage = to, code, msg
}

func (c *Conversation) applyAccepted(e Event) {
	var d TurnAcceptedData
	if e.DecodeData(&d) != nil {
		return
	}
	info := c.turnFor(e.TurnID)
	info.state = TurnStateAccepted
	info.source = ActiveSource{Kind: SourceModal}
	if d.SourceMessageID != "" {
		info.source = ActiveSource{Kind: SourceMesh, SenderID: d.SourceSenderID, SenderName: d.SourceSenderName}
	}
	for _, id := range d.MessageIDs {
		if m, ok := c.messages[id]; ok && m.State == MessageStateQueued {
			m.State, m.TurnID = MessageStateRunning, e.TurnID
			info.messageIDs = append(info.messageIDs, id)
		}
	}
	c.addClient(d.ClientMessageID, ClientRecord{
		TurnID: e.TurnID, Text: d.Message, StageID: d.StageID, AssistantTaskID: d.AssistantTaskID,
		SourceMessageID: d.SourceMessageID, SourceThreadID: d.SourceThreadID, SourceSenderID: d.SourceSenderID,
	})
}

func (c *Conversation) applyTurnState(e Event) {
	st, _ := e.Type.TurnState()
	info := c.turnFor(e.TurnID)
	info.state = st
	if !st.IsTerminal() {
		return
	}
	to := map[TurnState]MessageState{
		TurnStateCompleted: MessageStateCompleted, TurnStateFailed: MessageStateFailed,
		TurnStateInterrupted: MessageStateInterrupted,
	}[st]
	var failure TurnFailedData
	if st == TurnStateFailed {
		_ = e.DecodeData(&failure) // без данных код остаётся пустым
	}
	for _, id := range info.messageIDs {
		if m := c.messages[id]; m.State == MessageStateRunning {
			m.State, m.ErrorCode, m.ErrorMessage = to, failure.Code, failure.Message
		}
	}
}

func (c *Conversation) turnFor(turnID string) *turnInfo {
	info, ok := c.turns[turnID]
	if !ok {
		info = &turnInfo{}
		c.turns[turnID] = info
		c.turnOrder = append(c.turnOrder, turnID)
	}
	return info
}

func (c *Conversation) addClient(clientID string, rec ClientRecord) {
	if clientID == "" {
		return
	}
	if _, ok := c.clients[clientID]; !ok {
		c.clients[clientID] = rec
	}
}

// validate проверяет закодированное событие до записи в журнал.
func (c *Conversation) validate(e Event) error {
	switch e.Type {
	case EventMessageQueued, EventMessageCancelled, EventMessageFailed:
		return c.validateMessageEvent(e)
	case EventTurnAccepted:
		return c.validateAccepted(e)
	}
	return c.validateTurnEvent(e)
}

func (c *Conversation) validateMessageEvent(e Event) error {
	if e.TurnID != "" {
		return fmt.Errorf("%w: %s must not carry turn %q", ErrInvalidTransition, e.Type, e.TurnID)
	}
	var id string
	switch e.Type {
	case EventMessageQueued:
		var d MessageQueuedData
		if err := e.DecodeData(&d); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidTransition, err)
		}
		if d.MessageID == "" {
			return fmt.Errorf("%w: message_queued without message_id", ErrInvalidTransition)
		}
		if _, dup := c.messages[d.MessageID]; dup {
			return fmt.Errorf("%w: message %q already exists", ErrInvalidTransition, d.MessageID)
		}
		return nil
	case EventMessageCancelled:
		var d MessageCancelledData
		if err := e.DecodeData(&d); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidTransition, err)
		}
		id = d.MessageID
	default:
		var d MessageFailedData
		if err := e.DecodeData(&d); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidTransition, err)
		}
		id = d.MessageID
	}
	return c.requireQueued(id)
}

func (c *Conversation) requireQueued(id string) error {
	m, ok := c.messages[id]
	if !ok {
		return fmt.Errorf("%w: message %q: %w", ErrInvalidTransition, id, ErrMessageNotFound)
	}
	if m.State != MessageStateQueued {
		return fmt.Errorf("%w: message %q is %s: %w", ErrInvalidTransition, id, m.State, ErrMessageNotQueued)
	}
	return nil
}

func (c *Conversation) validateAccepted(e Event) error {
	if _, exists := c.turns[e.TurnID]; exists {
		return fmt.Errorf("%w: turn %q already exists", ErrInvalidTransition, e.TurnID)
	}
	var d TurnAcceptedData
	if err := e.DecodeData(&d); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidTransition, err)
	}
	if len(d.MessageIDs) == 0 {
		return nil
	}
	queue := c.Queued()
	for i, id := range d.MessageIDs {
		if err := c.requireQueued(id); err != nil {
			return err
		}
		if i >= len(queue) || queue[i].MessageID != id {
			return fmt.Errorf("%w: message %q is not the next in the queue", ErrInvalidTransition, id)
		}
		if m := c.messages[id]; m.StageID != d.StageID || m.AssistantTaskID != d.AssistantTaskID {
			return fmt.Errorf("%w: message %q has another context than the turn", ErrInvalidTransition, id)
		}
	}
	return nil
}

// validateTurnEvent проверяет допустимость FSM- и content-событий хода (кроме приёма).
func (c *Conversation) validateTurnEvent(e Event) error {
	info, exists := c.turns[e.TurnID]
	newState, isFSM := e.Type.TurnState()
	if !isFSM {
		// content-событие допустимо только для активного (started) хода
		if !exists || info.state != TurnStateStarted {
			return fmt.Errorf("%w: %s on turn %q in state %q", ErrInvalidTransition, e.Type, e.TurnID, c.stateOf(e.TurnID))
		}
		return nil
	}
	if !exists {
		return fmt.Errorf("%w: turn %q not found for %s", ErrInvalidTransition, e.TurnID, e.Type)
	}
	if !info.state.CanTransitionTo(newState) {
		return fmt.Errorf("%w: %s->%s on turn %q", ErrInvalidTransition, info.state, newState, e.TurnID)
	}
	return nil
}

func (c *Conversation) stateOf(turnID string) TurnState {
	if info, ok := c.turns[turnID]; ok {
		return info.state
	}
	return ""
}

// --- Чтение проекции ---

// TurnState возвращает состояние хода.
func (c *Conversation) TurnState(turnID string) (TurnState, bool) {
	info, ok := c.turns[turnID]
	if !ok {
		return "", false
	}
	return info.state, true
}

// ActiveTurnID — последний по порядку ход, если он не терминален; иначе пусто.
func (c *Conversation) ActiveTurnID() string {
	if len(c.turnOrder) == 0 {
		return ""
	}
	last := c.turnOrder[len(c.turnOrder)-1]
	if c.turns[last].state.IsTerminal() {
		return ""
	}
	return last
}

// State — StateRunning при активном ходе, иначе StateIdle (очередь на него не влияет).
func (c *Conversation) State() string {
	if c.ActiveTurnID() != "" {
		return StateRunning
	}
	return StateIdle
}

// ActiveSource — чей запрос выполняет активный ход; nil без активного хода.
func (c *Conversation) ActiveSource() *ActiveSource {
	id := c.ActiveTurnID()
	if id == "" {
		return nil
	}
	src := c.turns[id].source
	return &src
}

// Message возвращает проекцию сообщения очереди.
func (c *Conversation) Message(id string) (MessageInfo, bool) {
	m, ok := c.messages[id]
	if !ok {
		return MessageInfo{}, false
	}
	return *m, true
}

// Client ищет принятое ранее обращение по client_message_id.
func (c *Conversation) Client(clientID string) (ClientRecord, bool) {
	rec, ok := c.clients[clientID]
	return rec, ok
}

// Queued возвращает ожидающие сообщения в порядке приёма.
func (c *Conversation) Queued() []MessageInfo {
	var out []MessageInfo
	for _, id := range c.messageOrder {
		if m := c.messages[id]; m.State == MessageStateQueued {
			out = append(out, *m)
		}
	}
	return out
}

// QueuedGroup — начальная группа очереди с одним контекстом (стадия + задача помощника).
func (c *Conversation) QueuedGroup() []MessageInfo {
	queue := c.Queued()
	n := 0
	for n < len(queue) && queue[n].StageID == queue[0].StageID && queue[n].AssistantTaskID == queue[0].AssistantTaskID {
		n++
	}
	return queue[:n]
}
