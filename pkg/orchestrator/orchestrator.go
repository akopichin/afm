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

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/browser"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/memorypipeline"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// Имена фаз выполнения — единый источник в pkg/flow. Локальные строковые алиасы оставлены, т.к. по коду
// orchestrator фаза используется как string; значения больше не дублируются литералами.
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
	// Verify — assets/prompts/verify.md, инструкция AI-verify агенту. Отдельно от остальных фаз: verify не
	// входит в flow.Phases(), это ортогональный проход ПОСЛЕ заявленного стадией завершения.
	Verify string
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
	// CurrentFileSHA резолвит текущий content_sha файла для renderReviewFeedback — сравнить с content_sha
	// ноты на момент injection и пометить дрифт.
	CurrentFileSHA func(root, path string) (string, bool)
	// ResolveFile резолвит {root,path} в ResolvedFile для AddNote — display/reference, текущий content_sha и,
	// для построчной ноты, текст строки + её валидность.
	ResolveFile func(root, path string, line *int) (ResolvedFile, bool)
	// Accounting — durable usage.jsonl store for this run. Observability only: never gates the FSM, never
	// fails the run.
	Accounting *accounting.Store
	// Lifecycle hooks (Phase 1): FlowName/RunID/Resumed питают payload,
	// Hooks — собранный dispatcher (nil = хуков нет). Сборка — cmd/afm/run.go.
	FlowName string
	RunID    string
	Resumed  bool
	Hooks    *lifecyclehooks.Dispatcher
	// BrowserDescriptor — managed Chrome DevTools capability for this run.
	BrowserDescriptor *browser.Descriptor
	// AssistantsEnabled/AssistantsRegistry — the collaboration capability for this run (T3.4). Resolved ONCE
	// by cmd/afm before Orchestrator.New (mirrors resolveCollaboration/startCollaboration) — disabled or a nil
	// registry both mean "capability off": Attach rejects immediately, nothing here ever spawns a model on
	// its own (Инвариант 9).
	AssistantsEnabled  bool
	AssistantsRegistry *assistants.Registry
}

// ResolvedFile описывает файл review-ноты: путь, ссылку, текущий SHA и, для построчной ноты, текст строки
// и InRange.
type ResolvedFile struct {
	DisplayPath string
	Reference   string
	ContentSHA  string
	LineText    string
	InRange     bool
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
	// interruptChans хранит канал прерывания на время КОНКРЕТНОЙ попытки RunAgent — создаётся в начале
	// runWithRetry, удаляется по её завершении.
	interruptChans sync.Map
	// stageLeases хранит *concurrency.Lease текущего исполнительского раннера стадии — на время его
	// выполнения.
	stageLeases sync.Map
	// preAskPhase хранит корректную фазу в момент EvAskUser.
	preAskPhase sync.Map
	// hookWaiters holds, per stageID, the channel a blocked before/after hook is waiting on for a user
	// decision. Only populated while a hook is actually blocked.
	hookWaiters sync.Map
	// violationCache кешует stat .jsonl-файлов для detectDialogViolation.
	// Доступен только из горутины поллера — мьютекс не нужен.
	violationCache map[string]violationCacheEntry // key: путь к .jsonl
	// lastRootScan хранит время последнего скана root_dir на стадию (throttle
	// в relocateMisplacedQuestions). Доступен только из горутины поллера.
	lastRootScan map[string]time.Time
	// maxRetries/retryBackoff — снапшоты package-level RetryBackoff/MaxRetries, снятые в New. Снапшот на
	// инстансе устраняет гонку: горутины читают immutable-поля.
	maxRetries   int
	retryBackoff time.Duration
	// spawnJSONFix запускает свежий изолированный агент, единственная задача которого — переписать битый
	// question.json валидным JSON. Возвращает канал, закрываемый по завершении агента.
	spawnJSONFix func(s flow.Stage, phase, id string) <-chan struct{}

	// mem — двухфазный движок конвейера памяти, собранный в New: CaptureStage и Finalize.
	mem *memorypipeline.Pipeline

	// hooks — lifecycle dispatcher (observer-only; nil-safe).
	hooks *lifecyclehooks.Dispatcher

	// runVerifyAgent — подменяемый запуск одного AI-verify шага; продакшн использует execVerifyAgent со
	// свежим executor.
	runVerifyAgent verifyAgentRunner

	// runVerifyShell — подменяемый запуск shell-verify шага для детерминированных тестов гонок.
	runVerifyShell verifyShellRunner

	// fingerprintFn — подменяемый отпечаток дерева: используется review_patience (gated на reviewPatience>0 в
	// retry.go) и наблюдательной проверкой verify_tree_modified (безусловно, на каждом AI-verify шаге).
	fingerprintFn func(dir string) (string, bool)

	// runHelperAgent — подменяемый запуск одного хода помощника (T3.4), продакшн — execHelperAgent. Тесты
	// подставляют фейковый раннер (см. assistants_test.go) — тот же seam-паттерн, что и runVerifyAgent выше.
	runHelperAgent helperAgentRunner
	// taskCancels хранит context.CancelFunc активного прогона задачи на время её выполнения — Pause/Cancel
	// используют его, чтобы прервать уже стартовавший helper-процесс немедленно (ключ — assistants.TaskID).
	taskCancels sync.Map

	// terminalFlow — финальное flow-событие, установленное одним из выходов Run; эмитится finalizeLifecycle
	// ПОСЛЕ остановки продюсеров.
	terminalFlow lifecyclehooks.EventType

	// pollerDone закрывается горутиной startQuestionPoller при её выходе.
	pollerDone chan struct{}

	// fatalMu/fatalErr/cancelRun останавливают run только при StorageError; CAS-mismatch не фатален.
	fatalMu   sync.Mutex
	fatalErr  error
	cancelRun context.CancelFunc

	// pendingAfterHooks — счётчик живых script_after горутин. Обычный sync.WaitGroup не даёт прочитать
	// текущий счётчик, поэтому отдельный atomic.
	pendingAfterHooks atomic.Int32

	// pendingReflections — счётчик живых конвейеров памяти, чтобы shouldExit не завершил ран, пока конвейер в
	// полёте. Зеркалит pendingAfterHooks.
	pendingReflections atomic.Int32
	// reflectMu сериализует запись в общие файлы памяти: одновременно бежит
	// максимум один конвейер (best-effort/фон, латентность очереди неважна).
	reflectMu sync.Mutex
	// finalReflectDone гарантирует, что финальный проход памяти по всей сессии флоу прогонится не более
	// одного раза. Читается/пишется только на единственной горутине Run — без мьютекса.
	finalReflectDone bool

	// runMu/runCtx хранят долгоживущий контекст event loop, который HTTP-инициированные Approve/Revise/Retry
	// подставляют вместо request-ctx перед спавном агента (net/http отменяет r.Context() сразу после возврата
	// хэндлера — передать его в SpawnAgent значило бы убить только что запущенного агента). Мьютекс нужен, т.к.
	// HTTP-сервер начинает слушать ДО вызова Run — запрос может прийти раньше, чем Run проставит runCtx.
	runMu  sync.Mutex
	runCtx context.Context
	// Continue can race startup recovery. Keep its status transition and the
	// per-process continuation marker atomic with recovery's status read.
	continueMu           sync.Mutex
	continuedThisProcess sync.Map

	// pauseGen растёт при каждом Pause; before-hook сверяет generation, чтобы не запускать mainFn после
	// Pause/Continue.
	pauseGen sync.Map

	// runnerKind хранит, каким раннером сейчас активирована стадия — stageID -> одна из констант
	// kindPlanning/kindImplementation/kindReview/kindAutonomous.
	runnerKind sync.Map

	// resumeContextOnce включает retry-контекст на attempt 0 после Continue; runWithRetry потребляет маркер
	// однократно.
	resumeContextOnce sync.Map

	// retryCASBarrier — тестовый хук между проверкой failed и CAS; проверяет, что проигравший retry не
	// очищает данные победителя.
	retryCASBarrier func(stageID string)

	// verifyOutcomeGuardHook — тест-сейм: вызывается в retry.go's commitVerifyFailure НЕПОСРЕДСТВЕННО ПЕРЕД
	// атомарным Trigger — т.е. ПОСЛЕ дешёвого fast-path чтения, но ДО самого CAS.
	verifyOutcomeGuardHook func(stageID string)

	// taskRequeueBarrier — тест-сейм: вызывается в spawnTask's run body СРАЗУ после того, как taskHeld
	// вернул true (held), но ДО finishTaskFrom's requeue-commit (running->queued) — позволяет тесту
	// детерминированно воспроизвести lost-wakeup гонку PauseTask/ContinueTask вокруг этого окна (см.
	// spawnTask's doc comment).
	taskRequeueBarrier func(taskID assistants.TaskID)

	// flowPauseMu защищает составные решения PauseFlow; отдельные поля activationHeld/finalizing/reviewMarker
	// читаются атомарно.
	flowPauseMu sync.Mutex
	// activationHeld — true, пока активен ревью-режим: НИ ОДНА новая стадия не должна активироваться.
	activationHeld atomic.Bool
	// finalizing — true, пока идёт финализация ревью-транзакции (следующая задача фичи "review notes");
	// PauseFlow отклоняет новый раунд, пока флаг взведён (см. ErrRunFinalizing).
	finalizing atomic.Bool
	// reviewMarker — in-memory кэш текущего маркера паузы ревью. atomic.Pointer делает reviewTxnActive()
	// lock-free.
	reviewMarker atomic.Pointer[state.PauseMarker]

	// reviewResumed помечает owner'ов, запущенных review-recovery, чтобы последующий bootstrap не спавнил их
	// повторно.
	reviewResumed sync.Map

	// testRunnerHook подменяет Trigger+SpawnAgent в resumeOwner, позволяя проверить resume_kind и feedback
	// без subprocess.
	testRunnerHook func(kind string, withFeedback bool)

	// testClearReviewArtifacts подменяет очистку review-файлов для проверки fail-closed поведения при ошибке
	// удаления.
	testClearReviewArtifacts func() error
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

// Значения runnerKind — какой из 8 раннеров (agents.go) сейчас/последним
// активирован для стадии. См. комментарий у поля runnerKind.
const (
	kindPlanning       = "planning"
	kindImplementation = "implementation"
	kindReview         = "review"
	kindAutonomous     = "autonomous"
)

// setRunnerKind фиксирует, каким раннером активирована стадия stageID.
// Вызывается первой строкой в каждом из 8 run*Agent/run*WithFeedback.
func (o *Orchestrator) setRunnerKind(stageID, kind string) {
	o.runnerKind.Store(stageID, kind)
}

// clearRunnerKind удаляет текущий тип раннера при завершении, ошибке или ожидании пользователя.
func (o *Orchestrator) clearRunnerKind(stageID string) {
	o.runnerKind.Delete(stageID)
}

// runnerKindOf возвращает текущий runnerKind стадии либо "" если ни разу не
// выставлялся (или уже снят).
func (o *Orchestrator) runnerKindOf(stageID string) string {
	if v, ok := o.runnerKind.Load(stageID); ok {
		return v.(string)
	}
	return ""
}

// armResumeContext взводит однократный флаг resumeContextOnce для стадии stageID — следующий вызов
// runWithRetry соберёт retry-контекст уже на attempt 0.
func (o *Orchestrator) armResumeContext(stageID string) {
	o.resumeContextOnce.Store(stageID, struct{}{})
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

// recordUsage returns an executor.Config.OnUsage callback bound to one attribution: every runnerFor call
// site and the memory pipeline build their closure through this single seam.
func (o *Orchestrator) recordUsage(stageID, phase, scope string) func(accounting.Observation) {
	return func(obs accounting.Observation) {
		if o.opts.Accounting == nil {
			return
		}
		if err := o.opts.Accounting.Append(obs, stageID, phase, scope); err != nil {
			log.Printf("WARN: accounting: append usage (stage=%s phase=%s scope=%s): %v", stageID, phase, scope, err)
		}
	}
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
			WrapperDir:     executor.WrapperDirFor(opts.Config.Client.Command, opts.WrapperDir, opts.GeneratedAgents),
			Debug:          opts.Debug,
			RunDir:         opts.RunDir,
			Reasoning:      opts.Config.Client.Reasoning,
		})
	}

	// Семафоры на команду строятся из конфигурации стадий: per-stage MaxParallel имеет приоритет над
	// глобальным дефолтом.
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
	o.hooks = opts.Hooks
	o.spawnJSONFix = o.runJSONFixAgent
	o.runVerifyAgent = o.execVerifyAgent
	o.runVerifyShell = runVerifyShellCommand
	o.fingerprintFn = workingTreeFingerprint
	o.runHelperAgent = o.execHelperAgent
	o.mem = memorypipeline.New(memorypipeline.Prompts{
		Reflect:    opts.Prompts.Reflect,
		Aggregate:  opts.Prompts.Aggregate,
		Prioritize: opts.Prompts.Prioritize,
		Update:     opts.Prompts.Update,
	}, memorypipeline.AgentConfig{
		Command:     opts.Config.Client.Command,
		ExtraArgs:   opts.Config.Client.ExtraArgs,
		WrapperDir:  executor.WrapperDirFor(opts.Config.Client.Command, opts.WrapperDir, opts.GeneratedAgents),
		RootDir:     opts.RootDir,
		RunDir:      opts.RunDir,
		IdleTimeout: opts.Config.Executor.IdleTimeout,
		Debug:       opts.Debug,
		OnUsage:     o.recordUsage,
		Reasoning:   opts.Config.Client.Reasoning,
	})
	return o
}

// UIBus returns the UIBus for external subscribers (server, WebSocket).
func (o *Orchestrator) UIBus() *bus.UIBus { return o.ui }

// publishCritical wraps o.critical.Publish, logging instead of silently discarding a drop. Logging exists
// purely so this class of loss is visible during debugging instead of silent.
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

// triggerWithSeq — как Trigger, но дополнительно возвращает seq применённой transition. Остальные ~60 call
// site'ов Trigger в этом не нуждаются и не меняются.
func (o *Orchestrator) triggerWithSeq(stageID string, ev bus.FSMEvent, ctx bus.GuardCtx, reason string) (state.StageStatus, uint64, bool) {
	from, to, seq, ok, err := o.fsm.Apply(stageID, ev, ctx, reason)
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
		o.emitLifecycleTransition(stageID, from, to, seq, ev, reason)
		// runnerKind больше не актуален: стадия завершилась, провалилась или ушла ждать пользователя — во всех
		// случаях раннер, который её вёл, для неё закончил работу.
		if ev == bus.EvComplete || ev == bus.EvFail || ev == bus.EvVerifyFail || ev == bus.EvAskUser {
			o.clearRunnerKind(stageID)
		}
	}
	return to, seq, ok
}

// SetDashboardURL sets the dashboard URL after the server starts listening.
func (o *Orchestrator) SetDashboardURL(url string) { o.opts.DashboardURL = url }

// Run starts the event-driven orchestrator loop.
func (o *Orchestrator) Run(ctx context.Context) (runErr error) {
	ctx, cancel := context.WithCancel(ctx)
	o.cancelRun = cancel
	o.runMu.Lock()
	o.runCtx = ctx
	o.runMu.Unlock()
	// Регистрируем finalizeLifecycle первым: LIFO выполнит terminal emit после cancel и WaitAgents, когда
	// продюсеры уже остановлены.
	defer o.finalizeLifecycle()
	// Persist the terminal boundary after agents have stopped, before lifecycle
	// hooks are flushed. The dashboard remains available for its drain period.
	defer func() {
		if o.terminalFlow == "" {
			return
		}
		var status state.RunStatus
		switch o.terminalFlow {
		case lifecyclehooks.EventFlowFinished:
			status = state.RunStatusFinished
		case lifecyclehooks.EventFlowFailed:
			status = state.RunStatusFailed
		case lifecyclehooks.EventFlowInterrupted:
			status = state.RunStatusInterrupted
		default:
			runErr = errors.Join(runErr, fmt.Errorf("unknown terminal flow event %q", o.terminalFlow))
			return
		}
		if err := o.opts.Store.EndRun(status); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("persist run completion: %w", err))
		}
	}()
	defer o.concurrency.WaitAgents() // выполнится ПОСЛЕ cancel (LIFO) — сначала отмена, потом ожидание
	defer cancel()
	if err := o.opts.Store.BeginRun(); err != nil {
		return fmt.Errorf("persist run start: %w", err)
	}

	if o.opts.Resumed {
		o.emitLifecycle(lifecyclehooks.Event{Type: lifecyclehooks.EventFlowResumed})
	} else {
		o.emitLifecycle(lifecyclehooks.Event{Type: lifecyclehooks.EventFlowStarted})
	}

	// Restore review-pause before scheduling. On a corrupt resuming marker, hold activation and keep the
	// dashboard available for inspection.
	poisoned := false
	if err := o.recoverReviewPause(ctx); err != nil {
		log.Printf("review-pause: recovery error, activation held pending manual intervention: %v", err)
		// A poisoned round must NOT run normal bootstrap at all — activationHeld alone still lets some
		// scheduling side effects through.
		poisoned = errors.Is(err, errReviewPausePoisoned)
	}
	if !poisoned {
		o.startPlanningForPending(ctx)
	}
	o.startQuestionPoller(ctx) // file-based dialog poller

	for {
		select {
		case <-ctx.Done():
			if ferr := o.loadFatal(); ferr != nil {
				o.setTerminalFlow(lifecyclehooks.EventFlowFailed)
				return ferr
			}
			if o.hasFailedStage() {
				o.setTerminalFlow(lifecyclehooks.EventFlowFailed)
			} else {
				o.setTerminalFlow(lifecyclehooks.EventFlowInterrupted)
			}
			return ctx.Err()
		case ev := <-o.critical.Recv():
			if err := o.handleEvent(ctx, ev); err != nil {
				o.setTerminalFlow(lifecyclehooks.EventFlowFailed)
				return err
			}
			if ferr := o.loadFatal(); ferr != nil {
				o.setTerminalFlow(lifecyclehooks.EventFlowFailed)
				return ferr
			}
			if o.shouldExit() {
				// Serialize finalization with PauseFlow under flowPauseMu: either finalizing or the review transaction
				// wins.
				o.flowPauseMu.Lock()
				if !o.reviewTxnActive() {
					o.finalizing.Store(true)
					o.flowPauseMu.Unlock()
					o.runEndOfRunMemory(ctx)
					snap := o.opts.Store.Snapshot()
					if snap.AllDone() {
						o.setTerminalFlow(lifecyclehooks.EventFlowFinished)
					} else {
						o.setTerminalFlow(lifecyclehooks.EventFlowFailed)
					}
					return nil
				}
				o.flowPauseMu.Unlock()
			}
		}
	}
}

// setTerminalFlow фиксирует финальное flow-событие выхода Run.
func (o *Orchestrator) setTerminalFlow(ev lifecyclehooks.EventType) {
	o.terminalFlow = ev
}

// hasFailedStage — есть ли в снапшоте Failed-стадия.
func (o *Orchestrator) hasFailedStage() bool {
	for _, st := range o.opts.Store.Snapshot().Stages {
		if st.Status == state.StatusFailed {
			return true
		}
	}
	return false
}

// finalizeLifecyclePollerWait — safety cap на ожидание завершения горутины question poller.
const finalizeLifecyclePollerWait = 5 * time.Second

// finalizeLifecycle эмитит терминальное flow-событие и bounded-ждёт опустошения очередей dispatcher.
func (o *Orchestrator) finalizeLifecycle() {
	if o.pollerDone != nil {
		select {
		case <-o.pollerDone:
		case <-time.After(finalizeLifecyclePollerWait):
			log.Printf("WARN: question poller did not finish within %s before terminal lifecycle event", finalizeLifecyclePollerWait)
		}
	}
	if o.terminalFlow == "" || o.hooks == nil {
		return
	}
	o.emitLifecycle(lifecyclehooks.Event{Type: o.terminalFlow})
	if !o.hooks.Flush(lifecyclehooks.FlushTimeout) {
		log.Printf("WARN: lifecycle hooks flush timed out after %s", lifecyclehooks.FlushTimeout)
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

// onAgentCompleted consumes completions from runWithRetry and script stages. It applies phase transitions
// and unblocks dependents.
func (o *Orchestrator) onAgentCompleted(ctx context.Context, ev bus.Event) error {
	agentType, _ := ev.Data.(string)
	current := o.currentStatus(ev.StageID)

	// Check all phases for fresh questions.
	if current != state.StatusAwaitingUserInput && o.hasOpenQuestion(ev.StageID, agentType) {
		o.preAskPhase.Store(ev.StageID, agentType)
		o.Trigger(ev.StageID, bus.EvAskUser, bus.GuardCtx{Phase: agentType}, "")
		return nil
	}

	switch agentType {
	case phasePlanning:
		// Ignore stale completion if stage already left planning state. Rejecting it here would strand planning
		// forever with no agent process left to retry.
		if current != state.StatusPlanning && current != state.StatusRetrying && current != state.StatusAwaitingUserInput {
			return nil
		}
		o.Trigger(ev.StageID, bus.EvPlanReady, bus.GuardCtx{}, "")
		// auto_approve: true on the stage config wins regardless of
		// dashboard/--require-approval — checked before the headless branch.
		if stage := o.graph.Stage(ev.StageID); stage != nil && o.autoApproveIfConfigured(ctx, *stage) {
			return nil
		}
		// Headless: нет дашборда → никто не нажмёт Approve. RequireApproval=true → fail-fast с понятным
		// сообщением. RequireApproval=false (дефолт) → авто-апрув, flow идёт дальше.
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
		// Revise мог опередить естественное завершение агента без SIGINT; перезапускаем revising-стадию с
		// сохранённым feedback.
		if current == state.StatusRevising {
			stage := o.graph.Stage(ev.StageID)
			if stage == nil {
				return nil
			}
			if agentType == phaseAutonomous {
				o.spawnKind(ctx, *stage, kindAutonomous, o.runAutonomousWithFeedback)
			} else {
				o.spawnKind(ctx, *stage, kindImplementation, o.runImplementationWithFeedback)
			}
			return nil
		}
		// Фаза завершена: implementation-агент дошёл до конца, либо
		// autonomous-трек написал execution_summary.md → переводим в done.
		o.completeStage(ctx, ev.StageID, current, "")
	case phaseScript:
		// Script-стадия завершилась (runScriptStage): нет revising-гонки (interruptChans не регистрируется вне
		// runWithRetry, Revise() на script-стадию не осмыслен) — просто done, как остальные фазы.
		o.completeStage(ctx, ev.StageID, current, "")
	case phaseReview:
		// Standalone review публикует собственный completion; встроенный review завершается через implementation
		// и сюда не попадает.
		if current == state.StatusRunning || current == state.StatusRevising {
			o.completeStage(ctx, ev.StageID, current, "review complete")
		}
	default:
		// unknown agent type: no status change needed
	}
	return nil
}

// completeStage finalizes a stage that genuinely reached done.
func (o *Orchestrator) completeStage(ctx context.Context, stageID string, current state.StageStatus, reason string) {
	if current != state.StatusRunning && current != state.StatusRetrying && current != state.StatusAwaitingUserInput {
		return
	}
	// current — снимок статуса, прочитанный вызывающим ДО этого момента: между чтением и CAS сюда мог влезть
	// Pause/Revise на другой горутине и увести стадию из running/retrying.
	if _, ok := o.Trigger(stageID, bus.EvComplete, bus.GuardCtx{}, reason); !ok {
		return
	}
	// script_after runs first, and reflection only starts once it returns — so a stage's reflect capture
	// always sees after.log if the stage has one.
	o.maybeRunAfterHookThen(ctx, stageID, func(c context.Context) { o.maybeRunReflection(c, stageID) })
	o.failBlockedStages()
	o.startPlanningForUnblocked(ctx)
	o.startReadyStages(ctx)
	o.tryActivatePrePlanned(ctx)
}

// onUserAnswered resumes a stage that was paused on awaiting_user_input. If the agent exited before the
// user answered, we restart it here.
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
		o.spawnKind(ctx, *stage, kindPlanning, o.runPlanningAgent)
	case phaseImplementation:
		o.Trigger(ev.StageID, bus.EvUserAnswered, bus.GuardCtx{Phase: phaseImplementation}, "")
		o.spawnKind(ctx, *stage, kindImplementation, o.runImplementationAgent)
	case phaseReview:
		o.Trigger(ev.StageID, bus.EvUserAnswered, bus.GuardCtx{Phase: phaseReview}, "")
		o.spawnKind(ctx, *stage, kindReview, o.runReviewAgent)
	case phaseAutonomous:
		o.Trigger(ev.StageID, bus.EvUserAnswered, bus.GuardCtx{Phase: phaseAutonomous}, "")
		o.spawnKind(ctx, *stage, kindAutonomous, o.runAutonomousAgent)
	default:
		return fmt.Errorf("unexpected phase: %q", phase)
	}
	return nil
}

func (o *Orchestrator) currentStatus(id string) state.StageStatus {
	return o.opts.Store.Get(id)
}

// reviewTxnActive reports whether a review-pause marker is currently present.
func (o *Orchestrator) reviewTxnActive() bool { return o.reviewMarker.Load() != nil }

// activationBlocked reports whether new stage activations must be held. Already-running stages are never
// affected — callers only skip the transition that would START a new stage.
func (o *Orchestrator) activationBlocked() bool { return o.activationHeld.Load() }

// resolvePlanSource превращает путь stage.Plan в путь к существующему файлу. Plan — путь к существующему
// файлу плана.
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

// InterruptChanForTest возвращает канал прерывания текущей попытки RunAgent для стадии, если он
// зарегистрирован.
func InterruptChanForTest(o *Orchestrator, stageID string) (chan struct{}, bool) {
	ch, ok := o.interruptChans.Load(stageID)
	if !ok {
		return nil, false
	}
	return ch.(chan struct{}), true
}
