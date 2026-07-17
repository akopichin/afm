package orchestrator

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
)

// spawnAgent отслеживает горутину в WaitGroup: waitAgents дожидается её завершения.
func TestSpawnAgent_WaitAgentsBlocksUntilDone(t *testing.T) {
	o := &Orchestrator{ui: NewUIBus(), critical: NewCriticalBus(16), sems: map[string]interface {
		acquire()
		release()
	}{}}

	var finished atomic.Bool
	release := make(chan struct{})
	o.spawnAgent(context.Background(), flow.Stage{ID: "a"}, func(ctx context.Context, s flow.Stage) {
		<-release
		finished.Store(true)
	})

	done := make(chan struct{})
	go func() { o.waitAgents(); close(done) }()

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
	o := &Orchestrator{ui: NewUIBus(), critical: NewCriticalBus(16), sems: map[string]interface {
		acquire()
		release()
	}{}}

	started := make(chan struct{})
	var done atomic.Bool
	o.spawnAgent(context.Background(), flow.Stage{ID: "a"}, func(ctx context.Context, s flow.Stage) {
		close(started)
		time.Sleep(20 * time.Millisecond)
		done.Store(true)
	})
	<-started
	o.waitAgents()
	if !done.Load() {
		t.Fatal("waitAgents returned before agent completed")
	}
	_ = dir
}
