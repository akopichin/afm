package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// newExitOnFailStages — оркестратор без Run: переходы вызываются напрямую, ExitOnFail включён.
func newExitOnFailStages(t *testing.T, ids ...string) (*Orchestrator, *state.Store) {
	t.Helper()
	stages := make([]flow.Stage, len(ids))
	for i, id := range ids {
		stages[i] = flow.Stage{ID: id, Command: "claude"}
	}
	o, store := newMultiStageBarrierTestOrchestrator(t, stages)
	o.opts.ExitOnFail = true
	return o, store
}

func failStageForExit(o *Orchestrator, id, reason string) bool {
	_, ok := o.Trigger(id, bus.EvFail, bus.GuardCtx{}, reason)
	return ok
}

func TestExitOnFailCommittedTransitionRecordsFirstFailure(t *testing.T) {
	o, store := newExitOnFailStages(t, "a", "b")
	if o.stopError() != nil {
		t.Fatal("a run without failures must have no stop reason")
	}
	o.Trigger("a", bus.EvReady, bus.GuardCtx{}, "")
	o.Trigger("a", bus.EvStartRun, bus.GuardCtx{}, "")
	o.Trigger("a", bus.EvScheduleRetry, bus.GuardCtx{}, "transient")
	if o.stopError() != nil {
		t.Fatal("a retrying stage is not a failed stage")
	}

	if !failStageForExit(o, "a", "boom") || !failStageForExit(o, "b", "later") {
		t.Fatal("fail transitions were rejected")
	}
	var failed *StageFailedError
	if !errors.As(o.stopError(), &failed) || failed.StageID != "a" || failed.Reason != "boom" || failed.Seq != store.Snapshot().Stages["a"].Seq {
		t.Fatalf("stop error = %+v, want the first failure of a with its seq", o.stopError())
	}
}

func TestExitOnFailDisabledRecordsNothing(t *testing.T) {
	o, _ := newExitOnFailStages(t, "a")
	o.opts.ExitOnFail = false
	failStageForExit(o, "a", "boom")
	if o.stopError() != nil || o.exit.failure.Load() != nil {
		t.Fatal("without the option a failed stage must not become a stop reason")
	}
}

func TestExitOnFailCASMissIsNotAFailure(t *testing.T) {
	o, _ := newExitOnFailStages(t, "a")
	o.Trigger("a", bus.EvReady, bus.GuardCtx{}, "")
	o.Trigger("a", bus.EvStartRun, bus.GuardCtx{}, "")
	staleEpoch := uint64(99)

	if _, ok := o.Trigger("a", bus.EvFail, bus.GuardCtx{ExpectEpoch: &staleEpoch}, "stale"); ok {
		t.Fatal("a fenced fail from a stale epoch must lose")
	}
	if o.stopError() != nil {
		t.Fatal("a lost CAS must not be reported as a failed stage")
	}
}

// blockedStack возвращает стек горутины, которая сейчас стоит на sync.Mutex внутри fn. Тесты сериализации ждут
// именно блокировки, а не времени: без mutex горутина не блокируется и тест падает по early.
func blockedStack(fn string) (string, bool) {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	for _, goroutine := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(goroutine, fn) && strings.Contains(goroutine, "sync.(*Mutex).") {
			return goroutine, true
		}
	}
	return "", false
}

// awaitBlocked ждёт, пока горутина в fn встанет на mutex. early — результат этой горутины: пришёл раньше —
// значит, она не ждала и сериализации нет.
func awaitBlocked[T any](t *testing.T, fn string, early <-chan T) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if stack, ok := blockedStack(fn); ok {
			return stack
		}
		select {
		case got := <-early:
			t.Fatalf("%s returned %v without waiting for the exit mutex", fn, got)
		default:
		}
		runtime.Gosched()
	}
	t.Fatalf("no goroutine blocked on a mutex inside %s", fn)
	return ""
}

func failedTransitions(t *testing.T, store *state.Store) int {
	t.Helper()
	history, err := store.History()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tr := range history {
		if tr.To == state.StatusFailed {
			n++
		}
	}
	return n
}

// Второй провал не может закоммититься, пока первый между durable-коммитом и записью причины: первым в
// stopError остаётся тот, кто закоммитился раньше.
func TestExitOnFailConcurrentFailures(t *testing.T) {
	o, store := newExitOnFailStages(t, "first", "second")
	committed, release := make(chan struct{}), make(chan struct{})
	o.exitOnFailCommittedHook = func(stageID string) {
		if stageID == "first" {
			close(committed)
			<-release
		}
	}
	firstDone, secondDone := make(chan bool, 1), make(chan bool, 1)
	go func() { firstDone <- failStageForExit(o, "first", "boom first") }()
	<-committed

	go func() { secondDone <- failStageForExit(o, "second", "boom second") }()
	awaitBlocked(t, "applyTransition", secondDone)
	if got := failedTransitions(t, store); got != 1 {
		t.Fatalf("failed transitions while the first commit is held = %d, want 1: the second must wait for the mutex", got)
	}

	close(release)
	if !<-firstDone || !<-secondDone {
		t.Fatal("fail transitions were rejected")
	}
	var failed *StageFailedError
	if !errors.As(o.stopError(), &failed) || failed.StageID != "first" {
		t.Fatalf("stop error = %+v, want the failure of first", o.stopError())
	}
}

func TestExitOnFailSecretsAreRedacted(t *testing.T) {
	o, _ := newExitOnFailStages(t, "a")
	o.opts.SecretValues = []string{"s3cr3t-value-123"}
	failStageForExit(o, "a", "auth failed with s3cr3t-value-123")

	var failed *StageFailedError
	if !errors.As(o.stopError(), &failed) || strings.Contains(failed.Reason, "s3cr3t-value-123") || strings.Contains(failed.Error(), "s3cr3t-value-123") {
		t.Fatalf("stop error = %v, want the secret redacted", o.stopError())
	}
}

func TestExitOnFailStopErrorPrefersStorageFatal(t *testing.T) {
	o, _ := newExitOnFailStages(t, "a")
	failStageForExit(o, "a", "boom")
	fatal := errors.New("write events.jsonl: disk full")
	o.setFatal(fatal)

	if !errors.Is(o.stopError(), fatal) {
		t.Fatalf("stop error = %v, want the storage-fatal error", o.stopError())
	}
}

func TestExitOnFailFailureBeforeRunContext(t *testing.T) {
	o, _ := newExitOnFailStages(t, "a")
	failStageForExit(o, "a", "boom") // HTTP-переход, закоммиченный до Run

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o.installRunContext(ctx, cancel)
	if ctx.Err() == nil {
		t.Fatal("the run context must be cancelled as soon as it is installed")
	}
}

func TestExitOnFailDeadRunContextKeepsInterruption(t *testing.T) {
	o, _ := newExitOnFailStages(t, "a")
	ctx, cancel := context.WithCancel(context.Background())
	o.installRunContext(ctx, cancel)
	cancel() // Ctrl+C

	failStageForExit(o, "a", "killed by the interrupt")
	if o.stopError() != nil {
		t.Fatalf("stop error = %v, want none: stages failing after cancellation are not the cause", o.stopError())
	}
}

// Ctrl+C между durable-коммитом failed и записью причины: failed закоммичен при живом ране и остаётся причиной остановки.
func TestExitOnFailCancelAfterCommitKeepsFailure(t *testing.T) {
	o, _ := newExitOnFailStages(t, "a")
	ctx, cancel := context.WithCancel(context.Background())
	o.installRunContext(ctx, cancel)
	o.exitOnFailCommittedHook = func(string) { cancel() } // Ctrl+C

	failStageForExit(o, "a", "boom")
	var failed *StageFailedError
	if !errors.As(o.stopError(), &failed) || failed.StageID != "a" || failed.Reason != "boom" {
		t.Fatalf("stop error = %v, want the failure committed before the interrupt", o.stopError())
	}
}

// Коммит failed уже durable, причина ещё не записана — финализация ждёт mutex и не может пройти.
func TestExitOnFailFinalizeRace(t *testing.T) {
	o, _ := newExitOnFailStages(t, "x", "y")
	o.Trigger("y", bus.EvReady, bus.GuardCtx{}, "")
	o.Trigger("y", bus.EvStartRun, bus.GuardCtx{}, "")
	o.Trigger("y", bus.EvComplete, bus.GuardCtx{}, "")

	committed, release := make(chan struct{}), make(chan struct{})
	o.exitOnFailCommittedHook = func(string) {
		close(committed)
		<-release
	}
	go failStageForExit(o, "x", "boom")
	<-committed

	finalized := make(chan bool, 1)
	go func() { finalized <- o.finalizeRun() }()
	awaitBlocked(t, "finalizeRun", finalized)
	if o.stopError() != nil {
		t.Fatal("the reason must not be recorded before the committer is released")
	}

	close(release)
	if <-finalized {
		t.Fatal("the run finalized although a failed stage was already committed")
	}
	if o.finalizing.Load() {
		t.Fatal("finalizing must stay false when the failure wins")
	}
	var failed *StageFailedError
	if !errors.As(o.stopError(), &failed) || failed.StageID != "x" {
		t.Fatalf("stop error = %v, want the failure of x", o.stopError())
	}
}

// Решение о финализации принято, finalizing ещё не выставлен: переход ждёт mutex до коммита в журнал и после
// выхода финализации отвергается.
func TestExitOnFailFinalizeRejectsLateTransition(t *testing.T) {
	o, store := newExitOnFailStages(t, "x", "late")
	for _, id := range []string{"x", "late"} {
		o.Trigger(id, bus.EvReady, bus.GuardCtx{}, "")
		o.Trigger(id, bus.EvStartRun, bus.GuardCtx{}, "")
		o.Trigger(id, bus.EvComplete, bus.GuardCtx{}, "")
	}
	before := store.Snapshot().LastSeq
	decided, proceed := make(chan struct{}), make(chan struct{})
	o.finalizeDecidedHook = func() {
		close(decided)
		<-proceed
	}
	finalized := make(chan bool, 1)
	go func() { finalized <- o.finalizeRun() }()
	<-decided

	late := make(chan bool, 1)
	go func() { late <- failStageForExit(o, "late", "too late") }()
	stack := awaitBlocked(t, "applyTransition", late)
	if strings.Contains(stack, "bus.(*FSM).Apply") {
		t.Fatalf("the late transition reached the FSM commit inside the finalization window:\n%s", stack)
	}

	close(proceed)
	if !<-finalized {
		t.Fatal("a run with every stage done must finalize")
	}
	if <-late || o.stopError() != nil {
		t.Fatal("a transition after finalization must be rejected and must not become a failure")
	}
	if got := store.Snapshot().LastSeq; got != before {
		t.Fatalf("journal grew from seq %d to %d after finalization", before, got)
	}
}

func TestExitOnFailNoNewSpawn(t *testing.T) {
	for _, exitOnFail := range []bool{true, false} {
		t.Run(fmt.Sprintf("exit_on_fail=%v", exitOnFail), func(t *testing.T) {
			stages := []flow.Stage{{ID: "a", Command: "claude"}, {ID: "b", Command: "claude"}}
			store, err := state.Open(t.TempDir(), []string{"a", "b"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Close() })
			o := New(Options{RunDir: t.TempDir(), Stages: stages, Store: store, ExitOnFail: exitOnFail})
			t.Cleanup(o.concurrency.WaitAgents)
			failStageForExit(o, "a", "boom")

			var started atomic.Bool
			o.concurrency.SpawnAgent(context.Background(), stages[1], func(context.Context, flow.Stage) { started.Store(true) })
			o.concurrency.WaitAgents()

			if started.Load() == exitOnFail {
				t.Fatalf("stage spawned after the first failure = %v, want %v", started.Load(), !exitOnFail)
			}
		})
	}
}

// --- required helper gate ---

// requiredGate — стадия impl в running и фейковые помощники: первый вызов блокируется до release, остальные падают.
type requiredGate struct {
	o       *Orchestrator
	store   *state.Store
	release chan struct{}
	entered chan struct{} // закрывается, когда первый (блокирующийся) помощник вошёл в runHelperAgent
	calls   atomic.Int32
}

func newRequiredGate(t *testing.T) *requiredGate {
	t.Helper()
	o, store := newAssistantsTestOrchestrator(t, true)
	o.opts.ExitOnFail = true
	activateStage(t, o, assistantsTestStageID)
	g := &requiredGate{o: o, store: store, release: make(chan struct{}), entered: make(chan struct{})}
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if g.calls.Add(1) == 1 {
			close(g.entered)
			awaitRelease(t, g.release)
			return passResultOutcome(), nil
		}
		return executor.HelperOutcome{}, errors.New("helper crashed")
	}
	return g
}

// failAll заставляет все вызовы помощника падать; вызывать до первого Attach.
func (g *requiredGate) failAll() {
	g.o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{}, errors.New("helper crashed")
	}
}

func (g *requiredGate) attach(t *testing.T, opID string, mode assistants.Mode) assistants.TaskID {
	t.Helper()
	id, err := g.o.Attach(context.Background(), AttachRequest{
		OperationID: opID, StageID: assistantsTestStageID, ProfileID: "reviewer", Mode: mode,
		Instruction: "check", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return id
}

// attachBlocker подключает required-помощника и ждёт, пока он войдёт в runHelperAgent: статус running пишется
// раньше вызова, и без этого ожидания блокирующимся стал бы помощник, подключённый следом.
func (g *requiredGate) attachBlocker(t *testing.T, opID string) assistants.TaskID {
	t.Helper()
	id := g.attach(t, opID, assistants.ModeRequired)
	select {
	case <-g.entered:
	case <-t.Context().Done():
		t.Fatal("the blocking helper never started")
	}
	return id
}

func (g *requiredGate) requireStageFailedByHelper(t *testing.T) {
	t.Helper()
	if got := g.o.currentStatus(assistantsTestStageID); got != state.StatusFailed {
		t.Fatalf("stage status = %v, want failed", got)
	}
	var failed *StageFailedError
	if !errors.As(g.o.stopError(), &failed) || failed.StageID != assistantsTestStageID || !strings.Contains(failed.Reason, requiredAssistantFailedReason) {
		t.Fatalf("stop error = %v, want a required-assistant failure of %s", g.o.stopError(), assistantsTestStageID)
	}
}

func TestExitOnFailRequiredAssistantBeforeBarrier(t *testing.T) {
	g := newRequiredGate(t)
	g.failAll()
	id := g.attach(t, "op-1", assistants.ModeRequired)
	waitForTaskStatus(t, g.store, id, assistants.StatusFailed, 2*time.Second)
	g.o.concurrency.WaitAgents()

	g.o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")

	g.requireStageFailedByHelper(t)
}

func TestExitOnFailRequiredAssistantDuringWait(t *testing.T) {
	g := newRequiredGate(t)
	started, fail := make(chan struct{}), make(chan struct{})
	g.o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		close(started)
		awaitRelease(t, fail)
		return executor.HelperOutcome{}, errors.New("helper crashed")
	}
	g.attach(t, "op-1", assistants.ModeRequired)
	<-started
	g.o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
	if got := g.o.currentStatus(assistantsTestStageID); got != state.StatusAwaitingAssistants {
		t.Fatalf("stage status = %v, want awaiting_assistants while the helper works", got)
	}

	close(fail)
	g.o.concurrency.WaitAgents()

	g.requireStageFailedByHelper(t)
}

// Мёртвая задача валит стадию, даже если более ранний blocker жив.
func TestExitOnFailRequiredAssistantMixedBlockers(t *testing.T) {
	g := newRequiredGate(t)
	live := g.attachBlocker(t, "op-1")
	dead := g.attach(t, "op-2", assistants.ModeRequired)
	waitForTaskStatus(t, g.store, dead, assistants.StatusFailed, 2*time.Second)
	if blocker, _ := g.store.Snapshot().Assistants.RequiredBlocker(assistantsTestStageID); blocker.ID != live {
		t.Fatalf("fixture invariant: the first blocker is %s, want the live task %s", blocker.ID, live)
	}

	g.o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")

	g.requireStageFailedByHelper(t)
	t.Cleanup(func() { close(g.release) })
}

// После рестарта (ожидание пережило процесс без политики) startup-проверка добирает мёртвую задачу.
func TestExitOnFailRequiredAssistantResume(t *testing.T) {
	g := newRequiredGate(t)
	g.o.opts.ExitOnFail = false
	g.failAll()
	id := g.attach(t, "op-1", assistants.ModeRequired)
	waitForTaskStatus(t, g.store, id, assistants.StatusFailed, 2*time.Second)
	g.o.concurrency.WaitAgents()
	g.o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
	if got := g.o.currentStatus(assistantsTestStageID); got != state.StatusAwaitingAssistants {
		t.Fatalf("stage status = %v, want awaiting_assistants without the option", got)
	}

	g.o.opts.ExitOnFail = true
	g.o.maybeCompleteAfterAssistants(context.Background(), assistantsTestStageID)

	g.requireStageFailedByHelper(t)
}

func TestExitOnFailAdvisoryAssistant(t *testing.T) {
	g := newRequiredGate(t)
	g.failAll()
	id := g.attach(t, "op-1", assistants.ModeAdvisory)
	waitForTaskStatus(t, g.store, id, assistants.StatusFailed, 2*time.Second)
	g.o.concurrency.WaitAgents()

	g.o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")

	if got := g.o.currentStatus(assistantsTestStageID); got != state.StatusDone || g.o.stopError() != nil {
		t.Fatalf("stage %v, stop error %v; want done without a stop: advisory helpers never gate", got, g.o.stopError())
	}
}

func TestExitOnFailWaivedAssistant(t *testing.T) {
	g := newRequiredGate(t)
	g.failAll()
	id := g.attach(t, "op-1", assistants.ModeRequired)
	waitForTaskStatus(t, g.store, id, assistants.StatusFailed, 2*time.Second)
	g.o.concurrency.WaitAgents()
	if err := g.o.WaiveTask(context.Background(), id, state.Actor{Kind: "user"}, "accepted"); err != nil {
		t.Fatalf("WaiveTask: %v", err)
	}

	g.o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")

	if got := g.o.currentStatus(assistantsTestStageID); got != state.StatusDone || g.o.stopError() != nil {
		t.Fatalf("stage %v, stop error %v; want done: a waiver covers the dead helper", got, g.o.stopError())
	}
}

// Pause, закоммиченный между снимком и переходом, не перезаписывается failed.
func TestExitOnFailRequiredAssistantStaleSnapshot(t *testing.T) {
	g := newRequiredGate(t)
	g.failAll()
	id := g.attach(t, "op-1", assistants.ModeRequired)
	waitForTaskStatus(t, g.store, id, assistants.StatusFailed, 2*time.Second)
	g.o.concurrency.WaitAgents()
	g.o.requiredFailureDecidedHook = func(stageID string) {
		g.o.Trigger(stageID, bus.EvPause, bus.GuardCtx{}, "user pause")
	}

	g.o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")

	if got := g.o.currentStatus(assistantsTestStageID); got != state.StatusPaused || g.o.stopError() != nil {
		t.Fatalf("stage %v, stop error %v; want the concurrent Pause to win", got, g.o.stopError())
	}
}

// failedRequiredHelper — стадия с мёртвой required-задачей, готовая к решению о провале в completeStage.
func (g *requiredGate) failedRequiredHelper(t *testing.T) assistants.TaskID {
	t.Helper()
	g.failAll()
	id := g.attach(t, "op-1", assistants.ModeRequired)
	waitForTaskStatus(t, g.store, id, assistants.StatusFailed, 2*time.Second)
	g.o.concurrency.WaitAgents()
	return id
}

func (g *requiredGate) requireNoFailure(t *testing.T) {
	t.Helper()
	if got := g.o.currentStatus(assistantsTestStageID); got == state.StatusFailed || g.o.stopError() != nil {
		t.Fatalf("stage %v, stop error %v; want no failure: the decision was stale at the commit", got, g.o.stopError())
	}
}

// Waiver, закоммиченный между снимком и переходом, снимает решение о провале: Fence проверяет задачу при коммите.
func TestExitOnFailRequiredFailureWaivedBeforeCommit(t *testing.T) {
	g := newRequiredGate(t)
	id := g.failedRequiredHelper(t)
	g.o.requiredFailureDecidedHook = func(string) {
		if err := g.o.WaiveTask(context.Background(), id, state.Actor{Kind: "user"}, "accepted"); err != nil {
			t.Errorf("WaiveTask: %v", err)
		}
	}

	g.o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")

	g.requireNoFailure(t)
	waitStageStatus(t, g.store, assistantsTestStageID, state.StatusDone, 2*time.Second)
	if err := g.o.stopError(); err != nil {
		t.Fatalf("stop error %v; want none: the waiver closed the gate, the stage must not hang in awaiting_assistants", err)
	}
}

// Required-помощник подключён между снимком гейта и EvComplete: стадия не должна стать done с открытым
// гейтом, а должна дождаться помощника.
func TestExitOnFailRequiredAttachedBeforeComplete(t *testing.T) {
	g := newRequiredGate(t)
	g.o.stageCompletingHook = func(string) {
		g.o.stageCompletingHook = nil
		g.attachBlocker(t, "op-1")
	}

	g.o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")

	if got := g.o.currentStatus(assistantsTestStageID); got != state.StatusAwaitingAssistants {
		t.Fatalf("stage status = %v, want awaiting_assistants: the required helper attached before EvComplete", got)
	}
	close(g.release)
	waitStageStatus(t, g.store, assistantsTestStageID, state.StatusDone, 2*time.Second)
	g.requireNoFailure(t)
}

// Другая required-задача умирает между снимком и переходом: Fence отвергает провал, но стадия не должна
// застрять в awaiting_assistants с мёртвым non-waived помощником.
func TestExitOnFailRequiredAssistantDiesBeforeAwait(t *testing.T) {
	g := newRequiredGate(t)
	live := g.attachBlocker(t, "op-1")
	g.failAll()
	first := g.attach(t, "op-2", assistants.ModeRequired)
	waitForTaskStatus(t, g.store, first, assistants.StatusFailed, 2*time.Second)
	var once sync.Once
	g.o.requiredFailureDecidedHook = func(string) {
		once.Do(func() {
			if err := g.o.WaiveTask(context.Background(), first, state.Actor{Kind: "user"}, "accepted"); err != nil {
				t.Errorf("WaiveTask: %v", err)
			}
			g.o.finishTaskFrom(live, assistants.StatusRunning, assistants.StatusFailed, "crash")
			close(g.release) // задача уже failed: её runner не отменяется и ждал бы release до конца теста
		})
	}

	g.o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")

	g.requireStageFailedByHelper(t)
	if reason := g.o.stopError().Error(); !strings.Contains(reason, string(live)) {
		t.Fatalf("stop error %q does not name the task %s that died before the await", reason, live)
	}
}

// Retry той же задачи между снимком и переходом: повторная попытка живёт, стадию валить нельзя.
func TestExitOnFailRequiredFailureRetriedBeforeCommit(t *testing.T) {
	g := newRequiredGate(t)
	var calls atomic.Int32
	g.o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			return executor.HelperOutcome{}, errors.New("helper crashed")
		}
		awaitRelease(t, g.release)
		return passResultOutcome(), nil
	}
	t.Cleanup(func() { close(g.release) })
	id := g.attach(t, "op-1", assistants.ModeRequired)
	waitForTaskStatus(t, g.store, id, assistants.StatusFailed, 2*time.Second)
	g.o.concurrency.WaitAgents()
	g.o.requiredFailureDecidedHook = func(string) {
		if err := g.o.RetryTask(context.Background(), id); err != nil {
			t.Errorf("RetryTask: %v", err)
		}
	}

	g.o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")

	g.requireNoFailure(t)
}

// Ctrl+C между снимком и переходом остаётся прерыванием: failed не коммитится и причины провала нет.
func TestExitOnFailRequiredFailureRunCancelledBeforeCommit(t *testing.T) {
	g := newRequiredGate(t)
	g.failedRequiredHelper(t)
	ctx, cancel := context.WithCancel(context.Background())
	g.o.installRunContext(ctx, cancel)
	g.o.requiredFailureDecidedHook = func(string) { cancel() }

	g.o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")

	g.requireNoFailure(t)
	if got := failedTransitions(t, g.store); got != 0 {
		t.Fatalf("failed transitions after the interrupt = %d, want none in the journal", got)
	}
}

func TestExitOnFailRequiredAssistantRunCancelled(t *testing.T) {
	g := newRequiredGate(t)
	g.failAll()
	id := g.attach(t, "op-1", assistants.ModeRequired)
	waitForTaskStatus(t, g.store, id, assistants.StatusFailed, 2*time.Second)
	g.o.concurrency.WaitAgents()
	ctx, cancel := context.WithCancel(context.Background())
	g.o.installRunContext(ctx, cancel)
	cancel()

	g.o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")

	if got := g.o.currentStatus(assistantsTestStageID); got != state.StatusAwaitingAssistants || g.o.stopError() != nil {
		t.Fatalf("stage %v, stop error %v; want awaiting_assistants: a cancelled run is not rewritten", got, g.o.stopError())
	}
}

// --- Retry after a barrier failure ---

func TestExitOnFailBarrierRetryStartsNewGeneration(t *testing.T) {
	g := newRequiredGate(t)
	g.failAll()
	id := g.attach(t, "op-1", assistants.ModeRequired)
	waitForTaskStatus(t, g.store, id, assistants.StatusFailed, 2*time.Second)
	g.o.concurrency.WaitAgents()
	g.o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
	g.requireStageFailedByHelper(t)
	generation := func() uint64 { return g.store.Snapshot().Assistants.RootGenerations[assistantsTestStageID] }

	if err := g.o.finishBarrierFailureBump(assistantsTestStageID); err != nil || generation() != 0 {
		t.Fatalf("bump before any Retry: err=%v generation=%d, want a no-op", err, generation())
	}

	g.o.Trigger(assistantsTestStageID, bus.EvManualRetry, bus.GuardCtx{}, "")
	for range 2 { // the second call is the restart after a crash between the Retry and the bump
		if err := g.o.finishBarrierFailureBump(assistantsTestStageID); err != nil {
			t.Fatalf("finishBarrierFailureBump: %v", err)
		}
		if generation() != 1 {
			t.Fatalf("root generation = %d, want 1 exactly once", generation())
		}
	}
	if _, dead := g.store.Snapshot().Assistants.DeadRequiredTask(assistantsTestStageID); dead {
		t.Fatal("the dead helper of the previous generation must not gate the new attempt")
	}

	// A later failure for another reason does not touch the tree again.
	failStageForExit(g.o, assistantsTestStageID, "tests failed")
	g.o.Trigger(assistantsTestStageID, bus.EvManualRetry, bus.GuardCtx{}, "")
	if err := g.o.finishBarrierFailureBump(assistantsTestStageID); err != nil || generation() != 1 {
		t.Fatalf("Retry of an ordinary failure: err=%v generation=%d, want it untouched at 1", err, generation())
	}
}

// retryStage сам продвигает поколение до новой активации: готовый старт видит дерево без мёртвого помощника.
func TestExitOnFailBarrierRetryStage(t *testing.T) {
	g := newRequiredGate(t)
	g.failAll()
	id := g.attach(t, "op-1", assistants.ModeRequired)
	waitForTaskStatus(t, g.store, id, assistants.StatusFailed, 2*time.Second)
	g.o.concurrency.WaitAgents()
	g.o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
	g.requireStageFailedByHelper(t)
	declStageSetup(t, g.o, assistantsTestStageID)
	g.o.opts.Runner, g.o.runner = &fakeAgentRunner{}, &fakeAgentRunner{}

	g.o.retryStage(context.Background(), assistantsTestStageID)
	g.o.concurrency.WaitAgents()

	if got := g.store.Snapshot().Assistants.RootGenerations[assistantsTestStageID]; got != 1 {
		t.Fatalf("root generation after Retry = %d, want 1", got)
	}
	if !slices.Contains(g.store.Snapshot().Assistants.ByRootStage[assistantsTestStageID], id) {
		t.Fatal("the dead task must stay in history")
	}
}

// --- replay after a crash between Retry and the generation bump ---

// reopenAfterCrash закрывает store (краш) и поднимает новый оркестратор над тем же журналом; активация стадий
// удержана, чтобы рестарт проверял только восстановление поколения, без запуска агента.
func (g *requiredGate) reopenAfterCrash(t *testing.T) (*Orchestrator, *state.Store) {
	t.Helper()
	if err := g.store.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, store := reopenAssistantsTestOrchestrator(t, g.o)
	resumed.absRunDir = resumed.opts.RunDir
	resumed.activationHeld.Store(true)
	return resumed, store
}

// Retry закоммичен, bump — нет: рестарт сам даёт стадии новое поколение и не валит её на той же мёртвой задаче.
func TestExitOnFailReplay(t *testing.T) {
	t.Run("retry committed, bump missing", func(t *testing.T) {
		g := newRequiredGate(t)
		g.failedRequiredHelper(t)
		g.o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
		g.requireStageFailedByHelper(t)
		if _, ok := g.o.Trigger(assistantsTestStageID, bus.EvManualRetry, bus.GuardCtx{}, ""); !ok {
			t.Fatal("Retry was rejected")
		}

		resumed, store := g.reopenAfterCrash(t)
		resumed.startPlanningForPending(context.Background())
		resumed.concurrency.WaitAgents()

		requireNewGenerationAfterRestart(t, resumed, store, 1)
	})

	t.Run("bump committed, task cancellation missing", func(t *testing.T) {
		g := newRequiredGate(t)
		live := g.attachBlocker(t, "op-live")
		dead := g.attach(t, "op-dead", assistants.ModeRequired)
		waitForTaskStatus(t, g.store, dead, assistants.StatusFailed, 2*time.Second)
		g.o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
		g.requireStageFailedByHelper(t)
		failSeq := g.store.Snapshot().Stages[assistantsTestStageID].Seq
		if _, ok := g.o.Trigger(assistantsTestStageID, bus.EvManualRetry, bus.GuardCtx{}, ""); !ok {
			t.Fatal("Retry was rejected")
		}
		bump := state.Operation{
			ID: barrierFailureBumpOpID(assistantsTestStageID, failSeq), Kind: opKindBumpRootGeneration,
			Effects: []state.Effect{{Kind: "root_generation_bumped", Target: state.Target{StageID: assistantsTestStageID}}},
		}
		if _, err := g.store.ApplyRunOperation(bump, nil); err != nil {
			t.Fatalf("bump: %v", err)
		}
		t.Cleanup(func() {
			close(g.release)
			g.o.concurrency.WaitAgents()
		})

		resumed, store := g.reopenAfterCrash(t)
		resumed.startPlanningForPending(context.Background())
		resumed.concurrency.WaitAgents()

		requireNewGenerationAfterRestart(t, resumed, store, 1)
		if got := store.Snapshot().Assistants.Tasks[live].Status; got != assistants.StatusCancelled {
			t.Fatalf("live task of the previous generation = %v, want cancelled by the restart", got)
		}
	})
}

func requireNewGenerationAfterRestart(t *testing.T, o *Orchestrator, store *state.Store, want uint64) {
	t.Helper()
	rs := store.Snapshot()
	if got := rs.Assistants.RootGenerations[assistantsTestStageID]; got != want {
		t.Fatalf("root generation after the restart = %d, want %d exactly once", got, want)
	}
	if dead, found := rs.Assistants.DeadRequiredTask(assistantsTestStageID); found {
		t.Fatalf("dead task %s of the previous generation still gates the new attempt", dead.ID)
	}
	if got := rs.Stages[assistantsTestStageID].Status; got == state.StatusFailed || o.stopError() != nil {
		t.Fatalf("stage %v, stop error %v; want the retried stage not failed again", got, o.stopError())
	}
}

// Ctrl+C в окне между durable-коммитом failed и записью причины: Run дожидается коммита и возвращает провал,
// а не context.Canceled (CLI иначе выйдет с 130 при уже записанной failed-стадии).
func TestExitOnFailInterruptDuringCommitReturnsFailure(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{ID: "bad", Name: "Bad", Script: "exit 7"}}
	store, err := state.Open(runDir, []string{"bad"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	o := New(Options{
		RunDir: runDir, RootDir: t.TempDir(), Stages: stages, Store: store,
		Config: config.Default(), Prompts: DefaultPrompts(),
		Clock: newFakeClockWithPump(t), ExitOnFail: true,
	})
	committed, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseHook := sync.OnceFunc(func() { close(release) })
	o.exitOnFailCommittedHook = func(string) {
		once.Do(func() { close(committed) })
		<-release
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	runErr, finished := make(chan error, 1), make(chan struct{})
	go func() {
		runErr <- o.Run(ctx)
		close(finished)
	}()
	t.Cleanup(func() {
		releaseHook()
		cancel()
		<-finished
	})

	select {
	case <-committed:
	case <-ctx.Done():
		t.Fatal("failed transition was never committed")
	}
	cancel() // Ctrl+C, пока коммит держит exit.mu
	awaitBlocked(t, "awaitInFlightCommit", runErr)
	releaseHook()

	var failed *StageFailedError
	if err := <-runErr; !errors.As(err, &failed) || failed.StageID != "bad" {
		t.Fatalf("Run = %v, want the stage failure committed before the interrupt", err)
	}
}

// HTTP-переход закоммитил failed, но ещё не записал причину, когда стартует Run: auto_recover не должен вернуть
// стадию в pending, а Run обязан вернуть этот провал.
func TestExitOnFailStartDuringCommitKeepsFailedStage(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{ID: "bad", Name: "Bad", Script: "exit 7"}}
	store, err := state.Open(runDir, []string{"bad"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	o := New(Options{
		RunDir: runDir, RootDir: t.TempDir(), Stages: stages, Store: store,
		Config: config.Default(), Prompts: DefaultPrompts(),
		Clock: newFakeClockWithPump(t), ExitOnFail: true,
	})
	committed, release := make(chan struct{}), make(chan struct{})
	releaseHook := sync.OnceFunc(func() { close(release) })
	o.exitOnFailCommittedHook = func(string) {
		close(committed)
		<-release
	}
	t.Cleanup(releaseHook)

	go failStageForExit(o, "bad", "boom") // HTTP-переход до Run
	select {
	case <-committed:
	case <-time.After(10 * time.Second):
		t.Fatal("failed transition was never committed")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- o.Run(ctx) }()
	awaitBlocked(t, "awaitInFlightCommit", runErr)
	releaseHook()

	var failed *StageFailedError
	if err := <-runErr; !errors.As(err, &failed) || failed.StageID != "bad" {
		t.Fatalf("Run = %v, want the committed stage failure", err)
	}
	if got := store.Get("bad"); got != state.StatusFailed {
		t.Fatalf("stage status = %v, want failed: auto_recover must not retry it", got)
	}
}

// Провал остановил ран уже после startupStop: auto_recover не должен вернуть failed-стадию в pending.
func TestExitOnFailAutoRecoverSkippedAfterStop(t *testing.T) {
	o, store := newExitOnFailStages(t, "a")
	ctx, cancel := context.WithCancel(context.Background())
	o.installRunContext(ctx, cancel)
	failStageForExit(o, "a", "boom") // записывает причину и отменяет ран

	o.autoRecoverFailedStages()
	if got := store.Get("a"); got != state.StatusFailed {
		t.Fatalf("stage status = %v, want failed after the run was stopped", got)
	}
}
