package orchestrator_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

func TestScenarios(t *testing.T) {
	scenarios := []Scenario{
		{
			Name: "happy-multistage",
			Stages: []flow.Stage{
				{ID: "plan-stage", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
				{ID: "impl-stage", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, DependsOn: []string{"plan-stage"}},
			},
			Agents: map[string]AgentSpec{
				"plan-stage": {}, "impl-stage": {},
			},
			Expect: Expectation{Statuses: map[string]state.StageStatus{
				"plan-stage": state.StatusDone, "impl-stage": state.StatusDone,
			}},
		},
		{
			Name:   "auto-phase",
			Stages: []flow.Stage{{ID: "hard", Agents: []flow.AgentType{flow.AgentAuto}}},
			Agents: map[string]AgentSpec{"hard": {}},
			Expect: Expectation{
				Statuses:     map[string]state.StageStatus{"hard": state.StatusDone},
				FilesPresent: map[string][]string{"hard": {"autonomous.flag", "execution_summary.md"}},
				FilesAbsent:  map[string][]string{"hard": {"plan.md"}},
			},
		},
		{
			Name:   "retry-after-transient",
			Stages: []flow.Stage{{ID: "flaky", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}}},
			Agents: map[string]AgentSpec{"flaky": {Inject: InjectRateLimitThenOK}},
			Expect: Expectation{Statuses: map[string]state.StageStatus{"flaky": state.StatusDone}},
		},
		{
			// Happy path: интерактивная planning-стадия задаёт вопрос по каноническому пути, пользователь отвечает
			// — стадия дописывает plan.md и (headless auto-approve) доходит до done.
			Name:   "interactive-dialog-happy",
			Stages: []flow.Stage{{ID: "dialog-stage", Agents: []flow.AgentType{flow.AgentPlanning}}},
			Agents: map[string]AgentSpec{
				"dialog-stage": {Interactive: true, Question: &QuestionInject{Phase: "planning", ID: "q1", Answer: "да", Fault: FaultNone}},
			},
			Expect: Expectation{Statuses: map[string]state.StageStatus{"dialog-stage": state.StatusDone}},
		},
		{
			// Poller исправляет stage-id префикс на planning; после ответа стадия завершается.
			Name:   "misprefixed-question",
			Stages: []flow.Stage{{ID: "misprefixed-stage", Agents: []flow.AgentType{flow.AgentPlanning}}},
			Agents: map[string]AgentSpec{
				"misprefixed-stage": {Interactive: true, Question: &QuestionInject{Phase: "planning", ID: "q1", Answer: "да", Fault: FaultWrongPrefix}},
			},
			Expect: Expectation{
				Statuses:             map[string]state.StageStatus{"misprefixed-stage": state.StatusDone},
				ReachesAwaitingInput: []string{"misprefixed-stage"},
			},
		},
		{
			// Poller переносит misplaced-вопрос в stageDir и создаёт answer-симлинк по старому пути.
			Name:   "wrong-folder-question",
			Stages: []flow.Stage{{ID: "wrongfolder-stage", Agents: []flow.AgentType{flow.AgentPlanning}}},
			Agents: map[string]AgentSpec{
				"wrongfolder-stage": {Interactive: true, Question: &QuestionInject{Phase: "planning", ID: "q1", Answer: "да", Fault: FaultWrongFolder}},
			},
			Expect: Expectation{
				Statuses:             map[string]state.StageStatus{"wrongfolder-stage": state.StatusDone},
				ReachesAwaitingInput: []string{"wrongfolder-stage"},
			},
		},
		hungDialogScenario(),
		{
			// InjectNoDone: RunAgent "отрабатывает", но не пишет .done — имитирует агента, который вышел, не
			// закончив работу.
			Name:   "incomplete-work-fails",
			Stages: []flow.Stage{{ID: "no-done", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}}},
			Agents: map[string]AgentSpec{"no-done": {Inject: InjectNoDone}},
			Expect: Expectation{Statuses: map[string]state.StageStatus{"no-done": state.StatusFailed}},
		},
		{
			// Полная битая строка между валидными событиями должна вернуть ErrCorruptLog.
			Name:   "corrupt-log-recovery",
			Stages: []flow.Stage{{ID: "a", Agents: []flow.AgentType{flow.AgentPlanning}}},
			PreSeedEventsLog: []byte(
				`{"seq":1,"stage_id":"a","from":"pending","to":"planning","event":"x"}` + "\n" +
					`NOT JSON AT ALL` + "\n" +
					`{"seq":2,"stage_id":"a","from":"planning","to":"done","event":"y"}` + "\n"),
			Expect: Expectation{RunErrSubstr: "corrupted"},
		},
	}
	for _, sc := range scenarios {
		t.Run(sc.Name, func(t *testing.T) { runScenario(t, sc) })
	}
}

// hungDialogScenario — без ответа стадия остаётся awaiting_user_input до
// таймаута харнесса. Вынесена в функцию, чтобы TestScenarios и
// TestScenarios_HungDialogIsFast гоняли один и тот же сценарий.
func hungDialogScenario() Scenario {
	return Scenario{
		Name:   "hung-dialog",
		Stages: []flow.Stage{{ID: "hung-stage", Agents: []flow.AgentType{flow.AgentPlanning}}},
		Agents: map[string]AgentSpec{
			"hung-stage": {Interactive: true, Question: &QuestionInject{Phase: "planning", ID: "q1", Answer: "", Fault: FaultNone}},
		},
		Expect: Expectation{
			Statuses:             map[string]state.StageStatus{"hung-stage": state.StatusAwaitingUserInput},
			ReachesAwaitingInput: []string{"hung-stage"},
		},
	}
}

// TestScenarios_HungDialogIsFast гарантирует, что сценарий с нетерминальным
// ожидаемым статусом (стадия зависает в awaiting_user_input) не ждёт ctx-
// дедлайн харнесса — см. T3 в dev-plan.md.
func TestScenarios_HungDialogIsFast(t *testing.T) {
	start := time.Now()
	runScenario(t, hungDialogScenario())
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("hung-dialog scenario took %v, want < 5s", elapsed)
	}
}

func TestScenarioHarness_FailsOnWrongExpectation(t *testing.T) {
	sc := Scenario{
		Name:   "self-test-base",
		Stages: []flow.Stage{{ID: "hard", Agents: []flow.AgentType{flow.AgentAuto}}},
		Agents: map[string]AgentSpec{"hard": {}},
	}
	orch, runDir, reached := runScenarioUpToAssert(t, sc)
	if orch == nil {
		t.Fatal("harness self-test: базовый сценарий должен пройти штатно, но runScenarioUpToAssert вернул nil orch")
	}

	// Реальный исход стадии "hard" — state.StatusDone. Здесь намеренно
	// указан заведомо неверный StatusFailed.
	wrongExpect := Expectation{Statuses: map[string]state.StageStatus{"hard": state.StatusFailed}}

	rec := &recordingT{}
	assertExpectation(rec, orch, runDir, wrongExpect, reached)
	if len(rec.errors) == 0 {
		t.Fatal("harness self-test: assertExpectation должна была зафиксировать расхождение (StatusFailed вместо реального StatusDone), но не записала ни одной ошибки — assertExpectation превратилась в no-op")
	}
}

// recordingT — тестовый двойник expectationReporter: вместо того чтобы безусловно валить вызывающий тест,
// просто собирает сообщения.
type recordingT struct {
	errors []string
}

func (r *recordingT) Helper() {}

func (r *recordingT) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}
