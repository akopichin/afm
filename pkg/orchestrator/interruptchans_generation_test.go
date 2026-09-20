package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
)

// TestRunWithRetry_InterruptChans_OverlappingGenerationsPreservesNewerChannel —
// G3 (5-е код-ревью): та же гонка перекрывающихся поколений, что E3 закрыл
// для o.stageLeases (см. spawn_agent_leased_generation_test.go), но для
// o.interruptChans. runWithRetry раньше делал безусловный
// o.interruptChans.Delete(s.ID) в defer — на Revise-прерывании
// раннер-замена (generation B) может Store'ить свой канал под тем же
// stageID ДО того, как ЕЩЁ ЖИВОЙ старый вызов (generation A) размотает свой
// defer (тот же реальный сценарий, что и у stageLeases: новое поколение
// спавнится раньше, чем возвращается прерванный agentFn старого). Безусловный
// Delete стирал канал B, а не свой собственный — runnerForVerify для
// generation B тогда не подключал бы InterruptCh вовсе
// (~runner_factory.go:162), и Pause()/Revise() не могли бы остановить этот
// верификатор.
//
// Обе "генерации" здесь — реальные конкурентные вызовы runWithRetry для
// ОДНОГО и того же stageID (не два примитива пониже уровнем, как у
// spawnAgentLeased — сам Store/Delete интерlючён внутрь runWithRetry, а не
// вынесен в отдельный переиспользуемый хелпер), каждый со своим agentFn,
// блокирующимся на канале до явного релиза — детерминированная синхронизация
// без единого sleep/poll, тот же приём, что и в E3-тесте.
func TestRunWithRetry_InterruptChans_OverlappingGenerationsPreservesNewerChannel(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.maxRetries = 15
	s := flow.Stage{ID: "s1"}

	// Generation A: старый вызов, ещё не размотавший свой defer — блокируется
	// в agentFn, пока его явно не отпустят, затем возвращает нересурсируемую
	// (fatal, non-retryable) ошибку — самый короткий путь runWithRetry к
	// возврату (немедленный EvFail, без backoff/retry).
	aStarted := make(chan struct{})
	releaseA := make(chan struct{})
	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		o.runWithRetry(context.Background(), s, phaseImplementation,
			func(string) error {
				close(aStarted)
				<-releaseA
				return errors.New("boom")
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

	// Generation B: раннер-замена, спавнится, пока A всё ещё жив (реальный
	// сценарий retry.go — новый вызов runWithRetry стартует ДО того, как
	// вернётся прерванный вызов A).
	bStarted := make(chan struct{})
	releaseB := make(chan struct{})
	doneB := make(chan struct{})
	go func() {
		defer close(doneB)
		o.runWithRetry(context.Background(), s, phaseImplementation,
			func(string) error {
				close(bStarted)
				<-releaseB
				return errors.New("boom")
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
