package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/execprep"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/memorypipeline"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
	"github.com/akopichin/afm/pkg/watch"
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
	// HelperResult — assets/prompts/helper-result.md, машинный контракт ответа помощника (envelope
	// kind:"result"), приложенный к каждому ходу assistants-задачи (buildTaskContext).
	HelperResult string
	// HelperDelegate — assets/prompts/helper-delegate.md, альтернативная форма ответа kind:"delegate".
	// Приложена только для профилей с Capabilities.Delegate — непригодному профилю её не показывают, иначе
	// он может попытаться делегировать и потратить ход на отказ admission.
	HelperDelegate string
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
	// SecretValues are secret values resolved outside afm's environment (project and global secrets.env) that
	// must be redacted from mesh text, task context and logs like environment secrets are.
	SecretValues []string
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
	// Repeated — ран создан циклом repeat со второго: стартовое событие flow_repeated вместо flow_started.
	// Resumed приоритетнее: продолжение рана после краша по-прежнему эмитит flow_resumed.
	Repeated bool
	Hooks    *lifecyclehooks.Dispatcher
	// BrowserDescriptor — managed Chrome DevTools capability for this run.
	BrowserDescriptor *browser.Descriptor
	// AssistantsEnabled/AssistantsRegistry — the collaboration capability for this run (T3.4). Resolved ONCE
	// by cmd/afm before Orchestrator.New (mirrors resolveCollaboration/startCollaboration) — disabled or a nil
	// registry both mean "capability off": Attach rejects immediately, nothing here ever spawns a model on
	// its own (Инвариант 9).
	AssistantsEnabled  bool
	AssistantsRegistry *assistants.Registry
	// Team — the flow's team: declarations, run agents initTeam creates before the stages start. Read from
	// the CURRENT flow.yaml on every start; on resume the agents the run already created are kept as saved.
	Team flow.Team
	// AliasWarning receives the one-line warnings about an @name reassigned to make room for the bound
	// Assistant (assistantAliasWarning). cmd/afm sets it to stderr; nil (tests, library use) prints nothing.
	AliasWarning io.Writer
	// Clock — часы рана. Пусто (nil) означает clock.Real(): прод-код и большинство тестов, конструирующих
	// Orchestrator через New, не задают его явно и получают реальное время без дополнительных действий.
	// Fake-часы инжектируют только тесты, которым нужно проматывать паузы вручную.
	Clock clock.Clock
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
	opts Options
	// sideAgentReachable is true only while this process has a usable side store and inbox bridge.
	sideAgentReachable atomic.Bool
	sideAgentGate      sync.RWMutex
	// sideMeshDrain is installed before Run. Finalization waits for its Mesh
	// inbox and active turn without blocking the event loop that handles replies.
	sideMeshDrain     func(context.Context) bool
	sideMeshDrainOnce sync.Once
	sideMeshDrained   atomic.Bool
	// absRunDir — опредёлённый ОДИН РАЗ в New абсолютный вариант opts.RunDir (filepath.Abs; при ошибке —
	// исходное значение as-is). Используется ТОЛЬКО через stageDir() — на границе с агентом (AFM_STAGE_DIR),
	// где CWD агента (root_dir/worktree) может не совпадать с CWD afm, и относительный путь резолвился бы не
	// туда. Внутренние call sites (filepath.Join(o.opts.RunDir, s.ID)) намеренно не переведены на него — afm
	// никогда не делает os.Chdir, поэтому относительный RunDir резолвится стабильно везде, где путь не
	// пересекает границу процесса (D4, dev-plan.md).
	absRunDir string
	// graph is a *graph.Store, not a bare *graph.Registry (Этап 9, T9.4): every read in this file goes
	// through reg() (o.graph.Load()), a fresh snapshot each time, so a successful AddStageToGraph commit's
	// subsequent graph.Replace becomes visible to the NEXT read everywhere in this package without restarting
	// anything. Before Этап 9 this was always revision 1, so nothing here changed behavior.
	graph *graph.Store
	// preparedCommands is this run's pre-resolved command set (T8.5), consulted by Этап 9's draft validation
	// (IsCommandPrepared) — built once in New from the stage set the run actually started with.
	preparedCommands *execprep.Set
	runner           executor.Runner
	critical         *bus.CriticalBus
	ui               *bus.UIBus
	fsm              *bus.FSM
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
	// prePeerPhase — Agent Mesh Phase 5 (T5.2/T5.3): the peer twin of preAskPhase, storing the phase a stage
	// was running when it registered a mesh Wait and parked in awaiting_peer, so onPeerReplied knows which
	// kind of agent to respawn.
	prePeerPhase sync.Map
	// unreadTurns counts, per stageID, the extra turns given because the stage agent finished with unread
	// messages (int); unreadNotePending marks a stage whose next prompt gets the "answer them, then finish" note.
	// See agentcomm_unread.go.
	unreadTurns       sync.Map
	unreadNotePending sync.Map
	// hookWaiters holds, per stageID, the channel a blocked before/after hook is waiting on for a user
	// decision. Only populated while a hook is actually blocked.
	hookWaiters sync.Map
	// violationCache кешует stat .jsonl-файлов для detectDialogViolation.
	// Доступен только из горутины поллера — мьютекс не нужен.
	violationCache map[string]violationCacheEntry // key: путь к .jsonl
	// lastRootScan хранит время последнего скана root_dir на стадию (throttle
	// в relocateMisplacedQuestions). Доступен только из горутины поллера.
	lastRootScan map[string]time.Time
	// maxRetries/retryBackoff/maxUnknownRetries/retryBackoffMax — снапшоты package-level
	// RetryBackoff/MaxRetries/MaxUnknownRetries/RetryBackoffMax, снятые в New. Снапшот на инстансе устраняет
	// гонку: горутины читают immutable-поля.
	maxRetries        int
	retryBackoff      time.Duration
	maxUnknownRetries int
	retryBackoffMax   time.Duration
	// clock — часы рана, снятые в New из opts.Clock. nil означает Orchestrator собран структурным литералом в
	// тестах, в обход New (>40 мест в pkg/orchestrator) — доступ ТОЛЬКО через nil-safe o.clk(), прямое o.clock.X()
	// запаниковало бы в половине тестов пакета.
	clock clock.Clock
	// jitterFrac — доля retry-backoff (см. jittered, retry.go), которую New снимает с defaultJitterFrac.
	// Zero value (структурный литерал в тестах) означает 0 — тест видит точный номинал backoffFor, а не
	// случайный шум; нет отдельного nil-safe аксессора, т.к. 0 — корректное самостоятельное значение, а не
	// маркер "не задано" (в отличие от clock, где nil не является валидным Clock).
	jitterFrac float64
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
	// taskCancels хранит *taskCancelEntry активного прогона задачи на время её выполнения — Pause/Cancel
	// используют его, чтобы прервать уже стартовавший helper-процесс немедленно (ключ — assistants.TaskID).
	// Значение — указатель (не голый context.CancelFunc), чтобы runHelperTask's defer мог CompareAndDelete
	// СВОЙ СОБСТВЕННЫЙ entry по identity, а не слепой Delete: requeue-ветка runHelperTask синхронно зовёт
	// maybeScheduleTask → spawnTask → SpawnOwned, который стартует СЛЕДУЮЩУЮ попытку в новой горутине ДО того,
	// как эта (старая) горутина дойдёт до своего defer; если новая попытка успевает сделать свой Store раньше
	// — голый Delete по ключу стёр бы чужой (уже актуальный) cancel, и Pause/Cancel больше не находили бы
	// способа остановить реально бегущий процесс (см. TestPauseTask_SurvivesImmediateRequeueRace).
	taskCancels sync.Map
	// taskClarifyPending отмечает задачу, чья текущая попытка отменена через StopAndClarify (T4.7), а не
	// PauseTask — установлена ДО cancel(), потребляется ОДИН РАЗ через LoadAndDelete внутри runHelperTask'а
	// (та же горутина, что решает исход прогона), а не отдельным "clear"-шагом извне: отдельный шаг гонялся
	// бы с этой самой проверкой (окно, где сигнал уже снят, а runHelperTask ещё не успел его увидеть, —
	// задача ошибочно помечается failed вместо requeue). Ключ — assistants.TaskID.
	taskClarifyPending sync.Map
	// taskPausePending — тот же one-shot сигнал, что taskClarifyPending, но для PauseTask/ContinueTask:
	// установлен ДО cancel(), потребляется ОДИН РАЗ через LoadAndDelete в том же месте runHelperTask's switch.
	// Без него классификация опиралась бы ТОЛЬКО на o.taskHeld (живое состояние hold) — а немедленный
	// ContinueTask, обогнавший классификацию, успевает снять hold ДО того, как эта горутина дойдёт до проверки,
	// из-за чего отменённая по Pause попытка ошибочно помечается failed вместо requeue (см.
	// TestPause_ImmediateContinueBeforeClassificationStillRequeues).
	taskPausePending sync.Map
	// taskDelegationRejects считает ПОДРЯД идущие отклонения delegate-хода для одной задачи (ключ —
	// assistants.TaskID, значение — int). Сбрасывается при успешном делегировании (handleDelegate). Нужен,
	// чтобы модель, упорно повторяющая один и тот же отклоняемый delegate, не крутила задачу running⇄queued
	// бесконечно без backoff — после maxConsecutiveDelegationRejections задача падает (assistants_reject.go).
	taskDelegationRejects sync.Map

	// terminalFlow — финальное flow-событие, установленное одним из выходов Run; эмитится finalizeLifecycle
	// ПОСЛЕ остановки продюсеров.
	terminalFlow lifecyclehooks.EventType

	// pollerDone закрывается горутиной startQuestionPoller при её выходе.
	pollerDone chan struct{}
	// pollTickHook — тестовый seam (nil-safe, тот же приём, что и у retryCASBarrier/commitAddStageBarrier):
	// вызывается ПОСЛЕ каждого pollQuestions внутри тика поллера, чтобы тест мог посчитать реальные тики по
	// fake-часам, не имея другого способа наблюдать внутренний цикл startQuestionPoller.
	pollTickHook func()

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

	// inFlightFeedbackIntents (T6.5/T6.6) guards attemptFeedbackDelivery against re-entering itself for the
	// SAME intent id: delivering to a "running" stage triggers a NESTED EvRevise transition
	// (reviseRunning->triggerWithSeq), whose own post-transition hook would otherwise see the SAME
	// still-"accepted" intent and recurse into attemptFeedbackDelivery again before the outer call ever gets a
	// chance to mark it delivered.
	inFlightFeedbackIntents sync.Map
	// feedbackWakeups — intent ids with a delivery request not yet handled by the deliverer holding
	// inFlightFeedbackIntents (see attemptFeedbackDelivery): no request that arrives during a delivery is lost.
	feedbackWakeups sync.Map

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
	// feedbackDeliveryDecidedHook — тест-сейм: вызывается в deliverFeedbackIntentOnce между выбором пути по
	// статусу и самим Revise, чтобы тест мог вклинить конкурентный Pause. nil в продакшене.
	feedbackDeliveryDecidedHook func(stageID string)

	// taskRequeueBarrier — тест-сейм: вызывается в spawnTask's run body СРАЗУ после того, как taskHeld
	// вернул true (held), но ДО finishTaskFrom's requeue-commit (running->queued) — позволяет тесту
	// детерминированно воспроизвести lost-wakeup гонку PauseTask/ContinueTask вокруг этого окна (см.
	// spawnTask's doc comment).
	taskRequeueBarrier func(taskID assistants.TaskID)
	// taskRequeuePreDeferHook — тест-сейм: вызывается в runHelperTask's requeue-while-running ветке СРАЗУ
	// после синхронного maybeScheduleTask (который уже мог успеть застартовать СЛЕДУЮЩУЮ попытку в новой
	// горутине под тем же taskID), но ДО того, как ЭТА (старая) горутина дойдёт до своего defer
	// (CompareAndDelete в o.taskCancels). Позволяет тесту детерминированно воспроизвести гонку, где новая
	// попытка успевает сохранить свой cancel-entry раньше старой (см. TestPauseTask_SurvivesImmediateRequeueRace).
	taskRequeuePreDeferHook func(taskID assistants.TaskID)

	// commitAddStageBarrier — тест-сейм (Этап 9, T9.4): вызывается в CommitAddStage ПОСЛЕ planDraft
	// (advisory preflight), НЕПОСРЕДСТВЕННО ПЕРЕД Store.AddStageToGraph — т.е. в точке, где ни один lock ещё
	// не взят. Позволяет тесту детерминированно доказать TestCommit_NoLockHeldDuringModelWorkOrPreflight: пока
	// хук блокирует эту горутину, конкурентная операция над тем же Store не должна ждать.
	commitAddStageBarrier func()

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

	// loopTick доставляет тик лупа; nil означает commitLoopTick. Подменяется в тестах планировщика, как
	// runHelperAgent.
	loopTick loopTickFunc
	// loopWake будит планировщик лупов при создании и остановке лупа (agentcomm_loop_scheduler.go).
	loopWake wakeSignal
	// testAfterLoopRestartCommit exposes the gap between the durable restart and collector claim.
	testAfterLoopRestartCommit func()
	// testAfterCheckpointCommit exposes the gap between checkpoint persistence and its fire CAS.
	testAfterCheckpointCommit func()
	// testAfterFireCASLost runs in commitAndFire after fireWatcherTask's CAS failed but before the fire debt
	// is registered, so a test can settle a competing turn in that gap.
	testAfterFireCASLost func()
	// testAfterLostFireStatusRead lets a test settle the competing turn after commitAndFire has observed its
	// status but before commitAndFire retries. The owed fire must already be registered at that point.
	testAfterLostFireStatusRead func(assistants.TaskStatus)

	// reviewResumed помечает owner'ов, запущенных review-recovery, чтобы последующий bootstrap не спавнил их
	// повторно.
	reviewResumed sync.Map

	// testRunnerHook подменяет Trigger+SpawnAgent в resumeOwner, позволяя проверить resume_kind и feedback
	// без subprocess.
	testRunnerHook func(kind string, withFeedback bool)

	// testClearReviewArtifacts подменяет очистку review-файлов для проверки fail-closed поведения при ошибке
	// удаления.
	testClearReviewArtifacts func() error

	// testAfterAttachSnapshot runs in AttachAndSend right after it reads the snapshot it plans from, so a test
	// can commit a competing attach in between (the alias race initTeam retries).
	testAfterAttachSnapshot func()

	// testAfterBoardTaskLookup runs in BoardTask right after its lookup of a committed request with the same id
	// found none, so a test can commit a concurrent duplicate of the request in between.
	testAfterBoardTaskLookup func()

	// --- Этап 5: watcher helper tasks (checkpoints/on_completion-triggered observation of the main agent) ---

	// watchMu guards watchByStage/watchByTask — rare, admission-time/poll-time mutation, not a hot path, so a
	// plain mutex (not sync.Map) keeps the fan-out-by-stage slice simple to maintain.
	watchMu      sync.Mutex
	watchByStage map[string][]*watcherBinding
	watchByTask  map[assistants.TaskID]*watcherBinding
	// taskFinalPass marks a taskID whose NEXT completed turn must land in the terminal "completed" status
	// instead of the resting "watching" one — consumed exactly once (LoadAndDelete) by runHelperTask's success
	// tail, mirroring taskPausePending/taskClarifyPending's one-shot signal pattern above.
	taskFinalPass sync.Map
	// renderedInbox maps a mesh participant to the message ids its most recently built prompt put into the
	// <inbox> block — what that turn's OnDelivered marks delivered once its process has started (see
	// agentCommInboxFor). One agent per participant runs at a time, so the latest prompt is that turn's prompt.
	renderedInbox sync.Map
	// watchClock — injectable Clock for watch.NewCollector (test seam, mirrors fingerprintFn); nil = real time.
	watchClock watch.Clock
	// watchPollInterval overrides the watcher poll ticker period in tests; 0 = production default.
	watchPollInterval time.Duration

	// --- Agent Mesh Phase 3 (T3.1): per-invocation agent-mesh bearer tokens ---
	meshTokensOnce  sync.Once
	meshTokensStore *meshTokenStore
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

	// T8.1/T8.2: opts.Stages is read here ONCE, to seed the runtime registry — every other stage-definition
	// lookup in this package goes through o.graph (the registry store), never opts.Stages again. Этап 9's
	// AddStageToGraph commit path is the only thing that ever calls o.graph.Replace after this.
	initialReg := graph.NewRegistry(opts.Stages)
	gstore := graph.NewStore(initialReg)

	// Семафоры на команду строятся из конфигурации стадий: per-stage MaxParallel имеет приоритет над
	// глобальным дефолтом.
	conc := concurrency.New(critical, initialReg.Stages(), opts.Config.Client.Command, opts.Config.Executor.MaxParallel,
		func(stageID string) bool { return opts.Store.Get(stageID) != state.StatusPaused })
	// conc.Clock намеренно остаётся nil (реальные часы), а не opts.Clock: тесты подсовывают в Options.Clock
	// быстро прокручиваемые fake-часы, чтобы пропустить ожидания hookRetryBackoff, и те же часы обогнали бы
	// drain-таймаут WaitAgents (agentDrainTimeout, concurrency.go) ЗАДОЛГО до реального завершения agentWG,
	// которое этот таймаут обязан ограничивать — тихо срывая happens-before гарантию cancel->WaitAgents->Close
	// в тестовой инфраструктуре (ровно тот класс флейка "write events.jsonl: invalid argument", AGENTS.md).
	// Ни прод, ни тесты сейчас не нуждаются в Manager.Clock, управляемом часами Orchestrator — тесты самого
	// concurrency ставят Clock напрямую на отдельно сконструированный Manager, когда нужно прогнать drain-таймаут.

	absRunDir := opts.RunDir
	if abs, err := filepath.Abs(opts.RunDir); err == nil {
		absRunDir = abs
	}

	o := &Orchestrator{
		opts:              opts,
		absRunDir:         absRunDir,
		graph:             gstore,
		preparedCommands:  buildPreparedCommandSet(initialReg.Stages(), opts.Config.Client.Command),
		runner:            r,
		critical:          critical,
		ui:                ui,
		fsm:               bus.NewFSM(opts.Store),
		concurrency:       conc,
		violationCache:    make(map[string]violationCacheEntry),
		lastRootScan:      make(map[string]time.Time),
		maxRetries:        MaxRetries,
		retryBackoff:      RetryBackoff,
		maxUnknownRetries: MaxUnknownRetries,
		retryBackoffMax:   RetryBackoffMax,
		clock:             opts.Clock,
		jitterFrac:        defaultJitterFrac,
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

// stageDir — каталог стадии АБСОЛЮТНЫМ путём. Нужен там, где путь пересекает границу процесса: CWD
// агента (root_dir/worktree) не совпадает с CWD afm, и относительный путь резолвился бы не туда.
func (o *Orchestrator) stageDir(stageID string) string {
	return filepath.Join(o.absRunDir, stageID)
}

// clk возвращает часы рана; nil (Orchestrator собран структурным литералом в тестах, в обход New) означает
// реальные часы — тот же fail-open приём, что уже есть у o.fingerprintFn (verify.go). Все обращения к
// времени ОБЯЗАНЫ идти через o.clk(), а не o.clock напрямую — прямой вызов паникует на nil в половине
// тестов пакета.
func (o *Orchestrator) clk() clock.Clock {
	if o.clock == nil {
		return clock.Real()
	}
	return o.clock
}

// reg returns the CURRENT registry snapshot — every stage-definition read in this package goes through this,
// never o.graph directly, so a call right before and right after a concurrent AddStageToGraph commit can see
// either the old or the new revision, but always a single, internally consistent one (graph.Store's own
// atomic-pointer contract).
func (o *Orchestrator) reg() *graph.Registry { return o.graph.Load() }

// Stages returns the run's CURRENT stage definitions from the runtime registry, in stable declaration
// order. T8.3: callers that need to re-derive dashboard-facing config (cmd/afm's RuntimeStageProvider) call
// this fresh every time, instead of snapshotting a static []flow.Stage once at startup — so a registry
// revision bumped by Этап 9's AddStageToGraph is visible without rebuilding the dashboard.
func (o *Orchestrator) Stages() []flow.Stage { return o.reg().Stages() }

// GraphRevision returns the runtime registry's current revision (Этап 9, T9.1's preview/T9.4's commit
// optimistic-concurrency check).
func (o *Orchestrator) GraphRevision() int64 { return o.reg().Revision() }

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
	beforeReady, stageWaitsPending := o.stageStatusWaitsPending(stageID)
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
		if stageWaitsPending {
			o.resumeWaitersAfterTransition(beforeReady)
		}
		if bus.IsTerminal(to) && o.opts.Store != nil && len(unreadOf(o.opts.Store.Snapshot(), meshStageParticipantID(stageID))) > 0 {
			// Every unread message is released, passive turn results included (they hold the run until then).
			o.releaseUnreadAfterTransition(stageID)
		}
		if bus.IsTerminal(to) {
			o.noticeUnansweredHumanPosts(stageID)
			o.releaseUnanswerableWaitsAfterFinish()
		}
		// Wake the event loop so it can check shouldExit(). Non-blocking to avoid deadlock.
		o.critical.TryPublish(pubEv)
		o.emitLifecycleTransition(stageID, from, to, seq, ev, reason)
		// runnerKind больше не актуален: стадия завершилась, провалилась, ушла ждать пользователя или ушла
		// ждать помощников — во всех случаях раннер, который её вёл, для неё закончил работу (awaiting_assistants
		// is entered only once the main executor's own process has already exited, T7.2).
		if ev == bus.EvComplete || ev == bus.EvFail || ev == bus.EvVerifyFail || ev == bus.EvAskUser || ev == bus.EvAwaitAssistants {
			o.clearRunnerKind(stageID)
		}
		// Этап 5 T5.4 / Этап 7 T7.2: a single funnel for "this stage's own result is ready/failed/now waiting on
		// required assistants" regardless of which of the ~60 call sites triggered it — see
		// finalizeWatchersForStage's doc comment for why hooking here (rather than completeStage/each EvFail
		// call site individually) is the safe, additive choice. EvAwaitAssistants forces any orphaned
		// checkpoints/on_completion watcher into its bounded final pass the moment there is no more main-agent
		// output coming — otherwise a required watcher could be left "watching" forever.
		if ev == bus.EvComplete || ev == bus.EvFail || ev == bus.EvVerifyFail || ev == bus.EvAwaitAssistants {
			o.finalizeWatchersForStage(stageID, ev)
		}
		// T6.5/T6.6: a stage settling into ANY new status may be exactly what a deferred FeedbackIntent
		// (planning/revising/retrying/paused/awaiting_user_input/hook_failed -> now interruptible or terminal)
		// was waiting for — same additive "single funnel" choice as finalizeWatchersForStage above, just for
		// every transition rather than only completion/failure.
		if o.opts.AssistantsEnabled {
			o.redeliverPendingFeedbackIntents(o.runContext(context.Background()), stageID)
		}
	}
	return to, seq, ok
}

// SetDashboardURL sets the dashboard URL after the server starts listening.
func (o *Orchestrator) SetDashboardURL(url string) { o.opts.DashboardURL = url }

// flowStartEvent выбирает событие начала рана: продолжение, новый цикл repeat или обычный старт.
func (o *Orchestrator) flowStartEvent() lifecyclehooks.EventType {
	switch {
	case o.opts.Resumed:
		return lifecyclehooks.EventFlowResumed
	case o.opts.Repeated:
		return lifecyclehooks.EventFlowRepeated
	default:
		return lifecyclehooks.EventFlowStarted
	}
}

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
	// closeMeshWaitsIfFinalized (T5.6) runs AFTER agents have drained (WaitAgents) but BEFORE EndRun persists the
	// terminal status — registered here, between the two, so LIFO gives exactly that order (cancel, drain,
	// close remaining waits, persist, lifecycle hooks).
	defer o.closeMeshWaitsIfFinalized()
	defer o.concurrency.WaitAgents() // выполнится ПОСЛЕ cancel (LIFO) — сначала отмена, потом ожидание
	defer cancel()
	if err := o.opts.Store.BeginRun(); err != nil {
		return fmt.Errorf("persist run start: %w", err)
	}

	o.emitLifecycle(lifecyclehooks.Event{Type: o.flowStartEvent()})

	// Restore review-pause before scheduling. On a corrupt resuming marker, hold activation and keep the
	// dashboard available for inspection.
	// The marker is read first, its owners are respawned only after the team exists: every way a stage starts
	// — including a review-pause resume — sees the flow's team from its first turn.
	marker, resuming, err := o.loadReviewPause()
	if err != nil {
		log.Printf("review-pause: recovery error, activation held pending manual intervention: %v", err)
	}
	// A poisoned round must NOT run normal bootstrap at all — activationHeld alone still lets some
	// scheduling side effects through.
	poisoned := errors.Is(err, errReviewPausePoisoned)
	if !poisoned {
		if err := o.initTeam(ctx); err != nil {
			o.setTerminalFlow(lifecyclehooks.EventFlowFailed)
			return fmt.Errorf("team: %w", err)
		}
	}
	if resuming {
		if err := o.resumeReviewPause(ctx, marker); err != nil {
			log.Printf("review-pause: recovery error, activation held pending manual intervention: %v", err)
		}
	}
	if !poisoned {
		o.startPlanningForPending(ctx)
		o.recoverAssistants(ctx)
		o.recoverFeedbackIntents(ctx)
		o.recoverMeshWaits(ctx)
		o.recoverUnreadOfFinishedStages(ctx)
		o.releaseUnanswerableWaits(ctx, o.resumeWaiterAfterTerminalWaitAtStartup)
		o.recoverMeshWaitDeadlines(ctx)
	}
	o.startQuestionPoller(ctx) // file-based dialog poller
	if o.opts.AssistantsEnabled {
		o.startWatchPoller(ctx) // Этап 5: checkpoints/on_completion watcher firing
	}
	if o.opts.AssistantsEnabled && !poisoned {
		// Планировщик лупов останавливается и ожидается раньше cancel и WaitAgents (LIFO defer): тик не
		// переживёт ран и не пишет в журнал после закрытия Store.
		stopLoops := o.startLoopScheduler(ctx)
		defer stopLoops()
	}

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
				// The exit decision and "reject every later operation" are one step under the store lock
				// (FinalizeIf): a message, attach or dynamic stage (Этап 9, T9.3) committed a moment before
				// keeps the run alive, and one arriving after is rejected run_finalizing — never accepted and
				// then left without a turn.
				if !o.reviewTxnActive() && o.opts.Store.FinalizeIf(o.runStateAllowsExit) {
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
	case bus.EventPeerReplied:
		return o.onPeerReplied(ctx, ev)
	case bus.EventPeerQuestion:
		return o.onPeerQuestion(ctx, ev)
	case bus.EventPeerWaitEnded:
		return o.onPeerWaitEnded(ctx, ev)
	}
	return nil
}

// onAgentCompleted consumes completions from runWithRetry and script stages. It applies phase transitions
// and unblocks dependents.
func (o *Orchestrator) onAgentCompleted(ctx context.Context, ev bus.Event) error {
	agentType, _ := ev.Data.(string)
	st := o.opts.Store.Snapshot().Stages[ev.StageID]
	current := st.Status
	// A completion carries the epoch of the turn that completed (completeAgent): it acts only while that turn
	// still owns the stage — or for the Revise of that very turn (the feedback branch below). Every transition
	// it makes is fenced with the epoch too, so a takeover after this check still wins.
	fence := ev.Epoch
	if fence != nil && st.Epoch != *fence && !reviseOfTurn(st, *fence) {
		return nil
	}

	// Check all phases for fresh questions.
	if current != state.StatusAwaitingUserInput && o.hasOpenQuestion(ev.StageID, agentType) {
		o.preAskPhase.Store(ev.StageID, agentType)
		o.Trigger(ev.StageID, bus.EvAskUser, bus.GuardCtx{Phase: agentType, ExpectEpoch: fence}, "")
		return nil
	}

	switch agentType {
	case phasePlanning:
		// Ignore stale completion if stage already left planning state. Rejecting it here would strand planning
		// forever with no agent process left to retry.
		if current != state.StatusPlanning && current != state.StatusRetrying && current != state.StatusAwaitingUserInput {
			return nil
		}
		if _, ok := o.Trigger(ev.StageID, bus.EvPlanReady, bus.GuardCtx{ExpectEpoch: fence}, ""); !ok && fence != nil && !o.turnOwnsStage(ev.StageID, *fence) {
			return nil // the stage was taken over after the check above
		}
		// auto_approve: true on the stage config wins regardless of
		// dashboard/--require-approval — checked before the headless branch.
		if stage := o.reg().Stage(ev.StageID); stage != nil && o.autoApproveIfConfigured(ctx, *stage) {
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
			stage := o.reg().Stage(ev.StageID)
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
		// A teammate wrote while the agent was working: one more turn to answer instead of done.
		if o.continueForUnreadMessages(ctx, ev.StageID, agentType, fence) {
			return nil
		}
		// Фаза завершена: implementation-агент дошёл до конца, либо
		// autonomous-трек написал execution_summary.md → переводим в done.
		o.completeStageFenced(ctx, ev.StageID, current, "", fence)
	case phaseScript:
		// Script-стадия завершилась (runScriptStage): нет revising-гонки (interruptChans не регистрируется вне
		// runWithRetry, Revise() на script-стадию не осмыслен) — просто done, как остальные фазы.
		o.completeStageFenced(ctx, ev.StageID, current, "", fence)
	case phaseReview:
		// Standalone review публикует собственный completion; встроенный review завершается через implementation
		// и сюда не попадает.
		if current == state.StatusRunning || current == state.StatusRevising {
			if o.continueForUnreadMessages(ctx, ev.StageID, agentType, fence) {
				return nil
			}
			o.completeStageFenced(ctx, ev.StageID, current, "review complete", fence)
		}
	default:
		// unknown agent type: no status change needed
	}
	return nil
}

// completeStage finalizes a stage whose main executor reached done — UNLESS Этап 7's durable completion
// barrier (T7.2) finds at least one open required helper task under it, in which case the stage instead
// settles into awaiting_assistants and this function returns WITHOUT running any completion side effect
// (after-hook/reflection/cascade): those only ever run once EvComplete itself actually fires, whether that
// happens right here or later via maybeCompleteAfterAssistants once the blocking task resolves.
//
// Order (§5.3's "durable completion barrier"): (1) the caller already ran the main executor's own file-probe
// AND the independent verify gate (gateWithVerify/RunVerification) before ever reaching this function — the
// barrier below is a SEPARATE, ADDITIONAL gate, it does not replace verify; (2) the required-task check below
// runs regardless of o.opts.AssistantsEnabled, because that flag only controls whether NEW tasks may be
// created/scheduled — an EXISTING durable requirement from before the flag was flipped off must still be
// honored (TestCollaborationDisabledBeforeResumeDoesNotSkipRequirement), and RequiredBlocker is a cheap,
// file-I/O-free read that is a correct no-op when no task was ever attached.
func (o *Orchestrator) completeStage(ctx context.Context, stageID string, current state.StageStatus, reason string) {
	o.completeStageFenced(ctx, stageID, current, reason, nil)
}

// completeStageFenced is completeStage for a completion bound to a turn's execution epoch: its transitions
// commit only while that turn still owns the stage (nil = not bound).
func (o *Orchestrator) completeStageFenced(ctx context.Context, stageID string, current state.StageStatus, reason string, fence *uint64) {
	if current != state.StatusRunning && current != state.StatusRetrying && current != state.StatusAwaitingUserInput && current != state.StatusAwaitingAssistants {
		return
	}
	if blocker, open := o.opts.Store.Snapshot().Assistants.RequiredBlocker(stageID); open {
		if current != state.StatusAwaitingAssistants {
			// First time the main executor finishes: commit the barrier durably. triggerWithSeq's own funnel
			// (orchestrator.go) forces any orphaned checkpoints/on_completion watcher into its bounded final
			// pass right after this commits — see finalizeWatchersForStage's EvAwaitAssistants case — so a
			// required watcher can never be left "watching" forever once there is no more main-agent output to
			// wait for.
			o.Trigger(stageID, bus.EvAwaitAssistants, bus.GuardCtx{ExpectEpoch: fence}, "awaiting required assistant: "+string(blocker.ID))
		}
		// Already in awaiting_assistants and still open: nothing to do until the blocking task resolves — see
		// maybeCompleteAfterAssistants, called from finishTaskFrom's terminal-transition hook.
		return
	}
	// current — снимок статуса, прочитанный вызывающим ДО этого момента: между чтением и CAS сюда мог влезть
	// Pause/Revise на другой горутине и увести стадию из running/retrying.
	if _, ok := o.Trigger(stageID, bus.EvComplete, bus.GuardCtx{ExpectEpoch: fence}, reason); !ok {
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

// maybeCompleteAfterAssistants re-attempts completeStage for stageID if (and only if) it is currently
// awaiting_assistants — the counterpart to the barrier commit above, called whenever a task's terminal
// transition COULD have just closed a stage's required gate (finishTaskFrom). A stage not in
// awaiting_assistants is a correct, cheap no-op (most task completions have nothing to do with any stage
// sitting in that status).
func (o *Orchestrator) maybeCompleteAfterAssistants(ctx context.Context, stageID string) {
	if o.currentStatus(stageID) != state.StatusAwaitingAssistants {
		return
	}
	o.completeStage(ctx, stageID, state.StatusAwaitingAssistants, "required assistants satisfied")
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

	stage := o.reg().Stage(ev.StageID)
	if stage == nil {
		return nil
	}

	// Используем корректную фазу из preAskPhase, а не сырую из имени файла вопроса.
	phase := o.popPreAskPhase(ev.StageID, rawPhase)

	// Agent exited before the user answered. Restart it so it can read
	// answer.json (bash loop exits immediately since the file now exists).
	return o.resumeStageForPhase(ctx, *stage, phase, bus.EvUserAnswered, 0)
}

// resumeStageForPhase triggers ev (dispatching, via phaseDispatch, to the FSM status that phase implies)
// and spawns the matching fresh agent for it — the one "resume a parked stage by phase" primitive shared by
// onUserAnswered (human answered) and onPeerReplied (Agent Mesh Phase 5, T5.3: a colleague replied). Each
// caller is responsible for its own precondition checks (current status, that a phase was actually
// stashed) before calling this.
//
// parkSeq, when non-zero, binds the resume to one park (state.StageState.Seq of the awaiting_peer transition):
// the transition commits only while the stage is still in that park.
func (o *Orchestrator) resumeStageForPhase(ctx context.Context, stage flow.Stage, phase string, ev bus.FSMEvent, parkSeq uint64) error {
	var expectSeq *uint64
	if parkSeq != 0 {
		expectSeq = &parkSeq
	}
	switch phase {
	case phasePlanning:
		if _, ok := o.Trigger(stage.ID, ev, bus.GuardCtx{Phase: phasePlanning, ExpectStageSeq: expectSeq}, ""); !ok {
			return nil // another resume won the transition — or the stage left the status: nothing to spawn
		}
		o.spawnKind(ctx, stage, kindPlanning, o.runPlanningAgent)
	case phaseImplementation:
		if _, ok := o.Trigger(stage.ID, ev, bus.GuardCtx{Phase: phaseImplementation, ExpectStageSeq: expectSeq}, ""); !ok {
			return nil // another resume won the transition — or the stage left the status: nothing to spawn
		}
		o.spawnKind(ctx, stage, kindImplementation, o.runImplementationAgent)
	case phaseReview:
		if _, ok := o.Trigger(stage.ID, ev, bus.GuardCtx{Phase: phaseReview, ExpectStageSeq: expectSeq}, ""); !ok {
			return nil // another resume won the transition — or the stage left the status: nothing to spawn
		}
		o.spawnKind(ctx, stage, kindReview, o.runReviewAgent)
	case phaseAutonomous:
		if _, ok := o.Trigger(stage.ID, ev, bus.GuardCtx{Phase: phaseAutonomous, ExpectStageSeq: expectSeq}, ""); !ok {
			return nil // another resume won the transition — or the stage left the status: nothing to spawn
		}
		o.spawnKind(ctx, stage, kindAutonomous, o.runAutonomousAgent)
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

// resolvePlanSource — откуда скопировать готовый `plan:` стадии. "./x" ищется в run-каталогах зависимостей,
// иначе (как и любой относительный путь) — в корне проекта агентов projectDir; абсолютный путь — как есть.
func resolvePlanSource(runDir, projectDir string, stage flow.Stage) string {
	if stage.Plan == "" || filepath.IsAbs(stage.Plan) {
		return stage.Plan
	}
	rel := strings.TrimPrefix(stage.Plan, "./")
	if strings.HasPrefix(stage.Plan, "./") {
		for _, depID := range stage.DependsOn {
			candidate := filepath.Join(runDir, depID, rel)
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	return filepath.Join(projectDir, rel)
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
