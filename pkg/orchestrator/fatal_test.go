package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Unit-тест не покрывает StorageError из Store: для инъекции I/O-сбоя нужен отдельный seam.

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

// setFatal/loadFatal: первая ошибка фиксируется, повторные вызовы её не перезаписывают, и cancelRun
// вызывается на каждый setFatal, чтобы event loop гарантированно проснулся и завершился.
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

// publishCritical raced against ctx cancellation used to be a bare `_ = o.critical.Publish` at every call
// site — a dropped event left zero trace anywhere.
func TestPublishCritical_CancelledCtxDoesNotBlockOrPanic(t *testing.T) {
	o := &Orchestrator{critical: bus.NewCriticalBus(1)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Fill the buffer so only cancellation is ready; otherwise select may randomly choose send.
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

// ErrNoRule НЕ должен валить run: Trigger обязан отличать *StorageError от прочих ошибок fsm.Apply.
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
