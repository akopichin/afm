package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/state"
)

// phasePlanning is the agent phase name used for planning agents.
const phasePlanning = "planning"

const phaseImplementation = "implementation"
const phaseReview = "review"

// phaseAutonomous — фаза autonomous-трека (супервизор решил can_execute_autonomously).
// Как implementation даёт running→done, но без plan.md/approval: агент со скиллами
// пишет execution_summary.md. Фаза ДИАЛОГОВАЯ — скилл может спрашивать пользователя
// через тот же file-based dialog protocol (вопросы autonomous_execution.q<N>.*).
const phaseAutonomous = "autonomous_execution"
const keyAnswer = "answer"
const keyID = "id"
const keyPhase = "phase"

const planningContract = `## Output Contract (mandatory)
The plan MUST contain sections: "## Tasks", "## Assumptions", "## Acceptance Criteria".`

const sectionAssumptions = "Assumptions"

var requiredPlanSections = []string{"Tasks", sectionAssumptions, "Acceptance Criteria"}

// semNop is a no-op semaphore used when MaxParallel is 0 (unlimited).
type semNop struct{}

func (semNop) acquire() {}
func (semNop) release() {}

// semChan is a real semaphore backed by a buffered channel.
type semChan chan struct{}

func (s semChan) acquire() { s <- struct{}{} }
func (s semChan) release() { <-s }

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
	RequireApproval bool            // headless: fail instead of auto-approve on awaiting_approval
	// SupervisorRunner — runner для вызовов Supervisor.EvaluateStage.
	// nil = Supervisor отключён глобально (DetermineStagePhases всегда вернёт базовые фазы).
	SupervisorRunner executor.Runner
}

// violationCacheEntry хранит stat-данные для одного .jsonl файла.
// Используется в detectDialogViolation чтобы не перечитывать неизменившиеся файлы.
type violationCacheEntry struct {
	size  int64
	mtime time.Time
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

// agentDrainTimeout — сколько ждём завершения агентских горутин на выходе Run,
// прежде чем вернуться (агентские процессы уже убиты отменой ctx; ожидание
// защищает Store от использования после Close).
const agentDrainTimeout = 10 * time.Second

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

// FailStage marks a stage as failed with a reason.
func (o *Orchestrator) FailStage(stageID, reason string) {
	o.Trigger(stageID, EvFail, GuardCtx{}, reason)
	o.failBlockedStages()
}

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

// NotifyAnswer is called by the HTTP handler when the user submits an answer.
// If the agent goroutine is still running (its bash loop is awaiting
// answer.json), we only transition the status — the bash loop will detect the
// file and continue without a restart. If the goroutine has exited, we publish
// to the critical bus so onUserAnswered can restart it.
func (o *Orchestrator) NotifyAnswer(stageID, phase, qID, answer string, fromOptions bool) error {
	if o.isAgentActive(stageID) {
		guardPhase := o.popPreAskPhase(stageID, phase)
		o.Trigger(stageID, EvUserAnswered, GuardCtx{Phase: guardPhase}, "")
		o.ui.Publish(Event{Type: EventUserAnswered, StageID: stageID, Data: map[string]any{
			keyID: qID, keyPhase: phase, keyAnswer: answer,
		}})
		return nil
	}
	return o.critical.Publish(context.Background(), Event{
		Type:    EventUserAnswered,
		StageID: stageID,
		Data:    map[string]any{keyID: qID, "phase": phase, keyAnswer: answer},
	})
}

// wrapperDirFor возвращает wrapper-dir для команды cmd: для generated-команд
// (autoShim) — opts.WrapperDir, чтобы сгенерированный скрипт резолвился на PATH;
// для остальных (включая claude) — пусто (используется реальный бинарник).
func wrapperDirFor(cmd string, wrapperDir string, generated map[string]bool) string {
	if generated[cmd] {
		return wrapperDir
	}
	return ""
}

// runnerFor returns the appropriate Runner for a stage's phase.
// For interactive stages it generates a session id and returns an executor
// configured with --session-id / --resume and AFM_STAGE_DIR env.
func (o *Orchestrator) runnerFor(s flow.Stage, phase string) executor.Runner {
	if !s.Interactive {
		if s.Command == "" {
			return o.runner
		}
		cfg := executor.Config{
			Command:     s.Command,
			IdleTimeout: o.opts.Config.Executor.IdleTimeout,
			OnAction:    uiActionPublisher(o.ui, s.ID),
			WrapperDir:  wrapperDirFor(s.Command, o.opts.WrapperDir, o.opts.GeneratedAgents),
		}
		// Autonomous-фаза диалоговая: агенту нужен AFM_STAGE_DIR, чтобы писать
		// question.json и писать execution_summary.md в каталог стадии.
		if phase == phaseAutonomous {
			cfg.StageDir = filepath.Join(o.opts.RunDir, s.ID)
		}
		return executor.New(cfg)
	}

	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	resume := sessionExists(stageDir, phase)
	sessionID, err := loadOrCreateSession(stageDir, phase)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: interactive stage %q: session failed: %v; using non-interactive runner\n", s.ID, err)
		return o.runnerForFallback(s)
	}

	cmd := s.Command
	if cmd == "" {
		cmd = o.opts.Config.Client.Command
	}
	// Interactive stages always need the claude stream-json flags (incl. --verbose,
	// afm bug #1.1). ResolveArgs prepends defaults and dedups user overrides.
	extraArgs := executor.ResolveArgs(o.opts.Config.Client.ExtraArgs)
	return executor.New(executor.Config{
		Command:     cmd,
		ExtraArgs:   extraArgs,
		IdleTimeout: o.opts.Config.Executor.IdleTimeout,
		OnAction:    uiActionPublisher(o.ui, s.ID),
		SessionID:   sessionID,
		Resume:      resume,
		StageDir:    stageDir,
		WrapperDir:  wrapperDirFor(cmd, o.opts.WrapperDir, o.opts.GeneratedAgents),
	})
}

func (o *Orchestrator) runnerForFallback(s flow.Stage) executor.Runner {
	if s.Command == "" {
		return o.runner
	}
	return executor.New(executor.Config{
		Command:     s.Command,
		IdleTimeout: o.opts.Config.Executor.IdleTimeout,
		OnAction:    uiActionPublisher(o.ui, s.ID),
		WrapperDir:  wrapperDirFor(s.Command, o.opts.WrapperDir, o.opts.GeneratedAgents),
	})
}

func uiActionPublisher(ui *UIBus, stageID string) func(string, string) {
	return func(tool, detail string) {
		ui.Publish(Event{Type: EventAgentAction, StageID: stageID, Data: map[string]string{
			"tool":   tool,
			"detail": detail,
		}})
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

// startQuestionPoller launches a goroutine that scans active stage directories
// every second for new *.question.json files (file-based dialog protocol).
func (o *Orchestrator) startQuestionPoller(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		processed := map[string]bool{} // "stageID|phase|id" → true
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				o.pollQuestions(processed)
			}
		}
	}()
}

// pollQuestions scans each active stage directory for unanswered question files.
// For each new file: writes it to dialog.jsonl (for UI history) and publishes
// EventAskUser to transition the stage to awaiting_user_input.
func (o *Orchestrator) pollQuestions(processed map[string]bool) {
	snap := o.opts.Store.Snapshot()
	for stageID, st := range snap.Stages {
		switch st.Status {
		case state.StatusPlanning, state.StatusRunning, state.StatusRevising,
			state.StatusRetrying, state.StatusAwaitingUserInput:
		default:
			continue
		}
		stageDir := filepath.Join(o.opts.RunDir, stageID)
		questions, err := mcp.FindUnansweredQuestions(stageDir)
		if err != nil {
			continue
		}
		for _, q := range questions {
			key := stageID + "|" + q.Phase + "|" + q.ID
			if processed[key] {
				continue
			}
			processed[key] = true
			// Write to dialog.jsonl for history (idempotent via FindEntry check).
			dialogPath := filepath.Join(stageDir, q.Phase+".dialog.jsonl")
			if e, _ := mcp.FindEntry(dialogPath, q.ID); e == nil {
				_ = mcp.AppendQuestion(dialogPath, mcp.Question{
					ID:          q.ID,
					Question:    q.Question,
					Options:     q.Options,
					AllowCustom: q.AllowCustom,
				})
			}
			// Notify UI and transition stage status.
			o.ui.Publish(Event{
				Type:    EventAskUser,
				StageID: stageID,
				Data: map[string]any{
					keyID: q.ID, keyPhase: q.Phase, "question": q.Question,
					"options": q.Options, "allow_custom": q.AllowCustom,
				},
			})
			// Сохраняем реальную фазу ДО перехода в awaiting_user_input.
			// Фаза из имени файла (q.Phase) может быть неправильной (агент написал
			// "review" вместо "planning") — при EvUserAnswered используем сохранённую
			// фазу, а не ту что в файле вопроса.
			o.preAskPhase.Store(stageID, o.correctPhaseForState(o.currentStatus(stageID), q.Phase))
			o.Trigger(stageID, EvAskUser, GuardCtx{Phase: q.Phase}, "")
		}
		// No open question in stageDir: if this is an interactive stage, check
		// whether the agent wrote one elsewhere (GLM-4.7 hallucination bug: agent
		// constructs path from CWD instead of reading $AFM_STAGE_DIR).
		// Auto-relocate the misplaced file so the dialog becomes visible in the UI.
		if len(questions) == 0 {
			if stage := o.graph.Stage(stageID); stage != nil && stage.Interactive {
				o.relocateMisplacedQuestions(stageDir)
			}
		}
	}
}

// detectDialogViolation scans the agent's stream-json logs (<phase>.jsonl) for a
// Write of a *.question.json file OUTSIDE the stage directory. Such a write
// violates the file-based dialog contract: the poller and dashboard only look
// inside stageDir, so a misplaced question hangs the stage forever. Returns a
// human-readable reason when a violation is found.
//
// Результат каждого файла кешируется по (size, mtime): если файл не изменился
// с прошлого тика, он не перечитывается. Метод вызывается только из поллера —
// синхронизация не нужна.
func (o *Orchestrator) detectDialogViolation(stageDir string) (string, bool) {
	phases := []string{phasePlanning, phaseImplementation, phaseReview}
	if isAutonomousStage(stageDir) {
		phases = append(phases, phaseAutonomous)
	}
	for _, phase := range phases {
		jsonlPath := filepath.Join(stageDir, jsonlFileForPhase(phase))
		info, err := os.Stat(jsonlPath)
		if err != nil {
			continue // файл не существует — нарушений нет
		}
		cached, ok := o.violationCache[jsonlPath]
		if ok && cached.size == info.Size() && cached.mtime.Equal(info.ModTime()) {
			continue // не изменился с прошлого тика
		}
		for _, f := range executor.WrittenFiles(jsonlPath) {
			if !strings.HasSuffix(filepath.Base(f), ".question.json") {
				continue
			}
			if !pathInside(f, stageDir) {
				return fmt.Sprintf("dialog protocol violation: question written to %s, expected %s", f, stageDir), true
			}
		}
		o.violationCache[jsonlPath] = violationCacheEntry{size: info.Size(), mtime: info.ModTime()}
	}
	return "", false
}

// jsonlFileForPhase возвращает имя JSONL-лога для фазы.
// Autonomous-фаза логируется в "autonomous.log" → "autonomous.jsonl",
// а не в "autonomous_execution.jsonl" (как следовало бы из имени фазы).
func jsonlFileForPhase(phase string) string {
	if phase == phaseAutonomous {
		return "autonomous.jsonl"
	}
	return phase + ".jsonl"
}

// relocateMisplacedQuestions ищет question.json файлы, записанные агентом вне stageDir,
// и автоматически перемещает их внутрь. Для каждого перемещённого файла создаётся
// dangling-симлинк <wrongDir>/answer.json → <stageDir>/answer.json, чтобы агентский
// bash-polling-loop нашёл ответ по своему (неверному) пути.
//
// Это защита от GLM-4.7 bug: модель конструирует путь из CWD вместо $AFM_STAGE_DIR.
func (o *Orchestrator) relocateMisplacedQuestions(stageDir string) {
	phases := []string{phasePlanning, phaseImplementation, phaseReview}
	if isAutonomousStage(stageDir) {
		phases = append(phases, phaseAutonomous)
	}
	for _, phase := range phases {
		jsonlPath := filepath.Join(stageDir, jsonlFileForPhase(phase))
		for _, f := range executor.WrittenFiles(jsonlPath) {
			if !strings.HasSuffix(filepath.Base(f), ".question.json") {
				continue
			}
			if pathInside(f, stageDir) {
				continue // уже в правильном месте
			}
			// Файл написан в неправильную директорию.
			if _, err := os.Stat(f); err != nil {
				continue // файл не существует — агент ещё не дошёл до записи
			}
			dst := filepath.Join(stageDir, filepath.Base(f))
			if _, err := os.Stat(dst); err == nil {
				continue // уже скопирован ранее
			}
			data, err := os.ReadFile(f)
			if err != nil {
				log.Printf("WARN: relocate question %s: read: %v", f, err)
				continue
			}
			if err := os.WriteFile(dst, data, 0644); err != nil {
				log.Printf("WARN: relocate question %s → %s: write: %v", f, dst, err)
				continue
			}
			// Создаём симлинк wrongDir/answer.json → stageDir/answer.json (dangling),
			// чтобы агентский polling-loop нашёл ответ по своему неверному пути.
			answerBase := strings.TrimSuffix(filepath.Base(f), ".question.json") + ".answer.json"
			wrongAnswer := filepath.Join(filepath.Dir(f), answerBase)
			rightAnswer := filepath.Join(stageDir, answerBase)
			if _, err := os.Lstat(wrongAnswer); err != nil {
				_ = os.MkdirAll(filepath.Dir(wrongAnswer), 0755)
				_ = os.Symlink(rightAnswer, wrongAnswer)
			}
			log.Printf("INFO: relocated misplaced question %s → %s (symlink answer)", f, dst)
		}
	}
}

// pathInside reports whether file is located inside dir. Both are resolved to
// absolute paths the same way (filepath.Abs, no symlink resolution), so they
// stay in a consistent form — the agent's Write paths and stageDir both originate
// from the same source (AFM_STAGE_DIR), so a consistent resolution is
// sufficient and avoids EvalSymlinks' failure on not-yet-existing files.
func pathInside(file, dir string) bool {
	absFile, err := filepath.Abs(file)
	if err != nil {
		absFile = filepath.Clean(file)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = filepath.Clean(dir)
	}
	if absDir != string(filepath.Separator) {
		absDir += string(filepath.Separator)
	}
	return strings.HasPrefix(absFile+string(filepath.Separator), absDir)
}

// approveStage долговечно переводит стадию из awaiting_approval и запускает
// побочные эффекты. Вызывается СИНХРОННО (из HTTP-обработчика и из headless
// auto-approve), поэтому переход фиксируется в Store до возврата — краш после
// approve не теряет интент (recovery резюмит ready/done). Идемпотентна: если
// стадия уже не в awaiting_approval, только до-запускает побочные эффекты.
func (o *Orchestrator) approveStage(ctx context.Context, stageID string) {
	if o.currentStatus(stageID) == state.StatusAwaitingApproval {
		stage := o.graph.Stage(stageID)
		if stage != nil && !stage.HasAgent(flow.AgentImplementation) {
			o.Trigger(stageID, EvComplete, GuardCtx{}, "planning-only stage")
		} else {
			o.Trigger(stageID, EvApprove, GuardCtx{}, "")
		}
	}
	o.startPlanningForUnblocked(ctx)
	o.startReadyStages(ctx)
	o.tryActivatePrePlanned(ctx)
}

// runContext returns the long-lived run-scoped context for spawning agents from
// HTTP-initiated actions. Agents MUST NOT inherit the HTTP request context: net/http
// cancels it when the handler returns, which would kill the just-spawned agent.
// Falls back to context.WithoutCancel(fallback) if Run hasn't set runCtx yet
// (tiny window before Run starts) so the agent still isn't bound to the request.
func (o *Orchestrator) runContext(fallback context.Context) context.Context {
	o.runMu.Lock()
	defer o.runMu.Unlock()
	if o.runCtx != nil {
		return o.runCtx
	}
	return context.WithoutCancel(fallback)
}

// Approve approves a stage plan (синхронно и долговечно). ctx приходит от
// вызывающей стороны (HTTP request context у dashboard-инициированного approve
// или Run ctx у headless auto-approve) — подставляем run ctx перед спавном
// агента, см. runContext.
func (o *Orchestrator) Approve(ctx context.Context, stageID string) error {
	o.approveStage(o.runContext(ctx), stageID)
	return nil
}

// Revise sends feedback to re-plan a stage (синхронно и долговечно): переход
// в revising фиксируется в Store до возврата — краш после Revise не теряет
// интент (recovery резюмит revising через тот же путь, что и planning).
func (o *Orchestrator) Revise(reqCtx context.Context, stageID, feedback string) error {
	if o.currentStatus(stageID) != state.StatusAwaitingApproval {
		return nil
	}

	if _, ok := o.Trigger(stageID, EvRevise, GuardCtx{}, feedback); !ok {
		return nil
	}

	stageDir := filepath.Join(o.opts.RunDir, stageID)
	if _, err := state.VersionPlan(stageDir); err != nil {
		return fmt.Errorf("version plan for %s: %w", stageID, err)
	}
	if err := state.SaveFeedback(stageDir, feedback); err != nil {
		return fmt.Errorf("save feedback for %s: %w", stageID, err)
	}

	if stage := o.graph.Stage(stageID); stage != nil {
		// Спавним под run ctx, а не reqCtx: HTTP-хэндлер отменит reqCtx сразу
		// после возврата ответа, и агент был бы убит немедленно (см. runContext).
		o.spawnAgent(o.runContext(reqCtx), *stage, o.runPlanningWithFeedback)
	}
	return nil
}

// Retry retries a failed stage by transitioning it to pending and restarting
// (синхронно и долговечно).
func (o *Orchestrator) Retry(ctx context.Context, stageID string) error {
	o.retryStage(o.runContext(ctx), stageID)
	return nil
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

// hasOpenQuestion reports whether stageDir contains a *.question.json file
// for the given phase that has no corresponding *.answer.json yet.
func (o *Orchestrator) hasOpenQuestion(stageID, phase string) bool {
	if phase == "" {
		return false
	}
	questions, err := mcp.FindUnansweredQuestions(filepath.Join(o.opts.RunDir, stageID))
	if err != nil {
		return false
	}
	for _, q := range questions {
		if q.Phase == phase {
			return true
		}
	}
	return false
}

// correctPhaseForState возвращает корректную фазу для возврата из awaiting_user_input,
// основываясь на текущем состоянии FSM, а не на фазе из имени файла вопроса.
// Агент может написать неправильное имя фазы (напр. "review" вместо "planning"),
// поэтому мы дублируем правило phaseDispatch на основе реального состояния:
// planning/revising → phasePlanning, всё остальное → фаза из файла (обычно корректна).
func (o *Orchestrator) correctPhaseForState(current state.StageStatus, filePhase string) string {
	if current == state.StatusPlanning || current == state.StatusRevising {
		return phasePlanning
	}
	return filePhase
}

// popPreAskPhase читает и удаляет сохранённую фазу для стейджа.
// Если запись отсутствует (напр. перезапуск afm без перехода через EvAskUser),
// возвращает fallback — фазу из имени файла вопроса.
func (o *Orchestrator) popPreAskPhase(stageID, fallback string) string {
	if v, ok := o.preAskPhase.LoadAndDelete(stageID); ok {
		return v.(string)
	}
	return fallback
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

// retryStage долговечно переводит проваленную стадию из failed и перезапускает
// её (планирование или implementation, в зависимости от наличия plan.md).
// Вызывается СИНХРОННО из Retry (HTTP-обработчик) — переход в Store фиксируется
// до возврата, краш после Retry не теряет интент (recovery резюмит по логу).
func (o *Orchestrator) retryStage(ctx context.Context, stageID string) {
	current := o.currentStatus(stageID)

	if current != state.StatusFailed {
		return
	}

	stage := o.graph.Stage(stageID)
	if stage == nil {
		return
	}

	// Manual retry of an interactive stage must start a fresh Claude session:
	// a leftover <phase>.session.json may reference a conversation that was
	// never created (phantom), which makes claude fail with "No conversation
	// found". Clear all phase sessions for this stage.
	//
	// Also truncate <phase>.jsonl: detectDialogViolation re-scans the raw
	// stream-json log every poll tick, and a *.question.json Write from a
	// previous (violating) run would otherwise re-fire instantly and make the
	// stage un-retryable. Truncating here (before re-activation) is race-free
	// because the poller skips stages in non-active states.
	if stage.Interactive {
		stageDir := filepath.Join(o.opts.RunDir, stageID)
		for _, ph := range []string{phasePlanning, phaseImplementation, phaseReview} {
			_ = os.Remove(sessionFile(stageDir, ph))
			_ = os.Truncate(filepath.Join(stageDir, ph+".jsonl"), 0)
		}
	}

	if _, ok := o.Trigger(stageID, EvManualRetry, GuardCtx{}, ""); !ok {
		return
	}

	// Autonomous-стадия (супервизор ранее выбрал автономный трек — на диске лежит
	// autonomous.flag): retry чтит это решение и перезапускает автономный агент
	// напрямую, а не «сваливается» в planning. Супервизор повторно НЕ опрашивается —
	// симметрично resume-on-restart в recovery.go, который тоже чтит флаг. Переход
	// pending → ready → running зеркалит ветку «plan.md уже есть» ниже (EvReady →
	// EvStartRun), только агент — автономный (без plan.md/approval).
	if isAutonomousStage(filepath.Join(o.opts.RunDir, stageID)) {
		o.Trigger(stageID, EvReady, GuardCtx{}, "manual retry: autonomous")
		// CAS-guard на EvStartRun — как в остальных spawn-путях (нет двойного запуска).
		if _, ok := o.Trigger(stageID, EvStartRun, GuardCtx{}, ""); !ok {
			return
		}
		o.spawnAgent(ctx, *stage, o.runAutonomousAgent)
		o.startReadyStages(ctx)
		return
	}

	if !stage.NeedsPlanning() {
		stageDir := filepath.Join(o.opts.RunDir, stageID)
		planPath := filepath.Join(stageDir, "plan.md")
		if _, err := os.Stat(planPath); err != nil {
			// plan.md not yet on disk — try to copy it from stage.Plan source.
			if !o.depsDone(*stage) {
				return
			}
			if stage.Plan == "" {
				o.Trigger(stageID, EvFail, GuardCtx{}, "no plan.md and no plan source configured")
				return
			}
			if err := os.MkdirAll(stageDir, 0755); err != nil {
				o.Trigger(stageID, EvFail, GuardCtx{}, "mkdir failed")
				return
			}
			if err := copyFile(resolvePlanSource(o.opts.RunDir, *stage), planPath); err != nil {
				o.Trigger(stageID, EvFail, GuardCtx{}, "copy plan failed: "+err.Error())
				return
			}
		}
		o.Trigger(stageID, EvReady, GuardCtx{}, "")
		// Synchronous transition guards against a concurrent event-loop path
		// (e.g. startReadyStages) also winning EvStartRun for this stage.
		if _, ok := o.Trigger(stageID, EvStartRun, GuardCtx{}, ""); !ok {
			return
		}
		o.spawnAgent(ctx, *stage, o.runImplementationAgent)
		o.startReadyStages(ctx)
		return
	}

	stageDir := filepath.Join(o.opts.RunDir, stageID)
	planPath := filepath.Join(stageDir, "plan.md")
	if _, err := os.Stat(planPath); err == nil {
		o.Trigger(stageID, EvReady, GuardCtx{}, "")
		// Same CAS guard as above: only the winner spawns.
		if _, ok := o.Trigger(stageID, EvStartRun, GuardCtx{}, ""); !ok {
			return
		}
		o.spawnAgent(ctx, *stage, o.runImplementationAgent)
	} else {
		// Deps not done — stay pending; planning starts automatically
		// via startPlanningForUnblocked once dependencies complete.
		if !stage.EagerPlanning && !o.depsDone(*stage) {
			return
		}
		// Synchronous transition guards against double start
		// (matches startPlanningForUnblocked pattern).
		if _, ok := o.Trigger(stageID, EvStartPlanning, GuardCtx{Stage: *stage}, "manual retry"); !ok {
			return
		}
		o.spawnAgent(ctx, *stage, o.runPlanningAgent)
	}
}

// depsDone checks whether all dependencies of a stage are in StatusDone.
func (o *Orchestrator) depsDone(s flow.Stage) bool {
	for _, dep := range s.DependsOn {
		if o.opts.Store.Get(dep) != state.StatusDone {
			return false
		}
	}
	return true
}

// tryActivatePrePlanned checks all pre-planned stages (those with Plan != "")
// and activates any whose dependencies are now done but status is still pending.
func (o *Orchestrator) tryActivatePrePlanned(ctx context.Context) {
	for _, s := range o.opts.Stages {
		if s.NeedsPlanning() {
			continue
		}

		current := o.opts.Store.Get(s.ID)

		if current != state.StatusPending {
			continue
		}

		if !o.depsDone(s) {
			continue
		}

		stageDir := filepath.Join(o.opts.RunDir, s.ID)
		if err := os.MkdirAll(stageDir, 0755); err != nil {
			o.Trigger(s.ID, EvFail, GuardCtx{}, "mkdir failed")
			continue
		}
		dst := filepath.Join(stageDir, "plan.md")
		if err := copyFile(resolvePlanSource(o.opts.RunDir, s), dst); err != nil {
			o.Trigger(s.ID, EvFail, GuardCtx{}, "copy plan failed")
			continue
		}
		o.Trigger(s.ID, EvReady, GuardCtx{}, "")
	}

	// Newly activated stages may now be ready to run.
	o.startReadyStages(ctx)
}

// startPlanningForUnblocked starts planning for pending stages whose
// dependencies are all done. Stages with eager_planning start at flow
// start and are never gated here.
func (o *Orchestrator) startPlanningForUnblocked(ctx context.Context) {
	for _, s := range o.opts.Stages {
		if !s.NeedsPlanning() {
			continue
		}
		if o.opts.Store.Get(s.ID) != state.StatusPending {
			continue
		}
		if !o.depsDone(s) {
			continue
		}
		// Synchronous transition out of pending guards against double
		// start: a second call sees "planning" and skips the stage.
		if _, ok := o.Trigger(s.ID, EvStartPlanning, GuardCtx{Stage: s}, "deps done"); !ok {
			continue
		}
		o.spawnAgent(ctx, s, func(ctx context.Context, st flow.Stage) {
			phases := o.DetermineStagePhases(ctx, st)
			if len(phases) == 1 && phases[0] == "autonomous_execution" {
				stageDir := filepath.Join(o.opts.RunDir, st.ID)
				if err := os.MkdirAll(stageDir, 0755); err != nil {
					o.Trigger(st.ID, EvFail, GuardCtx{}, "mkdir failed")
					return
				}
				_ = os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0644)
				o.Trigger(st.ID, EvSupervisorApproved, GuardCtx{}, "supervisor: autonomous")
				o.Trigger(st.ID, EvStartRun, GuardCtx{}, "")
				o.runAutonomousAgent(ctx, st)
			} else {
				o.runPlanningAgent(ctx, st)
			}
		})
	}
}

// startReadyStages starts implementation for stages whose dependencies are done.
func (o *Orchestrator) startReadyStages(ctx context.Context) {
	snap := o.opts.Store.Snapshot()
	statuses := make(map[string]state.StageStatus, len(snap.Stages))
	for id, s := range snap.Stages {
		statuses[id] = s.Status
	}

	ready := o.graph.ReadyStages(statuses)
	for _, id := range ready {
		stage := o.graph.Stage(id)
		if stage == nil {
			continue
		}
		if _, ok := o.Trigger(id, EvStartRun, GuardCtx{}, ""); !ok {
			continue
		}
		// Autonomous-стадия могла оказаться в Ready через retryStage (retry
		// упавшей autonomous-стадии) в узком окне между EvReady и её собственным
		// EvStartRun. Без этой проверки конкурентный вызов startReadyStages из
		// другой стадии event-loop'а мог выиграть CAS на EvStartRun первым и
		// запустить runImplementationAgent — тот читает plan.md, которого у
		// autonomous-стадии нет, и стадия падает "no such file or directory".
		if isAutonomousStage(filepath.Join(o.opts.RunDir, id)) {
			o.spawnAgent(ctx, *stage, o.runAutonomousAgent)
			continue
		}
		o.spawnAgent(ctx, *stage, o.runImplementationAgent)
	}
}

func (o *Orchestrator) runPlanningAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		o.Trigger(s.ID, EvFail, GuardCtx{}, "mkdir failed")
		return
	}

	// Планирование = стандартный (не автономный) трек. Если от предыдущей попытки
	// (или от неудавшегося autonomous-запуска до retry) остался autonomous.flag —
	// он теперь устарел: стадия пройдёт planning и получит настоящий plan.md с
	// approve/revise. Без снятия флага stage_autonomous в /api/status оставался бы
	// true, а дашборд прятал бы plan-панель (нет approve-кнопки на awaiting_approval).
	clearStaleAutonomousFlag(stageDir)

	// Defensive: may be a no-op if the caller already transitioned
	// the stage to "planning" (e.g. startPlanningForUnblocked).
	o.Trigger(s.ID, EvStartPlanning, GuardCtx{Stage: s}, "")

	o.runWithRetry(ctx, s, phasePlanning, func(retryContext string) error {
		depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages)
		artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
		if artErr != nil {
			log.Printf("WARN: collect artifacts for %s planning: %v", s.ID, artErr)
		}
		prompt := prompts.Build(prompts.Inputs{
			Template:         o.opts.Prompts.Planning,
			Stage:            s,
			PhaseAgent:       prompts.AgentPlanning,
			DependencyPlans:  depPlans,
			Artifacts:        artCtx,
			StageDir:         stageDir,
			Interactive:      s.Interactive,
			OutputContractMD: planningContract,
			RetryContext:     retryContext,
			GlobalPrompt:     o.opts.GlobalPrompt,
		})
		outFile := filepath.Join(stageDir, "plan.md")
		logFile := filepath.Join(stageDir, "planning.log")

		r := o.runnerFor(s, phasePlanning)
		if err := r.RunPlanning(ctx, s.Name, prompt, outFile, logFile); err != nil {
			return err
		}

		planMD, _ := os.ReadFile(outFile)
		issues := prompts.ValidatePlan(string(planMD), requiredPlanSections)
		if !issues.IsClean() {
			if adoptWrittenPlan(logFile, outFile) {
				return nil
			}
			if s.Interactive {
				return nil
			}
			if err := o.rePromptMissingSections(ctx, s, string(planMD), issues.MissingSections, outFile); err != nil {
				return err
			}
		}
		return nil
	}, func() error {
		return checkPlanCompletionFor(stageDir, s.Interactive)
	})
}

func (o *Orchestrator) rePromptMissingSections(ctx context.Context, s flow.Stage, prevPlan string, missing []string, outFile string) error {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	prompt := fmt.Sprintf(
		"Your previous plan was missing required sections: %s.\nAdd ONLY the missing sections to the existing plan below. Do not rewrite the rest.\n\n<previous_plan>\n%s\n</previous_plan>",
		strings.Join(missing, ", "),
		prompts.EscapeTagsForReprompt(prevPlan),
	)
	logFile := filepath.Join(stageDir, "planning-reprompt.log")
	r := o.runnerFor(s, phasePlanning)
	if err := r.RunPlanning(ctx, s.Name, prompt, outFile, logFile); err != nil {
		return err
	}
	planMD, _ := os.ReadFile(outFile)
	issues := prompts.ValidatePlan(string(planMD), requiredPlanSections)
	if !issues.IsClean() {
		if adoptWrittenPlan(logFile, outFile) {
			return nil
		}
		return &MissingSectionsError{Missing: issues.MissingSections}
	}
	return nil
}

func (o *Orchestrator) runPlanningWithFeedback(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)

	o.Trigger(s.ID, EvStartPlanning, GuardCtx{Stage: s}, "")

	o.runWithRetry(ctx, s, phasePlanning, func(retryContext string) error {
		feedbackData, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
		var prevPlan string
		planVersionRe := regexp.MustCompile(`^plan\.v(\d+)\.md$`)
		var bestVer int
		entries, _ := os.ReadDir(stageDir)
		for _, e := range entries {
			m := planVersionRe.FindStringSubmatch(e.Name())
			if m == nil {
				continue
			}
			v, _ := strconv.Atoi(m[1])
			if v > bestVer {
				bestVer = v
				data, _ := os.ReadFile(filepath.Join(stageDir, e.Name()))
				prevPlan = string(data)
			}
		}

		depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages)
		artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
		if artErr != nil {
			log.Printf("WARN: collect artifacts for %s revise: %v", s.ID, artErr)
		}
		prompt := prompts.Build(prompts.Inputs{
			Template:         o.opts.Prompts.Planning,
			Stage:            s,
			PhaseAgent:       prompts.AgentPlanning,
			DependencyPlans:  depPlans,
			Artifacts:        artCtx,
			PreviousPlan:     prevPlan,
			Feedback:         string(feedbackData),
			StageDir:         stageDir,
			Interactive:      s.Interactive,
			OutputContractMD: planningContract,
			RetryContext:     retryContext,
			GlobalPrompt:     o.opts.GlobalPrompt,
		})
		outFile := filepath.Join(stageDir, "plan.md")
		logFile := filepath.Join(stageDir, "planning-revision.log")

		r := o.runnerFor(s, phasePlanning)
		if err := r.RunPlanning(ctx, s.Name, prompt, outFile, logFile); err != nil {
			return err
		}
		planMD, _ := os.ReadFile(outFile)
		issues := prompts.ValidatePlan(string(planMD), requiredPlanSections)
		if !issues.IsClean() {
			if adoptWrittenPlan(logFile, outFile) {
				return nil
			}
			if s.Interactive {
				return nil
			}
			if err := o.rePromptMissingSections(ctx, s, string(planMD), issues.MissingSections, outFile); err != nil {
				return err
			}
		}
		return nil
	}, func() error {
		return checkPlanCompletionFor(stageDir, s.Interactive)
	})
}

func (o *Orchestrator) runImplementationAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)

	o.runWithRetry(ctx, s, phaseImplementation, func(retryContext string) error {
		planData, err := os.ReadFile(filepath.Join(stageDir, "plan.md"))
		if err != nil {
			return err
		}

		depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages)
		artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
		if artErr != nil {
			log.Printf("WARN: collect artifacts for %s impl: %v", s.ID, artErr)
		}

		// Format output artifact requirements
		if len(s.Artifacts) > 0 {
			var buf strings.Builder
			buf.WriteString("\n\nRequired output artifacts (MUST exist at these paths when stage finishes):\n\n")
			for _, art := range s.Artifacts {
				dst := art.Path
				if strings.HasPrefix(art.Path, "./") {
					dst = filepath.Join(stageDir, art.Path[2:])
				}
				desc := ""
				if art.Description != "" {
					desc = " — " + art.Description
				}
				fmt.Fprintf(&buf, "- %s%s → %s\n", art.Name, desc, dst)
			}
			artCtx += buf.String()
		}

		stageDirNote := fmt.Sprintf("\n\nStage directory for .done file: %s", stageDir)
		if s.Verify != "" {
			stageDirNote += fmt.Sprintf("\n\nVerify command (runs automatically after you finish; it MUST exit 0, "+
				"so run it yourself before creating .done):\n%s", s.Verify)
		}
		prompt := prompts.Build(prompts.Inputs{
			Template:        o.opts.Prompts.Implementation,
			Stage:           s,
			PhaseAgent:      prompts.AgentImplementation,
			DependencyPlans: depPlans,
			Artifacts:       artCtx,
			Plan:            string(planData),
			StageDir:        stageDir,
			Interactive:     s.Interactive,
			RetryContext:    retryContext + stageDirNote,
			GlobalPrompt:    o.opts.GlobalPrompt,
		})
		logFile := filepath.Join(stageDir, "implementation.log")

		r := o.runnerFor(s, phaseImplementation)
		if err := r.RunAgent(ctx, string(s.ImplAgent()), s.Name, prompt, logFile); err != nil {
			return err
		}

		if s.HasAgent(flow.AgentReview) {
			reviewPrompt := prompts.Build(prompts.Inputs{
				Template:        o.opts.Prompts.Review,
				Stage:           s,
				PhaseAgent:      prompts.AgentReview,
				DependencyPlans: depPlans,
				Artifacts:       artCtx,
				StageDir:        stageDir,
				Interactive:     s.Interactive,
				GlobalPrompt:    o.opts.GlobalPrompt,
			})
			reviewLog := filepath.Join(stageDir, "review.log")
			rr := o.runnerFor(s, phaseReview)
			if err := rr.RunAgent(ctx, phaseReview, s.Name, reviewPrompt, reviewLog); err != nil {
				return err
			}
		}

		return nil
	}, func() error {
		return checkCompletion(stageDir, ".", s)
	})
}

func (o *Orchestrator) runReviewAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		o.Trigger(s.ID, EvFail, GuardCtx{}, "mkdir failed")
		return
	}

	depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages)
	artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
	if artErr != nil {
		log.Printf("WARN: collect artifacts for %s review: %v", s.ID, artErr)
	}

	o.runWithRetry(ctx, s, phaseReview, func(retryContext string) error {
		reviewPrompt := prompts.Build(prompts.Inputs{
			Template:        o.opts.Prompts.Review,
			Stage:           s,
			PhaseAgent:      prompts.AgentReview,
			DependencyPlans: depPlans,
			Artifacts:       artCtx,
			StageDir:        stageDir,
			Interactive:     s.Interactive,
			RetryContext:    retryContext,
			GlobalPrompt:    o.opts.GlobalPrompt,
		})
		reviewLog := filepath.Join(stageDir, "review.log")
		rr := o.runnerFor(s, phaseReview)
		return rr.RunAgent(ctx, phaseReview, s.Name, reviewPrompt, reviewLog)
	}, func() error {
		return checkCompletion(stageDir, ".", s)
	})
}

// failBlockedStages marks pending stages as failed if any of their
// dependencies are in StatusFailed. This prevents the flow from hanging
// when a dependency fails and dependent stages can never start.
func (o *Orchestrator) failBlockedStages() {
	changed := true
	for changed {
		changed = false
		for _, s := range o.opts.Stages {
			current := o.opts.Store.Get(s.ID)

			if current != state.StatusPending {
				continue
			}

			for _, dep := range s.DependsOn {
				if o.opts.Store.Get(dep) == state.StatusFailed {
					o.Trigger(s.ID, EvBlockedByDep, GuardCtx{}, "dep failed")
					changed = true
					break
				}
			}
		}
	}
}

func (o *Orchestrator) allTerminal() bool {
	snap := o.opts.Store.Snapshot()
	if len(snap.Stages) == 0 {
		return true
	}
	for _, s := range snap.Stages {
		if !IsTerminal(s.Status) {
			return false
		}
	}
	return true
}

// shouldExit reports whether the orchestrator loop should stop.
// Without a dashboard, any terminal state (done or failed) is final.
// With a dashboard, exit only when all stages are done — failed stages stay
// visible so the user can retry them without restarting the process.
func (o *Orchestrator) shouldExit() bool {
	if !o.allTerminal() {
		return false
	}
	if o.opts.DashboardURL == "" {
		return true
	}
	snap := o.opts.Store.Snapshot()
	return snap.AllDone()
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

// isAutonomousStage возвращает true, если stageDir содержит autonomous.flag —
// маркер того, что стадия уже переведена на автономный трек.
func isAutonomousStage(stageDir string) bool {
	_, err := os.Stat(filepath.Join(stageDir, "autonomous.flag"))
	return err == nil
}

// clearStaleAutonomousFlag удаляет autonomous.flag, оставшийся от более раннего
// решения супервизора (или от неудавшейся автономной попытки), когда текущая
// попытка идёт по стандартному треку (planning). Без этого isAutonomousStage
// (и производный от неё stage_autonomous в /api/status) навсегда считал бы
// стадию автономной — даже после того, как она реально прошла planning и
// получила настоящий plan.md, ожидающий approve/revise в дашборде.
func clearStaleAutonomousFlag(stageDir string) {
	_ = os.Remove(filepath.Join(stageDir, "autonomous.flag"))
}

// logSupervisorDecision записывает решение супервизора в <runDir>/supervisor.jsonl
// (одна JSON-запись на строку). Ошибки записи логируются молча — этот файл
// лучшего характера (для аудита/UI), fallback DetermineStagePhases на него не завязан.
func (o *Orchestrator) logSupervisorDecision(stageID, decision, reason string) {
	type entry struct {
		Ts       string `json:"ts"`
		StageID  string `json:"stage_id"`
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	e := entry{
		Ts:       time.Now().UTC().Format(time.RFC3339),
		StageID:  stageID,
		Decision: decision,
		Reason:   reason,
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	logPath := filepath.Join(o.opts.RunDir, "supervisor.jsonl")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

// DetermineStagePhases вызывает Supervisor и возвращает выбранные фазы для стадии.
// Вызывается внутри горутины планирования (не блокирует event loop).
//
// Правила:
//   - stage.Supervisor=false ИЛИ supervisor отключён (nil) → базовые фазы.
//   - inline-артефакт guard: наличие inline-артефакта форсирует стандартный цикл
//     (planning пропускать нельзя — агенту нужен контекст артефакта в plan.md).
//   - при любой ошибке LLM/парсинга → фолбэк на базовые фазы (без crash flow).
//   - CanExecuteAutonomously=true → ["autonomous_execution"], логируется решение,
//     публикуется событие EventSupervisorDecision.
func (o *Orchestrator) DetermineStagePhases(ctx context.Context, s flow.Stage) []string {
	base := agentTypesToStrings(s.Agents)

	if !s.Supervisor || o.supervisor == nil {
		return base
	}
	// Inline-артефакт guard: planning пропускать нельзя — агенту нужен контекст
	// артефакта (фабрика/спецификация) для корректного plan.md.
	for _, art := range s.Artifacts {
		if art.IsInline() {
			log.Printf("supervisor: stage %s has inline artifact %q, skipping evaluation", s.ID, art.Name)
			return base
		}
	}
	decision, err := o.supervisor.EvaluateStage(ctx, s, o.opts.GlobalPrompt)
	if err != nil {
		log.Printf("supervisor: fallback for stage %s: %v", s.ID, err)
		return base
	}
	// Решение супервизора публикуем в UI и пишем в supervisor.jsonl для ОБОИХ треков
	// (раньше standard не публиковался — UI не видел резолюцию).
	track := "standard"
	if decision.CanExecuteAutonomously {
		track = "autonomous"
	}
	o.logSupervisorDecision(s.ID, track, decision.Reason)
	o.ui.Publish(Event{
		Type:    EventSupervisorDecision,
		StageID: s.ID,
		Data:    decision,
	})
	if decision.CanExecuteAutonomously {
		log.Printf("supervisor: stage %s → autonomous_execution. Reason: %s", s.ID, decision.Reason)
		return []string{"autonomous_execution"}
	}
	log.Printf("supervisor: stage %s → standard. Reason: %s", s.ID, decision.Reason)
	return base
}

// runAutonomousAgent выполняет стадию в автономном треке — без plan.md и approval.
// Агент использует прикреплённые скиллы и обязан написать execution_summary.md
// по завершении (проверяется completion-check'ом checkAutonomousCompletion).
//
// Трек отличается от runImplementationAgent: нет чтения plan.md, нет .done,
// фаза — "autonomous_execution", используется Autonomous-шаблон промпта.
func (o *Orchestrator) runAutonomousAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)

	o.runWithRetry(ctx, s, phaseAutonomous, func(retryContext string) error {
		artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
		if artErr != nil {
			log.Printf("WARN: collect artifacts for %s autonomous: %v", s.ID, artErr)
		}
		depCtx := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages)

		summaryNote := fmt.Sprintf("\n\nStage directory: %s\nWrite execution_summary.md here when done.", stageDir)
		prompt := prompts.Build(prompts.Inputs{
			Template:        o.opts.Prompts.Implementation, // fallback, если Autonomous пустой
			Autonomous:      o.opts.Prompts.Autonomous,
			Stage:           s,
			PhaseAgent:      prompts.AgentAutonomous,
			Interactive:     true, // dialog protocol — autonomous-скилл может спрашивать пользователя
			Artifacts:       artCtx,
			DependencyPlans: depCtx,
			StageDir:        stageDir,
			GlobalPrompt:    o.opts.GlobalPrompt,
			RetryContext:    retryContext + summaryNote,
		})
		logFile := filepath.Join(stageDir, "autonomous.log")
		r := o.runnerFor(s, phaseAutonomous)
		return r.RunAgent(ctx, phaseAutonomous, s.Name, prompt, logFile)
	}, func() error {
		return checkAutonomousCompletion(stageDir)
	})
}

// StoreFromOrch возвращает Store оркестратора. Только для тестов.
func StoreFromOrch(o *Orchestrator) *state.Store { return o.opts.Store }
