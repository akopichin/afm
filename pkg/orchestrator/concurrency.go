package orchestrator

import (
	"context"
	"log"
	"time"

	"github.com/akopichin/afm/pkg/flow"
)

// semNop is a no-op semaphore used when MaxParallel is 0 (unlimited).
type semNop struct{}

func (semNop) acquire() {}
func (semNop) release() {}

// semChan is a real semaphore backed by a buffered channel.
type semChan chan struct{}

func (s semChan) acquire() { s <- struct{}{} }
func (s semChan) release() { <-s }

// agentDrainTimeout — сколько ждём завершения агентских горутин на выходе Run,
// прежде чем вернуться (агентские процессы уже убиты отменой ctx; ожидание
// защищает Store от использования после Close).
const agentDrainTimeout = 10 * time.Second

// markAgentActive records that an agent goroutine is running for a stage.
// Called after sem.acquire() so it reflects actively-running agents only.
// Store is idempotent, so double-marking (e.g. goroutine + nested call) is safe.
func (o *Orchestrator) markAgentActive(stageID string) { o.activeAgents.Store(stageID, struct{}{}) }

// markAgentDone clears the active-agent marker for a stage. Called via defer
// before sem.release().
func (o *Orchestrator) markAgentDone(stageID string) { o.activeAgents.Delete(stageID) }

// isAgentActive reports whether an agent goroutine is currently running for a stage.
func (o *Orchestrator) isAgentActive(stageID string) bool {
	_, ok := o.activeAgents.Load(stageID)
	return ok
}

// spawnAgent запускает агентскую горутину под семафором команды, помечает стадию
// активной и учитывает горутину в WaitGroup. Единственная точка запуска —
// заменяет ~10 копий одинакового boilerplate и гарантирует чистый shutdown.
//
// Намеренно НЕ трогает pendingAfterHooks — эта бухгалтерия нужна только
// script_after (см. maybeRunAfterHook в hooks.go, которая сама оборачивает
// run перед вызовом spawnAgent): все остальные вызовы spawnAgent уже двигают
// FSM-статус своей стадии, на который и так смотрит allTerminal()/shouldExit();
// добавлять сюда счётчик "для всех" расширило бы область изменения на каждый
// тип агента без всякой пользы для них.
func (o *Orchestrator) spawnAgent(ctx context.Context, s flow.Stage, run func(context.Context, flow.Stage)) {
	o.agentWG.Add(1)
	go func() {
		defer o.agentWG.Done()
		sem := o.semFor(s)
		sem.acquire()
		o.markAgentActive(s.ID)
		defer func() {
			o.markAgentDone(s.ID)
			sem.release()
		}()
		run(ctx, s)
	}()
}

// wakeEventLoop будит select Run()'а неблокирующей отправкой во внутреннее
// событие — используется maybeRunAfterHook после того, как after-hook
// горутина реально завершилась (pendingAfterHooks уже декрементирован), т.к.
// script_after никогда не публикует EventAgentCompleted сама (не трогает
// FSM), так что без явного толчка Run() мог бы простаивать в select,
// не перепроверяя shouldExit(). Best-effort: если буфер critical-шины полон,
// там уже стоят другие события — их обработка и так вызовет свою
// перепроверку shouldExit(), так что потеря этого толчка безвредна.
func (o *Orchestrator) wakeEventLoop() {
	select {
	case o.critical.ch <- Event{Type: eventAgentDrained}:
	default:
	}
}

// waitAgents дожидается завершения всех агентских горутин (с ограничением),
// чтобы Run не вернулся, пока горутины ещё пишут в Store.
func (o *Orchestrator) waitAgents() {
	done := make(chan struct{})
	go func() {
		o.agentWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(agentDrainTimeout):
		log.Printf("WARN: agent drain timed out after %v", agentDrainTimeout)
	}
}

// semFor returns the semaphore for a stage's effective command.
func (o *Orchestrator) semFor(s flow.Stage) interface {
	acquire()
	release()
} {
	cmd := s.Command
	if cmd == "" {
		cmd = o.opts.Config.Client.Command
	}
	if sem, ok := o.sems[cmd]; ok {
		return sem
	}
	return semNop{}
}
