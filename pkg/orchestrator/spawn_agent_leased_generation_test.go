package orchestrator

import (
	"context"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
)

func TestSpawnAgentLeased_OverlappingGenerationsPreservesNewerLease(t *testing.T) {
	semA := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	mgrA := concurrency.NewWithSemaphores(bus.NewCriticalBus(1), map[string]concurrency.Semaphore{"claude": semA}, "claude")
	semB := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	mgrB := concurrency.NewWithSemaphores(bus.NewCriticalBus(1), map[string]concurrency.Semaphore{"claude": semB}, "claude")

	o := &Orchestrator{concurrency: mgrA}
	s := flow.Stage{ID: "s1", Command: "claude"}

	// Generation A: старый раннер, ещё не размотавший свой defer — имитирует verify/RunVerification, всё ещё
	// выполняющиеся в момент, когда Revise() уже решил перезапустить стадию.
	aStarted := make(chan struct{})
	releaseA := make(chan struct{})
	o.spawnAgentLeased(context.Background(), s, func(context.Context, flow.Stage) {
		close(aStarted)
		awaitRelease(t, releaseA)
	})
	<-aStarted

	leaseA, ok := o.stageLeases.Load(s.ID)
	if !ok {
		t.Fatal("generation A must register its lease")
	}

	// Generation B: раннер-замена, спавнится, пока A всё ещё жив (реальный
	// сценарий retry.go — новый раннер стартует до возврата старого callback).
	o.concurrency = mgrB
	bStarted := make(chan struct{})
	releaseB := make(chan struct{})
	o.spawnAgentLeased(context.Background(), s, func(context.Context, flow.Stage) {
		close(bStarted)
		awaitRelease(t, releaseB)
	})
	<-bStarted

	leaseB, ok := o.stageLeases.Load(s.ID)
	if !ok || leaseB == leaseA {
		t.Fatal("generation B must overwrite the registered lease with its own")
	}

	// Дать generation A вернуться и полностью размотать свой defer.
	close(releaseA)
	mgrA.WaitAgents()

	cur, ok := o.stageLeases.Load(s.ID)
	if !ok || cur != leaseB {
		t.Fatalf("generation A's cleanup erased generation B's still-live lease: got %v (ok=%v), want %v", cur, ok, leaseB)
	}

	close(releaseB)
	mgrB.WaitAgents()
}
