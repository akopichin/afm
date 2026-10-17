package orchestrator

import (
	"context"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
)

func TestRunWithRetry_InterruptChans_OverlappingGenerationsPreservesNewerChannel(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.maxRetries = 15
	s := flow.Stage{ID: "s1"}

	// Generation A держит старый вызов до явного release, затем возвращает fatal-ошибку для выхода без retry.
	aStarted := make(chan struct{})
	releaseA := make(chan struct{})
	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		o.runWithRetry(context.Background(), s, phaseImplementation,
			func(string) error {
				close(aStarted)
				<-releaseA
				return context.Canceled
			},
			nil,
			func() { t.Error("onUserInterrupted must not be called for generation A") },
		)
	}()
	<-aStarted

	chAVal, ok := o.interruptChans.Load("s1")
	if !ok {
		t.Fatal("generation A must register its interrupt channel")
	}
	chA := chAVal.(chan struct{})

	// Generation B: раннер-замена, спавнится, пока A всё ещё жив (реальный сценарий retry.go — новый вызов
	// runWithRetry стартует ДО того, как вернётся прерванный вызов A).
	bStarted := make(chan struct{})
	releaseB := make(chan struct{})
	doneB := make(chan struct{})
	go func() {
		defer close(doneB)
		o.runWithRetry(context.Background(), s, phaseImplementation,
			func(string) error {
				close(bStarted)
				<-releaseB
				return context.Canceled
			},
			nil,
			func() { t.Error("onUserInterrupted must not be called for generation B") },
		)
	}()
	<-bStarted

	chBVal, ok := o.interruptChans.Load("s1")
	if !ok {
		t.Fatal("generation B must register its interrupt channel")
	}
	chB := chBVal.(chan struct{})
	if chB == chA {
		t.Fatal("generation B must overwrite the registered channel with its own, got the same channel as A")
	}

	// Дать generation A вернуться и полностью размотать свой defer.
	close(releaseA)
	<-doneA

	cur, ok := o.interruptChans.Load("s1")
	if !ok || cur.(chan struct{}) != chB {
		t.Fatalf("generation A's cleanup erased generation B's still-live interrupt channel: got %v (ok=%v), want %v", cur, ok, chB)
	}

	close(releaseB)
	<-doneB
}
