package concurrency

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

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
