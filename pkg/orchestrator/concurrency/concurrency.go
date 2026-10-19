package concurrency

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/akopichin/afm/pkg/clock"
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
	// owners maps an owner key to a channel closed once that owner's goroutine has completely finished — its
	// run returned and its lease is released. SpawnOwned for a key that is still running waits for it.
	owners sync.Map // OwnerKey -> chan struct{}

	// shouldRun — единая точка входа для пред-запускной проверки стадии. nil означает "всегда запускать"
	// (используется тестами, которым эта проверка не нужна).
	shouldRun func(stageID string) bool

	// dynMu/dynSems guard a SMALL, lazily-grown set of semaphores for commands NOT known at construction time
	// (New/NewWithSemaphores build m.sems once from stage.Command/VerifyAgentCommands and never mutate it
	// afterward — that map stays lock-free to read). A helper task's profile command (T3.3/T3.4) may name a
	// command no stage ever declared; semForCmd must still cap its concurrency rather than silently falling
	// back to an unlimited noopSemaphore (§7.2 — "an unknown command must not get an unbounded run").
	dynMu   sync.Mutex
	dynSems map[string]Semaphore
	// dynCap — how many agents may run at once on one such command: the run's global max_parallel, or
	// defaultDynamicCap when the run sets no global limit. Several run agents of one role (two reviewers, QA
	// and DevOps on one recipe) must work side by side, not strictly one after another.
	dynCap int

	// Clock — часы WaitAgents' drain таймаута. nil (не задан конструктором — New/NewWithSemaphores его не
	// принимают, чтобы не трогать ~40 call sites) означает clock.Real(). orchestrator.New НАМЕРЕННО не
	// прокидывает сюда opts.Clock: тестовый fake-clock-pump (ускоряющий hookRetryBackoff) racе'ил бы с
	// этим drain-таймаутом и срывал бы happens-before агента (см. AGENTS.md's "pkg/clock"). Тесты, которым
	// нужен управляемый drain-таймаут, ставят Clock напрямую на отдельно сконструированном Manager.
	Clock clock.Clock
}

// clk возвращает часы менеджера; nil означает clock.Real().
func (m *Manager) clk() clock.Clock {
	if m.Clock == nil {
		return clock.Real()
	}
	return m.Clock
}

// OwnerKey is the activeAgents key for one concurrently-running agent goroutine. Two different owners must
// never collide in the shared sync.Map: "stage:<id>" for a stage's own runner (stageOwnerKey — the pre-T3.3
// behavior, preserved byte-for-byte via markActive/markDone/IsActive), "task:<id>" for a helper task
// (taskOwnerKey, T3.3/T3.4) — so two helper tasks on the SAME root stage, or a helper and its root stage
// itself, each get their own independent activity marker instead of clobbering one shared stageID key.
type OwnerKey string

func stageOwnerKey(stageID string) OwnerKey { return OwnerKey("stage:" + stageID) }

// TaskOwnerKey returns the owner key for a helper task — exported so orchestrator-level callers (T3.4) can
// pass it to SpawnOwned without this package needing to know anything about assistants.TaskID.
func TaskOwnerKey(taskID string) OwnerKey { return OwnerKey("task:" + taskID) }

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
	dynCap := globalMaxParallel
	if dynCap <= 0 {
		dynCap = defaultDynamicCap
	}
	return &Manager{critical: critical, sems: sems, defaultCmd: defaultCommand, shouldRun: shouldRun, dynCap: dynCap}
}

// defaultDynamicCap — the limit for a command no stage declared when the run sets no global max_parallel:
// bounded (§7.2), but wide enough for a small team of agents sharing one recipe.
const defaultDynamicCap = 4

// NewWithSemaphores строит Manager с готовой картой семафоров — используется
// тестами, которым нужен прямой контроль над блокировкой (см. ChannelSemaphore).
func NewWithSemaphores(critical *bus.CriticalBus, sems map[string]Semaphore, defaultCommand string) *Manager {
	return &Manager{critical: critical, sems: sems, defaultCmd: defaultCommand}
}

// markActiveKey/markDoneKey are called only from SpawnOwned (SpawnAgentLease/SpawnAgent go through it too —
// see stageOwnerKey's doc comment for the "stage:"+id key shape that preserves pre-T3.3 IsActive semantics).
func (m *Manager) markActiveKey(k OwnerKey) { m.activeAgents.Store(k, struct{}{}) }
func (m *Manager) markDoneKey(k OwnerKey)   { m.activeAgents.Delete(k) }

// IsActive сообщает, выполняется ли сейчас агентская горутина для стадии. Смотрит ТОЛЬКО ключи "stage:" —
// задачи помощников (taskOwnerKey) никогда не отвечают на этот вопрос, даже если их root-стадия совпадает.
func (m *Manager) IsActive(stageID string) bool {
	_, ok := m.activeAgents.Load(stageOwnerKey(stageID))
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

// semForCmd выбирает семафор команды для Lease. Известная (зарегистрированная в New/NewWithSemaphores)
// команда — её собственный семафор (возможно noopSemaphore, если у неё явно нет лимита). Команда, которую
// Manager никогда не видел (ни одна стадия её не объявляла) — НЕ unlimited noop: лениво заводим для неё
// семафор ёмкостью dynCap (глобальный max_parallel или defaultDynamicCap; у NewWithSemaphores — 1),
// кешируем в dynSems под dynMu, и дальше отдаём тот же
// инстанс каждому вызову с тем же именем команды — иначе два конкурентных помощника с одним и тем же
// неизвестным profile-командой получили бы КАЖДЫЙ свой независимый лимит=1 вместо общего.
func (m *Manager) semForCmd(cmd string) Semaphore {
	resolved := m.resolveCmd(cmd)
	if sem, ok := m.sems[resolved]; ok {
		return sem
	}
	return m.dynamicSemForCmd(resolved)
}

func (m *Manager) dynamicSemForCmd(cmd string) Semaphore {
	m.dynMu.Lock()
	defer m.dynMu.Unlock()
	if sem, ok := m.dynSems[cmd]; ok {
		return sem
	}
	if m.dynSems == nil {
		m.dynSems = map[string]Semaphore{}
	}
	sem := ChannelSemaphore(make(chan struct{}, max(m.dynCap, 1)))
	m.dynSems[cmd] = sem
	return sem
}

// SpawnOwned runs one agent goroutine under cmd's command-slot semaphore, marking owner active for the
// duration — the general primitive behind both SpawnAgentLease (stage owner) and T3.4's helper-task spawn
// (task owner): takes the semaphore, marks owner active, participates in agentWG/drain, and re-checks
// shouldRun IMMEDIATELY AFTER acquiring the semaphore (the goroutine may have queued for an arbitrary amount
// of time, during which the owner could have been paused/cancelled) — same ordering as SpawnAgentLease used
// to inline before this extraction. shouldRun==nil means "always run".
//
// One owner key runs one goroutine at a time: a spawn for an owner whose previous goroutine is still running
// (a run agent's next turn scheduled from its previous turn's tail, or by a message arriving just then) starts
// only after that goroutine has finished, deferred cleanup included — so the old turn can never release
// state, a lease or an activity marker that already belongs to the new one.
func (m *Manager) SpawnOwned(ctx context.Context, owner OwnerKey, cmd string, shouldRun func() bool, run func(context.Context, *Lease)) {
	m.agentWG.Add(1)
	go func() {
		defer m.agentWG.Done()
		done, ok := m.claimOwner(ctx, owner)
		if !ok {
			return
		}
		defer m.releaseOwner(owner, done)
		lease, err := m.AcquireLease(ctx, cmd)
		if err != nil {
			return
		}
		m.markActiveKey(owner)
		defer func() {
			m.markDoneKey(owner)
			lease.Release()
		}()
		if shouldRun != nil && !shouldRun() {
			return
		}
		run(ctx, lease)
	}()
}

// claimOwner makes the calling goroutine owner's only running goroutine, waiting for a previous one to
// finish. It fails only when ctx is cancelled while waiting.
func (m *Manager) claimOwner(ctx context.Context, owner OwnerKey) (chan struct{}, bool) {
	done := make(chan struct{})
	for {
		prev, running := m.owners.LoadOrStore(owner, done)
		if !running {
			return done, true
		}
		select {
		case <-prev.(chan struct{}):
		case <-ctx.Done():
			return nil, false
		}
	}
}

// releaseOwner ends the claim taken by claimOwner and wakes a spawn waiting for owner.
func (m *Manager) releaseOwner(owner OwnerKey, done chan struct{}) {
	m.owners.CompareAndDelete(owner, done)
	close(done)
}

// SpawnAgentLease runs an agent with one transferable command-slot lease. Cancellation while waiting
// prevents startup. Thin wrapper over SpawnOwned with the stage owner key — kept as its own method since
// callers pass/receive flow.Stage, not a bare OwnerKey/command.
func (m *Manager) SpawnAgentLease(ctx context.Context, s flow.Stage, run func(ctx context.Context, s flow.Stage, lease *Lease)) {
	shouldRun := func() bool { return m.shouldRun == nil || m.shouldRun(s.ID) }
	m.SpawnOwned(ctx, stageOwnerKey(s.ID), s.Command, shouldRun, func(ctx context.Context, lease *Lease) {
		run(ctx, s, lease)
	})
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
	case <-m.clk().After(agentDrainTimeout):
		log.Printf("WARN: agent drain timed out after %v", agentDrainTimeout)
	}
}
