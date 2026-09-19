package concurrency

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

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

// TestSpawnAgentLease_PassesHeldLeaseForStageCommand — базовая проверка
// проводки: callback получает lease, изначально держащий слот Stage.Command,
// а не пустой/nil.
func TestSpawnAgentLease_PassesHeldLeaseForStageCommand(t *testing.T) {
	m := New(bus.NewCriticalBus(16), nil, "", 0, nil)
	done := make(chan struct{})
	m.SpawnAgentLease(context.Background(), flow.Stage{ID: "a", Command: "claude"}, func(ctx context.Context, s flow.Stage, lease *Lease) {
		if !lease.held || lease.cmd != "claude" {
			t.Errorf("lease should hold cmd=claude at start, got held=%v cmd=%q", lease.held, lease.cmd)
		}
		close(done)
	})
	<-done
	m.WaitAgents()
}

// TestSpawnAgentLease_SameCommandSwapTo_NoSelfDeadlock — на
// max_parallel=1 SwapTo на ТУ ЖЕ команду не должен освобождать+заново
// захватывать единственный слот (иначе это была бы гонка сама с собой —
// self-дедлок, если бы кто-то другой успел перехватить слот в промежутке).
func TestSpawnAgentLease_SameCommandSwapTo_NoSelfDeadlock(t *testing.T) {
	sem := ChannelSemaphore(make(chan struct{}, 1))
	m := NewWithSemaphores(bus.NewCriticalBus(16), map[string]Semaphore{"a": sem}, "")

	done := make(chan error, 1)
	m.SpawnAgentLease(context.Background(), flow.Stage{ID: "s1", Command: "a"}, func(ctx context.Context, s flow.Stage, lease *Lease) {
		done <- lease.SwapTo(ctx, "a")
	})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("same-command SwapTo must not fail: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("same-command SwapTo deadlocked on max_parallel=1")
	}
	m.WaitAgents()
}

// TestLease_SwapTo_CrossCommandReleasesBeforeAcquiring доказывает порядок
// "release старого слота СТРОГО до acquire нового" через цепочку каналов,
// без единого sleep: слот "B" изначально занят посторонним — SwapTo(B)
// может завершиться, только когда кто-то его освободит; этим "кем-то"
// выступает waiterA, который сам может захватить "A" только после того, как
// SwapTo реально освободил "A". Захват "A" исходным lease делается
// синхронно ДО запуска waiterA (иначе оба захвата "A" — исходный и
// waiterA — гонялись бы за одним и тем же изначально пустым каналом:
// SpawnAgentLease здесь намеренно не используется, т.к. его собственный
// AcquireLease не синхронизирован с тестом никаким барьером). Порядок
// событий:
//
//	SwapTo релизит A → waiterA захватывает A → waiterA освобождает B → SwapTo захватывает B
func TestLease_SwapTo_CrossCommandReleasesBeforeAcquiring(t *testing.T) {
	semA := ChannelSemaphore(make(chan struct{}, 1))
	semB := ChannelSemaphore(make(chan struct{}, 1))
	semB <- struct{}{} // "B" занята посторонним — освобождается только waiterA

	m := NewWithSemaphores(bus.NewCriticalBus(16), map[string]Semaphore{"a": semA, "b": semB}, "")

	lease, err := m.AcquireLease(context.Background(), "a") // синхронно, без гонки с waiterA
	if err != nil {
		t.Fatal(err)
	}

	waiterAAcquired := make(chan struct{})
	go func() {
		semA.acquire() // блокируется, пока SwapTo не освободит A
		close(waiterAAcquired)
		<-semB // освобождает B — только теперь acquire(B) внутри SwapTo может пройти
	}()

	swapErr := make(chan error, 1)
	go func() {
		swapErr <- lease.SwapTo(context.Background(), "b")
	}()

	select {
	case <-waiterAAcquired:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter on A never proceeded — SwapTo did not release A before acquiring B")
	}
	select {
	case err := <-swapErr:
		if err != nil {
			t.Fatalf("cross-command SwapTo failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SwapTo(B) never completed after A was released and B was freed")
	}
}

// TestSpawnAgentLease_ABBA_NoDeadlock — классический AB-BA: одна горутина
// держит A и меняет на B, другая держит B и меняет на A, оба лимита — 1.
// Поскольку SwapTo сначала release, потом acquire (не "hold and wait"),
// цикл ожидания невозможен в принципе — обе горутины обязаны завершиться в
// ограниченное время.
func TestSpawnAgentLease_ABBA_NoDeadlock(t *testing.T) {
	semA := ChannelSemaphore(make(chan struct{}, 1))
	semB := ChannelSemaphore(make(chan struct{}, 1))
	m := NewWithSemaphores(bus.NewCriticalBus(16), map[string]Semaphore{"a": semA, "b": semB}, "")

	errCh1 := make(chan error, 1)
	errCh2 := make(chan error, 1)
	m.SpawnAgentLease(context.Background(), flow.Stage{ID: "g1", Command: "a"}, func(ctx context.Context, s flow.Stage, lease *Lease) {
		errCh1 <- lease.SwapTo(ctx, "b")
	})
	m.SpawnAgentLease(context.Background(), flow.Stage{ID: "g2", Command: "b"}, func(ctx context.Context, s flow.Stage, lease *Lease) {
		errCh2 <- lease.SwapTo(ctx, "a")
	})

	timeout := time.After(2 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case err := <-errCh1:
			if err != nil {
				t.Errorf("g1 SwapTo failed: %v", err)
			}
			errCh1 = nil
		case err := <-errCh2:
			if err != nil {
				t.Errorf("g2 SwapTo failed: %v", err)
			}
			errCh2 = nil
		case <-timeout:
			t.Fatal("AB-BA deadlock: not both swaps completed in time")
		}
	}
	m.WaitAgents()
}

// TestLease_SwapTo_CancelWhileWaitingForFullTarget — целевой слот "B"
// постоянно занят (никто его не освобождает во время попытки): отмена ctx
// должна вернуть ошибку без захвата B, и удерживаемый ранее слот "A" должен
// быть уже освобождён (release-before-acquire срабатывает и на неудачном
// свопе). После теста оба слота проверяются напрямую на канале — ни один не
// "утёк" в фоновой горутине.
func TestLease_SwapTo_CancelWhileWaitingForFullTarget(t *testing.T) {
	semA := ChannelSemaphore(make(chan struct{}, 1))
	semB := ChannelSemaphore(make(chan struct{}, 1))
	semB <- struct{}{} // B занята весь тест — acquire(B) внутри SwapTo не может пройти

	m := NewWithSemaphores(bus.NewCriticalBus(16), map[string]Semaphore{"a": semA, "b": semB}, "")

	lease, err := m.AcquireLease(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- lease.SwapTo(ctx, "b")
	}()
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected SwapTo to fail after ctx cancellation while B stays full")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SwapTo did not return after ctx cancellation — goroutine leak")
	}
	if lease.held {
		t.Error("lease must hold nothing after a failed SwapTo")
	}

	// A уже освобождён неудачным SwapTo — новый захватчик может его занять.
	select {
	case semA <- struct{}{}:
	default:
		t.Error("slot A must have been released by the failed SwapTo attempt")
	}

	// Неудавшийся SwapTo не "украл" B: когда исходный держатель B
	// освобождает слот, он остаётся пустым и доступным — не занят фоновой
	// утечкой от отменённой попытки.
	<-semB
	select {
	case semB <- struct{}{}:
		<-semB
	default:
		t.Error("slot B must be free for a new acquirer; a leaked goroutine may have stolen it")
	}
}
