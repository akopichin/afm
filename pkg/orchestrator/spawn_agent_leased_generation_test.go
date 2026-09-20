package orchestrator

import (
	"context"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
)

// TestSpawnAgentLeased_OverlappingGenerationsPreservesNewerLease — E3 (третье
// код-ревью): на Revise-прерывании замена-раннер (generation B) спавнится
// ДО того, как ЕЩЁ ЖИВОЙ старый callback (generation A) успевает размотать
// свой defer (см. retry.go/control_api.go). spawnAgentLeased раньше делал
// безусловный o.stageLeases.Delete(s.ID) — если A's defer срабатывает ПОСЛЕ
// того, как B уже сохранил свой lease под тем же stageID, он стирает lease B,
// а не свой собственный, и RunVerification генерации B молча деградирует
// (o.stageLeases.Load возвращает ok=false — верификатор перестаёт учитываться
// в командном семафоре).
//
// Generation A и B спавнятся на РАЗНЫХ *concurrency.Manager (общий
// o.stageLeases живёт на Orchestrator, не на Manager) — это позволяет
// детерминированно дождаться ПОЛНОГО разматывания defer'ов ИМЕННО generation
// A через mgrA.WaitAgents(), не трогая ещё не завершившуюся generation B
// (зарегистрированную в agentWG другого, mgrB, экземпляра) — без единого
// sleep/poll.
func TestSpawnAgentLeased_OverlappingGenerationsPreservesNewerLease(t *testing.T) {
	semA := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	mgrA := concurrency.NewWithSemaphores(bus.NewCriticalBus(1), map[string]concurrency.Semaphore{"claude": semA}, "claude")
	semB := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	mgrB := concurrency.NewWithSemaphores(bus.NewCriticalBus(1), map[string]concurrency.Semaphore{"claude": semB}, "claude")

	o := &Orchestrator{concurrency: mgrA}
	s := flow.Stage{ID: "s1", Command: "claude"}

	// Generation A: старый раннер, ещё не размотавший свой defer — имитирует
	// verify/RunVerification, всё ещё выполняющиеся в момент, когда Revise()
	// уже решил перезапустить стадию.
	aStarted := make(chan struct{})
	releaseA := make(chan struct{})
	o.spawnAgentLeased(context.Background(), s, func(context.Context, flow.Stage) {
		close(aStarted)
		<-releaseA
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
		<-releaseB
	})
	<-bStarted

	leaseB, ok := o.stageLeases.Load(s.ID)
	if !ok || leaseB == leaseA {
		t.Fatal("generation B must overwrite the registered lease with its own")
	}

	// Дать generation A вернуться и полностью размотать свой defer.
	// mgrA.WaitAgents() ждёт ровно её agentWG (B зарегистрирован в agentWG
	// mgrB и не блокирует этот вызов) — детерминированно, без sleep.
	close(releaseA)
	mgrA.WaitAgents()

	cur, ok := o.stageLeases.Load(s.ID)
	if !ok || cur != leaseB {
		t.Fatalf("generation A's cleanup erased generation B's still-live lease: got %v (ok=%v), want %v", cur, ok, leaseB)
	}

	close(releaseB)
	mgrB.WaitAgents()
}
