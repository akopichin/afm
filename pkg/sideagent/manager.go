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
	// MaxMessageBytes — верхний предел размера ОДНОГО пользовательского
	// сообщения (отдельно от бюджета всего собранного промпта —
	// ContextBuilder.ByteLimit). Экспортирован: C1 маппит превышение в HTTP.
	MaxMessageBytes = 64 * 1024

	// subBufferSize — буфер канала одного подписчика. Медленный подписчик,
	// переполнивший буфер, отцепляется (канал закрывается), чтобы не блокировать
	// агентскую горутину и не терять durable-события (они уже в журнале).
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
	// Context — сборщик промпта из истории + текущего сообщения.
	Context ContextBuilder
	// Secrets — поставщик значений секретов для редакции пользовательского
	// текста (собственный секрет агента + recipe-секреты). nil → без редакции.
	// Функция (а не []string), чтобы вызывающий (D1) мог резолвить лениво.
	Secrets func() []string
	// OnUsage — приёмник учёта стоимости одного хода (проброс в
	// executor.Config.OnUsage). nil → учёт выключен. Вызывающий (D1) обычно
	// связывает это с accounting-стором по Store.UsagePath()/Store.Dir().
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

// Manager владеет единственным активным ходом бокового разговора: durable-first
// приём, запуск агента под ПРОЦЕССНЫМ ctx (не ctx запроса), доставку событий
// подписчикам и ограниченный Drain для shutdown.
type Manager struct {
	store       *Store
	command     string
	args        []string
	dir         string
	wrapperDir  string
	idleTimeout time.Duration
	ctxBuilder  ContextBuilder
	secrets     func() []string
	onUsage     func(accounting.Observation)
	newExecutor func(executor.Config) AgentRunner

	// ctx — процессный контекст агентских горутин. Отмена запроса (POST/SSE) на
	// него НЕ влияет: ход прерывается только через Stop/Shutdown (InterruptCh).
	ctx context.Context

	// mu защищает admission: активный ход, карту идемпотентности и флаг shutdown.
	// Один активный ход за раз.
	mu     sync.Mutex
	active *activeTurn
	// idem — client_message_id → принятый ход, ВО ВСЁ БЕСЕДЕ (не только последний).
	// Восстанавливается из turn_accepted-событий журнала при создании менеджера,
	// поэтому повтор любого id (в т.ч. после рестарта процесса) возвращает исходный
	// ход, а не запускает агента заново (инвариант 3).
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
		store:       cfg.Store,
		command:     cfg.Command,
		args:        cfg.ExtraArgs,
		dir:         cfg.Dir,
		wrapperDir:  cfg.WrapperDir,
		idleTimeout: cfg.IdleTimeout,
		ctxBuilder:  cfg.Context,
		secrets:     cfg.Secrets,
		onUsage:     cfg.OnUsage,
		newExecutor: nx,
		ctx:         context.Background(),
		idem:        rebuildIdempotency(cfg.Store.History()),
		subs:        make(map[int]chan Event),
	}
}

// rebuildIdempotency восстанавливает карту client_message_id → ход из всех
// turn_accepted-событий журнала (инвариант 3: идемпотентность durable и
// действует на всю беседу, в т.ч. после рестарта процесса).
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

// Send принимает ход разговора. Durable-first: turn_accepted фиксируется в
// журнале (fsync) ДО резервирования активного хода и возврата turnID.
// Возвращаемая ошибка — всегда *Error со стабильным кодом.
func (m *Manager) Send(clientMessageID, text string) (string, error) {
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
	if _, err := m.record(EventTurnAccepted, turnID, TurnAcceptedData{Message: text, ClientMessageID: clientMessageID}); err != nil {
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
			}
		},
	}
	runner := m.newExecutor(cfg)
	runErr := runner.RunAgent(m.ctx, sideAgentType, sideStageName, prompt, logFile)

	// Шаг 7: терминальное событие ДО освобождения слота (releaseActive — defer).
	switch {
	case at.storeFail.Load():
		// Durable-запись действия провалилась: исход хода — storage_unavailable
		// (попытка записи best-effort; если журнал мёртв, она тоже не сохранится,
		// но ход уже остановлен, а flow FSM не затронут).
		m.record(EventTurnFailed, at.turnID, TurnFailedData{Code: ErrCodeStorageUnavailable, Message: "conversation journal unavailable"}) //nolint:errcheck
	case errors.Is(runErr, executor.ErrUserInterrupted):
		m.record(EventTurnInterrupted, at.turnID, TurnInterruptedData{Reason: "stopped by user"}) //nolint:errcheck
	case runErr != nil:
		m.record(EventTurnFailed, at.turnID, TurnFailedData{Code: ErrCodeAgentError, Message: m.redactErr(runErr)}) //nolint:errcheck
	case !at.answered.Load():
		// Штатный выход без видимого ответа — протокольная ошибка, не «успех».
		m.record(EventTurnFailed, at.turnID, TurnFailedData{Code: ErrCodeNoAnswer, Message: "agent produced no visible answer"}) //nolint:errcheck
	default:
		m.record(EventTurnCompleted, at.turnID, TurnCompletedData{}) //nolint:errcheck
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

// CloseAdmission закрывает приём новых ходов, НЕ прерывая активный ход (штатное
// завершение: ход пользователя доигрывается естественно). Вызывается ДО Drain,
// чтобы между проверкой «нет активного хода» и выключением сервера не проскочил
// принятый 202 (один активный ход за раз: после закрытия приёма новый начаться
// уже не может, поэтому Drain наблюдает финальное множество горутин).
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

// Drain ждёт завершения активной агентской горутины ИЛИ отмены ctx и сообщает,
// реально ли дождались (drained). В отличие от concurrency.WaitAgents, не
// «молчит» по таймауту — вызывающий (D2) сам решает по результату.
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

// Subscribe возвращает канал событий и функцию отписки. Каждое durable-событие
// доставляется с его seq (дедуп — на стороне потребителя). Переполнивший буфер
// подписчик отцепляется (канал закрывается).
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
