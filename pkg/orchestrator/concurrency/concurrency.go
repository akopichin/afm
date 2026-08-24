package concurrency

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
)

// Semaphore — интерфейс командного семафора. Неэкспортированные методы:
// реализуется только типами этого пакета (noopSemaphore, ChannelSemaphore).
type Semaphore interface {
	acquire()
	release()
}

// noopSemaphore — семафор-заглушка для MaxParallel=0 (без ограничения).
type noopSemaphore struct{}

func (noopSemaphore) acquire() {}
func (noopSemaphore) release() {}

// ChannelSemaphore — реальный семафор на буферизованном канале. Экспортирован,
// чтобы тесты ядра (pkg/orchestrator) могли собрать блокирующий семафор для
// точного контроля таймингов через NewWithSemaphores (см.
// TestRevise_DurableTransition в approve_test.go).
type ChannelSemaphore chan struct{}

func (s ChannelSemaphore) acquire() { s <- struct{}{} }
func (s ChannelSemaphore) release() { <-s }

// agentDrainTimeout — сколько ждём завершения агентских горутин на выходе
// Run, прежде чем вернуться (агентские процессы уже убиты отменой ctx;
// ожидание защищает Store от использования после Close).
const agentDrainTimeout = 10 * time.Second

// Manager инкапсулирует конкурентность агентских горутин: семафоры на
// команду, учёт активных стадий, WaitGroup для чистого shutdown.
type Manager struct {
	critical     *bus.CriticalBus
	sems         map[string]Semaphore
	defaultCmd   string
	activeAgents sync.Map
	agentWG      sync.WaitGroup

	// shouldRun — единая точка входа для пред-запускной проверки стадии.
	// Проверяется в SpawnAgent ПОСЛЕ того, как горутина прошла семафор
	// (закрывает окно, где стадию поставили на паузу, пока она стояла в
	// очереди на слот) — это заменяет ранее разрозненные проверки в
	// withBeforeHook и runWithRetry. nil означает "всегда запускать"
	// (используется тестами, которым эта проверка не нужна).
	shouldRun func(stageID string) bool
}

// New строит Manager с семафорами на команду из конфигурации стадий:
// per-stage MaxParallel имеет приоритет над globalMaxParallel; MaxParallel<=0
// означает отсутствие ограничения (noopSemaphore). shouldRun — см. поле
// Manager.shouldRun; nil допустим.
func New(critical *bus.CriticalBus, stages []flow.Stage, defaultCommand string, globalMaxParallel int, shouldRun func(stageID string) bool) *Manager {
	limits := make(map[string]int)
	cmds := make(map[string]bool)
	for _, s := range stages {
		cmd := s.Command
		if cmd == "" {
			cmd = defaultCommand
		}
		cmds[cmd] = true
		if s.MaxParallel <= 0 {
			continue
		}
		if cur, ok := limits[cmd]; !ok || s.MaxParallel < cur {
			limits[cmd] = s.MaxParallel
		}
	}
	sems := make(map[string]Semaphore)
	for cmd := range cmds {
		mp, ok := limits[cmd]
		if !ok {
			mp = globalMaxParallel
		}
		if mp > 0 {
			sems[cmd] = ChannelSemaphore(make(chan struct{}, mp))
		} else {
			sems[cmd] = noopSemaphore{}
		}
	}
	return &Manager{critical: critical, sems: sems, defaultCmd: defaultCommand, shouldRun: shouldRun}
}

// NewWithSemaphores строит Manager с готовой картой семафоров — используется
// тестами, которым нужен прямой контроль над блокировкой (см. ChannelSemaphore).
func NewWithSemaphores(critical *bus.CriticalBus, sems map[string]Semaphore, defaultCommand string) *Manager {
	return &Manager{critical: critical, sems: sems, defaultCmd: defaultCommand}
}

// markActive/markDone/semFor остаются приватными методами — вызываются только
// изнутри SpawnAgent.

func (m *Manager) markActive(stageID string) { m.activeAgents.Store(stageID, struct{}{}) }
func (m *Manager) markDone(stageID string)   { m.activeAgents.Delete(stageID) }

// IsActive сообщает, выполняется ли сейчас агентская горутина для стадии.
func (m *Manager) IsActive(stageID string) bool {
	_, ok := m.activeAgents.Load(stageID)
	return ok
}

func (m *Manager) semFor(s flow.Stage) Semaphore {
	cmd := s.Command
	if cmd == "" {
		cmd = m.defaultCmd
	}
	if sem, ok := m.sems[cmd]; ok {
		return sem
	}
	return noopSemaphore{}
}

// SpawnAgent запускает агентскую горутину под семафором команды, помечает
// стадию активной и учитывает горутину в WaitGroup. Единственная точка
// запуска — заменяет ~10 копий одинакового boilerplate и гарантирует чистый
// shutdown.
func (m *Manager) SpawnAgent(ctx context.Context, s flow.Stage, run func(context.Context, flow.Stage)) {
	m.agentWG.Add(1)
	go func() {
		defer m.agentWG.Done()
		sem := m.semFor(s)
		sem.acquire()
		m.markActive(s.ID)
		defer func() {
			m.markDone(s.ID)
			sem.release()
		}()
		// Re-check right before run: the goroutine may have queued on sem for
		// an arbitrary amount of time, during which the stage could have been
		// paused. Without this, a queued call fires run() for a stage that's
		// no longer supposed to be running.
		if m.shouldRun != nil && !m.shouldRun(s.ID) {
			return
		}
		run(ctx, s)
	}()
}

// SpawnDetached запускает вспомогательную агентскую горутину, которая
// учитывается в WaitGroup чистого shutdown (Run не вернётся, пока она не
// завершится), но НЕ берёт командный семафор и НЕ помечает стадию активной.
// Нужна для агентов-помощников (напр. агент починки битого question.json,
// см. orchestrator.runJSONFixAgent), которые обязаны работать ПАРАЛЛЕЛЬНО с
// основным агентом стадии, уже держащим её слот в семафоре: провести их
// через SpawnAgent значило бы (1) заклинить на полном семафоре (основной
// агент заблокирован в ожидании ответа, который может дать только помощник)
// и (2) затереть active-маркер стадии, когда помощник завершится раньше
// основного агента.
func (m *Manager) SpawnDetached(ctx context.Context, run func(context.Context)) {
	m.agentWG.Add(1)
	go func() {
		defer m.agentWG.Done()
		run(ctx)
	}()
}

// WakeEventLoop будит select Run()'а неблокирующей отправкой внутреннего
// маркер-события через bus.CriticalBus.WakeEventLoop — используется
// maybeRunAfterHook после того, как after-hook горутина реально завершилась,
// т.к. script_after никогда не публикует EventAgentCompleted сама (не трогает
// FSM), так что без явного толчка Run() мог бы простаивать в select.
func (m *Manager) WakeEventLoop() {
	m.critical.WakeEventLoop()
}

// WaitAgents дожидается завершения всех агентских горутин (с ограничением),
// чтобы Run не вернулся, пока горутины ещё пишут в Store.
func (m *Manager) WaitAgents() {
	done := make(chan struct{})
	go func() {
		m.agentWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(agentDrainTimeout):
		log.Printf("WARN: agent drain timed out after %v", agentDrainTimeout)
	}
}
