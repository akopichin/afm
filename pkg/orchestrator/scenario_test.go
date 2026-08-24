package orchestrator_test

import (
	"fmt"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// TestScenarios прогоняет таблицу неинтерактивных сценариев через единый
// scenario-харнесс (scriptedRunner + runScenario, см. scenario_harness_test.go).
//
// Судьба Plan-стадий (см. task-1-brief.md, Step 4): resolvePlanSource
// подставляет путь зависимости только для Plan вида "./...", а буквальный
// путь вроде "plan-stage-plan" не резолвится в файл соседней стадии и не
// существует на диске — copyFile упал бы. Поэтому impl-stage и flaky здесь
// НЕ используют Plan-как-путь-к-чужому-плану, а вместо этого сами проходят
// planning+implementation (Agents: [planning, implementation]):
// scriptedRunner.RunPlanning пишет валидный plan.md для каждой такой стадии
// самостоятельно — не нужен ни внешний файл плана, ни завязка на
// resolvePlanSource. depends_on между plan-stage и impl-stage сохранён, чтобы
// проверить многостадийный happy-path с зависимостью.
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
			// Happy path: интерактивная planning-стадия задаёт вопрос по
			// каноническому пути, пользователь отвечает — стадия дописывает
			// plan.md и (headless auto-approve) доходит до done.
			Name:   "interactive-dialog-happy",
			Stages: []flow.Stage{{ID: "dialog-stage", Agents: []flow.AgentType{flow.AgentPlanning}}},
			Agents: map[string]AgentSpec{
				"dialog-stage": {Interactive: true, Question: &QuestionInject{Phase: "planning", ID: "q1", Answer: "да", Fault: FaultNone}},
			},
			Expect: Expectation{Statuses: map[string]state.StageStatus{"dialog-stage": state.StatusDone}},
		},
		{
			// Агент пишет question.json внутри stageDir, но с префиксом id
			// стадии вместо канонической фазы — poller нормализует к
			// planning.q1.question.json (см. relocateMisplacedQuestions),
			// стадия проходит через awaiting_user_input и всё равно
			// доходит до done после ответа.
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
			// Агент пишет question.json ВНЕ stageDir (GLM-4.7-style баг) —
			// poller релоцирует файл внутрь stageDir и создаёт dangling-
			// symlink на answer.json по неверному пути; стадия проходит
			// через awaiting_user_input и доходит до done после ответа.
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
		{
			// Пользователь никогда не отвечает (Answer==""): стадия
			// достигает awaiting_user_input и остаётся там — synth-агент
			// сдаётся после ограниченного поллинга (~20с), не подвешивая
			// субпроцесс; сценарий завершается по ctx-таймауту харнесса.
			Name:   "hung-dialog",
			Stages: []flow.Stage{{ID: "hung-stage", Agents: []flow.AgentType{flow.AgentPlanning}}},
			Agents: map[string]AgentSpec{
				"hung-stage": {Interactive: true, Question: &QuestionInject{Phase: "planning", ID: "q1", Answer: "", Fault: FaultNone}},
			},
			Expect: Expectation{
				Statuses:             map[string]state.StageStatus{"hung-stage": state.StatusAwaitingUserInput},
				ReachesAwaitingInput: []string{"hung-stage"},
			},
		},
		{
			// InjectNoDone: RunAgent "отрабатывает" (err==nil), но не пишет .done —
			// имитирует агента, который вышел, не закончив работу. checkCompletion
			// возвращает IncompleteWorkError: runWithRetry (retry.go) даёт ОДИН
			// бесплатный повтор без backoff (attempt==0 → continue), но на втором
			// заходе (attempt==1, снова incomplete) считает это исчерпанным и
			// фейлит стадию — EvFail("missing artifact or incomplete"). Планирование
			// не затронуто (scriptedRunner.RunPlanning всегда пишет валидный
			// plan.md), поэтому стадия доходит до approval и до реализации, и
			// именно implementation-фаза уходит в failed.
			Name:   "incomplete-work-fails",
			Stages: []flow.Stage{{ID: "no-done", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}}},
			Agents: map[string]AgentSpec{"no-done": {Inject: InjectNoDone}},
			Expect: Expectation{Statuses: map[string]state.StageStatus{"no-done": state.StatusFailed}},
		},
		{
			// Предзасеянный events.jsonl с битой строкой В СЕРЕДИНЕ (валидная
			// строка после неё) — модель точь-в-точь TestOpen_MidCorruptionQuarantines
			// (pkg/state/store_replay_test.go): state.Open обязан вернуть
			// ErrCorruptLog ("events.jsonl is corrupted mid-log"), что уже
			// покрывается веткой RunErrSubstr в runScenario (см. Task 3,
			// scenario_harness_test.go).
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

// TestScenarioHarness_FailsOnWrongExpectation — негативный само-тест харнесса.
//
// Все остальные тесты в этом пакете доказывают, что сценарии проходят. Но
// это ничего не говорит о том, действительно ли assertExpectation детектирует
// расхождение, если оно есть — если бы assertExpectation была случайно
// выхолощена (например, кто-то убрал бы t.Errorf, оставив пустые циклы),
// TestScenarios продолжал бы зелёным, и никто бы не заметил, что харнесс
// перестал что-либо проверять.
//
// Наивный вариант — обернуть заведомо неверный сценарий в t.Run и проверить
// возвращённый bool — НЕ работает: провал вложенного subtest'а через
// t.Errorf/t.Fatalf безусловно помечает и родительский *testing.T как
// проваленный (это стандартное поведение пакета testing), независимо от
// того, что делает код после t.Run. Поэтому вместо настоящего *testing.T
// сюда передаётся recordingT — тестовый двойник expectationReporter
// (scenario_harness_test.go), который лишь накапливает сообщения Errorf, не
// трогая состояние текущего теста.
//
// План: прогнать заведомо рабочий сценарий (тот же паттерн, что и
// "auto-phase" в TestScenarios — стадия с flow.AgentAuto реально доходит до
// state.StatusDone через scriptedRunner) через runScenarioUpToAssert с
// НАСТОЯЩИМ t (сам прогон обязан пройти штатно), получить orch/runDir/reached,
// затем вызвать assertExpectation НАПРЯМУЮ с заведомо неверным Expectation
// (StatusFailed вместо фактического StatusDone) и recordingT. Если
// assertExpectation работает — recordingT.errors непуст. Если её
// когда-нибудь выхолостят — errors останется пустым, и здесь сработает
// t.Fatal: само-тест ОБНАРУЖИТ, что харнесс перестал детектировать провалы.
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

// recordingT — тестовый двойник expectationReporter (scenario_harness_test.go):
// вместо того чтобы безусловно валить вызывающий тест (как Errorf настоящего
// *testing.T), просто собирает сообщения. Используется ТОЛЬКО в
// TestScenarioHarness_FailsOnWrongExpectation, чтобы вызвать assertExpectation
// с заведомо неверным Expectation и убедиться, что она реально сообщает о
// провале, не заваливая сам тестовый процесс.
type recordingT struct {
	errors []string
}

func (r *recordingT) Helper() {}

func (r *recordingT) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}
