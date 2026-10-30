package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// exitOnFailRun — один ран из script-стадий с реальным Run и fake-часами.
type exitOnFailRun struct {
	orch    *orchestrator.Orchestrator
	store   *state.Store
	runDir  string
	rootDir string
}

func newExitOnFailRun(t *testing.T, stages []flow.Stage, exitOnFail bool, mutate ...func(*orchestrator.Options)) *exitOnFailRun {
	t.Helper()
	runDir, rootDir := t.TempDir(), t.TempDir()
	ids := make([]string, len(stages))
	for i, s := range stages {
		ids[i] = s.ID
	}
	store, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return startExitOnFailRun(t, runDir, rootDir, store, stages, exitOnFail, mutate...)
}

func startExitOnFailRun(t *testing.T, runDir, rootDir string, store *state.Store, stages []flow.Stage, exitOnFail bool, mutate ...func(*orchestrator.Options)) *exitOnFailRun {
	t.Helper()
	opts := orchestrator.Options{
		RunDir: runDir, RootDir: rootDir, Stages: stages, Store: store,
		Config: config.Default(), Prompts: orchestrator.DefaultPrompts(),
		Clock: newFakeClockWithPump(t), ExitOnFail: exitOnFail,
	}
	for _, m := range mutate {
		m(&opts)
	}
	return &exitOnFailRun{orch: orchestrator.New(opts), store: store, runDir: runDir, rootDir: rootDir}
}

// start запускает Run в фоне; cleanup по порядку «отмена → ожидание Run», до закрытия Store.
func (r *exitOnFailRun) start(t *testing.T) (context.Context, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	runErr, finished := make(chan error, 1), make(chan struct{})
	go func() {
		runErr <- r.orch.Run(ctx)
		close(finished)
	}()
	t.Cleanup(func() {
		cancel()
		<-finished
	})
	return ctx, cancel, runErr
}

// awaitStageStatus ждёт UI-событие о смене статуса стадии.
func awaitStageStatus(ctx context.Context, t *testing.T, events <-chan bus.Event, stageID string, status state.StageStatus) {
	t.Helper()
	for {
		select {
		case ev := <-events:
			if ev.Type == bus.EventStageStatusChanged && ev.StageID == stageID && ev.Data == string(status) {
				return
			}
		case <-ctx.Done():
			t.Fatalf("stage %s never reached %s", stageID, status)
		}
	}
}

// run выполняет Run до его возврата; таймаут контекста — только страховка от зависания.
func (r *exitOnFailRun) run(t *testing.T) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return r.orch.Run(ctx)
}

func (r *exitOnFailRun) journal(t *testing.T) state.RunState {
	t.Helper()
	rs, err := state.LoadRunState(r.runDir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	return rs
}

func requireStageFailed(t *testing.T, err error, stageID string) *orchestrator.StageFailedError {
	t.Helper()
	var failed *orchestrator.StageFailedError
	if !errors.As(err, &failed) {
		t.Fatalf("Run error = %v, want *StageFailedError", err)
	}
	if failed.StageID != stageID || failed.Reason == "" || failed.Seq == 0 {
		t.Fatalf("StageFailedError = %+v, want stage %q with a reason and seq", failed, stageID)
	}
	return failed
}

func TestExitOnFailDefault(t *testing.T) {
	r := newExitOnFailRun(t, []flow.Stage{{ID: "bad", Name: "Bad", Script: "exit 7"}}, false)

	if err := r.run(t); err != nil {
		t.Fatalf("Run without the option = %v, want the previous nil on a headless failed stage", err)
	}
	rs := r.journal(t)
	if rs.Stages["bad"].Status != state.StatusFailed || rs.RunStatus != state.RunStatusFailed {
		t.Fatalf("stage %q, run %q; want failed/failed", rs.Stages["bad"].Status, rs.RunStatus)
	}
}

// Сосед первичного провала не ждёт своих 60 секунд: Run возвращает StageFailedError до таймаута, журнал сохранён.
func TestExitOnFailCommittedTransition(t *testing.T) {
	stages := []flow.Stage{
		{ID: "bad", Name: "Bad", Script: "echo boom >&2; exit 7"},
		{ID: "slow", Name: "Slow", Script: "exec sleep 60"},
	}
	r := newExitOnFailRun(t, stages, true)

	failed := requireStageFailed(t, r.run(t), "bad")
	if !strings.Contains(failed.Reason, "exit status 7") {
		t.Errorf("reason = %q, want the script exit status", failed.Reason)
	}

	rs := r.journal(t)
	if rs.Stages["bad"].Status != state.StatusFailed || rs.Stages["bad"].Seq != failed.Seq {
		t.Errorf("replayed stage = %+v, want failed at seq %d", rs.Stages["bad"], failed.Seq)
	}
	if rs.RunStatus != state.RunStatusFailed {
		t.Errorf("replayed run status = %q, want failed", rs.RunStatus)
	}
}

// Промежуточные попытки (retrying) не останавливают ран: скрипт падает один раз и проходит на повторе.
func TestExitOnFailRetry(t *testing.T) {
	script := `if [ -e attempted ]; then exit 0; fi; touch attempted; exit 1`
	r := newExitOnFailRun(t, []flow.Stage{{ID: "flaky", Name: "Flaky", Script: script}}, true)

	if err := r.run(t); err != nil {
		t.Fatalf("Run = %v, want nil: a retried attempt is not a failed stage", err)
	}
	if got := r.journal(t).Stages["flaky"].Status; got != state.StatusDone {
		t.Fatalf("stage = %q, want done", got)
	}
}

// Провал script_after — hook_failed-уведомление, а не failed-стадия: политика ран не останавливает.
func TestExitOnFailNonTerminalBoundaries(t *testing.T) {
	stages := []flow.Stage{{ID: "ok", Name: "Ok", Script: "true", ScriptAfter: "exit 3"}}
	r := newExitOnFailRun(t, stages, true)
	subID, events := r.orch.UIBus().Subscribe(64)
	t.Cleanup(func() { r.orch.UIBus().Unsubscribe(subID) })

	ctx, cancel, runErr := r.start(t)
	for hookFailed := false; !hookFailed; {
		select {
		case ev := <-events:
			hookFailed = ev.Type == bus.EventHookFailed
		case <-ctx.Done():
			t.Fatal("hook_failed was never published")
		}
	}
	cancel()

	err := <-runErr
	var failed *orchestrator.StageFailedError
	if errors.As(err, &failed) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled: a failed script_after is not a failed stage", err)
	}
	if got := r.journal(t).Stages["ok"].Status; got != state.StatusDone {
		t.Fatalf("stage = %q, want done: the after-hook does not change stage status", got)
	}
}

// Отказ в одобрении без dashboard случается в самом event loop: Run не уходит в естественный выход.
func TestExitOnFailEventLoopFailure(t *testing.T) {
	stages := []flow.Stage{{ID: "manual", Name: "Manual", Description: "needs a human",
		Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}}}
	r := newExitOnFailRun(t, stages, true, func(o *orchestrator.Options) {
		o.Runner = &doneCreatingRunner{delegate: mockRunner(t, mockPlanningScript)}
		o.RequireApproval = true
	})

	failed := requireStageFailed(t, r.run(t), "manual")
	if !strings.Contains(failed.Reason, "approval required") {
		t.Errorf("reason = %q, want the approval message", failed.Reason)
	}
	if rs := r.journal(t); rs.RunStatus != state.RunStatusFailed {
		t.Errorf("run status = %q, want failed", rs.RunStatus)
	}
}

// Остановка снаружи без провала остаётся прерыванием: запущенная стадия, убитая отменой, причиной не становится.
func TestExitOnFailSignal(t *testing.T) {
	r := newExitOnFailRun(t, []flow.Stage{{ID: "slow", Name: "Slow", Script: "exec sleep 60"}}, true)
	subID, events := r.orch.UIBus().Subscribe(64)
	t.Cleanup(func() { r.orch.UIBus().Unsubscribe(subID) })
	ctx, cancel, runErr := r.start(t)
	awaitStageStatus(ctx, t, events, "slow", state.StatusRunning)

	cancel()

	err := <-runErr
	var failed *orchestrator.StageFailedError
	if errors.As(err, &failed) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled without a stage failure", err)
	}
}

// Журнал, записанный в прошлом процессе, при auto_recover:false останавливает ран до запуска агента.
func TestExitOnFailStartupBeforeAgentStarts(t *testing.T) {
	runDir, rootDir := t.TempDir(), t.TempDir()
	stages := []flow.Stage{
		{ID: "a", Name: "A", Script: "touch ran-a"},
		{ID: "b", Name: "B", Script: "touch ran-b"},
	}
	store, err := state.Open(runDir, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	// b провалилась раньше a: первичной считается она.
	for _, step := range []struct{ id, reason string }{{"b", "first boom"}, {"a", "second boom"}} {
		tr := &state.Transition{StageID: step.id, From: state.StatusPending, To: state.StatusFailed, Event: "fail", Reason: step.reason}
		if err := store.Apply(tr); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	}
	t.Cleanup(func() { store.Close() })

	noRecover := false
	r := startExitOnFailRun(t, runDir, rootDir, store, stages, true, func(o *orchestrator.Options) {
		o.Config.AutoRecover = &noRecover
	})

	failed := requireStageFailed(t, r.run(t), "b")
	if failed.Reason != "first boom" {
		t.Errorf("reason = %q, want the earliest failure's reason", failed.Reason)
	}
	for _, marker := range []string{"ran-a", "ran-b"} {
		if _, err := os.Stat(filepath.Join(rootDir, marker)); err == nil {
			t.Errorf("%s exists: an agent started although the run was already failed", marker)
		}
	}
	if rs := r.journal(t); rs.RunStatus != state.RunStatusFailed {
		t.Errorf("run status = %q, want failed", rs.RunStatus)
	}
}

// С auto_recover (по умолчанию) старый failed — обычный кандидат на повтор, а не причина остановки.
func TestExitOnFailStartupAutoRecover(t *testing.T) {
	runDir, rootDir := t.TempDir(), t.TempDir()
	stages := []flow.Stage{{ID: "a", Name: "A", Script: "true"}}
	store, err := state.Open(runDir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusFailed, Event: "fail", Reason: "old"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	r := startExitOnFailRun(t, runDir, rootDir, store, stages, true)
	if err := r.run(t); err != nil {
		t.Fatalf("Run = %v, want nil after auto_recover reran the stage", err)
	}
	if got := r.journal(t).Stages["a"].Status; got != state.StatusDone {
		t.Fatalf("stage = %q, want done", got)
	}
}

// Провал не закрывает ожидания mesh как run_closed: ран не финализирован, его можно продолжить.
func TestExitOnFailMeshWaits(t *testing.T) {
	const stageID = "work"
	stages := []flow.Stage{{ID: stageID, Name: "Work", Script: "until [ -e go ]; do sleep 0.01; done; exit 1"}}
	rec := &flowEventRecorder{}
	r := newExitOnFailRun(t, stages, true, func(o *orchestrator.Options) {
		reg, err := assistants.BuildRegistry(nil, []assistants.Profile{
			{ID: "qa", Title: "QA", Command: "helper-cmd", Capabilities: assistants.Capabilities{Delegate: true}},
		}, nil, "claude", func(string) (bool, string, assistants.ReadonlyEnforcement) {
			return true, "", assistants.ReadonlyEnforcementOS
		})
		if err != nil {
			t.Fatalf("BuildRegistry: %v", err)
		}
		o.AssistantsEnabled, o.AssistantsRegistry = true, reg
		o.Hooks = newFlowRecorderDispatcher(t, rec)
	})
	if _, err := r.store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-1"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}

	registered := make(chan agentcomm.WaitID, 1)
	orchestrator.SetRunHelperAgentForTest(r.orch, func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		receipt, err := r.orch.Wait(ctx, orchestrator.WaitRequest{
			OperationID: "wait-" + taskID, Waiter: orchestrator.RunAgentParticipantIDForTest("op-add-qa"),
			Condition:        agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: stageID, Status: string(state.StatusDone)},
			ContinuationNote: "never satisfied: the stage fails",
			Actor:            state.Actor{Kind: "assistant", ID: taskID},
		})
		if err != nil {
			t.Errorf("Wait: %v", err)
			return executor.HelperOutcome{}, err
		}
		registered <- receipt.WaitID
		return executor.HelperOutcome{ProcessOK: true}, nil
	})

	ctx, _, runErr := r.start(t)

	msg := "go"
	res, err := r.orch.AddRunAgent(ctx, orchestrator.AddRunAgentRequest{OperationID: "op-add-qa",
		ProfileID: "qa", Title: "My QA", Instruction: "watch", FirstMessage: &msg, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AddRunAgent: %v", err)
	}
	var waitID agentcomm.WaitID
	select {
	case waitID = <-registered:
	case <-ctx.Done():
		t.Fatal("task never registered its mesh wait")
	}
	waitForTaskStatus(t, r.store, res.TaskID, assistants.StatusWaitingPeer, 5*time.Second)

	if err := os.WriteFile(filepath.Join(r.rootDir, "go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = requireStageFailed(t, <-runErr, stageID)

	if got := r.store.Snapshot().Assistants.Mesh.Waits[waitID].Status; got != agentcomm.WaitPending {
		t.Fatalf("wait status after the failed run = %q, want pending (the run is resumed later)", got)
	}
	if containsEvent(rec.ordered(), lifecyclehooks.EventFlowFinished) || !containsEvent(rec.ordered(), lifecyclehooks.EventFlowFailed) {
		t.Fatalf("lifecycle events = %v, want flow_failed and no flow_finished", rec.ordered())
	}
}
