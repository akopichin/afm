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
	// Verify — assets/prompts/verify.md, инструкция AI-verify агенту (см.
	// pkg/prompts.BuildVerify, RunVerification). Отдельно от остальных фаз:
	// verify не входит в flow.Phases(), это ортогональный проход ПОСЛЕ
	// заявленного стадией завершения.
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
	// CurrentFileSHA резолвит текущий content_sha файла (root/path — та же
	// пара, что в state.ReviewNote) для renderReviewFeedback — сравнить с
	// content_sha ноты на момент injection и пометить дрифт. nil (по
	// умолчанию) означает "нет доступа к воркспейсу" — see currentFileSHA:
	// каждая нота рендерится с "(⚠ file unavailable at injection)". Сервер
	// подключит workspace-backed резолвер отдельной задачей.
	CurrentFileSHA func(root, path string) (string, bool)
	// ResolveFile резолвит {root,path} (+ line, если нота построчная) в
	// ResolvedFile для AddNote (reviewpause.go) — display/reference,
	// текущий content_sha (сравнить с тем, что прислал клиент — детект
	// stale-правки) и, для построчной ноты, текст строки + её валидность.
	// nil (по умолчанию в хостовом режиме — там нет file browser) означает
	// "файл не резолвится вообще": AddNote возвращает ErrStaleContent. Реальный
	// workspace-backed резолвер подключается отдельной задачей (см. cmd/afm).
	ResolveFile func(root, path string, line *int) (ResolvedFile, bool)
	// Accounting — durable usage.jsonl store for this run (nil = accounting
	// disabled, e.g. Open failed hard on the host — see cmd/afm/run.go). Every
	// LLM invocation's OnUsage callback (runnerFor, memory pipeline) resolves
	// through recordUsage, which no-ops safely when this is nil. Observability
	// only: never gates the FSM, never fails the run.
	Accounting *accounting.Store
	// Lifecycle hooks (Phase 1): FlowName/RunID/Resumed питают payload,
	// Hooks — собранный dispatcher (nil = хуков нет). Сборка — cmd/afm/run.go.
	FlowName string
	RunID    string
	Resumed  bool
	Hooks    *lifecyclehooks.Dispatcher
}

// ResolvedFile — то, что Options.ResolveFile возвращает про файл, к которому
// привязывается review-нота: отображаемый путь и ссылка (те же поля, что
// renderReviewFeedback уже кладёт в state.ReviewNote), текущий content_sha
// (для сравнения с content_sha, который прислал клиент — детект stale-правки
// между тем, как пользователь открыл файл в браузере, и тем, как он отправил
// ноту) и, для построчной ноты (line != nil на входе), текст этой строки
// (LineText) и попадает ли номер строки в текущий файл (InRange). Для файловой
// ноты (line == nil на входе) LineText/InRange не используются вызывающим кодом.
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
	// interruptChans хранит канал прерывания (stageID → chan struct{}, буфер
	// 1) на время КОНКРЕТНОЙ попытки RunAgent — создаётся в начале
	// runWithRetry, удаляется по её завершении (успешном или нет). Revise
	// (agent_suggest) шлёт в этот канал, чтобы запросить graceful-прерывание
	// текущего вызова агента через executor.Config.InterruptCh.
	interruptChans sync.Map
	// stageLeases хранит *concurrency.Lease текущего исполнительского раннера
	// стадии (stageID → *concurrency.Lease) — на время его выполнения (см.
	// spawnKind: Store сразу после SpawnAgentLease отдаёт lease, Delete в
	// defer вокруг run(ctx,s)). AI-verify (RunVerification) достаёт lease
	// отсюда, чтобы временно переключить единственный удерживаемый слот
	// командного семафора на команду verify-агента (Lease.SwapTo) и вернуть
	// его автору по завершении — afm никогда не держит два слота разом при
	// переходе автор→верификатор. Раннеры планирования тоже получают lease
	// (через тот же spawnKind), но никогда им не пользуются (планирование не
	// вызывает RunVerification) — безвредно. Отсутствие записи (ok=false,
	// напр. resumeInteractiveAgent — путь резюма интерактивной стадии в обход
	// spawnKind) — безопасная деградация: verify просто не переносит слот,
	// как было до V3/V4b.
	stageLeases sync.Map
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

	// mem — двухфазный движок конвейера памяти (reflect/aggregate/prioritize/
	// update), собранный в New(): CaptureStage (per-stage каптура датасета,
	// см. maybeRunReflection) и Finalize (сборка всех датасетов рана в
	// project/per-stage файлы под общим memory-lock, см. runEndOfRunMemory).
	// Реальный раннер агента внутри — memorypipeline.NewExecRunner; тесты
	// подменяют его через memorypipeline.WithRunner при построении Pipeline.
	mem *memorypipeline.Pipeline

	// hooks — lifecycle dispatcher (observer-only; nil-safe).
	hooks *lifecyclehooks.Dispatcher

	// runVerifyAgent запускает один AI-verify шаг (см. RunVerification,
	// verify.go) — инъектируемый seam, тот же приём, что o.spawnJSONFix/
	// memorypipeline.AgentRunner: продакшн-реализация (execVerifyAgent)
	// строит свежий *executor.Executor через runnerForVerify и зовёт его
	// RunVerifyAgent; тесты подменяют этот field напрямую фейковой функцией,
	// не поднимая реальный subprocess.
	runVerifyAgent verifyAgentRunner

	// runVerifyShell исполняет один shell-шаг AI-verify (см. RunVerification,
	// verify.go) — инъектируемый seam, тот же приём, что runVerifyAgent выше:
	// продакшн-реализация — package-level runVerifyShellCommand; тесты
	// подменяют этот field фейком для детерминированного воспроизведения
	// тайминговых гонок (D3a/D4), не завязываясь на реальный OS-уровневый race.
	runVerifyShell verifyShellRunner

	// terminalFlow — финальное flow-событие, установленное одним из выходов
	// Run; эмитится finalizeLifecycle ПОСЛЕ остановки продюсеров (single
	// writer — горутина Run, отдельная синхронизация не нужна).
	terminalFlow lifecyclehooks.EventType

	// pollerDone закрывается горутиной startQuestionPoller при её выходе
	// (defer close). finalizeLifecycle bounded-ждёт этот канал ПЕРЕД эмитом
	// терминального flow-события: сама горутина не входит в agentWG/
	// WaitAgents (она наблюдатель, а не агент), поэтому без явного ожидания
	// она могла дожить до момента ПОСЛЕ терминального emit+Flush и опубликовать
	// stage-событие (EvAskUser и т.п.) уже после flow_finished/failed —
	// нарушая гарантию "терминальное событие — последнее". cancel() в Run
	// выполняется раньше (LIFO) — к моменту ожидания ctx поллера уже Done,
	// так что ожидание почти всегда моментальное; таймаут — просто safety cap.
	// nil, если startQuestionPoller не был вызван (не должно случаться в
	// проде, но тесты строят Orchestrator без Run) — finalizeLifecycle это
	// учитывает.
	pollerDone chan struct{}

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
	// Continue can race startup recovery. Keep its status transition and the
	// per-process continuation marker atomic with recovery's status read.
	continueMu           sync.Mutex
	continuedThisProcess sync.Map

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

	// runnerKind хранит, каким раннером (planning/implementation/review/
	// autonomous) сейчас активирована стадия — stageID -> одна из констант
	// kindPlanning/kindImplementation/kindReview/kindAutonomous. Нужно
	// последующей задаче (детерминированный resume-dispatch после паузы), чтобы
	// не гадать по файлам на диске, какой раннер запускать заново. Выставляется
	// в начале каждого из 8 run*Agent/run*WithFeedback раннеров (agents.go) и
	// снимается централизованно в triggerWithSeq при переходах EvComplete/
	// EvFail/EvAskUser (стадия завершилась, упала или ушла ждать пользователя —
	// раннер для неё больше не актуален). НЕ снимается на EvScheduleRetry
	// (retrying) — значение должно пережить backoff ретрая.
	runnerKind sync.Map

	// resumeContextOnce хранит однократный флаг stageID -> взведён: следующий
	// вызов runWithRetry для этой стадии должен собрать buildRetryContext
	// (блок "Previously completed actions") уже на attempt 0, а не только
	// начиная с attempt > 0 (обычное ретрай-поведение). Нужно для Continue
	// после паузы non-interactive стадии (Task 7 фичи "review notes"): агент
	// перезапускается заново (attempt 0), но должен видеть, что уже сделано,
	// иначе повторяет работу с нуля. Взводится armResumeContext, снимается
	// (LoadAndDelete) внутри runWithRetry — однократно, следующий resume той
	// же стадии должен быть заармлен заново явно.
	resumeContextOnce sync.Map

	// retryCASBarrier — тест-сейм (nil в проде): вызывается в retryStage сразу
	// после проверки статуса failed и ДО CAS EvManualRetry, позволяя тесту
	// детерминированно смоделировать проигрыш CAS (перевести стадию из failed
	// на другой горутине) и проверить, что проигравший вызов не чистит
	// session/jsonl победителя. Инъектируется через SetRetryCASBarrierForTest.
	retryCASBarrier func(stageID string)

	// verifyOutcomeGuardHook — тест-сейм (nil в проде): вызывается в
	// retry.go's commitVerifyFailure НЕПОСРЕДСТВЕННО ПЕРЕД атомарным
	// Trigger(EvVerifyFail) — т.е. ПОСЛЕ дешёвого fast-path чтения
	// (verifyOutcomeStillOwned), но ДО самого CAS. Это позволяет тесту
	// детерминированно закоммитить позднюю гонку Pause()/Revise() ИМЕННО в
	// TOCTOU-зазоре между чтением и коммитом (H1, 6-е код-ревью) и проверить,
	// что EvVerifyFail's ограниченный From атомарно отбрасывает переход
	// (ok=false), а не полагается на повторное чтение статуса. Инъектируется
	// через SetVerifyOutcomeGuardHookForTest.
	verifyOutcomeGuardHook func(stageID string)

	// flowPauseMu защищает составные решения PauseFlow (и последующей задачи
	// финализации ревью-паузы) от гонок между конкурентными HTTP-вызовами —
	// сами поля activationHeld/finalizing/reviewMarker читаются lock-free
	// через atomic, мьютекс нужен только там, где несколько полей меняются
	// как одна операция (см. PauseFlow, reviewpause.go).
	flowPauseMu sync.Mutex
	// activationHeld — true, пока активен ревью-режим: НИ ОДНА новая стадия
	// не должна активироваться (см. activationBlocked/guard-точки в
	// scheduling.go и recovery.go). Уже бегущие стадии не трогаются —
	// намеренно не меняется concurrency.shouldRun, чтобы не подвесить их.
	activationHeld atomic.Bool
	// finalizing — true, пока идёт финализация ревью-транзакции (следующая
	// задача фичи "review notes"); PauseFlow отклоняет новый раунд, пока флаг
	// взведён (см. ErrRunFinalizing).
	finalizing atomic.Bool
	// reviewMarker — in-memory кэш текущего маркера паузы ревью (nil = его
	// нет). atomic.Pointer делает reviewTxnActive() lock-free. Пишется
	// PauseFlow (reviewpause.go) после успешной записи маркера на диск.
	reviewMarker atomic.Pointer[state.PauseMarker]

	// reviewResumed (stageID → struct{}) помечает owner'ов, которых уже
	// возобновил recovery-путь review-паузы: recoverReviewPause синхронно
	// прогоняет runResumeTransaction (маркер state=resuming), выводя каждого
	// owner'а из paused и спавня раннер, ПОСЛЕ чего Run() безусловно зовёт
	// startPlanningForPending — тот увидел бы тех же owner'ов уже в
	// running/revising и через resumeStageAtStatus заспавнил бы ВТОРОГО агента
	// в ту же стадию. Обе фазы (recovery, затем bootstrap) исполняются на одной
	// горутине Run() строго последовательно, поэтому обычная sync.Map без
	// доп. синхронизации здесь race-free.
	reviewResumed sync.Map

	// testRunnerHook — тест-сейм (nil в проде) для resumeOwner (Task 12
	// фичи "review notes", reviewpause.go): если задан, resumeOwner вызывает
	// его вместо реального Trigger+SpawnAgent, чтобы тест мог наблюдать, какой
	// раннер (resume_kind) и в каком режиме (withFeedback) был бы выбран, без
	// запуска настоящего процесса-агента.
	testRunnerHook func(kind string, withFeedback bool)

	// testClearReviewArtifacts — тест-сейм (nil в проде) для clearReviewArtifacts
	// (reviewpause.go): если задан, runResumeTransaction зовёт его вместо
	// реального удаления review-notes.json/notes-pause.json, чтобы тест мог
	// смоделировать сбой durable-cleanup и проверить fail-closed поведение
	// (in-memory marker НЕ сбрасывается, пока файлы не удалены).
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

// clearRunnerKind снимает отметку о раннере стадии (завершилась/упала/ждёт
// пользователя — см. triggerWithSeq). Значение просто отсутствует до
// следующего setRunnerKind, ничего не ломает.
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

// armResumeContext взводит однократный флаг resumeContextOnce для стадии
// stageID — следующий вызов runWithRetry соберёт retry-контекст ("Previously
// completed actions") уже на attempt 0 (см. комментарий у поля
// resumeContextOnce).
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

// recordUsage returns an executor.Config.OnUsage callback bound to one
// (stageID, phase, scope) attribution: every runnerFor call site and the
// memory pipeline (via memorypipeline.AgentConfig.OnUsage) build their
// closure through this single seam. A nil Options.Accounting (accounting
// disabled, or Open failed hard on the host) makes the closure a no-op.
// A write failure is logged and otherwise ignored — accounting.Store.Append
// already self-marks the store Unavailable on its own; this must NEVER
// setFatal, touch the FSM, or trigger a retry. Usage is observability only.
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
	o.hooks = opts.Hooks
	o.spawnJSONFix = o.runJSONFixAgent
	o.runVerifyAgent = o.execVerifyAgent
	o.runVerifyShell = runVerifyShellCommand
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
	})
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
		// runnerKind больше не актуален: стадия завершилась (EvComplete),
		// провалилась (EvFail/EvVerifyFail) или ушла ждать пользователя
		// (EvAskUser) — во всех случаях раннер, который её вёл, для неё
		// закончил работу. Единая точка вместо ~15 разрозненных call site'ов
		// Trigger(EvFail)/Trigger(EvAskUser) по всему пакету: Trigger/
		// triggerWithSeq — общий funnel для всех переходов FSM (см. комментарий
		// выше), так что проверка здесь не пропустит ни один существующий или
		// будущий call site. EvScheduleRetry (retrying) сюда намеренно не
		// входит — значение должно пережить backoff.
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
	// finalizeLifecycle зарегистрирован ПЕРВЫМ — LIFO исполнит его ПОСЛЕДНИМ,
	// после cancel()+WaitAgents(): продюсеры lifecycle-событий (агенты) уже
	// остановлены к моменту терминального emit+flush (codex MAJ#7) —
	// раньше инлайн-flush на выходах Run наблюдал sent==done ДО того, как
	// отменённые агенты успевали эмитить последнее событие.
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

	// recoverReviewPause MUST run before startPlanningForPending: it
	// re-establishes activationHeld/reviewMarker from the durable
	// notes-pause.json marker (or finishes an interrupted resume
	// transaction) so a frozen review owner is never picked up by the
	// ordinary scheduler below. A non-nil error here means the marker was a
	// corrupt `resuming` one (fail-closed, see recoverReviewPause's doc
	// comment) — activationHeld is already set in that case, which is
	// enough to keep new stages from starting; we log prominently and keep
	// the run (and its dashboard/HTTP server) alive rather than aborting
	// Run() outright, so an operator can inspect the quarantined file and
	// the paused stages instead of losing all visibility into the run.
	poisoned := false
	if err := o.recoverReviewPause(ctx); err != nil {
		log.Printf("review-pause: recovery error, activation held pending manual intervention: %v", err)
		// A poisoned round (corrupt, un-recoverable resuming marker) must NOT run
		// normal bootstrap at all — activationHeld alone still lets some
		// scheduling side effects through. Skip startPlanningForPending entirely
		// (the poison reviewMarker keeps shouldExit() false, so the loop below
		// still keeps the dashboard + question poller alive) so an operator can
		// inspect and clear the durable poison breadcrumb by hand.
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
				// The finalize decision must be atomic with PauseFlow (see
				// reviewpause.go): both take flowPauseMu, so if a review-pause
				// round starts concurrently right in this window, either it
				// sees finalizing==true first (and backs off with
				// ErrRunFinalizing) or we see reviewTxnActive()==true first
				// (and stay alive for the reviewer) — never both proceeding.
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

// hasFailedStage — есть ли в снапшоте Failed-стадия. Классификатор codex
// MAJ#8: отмена ctx поверх рана, заблокированного failed-стадией (дашборд
// держит Run живым для retry), — это flow_failed, а не flow_interrupted.
func (o *Orchestrator) hasFailedStage() bool {
	for _, st := range o.opts.Store.Snapshot().Stages {
		if st.Status == state.StatusFailed {
			return true
		}
	}
	return false
}

// finalizeLifecyclePollerWait — safety cap на ожидание завершения горутины
// question poller (см. поле pollerDone). cancel() уже выполнен к этому
// моменту (LIFO в Run), так что поллер обычно возвращается почти сразу —
// таймаут защищает только от аномально долгой текущей итерации pollQuestions.
const finalizeLifecyclePollerWait = 5 * time.Second

// finalizeLifecycle эмитит терминальное flow-событие и bounded-ждёт
// опустошения очередей dispatcher. Вызывается ТОЛЬКО из defer, живёт после
// cancel()+WaitAgents() — продюсеры событий (агенты) уже остановлены.
// Раньше инлайн-flush на выходах Run наблюдал sent==done ДО того, как
// отменённые агенты успевали эмитить последнее (codex MAJ#7).
//
// Question poller ждём ЗДЕСЬ отдельно (WaitAgents его не покрывает — это
// наблюдатель, а не агент, см. поле pollerDone): иначе горутина поллера
// могла эмитить stage-событие ПОСЛЕ терминального flow-события, нарушая
// гарантию "терминальное событие — последнее" (finding #1 финального ревью).
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

// onAgentCompleted consumes completions from runWithRetry and script stages.
// It applies phase transitions and unblocks dependents. The open-question gate
// below also covers non-interactive phases that runWithRetry does not gate.
func (o *Orchestrator) onAgentCompleted(ctx context.Context, ev bus.Event) error {
	agentType, _ := ev.Data.(string)
	current := o.currentStatus(ev.StageID)

	// Check all phases for fresh questions. AwaitingUserInput means the poller
	// already parked this invocation; its remaining question is a stale tail,
	// so completion wins (the same precedence as runWithRetry).
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
		// Script-стадия завершилась (runScriptStage): нет revising-гонки
		// (interruptChans не регистрируется вне runWithRetry, Revise() на
		// script-стадию не осмыслен) — просто done, как остальные фазы.
		o.completeStage(ctx, ev.StageID, current, "")
	case phaseReview:
		// Standalone review-раннер (runReviewAgent/runReviewWithFeedback,
		// resume-dispatcher Task 12 фичи "review notes", reviewpause.go)
		// публикует своё собственное EventAgentCompleted{Data: phaseReview}
		// через тот же runWithRetry, что и остальные фазы — раньше review
		// здесь всегда падал в default и НЕ завершал стадию, оставляя её
		// висеть в running/revising навсегда. Inline review (вызванный ИЗ
		// runImplementationAgent/runImplementationWithFeedback как
		// подшаг implementation-раннера, agents.go) своего
		// EventAgentCompleted не публикует — он идёт напрямую через
		// rr.RunAgent, и вся implementation-стадия завершается через ветку
		// phaseImplementation выше, так что эта ветка её не задваивает.
		// Revising допущен по аналогии с agent_suggest-гонкой в ветке
		// phaseImplementation/phaseAutonomous выше (конкурентный Revise()
		// мог успеть перевести running->revising, пока review-агент уже
		// возвращался естественно) — completeStage сама отклонит переход,
		// если Revising не входит в её From-набор EvComplete, так что это
		// безопасный no-op, а не задваивание завершения.
		if current == state.StatusRunning || current == state.StatusRevising {
			o.completeStage(ctx, ev.StageID, current, "review complete")
		}
	default:
		// unknown agent type: no status change needed
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
	// script_after (if any) runs first, and reflection only starts once it
	// returns — so a stage's reflect capture always sees after.log if the
	// stage has one. The unblock cascade below still runs immediately; only
	// reflection is deferred behind the hook (see maybeRunAfterHookThen).
	o.maybeRunAfterHookThen(ctx, stageID, func(c context.Context) { o.maybeRunReflection(c, stageID) })
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

// reviewTxnActive reports whether a review-pause marker is currently present
// (in-memory cache, lock-free read). Used by InjectNotesAndResume/
// CancelNotesAndResume's async worker (reviewpause.go) to signal completion
// of the resume transaction back to the caller/tests.
func (o *Orchestrator) reviewTxnActive() bool { return o.reviewMarker.Load() != nil }

// activationBlocked reports whether new stage activations must be held
// (review mode is on). Already-running stages are never affected — callers
// only skip the transition that would START a new stage.
func (o *Orchestrator) activationBlocked() bool { return o.activationHeld.Load() }

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
