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
	// acquireCtx — отменяемый захват слота: возвращает ctx.Err(), если ctx
	// отменился до того, как слот освободился, БЕЗ захвата слота и без
	// утечки горутины. Нужен Lease (см. ниже) — пауза/отмена стадии должны
	// уметь прервать ожидание слота при переходе автор→верификатор.
	acquireCtx(ctx context.Context) error
	release()
}

// noopSemaphore — семафор-заглушка для MaxParallel=0 (без ограничения).
type noopSemaphore struct{}

func (noopSemaphore) acquire() {}

// acquireCtx не блокируется (лимита нет), но обязан уважать УЖЕ отменённый
// ctx — иначе (C5 код-ревью) Lease.SwapTo на команду без лимита молча
// репортовал бы успех, даже если Pause/Revise отменили ctx до самого вызова:
// вызывающий код (verify.go) считал бы переход на команду верификатора
// состоявшимся и запускал бы его на стадии, которая уже должна была
// остановиться.
func (noopSemaphore) acquireCtx(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
func (noopSemaphore) release() {}

// ChannelSemaphore — реальный семафор на буферизованном канале. Экспортирован,
// чтобы тесты ядра (pkg/orchestrator) могли собрать блокирующий семафор для
// точного контроля таймингов через NewWithSemaphores (см.
// TestRevise_DurableTransition в approve_test.go).
type ChannelSemaphore chan struct{}

func (s ChannelSemaphore) acquire() { s <- struct{}{} }

// acquireCtx ждёт слот ИЛИ отмену ctx — что наступит раньше. При отмене
// горутина не остаётся "приклеенной" к каналу: select с двумя ветками
// завершается сразу, канал не читается и не пишется дальше.
//
// Отдельная проверка ctx.Done() ПЕРЕД основным select обязательна: если ctx
// уже отменён В МОМЕНТ вызова, а в канале в это же время есть свободное
// место, select между двумя готовыми ветками выбрал бы одну псевдослучайно —
// вызов мог бы "успешно" занять слот несмотря на уже отменённый ctx.
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
//
// Команды агентских verify-шагов (Stage.VerifyAgentCommands, AI-verify задача
// V3) включаются в множество команд наравне с Stage.Command — ИНАЧЕ у
// верификатора, не встречающегося ни в одном Stage.Command, вообще не было
// бы предсозданного семафора в m.sems, и Lease.AcquireLease/SwapTo молча
// откатились бы на noopSemaphore (semForCmd) — то есть переход автор→
// верификатор на такую команду не ограничивался бы max_parallel вовсе.
// verify.max_parallel сознательно НЕ добавлен (см. бриф задачи V3) — у
// verify-команды без собственного Stage.MaxParallel в другой стадии лимит
// берётся из globalMaxParallel, как у любой прочей нелимитированной команды.
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

// resolveCmd нормализует имя команды к тому же виду, что использует New при
// построении m.sems: "" → дефолтная команда Manager'а. Общая точка для
// semForCmd и Lease — если бы Lease сравнивал НЕнормализованные имена (""
// против явного имени дефолтной команды), SwapTo не распознал бы переход
// "на самом деле в ту же команду" как no-op и лишний раз дёрнул бы
// release+acquire того же семафора.
func (m *Manager) resolveCmd(cmd string) string {
	if cmd == "" {
		return m.defaultCmd
	}
	return cmd
}

// semForCmd резолвит семафор по имени команды напрямую (без flow.Stage) —
// нужен Lease, у которого на момент SwapTo нет всей стадии, только имя
// команды верификатора. Команда без собственного семафора (в т.ч. созданная
// в обход New, напр. в тестах через NewWithSemaphores без этого ключа) —
// noopSemaphore (без ограничения), а не паника или молчаливый nil-семафор.
func (m *Manager) semForCmd(cmd string) Semaphore {
	if sem, ok := m.sems[m.resolveCmd(cmd)]; ok {
		return sem
	}
	return noopSemaphore{}
}

// SpawnAgentLease запускает агентскую горутину, вручая callback'у *Lease —
// единственный слот командного семафора, который тот может переносить между
// подпроцессами разного имени (SwapTo) в течение своего выполнения, никогда
// не держа два слота одновременно (AI-verify, переход автор→верификатор).
// Изначально lease держит слот Stage.Command. Захват отменяем через ctx: если
// ctx отменяется, пока горутина ждёт своей очереди на семафоре, агент вовсе
// не стартует (run не вызывается, ничего не помечается активным).
//
// SpawnAgent (ниже) — тонкая обёртка поверх этой функции с callback'ом,
// игнорирующим lease: единственная реализация запуска, как и раньше.
func (m *Manager) SpawnAgentLease(ctx context.Context, s flow.Stage, run func(ctx context.Context, s flow.Stage, lease *Lease)) {
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
		// Re-check right before run: the goroutine may have queued on sem for
		// an arbitrary amount of time, during which the stage could have been
		// paused. Without this, a queued call fires run() for a stage that's
		// no longer supposed to be running.
		if m.shouldRun != nil && !m.shouldRun(s.ID) {
			return
		}
		run(ctx, s, lease)
	}()
}

// SpawnAgent запускает агентскую горутину под семафором команды, помечает
// стадию активной и учитывает горутину в WaitGroup. Единственная точка
// запуска — заменяет ~10 копий одинакового boilerplate и гарантирует чистый
// shutdown. Делегирует SpawnAgentLease с callback'ом, игнорирующим lease —
// стадии без verify его никогда не видят, поведение не меняется.
func (m *Manager) SpawnAgent(ctx context.Context, s flow.Stage, run func(context.Context, flow.Stage)) {
	m.SpawnAgentLease(ctx, s, func(ctx context.Context, s flow.Stage, lease *Lease) {
		run(ctx, s)
	})
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
