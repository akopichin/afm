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
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
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
	// SupervisorRunner — runner для вызовов Supervisor.EvaluateStage.
	// nil = Supervisor отключён глобально (DetermineStagePhases всегда вернёт базовые фазы).
	SupervisorRunner executor.Runner
}

// Orchestrator manages the full lifecycle of a flow run via event loop.
type Orchestrator struct {
	opts     Options
	graph    *Graph
	runner   executor.Runner
	critical *CriticalBus
	ui       *UIBus
	fsm      *FSM
	sems     map[string]interface {
		acquire()
		release()
	} // per-command semaphores
	activeAgents sync.Map // stageID → struct{}: set while an agent goroutine runs
	// preAskPhase хранит корректную фазу в момент EvAskUser (stageID → phase string).
	// Используется при EvUserAnswered вместо фазы из имени файла вопроса:
	// агент может написать неправильное имя фазы (напр. "review" вместо "planning"),
	// что ломает phaseDispatch и уводит FSM в wrong state.
	preAskPhase sync.Map
	// violationCache кешует stat .jsonl-файлов для detectDialogViolation.
	// Доступен только из горутины поллера — мьютекс не нужен.
	violationCache map[string]violationCacheEntry // key: путь к .jsonl
	// supervisor оценивает, можно ли выполнить стадию автономно.
	// nil, если SupervisorRunner не задан в Options.
	supervisor *Supervisor
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

	// agentWG учитывает все агентские горутины, запущенные через spawnAgent.
	// waitAgents дожидается его опустошения на выходе из Run — иначе горутины,
	// ещё пишущие в Store, переживают Run и рискуют использовать Store после Close.
	agentWG sync.WaitGroup

	// runMu/runCtx хранят долгоживущий контекст event loop (см. Run), который
	// HTTP-инициированные Approve/Revise/Retry подставляют вместо request-ctx
	// перед спавном агента (см. runContext). net/http отменяет r.Context() сразу
	// после возврата хэндлера — если передать его в spawnAgent как есть, только
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
	critical := NewCriticalBus(16)
	ui := NewUIBus()

	r := opts.Runner
	if r == nil {
		r = executor.New(executor.Config{
			Command:     opts.Config.Client.Command,
			ExtraArgs:   opts.Config.Client.ExtraArgs,
			IdleTimeout: opts.Config.Executor.IdleTimeout,
			OnAction:    uiActionPublisher(ui, ""),
			WrapperDir:  wrapperDirFor(opts.Config.Client.Command, opts.WrapperDir, opts.GeneratedAgents),
		})
	}

	// Build per-command semaphores from stage configs.
	sems := make(map[string]interface {
		acquire()
		release()
	})
	globalMP := opts.Config.Executor.MaxParallel
	for _, s := range opts.Stages {
		cmd := s.Command
		if cmd == "" {
			cmd = opts.Config.Client.Command
		}
		if _, exists := sems[cmd]; exists {
			continue
		}
		mp := s.MaxParallel
		if mp <= 0 {
			mp = globalMP
		}
		if mp > 0 {
			sems[cmd] = semChan(make(chan struct{}, mp))
		} else {
			sems[cmd] = semNop{}
		}
	}

	// Supervisor включается только если задан SupervisorRunner; иначе
	// DetermineStagePhases всегда возвращает базовые фазы.
	var sup *Supervisor
	if opts.SupervisorRunner != nil {
		sup = NewSupervisor(opts.SupervisorRunner)
	}

	return &Orchestrator{
		opts:           opts,
		graph:          NewGraph(opts.Stages),
		runner:         r,
		critical:       critical,
		ui:             ui,
		fsm:            NewFSM(opts.Store),
		sems:           sems,
		violationCache: make(map[string]violationCacheEntry),
		supervisor:     sup,
		maxRetries:     MaxRetries,
		retryBackoff:   RetryBackoff,
	}
}

// UIBus returns the UIBus for external subscribers (server, WebSocket).
func (o *Orchestrator) UIBus() *UIBus { return o.ui }

// Trigger applies an FSM event to transition a stage's status.
// Returns the new status and whether the transition was applied.
func (o *Orchestrator) Trigger(stageID string, ev FSMEvent, ctx GuardCtx, reason string) (state.StageStatus, bool) {
	to, ok, err := o.fsm.Apply(stageID, ev, ctx, reason)
	if err != nil {
		var se *StorageError
		if errors.As(err, &se) {
			// Storage-fatal: authoritative log write failed — нельзя продолжать
			// принимать решения против сломанного лога. Завершаем run.
			log.Printf("FATAL: storage failure applying %s/%s: %v", stageID, ev, err)
			o.setFatal(err)
			return o.currentStatus(stageID), false
		}
		// Не-storage ошибка (напр. ErrNoRule — неизвестное событие, баг в коде):
		// логируем и роняем переход, но НЕ валим весь run.
		log.Printf("CRITICAL: FSM Apply %s/%s: %v", stageID, ev, err)
		return o.currentStatus(stageID), false
	}
	if ok {
		ev := Event{Type: EventStageStatusChanged, StageID: stageID, Data: string(to)}
		o.ui.Publish(ev)
		// Wake the event loop so it can check shouldExit(). Non-blocking to avoid deadlock.
		select {
		case o.critical.ch <- ev:
		default:
		}
	}
	return to, ok
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
	defer o.waitAgents() // выполнится ПОСЛЕ cancel (LIFO) — сначала отмена, потом ожидание
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
func (o *Orchestrator) handleEvent(ctx context.Context, ev Event) error {
	switch ev.Type {
	case EventAgentCompleted:
		return o.onAgentCompleted(ctx, ev)
	case EventUserAnswered:
		return o.onUserAnswered(ctx, ev)
	}
	return nil
}

func (o *Orchestrator) onAgentCompleted(ctx context.Context, ev Event) error {
	agentType, _ := ev.Data.(string)
	current := o.currentStatus(ev.StageID)

	// Open-question gate: if the agent finished but the user has not yet
	// answered an ask_user question, hold the stage in awaiting_user_input.
	// The stage resumes on EventUserAnswered.
	if o.hasOpenQuestion(ev.StageID, agentType) {
		// agentType здесь — реальная фаза от executor, не из имени файла вопроса.
		o.preAskPhase.Store(ev.StageID, agentType)
		o.Trigger(ev.StageID, EvAskUser, GuardCtx{Phase: agentType}, "")
		return nil
	}

	switch agentType {
	case phasePlanning:
		// Ignore stale completion if stage already left planning state
		// (e.g. approved, done, or restarted by onUserAnswered).
		if current != state.StatusPlanning && current != state.StatusRetrying {
			return nil
		}
		o.Trigger(ev.StageID, EvPlanReady, GuardCtx{}, "")
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
		// Фаза завершена: implementation-агент дошёл до конца, либо
		// autonomous-трек написал execution_summary.md → переводим в done.
		if current != state.StatusRunning && current != state.StatusRetrying {
			return nil
		}
		o.Trigger(ev.StageID, EvComplete, GuardCtx{}, "")
		o.failBlockedStages()
		o.startPlanningForUnblocked(ctx)
		o.startReadyStages(ctx)
		o.tryActivatePrePlanned(ctx)
	default:
		// review or unknown agent type: no status change needed
	}
	return nil
}

// onUserAnswered resumes a stage that was paused on awaiting_user_input.
// If the agent is still running (its bash loop is waiting for answer.json),
// NotifyAnswer already transitioned the status — this is a no-op.
// If the agent exited before the user answered, we restart it here.
func (o *Orchestrator) onUserAnswered(ctx context.Context, ev Event) error {
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
		o.Trigger(ev.StageID, EvUserAnswered, GuardCtx{Phase: phasePlanning}, "")
		o.spawnAgent(ctx, *stage, o.runPlanningAgent)
	case phaseImplementation:
		o.Trigger(ev.StageID, EvUserAnswered, GuardCtx{Phase: phaseImplementation}, "")
		o.spawnAgent(ctx, *stage, o.runImplementationAgent)
	case phaseReview:
		o.Trigger(ev.StageID, EvUserAnswered, GuardCtx{Phase: phaseReview}, "")
		o.spawnAgent(ctx, *stage, o.runReviewAgent)
	case phaseAutonomous:
		o.Trigger(ev.StageID, EvUserAnswered, GuardCtx{Phase: phaseAutonomous}, "")
		o.spawnAgent(ctx, *stage, o.runAutonomousAgent)
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
