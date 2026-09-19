package concurrency

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
)

// orderedSemaphore — семафор-шпион для юнит-тестов Lease: не блокирует, а
// детерминированно записывает порядок acquire/release в общий лог (по имени
// команды), без горутин/таймингов — прямая проверка "release раньше acquire".
type orderedSemaphore struct {
	name string
	mu   *sync.Mutex
	log  *[]string
}

func newOrderedSemaphores(log *[]string) (a, b Semaphore) {
	var mu sync.Mutex
	return orderedSemaphore{name: "a", mu: &mu, log: log}, orderedSemaphore{name: "b", mu: &mu, log: log}
}

func (s orderedSemaphore) record(action string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.log = append(*s.log, action+":"+s.name)
}

func (s orderedSemaphore) acquire()                             { s.record("acquire") }
func (s orderedSemaphore) acquireCtx(ctx context.Context) error { s.record("acquire"); return nil }
func (s orderedSemaphore) release()                             { s.record("release") }

func TestAcquireLease_AcquiresSlotAndMarksHeld(t *testing.T) {
	var log []string
	semA, _ := newOrderedSemaphores(&log)
	m := NewWithSemaphores(bus.NewCriticalBus(16), map[string]Semaphore{"a": semA}, "")

	lease, err := m.AcquireLease(context.Background(), "a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !lease.held || lease.cmd != "a" {
		t.Fatalf("lease should hold cmd=a, got held=%v cmd=%q", lease.held, lease.cmd)
	}
	if want := []string{"acquire:a"}; !equalLogs(log, want) {
		t.Errorf("log = %v, want %v", log, want)
	}
}

func TestAcquireLease_CtxCanceled_ReturnsErrorWithoutHolding(t *testing.T) {
	blocking := ChannelSemaphore(make(chan struct{}, 1))
	blocking <- struct{}{} // занята — acquireCtx будет ждать
	m := NewWithSemaphores(bus.NewCriticalBus(16), map[string]Semaphore{"a": blocking}, "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lease, err := m.AcquireLease(ctx, "a")
	if err == nil {
		t.Fatal("expected error for already-canceled ctx")
	}
	if lease != nil {
		t.Fatalf("expected nil lease on acquire failure, got %+v", lease)
	}
}

func TestLease_SwapTo_SameCommand_NoReacquireNoRelease(t *testing.T) {
	var log []string
	semA, _ := newOrderedSemaphores(&log)
	m := NewWithSemaphores(bus.NewCriticalBus(16), map[string]Semaphore{"a": semA}, "")

	lease, err := m.AcquireLease(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.SwapTo(context.Background(), "a"); err != nil {
		t.Fatalf("same-command SwapTo must not fail: %v", err)
	}
	if want := []string{"acquire:a"}; !equalLogs(log, want) {
		t.Errorf("same-command SwapTo must not touch the semaphore again, log = %v, want %v", log, want)
	}
	if !lease.held || lease.cmd != "a" {
		t.Fatalf("lease should still hold cmd=a, got held=%v cmd=%q", lease.held, lease.cmd)
	}
}

func TestLease_SwapTo_DifferentCommand_ReleasesBeforeAcquiring(t *testing.T) {
	var log []string
	semA, semB := newOrderedSemaphores(&log)
	m := NewWithSemaphores(bus.NewCriticalBus(16), map[string]Semaphore{"a": semA, "b": semB}, "")

	lease, err := m.AcquireLease(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.SwapTo(context.Background(), "b"); err != nil {
		t.Fatalf("cross-command SwapTo failed: %v", err)
	}
	want := []string{"acquire:a", "release:a", "acquire:b"}
	if !equalLogs(log, want) {
		t.Errorf("log = %v, want %v (release must precede the new acquire)", log, want)
	}
	if !lease.held || lease.cmd != "b" {
		t.Fatalf("lease should hold cmd=b after swap, got held=%v cmd=%q", lease.held, lease.cmd)
	}
}

func TestLease_Release_IsIdempotent(t *testing.T) {
	var log []string
	semA, _ := newOrderedSemaphores(&log)
	m := NewWithSemaphores(bus.NewCriticalBus(16), map[string]Semaphore{"a": semA}, "")

	lease, err := m.AcquireLease(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	lease.Release() // повторный вызов не должен освобождать слот второй раз
	want := []string{"acquire:a", "release:a"}
	if !equalLogs(log, want) {
		t.Errorf("log = %v, want %v (Release must be idempotent)", log, want)
	}
	if lease.held {
		t.Error("lease.held must be false after Release")
	}
}

// TestLease_SwapTo_EmptyCommandNormalizesToDefault_NoOp — стадия-автор без
// явной Stage.Command (использует дефолтную команду Manager'а через "").
// SwapTo с ЯВНЫМ именем той же дефолтной команды должен распознаться как
// "та же команда" и остаться no-op — иначе сравнение НЕнормализованных строк
// ("" != "claude") лишний раз дёрнуло бы release+acquire того же семафора.
func TestLease_SwapTo_EmptyCommandNormalizesToDefault_NoOp(t *testing.T) {
	var log []string
	semA, _ := newOrderedSemaphores(&log)
	m := NewWithSemaphores(bus.NewCriticalBus(16), map[string]Semaphore{"a": semA}, "a")

	lease, err := m.AcquireLease(context.Background(), "") // "" → дефолт "a"
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.SwapTo(context.Background(), "a"); err != nil {
		t.Fatalf("SwapTo to the resolved default command must not fail: %v", err)
	}
	if want := []string{"acquire:a"}; !equalLogs(log, want) {
		t.Errorf("SwapTo to the same resolved command must be a no-op, log = %v, want %v", log, want)
	}
}

// TestNew_PreCreatesSemaphoreForVerifyOnlyCommand — codex НЕ является
// Stage.Command ни одной стадии, только команда агентского verify-шага. New
// должен предсоздать для него реальный семафор с лимитом globalMaxParallel
// (то же правило, что и для любой нелимитированной команды) — иначе
// AcquireLease/SwapTo на codex молча откатились бы на noopSemaphore
// (semForCmd) и переход автор→верификатор не ограничивался бы вовсе.
func TestNew_PreCreatesSemaphoreForVerifyOnlyCommand(t *testing.T) {
	stages := []flow.Stage{
		{ID: "s1", Command: "claude", Verify: flow.VerifySpec{
			Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}},
		}},
	}
	m := New(bus.NewCriticalBus(16), stages, "claude", 1, nil)

	lease, err := m.AcquireLease(context.Background(), "codex")
	if err != nil {
		t.Fatalf("unexpected error acquiring verify-only command lease: %v", err)
	}
	defer lease.Release()

	// globalMaxParallel=1 → второй захват той же команды должен блокироваться,
	// а не пройти как через noopSemaphore.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.AcquireLease(ctx, "codex"); err == nil {
		t.Error("expected the codex semaphore to be a real limit=1 ChannelSemaphore, not a noopSemaphore fallback")
	}
}

func equalLogs(got, want []string) bool {
	return slices.Equal(got, want)
}
