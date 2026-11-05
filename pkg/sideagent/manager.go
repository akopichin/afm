package sideagent

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/redact"
)

// Константы бокового разговора.
const (
	// MaxMessageBytes — верхний предел размера ОДНОГО пользовательского сообщения. Экспортирован: C1 маппит
	// превышение в HTTP.
	MaxMessageBytes = 64 * 1024

	// subBufferSize — буфер канала одного подписчика.
	subBufferSize = 32

	sideAgentType = "side"
	sideStageName = "side-agent"
	turnLogName   = "agent.log"
)

// Коды отказа ХОДА (turn_failed), дополняющие admission-коды из types.go.
// Это коды времени выполнения агента, а не валидации запроса.
const (
	// ErrCodeAgentError — сам запуск/процесс агента завершился ошибкой.
	ErrCodeAgentError ErrorCode = "agent_error"
	// ErrCodeNoAnswer — штатный выход агента без единого видимого ответа:
	// явная протокольная ошибка, а не пустой «успех».
	ErrCodeNoAnswer ErrorCode = "no_answer"
)

// Error — типизированная ошибка Send/Stop со стабильным кодом (часть контракта;
// C1 маппит Code в HTTP-статус).
type Error struct {
	Code    ErrorCode
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Message
}

func newErr(code ErrorCode, msg string) *Error { return &Error{Code: code, Message: msg} }

// Config настраивает Manager. Store обязателен; остальное — опционально.
type Config struct {
	// Store — журнал разговора (владение жизненным циклом у вызывающего).
	Store *Store
	// Command — имя команды агента (claude и др.), уходит в executor.Config и в
	// событие turn_started.
	Command string
	// ExtraArgs/Dir/WrapperDir/IdleTimeout — проброс в executor.Config.
	ExtraArgs   []string
	Dir         string
	WrapperDir  string
	IdleTimeout time.Duration
	// Reasoning — client.reasoning; уходит в executor.Config. Executor эмитит claude --effort только для
	// дефолтной команды claude, прочие команды его игнорируют.
	Reasoning string
	// Context — сборщик промпта из истории + текущего сообщения.
	Context ContextBuilder
	// HideSource keeps the history of a source message out of every later prompt; it becomes Context.HideSource.
	HideSource func(sourceMessageID string) bool
	// Secrets — поставщик значений секретов для редакции пользовательского текста. nil → без редакции.
	Secrets func() []string
	// MentionResolve — резолвит "@word", извлечённое из текста сообщения, в нормализованный id профиля
	// помощника (narrow-функция вместо *assistants.Registry — pkg/sideagent не импортирует pkg/assistants,
	// тот же приём, что Secrets/OnUsage ниже). nil → mentions никогда не записываются (T4.1/T4.3 — opt-in).
	MentionResolve func(word string) (profileID string, ok bool)
	// KnownActionType reports whether an action draft's Type (extracted from the turn's final narration,
	// T4.4) is recognized — narrow function instead of importing pkg/assistants, same injection pattern as
	// MentionResolve above. nil fails CLOSED: no action draft is ever recorded (extractActionDrafts's own
	// nil-predicate convention).
	KnownActionType func(typ string) bool
	// MeshClient is resolved for each invocation, after the dashboard has bound its URL.
	MeshClient func() *executor.MeshClient
	// OnSourceDelivered runs only after the executor starts a sourced turn.
	OnSourceDelivered func(sourceMessageID string)
	// SourceReply returns a durable explicit Mesh reply to the sourced message.
	// It is consulted only when a successful turn emitted no assistant text.
	SourceReply func(sourceMessageID string) (string, bool)
	// OnUsage — приёмник учёта стоимости одного хода. nil → учёт выключен.
	OnUsage func(accounting.Observation)
	// Job measures every turn as an agent job in the Assistant's own journal. nil → turns are not measured.
	Job *executor.JobConfig
	// NewExecutor — фабрика AgentRunner из executor.Config (тест-шов). nil →
	// NewExecutorRunner (реальный executor).
	NewExecutor func(executor.Config) AgentRunner
}

// activeTurn — состояние единственного активного хода.
type activeTurn struct {
	turnID    string
	interrupt chan struct{} // сигнал Stop именно этому ходу (→ SIGINT)
	answered  atomic.Bool   // был ли хоть один ВИДИМЫЙ И ПЕРСИСТНУТЫЙ ответ
	storeFail atomic.Bool   // durable-запись действия провалилась — ход остановлен
	meshToken atomic.Value  // token minted by this invocation, retained for redacting terminal errors after revoke
}

// MessageReceipt — результат приёма или отмены сообщения очереди. MessageID пуст, когда повтор попал на
// старый/Mesh-ход (у него нет записи очереди); TurnID заполнен, пока сообщение забрано ходом.
type MessageReceipt struct {
	MessageID string
	State     MessageState
	ErrorCode ErrorCode
	TurnID    string
}

// Manager владеет единственным активным ходом бокового разговора: durable-first приём, запуск агента под
// ПРОЦЕССНЫМ ctx, доставку событий подписчикам и ограниченный Drain для shutdown.
type Manager struct {
	store             *Store
	command           string
	args              []string
	dir               string
	wrapperDir        string
	idleTimeout       time.Duration
	reasoning         string
	ctxBuilder        ContextBuilder
	secrets           func() []string
	onUsage           func(accounting.Observation)
	job               *executor.JobConfig
	mentionResolve    func(string) (string, bool)
	knownActionType   func(string) bool
	meshClient        func() *executor.MeshClient
	onSourceDelivered func(string)
	sourceReply       func(string) (string, bool)
	newExecutor       func(executor.Config) AgentRunner

	// ctx — процессный контекст агентских горутин. Отмена запроса (POST/SSE) на
	// него НЕ влияет: ход прерывается только через Stop/Shutdown (InterruptCh).
	ctx context.Context

	// mu защищает admission и диспетчер: активный ход и флаги жизненного цикла. Один активный ход за раз;
	// все запуски ходов идут через dispatchLocked под mu.
	mu     sync.Mutex
	active *activeTurn
	// admissionClosed — приём закрыт (graceful или emergency): очередь при этом дорабатывается.
	admissionClosed bool
	// noStart — emergency: новые ходы не стартуют, не начатая очередь остаётся в журнале.
	noStart bool
	// dispatchEnabled включает исполнение (Start): до него принятые сообщения только лежат в очереди.
	dispatchEnabled bool

	// wg учитывает агентскую горутину (своя, не concurrency.WaitAgents) — Drain
	// сообщает, реально ли дождались завершения.
	wg sync.WaitGroup

	// afterAppend — тест-шов: вызывается в record между durable-записью и публикацией.
	afterAppend func(Event)
	// beforeRelease — тест-шов: вызывается после терминального события хода, до освобождения слота.
	beforeRelease func()

	// recMu делает «durable-запись + публикация» неделимой: подписчики видят события строго в порядке seq
	// независимо от того, держит ли писатель mu.
	recMu sync.Mutex
	// subMu защищает подписчиков.
	subMu     sync.Mutex
	subs      map[int]chan Event
	nextSubID int
}

// New строит Manager. Паникует при nil Store (программерская ошибка вызова).
func New(cfg Config) *Manager {
	if cfg.Store == nil {
		panic("sideagent: Config.Store is nil")
	}
	ctxBuilder := cfg.Context
	ctxBuilder.HideSource = cfg.HideSource
	nx := cfg.NewExecutor
	if nx == nil {
		nx = NewExecutorRunner
	}
	return &Manager{
		store:             cfg.Store,
		command:           cfg.Command,
		args:              cfg.ExtraArgs,
		dir:               cfg.Dir,
		wrapperDir:        cfg.WrapperDir,
		idleTimeout:       cfg.IdleTimeout,
		reasoning:         cfg.Reasoning,
		ctxBuilder:        ctxBuilder,
		secrets:           cfg.Secrets,
		onUsage:           cfg.OnUsage,
		job:               cfg.Job,
		mentionResolve:    cfg.MentionResolve,
		knownActionType:   cfg.KnownActionType,
		meshClient:        cfg.MeshClient,
		onSourceDelivered: cfg.OnSourceDelivered,
		sourceReply:       cfg.SourceReply,
		newExecutor:       nx,
		ctx:               context.Background(),
		subs:              make(map[int]chan Event),
	}
}

// --- Аксессоры для C1 ---

// History возвращает копию журнала разговора.
func (m *Manager) History() []Event { return m.store.History() }

// ConversationID возвращает id разговора (== run_id).
func (m *Manager) ConversationID() string { return m.store.ConversationID() }

// Command возвращает имя команды агента.
func (m *Manager) Command() string { return m.command }

// Unavailable сообщает, заблокирован ли журнал (порча/сбой записи).
func (m *Manager) Unavailable() bool { return m.store.Unavailable() }

// --- Приём: Enqueue / CancelQueued / Send ---

// validateRequest — проверки сообщения, общие для Enqueue и Send; блокировки не нужны.
func (m *Manager) validateRequest(clientMessageID, text string) error {
	if clientMessageID == "" {
		return newErr(ErrCodeInvalidMessage, "client_message_id required")
	}
	if strings.TrimSpace(text) == "" {
		return newErr(ErrCodeInvalidMessage, "message is empty")
	}
	if len(text) > MaxMessageBytes {
		return newErr(ErrCodeMessageTooLarge, fmt.Sprintf("message %d > %d bytes", len(text), MaxMessageBytes))
	}
	if m.store.Unavailable() {
		return newErr(ErrCodeStorageUnavailable, "conversation journal unavailable")
	}
	return nil
}

// sameRequest сообщает, что повтор client_message_id несёт тот же текст и адресат, что исходный запрос.
func sameRequest(rec ClientRecord, text string, target TurnTarget) bool {
	return rec.Text == text && rec.StageID == target.StageID && rec.AssistantTaskID == target.AssistantTaskID &&
		rec.SourceMessageID == target.SourceMessageID && rec.SourceThreadID == target.SourceThreadID &&
		rec.SourceSenderID == target.SourceSenderID
}

// Enqueue durable-принимает сообщение в очередь и сразу пробует запустить ход; принято не значит прочитано.
// Стадия и задача из target фиксируются в message_queued навсегда, а mentions извлекает сам Enqueue.
func (m *Manager) Enqueue(clientMessageID, text string, target TurnTarget) (MessageReceipt, error) {
	if err := m.validateRequest(clientMessageID, text); err != nil {
		return MessageReceipt{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.admissionClosed {
		return MessageReceipt{}, newErr(ErrCodeShuttingDown, "service is shutting down")
	}
	conv := m.store.Conversation()
	if rec, ok := conv.Client(clientMessageID); ok {
		if !sameRequest(rec, text, target) {
			return MessageReceipt{}, newErr(ErrCodeClientMessageIDConflict, "client_message_id reused with a different request")
		}
		return receiptFor(conv, rec), nil
	}

	messageID := newMessageID()
	queued := MessageQueuedData{
		MessageID: messageID, ClientMessageID: clientMessageID, Message: text,
		StageID: target.StageID, AssistantTaskID: target.AssistantTaskID,
		Mentions: extractMentions(text, m.mentionResolve),
	}
	if _, err := m.record(EventMessageQueued, "", queued); err != nil {
		return MessageReceipt{}, newErr(ErrCodeStorageUnavailable, err.Error())
	}
	// Сообщение уже durable: сбой запуска не отменяет приём, ход подберёт следующий dispatch.
	m.dispatchLocked()
	return receiptFor(m.store.Conversation(), ClientRecord{MessageID: messageID}), nil
}

// receiptFor описывает состояние ранее принятого обращения по проекции журнала.
func receiptFor(conv *Conversation, rec ClientRecord) MessageReceipt {
	if rec.MessageID == "" {
		state, _ := conv.TurnState(rec.TurnID)
		return MessageReceipt{State: messageStateOfTurn(state), TurnID: rec.TurnID}
	}
	msg, _ := conv.Message(rec.MessageID)
	return MessageReceipt{MessageID: msg.MessageID, State: msg.State, ErrorCode: msg.ErrorCode, TurnID: msg.TurnID}
}

func messageStateOfTurn(state TurnState) MessageState {
	switch state {
	case TurnStateCompleted:
		return MessageStateCompleted
	case TurnStateFailed:
		return MessageStateFailed
	case TurnStateInterrupted:
		return MessageStateInterrupted
	default:
		return MessageStateRunning
	}
}

// CancelQueued удаляет ожидающее сообщение. Работает под mu вместе с диспетчером, поэтому гонка с claim
// даёт ровно один исход; уже забранное ходом сообщение отменить нельзя. Повтор отмены идемпотентен.
func (m *Manager) CancelQueued(messageID string) (MessageReceipt, error) {
	if m.store.Unavailable() {
		return MessageReceipt{}, newErr(ErrCodeStorageUnavailable, "conversation journal unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	msg, ok := m.store.Conversation().Message(messageID)
	switch {
	case !ok:
		return MessageReceipt{}, newErr(ErrCodeMessageNotFound, "no such message")
	case msg.State == MessageStateCancelled:
		return MessageReceipt{MessageID: messageID, State: MessageStateCancelled}, nil
	case msg.State != MessageStateQueued:
		return MessageReceipt{}, newErr(ErrCodeMessageNotQueued, "message is already "+string(msg.State))
	}
	if _, err := m.record(EventMessageCancelled, "", MessageCancelledData{MessageID: messageID}); err != nil {
		if errors.Is(err, ErrMessageNotQueued) {
			return MessageReceipt{}, newErr(ErrCodeMessageNotQueued, err.Error())
		}
		return MessageReceipt{}, newErr(ErrCodeStorageUnavailable, err.Error())
	}
	return MessageReceipt{MessageID: messageID, State: MessageStateCancelled}, nil
}

// Send принимает Mesh-ход (сообщение другого участника): без очереди, в одиночку и только когда
// исполнитель свободен, диспетчер включён, а очередь пользователя пуста — сообщения пользователя
// приоритетнее. Durable-first: turn_accepted фиксируется (fsync) ДО резервирования активного хода.
func (m *Manager) Send(clientMessageID, text string, target TurnTarget) (string, error) {
	if err := m.validateRequest(clientMessageID, text); err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.admissionClosed {
		return "", newErr(ErrCodeShuttingDown, "service is shutting down")
	}
	history := m.store.History()
	conv := ProjectConversation(history)
	if rec, ok := conv.Client(clientMessageID); ok {
		if rec.TurnID == "" || !sameRequest(rec, text, target) {
			return "", newErr(ErrCodeClientMessageIDConflict, "client_message_id reused with a different request")
		}
		return rec.TurnID, nil // тот же ход, без повторного запуска
	}
	if !m.dispatchEnabled || m.active != nil || len(conv.Queued()) > 0 {
		return "", newErr(ErrCodeBusy, "another request is in progress")
	}

	// Сборка контекста и бюджет ДО приёма — при превышении ничего не пишем.
	accepted := TurnAcceptedData{
		Message: text, ClientMessageID: clientMessageID,
		StageID: target.StageID, AssistantTaskID: target.AssistantTaskID,
		Mentions:        extractMentions(text, m.mentionResolve),
		SourceMessageID: target.SourceMessageID, SourceThreadID: target.SourceThreadID,
		SourceSenderID: target.SourceSenderID, SourceSenderName: target.SourceSenderName,
	}
	prompt, err := m.ctxBuilder.Build(history, accepted)
	if err != nil {
		if errors.Is(err, ErrContextLimit) {
			return "", newErr(ErrCodeContextLimit, err.Error())
		}
		return "", newErr(ErrCodeAgentError, err.Error())
	}

	turnID := newTurnID()
	if _, err := m.record(EventTurnAccepted, turnID, accepted); err != nil {
		return "", newErr(ErrCodeStorageUnavailable, err.Error())
	}
	m.startTurnLocked(turnID, prompt, accepted.SourceMessageID)
	return turnID, nil
}

// --- Диспетчер ---

// dispatchLocked — единственный путь запуска хода из очереди; вызывается под mu после приёма, освобождения
// исполнителя и из Start. Группу забирает одна запись turn_accepted, неподъёмную голову — message_failed.
// Любая ошибка записи прекращает цикл: сообщения остаются в журнале.
func (m *Manager) dispatchLocked() {
	for m.active == nil && m.dispatchEnabled && !m.noStart && !m.store.Unavailable() {
		history := m.store.History()
		group := ProjectConversation(history).QueuedGroup()
		if len(group) == 0 {
			return
		}
		prompt, used, err := m.ctxBuilder.BuildGroup(history, group)
		if err != nil {
			code := ErrCodeAgentError
			if errors.Is(err, ErrContextLimit) {
				code = ErrCodeContextLimit
			}
			failed := MessageFailedData{MessageID: group[0].MessageID, Code: code, Message: err.Error()}
			if _, err := m.record(EventMessageFailed, "", failed); err != nil {
				return
			}
			continue
		}
		group = group[:used]
		turnID := newTurnID()
		accepted := TurnAcceptedData{
			StageID: group[0].StageID, AssistantTaskID: group[0].AssistantTaskID,
			Mentions: groupMentions(group), MessageIDs: messageIDs(group),
		}
		if _, err := m.record(EventTurnAccepted, turnID, accepted); err != nil {
			return
		}
		m.startTurnLocked(turnID, prompt, "")
		return
	}
}

// startTurnLocked резервирует активный ход и запускает его горутину. wg.Add «с нуля» допустим только
// здесь под mu при открытом приёме (Enqueue, Send, Start); из releaseActive счётчик уже ≥ 1.
func (m *Manager) startTurnLocked(turnID, prompt, sourceMessageID string) {
	at := &activeTurn{turnID: turnID, interrupt: make(chan struct{}, 1)}
	m.active = at
	m.wg.Add(1)
	go m.runTurn(at, prompt, sourceMessageID)
}

// Start включает исполнение очереди: конструктор и Open ничего не запускают, пока вызывающий не готов
// (например, пока не известен URL Mesh-клиента). После закрытия приёма не делает ничего.
func (m *Manager) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.admissionClosed {
		return
	}
	m.dispatchEnabled = true
	m.dispatchLocked()
}

func messageIDs(group []MessageInfo) []string {
	ids := make([]string, len(group))
	for i, g := range group {
		ids[i] = g.MessageID
	}
	return ids
}

// groupMentions объединяет упоминания сообщений группы без дублей, в порядке появления.
func groupMentions(group []MessageInfo) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, g := range group {
		for _, mention := range g.Mentions {
			if _, dup := seen[mention]; !dup {
				seen[mention] = struct{}{}
				out = append(out, mention)
			}
		}
	}
	return out
}

// runTurn выполняет ход под процессным ctx менеджера.
func (m *Manager) runTurn(at *activeTurn, prompt, sourceMessageID string) {
	// Порядок defer важен: releaseActive может запустить следующий ход (wg.Add) и должен успеть до wg.Done,
	// иначе Drain увидит нулевой счётчик между ходами очереди.
	defer m.wg.Done()
	defer m.releaseActive(at)

	// Шаг 5: turn_started. Сбой журнала здесь — агента не запускаем.
	if _, err := m.record(EventTurnStarted, at.turnID, TurnStartedData{
		Command:              m.command,
		ContextFormatVersion: ContextFormatVersion,
	}); err != nil {
		return
	}

	logFile, logRef, err := m.turnPaths(at.turnID)
	if err != nil {
		m.record(EventTurnFailed, at.turnID, TurnFailedData{Code: ErrCodeAgentError, Message: m.redactErr(at, err)}) //nolint:errcheck
		return
	}

	// lastText — последний видимый блок прозы агента в этом ходе (перезаписывается, не накапливается —
	// T4.4 читает action_drafts ТОЛЬКО из финальной прозы, не из вывода инструментов и не из более ранних
	// блоков прозы того же хода).
	var lastText string

	// Шаг 6: каждое действие агента → редакция → durable-запись → доставка.
	cfg := executor.Config{
		Command:     m.command,
		ExtraArgs:   m.args,
		IdleTimeout: m.idleTimeout,
		Dir:         m.dir,
		WrapperDir:  m.wrapperDir,
		StageDir:    "", // инвариант 5: без файлового диалогового протокола
		StageID:     m.store.ConversationID(),
		Phase:       sideAgentType,
		InterruptCh: at.interrupt,
		OnUsage:     m.onUsage,
		Job:         m.job,
		Reasoning:   m.reasoning,
		// Read at write time: MeshClient.Mint runs later in executor.run, so
		// this includes the invocation token once stdout/stderr can contain it.
		Secrets: func() []string { return m.secretValues(at) },
		OnAction: func(tool, detail string) {
			// Уже провалилась durable-запись — не плодим новые (ход останавливается).
			if at.storeFail.Load() {
				return
			}
			typ, data := mapAction(tool, detail, logRef, m.secretValues(at))
			if _, err := m.record(typ, at.turnID, data); err != nil {
				// Сбой журнала во время хода: помечаем, мягко прерываем агента
				// (InterruptCh) — ход останавливается, flow FSM не затрагивается.
				at.storeFail.Store(true)
				select {
				case at.interrupt <- struct{}{}:
				default:
				}
				return
			}
			// answered ставим ТОЛЬКО после успешного durable-append — иначе
			// «успех без видимого ответа» не отличить от «ответ был, но не сохранён».
			if typ == EventAssistantText {
				at.answered.Store(true)
				lastText = data.(AssistantTextData).Text
			}
		},
	}
	if m.meshClient != nil {
		cfg.MeshClient = m.meshClient()
		if cfg.MeshClient != nil {
			client := *cfg.MeshClient
			mint := client.Mint
			client.Mint = func() (string, string, func()) {
				url, token, revoke := mint()
				if token != "" {
					at.meshToken.Store(token)
				}
				return url, token, revoke
			}
			cfg.MeshClient = &client
		}
	}
	if sourceMessageID != "" && m.onSourceDelivered != nil {
		cfg.OnDelivered = func() { m.onSourceDelivered(sourceMessageID) }
	}
	runner := m.newExecutor(cfg)
	runErr := runner.RunAgent(m.ctx, sideAgentType, sideStageName, prompt, logFile)

	// Шаг 7: терминальное событие ДО освобождения слота (releaseActive — defer).
	switch {
	case at.storeFail.Load():
		// Durable-запись действия провалилась: исход хода — storage_unavailable (попытка записи best-effort;
		// если журнал мёртв, она тоже не сохранится, но ход уже остановлен, а flow FSM не затронут).
		m.record(EventTurnFailed, at.turnID, TurnFailedData{Code: ErrCodeStorageUnavailable, Message: "conversation journal unavailable"}) //nolint:errcheck
	case errors.Is(runErr, executor.ErrUserInterrupted):
		m.record(EventTurnInterrupted, at.turnID, TurnInterruptedData{Reason: "stopped by user"}) //nolint:errcheck
	case runErr != nil:
		m.record(EventTurnFailed, at.turnID, TurnFailedData{Code: ErrCodeAgentError, Message: m.redactErr(at, runErr)}) //nolint:errcheck
	case !at.answered.Load():
		// An explicit afm agent reply is already visible in the source Mesh thread.
		// Mirror its durable, redacted body into the modal conversation so this
		// turn does not falsely claim the agent gave no answer.
		if sourceMessageID != "" && m.sourceReply != nil {
			if body, ok := m.sourceReply(sourceMessageID); ok && strings.TrimSpace(body) != "" {
				_, data := mapAction(toolText, body, logRef, m.secretValues(at))
				if _, err := m.record(EventAssistantText, at.turnID, data); err == nil {
					m.record(EventTurnCompleted, at.turnID, TurnCompletedData{}) //nolint:errcheck
					return
				}
				return // storage failure is fatal to this conversation
			}
		}
		// A successful process without a visible answer is a protocol failure.
		m.record(EventTurnFailed, at.turnID, TurnFailedData{Code: ErrCodeNoAnswer, Message: "agent produced no visible answer"}) //nolint:errcheck
	default:
		drafts := extractActionDrafts(lastText, m.knownActionType)
		m.record(EventTurnCompleted, at.turnID, TurnCompletedData{ActionDrafts: drafts}) //nolint:errcheck
	}
}

// releaseActive снимает резерв активного хода и сразу запускает следующую группу очереди под тем же mu —
// между ходами нет окна для гонки с новым Enqueue.
func (m *Manager) releaseActive(at *activeTurn) {
	if m.beforeRelease != nil {
		m.beforeRelease()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == at {
		m.active = nil
	}
	m.dispatchLocked()
}

// --- Stop / Shutdown / Drain ---

// Stop мягко прерывает (SIGINT) именно ход turnID. Устаревший id не прерывает
// следующий ход. Частичный ответ и маркер turn_interrupted остаются в истории.
func (m *Manager) Stop(turnID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil || m.active.turnID != turnID {
		return newErr(ErrCodeStaleTurn, "no active turn with that id")
	}
	select {
	case m.active.interrupt <- struct{}{}:
	default: // сигнал уже отправлен
	}
	return nil
}

// CloseAdmission закрывает приём новых сообщений, НЕ прерывая активный ход: уже принятая очередь
// дорабатывается (Drain ждёт её целиком). Отмена ожидающих сообщений остаётся доступной.
func (m *Manager) CloseAdmission() {
	m.mu.Lock()
	m.admissionClosed = true
	m.mu.Unlock()
}

// Shutdown — аварийное завершение: закрывает приём, запрещает запуск новых ходов (не начатая очередь
// остаётся в журнале) и мягко прерывает активный ход. Дожидаться завершения — через Drain.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.admissionClosed = true
	m.noStart = true
	at := m.active
	m.mu.Unlock()
	if at != nil {
		select {
		case at.interrupt <- struct{}{}:
		default:
		}
	}
}

// Drain ждёт завершения активной агентской горутины ИЛИ отмены ctx и сообщает, реально ли дождались.
func (m *Manager) Drain(ctx context.Context) (drained bool) {
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

// --- Подписка (live-доставка для SSE) ---

// Subscribe возвращает канал событий и функцию отписки. Каждое durable-событие доставляется с его seq
// (дедуп — на стороне потребителя).
func (m *Manager) Subscribe() (<-chan Event, func()) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	id := m.nextSubID
	m.nextSubID++
	ch := make(chan Event, subBufferSize)
	m.subs[id] = ch
	cancel := func() {
		m.subMu.Lock()
		defer m.subMu.Unlock()
		if existing, ok := m.subs[id]; ok {
			delete(m.subs, id)
			close(existing)
		}
	}
	return ch, cancel
}

// publish рассылает событие подписчикам без блокировки агентской горутины:
// переполненный подписчик отцепляется и его канал закрывается.
func (m *Manager) publish(ev Event) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for id, ch := range m.subs {
		select {
		case ch <- ev:
		default:
			delete(m.subs, id)
			close(ch)
		}
	}
}

// record — единая точка: durable-запись в журнал + доставка подписчикам.
func (m *Manager) record(typ EventType, turnID string, data any) (Event, error) {
	m.recMu.Lock()
	defer m.recMu.Unlock()
	ev, err := m.store.Append(typ, turnID, data)
	if err != nil {
		return Event{}, err
	}
	if m.afterAppend != nil {
		m.afterAppend(ev)
	}
	m.publish(ev)
	return ev, nil
}

// --- вспомогательное ---

func (m *Manager) secretValues(at *activeTurn) []string {
	var values []string
	if m.secrets != nil {
		values = append(values, m.secrets()...)
	}
	if token := at.meshToken.Load(); token != nil {
		values = append(values, token.(string))
	}
	return values
}

func (m *Manager) redactErr(at *activeTurn, err error) string {
	return redact.String(err.Error(), m.secretValues(at))
}

// turnPaths возвращает путь лога агента хода и относительную ссылку для log_ref.
func (m *Manager) turnPaths(turnID string) (logFile, logRef string, err error) {
	dir, err := m.store.TurnDir(turnID)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dir, turnLogName), filepath.Join("turns", turnID, turnLogName), nil
}

// newMessageID генерирует уникальный id сообщения очереди: m-<unixnano>-<rand4hex>.
func newMessageID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("m-%d-%x", time.Now().UTC().UnixNano(), b[:])
}

// newTurnID генерирует уникальный id хода: t-<unixnano>-<rand4hex>.
func newTurnID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("t-%d-%x", time.Now().UTC().UnixNano(), b[:])
}
