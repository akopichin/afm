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

// TestSpawnDetached_TracksWaitGroupWithoutSemaphoreOrActiveMarker proves the
// two properties the malformed-JSON fix agent depends on: a detached helper
// runs even when the ONLY command semaphore slot is already taken (it must not
// deadlock behind the blocked main agent), and it never marks the stage active
// (so it can't clobber the main agent's active marker) — yet it is still
// awaited by WaitAgents for clean shutdown.
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

// TestSpawnAgent_SkipsRunWhenPausedWhileQueuedBehindSemaphore is a regression
// test for the exact race this centralization closes: a goroutine queues
// behind a full command semaphore (e.g. a resumed stage landing in a
// max_parallel bucket that's already full), the stage gets paused while
// still queued, then the semaphore frees up — the queued call must not fire
// run() for a stage that is no longer supposed to be running.
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
