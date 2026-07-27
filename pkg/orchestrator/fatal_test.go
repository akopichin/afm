package orchestrator

import (
	"context"
	"errors"
	"testing"

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
	o := &Orchestrator{fsm: NewFSM(store), ui: NewUIBus(), critical: NewCriticalBus(16)}

	// Событие с неверным From → CAS-mismatch → benign.
	_, ok := o.Trigger("a", EvComplete, GuardCtx{}, "") // из pending complete не разрешён
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
		fsm:       NewFSM(store),
		ui:        NewUIBus(),
		critical:  NewCriticalBus(16),
		cancelRun: cancel,
	}

	_, ok := o.Trigger("a", FSMEvent("no_such_event"), GuardCtx{}, "")
	if ok {
		t.Fatal("expected transition to be rejected for unknown event")
	}
	if got := o.loadFatal(); got != nil {
		t.Fatalf("ErrNoRule must not set fatal, got %v", got)
	}
}
