package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
)

// TestSpawnKind_StoresLeaseDuringRunAndDeletesAfter — V4b.1: spawnKind
// (единственная точка запуска исполнительских и планировочных раннеров)
// обязана вручать каждому запуску *concurrency.Lease через o.stageLeases на
// время работы run(ctx,s) и убирать запись сразу после — иначе
// RunVerification (AI-verify) не сможет найти lease своей стадии, чтобы
// временно перевести командный слот на верификатора (см. Orchestrator.
// stageLeases).
func TestSpawnKind_StoresLeaseDuringRunAndDeletesAfter(t *testing.T) {
	cb := bus.NewCriticalBus(16)
	o := &Orchestrator{
		ui:          bus.NewUIBus(),
		critical:    cb,
		concurrency: concurrency.NewWithSemaphores(cb, map[string]concurrency.Semaphore{}, ""),
	}

	release := make(chan struct{})
	leaseSeen := make(chan *concurrency.Lease, 1)
	o.spawnKind(context.Background(), flow.Stage{ID: "s1"}, kindImplementation, func(ctx context.Context, s flow.Stage) {
		v, ok := o.stageLeases.Load(s.ID)
		if ok {
			leaseSeen <- v.(*concurrency.Lease)
		} else {
			leaseSeen <- nil
		}
		<-release
	})

	select {
	case l := <-leaseSeen:
		if l == nil {
			t.Fatal("lease must be retrievable from o.stageLeases while run(ctx,s) is executing")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run(ctx,s) was never invoked")
	}

	close(release)
	o.concurrency.WaitAgents()

	if _, ok := o.stageLeases.Load("s1"); ok {
		t.Error("lease must be deleted once run(ctx,s) returns")
	}
}
