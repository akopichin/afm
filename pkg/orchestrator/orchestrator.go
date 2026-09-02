package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// Имена фаз выполнения — единый источник в pkg/flow. Локальные строковые
// алиасы оставлены, т.к. по коду orchestrator фаза используется как string;
// значения больше не дублируются литералами.
const (
	phasePlanning       = string(flow.PhasePlanning)
	phaseImplementation = string(flow.PhaseImplementation)
	phaseReview         = string(flow.PhaseReview)
	phaseAutonomous     = string(flow.PhaseAutonomous)
)
const keyAnswer = "answer"
const keyID = "id"
const keyPhase = "phase"
const keyFromOptions = "from_options"

// Prompts holds the prompt templates for each agent type.
type Prompts struct {
	Planning       string
	Implementation string
	Review         string
	Summary        string
	Autonomous     string
	Reflect        string
	Aggregate      string
	Prioritize     string
	Update         string
}

// DefaultPrompts returns empty prompts (will be set from assets).
func DefaultPrompts() Prompts { return Prompts{} }

// Options configures an Orchestrator.
type Options struct {
	RunDir          string
	Stages          []flow.Stage
	Store           *state.Store
	Config          config.Config
	Prompts         Prompts
	Runner          executor.Runner   // nil = real Executor
	DashboardURL    string            // e.g. "http://127.0.0.1:9876"
	WrapperDir      string            // dir with generated wrapper scripts (prepended to agent PATH)
	GeneratedAgents map[string]bool   // autoShim: команды с generated-враппером (self-route)
	GlobalPrompt    string            // Flow.Prompt, forwarded to every prompts.Build call
	RootDir         string            // Flow.RootDir: project root as agent CWD (empty = inherit afm CWD)
	RequireApproval bool              // headless: fail instead of auto-approve on awaiting_approval
	Debug           bool              // if true, executors log the exact agent input to debug logs
	Memory          flow.MemoryConfig // agent-память v3: параметры конвейера (max_rules/commit)
	MemoryDir       string            // abs путь к директории памяти ("" = выключено)
}

// Orchestrator manages the full lifecycle of a flow run via event loop.
type Orchestrator struct {
	opts     Options
	graph    *graph.Graph
	runner   executor.Runner
	critical *bus.CriticalBus
	ui       *bus.UIBus
	fsm      *bus.FSM
	// concurrency инкапсулирует семафоры на команду, учёт активных агентских
	// горутин и WaitGroup для чистого shutdown (вынесено в отдельный пакет).
	concurrency *concurrency.Manager
	// interruptChans хранит канал прерывания (stageID → chan struct{}, буфер
	// 1) на время КОНКРЕТНОЙ попытки RunAgent — создаётся в начале
	// runWithRetry, удаляется по её завершении (успешном или нет). Revise
	// (agent_suggest) шлёт в этот канал, чтобы запросить graceful-прерывание
	// текущего вызова агента через executor.Config.InterruptCh.
	interruptChans sync.Map
	// preAskPhase хранит корректную фазу в момент EvAskUser (stageID → phase string).
	// Используется при EvUserAnswered вместо фазы из имени файла вопроса:
	// агент может написать неправильное имя фазы (напр. "review" вместо "planning"),
	// что ломает phaseDispatch и уводит FSM в wrong state.
	preAskPhase sync.Map
	// hookWaiters holds, per stageID, the channel a blocked before/after hook
	// is waiting on for a user decision (see hooks.go: waitForHookDecision/
	// resolveHook). Only populated while a hook is actually blocked.
	hookWaiters sync.Map
	// violationCache кешует stat .jsonl-файлов для detectDialogViolation.
	// Доступен только из горутины поллера — мьютекс не нужен.
	violationCache map[string]violationCacheEntry // key: путь к .jsonl
	// lastRootScan хранит время последнего скана root_dir на стадию (throttle
	// в relocateMisplacedQuestions). Доступен только из горутины поллера.
	lastRootScan map[string]time.Time
	// maxRetries/retryBackoff — снапшоты package-level RetryBackoff/MaxRetries,
	// снятые в New(). Агентские горутины могут пережить возврат Run(), поэтому
	// прямое чтение этих globals гонится с мутацией в тестах (data race).
	// Снапшот на инстансе устраняет гонку: горутины читают immutable-поля.
	maxRetries   int
	retryBackoff time.Duration
	// spawnJSONFix запускает свежий изолированный агент, единственная задача
	// которого — переписать битый question.json валидным JSON (см.
	// runJSONFixAgent). Возвращает канал, закрываемый по завершении агента.
	// Инъектируется в New(); тесты подменяют стабом, чинящим файл синхронно.
	spawnJSONFix func(s flow.Stage, phase, id string) <-chan struct{}

	// runMemoryAgent запускает один агент конвейера памяти (reflect/
	// aggregate/prioritize/update). Реальная реализация — execMemoryAgent;
	// тесты подменяют.
	runMemoryAgent func(ctx context.Context, spec memoryAgentSpec) error

	// fatalMu/fatalErr/cancelRun поддерживают разведение storage-fatal и
	// concurrent-change (см. Trigger/setFatal/loadFatal/Run): только реальный
	// сбой стораджа (StorageError) должен останавливать run, а не безобидный
	// CAS-mismatch между конкурентными переходами.
	fatalMu   sync.Mutex
	fatalErr  error
	cancelRun context.CancelFunc

	// pendingAfterHooks — счётчик живых script_after горутин (инкремент
	// синхронно в maybeRunAfterHook ДО concurrency.SpawnAgent, декремент из её же
	// cleanup-обёртки, см. hooks.go). Нужен specifically для shouldExit
	// (scheduling.go): script_after — единственный вид агента, который НЕ
	// трогает FSM своей стадии (см. runAfterHook), так что allTerminal()
	// остаётся true, пока хук ещё реально выполняется или ждёт решения
	// RetryHook/SkipHook — без этого счётчика Run() мог бы отменить свой ctx
	// (shutdown) в тот же момент, когда стадия стала done, убив только что
	// запущенный after-hook раньше, чем он успеет хоть раз стартовать.
	// Намеренно уже, чем общий agentWG внутри concurrency.Manager: остальные
	// типы агентов уже двигают FSM-статус, на который и так смотрит
	// allTerminal(), так что им эта бухгалтерия не нужна и не добавляется.
	// Обычный sync.WaitGroup не даёт прочитать текущий счётчик, поэтому
	// отдельный atomic.
	pendingAfterHooks atomic.Int32

	// pendingReflections — счётчик живых конвейеров памяти (инкремент в
	// maybeRunReflection ДО SpawnDetached, декремент в обёртке), чтобы
	// shouldExit не завершил ран, пока конвейер в полёте. Зеркалит
	// pendingAfterHooks.
	pendingReflections atomic.Int32
	// reflectMu сериализует запись в общие файлы памяти: одновременно бежит
	// максимум один конвейер (best-effort/фон, латентность очереди неважна).
	reflectMu sync.Mutex
	// finalReflectDone гарантирует, что финальный проход памяти по всей сессии
	// флоу (см. runEndOfRunMemory) прогонится не более одного раза.
	// Читается/пишется только на единственной горутине Run — без мьютекса.
	finalReflectDone bool

	// runMu/runCtx хранят долгоживущий контекст event loop (см. Run), который
	// HTTP-инициированные Approve/Revise/Retry подставляют вместо request-ctx
	// перед спавном агента (см. runContext). net/http отменяет r.Context() сразу
	// после возврата хэндлера — если передать его в SpawnAgent как есть, только
	// что запущенный агент будет убит немедленно (exec.CommandContext убивает
	// процесс на <-ctx.Done()). Мьютекс нужен, т.к. HTTP-сервер начинает слушать
	// ДО вызова Run (см. cmd/afm/run.go) — запрос может прийти раньше, чем Run
	// проставит runCtx.
	runMu  sync.Mutex
	runCtx context.Context

	// pauseGen — монотонный per-stage счётчик, инкрементируемый каждым Pause
	// (см. control_api.go). withBeforeHook захватывает его на входе и сверяет
	// после script_before: любой Pause за время хука (в т.ч. ABA
	// running->paused->running через Continue) меняет счётчик, и хук
	// отказывается запускать mainFn — повторный запуск становится
	// ответственностью Continue/resumeStageAtStatus, что исключает двойной старт
	// агента. Обычной проверки currentStatus==paused недостаточно: она не ловит
	// ABA (после Continue статус снова running). Значение — *atomic.Uint64 на
	// стадию (см. bumpPauseGen/loadPauseGen).
	pauseGen sync.Map

	// retryCASBarrier — тест-сейм (nil в проде): вызывается в retryStage сразу
	// после проверки статуса failed и ДО CAS EvManualRetry, позволяя тесту
	// детерминированно смоделировать проигрыш CAS (перевести стадию из failed
	// на другой горутине) и проверить, что проигравший вызов не чистит
	// session/jsonl победителя. Инъектируется через SetRetryCASBarrierForTest.
	retryCASBarrier func(stageID string)
}

// bumpPauseGen увеличивает per-stage generation-счётчик паузы (см. поле
// pauseGen). Вызывается из Pause после успешного EvPause.
func (o *Orchestrator) bumpPauseGen(stageID string) {
	v, _ := o.pauseGen.LoadOrStore(stageID, new(atomic.Uint64))
	v.(*atomic.Uint64).Add(1)
}

// loadPauseGen возвращает текущее значение generation-счётчика паузы стадии
// (0, если стадию ещё ни разу не ставили на паузу).
func (o *Orchestrator) loadPauseGen(stageID string) uint64 {
	v, ok := o.pauseGen.Load(stageID)
	if !ok {
		return 0
	}
	return v.(*atomic.Uint64).Load()
}

// setFatal фиксирует первую storage-fatal ошибку и отменяет run-контекст,
// чтобы event loop завершился и Run вернул ошибку.
func (o *Orchestrator) setFatal(err error) {
	o.fatalMu.Lock()
	if o.fatalErr == nil {
		o.fatalErr = err
	}
	o.fatalMu.Unlock()
	if o.cancelRun != nil {
		o.cancelRun()
	}
}

func (o *Orchestrator) loadFatal() error {
	o.fatalMu.Lock()
	defer o.fatalMu.Unlock()
	return o.fatalErr
}

// New creates an Orchestrator.
func New(opts Options) *Orchestrator {
	critical := bus.NewCriticalBus(16)
	ui := bus.NewUIBus()

	r := opts.Runner
	if r == nil {
		r = executor.New(executor.Config{
			Command:        opts.Config.Client.Command,
			ExtraArgs:      opts.Config.Client.ExtraArgs,
			IdleTimeout:    opts.Config.Executor.IdleTimeout,
			TruncateOutput: opts.Config.Executor.TruncateOutput,
			OnAction:       uiActionPublisher(ui, ""),
			WrapperDir:     wrapperDirFor(opts.Config.Client.Command, opts.WrapperDir, opts.GeneratedAgents),
			Debug:          opts.Debug,
			RunDir:         opts.RunDir,
		})
	}

	// Семафоры на команду строятся из конфигурации стадий: per-stage
	// MaxParallel имеет приоритет над глобальным дефолтом (см.
	// concurrency.New — 1:1 перенос прежней логики этого блока).
	// shouldRun — единая точка проверки паузы перед стартом агента (закрывает
	// гонку "стадию поставили на паузу, пока она ждала слот в семафоре"),
	// заменяет прежние разрозненные проверки в withBeforeHook и runWithRetry.
	conc := concurrency.New(critical, opts.Stages, opts.Config.Client.Command, opts.Config.Executor.MaxParallel,
		func(stageID string) bool { return opts.Store.Get(stageID) != state.StatusPaused })

	o := &Orchestrator{
		opts:           opts,
		graph:          graph.NewGraph(opts.Stages),
		runner:         r,
		critical:       critical,
		ui:             ui,
		fsm:            bus.NewFSM(opts.Store),
		concurrency:    conc,
		violationCache: make(map[string]violationCacheEntry),
		lastRootScan:   make(map[string]time.Time),
		maxRetries:     MaxRetries,
		retryBackoff:   RetryBackoff,
	}
	o.spawnJSONFix = o.runJSONFixAgent
	o.runMemoryAgent = o.execMemoryAgent
	return o
}

// UIBus returns the UIBus for external subscribers (server, WebSocket).
func (o *Orchestrator) UIBus() *bus.UIBus { return o.ui }

// publishCritical wraps o.critical.Publish, logging instead of silently
// discarding a drop. ctx here is always the shared, cancellable o.runCtx
// (every SpawnAgent calls, all the way down to runWithRetry, thread the same
// context passed to Run) — setFatal (above) proactively and synchronously
// cancels that exact context the instant ANY stage's storage write fails, so
// an unrelated stage's legitimate completion event racing that cancellation
// could have its Publish lose the ctx.Done()/channel-send select and vanish
// with zero trace. This doesn't retry or recover the event — a dropped
// EventAgentCompleted here still means that stage's FSM status can lag for
// the remainder of a dying run; recovery-on-restart independently re-derives
// completion from on-disk artifacts (plan.md/execution_summary.md/.done), so
// the loss isn't permanent across a restart. Logging exists purely so this
// class of loss is visible during debugging instead of silent.
func (o *Orchestrator) publishCritical(ctx context.Context, ev bus.Event) {
	if err := o.critical.Publish(ctx, ev); err != nil {
		log.Printf("orchestrator: dropped %s event for stage %q: %v", ev.Type, ev.StageID, err)
	}
}

// Trigger applies an FSM event to transition a stage's status.
// Returns the new status and whether the transition was applied.
func (o *Orchestrator) Trigger(stageID string, ev bus.FSMEvent, ctx bus.GuardCtx, reason string) (state.StageStatus, bool) {
	to, _, ok := o.triggerWithSeq(stageID, ev, ctx, reason)
	return to, ok
}

// triggerWithSeq — как Trigger, но дополнительно возвращает seq применённой
// transition (0, если переход не применился). Нужен только тем call site'ам,
// что публикуют СВОЙ, более специфичный тип UI-события рядом с этим же
// переходом (ask_user/user_answered/retry_scheduled/retry_exhausted) — им
// нужен тот же реальный seq, чтобы фронт дедуплицировал историю из
// /api/events с live-потоком по стабильному ключу, а не по содержимому.
// Остальные ~60 call site'ов Trigger в этом не нуждаются и не меняются.
func (o *Orchestrator) triggerWithSeq(stageID string, ev bus.FSMEvent, ctx bus.GuardCtx, reason string) (state.StageStatus, uint64, bool) {
	to, seq, ok, err := o.fsm.Apply(stageID, ev, ctx, reason)
	if err != nil {
		var se *StorageError
		if errors.As(err, &se) {
			// Storage-fatal: authoritative log write failed — нельзя продолжать
			// принимать решения против сломанного лога. Завершаем run.
			log.Printf("FATAL: storage failure applying %s/%s: %v", stageID, ev, err)
			o.setFatal(err)
			return o.currentStatus(stageID), 0, false
		}
		// Не-storage ошибка (напр. ErrNoRule — неизвестное событие, баг в коде):
		// логируем и роняем переход, но НЕ валим весь run.
		log.Printf("CRITICAL: FSM Apply %s/%s: %v", stageID, ev, err)
		return o.currentStatus(stageID), 0, false
	}
	if ok {
		pubEv := bus.Event{Type: bus.EventStageStatusChanged, StageID: stageID, Data: string(to), Seq: seq}
		o.ui.Publish(pubEv)
		// Wake the event loop so it can check shouldExit(). Non-blocking to avoid deadlock.
		o.critical.TryPublish(pubEv)
	}
	return to, seq, ok
}

// SetDashboardURL sets the dashboard URL after the server starts listening.
func (o *Orchestrator) SetDashboardURL(url string) { o.opts.DashboardURL = url }

// Run starts the event-driven orchestrator loop.
func (o *Orchestrator) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	o.cancelRun = cancel
	o.runMu.Lock()
	o.runCtx = ctx
	o.runMu.Unlock()
	defer o.concurrency.WaitAgents() // выполнится ПОСЛЕ cancel (LIFO) — сначала отмена, потом ожидание
	defer cancel()

	o.startPlanningForPending(ctx)
	o.startQuestionPoller(ctx) // file-based dialog poller

	for {
		select {
		case <-ctx.Done():
			if ferr := o.loadFatal(); ferr != nil {
				return ferr
			}
			return ctx.Err()
		case ev := <-o.critical.Recv():
			if err := o.handleEvent(ctx, ev); err != nil {
				return err
			}
			if ferr := o.loadFatal(); ferr != nil {
				return ferr
			}
			if o.shouldExit() {
				o.runEndOfRunMemory(ctx)
				return nil
			}
		}
	}
}

// handleEvent dispatches events to the appropriate handler.
func (o *Orchestrator) handleEvent(ctx context.Context, ev bus.Event) error {
	switch ev.Type {
	case bus.EventAgentCompleted:
		return o.onAgentCompleted(ctx, ev)
	case bus.EventUserAnswered:
		return o.onUserAnswered(ctx, ev)
	}
	return nil
}

// onAgentCompleted consumes EventAgentCompleted, published from exactly two
// places: runWithRetry (retry.go — planning/implementation/autonomous, the
// only phases the file-based dialog protocol applies to) and the
// phaseScript path (agents.go), which can never have an open question
// (flow.IsValidPhase rejects "script" as a dialog phase, so hasOpenQuestion
// would always be false for it anyway).
//
// This function does NOT re-derive "should we hold for an open question" —
// that decision belongs entirely to whichever code published the event.
// An earlier version DID duplicate runWithRetry's open-question-vs-
// completion check here, independently — found live, during a related fix,
// that the duplicate had silently drifted out of sync with the original
// (fixed in one place, not the other), stranding a genuinely finished stage
// forever. Two copies of the same business rule is the bug, not just an
// incomplete patch — removed rather than fixed a second time. See
// runWithRetry's comment for the actual precedence logic.
func (o *Orchestrator) onAgentCompleted(ctx context.Context, ev bus.Event) error {
	agentType, _ := ev.Data.(string)
	current := o.currentStatus(ev.StageID)

	// Open-question gate, ungated by s.Interactive/phase — unlike
	// runWithRetry's own narrower check (interactive/autonomous stages
	// only), this is the catch-all for phases that check skips (e.g. a
	// non-interactive planning stage whose agent leaves a genuinely fresh
	// question). Restored after being removed as a supposed pure duplicate
	// of runWithRetry's check — it isn't: it has a strictly broader scope,
	// and TestIntegration_PlanningWithOpenQuestionWaits (non-interactive
	// stage, expects to hold for its own fresh question) regressed without
	// it. Skipped when current is ALREADY AwaitingUserInput: that means the
	// independent question poller raced ahead of this exact agent
	// invocation and parked the stage there WHILE the agent was still
	// running — the open question is then a stale tail from earlier in the
	// stage's life (e.g. the id-reuse bug, since fixed), not a live one
	// this completion should wait on — see runWithRetry's matching comment.
	if current != state.StatusAwaitingUserInput && o.hasOpenQuestion(ev.StageID, agentType) {
		o.preAskPhase.Store(ev.StageID, agentType)
		o.Trigger(ev.StageID, bus.EvAskUser, bus.GuardCtx{Phase: agentType}, "")
		return nil
	}

	switch agentType {
	case phasePlanning:
		// Ignore stale completion if stage already left planning state
		// (e.g. approved, done, or restarted by onUserAnswered).
		//
		// AwaitingUserInput is accepted alongside Planning/Retrying for the
		// same reason as completeStage's precondition below: the poller can
		// independently race the stage there — due to an unrelated,
		// abandoned open question — while the planning agent is still alive
		// and about to return a valid plan.md. Rejecting it here would
		// strand planning forever with no agent process left to retry.
		if current != state.StatusPlanning && current != state.StatusRetrying && current != state.StatusAwaitingUserInput {
			return nil
		}
		o.Trigger(ev.StageID, bus.EvPlanReady, bus.GuardCtx{}, "")
		// auto_approve: true on the stage config wins regardless of
		// dashboard/--require-approval — checked before the headless branch.
		if stage := o.graph.Stage(ev.StageID); stage != nil && o.autoApproveIfConfigured(ctx, *stage) {
			return nil
		}
		// Headless: нет дашборда → никто не нажмёт Approve.
		// RequireApproval=true → fail-fast с понятным сообщением.
		// RequireApproval=false (дефолт) → авто-апрув, flow идёт дальше.
		if o.opts.DashboardURL == "" {
			if o.opts.RequireApproval {
				o.FailStage(ev.StageID, "approval required but no dashboard running (use --port or server.port in config)")
				return nil
			}
			log.Printf("headless: auto-approving plan for stage %q", ev.StageID)
			o.approveStage(ctx, ev.StageID)
			return nil
		}
		o.tryActivatePrePlanned(ctx)
	case phaseImplementation, phaseAutonomous:
		// agent_suggest race: агент завершился ЕСТЕСТВЕННО (вернул nil) в
		// момент, почти совпадающий с Revise() (running -> revising) —
		// SIGINT/сигнал в interruptChans так и не был замечен субпроцессом
		// (он уже возвращался), поэтому onUserInterrupted не сработал, и
		// runWithRetry дошёл сюда обычным успешным путём. current уже
		// StatusRevising, а не Running/Retrying — без реконсиляции стадия
		// осталась бы в revising до конца live-рана (тот же кейс для краша
		// уже решён в recovery.go/startPlanningForPending). Диспатчим тем
		// же способом: перезапуск с фидбеком, уже сохранённым на диске.
		if current == state.StatusRevising {
			stage := o.graph.Stage(ev.StageID)
			if stage == nil {
				return nil
			}
			if agentType == phaseAutonomous {
				o.concurrency.SpawnAgent(ctx, *stage, o.runAutonomousWithFeedback)
			} else {
				o.concurrency.SpawnAgent(ctx, *stage, o.runImplementationWithFeedback)
			}
			return nil
		}
		// Фаза завершена: implementation-агент дошёл до конца, либо
		// autonomous-трек написал execution_summary.md → переводим в done.
		o.completeStage(ctx, ev.StageID, current, "")
	case phaseScript:
		// Script-стадия завершилась (runScriptStage): нет revising-гонки
		// (interruptChans не регистрируется вне runWithRetry, Revise() на
		// script-стадию не осмыслен) — просто done, как остальные фазы.
		o.completeStage(ctx, ev.StageID, current, "")
	default:
		// review or unknown agent type: no status change needed
	}
	return nil
}

// completeStage finalizes a stage that genuinely reached done. It is the
// single chokepoint shared by onAgentCompleted's phaseImplementation/
// phaseAutonomous and phaseScript branches: their pre-checks differ (the
// former also handles the revising race above), but once a stage is
// confirmed Running/Retrying/AwaitingUserInput, the completion cascade —
// EvComplete, the script_after hook, and unblocking dependents — is
// identical for all three phases, so it lives here once instead of being
// duplicated per case.
//
// AwaitingUserInput is accepted alongside Running/Retrying because afm's own
// question poller can independently move a stage there — due to an
// unrelated, permanently-abandoned open question — WHILE the agent is still
// running and about to finish for real. runWithRetry (retry.go) is the sole
// authority on "is this stage actually done despite an open question" — by
// the time it publishes EventAgentCompleted here, that decision is already
// final; rejecting AwaitingUserInput here would strand a genuinely-finished
// stage forever, with no agent process left alive to ever retry.
//
// Also the chokepoint resumeStageAtStatus's "recovered from disk" fast paths
// use (recovery.go): those finalize a stage without ever spawning a new
// agent, so they need the exact same cascade a normally-completing agent
// gets — skipping it left dependents of a stage resumed via Continue()
// hanging in pending forever (found live: an autonomous stage paused right
// after writing execution_summary.md, Continue then hit the fast path and
// never re-evaluated stages waiting on it).
func (o *Orchestrator) completeStage(ctx context.Context, stageID string, current state.StageStatus, reason string) {
	if current != state.StatusRunning && current != state.StatusRetrying && current != state.StatusAwaitingUserInput {
		return
	}
	// current — снимок статуса, прочитанный вызывающим (onAgentCompleted/
	// resumeStageAtStatus) ДО этого момента: между чтением и CAS сюда мог влезть
	// Pause/Revise на другой горутине (HTTP-хендлер) и увести стадию из
	// running/retrying. Тогда EvComplete не применится — и побочные эффекты
	// (after-hook, reflection, каскад разблокировки зависимых) запускать НЕЛЬЗЯ:
	// стадия не завершена. Отдельно это чинит утечку pendingAfterHooks —
	// maybeRunAfterHook инкрементил счётчик, а SpawnAgent отбрасывал callback с
	// декрементом на paused-стадии (см. concurrency.shouldRun); после успешного
	// EvComplete стадия done, а из done Pause невозможен, так что callback
	// after-hook гарантированно выполнится и декремент произойдёт.
	if _, ok := o.Trigger(stageID, bus.EvComplete, bus.GuardCtx{}, reason); !ok {
		return
	}
	o.maybeRunAfterHook(ctx, stageID)
	o.maybeRunReflection(ctx, stageID)
	o.failBlockedStages()
	o.startPlanningForUnblocked(ctx)
	o.startReadyStages(ctx)
	o.tryActivatePrePlanned(ctx)
}

// onUserAnswered resumes a stage that was paused on awaiting_user_input.
// If the agent is still running (its bash loop is waiting for answer.json),
// NotifyAnswer already transitioned the status — this is a no-op.
// If the agent exited before the user answered, we restart it here.
func (o *Orchestrator) onUserAnswered(ctx context.Context, ev bus.Event) error {
	if o.currentStatus(ev.StageID) != state.StatusAwaitingUserInput {
		return nil
	}

	data, _ := ev.Data.(map[string]any)
	rawPhase, _ := data[keyPhase].(string)
	if rawPhase == "" {
		return nil
	}

	if o.hasOpenQuestion(ev.StageID, rawPhase) {
		return nil
	}

	stage := o.graph.Stage(ev.StageID)
	if stage == nil {
		return nil
	}

	// Используем корректную фазу из preAskPhase, а не сырую из имени файла вопроса.
	phase := o.popPreAskPhase(ev.StageID, rawPhase)

	// Agent exited before the user answered. Restart it so it can read
	// answer.json (bash loop exits immediately since the file now exists).
	switch phase {
	case phasePlanning:
		o.Trigger(ev.StageID, bus.EvUserAnswered, bus.GuardCtx{Phase: phasePlanning}, "")
		o.concurrency.SpawnAgent(ctx, *stage, o.runPlanningAgent)
	case phaseImplementation:
		o.Trigger(ev.StageID, bus.EvUserAnswered, bus.GuardCtx{Phase: phaseImplementation}, "")
		o.concurrency.SpawnAgent(ctx, *stage, o.runImplementationAgent)
	case phaseReview:
		o.Trigger(ev.StageID, bus.EvUserAnswered, bus.GuardCtx{Phase: phaseReview}, "")
		o.concurrency.SpawnAgent(ctx, *stage, o.runReviewAgent)
	case phaseAutonomous:
		o.Trigger(ev.StageID, bus.EvUserAnswered, bus.GuardCtx{Phase: phaseAutonomous}, "")
		o.concurrency.SpawnAgent(ctx, *stage, o.runAutonomousAgent)
	default:
		return fmt.Errorf("unexpected phase: %q", phase)
	}
	return nil
}

func (o *Orchestrator) currentStatus(id string) state.StageStatus {
	return o.opts.Store.Get(id)
}

// resolvePlanSource превращает путь stage.Plan в путь к существующему файлу.
// Plan — путь к существующему файлу плана. Если путь относительный (./…), план
// обычно произведён стадией-зависимостью и лежит в её run-директории
// (<runDir>/<depID>/<path>) — ищем его там. Не найден ни в одной зависимости —
// fallback на буквальный путь (pre-existing план в project-dir, прежнее поведение).
func resolvePlanSource(runDir string, stage flow.Stage) string {
	if stage.Plan == "" || !strings.HasPrefix(stage.Plan, "./") {
		return stage.Plan
	}
	rel := strings.TrimPrefix(stage.Plan, "./")
	for _, depID := range stage.DependsOn {
		candidate := filepath.Join(runDir, depID, rel)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return stage.Plan
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

// StoreFromOrch возвращает Store оркестратора. Только для тестов.
func StoreFromOrch(o *Orchestrator) *state.Store { return o.opts.Store }

// InterruptChanForTest возвращает канал прерывания текущей попытки RunAgent
// для стадии (см. interruptChans), если он зарегистрирован. Только для
// тестов: позволяет инъектированному mock-раннеру (пакет orchestrator_test)
// самому дождаться того же сигнала, который Revise() шлёт для running-стадии
// — без этого accessor'а мок не смог бы отличить "прервали фразой" от
// обычной отмены ctx, а runWithRetry.onUserInterrupted никогда бы не
// сработал в тесте.
func InterruptChanForTest(o *Orchestrator, stageID string) (chan struct{}, bool) {
	ch, ok := o.interruptChans.Load(stageID)
	if !ok {
		return nil, false
	}
	return ch.(chan struct{}), true
}
