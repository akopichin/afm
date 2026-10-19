package concurrency

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
)

func TestSpawnAgent_TracksActiveAndWaitGroup(t *testing.T) {
	m := New(bus.NewCriticalBus(16), nil, "", 0, nil)
	var ran atomic.Bool
	done := make(chan struct{})
	m.SpawnAgent(context.Background(), flow.Stage{ID: "a"}, func(ctx context.Context, s flow.Stage) {
		if !m.IsActive("a") {
			t.Error("stage should be marked active while agent runs")
		}
		ran.Store(true)
		close(done)
	})
	<-done
	m.WaitAgents()
	if !ran.Load() {
		t.Fatal("agent function did not run")
	}
	if m.IsActive("a") {
		t.Fatal("stage should not be active after agent completes")
	}
}

func TestSpawnDetached_TracksWaitGroupWithoutSemaphoreOrActiveMarker(t *testing.T) {
	fullSem := ChannelSemaphore(make(chan struct{}, 1))
	fullSem <- struct{}{} // occupy the only slot, as the blocked main agent would
	m := NewWithSemaphores(bus.NewCriticalBus(16), map[string]Semaphore{"": fullSem}, "")

	var ran atomic.Bool
	done := make(chan struct{})
	m.SpawnDetached(context.Background(), func(ctx context.Context) {
		if m.IsActive("a") {
			t.Error("detached helper must not mark any stage active")
		}
		ran.Store(true)
		close(done)
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("detached helper deadlocked behind the full command semaphore")
	}
	m.WaitAgents()
	if !ran.Load() {
		t.Fatal("detached helper did not run")
	}
}

func TestSpawnAgent_BlocksOnFullSemaphore(t *testing.T) {
	blockSem := ChannelSemaphore(make(chan struct{}, 1))
	blockSem <- struct{}{} // занят
	m := NewWithSemaphores(bus.NewCriticalBus(16), map[string]Semaphore{"": blockSem}, "")
	var ran atomic.Bool
	m.SpawnAgent(context.Background(), flow.Stage{ID: "a"}, func(ctx context.Context, s flow.Stage) {
		ran.Store(true)
	})
	time.Sleep(20 * time.Millisecond)
	if ran.Load() {
		t.Fatal("agent should be blocked on full semaphore")
	}
	<-blockSem // отпускаем
	m.WaitAgents()
	if !ran.Load() {
		t.Fatal("agent should run after semaphore released")
	}
}

func TestSpawnAgent_SkipsRunWhenShouldRunFalse(t *testing.T) {
	m := New(bus.NewCriticalBus(16), nil, "", 0, nil)
	m.shouldRun = func(stageID string) bool { return false }
	var ran atomic.Bool
	m.SpawnAgent(context.Background(), flow.Stage{ID: "a"}, func(ctx context.Context, s flow.Stage) {
		ran.Store(true)
	})
	m.WaitAgents()
	if ran.Load() {
		t.Fatal("run must not be called when shouldRun returns false")
	}
}

func TestSpawnAgent_SkipsRunWhenPausedWhileQueuedBehindSemaphore(t *testing.T) {
	blockSem := ChannelSemaphore(make(chan struct{}, 1))
	blockSem <- struct{}{} // occupied
	m := NewWithSemaphores(bus.NewCriticalBus(16), map[string]Semaphore{"": blockSem}, "")
	var paused atomic.Bool
	m.shouldRun = func(stageID string) bool { return !paused.Load() }

	var ran atomic.Bool
	m.SpawnAgent(context.Background(), flow.Stage{ID: "a"}, func(ctx context.Context, s flow.Stage) {
		ran.Store(true)
	})
	time.Sleep(20 * time.Millisecond) // goroutine is now queued on the semaphore
	paused.Store(true)                // Pause() fires while still queued
	<-blockSem                        // release the slot
	m.WaitAgents()

	if ran.Load() {
		t.Fatal("run should be skipped — stage was paused while queued behind the semaphore")
	}
}

func TestChannelSemaphore_AcquireCtx_AlreadyCanceled(t *testing.T) {
	sem := ChannelSemaphore(make(chan struct{}, 1))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := sem.acquireCtx(ctx); err == nil {
		t.Fatal("acquireCtx с уже отменённым ctx должен вернуть ошибку")
	}
	select {
	case sem <- struct{}{}:
		// слот свободен — acquireCtx не забрал его
	default:
		t.Fatal("acquireCtx не должен был занять слот при отменённом ctx")
	}
}

func TestChannelSemaphore_AcquireCtx_CancelWhileWaiting(t *testing.T) {
	sem := ChannelSemaphore(make(chan struct{}, 1))
	sem <- struct{}{} // занимаем единственный слот

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- sem.acquireCtx(ctx)
	}()

	// Даём горутине шанс встать в ожидание, затем отменяем.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("acquireCtx должен вернуть ошибку после отмены ctx")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acquireCtx не вернулся после отмены ctx — утечка горутины")
	}

	// Слот всё ещё занят оригинальным держателем и освобождается им.
	select {
	case <-sem:
	default:
		t.Fatal("оригинальный держатель должен иметь возможность освободить свой слот")
	}
}

// TestNoopSemaphore_AcquireCtx_LiveCtxReturnsNil — noop-семафор (без лимита)
// по-прежнему никогда не блокирует захват слота при живом ctx.
func TestNoopSemaphore_AcquireCtx_LiveCtxReturnsNil(t *testing.T) {
	var s noopSemaphore
	if err := s.acquireCtx(context.Background()); err != nil {
		t.Fatalf("noopSemaphore.acquireCtx с живым ctx должен вернуть nil, got %v", err)
	}
}

func TestNoopSemaphore_AcquireCtx_AlreadyCancelledReturnsErr(t *testing.T) {
	var s noopSemaphore
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.acquireCtx(ctx); err == nil {
		t.Fatal("noopSemaphore.acquireCtx с уже отменённым ctx должен вернуть ошибку, got nil")
	}
}

func TestWakeEventLoop_PublishesToBus(t *testing.T) {
	cb := bus.NewCriticalBus(1)
	m := New(cb, nil, "", 0, nil)
	m.WakeEventLoop()
	select {
	case <-cb.Recv():
	default:
		t.Fatal("WakeEventLoop should publish to critical bus")
	}
}

// ---- T3.3: SpawnOwned ----

func TestSpawnOwned_TwoTasksSameStageBothTracked(t *testing.T) {
	m := New(bus.NewCriticalBus(16), nil, "", 0, nil)
	release1 := make(chan struct{})
	release2 := make(chan struct{})
	started1 := make(chan struct{})
	started2 := make(chan struct{})

	// Distinct commands: this test is about owner-key tracking, not about command-semaphore sharing (that's
	// TestSpawnOwned_SharesCommandSemaphoreWithStages) — same command would serialize the two on purpose.
	m.SpawnOwned(context.Background(), TaskOwnerKey("t1"), "cmd-a", nil, func(ctx context.Context, lease *Lease) {
		close(started1)
		<-release1
	})
	<-started1
	m.SpawnOwned(context.Background(), TaskOwnerKey("t2"), "cmd-b", nil, func(ctx context.Context, lease *Lease) {
		close(started2)
		<-release2
	})
	<-started2

	if _, ok := m.activeAgents.Load(TaskOwnerKey("t1")); !ok {
		t.Error("task t1 should be tracked active while its run is in flight")
	}
	if _, ok := m.activeAgents.Load(TaskOwnerKey("t2")); !ok {
		t.Error("task t2 should be tracked active — it must not clobber t1's entry")
	}
	close(release1)
	close(release2)
	m.WaitAgents()
	if _, ok := m.activeAgents.Load(TaskOwnerKey("t1")); ok {
		t.Error("task t1 should no longer be tracked after it finishes")
	}
}

func TestSpawnOwned_SharesCommandSemaphoreWithStages(t *testing.T) {
	blockSem := ChannelSemaphore(make(chan struct{}, 1))
	blockSem <- struct{}{} // occupied, as a running stage on "cmd" would hold it
	m := NewWithSemaphores(bus.NewCriticalBus(16), map[string]Semaphore{"cmd": blockSem}, "cmd")

	var ran atomic.Bool
	m.SpawnOwned(context.Background(), TaskOwnerKey("t1"), "cmd", nil, func(ctx context.Context, lease *Lease) {
		ran.Store(true)
	})
	time.Sleep(20 * time.Millisecond)
	if ran.Load() {
		t.Fatal("helper task must wait behind the SAME command semaphore a running stage holds")
	}
	<-blockSem // release — as if the stage finished
	m.WaitAgents()
	if !ran.Load() {
		t.Fatal("helper task should run once the command slot is released")
	}
}

func TestSpawnOwned_ShouldRunCheckedAfterAcquire(t *testing.T) {
	blockSem := ChannelSemaphore(make(chan struct{}, 1))
	blockSem <- struct{}{}
	m := NewWithSemaphores(bus.NewCriticalBus(16), map[string]Semaphore{"cmd": blockSem}, "cmd")
	var paused atomic.Bool
	shouldRun := func() bool { return !paused.Load() }

	var ran atomic.Bool
	m.SpawnOwned(context.Background(), TaskOwnerKey("t1"), "cmd", shouldRun, func(ctx context.Context, lease *Lease) {
		ran.Store(true)
	})
	time.Sleep(20 * time.Millisecond) // goroutine now queued on the semaphore
	paused.Store(true)                // task paused while still queued
	<-blockSem
	m.WaitAgents()

	if ran.Load() {
		t.Fatal("run must be skipped — shouldRun flipped false while queued behind the semaphore")
	}
}

func TestSpawnOwned_CountedInDrain(t *testing.T) {
	m := New(bus.NewCriticalBus(16), nil, "", 0, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	var finished atomic.Bool

	m.SpawnOwned(context.Background(), TaskOwnerKey("t1"), "", nil, func(ctx context.Context, lease *Lease) {
		close(started)
		<-release
		finished.Store(true)
	})
	<-started

	done := make(chan struct{})
	go func() {
		m.WaitAgents()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("WaitAgents returned before the helper task finished")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	<-done
	if !finished.Load() {
		t.Fatal("helper task body never ran to completion")
	}
}

// TestWaitAgents_DrainTimeoutFiresViaFakeClock asserts the drain WARN timeout is clock-driven: a stuck
// agent goroutine that never finishes makes WaitAgents return once the clock is advanced past
// agentDrainTimeout, not after a real 10s wait (T10, dev-plan.md).
func TestWaitAgents_DrainTimeoutFiresViaFakeClock(t *testing.T) {
	fc := clock.NewFake(time.Unix(0, 0))
	m := New(bus.NewCriticalBus(16), nil, "", 0, nil)
	m.Clock = fc

	started := make(chan struct{})
	stuck := make(chan struct{}) // never closed — this agent goroutine never finishes
	m.SpawnOwned(context.Background(), TaskOwnerKey("t1"), "", nil, func(ctx context.Context, lease *Lease) {
		close(started)
		<-stuck
	})
	<-started

	done := make(chan struct{})
	go func() {
		m.WaitAgents()
		close(done)
	}()

	fc.BlockUntil(1)
	fc.Advance(agentDrainTimeout)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitAgents did not return after the drain timeout should have fired")
	}
}

func TestSpawnOwned_UnknownCommandDoesNotGetNoopSemaphore(t *testing.T) {
	m := NewWithSemaphores(bus.NewCriticalBus(16), map[string]Semaphore{}, "")

	started1 := make(chan struct{})
	release1 := make(chan struct{})
	m.SpawnOwned(context.Background(), TaskOwnerKey("t1"), "mystery-cmd", nil, func(ctx context.Context, lease *Lease) {
		close(started1)
		<-release1
	})
	<-started1

	var ran2 atomic.Bool
	m.SpawnOwned(context.Background(), TaskOwnerKey("t2"), "mystery-cmd", nil, func(ctx context.Context, lease *Lease) {
		ran2.Store(true)
	})
	time.Sleep(20 * time.Millisecond)
	if ran2.Load() {
		t.Fatal("an unknown command must still cap concurrency — a 2nd task on it ran in parallel with the 1st (unbounded noop semaphore)")
	}
	close(release1)
	m.WaitAgents()
	if !ran2.Load() {
		t.Fatal("2nd task on the unknown command should run after the 1st releases its slot")
	}
}

// Two run agents with one undeclared profile command (QA and DevOps on one recipe) work side by side under
// the run's limit, instead of strictly one after another; a global max_parallel still caps them.
func TestSpawnOwned_UnknownCommandSharesTheRunLimit(t *testing.T) {
	for _, tc := range []struct {
		name         string
		maxParallel  int
		wantParallel bool
	}{
		{"no global limit", 0, true},
		{"global limit 2", 2, true},
		{"global limit 1", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New(bus.NewCriticalBus(16), nil, "", tc.maxParallel, nil)
			release := make(chan struct{})
			started := make(chan struct{}, 2)
			for _, id := range []string{"qa", "devops"} {
				m.SpawnOwned(context.Background(), TaskOwnerKey(id), "developer", nil, func(ctx context.Context, lease *Lease) {
					started <- struct{}{}
					<-release
				})
			}
			<-started
			select {
			case <-started:
				if !tc.wantParallel {
					t.Error("the second agent started while the first held the only slot")
				}
			case <-time.After(100 * time.Millisecond):
				if tc.wantParallel {
					t.Error("the second agent did not start while the first was still running")
				}
			}
			close(release)
			m.WaitAgents()
		})
	}
}

// One owner never runs two goroutines at once: a second spawn for the same owner starts only after the first
// has finished completely, its lease released — even with free slots. A different owner is not held up.
func TestSpawnOwned_SameOwnerRunsOneAtATime(t *testing.T) {
	m := New(bus.NewCriticalBus(16), nil, "", 4, nil)
	release := make(chan struct{})
	started := make(chan string, 3)
	spawn := func(owner, name string) {
		m.SpawnOwned(context.Background(), TaskOwnerKey(owner), "developer", nil, func(ctx context.Context, lease *Lease) {
			started <- name
			<-release
		})
	}
	spawn("qa", "first turn")
	if got := <-started; got != "first turn" {
		t.Fatalf("started %q, want the first turn", got)
	}
	spawn("qa", "second turn")
	spawn("devops", "other agent")
	if got := <-started; got != "other agent" {
		t.Fatalf("started %q while the first turn runs, want only the other agent", got)
	}
	select {
	case got := <-started:
		t.Fatalf("%q started while the same owner's first turn was still running", got)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if got := <-started; got != "second turn" {
		t.Fatalf("started %q, want the second turn after the first finished", got)
	}
	m.WaitAgents()
}

func TestIsActive_StageSemanticsUnchanged(t *testing.T) {
	m := New(bus.NewCriticalBus(16), nil, "", 0, nil)
	done := make(chan struct{})
	release := make(chan struct{})
	m.SpawnAgent(context.Background(), flow.Stage{ID: "a"}, func(ctx context.Context, s flow.Stage) {
		if !m.IsActive("a") {
			t.Error("IsActive(stage) should be true while the stage's agent runs")
		}
		close(done)
		<-release
	})
	<-done
	// A helper task on an unrelated owner key must never make IsActive report true for a stage id that
	// merely shares the same string.
	m.SpawnOwned(context.Background(), TaskOwnerKey("a"), "", nil, func(ctx context.Context, lease *Lease) {})
	if !m.IsActive("a") {
		t.Fatal("IsActive(\"a\") must still be true — a task owner key sharing the string must not clobber it")
	}
	close(release)
	m.WaitAgents()
	if m.IsActive("a") {
		t.Fatal("IsActive(\"a\") should be false once the stage agent finished")
	}
}
