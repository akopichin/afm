package sideagent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/agentjobs"
	"github.com/akopichin/afm/pkg/executor"
)

// queueRunner — ход под управлением теста: стартует, сообщает о себе в started и ждёт release.
type queueRunner struct {
	f       *queueFactory
	cfg     executor.Config
	prompt  string
	release chan struct{}
	answer  string
	err     error // ошибка, которую вернёт RunAgent после release
}

// queueFactory раздаёт queueRunner по числу вызовов и считает максимум одновременных ходов.
type queueFactory struct {
	started   chan *queueRunner
	running   atomic.Int32
	maxActive atomic.Int32
	calls     atomic.Int32
	failCalls map[int32]error // номер запуска → ошибка исхода
	realExec  bool            // после release ход отдаётся настоящему executor со скрипт-агентом
}

func newQueueFactory() *queueFactory { return &queueFactory{started: make(chan *queueRunner, 64)} }

func (f *queueFactory) make(cfg executor.Config) AgentRunner {
	n := f.calls.Add(1)
	return &queueRunner{f: f, cfg: cfg, release: make(chan struct{}), answer: "answer-" + string(rune('0'+n)), err: f.failCalls[n]}
}

// newQueueManager строит запущенный Manager над queueFactory.
func newQueueManager(t *testing.T, store *Store, cb ContextBuilder) (*Manager, *queueFactory) {
	t.Helper()
	return newQueueManagerWith(t, newQueueFactory(), Config{Store: store, Command: "claude", Context: cb})
}

// newQueueManagerWith строит и запускает Manager над готовой фабрикой; cfg.NewExecutor подставляется здесь.
func newQueueManagerWith(t *testing.T, f *queueFactory, cfg Config) (*Manager, *queueFactory) {
	t.Helper()
	cfg.NewExecutor = f.make
	m := New(cfg)
	m.Start()
	t.Cleanup(func() {
		m.Shutdown()
		m.Drain(context.Background())
	})
	return m, f
}

func (q *queueRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	q.prompt = prompt
	n := q.f.running.Add(1)
	for {
		old := q.f.maxActive.Load()
		if n <= old || q.f.maxActive.CompareAndSwap(old, n) {
			break
		}
	}
	defer q.f.running.Add(-1)
	q.f.started <- q
	select {
	case <-q.release:
	case <-q.cfg.InterruptCh:
		return executor.ErrUserInterrupted
	case <-ctx.Done():
		return ctx.Err()
	}
	if q.err != nil {
		return q.err
	}
	if q.f.realExec {
		return executor.New(q.cfg).RunAgent(ctx, agentType, stageName, prompt, logFile)
	}
	q.cfg.OnAction("text", q.answer)
	return nil
}

func (f *queueFactory) next(t *testing.T) *queueRunner {
	t.Helper()
	select {
	case r := <-f.started:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("ход не стартовал")
		return nil
	}
}

func (f *queueFactory) expectNoStart(t *testing.T) {
	t.Helper()
	select {
	case r := <-f.started:
		t.Fatalf("неожиданный запуск хода: %q", r.prompt)
	case <-time.After(150 * time.Millisecond):
	}
}

func (q *queueRunner) finish() { close(q.release) }

func enqueue(t *testing.T, m *Manager, clientID, text, stage string) MessageReceipt {
	t.Helper()
	rc, err := m.Enqueue(clientID, text, TurnTarget{StageID: stage})
	if err != nil {
		t.Fatalf("Enqueue %s: %v", clientID, err)
	}
	return rc
}

// waitTurns ждёт n различных терминальных событий хода; подписка берётся до снимка истории, а повтор
// одного seq (снимок + live) не считается дважды.
func waitTurns(t *testing.T, m *Manager, n int) {
	t.Helper()
	ch, cancel := m.Subscribe()
	defer cancel()
	terminal := map[uint64]struct{}{}
	note := func(e Event) {
		if st, ok := e.Type.TurnState(); ok && st.IsTerminal() {
			terminal[e.Seq] = struct{}{}
		}
	}
	for _, e := range m.History() {
		note(e)
	}
	deadline := time.After(3 * time.Second)
	for len(terminal) < n {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatal("подписка закрыта")
			}
			note(e)
		case <-deadline:
			t.Fatalf("ожидалось %d завершённых ходов, есть %d", n, len(terminal))
		}
	}
}

func idsOf(group []MessageInfo) []string {
	var out []string
	for _, g := range group {
		out = append(out, g.MessageID)
	}
	return out
}

func TestQueue_MessagesWaitForActiveTurnThenRunAsOneGroup(t *testing.T) {
	store := newTestStore(t)
	m, f := newQueueManager(t, store, ContextBuilder{})

	a := enqueue(t, m, "cA", "вопрос A", "")
	if a.State != MessageStateRunning || a.TurnID == "" {
		t.Fatalf("A on idle manager = %+v, want running", a)
	}
	runA := f.next(t)
	b := enqueue(t, m, "cB", "вопрос B", "")
	c := enqueue(t, m, "cC", "вопрос C", "")
	if b.State != MessageStateQueued || c.State != MessageStateQueued {
		t.Fatalf("receipts during turn: %+v %+v", b, c)
	}
	f.expectNoStart(t)

	runA.finish()
	runGroup := f.next(t)
	if got := idsOf(store.Conversation().QueuedGroup()); len(got) != 0 {
		t.Fatalf("claimed messages still queued: %v", got)
	}
	iA, iAnswer, iB, iC := strings.Index(runGroup.prompt, "вопрос A"), strings.Index(runGroup.prompt, "answer-1"), strings.Index(runGroup.prompt, "вопрос B"), strings.Index(runGroup.prompt, "вопрос C")
	if iA < 0 || iAnswer < iA || iB < iAnswer || iC < iB {
		t.Fatalf("prompt order wrong (A=%d answer=%d B=%d C=%d):\n%s", iA, iAnswer, iB, iC, runGroup.prompt)
	}
	runGroup.finish()
	waitTurns(t, m, 2)
	if got := f.calls.Load(); got != 2 {
		t.Fatalf("runner calls = %d, want 2 (A and the group B+C)", got)
	}
	for _, id := range []string{b.MessageID, c.MessageID} {
		if msg, _ := store.Conversation().Message(id); msg.State != MessageStateCompleted {
			t.Fatalf("%s state = %q", id, msg.State)
		}
	}
	if f.maxActive.Load() != 1 {
		t.Fatalf("max concurrent turns = %d", f.maxActive.Load())
	}
}

func TestQueue_RemovedMessageNeverReachesPrompts(t *testing.T) {
	store := newTestStore(t)
	m, f := newQueueManager(t, store, ContextBuilder{})
	enqueue(t, m, "cA", "AAA", "")
	runA := f.next(t)
	b := enqueue(t, m, "cB", "BBB", "")
	c := enqueue(t, m, "cC", "CCC", "")
	d := enqueue(t, m, "cD", "DDD", "")
	if rc, err := m.CancelQueued(c.MessageID); err != nil || rc.State != MessageStateCancelled {
		t.Fatalf("cancel: %+v %v", rc, err)
	}
	runA.finish()
	runBD := f.next(t)
	if !strings.Contains(runBD.prompt, "BBB") || !strings.Contains(runBD.prompt, "DDD") || strings.Contains(runBD.prompt, "CCC") {
		t.Fatalf("group prompt must hold B and D only:\n%s", runBD.prompt)
	}
	// E приходит во время B+D и ждёт следующего хода.
	enqueue(t, m, "cE", "EEE", "")
	f.expectNoStart(t)
	runBD.finish()
	runE := f.next(t)
	if strings.Contains(runE.prompt, "CCC") || strings.Count(runE.prompt, "BBB") != 1 || strings.Count(runE.prompt, "DDD") != 1 || !strings.Contains(runE.prompt, "EEE") {
		t.Fatalf("prompt of E:\n%s", runE.prompt)
	}
	runE.finish()
	waitTurns(t, m, 3)
	_ = b
	_ = d
}

func TestQueue_ClaimedMessageCannotBeCancelled(t *testing.T) {
	m, f := newQueueManager(t, newTestStore(t), ContextBuilder{})
	a := enqueue(t, m, "cA", "A", "")
	f.next(t)
	_, err := m.CancelQueued(a.MessageID)
	if code := codeOf(t, err); code != ErrCodeMessageNotQueued {
		t.Fatalf("code = %q", code)
	}
	_, err = m.CancelQueued("m-unknown")
	if code := codeOf(t, err); code != ErrCodeMessageNotFound {
		t.Fatalf("code = %q", code)
	}
}

func TestQueue_ContextsAreNotMerged(t *testing.T) {
	store := newTestStore(t)
	m, f := newQueueManager(t, store, ContextBuilder{})
	enqueue(t, m, "cX", "XXX", "")
	runX := f.next(t)
	enqueue(t, m, "cA", "AAA", "s1")
	enqueue(t, m, "cB", "BBB", "s2")
	enqueue(t, m, "cC", "CCC", "s1")
	runX.finish()
	for _, want := range []string{"AAA", "BBB", "CCC"} {
		r := f.next(t)
		for _, other := range []string{"AAA", "BBB", "CCC"} {
			// в разделе «Текущие сообщения» только своё сообщение
			cur := r.prompt[strings.Index(r.prompt, CurrentHeading):]
			if (other == want) != strings.Contains(cur, other) {
				t.Fatalf("turn for %s: current section = %q", want, cur)
			}
		}
		r.finish()
	}
	waitTurns(t, m, 4)
}

// Cancel выигрывает: сообщение не запускается после полного освобождения исполнителя.
func TestQueue_CancelBeforeClaimNeverRuns(t *testing.T) {
	store := newTestStore(t)
	m, f := newQueueManager(t, store, ContextBuilder{})
	enqueue(t, m, "cA", "A", "")
	runA := f.next(t)
	b := enqueue(t, m, "cB", "BBB", "")
	if _, err := m.CancelQueued(b.MessageID); err != nil {
		t.Fatal(err)
	}
	runA.finish()
	if !m.Drain(ctxWithTimeout(t, 2*time.Second)) {
		t.Fatal("drain")
	}
	if f.calls.Load() != 1 {
		t.Fatalf("runner calls = %d, cancelled message was started", f.calls.Load())
	}
	var accepted, cancelled int
	for _, e := range store.History() {
		switch e.Type {
		case EventTurnAccepted:
			accepted++
		case EventMessageCancelled:
			cancelled++
		default:
		}
	}
	if accepted != 1 || cancelled != 1 {
		t.Fatalf("accepted=%d cancelled=%d, want 1/1", accepted, cancelled)
	}
}

// Claim выигрывает: отмена уже забранного сообщения отклоняется, сообщение выполняется.
func TestQueue_CancelAfterClaimIsRejected(t *testing.T) {
	store := newTestStore(t)
	m, f := newQueueManager(t, store, ContextBuilder{})
	enqueue(t, m, "cA", "A", "")
	runA := f.next(t)
	b := enqueue(t, m, "cB", "BBB", "")
	runA.finish()
	runB := f.next(t) // claim B уже записан
	_, err := m.CancelQueued(b.MessageID)
	if code := codeOf(t, err); code != ErrCodeMessageNotQueued {
		t.Fatalf("cancel after claim: code %q", code)
	}
	runB.finish()
	waitTurns(t, m, 2)
	if msg, _ := store.Conversation().Message(b.MessageID); msg.State != MessageStateCompleted {
		t.Fatalf("B = %q, want completed", msg.State)
	}
}

// Настоящая гонка отмены и освобождения исполнителя: исход всегда один из двух, и он согласован с журналом.
func TestQueue_CancelRacingWithClaimHasExactlyOneOutcome(t *testing.T) {
	for i := 0; i < 30; i++ {
		store := newTestStore(t)
		m, f := newQueueManager(t, store, ContextBuilder{})
		enqueue(t, m, "cA", "A", "")
		runA := f.next(t)
		b := enqueue(t, m, "cB", "BBB", "")

		var wg sync.WaitGroup
		var cancelErr error
		wg.Go(runA.finish)
		wg.Go(func() { _, cancelErr = m.CancelQueued(b.MessageID) })
		wg.Wait()

		if cancelErr == nil {
			if !m.Drain(ctxWithTimeout(t, 2*time.Second)) {
				t.Fatalf("iter %d: drain", i)
			}
			msg, _ := store.Conversation().Message(b.MessageID)
			if msg.State != MessageStateCancelled || f.calls.Load() != 1 {
				t.Fatalf("iter %d: cancel won but state %q, runs %d", i, msg.State, f.calls.Load())
			}
		} else {
			if code := codeOf(t, cancelErr); code != ErrCodeMessageNotQueued {
				t.Fatalf("iter %d: code %q", i, code)
			}
			f.next(t).finish()
			waitTurns(t, m, 2)
		}
		m.Shutdown()
		m.Drain(context.Background())
	}
}

func TestQueue_EnqueueIdempotency(t *testing.T) {
	store := newTestStore(t)
	m, f := newQueueManager(t, store, ContextBuilder{})
	enqueue(t, m, "cA", "A", "")
	f.next(t)
	b1 := enqueue(t, m, "cB", "B", "s1")
	b2 := enqueue(t, m, "cB", "B", "s1")
	if b1.MessageID == "" || b2.MessageID != b1.MessageID || b2.State != MessageStateQueued {
		t.Fatalf("repeat = %+v, first = %+v", b2, b1)
	}
	for _, tc := range []struct{ text, stage string }{{"другой", "s1"}, {"B", "s2"}} {
		_, err := m.Enqueue("cB", tc.text, TurnTarget{StageID: tc.stage})
		if code := codeOf(t, err); code != ErrCodeClientMessageIDConflict {
			t.Fatalf("changed payload %+v: code %q", tc, code)
		}
	}
	if _, err := m.CancelQueued(b1.MessageID); err != nil {
		t.Fatal(err)
	}
	if rc, err := m.CancelQueued(b1.MessageID); err != nil || rc.State != MessageStateCancelled {
		t.Fatalf("repeat cancel = %+v %v", rc, err)
	}
	again := enqueue(t, m, "cB", "B", "s1")
	if again.State != MessageStateCancelled || again.MessageID != b1.MessageID {
		t.Fatalf("repeat after cancel = %+v, must stay cancelled", again)
	}
	if n := len(store.Conversation().Queued()); n != 0 {
		t.Fatalf("queue = %d, cancelled message came back", n)
	}
}

func TestQueue_LegacyClientIDReturnsItsTurn(t *testing.T) {
	store := newTestStore(t)
	_, _ = store.Append(EventTurnAccepted, "t-old", TurnAcceptedData{Message: "old", ClientMessageID: "legacy"})
	_, _ = store.Append(EventTurnStarted, "t-old", TurnStartedData{})
	_, _ = store.Append(EventTurnCompleted, "t-old", TurnCompletedData{})
	m, f := newQueueManager(t, store, ContextBuilder{})
	rc := enqueue(t, m, "legacy", "old", "")
	if rc.MessageID != "" || rc.TurnID != "t-old" || rc.State != MessageStateCompleted {
		t.Fatalf("legacy receipt = %+v", rc)
	}
	f.expectNoStart(t)
}

func TestQueue_IdempotencySurvivesRestart(t *testing.T) {
	base := t.TempDir()
	s1, _ := Open(base, "run-r")
	m1 := New(Config{Store: s1, Command: "claude", NewExecutor: newQueueFactory().make})
	rc, err := m1.Enqueue("cB", "B", TurnTarget{}) // без Start — остаётся в очереди
	if err != nil || rc.State != MessageStateQueued {
		t.Fatalf("enqueue = %+v %v", rc, err)
	}
	s1.Close()
	s2, _ := Open(base, "run-r")
	defer s2.Close()
	m2 := New(Config{Store: s2, Command: "claude", NewExecutor: newQueueFactory().make})
	again, err := m2.Enqueue("cB", "B", TurnTarget{})
	if err != nil || again.MessageID != rc.MessageID {
		t.Fatalf("after restart = %+v %v, want %s", again, err, rc.MessageID)
	}
}

func TestQueue_StoppedTurnDoesNotDropQueue(t *testing.T) {
	store := newTestStore(t)
	m, f := newQueueManager(t, store, ContextBuilder{})
	a := enqueue(t, m, "cA", "A", "")
	runA := f.next(t)
	enqueue(t, m, "cB", "B", "")
	if err := m.Stop(a.TurnID); err != nil {
		t.Fatal(err)
	}
	runB := f.next(t)
	if !strings.Contains(runB.prompt, "B") {
		t.Fatal("queue did not continue after Stop")
	}
	_ = runA
	runB.finish()
	waitTurns(t, m, 2)
	if msg, _ := store.Conversation().Message(a.MessageID); msg.State != MessageStateInterrupted {
		t.Fatalf("A = %q", msg.State)
	}
}

func TestQueue_ContextLimitShrinksGroupToFittingPrefix(t *testing.T) {
	store := newTestStore(t)
	// Бюджет вмещает инструкцию, X и две короткие записи, но не три.
	m, f := newQueueManager(t, store, ContextBuilder{})
	enqueue(t, m, "cX", "X", "")
	runX := f.next(t)
	enqueue(t, m, "c1", "1111", "")
	enqueue(t, m, "c2", "2222", "")
	enqueue(t, m, "c3", "3333", "")
	runX.finish()
	runX2 := f.next(t)
	full := len(runX2.prompt)
	runX2.finish()
	waitTurns(t, m, 2)

	// Тот же сценарий с бюджетом на 3 байта меньше полной группы: третье сообщение уходит в следующий ход.
	store2 := newTestStore2(t)
	m2, f2 := newQueueManager(t, store2, ContextBuilder{ByteLimit: full - 3})
	enqueue(t, m2, "cX", "X", "")
	r := f2.next(t)
	enqueue(t, m2, "c1", "1111", "")
	enqueue(t, m2, "c2", "2222", "")
	enqueue(t, m2, "c3", "3333", "")
	r.finish()
	g1 := f2.next(t)
	if strings.Contains(g1.prompt, "3333") || !strings.Contains(g1.prompt, "2222") {
		t.Fatalf("first group prompt:\n%s", g1.prompt)
	}
	g1.finish()
	g2 := f2.next(t)
	if !strings.Contains(g2.prompt, "3333") || strings.Count(g2.prompt, "1111") != 1 {
		t.Fatalf("second group prompt:\n%s", g2.prompt)
	}
	g2.finish()
	waitTurns(t, m2, 3)
}

func newTestStore2(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir(), "run-test-2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestQueue_UnfittingHeadFailsAndQueueMovesOn(t *testing.T) {
	store := newTestStore(t)
	m, f := newQueueManager(t, store, ContextBuilder{ByteLimit: 1200})
	big := enqueue(t, m, "cBig", strings.Repeat("x", 5000), "")
	if big.State != MessageStateFailed || big.ErrorCode != ErrCodeContextLimit || big.TurnID != "" {
		t.Fatalf("idle oversize receipt = %+v", big)
	}
	f.expectNoStart(t)
	ok := enqueue(t, m, "cOk", "ok", "")
	if ok.State != MessageStateRunning {
		t.Fatalf("next message must run after a failed head: %+v", ok)
	}
	run := f.next(t)
	enqueue(t, m, "cBig2", strings.Repeat("y", 5000), "")
	enqueue(t, m, "cOk2", "ok2", "")
	run.finish()
	next := f.next(t)
	if !strings.Contains(next.prompt, "ok2") {
		t.Fatalf("queue stuck behind unfitting head:\n%s", next.prompt)
	}
	next.finish()
	waitTurns(t, m, 2)
	var failed int
	for _, e := range store.History() {
		if e.Type == EventMessageFailed {
			failed++
		}
	}
	if failed != 2 {
		t.Fatalf("message_failed events = %d, want 2", failed)
	}
}

func TestQueue_StorageFailureStopsNewTurns(t *testing.T) {
	store := newTestStore(t)
	m, f := newQueueManager(t, store, ContextBuilder{})
	enqueue(t, m, "cA", "A", "")
	runA := f.next(t)
	enqueue(t, m, "cB", "B", "")
	store.chatLog.Close() // следующая запись провалится
	runA.finish()
	f.expectNoStart(t)
	if !m.Drain(ctxWithTimeout(t, time.Second)) {
		t.Fatal("drain hangs on unavailable store")
	}
}

func TestQueue_GracefulDrainWaitsForWholeQueue(t *testing.T) {
	store := newTestStore(t)
	m, f := newQueueManager(t, store, ContextBuilder{})
	enqueue(t, m, "cA", "A", "")
	runA := f.next(t)
	enqueue(t, m, "cB", "B", "")
	m.CloseAdmission()
	if _, err := m.Enqueue("cC", "C", TurnTarget{}); codeOf(t, err) != ErrCodeShuttingDown {
		t.Fatal("closed admission must reject Enqueue")
	}
	if m.Drain(ctxWithTimeout(t, 100*time.Millisecond)) {
		t.Fatal("drain finished while A is running")
	}
	runA.finish()
	runB := f.next(t)
	if m.Drain(ctxWithTimeout(t, 100*time.Millisecond)) {
		t.Fatal("drain finished while B is running")
	}
	runB.finish()
	if !m.Drain(ctxWithTimeout(t, 2*time.Second)) {
		t.Fatal("drain did not finish after the whole queue")
	}
}

func TestQueue_CancelWorksDuringGracefulDrain(t *testing.T) {
	m, f := newQueueManager(t, newTestStore(t), ContextBuilder{})
	enqueue(t, m, "cA", "A", "")
	runA := f.next(t)
	b := enqueue(t, m, "cB", "B", "")
	m.CloseAdmission()
	if _, err := m.CancelQueued(b.MessageID); err != nil {
		t.Fatalf("cancel during drain: %v", err)
	}
	runA.finish()
	if !m.Drain(ctxWithTimeout(t, 2*time.Second)) {
		t.Fatal("drain")
	}
	f.expectNoStart(t)
}

func TestQueue_EmergencyShutdownKeepsQueued(t *testing.T) {
	store := newTestStore(t)
	m, f := newQueueManager(t, store, ContextBuilder{})
	enqueue(t, m, "cA", "A", "")
	f.next(t)
	b := enqueue(t, m, "cB", "B", "")
	m.Shutdown()
	if !m.Drain(ctxWithTimeout(t, 2*time.Second)) {
		t.Fatal("drain")
	}
	f.expectNoStart(t)
	if msg, _ := store.Conversation().Message(b.MessageID); msg.State != MessageStateQueued {
		t.Fatalf("B = %q, must stay queued", msg.State)
	}
	if _, err := m.CancelQueued(b.MessageID); err != nil {
		t.Fatalf("cancel after emergency shutdown: %v", err)
	}
}

func TestQueue_NothingStartsBeforeStart(t *testing.T) {
	base := t.TempDir()
	s1, _ := Open(base, "run-s")
	_, _ = s1.Append(EventMessageQueued, "", MessageQueuedData{MessageID: "m1", ClientMessageID: "c1", Message: "BBB"})
	_, _ = s1.Append(EventMessageQueued, "", MessageQueuedData{MessageID: "m2", ClientMessageID: "c2", Message: "CCC"})
	_, _ = s1.Append(EventTurnAccepted, "tA", TurnAcceptedData{Message: "AAA", ClientMessageID: "cA"}) // A не завершился: упал процесс
	s1.Close()
	// В журнал поверх A попадают B и C: переставляем порядок вручную нельзя, поэтому A принят ДО очереди.
	s2, err := Open(base, "run-s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	f := newQueueFactory()
	m := New(Config{Store: s2, Command: "claude", NewExecutor: f.make})
	t.Cleanup(func() { m.Shutdown(); m.Drain(context.Background()) })
	f.expectNoStart(t)
	if st, _ := s2.Conversation().TurnState("tA"); st != TurnStateInterrupted {
		t.Fatalf("A = %q, want interrupted after recovery", st)
	}
	m.Start()
	run := f.next(t)
	if !strings.Contains(run.prompt, "BBB") || !strings.Contains(run.prompt, "CCC") {
		t.Fatalf("recovered queue prompt:\n%s", run.prompt)
	}
	run.finish()
	waitTurns(t, m, 2)
	if f.calls.Load() != 1 {
		t.Fatalf("runner calls = %d", f.calls.Load())
	}
}

func TestQueue_MeshSendYieldsToQueueAndBeforeStart(t *testing.T) {
	store := newTestStore(t)
	f := newQueueFactory()
	m := New(Config{Store: store, Command: "claude", NewExecutor: f.make})
	mesh := TurnTarget{SourceMessageID: "mm-1", SourceThreadID: "th", SourceSenderID: "stage:a", SourceSenderName: "Alice"}
	if _, err := m.Send("mesh:mm-1", "ping", mesh); codeOf(t, err) != ErrCodeBusy {
		t.Fatalf("Send before Start must be busy, got %v", err)
	}
	m.Start()
	t.Cleanup(func() { m.Shutdown(); m.Drain(context.Background()) })
	enqueue(t, m, "cA", "A", "")
	runA := f.next(t)
	enqueue(t, m, "cB", "B", "")
	if _, err := m.Send("mesh:mm-1", "ping", mesh); codeOf(t, err) != ErrCodeBusy {
		t.Fatalf("Send behind a non-empty queue must be busy, got %v", err)
	}
	runA.finish()
	runB := f.next(t)
	runB.finish()
	waitTurns(t, m, 2)
	// Терминал хода пишется раньше освобождения исполнителя: Drain ждёт именно освобождения.
	if !m.Drain(ctxWithTimeout(t, 2*time.Second)) {
		t.Fatal("drain")
	}
	if _, err := m.Send("mesh:mm-1", "ping", mesh); err != nil {
		t.Fatalf("Send after the queue emptied: %v", err)
	}
	f.next(t).finish()
}

// Единственная причина отказа — непустая очередь: исполнитель свободен, диспетчер включён, активного хода нет.
func TestQueue_MeshSendBusyOnlyBecauseQueueIsNotEmpty(t *testing.T) {
	store := newTestStore(t)
	queueMsg(t, store, "m1", "queued", "")
	f := newQueueFactory()
	m := New(Config{Store: store, Command: "claude", NewExecutor: f.make})
	m.mu.Lock()
	m.dispatchEnabled = true // без dispatchLocked: сообщение остаётся в очереди при свободном исполнителе
	m.mu.Unlock()
	t.Cleanup(func() { m.Shutdown(); m.Drain(context.Background()) })
	mesh := TurnTarget{SourceMessageID: "mm-1", SourceSenderID: "stage:a"}
	if _, err := m.Send("mesh:mm-1", "ping", mesh); codeOf(t, err) != ErrCodeBusy {
		t.Fatalf("Send with a queued message must be busy, got %v", err)
	}
	if _, err := m.CancelQueued("m1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Send("mesh:mm-1", "ping", mesh); err != nil {
		t.Fatalf("Send after the queue emptied: %v", err)
	}
	f.next(t).finish()
}

func TestQueue_EnqueueDuringMeshTurnKeepsSourceMesh(t *testing.T) {
	store := newTestStore(t)
	m, f := newQueueManager(t, store, ContextBuilder{})
	if _, err := m.Send("mesh:mm-1", "ping", TurnTarget{SourceMessageID: "mm-1", SourceThreadID: "th", SourceSenderID: "stage:a", SourceSenderName: "Alice"}); err != nil {
		t.Fatal(err)
	}
	mesh := f.next(t)
	rc := enqueue(t, m, "cB", "B", "")
	if rc.State != MessageStateQueued {
		t.Fatalf("receipt = %+v", rc)
	}
	src := store.Conversation().ActiveSource()
	if src == nil || src.Kind != SourceMesh || src.SenderID != "stage:a" {
		t.Fatalf("active source = %+v", src)
	}
	f.expectNoStart(t)
	mesh.finish()
	f.next(t).finish()
	waitTurns(t, m, 2)
}

func TestQueue_ConcurrentEnqueuesNeverRunTwoTurnsAndLoseNothing(t *testing.T) {
	store := newTestStore(t)
	m, f := newQueueManager(t, store, ContextBuilder{})
	const total = 12
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		accepted = map[string]struct{}{}
	)
	for i := 0; i < total; i++ {
		wg.Go(func() {
			rc, err := m.Enqueue("c"+string(rune('a'+i)), "msg-"+string(rune('a'+i)), TurnTarget{})
			if err != nil {
				t.Errorf("enqueue %d: %v", i, err)
				return
			}
			mu.Lock()
			accepted[rc.MessageID] = struct{}{}
			mu.Unlock()
		})
	}
	// Завершаем ходы по мере запуска, пока все принятые сообщения не получат терминал.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	deadline := time.After(10 * time.Second)
	for finished := false; !finished; {
		select {
		case r := <-f.started:
			r.finish()
		case <-done:
			finished = true
		case <-deadline:
			t.Fatal("enqueues did not finish")
		}
	}
	for {
		select {
		case r := <-f.started:
			r.finish()
			continue
		case <-time.After(20 * time.Millisecond):
		}
		if len(store.Conversation().Queued()) == 0 && m.Drain(ctxWithTimeout(t, 50*time.Millisecond)) {
			break
		}
	}
	if f.maxActive.Load() != 1 {
		t.Fatalf("max concurrent turns = %d", f.maxActive.Load())
	}
	claimed := map[string]int{}
	for _, e := range store.History() {
		if e.Type != EventTurnAccepted {
			continue
		}
		var d TurnAcceptedData
		if err := e.DecodeData(&d); err != nil {
			t.Fatal(err)
		}
		for _, id := range d.MessageIDs {
			claimed[id]++
		}
	}
	if len(accepted) != total {
		t.Fatalf("accepted %d distinct messages, want %d", len(accepted), total)
	}
	conv := store.Conversation()
	for id := range accepted {
		if claimed[id] != 1 {
			t.Fatalf("message %s claimed %d times", id, claimed[id])
		}
		if msg, _ := conv.Message(id); msg.State != MessageStateCompleted {
			t.Fatalf("message %s = %q", id, msg.State)
		}
	}
}

// Границы Enqueue относительно завершения хода: до releaseActive и после него сообщение запускается ровно раз.
func TestQueue_EnqueueAtTurnBoundaryRunsExactlyOnce(t *testing.T) {
	t.Run("between terminal event and release", func(t *testing.T) {
		store := newTestStore(t)
		f := newQueueFactory()
		atTerminal, resume := make(chan struct{}), make(chan struct{})
		m := New(Config{Store: store, Command: "claude", NewExecutor: f.make})
		var held, gate sync.Once
		openGate := func() { gate.Do(func() { close(resume) }) }
		m.beforeRelease = func() {
			held.Do(func() {
				close(atTerminal)
				<-resume
			})
		}
		m.Start()
		t.Cleanup(func() { openGate(); m.Shutdown(); m.Drain(context.Background()) })
		enqueue(t, m, "cA", "A", "")
		f.next(t).finish()
		<-atTerminal // A записал терминал, но ещё держит слот
		b := enqueue(t, m, "cB", "BBB", "")
		if b.State != MessageStateQueued {
			t.Fatalf("B = %+v, must wait while A holds the slot", b)
		}
		openGate()
		f.next(t).finish()
		waitTurns(t, m, 2)
		if f.calls.Load() != 2 {
			t.Fatalf("runner calls = %d", f.calls.Load())
		}
	})
	t.Run("after release", func(t *testing.T) {
		store := newTestStore(t)
		m, f := newQueueManager(t, store, ContextBuilder{})
		enqueue(t, m, "cA", "A", "")
		f.next(t).finish()
		if !m.Drain(ctxWithTimeout(t, 2*time.Second)) {
			t.Fatal("drain")
		}
		if b := enqueue(t, m, "cB", "BBB", ""); b.State != MessageStateRunning {
			t.Fatalf("B on a free executor = %+v", b)
		}
		f.next(t).finish()
		waitTurns(t, m, 2)
		if f.calls.Load() != 2 {
			t.Fatalf("runner calls = %d", f.calls.Load())
		}
	})
}

// Ход с ошибкой агента не теряет очередь, а его сообщения получают код ошибки.
func TestQueue_FailedTurnKeepsQueueAndReportsErrorCode(t *testing.T) {
	store := newTestStore(t)
	f := newQueueFactory()
	f.failCalls = map[int32]error{1: errors.New("agent exploded")}
	m, f := newQueueManagerWith(t, f, Config{Store: store, Command: "claude"})
	a := enqueue(t, m, "cA", "A", "")
	runA := f.next(t)
	enqueue(t, m, "cB", "BBB", "")
	runA.finish()
	runB := f.next(t)
	if !strings.Contains(runB.prompt, "BBB") {
		t.Fatalf("queue did not continue after a failed turn:\n%s", runB.prompt)
	}
	runB.finish()
	waitTurns(t, m, 2)
	if msg, _ := store.Conversation().Message(a.MessageID); msg.State != MessageStateFailed || msg.ErrorCode != ErrCodeAgentError {
		t.Fatalf("A = %q/%q, want failed/agent_error", msg.State, msg.ErrorCode)
	}
	again := enqueue(t, m, "cA", "A", "")
	if again.State != MessageStateFailed || again.ErrorCode != ErrCodeAgentError {
		t.Fatalf("repeat receipt = %+v, want failed with agent_error", again)
	}
}

// queueJobSink собирает записи job'ов; потокобезопасен, как и настоящий журнал.
type queueJobSink struct {
	mu   sync.Mutex
	recs []agentjobs.Record
}

func (s *queueJobSink) Append(r agentjobs.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, r)
	return nil
}

func (s *queueJobSink) jobIDs(typ agentjobs.RecordType) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, r := range s.recs {
		if r.Type == typ {
			ids = append(ids, r.JobID)
		}
	}
	return ids
}

// Группа сообщений — один запуск, один job и один учёт с JobID этого job; отменённое сообщение не создаёт ничего.
// Job и usage создаёт настоящий executor над скрипт-агентом, а не fake.
func TestQueue_GroupIsOneJobAndOneUsage(t *testing.T) {
	store := newTestStore(t)
	sink := &queueJobSink{}
	var usageMu sync.Mutex
	var usageJobIDs []string
	script := `cat >/dev/null
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}'
echo '` + `{"type":"result","subtype":"success","is_error":false,"result":"ok","usage":{"input_tokens":3,"output_tokens":4}}` + `'`
	f := newQueueFactory()
	f.realExec = true
	m, f := newQueueManagerWith(t, f, Config{
		Store: store, Command: "bash", ExtraArgs: []string{"-c", script},
		Job: &executor.JobConfig{Sink: sink},
		OnUsage: func(o accounting.Observation) {
			usageMu.Lock()
			defer usageMu.Unlock()
			usageJobIDs = append(usageJobIDs, o.JobID)
		},
	})
	enqueue(t, m, "cA", "AAA", "")
	runA := f.next(t)
	b := enqueue(t, m, "cB", "BBB", "")
	c := enqueue(t, m, "cC", "CCC", "")
	d := enqueue(t, m, "cD", "DDD", "")
	if _, err := m.CancelQueued(c.MessageID); err != nil {
		t.Fatal(err)
	}
	runA.finish()
	group := f.next(t)
	cur := group.prompt[strings.Index(group.prompt, CurrentHeading):]
	if !strings.Contains(cur, "BBB") || !strings.Contains(cur, "DDD") || strings.Contains(group.prompt, "CCC") || strings.Contains(runA.prompt, "CCC") {
		t.Fatalf("group prompt:\n%s", group.prompt)
	}
	group.finish()
	if !m.Drain(ctxWithTimeout(t, 5*time.Second)) {
		t.Fatal("drain")
	}
	started, finished := sink.jobIDs(agentjobs.TypeStarted), sink.jobIDs(agentjobs.TypeFinished)
	if len(started) != 2 || started[0] == "" || started[0] == started[1] {
		t.Fatalf("started jobs = %v, want 2 distinct non-empty IDs (A and the B+D group)", started)
	}
	if len(sink.jobIDs(agentjobs.TypeStartFailed)) != 0 {
		t.Fatal("unexpected start_failed job")
	}
	if !sameElements(started, finished) {
		t.Fatalf("finished jobs %v do not match started %v", finished, started)
	}
	usageMu.Lock()
	defer usageMu.Unlock()
	if !sameElements(started, usageJobIDs) {
		t.Fatalf("usage job IDs %v do not match started jobs %v", usageJobIDs, started)
	}
	for id, want := range map[string]MessageState{b.MessageID: MessageStateCompleted, d.MessageID: MessageStateCompleted, c.MessageID: MessageStateCancelled} {
		if msg, _ := store.Conversation().Message(id); msg.State != want {
			t.Fatalf("message %s = %q, want %q", id, msg.State, want)
		}
	}
	if f.calls.Load() != 2 {
		t.Fatalf("runs = %d, want 2", f.calls.Load())
	}
}

func sameElements(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// События доходят до подписчика строго в порядке seq, даже когда отмена и вывод агента пишутся параллельно.
func TestQueue_SubscribersSeeEventsInSeqOrder(t *testing.T) {
	store := newTestStore(t)
	f := newQueueFactory()
	inHook, proceed := make(chan struct{}), make(chan struct{})
	m := New(Config{Store: store, Command: "claude", NewExecutor: f.make})
	m.afterAppend = func(e Event) {
		if e.Type == EventMessageCancelled {
			close(inHook)
			<-proceed // отмена записана, но ещё не опубликована
		}
	}
	m.Start()
	var release sync.Once
	openGate := func() { release.Do(func() { close(proceed) }) }
	t.Cleanup(func() { openGate(); m.Shutdown(); m.Drain(context.Background()) })
	enqueue(t, m, "cA", "A", "")
	runA := f.next(t)
	b := enqueue(t, m, "cB", "BBB", "")
	events, cancel := m.Subscribe()
	defer cancel()

	go m.CancelQueued(b.MessageID) //nolint:errcheck
	<-inHook
	go runA.finish() // вывод агента пишется, пока отмена не опубликована
	select {
	case e := <-events:
		t.Fatalf("event seq %d (%s) published before the earlier cancel", e.Seq, e.Type)
	case <-time.After(100 * time.Millisecond):
	}
	openGate()

	var last uint64
	for last == 0 || !turnCompletedSeen(store.History(), last) {
		select {
		case e := <-events:
			if e.Seq <= last {
				t.Fatalf("seq %d delivered after %d", e.Seq, last)
			}
			last = e.Seq
		case <-time.After(3 * time.Second):
			t.Fatal("events stalled")
		}
	}
}

func turnCompletedSeen(history []Event, seq uint64) bool {
	for _, e := range history {
		if e.Seq == seq {
			return e.Type == EventTurnCompleted
		}
	}
	return false
}

func TestQueue_RenderHistoryPlacesGroupAtClaimPosition(t *testing.T) {
	store := newTestStore(t)
	m, f := newQueueManager(t, store, ContextBuilder{})
	enqueue(t, m, "cA", "AAA", "")
	runA := f.next(t)
	enqueue(t, m, "cB", "BBB", "")
	c := enqueue(t, m, "cC", "CCC", "")
	if _, err := m.CancelQueued(c.MessageID); err != nil {
		t.Fatal(err)
	}
	runA.finish()
	runB := f.next(t)
	runB.finish()
	waitTurns(t, m, 2)
	enqueue(t, m, "cE", "EEE", "")
	runE := f.next(t)
	hist := runE.prompt[:strings.Index(runE.prompt, CurrentHeading)]
	if strings.Contains(hist, "CCC") {
		t.Fatalf("cancelled text in history:\n%s", hist)
	}
	iA, iAns, iB := strings.Index(hist, "AAA"), strings.Index(hist, "answer-1"), strings.Index(hist, "BBB")
	if iA < 0 || iAns < iA || iB < iAns || strings.Count(runE.prompt, "BBB") != 1 {
		t.Fatalf("history order (A=%d ans=%d B=%d):\n%s", iA, iAns, iB, runE.prompt)
	}
	runE.finish()
	waitTurns(t, m, 3)
}

func TestQueue_EnqueueOnDeadJournalIsStorageUnavailable(t *testing.T) {
	store := newTestStore(t)
	m, f := newQueueManager(t, store, ContextBuilder{})
	store.chatLog.Close()
	_, err := m.Enqueue("cA", "A", TurnTarget{})
	if code := codeOf(t, err); code != ErrCodeStorageUnavailable {
		t.Fatalf("code = %q", code)
	}
	if _, err := m.CancelQueued("m-1"); codeOf(t, err) != ErrCodeStorageUnavailable {
		t.Fatal("cancel on an unavailable store must report storage_unavailable")
	}
	f.expectNoStart(t)
}

func TestQueue_LegacyJournalKeepsSendAndStop(t *testing.T) {
	store := newTestStore(t)
	_, _ = store.Append(EventTurnAccepted, "t-old", TurnAcceptedData{Message: "old", ClientMessageID: "legacy"})
	_, _ = store.Append(EventTurnStarted, "t-old", TurnStartedData{})
	_, _ = store.Append(EventTurnCompleted, "t-old", TurnCompletedData{})
	m, f := newQueueManager(t, store, ContextBuilder{})
	turnID, err := m.Send("mesh:x", "ping", TurnTarget{SourceMessageID: "x", SourceSenderID: "peer"})
	if err != nil {
		t.Fatal(err)
	}
	run := f.next(t)
	if !strings.Contains(run.prompt, "Пользователь: old") {
		t.Fatalf("legacy history missing:\n%s", run.prompt)
	}
	if err := m.Stop(turnID); err != nil {
		t.Fatal(err)
	}
	waitTurns(t, m, 2)
}

// Отказ fsync на приёме: подписчики ничего не видят, ход не стартует, ответ — storage_unavailable.
func TestQueue_UnconfirmedEnqueueIsNotPublishedOrStarted(t *testing.T) {
	store := newTestStore(t)
	m, f := newQueueManager(t, store, ContextBuilder{})
	events, cancel := m.Subscribe()
	defer cancel()
	failSyncAfterWrite(store)
	_, err := m.Enqueue("cA", "A", TurnTarget{})
	if code := codeOf(t, err); code != ErrCodeStorageUnavailable {
		t.Fatalf("code = %q", code)
	}
	select {
	case e := <-events:
		t.Fatalf("unconfirmed write published %s", e.Type)
	default:
	}
	f.expectNoStart(t)
}
