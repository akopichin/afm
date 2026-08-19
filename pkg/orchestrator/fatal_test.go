package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Примечание: настоящий путь *StorageError → Trigger завершает run НЕ покрыт
// unit-тестом здесь — *state.Store конкретный тип, и заставить его вернуть
// I/O-ошибку без Store-интерфейсного шва нельзя. Добавление такого шва — вне
// рамок этого фикса (см. отчёт задачи).

// concurrent-change НЕ должен валить run — это доброкачественный no-op.
func TestTrigger_ConcurrentChangeIsBenign(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	o := &Orchestrator{fsm: bus.NewFSM(store), ui: bus.NewUIBus(), critical: bus.NewCriticalBus(16)}

	// Событие с неверным From → CAS-mismatch → benign.
	_, ok := o.Trigger("a", bus.EvComplete, bus.GuardCtx{}, "") // из pending complete не разрешён
	if ok {
		t.Fatal("expected transition to be rejected")
	}
	if o.loadFatal() != nil {
		t.Fatalf("concurrent-change must not set fatal: %v", o.loadFatal())
	}
}

func TestStore_ApplyConcurrentChangeSentinel(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	err = store.Apply(&state.Transition{StageID: "a", From: state.StatusRunning, To: state.StatusDone, Event: "x"})
	if !errors.Is(err, state.ErrConcurrentChange) {
		t.Fatalf("want ErrConcurrentChange, got %v", err)
	}
}

// setFatal/loadFatal: первая ошибка фиксируется, повторные вызовы её не
// перезаписывают (первая storage-fatal ошибка важнее последующих), и
// cancelRun вызывается на каждый setFatal, чтобы event loop гарантированно
// проснулся и завершился.
func TestSetFatal_RecordsFirstErrorAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	o := &Orchestrator{cancelRun: cancel}

	first := errors.New("first storage failure")
	second := errors.New("second storage failure")

	o.setFatal(first)
	if got := o.loadFatal(); !errors.Is(got, first) {
		t.Fatalf("loadFatal() = %v, want %v", got, first)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("setFatal must cancel the run context")
	}

	// Второй вызов НЕ должен перезаписать первую ошибку.
	o.setFatal(second)
	if got := o.loadFatal(); !errors.Is(got, first) {
		t.Fatalf("loadFatal() after second setFatal = %v, want first error %v", got, first)
	}
}

// publishCritical raced against ctx cancellation used to be a bare
// `_ = o.critical.Publish(ctx, ev)` at every call site — a dropped event
// (e.g. setFatal from an unrelated stage cancelling the shared o.runCtx at
// the exact instant this stage's own EventAgentCompleted tries to publish)
// left zero trace anywhere. This only verifies the non-blocking/non-panicking
// contract on both outcomes; the log line itself isn't asserted (no existing
// convention in this package for capturing log.Printf output).
func TestPublishCritical_CancelledCtxDoesNotBlockOrPanic(t *testing.T) {
	o := &Orchestrator{critical: bus.NewCriticalBus(1)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Fill the buffer first so the send case in Publish's select isn't ready —
	// otherwise, with an empty buffer and an already-cancelled ctx, BOTH
	// select cases are simultaneously ready and Go picks between them at
	// random (this is exactly the real race publishCritical exists to make
	// visible, not a test bug to paper over — but asserting a specific
	// non-blocking outcome needs the send case genuinely blocked).
	o.critical.TryPublish(bus.Event{Type: bus.EventAgentCompleted, StageID: "filler"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		o.publishCritical(ctx, bus.Event{Type: bus.EventAgentCompleted, StageID: "a"})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publishCritical blocked on an already-cancelled ctx")
	}

	ev := <-o.critical.Recv()
	if ev.StageID != "filler" {
		t.Fatalf("event should not have been delivered, got %+v", ev)
	}
	select {
	case ev := <-o.critical.Recv():
		t.Fatalf("only the filler event should be in the buffer, also got %+v", ev)
	default:
	}
}

func TestPublishCritical_LiveCtxDeliversEvent(t *testing.T) {
	o := &Orchestrator{critical: bus.NewCriticalBus(1)}
	o.publishCritical(context.Background(), bus.Event{Type: bus.EventAgentCompleted, StageID: "a"})

	select {
	case ev := <-o.critical.Recv():
		if ev.StageID != "a" {
			t.Fatalf("got stage %q, want \"a\"", ev.StageID)
		}
	default:
		t.Fatal("event should have been delivered on a live ctx with room in the buffer")
	}
}

// ErrNoRule (неизвестное FSM-событие — баг в коде, не проблема storage)
// НЕ должен валить run: Trigger обязан отличать *StorageError (fatal) от
// прочих ошибок fsm.Apply (лог + отказ перехода, run продолжается).
func TestTrigger_NoRuleIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	_, cancel := context.WithCancel(context.Background())
	o := &Orchestrator{
		opts:      Options{Store: store},
		fsm:       bus.NewFSM(store),
		ui:        bus.NewUIBus(),
		critical:  bus.NewCriticalBus(16),
		cancelRun: cancel,
	}

	_, ok := o.Trigger("a", bus.FSMEvent("no_such_event"), bus.GuardCtx{}, "")
	if ok {
		t.Fatal("expected transition to be rejected for unknown event")
	}
	if got := o.loadFatal(); got != nil {
		t.Fatalf("ErrNoRule must not set fatal, got %v", got)
	}
}
