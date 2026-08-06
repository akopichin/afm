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
	m := New(bus.NewCriticalBus(16), nil, "", 0)
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

func TestWakeEventLoop_PublishesToBus(t *testing.T) {
	cb := bus.NewCriticalBus(1)
	m := New(cb, nil, "", 0)
	m.WakeEventLoop()
	select {
	case <-cb.Recv():
	default:
		t.Fatal("WakeEventLoop should publish to critical bus")
	}
}
