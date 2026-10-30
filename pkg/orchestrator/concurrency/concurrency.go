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
	// acquireCtx — отменяемый захват слота: возвращает ctx.Err, если ctx отменился до того, как слот
	// освободился, БЕЗ захвата слота и без утечки горутины.
	acquireCtx(ctx context.Context) error
	release()
}

// noopSemaphore — семафор-заглушка для MaxParallel=0 (без ограничения).
type noopSemaphore struct{}

func (noopSemaphore) acquire() {}

// Даже без лимита проверяем ctx: отменённый Pause/Revise не должен разрешить запуск verify.
func (noopSemaphore) acquireCtx(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
func (noopSemaphore) release() {}

// ChannelSemaphore — реальный семафор на буферизованном канале.
type ChannelSemaphore chan struct{}

func (s ChannelSemaphore) acquire() { s <- struct{}{} }

// acquireCtx ждёт слот ИЛИ отмену ctx — что наступит раньше.
func (s ChannelSemaphore) acquireCtx(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	select {
	case s <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s ChannelSemaphore) release() { <-s }

// agentDrainTimeout — сколько ждём завершения агентских горутин на выходе Run, прежде чем вернуться.
const agentDrainTimeout = 10 * time.Second

// Manager инкапсулирует конкурентность агентских горутин: семафоры на
// команду, учёт активных стадий, WaitGroup для чистого shutdown.
type Manager struct {
	critical     *bus.CriticalBus
	sems         map[string]Semaphore
	defaultCmd   string
	activeAgents sync.Map
	agentWG      sync.WaitGroup
	// spawned marks stages for which this process started an executor, queued or finished.
	// It is set synchronously at spawn and never cleared.
	spawned sync.Map

	// shouldRun — единая точка входа для пред-запускной проверки стадии. nil означает "всегда запускать"
	// (используется тестами, которым эта проверка не нужна).
	shouldRun func(stageID string) bool
}

// New строит Manager с семафорами на команду из конфигурации стадий: per-stage MaxParallel имеет приоритет
// над globalMaxParallel; MaxParallel<=0 означает отсутствие ограничения.
func New(critical *bus.CriticalBus, stages []flow.Stage, defaultCommand string, globalMaxParallel int, shouldRun func(stageID string) bool) *Manager {
	limits := make(map[string]int)
	cmds := make(map[string]bool)
	for _, s := range stages {
		cmd := s.Command
		if cmd == "" {
			cmd = defaultCommand
		}
		cmds[cmd] = true
		if s.MaxParallel > 0 {
			if cur, ok := limits[cmd]; !ok || s.MaxParallel < cur {
				limits[cmd] = s.MaxParallel
			}
		}
		for _, vcmd := range s.VerifyAgentCommands() {
			cmds[vcmd] = true
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

// markActive/markDone остаются приватными методами — вызываются только
// изнутри SpawnAgentLease.

func (m *Manager) markActive(stageID string) { m.activeAgents.Store(stageID, struct{}{}) }
func (m *Manager) markDone(stageID string)   { m.activeAgents.Delete(stageID) }

// IsActive сообщает, выполняется ли сейчас агентская горутина для стадии.
func (m *Manager) IsActive(stageID string) bool {
	_, ok := m.activeAgents.Load(stageID)
	return ok
}

// Spawned reports whether this process has started an executor for the stage.
func (m *Manager) Spawned(stageID string) bool {
	_, ok := m.spawned.Load(stageID)
	return ok
}

// resolveCmd нормализует имя команды к тому же виду, что использует New при построении m.sems: "" →
// дефолтная команда Manager'а.
func (m *Manager) resolveCmd(cmd string) string {
	if cmd == "" {
		return m.defaultCmd
	}
	return cmd
}

// semForCmd выбирает семафор команды для Lease; без заданного лимита возвращает noopSemaphore.
func (m *Manager) semForCmd(cmd string) Semaphore {
	if sem, ok := m.sems[m.resolveCmd(cmd)]; ok {
		return sem
	}
	return noopSemaphore{}
}

// SpawnAgentLease runs an agent with one transferable command-slot lease. Cancellation while waiting
// prevents startup.
func (m *Manager) SpawnAgentLease(ctx context.Context, s flow.Stage, run func(ctx context.Context, s flow.Stage, lease *Lease)) {
	m.spawned.Store(s.ID, struct{}{})
	m.agentWG.Add(1)
	go func() {
		defer m.agentWG.Done()
		lease, err := m.AcquireLease(ctx, s.Command)
		if err != nil {
			return
		}
		m.markActive(s.ID)
		defer func() {
			m.markDone(s.ID)
			lease.Release()
		}()
		// Re-check right before run: the goroutine may have queued on sem for an arbitrary amount of time,
		// during which the stage could have been paused.
		if m.shouldRun != nil && !m.shouldRun(s.ID) {
			return
		}
		run(ctx, s, lease)
	}()
}

// SpawnAgent запускает агентскую горутину под семафором команды, помечает стадию активной и учитывает
// горутину в WaitGroup.
func (m *Manager) SpawnAgent(ctx context.Context, s flow.Stage, run func(context.Context, flow.Stage)) {
	m.SpawnAgentLease(ctx, s, func(ctx context.Context, s flow.Stage, lease *Lease) {
		run(ctx, s)
	})
}

// SpawnDetached запускает вспомогательную агентскую горутину, которая учитывается в WaitGroup чистого
// shutdown, но НЕ берёт командный семафор и НЕ помечает стадию активной.
func (m *Manager) SpawnDetached(ctx context.Context, run func(context.Context)) {
	m.agentWG.Add(1)
	go func() {
		defer m.agentWG.Done()
		run(ctx)
	}()
}

// WakeEventLoop будит Run после завершения after-hook, который не публикует EventAgentCompleted.
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
