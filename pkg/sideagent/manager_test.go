package sideagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/executor"
)

// --- Фейковый AgentRunner (без реального подпроцесса) ---

type fakeAction struct{ tool, detail string }

// fakeRunner имитирует один ход: эмитит заранее заданные действия через
// cfg.OnAction, затем (опционально) блокируется до close(block) / сигнала
// InterruptCh / отмены ctx и возвращает retErr.
type fakeRunner struct {
	cfg     executor.Config
	actions []fakeAction
	retErr  error
	block   chan struct{} // nil = не блокироваться

	beforeActions     func()      // вызывается в начале RunAgent, ДО эмита действий
	interruptedReturn atomic.Bool // RunAgent вернулся по InterruptCh

	started   chan struct{} // закрывается ПОСЛЕ эмита действий, ДО блокировки
	gotCtx    context.Context
	gotPrompt string
	gotLog    string
}

func (f *fakeRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	f.gotCtx = ctx
	f.gotPrompt = prompt
	f.gotLog = logFile
	if f.beforeActions != nil {
		f.beforeActions()
	}
	for _, a := range f.actions {
		if f.cfg.OnAction != nil {
			f.cfg.OnAction(a.tool, a.detail)
		}
	}
	if f.started != nil {
		close(f.started)
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-f.cfg.InterruptCh:
			f.interruptedReturn.Store(true)
			return executor.ErrUserInterrupted
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.retErr
}

// fakeFactory раздаёт фейки по очереди и считает число запусков.
type fakeFactory struct {
	mu    sync.Mutex
	fakes []*fakeRunner
	calls int
}

func (ff *fakeFactory) make(cfg executor.Config) AgentRunner {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	f := ff.fakes[ff.calls]
	ff.calls++
	f.cfg = cfg
	return f
}

func (ff *fakeFactory) count() int {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	return ff.calls
}

// --- Хелперы ---

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir(), "run-test")
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newCorruptStore(t *testing.T) *Store {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "side-agent", "run-corrupt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chat.jsonl"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	s, _ := Open(base, "run-corrupt") // возвращает unavailable-стор + ошибку
	t.Cleanup(func() { s.Close() })
	return s
}

func newManager(t *testing.T, store *Store, ff *fakeFactory, cb ContextBuilder) *Manager {
	t.Helper()
	m := New(Config{
		Store:       store,
		Command:     "claude",
		Context:     cb,
		NewExecutor: ff.make,
	})
	return m
}

// drainUntilTerminal читает события из подписки, пока не встретит терминальное
// или не истечёт таймаут.
func drainUntilTerminal(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var got []Event
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, ev)
			if st, isFSM := ev.Type.TurnState(); isFSM && st.IsTerminal() {
				return got
			}
		case <-deadline:
			t.Fatalf("таймаут ожидания терминального события, получено: %v", got)
			return got
		}
	}
}

func codeOf(t *testing.T, err error) ErrorCode {
	t.Helper()
	if err == nil {
		t.Fatal("ожидалась ошибка, получили nil")
	}
	e, ok := err.(*Error)
	if !ok {
		t.Fatalf("ошибка типа %T, ожидался *Error: %v", err, err)
	}
	return e.Code
}

func lastDataTexts(h []Event, typ EventType) []string {
	var out []string
	for _, e := range h {
		if e.Type != typ {
			continue
		}
		switch typ {
		case EventAssistantText:
			var d AssistantTextData
			_ = e.DecodeData(&d)
			out = append(out, d.Text)
		case EventToolAction:
			var d ToolActionData
			_ = e.DecodeData(&d)
			out = append(out, d.Detail)
		case EventTurnFailed:
			var d TurnFailedData
			_ = e.DecodeData(&d)
			out = append(out, d.Message)
		}
	}
	return out
}

// --- Тесты ---

func TestManager_ConstructionDoesNotLaunch(t *testing.T) {
	ff := &fakeFactory{fakes: []*fakeRunner{{}}}
	m := newManager(t, newTestStore(t), ff, ContextBuilder{})
	_ = m.History()
	_ = m.ConversationID()
	_ = m.Command()
	_ = m.Unavailable()
	if ff.count() != 0 {
		t.Fatalf("агент запущен без Send: calls = %d", ff.count())
	}
}

func TestManager_SendCompletesAndDelivers(t *testing.T) {
	ff := &fakeFactory{fakes: []*fakeRunner{{
		actions: []fakeAction{{"text", "ответ агента"}},
	}}}
	m := newManager(t, newTestStore(t), ff, ContextBuilder{})
	ch, cancel := m.Subscribe()
	defer cancel()

	turnID, err := m.Send("c1", "привет")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if turnID == "" {
		t.Fatal("пустой turnID")
	}

	got := drainUntilTerminal(t, ch)
	var types []EventType
	var lastSeq uint64
	for _, e := range got {
		types = append(types, e.Type)
		if e.Seq <= lastSeq {
			t.Fatalf("seq не возрастает: %d после %d", e.Seq, lastSeq)
		}
		lastSeq = e.Seq
	}
	wantSeq := []EventType{EventTurnAccepted, EventTurnStarted, EventAssistantText, EventTurnCompleted}
	if len(types) != len(wantSeq) {
		t.Fatalf("события = %v, want %v", types, wantSeq)
	}
	for i := range wantSeq {
		if types[i] != wantSeq[i] {
			t.Fatalf("события = %v, want %v", types, wantSeq)
		}
	}
}

func TestManager_IdempotentRepeatWhileRunning(t *testing.T) {
	block := make(chan struct{})
	ff := &fakeFactory{fakes: []*fakeRunner{{
		started: make(chan struct{}),
		block:   block,
	}}}
	m := newManager(t, newTestStore(t), ff, ContextBuilder{})

	id1, err := m.Send("c1", "привет")
	if err != nil {
		t.Fatalf("Send1: %v", err)
	}
	<-ff.fakes[0].started

	id2, err := m.Send("c1", "привет")
	if err != nil {
		t.Fatalf("повтор Send: %v", err)
	}
	if id2 != id1 {
		t.Fatalf("idempotent повтор дал другой turnID: %q != %q", id2, id1)
	}
	if ff.count() != 1 {
		t.Fatalf("второй запуск агента на тот же client_message_id: calls = %d", ff.count())
	}

	close(block)
	if !m.Drain(ctxWithTimeout(t, time.Second)) {
		t.Fatal("Drain не завершился")
	}
}

func TestManager_IdempotentRepeatAfterCompletion(t *testing.T) {
	ff := &fakeFactory{fakes: []*fakeRunner{{actions: []fakeAction{{"text", "ok"}}}}}
	m := newManager(t, newTestStore(t), ff, ContextBuilder{})

	id1, err := m.Send("c1", "привет")
	if err != nil {
		t.Fatalf("Send1: %v", err)
	}
	if !m.Drain(ctxWithTimeout(t, time.Second)) {
		t.Fatal("Drain")
	}

	id2, err := m.Send("c1", "привет")
	if err != nil {
		t.Fatalf("повтор после завершения: %v", err)
	}
	if id2 != id1 {
		t.Fatalf("повтор дал другой turnID: %q != %q", id2, id1)
	}
	if ff.count() != 1 {
		t.Fatalf("повтор запустил нового агента: calls = %d", ff.count())
	}
}

func TestManager_SameIDDifferentTextIsConflict(t *testing.T) {
	block := make(chan struct{})
	ff := &fakeFactory{fakes: []*fakeRunner{{started: make(chan struct{}), block: block}}}
	m := newManager(t, newTestStore(t), ff, ContextBuilder{})

	if _, err := m.Send("c1", "первый"); err != nil {
		t.Fatalf("Send1: %v", err)
	}
	<-ff.fakes[0].started

	_, err := m.Send("c1", "ДРУГОЙ текст")
	if code := codeOf(t, err); code != ErrCodeBusy {
		t.Fatalf("code = %q, want %q", code, ErrCodeBusy)
	}
	close(block)
	m.Drain(ctxWithTimeout(t, time.Second))
}

func TestManager_SecondRequestWhileRunningBusyNoHistory(t *testing.T) {
	block := make(chan struct{})
	ff := &fakeFactory{fakes: []*fakeRunner{{started: make(chan struct{}), block: block}}}
	store := newTestStore(t)
	m := newManager(t, store, ff, ContextBuilder{})

	if _, err := m.Send("c1", "первый"); err != nil {
		t.Fatalf("Send1: %v", err)
	}
	<-ff.fakes[0].started
	before := len(store.History())

	_, err := m.Send("c2", "второй")
	if code := codeOf(t, err); code != ErrCodeBusy {
		t.Fatalf("code = %q, want %q", code, ErrCodeBusy)
	}
	if after := len(store.History()); after != before {
		t.Fatalf("busy-запрос записал историю: было %d, стало %d", before, after)
	}
	close(block)
	m.Drain(ctxWithTimeout(t, time.Second))
}

func TestManager_StopSpecificTurnPersistsPartialAndInterrupted(t *testing.T) {
	block := make(chan struct{})
	ff := &fakeFactory{fakes: []*fakeRunner{{
		started: make(chan struct{}),
		block:   block,
		actions: []fakeAction{{"text", "частичный ответ"}},
	}}}
	store := newTestStore(t)
	m := newManager(t, store, ff, ContextBuilder{})

	id, err := m.Send("c1", "привет")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	<-ff.fakes[0].started

	if err := m.Stop(id); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !m.Drain(ctxWithTimeout(t, time.Second)) {
		t.Fatal("Drain")
	}
	close(block) // на случай если дошло; фейк уже вышел по InterruptCh

	h := store.History()
	if texts := lastDataTexts(h, EventAssistantText); len(texts) != 1 || texts[0] != "частичный ответ" {
		t.Fatalf("частичный ответ не сохранён: %v", texts)
	}
	last := h[len(h)-1]
	if last.Type != EventTurnInterrupted {
		t.Fatalf("последнее событие = %q, want %q", last.Type, EventTurnInterrupted)
	}
}

func TestManager_StopStaleIDDoesNotInterruptNextTurn(t *testing.T) {
	// ход 1 завершается, ход 2 блокируется.
	block := make(chan struct{})
	ff := &fakeFactory{fakes: []*fakeRunner{
		{actions: []fakeAction{{"text", "первый ok"}}},
		{started: make(chan struct{}), block: block},
	}}
	m := newManager(t, newTestStore(t), ff, ContextBuilder{})

	id1, err := m.Send("c1", "первый")
	if err != nil {
		t.Fatalf("Send1: %v", err)
	}
	if !m.Drain(ctxWithTimeout(t, time.Second)) {
		t.Fatal("Drain1")
	}

	id2, err := m.Send("c2", "второй")
	if err != nil {
		t.Fatalf("Send2: %v", err)
	}
	<-ff.fakes[1].started

	// Stop на устаревший id1 не должен прервать ход 2.
	if err := m.Stop(id1); err == nil {
		t.Fatal("Stop(stale) должен вернуть ошибку")
	} else if code := codeOf(t, err); code != ErrCodeStaleTurn {
		t.Fatalf("code = %q, want %q", code, ErrCodeStaleTurn)
	}

	// Ход 2 всё ещё идёт.
	if m.Drain(ctxWithTimeout(t, 150*time.Millisecond)) {
		t.Fatal("ход 2 неожиданно завершился после Stop(stale)")
	}

	// Корректный Stop прерывает ход 2.
	if err := m.Stop(id2); err != nil {
		t.Fatalf("Stop(id2): %v", err)
	}
	if !m.Drain(ctxWithTimeout(t, time.Second)) {
		t.Fatal("ход 2 не прервался корректным Stop")
	}
	close(block)
}

func TestManager_AgentRunsUnderManagerCtxNotRequest(t *testing.T) {
	block := make(chan struct{})
	ff := &fakeFactory{fakes: []*fakeRunner{{started: make(chan struct{}), block: block}}}
	m := newManager(t, newTestStore(t), ff, ContextBuilder{})

	// "Запрос" с собственным ctx, который мы отменяем сразу.
	reqCtx, reqCancel := context.WithCancel(context.Background())
	reqCancel()

	if _, err := m.Send("c1", "привет"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	<-ff.fakes[0].started

	// ctx агента не должен быть отменён отменой запроса.
	if err := ff.fakes[0].gotCtx.Err(); err != nil {
		t.Fatalf("ctx агента отменён (%v) — агент привязан к ctx запроса", err)
	}
	_ = reqCtx
	close(block)
	m.Drain(ctxWithTimeout(t, time.Second))
}

func TestManager_SuccessWithNoAnswerIsProtocolError(t *testing.T) {
	// Только инструментальное действие, без текста, штатный выход.
	ff := &fakeFactory{fakes: []*fakeRunner{{actions: []fakeAction{{"Bash", "ls"}}}}}
	store := newTestStore(t)
	m := newManager(t, store, ff, ContextBuilder{})

	if _, err := m.Send("c1", "привет"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !m.Drain(ctxWithTimeout(t, time.Second)) {
		t.Fatal("Drain")
	}
	last := store.History()[len(store.History())-1]
	if last.Type != EventTurnFailed {
		t.Fatalf("последнее событие = %q, want turn_failed", last.Type)
	}
	var d TurnFailedData
	_ = last.DecodeData(&d)
	if d.Code != ErrCodeNoAnswer {
		t.Fatalf("code = %q, want %q", d.Code, ErrCodeNoAnswer)
	}
}

func TestManager_GoroutineRegisteredBeforeAdmissionUnlock(t *testing.T) {
	block := make(chan struct{})
	ff := &fakeFactory{fakes: []*fakeRunner{{block: block}}}
	m := newManager(t, newTestStore(t), ff, ContextBuilder{})

	if _, err := m.Send("c1", "привет"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Сразу после возврата Send горутина уже учтена в WaitGroup: Drain с уже
	// отменённым ctx обязан вернуть false (иначе wg.Add произошёл позже).
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if m.Drain(cancelled) {
		t.Fatal("Drain вернул true — горутина не была зарегистрирована до возврата Send")
	}
	close(block)
	if !m.Drain(ctxWithTimeout(t, time.Second)) {
		t.Fatal("Drain после разблокировки")
	}
}

func TestManager_RedactionAcrossUserEvents(t *testing.T) {
	secret := "SUPERSECRET"
	ff := &fakeFactory{fakes: []*fakeRunner{{
		actions: []fakeAction{
			{"text", "вот секрет " + secret + " готово"},
			{"Bash", "echo " + secret},
		},
		retErr: &Error{Code: ErrCodeAgentError, Message: "упал с " + secret},
	}}}
	store := newTestStore(t)
	m := New(Config{
		Store:       store,
		Command:     "claude",
		NewExecutor: ff.make,
		Secrets:     func() []string { return []string{secret} },
	})
	ch, cancel := m.Subscribe()
	defer cancel()

	if _, err := m.Send("c1", "привет"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	delivered := drainUntilTerminal(t, ch)
	if !m.Drain(ctxWithTimeout(t, time.Second)) {
		t.Fatal("Drain")
	}

	// Ни одно доставленное событие не содержит секрет.
	for _, e := range delivered {
		if containsRaw(e.Data, secret) {
			t.Fatalf("секрет утёк в доставленном событии %q: %s", e.Type, e.Data)
		}
	}
	// Ни одно durable-событие не содержит секрет.
	for _, e := range store.History() {
		if containsRaw(e.Data, secret) {
			t.Fatalf("секрет утёк в durable-событии %q: %s", e.Type, e.Data)
		}
	}
	// И при этом редакция реально произошла (маркер присутствует).
	if texts := lastDataTexts(store.History(), EventAssistantText); len(texts) == 0 || texts[0] == "" {
		t.Fatal("assistant_text отсутствует")
	}
}

func TestManager_ContextLimitFailsBeforeAccept(t *testing.T) {
	ff := &fakeFactory{fakes: []*fakeRunner{{}}}
	store := newTestStore(t)
	m := newManager(t, store, ff, ContextBuilder{ByteLimit: 8}) // заведомо мало

	_, err := m.Send("c1", "это сообщение заведомо длиннее восьми байт")
	if code := codeOf(t, err); code != ErrCodeContextLimit {
		t.Fatalf("code = %q, want %q", code, ErrCodeContextLimit)
	}
	if len(store.History()) != 0 {
		t.Fatalf("при context_limit записана история: %v", store.History())
	}
	if ff.count() != 0 {
		t.Fatalf("агент запущен при context_limit: calls = %d", ff.count())
	}
}

func TestManager_StorageUnavailable(t *testing.T) {
	ff := &fakeFactory{fakes: []*fakeRunner{{}}}
	m := newManager(t, newCorruptStore(t), ff, ContextBuilder{})
	_, err := m.Send("c1", "привет")
	if code := codeOf(t, err); code != ErrCodeStorageUnavailable {
		t.Fatalf("code = %q, want %q", code, ErrCodeStorageUnavailable)
	}
	if ff.count() != 0 {
		t.Fatalf("агент запущен при недоступном сторе")
	}
}

func TestManager_ShuttingDownRejectsSend(t *testing.T) {
	ff := &fakeFactory{fakes: []*fakeRunner{{}}}
	m := newManager(t, newTestStore(t), ff, ContextBuilder{})
	m.Shutdown()
	_, err := m.Send("c1", "привет")
	if code := codeOf(t, err); code != ErrCodeShuttingDown {
		t.Fatalf("code = %q, want %q", code, ErrCodeShuttingDown)
	}
}

func TestManager_DrainTrueAndFalse(t *testing.T) {
	block := make(chan struct{})
	ff := &fakeFactory{fakes: []*fakeRunner{{block: block}}}
	m := newManager(t, newTestStore(t), ff, ContextBuilder{})

	if _, err := m.Send("c1", "привет"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if m.Drain(ctxWithTimeout(t, 100*time.Millisecond)) {
		t.Fatal("Drain вернул true при незавершённом агенте")
	}
	close(block)
	if !m.Drain(ctxWithTimeout(t, time.Second)) {
		t.Fatal("Drain вернул false после завершения агента")
	}
}

func TestManager_SlowSubscriberDropped(t *testing.T) {
	// Много действий, чтобы переполнить буфер подписчика, который никто не читает.
	var acts []fakeAction
	for i := 0; i < subBufferSize*4; i++ {
		acts = append(acts, fakeAction{"Bash", "cmd"})
	}
	ff := &fakeFactory{fakes: []*fakeRunner{{actions: acts}}}
	m := newManager(t, newTestStore(t), ff, ContextBuilder{})

	ch, cancel := m.Subscribe()
	defer cancel()

	if _, err := m.Send("c1", "привет"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Медленный подписчик не должен заблокировать агента: ход завершается.
	if !m.Drain(ctxWithTimeout(t, 2*time.Second)) {
		t.Fatal("агент заблокирован медленным подписчиком (Drain=false)")
	}
	// Переполненная подписка в итоге закрыта.
	closed := false
	deadline := time.After(time.Second)
	for !closed {
		select {
		case _, ok := <-ch:
			if !ok {
				closed = true
			}
		case <-deadline:
			t.Fatal("переполненная подписка не закрыта")
		}
	}
}

func TestManager_ValidationErrors(t *testing.T) {
	ff := &fakeFactory{fakes: []*fakeRunner{{}, {}, {}}}
	m := newManager(t, newTestStore(t), ff, ContextBuilder{})

	if _, err := m.Send("c1", "   "); codeOf(t, err) != ErrCodeInvalidMessage {
		t.Fatalf("пустой текст: code = %q", codeOf(t, err))
	}
	if _, err := m.Send("", "привет"); codeOf(t, err) != ErrCodeInvalidMessage {
		t.Fatalf("пустой client_message_id: code = %q", codeOf(t, err))
	}
	big := make([]byte, MaxMessageBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	if _, err := m.Send("c1", string(big)); codeOf(t, err) != ErrCodeMessageTooLarge {
		t.Fatalf("слишком большой текст: code = %q", codeOf(t, err))
	}
	if ff.count() != 0 {
		t.Fatalf("агент запущен при провале валидации")
	}
}

// --- вспомогательное ---

func ctxWithTimeout(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func containsRaw(data []byte, secret string) bool {
	return len(data) > 0 && strings.Contains(string(data), secret)
}

// Идемпотентность client_message_id — на всю беседу и durable: повтор id после
// промежуточного хода возвращает исходный ход без нового запуска агента, и это
// переживает «рестарт» (новый Manager над тем же журналом).
func TestManager_IdempotencyIsConversationWideAndDurable(t *testing.T) {
	store := newTestStore(t)
	ff := &fakeFactory{fakes: []*fakeRunner{
		{actions: []fakeAction{{"text", "a"}}},
		{actions: []fakeAction{{"text", "b"}}},
		{actions: []fakeAction{{"text", "tripwire"}}}, // не должен быть израсходован
	}}
	m := newManager(t, store, ff, ContextBuilder{ByteLimit: 1 << 20})

	id1, err := m.Send("c1", "a")
	if err != nil {
		t.Fatalf("Send c1: %v", err)
	}
	if !m.Drain(context.Background()) {
		t.Fatal("turn1 не завершился")
	}
	if _, err := m.Send("c2", "b"); err != nil {
		t.Fatalf("Send c2: %v", err)
	}
	if !m.Drain(context.Background()) {
		t.Fatal("turn2 не завершился")
	}

	// Повтор c1 ПОСЛЕ промежуточного хода c2 — тот же ход, без нового запуска.
	again, err := m.Send("c1", "a")
	if err != nil {
		t.Fatalf("repeat c1: %v", err)
	}
	if again != id1 {
		t.Errorf("повтор c1 вернул %q, ожидался исходный %q", again, id1)
	}
	if ff.count() != 2 {
		t.Errorf("повтор не должен запускать агента: calls=%d, ожидалось 2", ff.count())
	}

	// «Рестарт»: новый Manager над тем же журналом восстанавливает идемпотентность.
	m2 := newManager(t, store, ff, ContextBuilder{ByteLimit: 1 << 20})
	r, err := m2.Send("c1", "a")
	if err != nil {
		t.Fatalf("repeat c1 after restart: %v", err)
	}
	if r != id1 {
		t.Errorf("после рестарта повтор c1 вернул %q, ожидался %q", r, id1)
	}
	if ff.count() != 2 {
		t.Errorf("после рестарта повтор не должен запускать агента: calls=%d", ff.count())
	}
}

// Сбой журнала во время хода останавливает боковой запрос (мягкий interrupt),
// не трогая flow. Журнал закрывается ПОСЛЕ turn_started, но ДО первого действия
// (beforeActions) — так record() действия падает в середине хода.
func TestManager_StorageFailureMidTurnStopsAgent(t *testing.T) {
	store := newTestStore(t)
	fr := &fakeRunner{actions: []fakeAction{{"text", "answer"}}, block: make(chan struct{})}
	fr.beforeActions = func() { _ = store.Close() }
	ff := &fakeFactory{fakes: []*fakeRunner{fr}}
	m := newManager(t, store, ff, ContextBuilder{ByteLimit: 1 << 20})

	if _, err := m.Send("c1", "hi"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !m.Drain(ctx) {
		t.Fatal("ход не остановился при сбое журнала (агент не прерван)")
	}
	if !fr.interruptedReturn.Load() {
		t.Error("агент должен быть прерван (InterruptCh) при сбое durable-записи")
	}
}
