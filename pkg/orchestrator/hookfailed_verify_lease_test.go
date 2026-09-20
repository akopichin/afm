package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/state"
)

// TestStartPlanningForPending_HookFailedRecovery_RegistersLeaseForVerifierSlotSwap
// — регрессия на находку финального ревью AI-verify: recovery.go резюмировал
// стадию в StatusHookFailed через голый concurrency.SpawnAgent, минуя
// spawnAgentLeased. Как только before-hook разрешался (Retry/Skip),
// resumeHookFailedWait диспетчеризовал в runImplementationAgent (через
// dispatchMainAfterBeforeHook), который мог вызвать RunVerification — а без
// зарегистрированного lease в o.stageLeases верификатор запускался бы БЕЗ
// учёта командного слота (max_parallel-дыра именно на этом пути восстановления
// после краха, см. TestResumeOwner_RegistersLeaseForVerifierSlotSwap — тот же
// класс бага на соседнем пути StatusAwaitingUserInput).
//
// Тест гоняет РЕАЛЬНый спавн startPlanningForPending (без o.testRunnerHook) с
// двумя настоящими concurrency.ChannelSemaphore для команды автора ("claude",
// дефолт для s.Command=="") и команды верификатора ("codex"): пока выполняется
// verify-шаг, слот codex должен быть занят (len==1), а слот claude — уже
// освобождён (len==0), что доказывает: lease был зарегистрирован спавном
// resumeHookFailedWait и реально переключился на верификатора.
func TestStartPlanningForPending_HookFailedRecovery_RegistersLeaseForVerifierSlotSwap(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("## Tasks\n- x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Стадия крашнулась, ожидая решения по before-hook — hook_pending.json на
	// диске, статус StatusHookFailed (та самая комбинация, которую recovery.go
	// находит при рестарте afm run).
	if err := writeHookPending(stageDir, hookPending{Hook: hookBefore, Script: "exit 1", Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	seedStageStatus(t, store, "s1", state.StatusHookFailed)

	stage := flow.Stage{ID: "s1", Name: "S1",
		Agents:       []flow.AgentType{flow.AgentImplementation},
		ScriptBefore: "exit 1",
		Verify:       flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}},
	}

	claudeSem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	codexSem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	cb := bus.NewCriticalBus(16)
	mgr := concurrency.NewWithSemaphores(cb, map[string]concurrency.Semaphore{
		"claude": claudeSem,
		"codex":  codexSem,
	}, "claude")

	fake := doneWritingRunner{}
	o := &Orchestrator{
		opts:        Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default(), Runner: fake},
		graph:       graph.NewGraph([]flow.Stage{stage}),
		runner:      fake,
		critical:    cb,
		ui:          bus.NewUIBus(),
		fsm:         bus.NewFSM(store),
		concurrency: mgr,
	}

	var sawVerifierSlotHeld, sawAuthorSlotReleased bool
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		sawVerifierSlotHeld = len(codexSem) == 1
		sawAuthorSlotReleased = len(claudeSem) == 0
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	}

	// startPlanningForPending — то, что recovery вызывает при рестарте
	// afm run; для нашей стадии в StatusHookFailed она попадает во вторую
	// switch-ветку и спавнит resumeHookFailedWait, который блокируется в
	// waitForHookDecision.
	o.startPlanningForPending(context.Background())

	// Дожидаемся регистрации waiter'а и шлём Skip — hook не должен
	// перезапускаться, нас интересует только диспетч в основной агент.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if o.resolveHook("s1", hookDecisionSkip) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	o.concurrency.WaitAgents()

	if !sawVerifierSlotHeld {
		t.Error("expected the codex slot to be held while the verify agent step runs — hook-failed recovery must register a lease")
	}
	if !sawAuthorSlotReleased {
		t.Error("expected the claude (author) slot to be released while the verify agent step runs — never two slots at once")
	}
}
