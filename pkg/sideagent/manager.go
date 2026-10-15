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
	// OnUsage — приёмник учёта стоимости одного хода. nil → учёт выключен.
	OnUsage func(accounting.Observation)
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
}

// turnRecord — минимум для идемпотентности: совпадение client_message_id.
type turnRecord struct {
	message string
	turnID  string
}

// Manager владеет единственным активным ходом бокового разговора: durable-first приём, запуск агента под
// ПРОЦЕССНЫМ ctx, доставку событий подписчикам и ограниченный Drain для shutdown.
type Manager struct {
	store           *Store
	command         string
	args            []string
	dir             string
	wrapperDir      string
	idleTimeout     time.Duration
	reasoning       string
	ctxBuilder      ContextBuilder
	secrets         func() []string
	onUsage         func(accounting.Observation)
	mentionResolve  func(string) (string, bool)
	knownActionType func(string) bool
	newExecutor     func(executor.Config) AgentRunner

	// ctx — процессный контекст агентских горутин. Отмена запроса (POST/SSE) на
	// него НЕ влияет: ход прерывается только через Stop/Shutdown (InterruptCh).
	ctx context.Context

	// mu защищает admission: активный ход, карту идемпотентности и флаг shutdown.
	// Один активный ход за раз.
	mu     sync.Mutex
	active *activeTurn
	// idem хранит client_message_id → принятый ход для всей беседы, включая рестарты, предотвращая повторный
	// запуск.
	idem         map[string]turnRecord
	shuttingDown bool

	// wg учитывает агентскую горутину (своя, не concurrency.WaitAgents) — Drain
	// сообщает, реально ли дождались завершения.
	wg sync.WaitGroup

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
	nx := cfg.NewExecutor
	if nx == nil {
		nx = NewExecutorRunner
	}
	return &Manager{
		store:           cfg.Store,
		command:         cfg.Command,
		args:            cfg.ExtraArgs,
		dir:             cfg.Dir,
		wrapperDir:      cfg.WrapperDir,
		idleTimeout:     cfg.IdleTimeout,
		reasoning:       cfg.Reasoning,
		ctxBuilder:      cfg.Context,
		secrets:         cfg.Secrets,
		onUsage:         cfg.OnUsage,
		mentionResolve:  cfg.MentionResolve,
		knownActionType: cfg.KnownActionType,
		newExecutor:     nx,
		ctx:             context.Background(),
		idem:            rebuildIdempotency(cfg.Store.History()),
		subs:            make(map[int]chan Event),
	}
}

// rebuildIdempotency восстанавливает client_message_id → ход из всей durable-истории turn_accepted.
func rebuildIdempotency(history []Event) map[string]turnRecord {
	idem := make(map[string]turnRecord)
	for _, e := range history {
		if e.Type != EventTurnAccepted {
			continue
		}
		var d TurnAcceptedData
		if err := e.DecodeData(&d); err != nil || d.ClientMessageID == "" {
			continue
		}
		idem[d.ClientMessageID] = turnRecord{message: d.Message, turnID: e.TurnID}
	}
	return idem
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

// --- Send ---

// Send принимает ход разговора. Durable-first: turn_accepted фиксируется в журнале (fsync) ДО
// резервирования активного хода и возврата turnID.
//
// target — адресат, фиксируемый НАВСЕГДА в этой записи (T4.3): после возврата Send ничто (включая смену
// выбранной стадии в дашборде) не может задним числом поменять то, что уже ушло в durable-журнал. Mentions
// в target не передаются — Send сам извлекает их из text через Config.MentionResolve (клиент не может
// заявить, что упомянул роль, которую не упоминал).
func (m *Manager) Send(clientMessageID, text string, target TurnTarget) (string, error) {
	// Шаг 1: валидация без блокировок.
	if clientMessageID == "" {
		return "", newErr(ErrCodeInvalidMessage, "client_message_id required")
	}
	if strings.TrimSpace(text) == "" {
		return "", newErr(ErrCodeInvalidMessage, "message is empty")
	}
	if len(text) > MaxMessageBytes {
		return "", newErr(ErrCodeMessageTooLarge, fmt.Sprintf("message %d > %d bytes", len(text), MaxMessageBytes))
	}
	if m.store.Unavailable() {
		return "", newErr(ErrCodeStorageUnavailable, "conversation journal unavailable")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Шаг 2: admission. Один активный ход; shutdown закрывает приём.
	if m.shuttingDown {
		return "", newErr(ErrCodeShuttingDown, "service is shutting down")
	}
	// Идемпотентность по client_message_id (вся беседа, durable — см. idem).
	if rec, ok := m.idem[clientMessageID]; ok {
		if rec.message == text {
			return rec.turnID, nil // тот же ход, без повторного запуска
		}
		return "", newErr(ErrCodeBusy, "client_message_id reused with different text")
	}
	if m.active != nil {
		return "", newErr(ErrCodeBusy, "another request is in progress")
	}

	// Шаг 3: сборка контекста и бюджет ДО приёма — при превышении ничего не
	// пишем, прежняя история и черновик сохраняются.
	prompt, err := m.ctxBuilder.Build(m.store.History(), text)
	if err != nil {
		if errors.Is(err, ErrContextLimit) {
			return "", newErr(ErrCodeContextLimit, err.Error())
		}
		return "", newErr(ErrCodeAgentError, err.Error())
	}

	// Шаг 4: turn_accepted (fsync) → резерв активного хода → учёт горутины.
	turnID := newTurnID()
	accepted := TurnAcceptedData{
		Message:         text,
		ClientMessageID: clientMessageID,
		StageID:         target.StageID,
		AssistantTaskID: target.AssistantTaskID,
		Mentions:        extractMentions(text, m.mentionResolve),
	}
	if _, err := m.record(EventTurnAccepted, turnID, accepted); err != nil {
		return "", newErr(ErrCodeStorageUnavailable, err.Error())
	}
	at := &activeTurn{turnID: turnID, interrupt: make(chan struct{}, 1)}
	m.active = at
	m.idem[clientMessageID] = turnRecord{message: text, turnID: turnID}

	// wg.Add ДО выхода из admission-mutex: shutdown/Drain не сможет обогнать
	// регистрацию горутины.
	m.wg.Add(1)
	go m.runTurn(at, prompt)

	return turnID, nil
}

// runTurn выполняет ход под процессным ctx менеджера.
func (m *Manager) runTurn(at *activeTurn, prompt string) {
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
		m.record(EventTurnFailed, at.turnID, TurnFailedData{Code: ErrCodeAgentError, Message: m.redactErr(err)}) //nolint:errcheck
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
		Reasoning:   m.reasoning,
		OnAction: func(tool, detail string) {
			// Уже провалилась durable-запись — не плодим новые (ход останавливается).
			if at.storeFail.Load() {
				return
			}
			typ, data := mapAction(tool, detail, logRef, m.secretValues())
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
		m.record(EventTurnFailed, at.turnID, TurnFailedData{Code: ErrCodeAgentError, Message: m.redactErr(runErr)}) //nolint:errcheck
	case !at.answered.Load():
		// Штатный выход без видимого ответа — протокольная ошибка, не «успех».
		m.record(EventTurnFailed, at.turnID, TurnFailedData{Code: ErrCodeNoAnswer, Message: "agent produced no visible answer"}) //nolint:errcheck
	default:
		drafts := extractActionDrafts(lastText, m.knownActionType)
		m.record(EventTurnCompleted, at.turnID, TurnCompletedData{ActionDrafts: drafts}) //nolint:errcheck
	}
}

// releaseActive снимает резерв активного хода (не трогая idem — идемпотентность
// переживает завершение).
func (m *Manager) releaseActive(at *activeTurn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == at {
		m.active = nil
	}
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

// CloseAdmission закрывает приём новых ходов, НЕ прерывая активный ход.
func (m *Manager) CloseAdmission() {
	m.mu.Lock()
	m.shuttingDown = true
	m.mu.Unlock()
}

// Shutdown закрывает приём новых ходов И мягко прерывает активный ход (аварийное
// завершение). Дожидаться завершения — через Drain.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.shuttingDown = true
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
	ev, err := m.store.Append(typ, turnID, data)
	if err != nil {
		return Event{}, err
	}
	m.publish(ev)
	return ev, nil
}

// --- вспомогательное ---

func (m *Manager) secretValues() []string {
	if m.secrets == nil {
		return nil
	}
	return m.secrets()
}

func (m *Manager) redactErr(err error) string {
	return redact.String(err.Error(), m.secretValues())
}

// turnPaths возвращает путь лога агента хода и относительную ссылку для log_ref.
func (m *Manager) turnPaths(turnID string) (logFile, logRef string, err error) {
	dir, err := m.store.TurnDir(turnID)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dir, turnLogName), filepath.Join("turns", turnID, turnLogName), nil
}

// newTurnID генерирует уникальный id хода: t-<unixnano>-<rand4hex>.
func newTurnID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("t-%d-%x", time.Now().UTC().UnixNano(), b[:])
}
