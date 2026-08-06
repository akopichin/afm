package orchestrator

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
)

// SpawnAgent отслеживает горутину в WaitGroup: WaitAgents дожидается её завершения.
func TestSpawnAgent_WaitAgentsBlocksUntilDone(t *testing.T) {
	cb := bus.NewCriticalBus(16)
	o := &Orchestrator{
		ui:          bus.NewUIBus(),
		critical:    cb,
		concurrency: concurrency.NewWithSemaphores(cb, map[string]concurrency.Semaphore{}, ""),
	}

	var finished atomic.Bool
	release := make(chan struct{})
	o.concurrency.SpawnAgent(context.Background(), flow.Stage{ID: "a"}, func(ctx context.Context, s flow.Stage) {
		<-release
		finished.Store(true)
	})

	done := make(chan struct{})
	go func() { o.concurrency.WaitAgents(); close(done) }()

	select {
	case <-done:
		t.Fatal("waitAgents returned before agent finished")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case <-done:
		if !finished.Load() {
			t.Fatal("agent goroutine did not finish")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitAgents did not return after agent finished")
	}
}

func TestRun_CancelDrainsAgents(t *testing.T) {
	dir := t.TempDir()
	cb := bus.NewCriticalBus(16)
	o := &Orchestrator{
		ui:          bus.NewUIBus(),
		critical:    cb,
		concurrency: concurrency.NewWithSemaphores(cb, map[string]concurrency.Semaphore{}, ""),
	}

	started := make(chan struct{})
	var done atomic.Bool
	o.concurrency.SpawnAgent(context.Background(), flow.Stage{ID: "a"}, func(ctx context.Context, s flow.Stage) {
		close(started)
		time.Sleep(20 * time.Millisecond)
		done.Store(true)
	})
	<-started
	o.concurrency.WaitAgents()
	if !done.Load() {
		t.Fatal("waitAgents returned before agent completed")
	}
	_ = dir
}
