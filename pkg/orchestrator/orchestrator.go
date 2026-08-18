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
	"github.com/akopichin/afm/pkg/orchestrator/supervisor"
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

// Prompts holds the prompt templates for each agent type.
type Prompts struct {
	Planning       string
	Implementation string
	Review         string
	Summary        string
	Autonomous     string
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
	Runner          executor.Runner // nil = real Executor
	DashboardURL    string          // e.g. "http://127.0.0.1:9876"
	WrapperDir      string          // dir with generated wrapper scripts (prepended to agent PATH)
	GeneratedAgents map[string]bool // autoShim: команды с generated-враппером (self-route)
	GlobalPrompt    string          // Flow.Prompt, forwarded to every prompts.Build call
	RootDir         string          // Flow.RootDir: project root as agent CWD (empty = inherit afm CWD)
	RequireApproval bool            // headless: fail instead of auto-approve on awaiting_approval
	Debug           bool            // if true, executors log the exact agent input to debug logs
	// SupervisorRunner — runner для вызовов Supervisor.EvaluateStage.
	// nil = Supervisor отключён глобально (DetermineStagePhases всегда вернёт базовые фазы).
	SupervisorRunner executor.Runner
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
	// supervisor оценивает, можно ли выполнить стадию автономно.
	// nil, если SupervisorRunner не задан в Options.
	supervisor *supervisor.Supervisor
	// maxRetries/retryBackoff — снапшоты package-level RetryBackoff/MaxRetries,
	// снятые в New(). Агентские горутины могут пережить возврат Run(), поэтому
	// прямое чтение этих globals гонится с мутацией в тестах (data race).
	// Снапшот на инстансе устраняет гонку: горутины читают immutable-поля.
	maxRetries   int
	retryBackoff time.Duration

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

	// Supervisor включается только если задан SupervisorRunner; иначе
	// DetermineStagePhases всегда возвращает базовые фазы.
	var sup *supervisor.Supervisor
	if opts.SupervisorRunner != nil {
		sup = supervisor.NewSupervisor(opts.SupervisorRunner)
	}

	return &Orchestrator{
		opts:           opts,
		graph:          graph.NewGraph(opts.Stages),
		runner:         r,
		critical:       critical,
		ui:             ui,
		fsm:            bus.NewFSM(opts.Store),
		concurrency:    conc,
		violationCache: make(map[string]violationCacheEntry),
		lastRootScan:   make(map[string]time.Time),
		supervisor:     sup,
		maxRetries:     MaxRetries,
		retryBackoff:   RetryBackoff,
	}
}

// UIBus returns the UIBus for external subscribers (server, WebSocket).
func (o *Orchestrator) UIBus() *bus.UIBus { return o.ui }

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

func (o *Orchestrator) onAgentCompleted(ctx context.Context, ev bus.Event) error {
	agentType, _ := ev.Data.(string)
	current := o.currentStatus(ev.StageID)

	// Open-question gate: if the agent finished but the user has not yet
	// answered an ask_user question, hold the stage in awaiting_user_input.
	// The stage resumes on EventUserAnswered.
	if o.hasOpenQuestion(ev.StageID, agentType) {
		// agentType здесь — реальная фаза от executor, не из имени файла вопроса.
		o.preAskPhase.Store(ev.StageID, agentType)
		o.Trigger(ev.StageID, bus.EvAskUser, bus.GuardCtx{Phase: agentType}, "")
		return nil
	}

	switch agentType {
	case phasePlanning:
		// Ignore stale completion if stage already left planning state
		// (e.g. approved, done, or restarted by onUserAnswered).
		if current != state.StatusPlanning && current != state.StatusRetrying {
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
// confirmed Running/Retrying, the completion cascade — EvComplete, the
// script_after hook, and unblocking dependents — is identical for all three
// phases, so it lives here once instead of being duplicated per case.
//
// Also the chokepoint resumeStageAtStatus's "recovered from disk" fast paths
// use (recovery.go): those finalize a stage without ever spawning a new
// agent, so they need the exact same cascade a normally-completing agent
// gets — skipping it left dependents of a stage resumed via Continue()
// hanging in pending forever (found live: an autonomous stage paused right
// after writing execution_summary.md, Continue then hit the fast path and
// never re-evaluated stages waiting on it).
func (o *Orchestrator) completeStage(ctx context.Context, stageID string, current state.StageStatus, reason string) {
	if current != state.StatusRunning && current != state.StatusRetrying {
		return
	}
	o.Trigger(stageID, bus.EvComplete, bus.GuardCtx{}, reason)
	o.maybeRunAfterHook(ctx, stageID)
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
