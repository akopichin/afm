package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

func writeQuestionFile(t *testing.T, stageDir, phase, id string, options []string) {
	t.Helper()
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"id": id, "question": "which?", "options": options, "allow_custom": true,
	})
	path := filepath.Join(stageDir, phase+"."+id+".question.json")
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatal(err)
	}
}

// TestPollQuestions_NonInteractiveStageAutoAnswers покрывает ядро фичи:
// non-interactive стадия с открытым вопросом получает ответ от afm, не
// переходя в awaiting_user_input.
func TestPollQuestions_NonInteractiveStageAutoAnswers(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Backend", Agents: []flow.AgentType{flow.AgentImplementation}}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	stageDir := filepath.Join(runDir, stage.ID)
	writeQuestionFile(t, stageDir, "implementation", "q1", []string{"Вариант A", "Вариант B (recommended)"})

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})

	subID, events := o.ui.Subscribe(8)
	defer o.ui.Unsubscribe(subID)

	o.pollQuestions(map[string]bool{}, map[string]*malformedQuestionState{})

	data, err := os.ReadFile(filepath.Join(stageDir, "implementation.q1.answer.json"))
	if err != nil {
		t.Fatalf("answer.json not written: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["answer"] != "Вариант B" {
		t.Errorf("auto-answer = %v, want %q", got["answer"], "Вариант B")
	}

	select {
	case ev := <-events:
		if ev.Type != bus.EventAutoAnswered {
			t.Errorf("got event %s, want %s", ev.Type, bus.EventAutoAnswered)
		}
	default:
		t.Fatal("expected EventAutoAnswered to be published")
	}

	if got := store.Snapshot().Stages[stage.ID].Status; got != state.StatusRunning {
		t.Errorf("stage status = %s, want unchanged %s (no FSM transition on auto-answer)", got, state.StatusRunning)
	}

	entries, err := mcp.ReadDialog(filepath.Join(stageDir, "implementation.dialog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Answer == nil || !entries[0].AutoAnswered {
		t.Fatalf("dialog entry missing auto_answered marker: %+v", entries)
	}
}

// TestPollQuestions_AutoStageAutoAnswers покрывает "agents: [auto]" стадию —
// та же non-interactive обработка (Interactive: false у auto-стадий и так
// по умолчанию, специальной проверки на IsAuto() коду не требуется).
func TestPollQuestions_AutoStageAutoAnswers(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Autonomous", Agents: []flow.AgentType{flow.AgentAuto}}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	stageDir := filepath.Join(runDir, stage.ID)
	writeQuestionFile(t, stageDir, "autonomous_execution", "q1", []string{"Вариант A"})

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	o.pollQuestions(map[string]bool{}, map[string]*malformedQuestionState{})

	if _, err := os.Stat(filepath.Join(stageDir, "autonomous_execution.q1.answer.json")); err != nil {
		t.Errorf("auto-стадия должна получить авто-ответ так же, как обычная non-interactive: %v", err)
	}
	if got := store.Snapshot().Stages[stage.ID].Status; got != state.StatusRunning {
		t.Errorf("stage status = %s, want unchanged %s", got, state.StatusRunning)
	}
}

// TestPollQuestions_ParkedNonInteractiveStageIsUnparked закрывает постоянный
// висяк: non-interactive/autonomous стадия, чей агент задал вопрос и завершился,
// не дописав артефакт, паркуется в awaiting_user_input (retry.go при err==nil /
// onAgentCompleted). Авто-ответ пишет answer.json, но раньше НЕ выводил стадию из
// awaiting_user_input и не перезапускал вышедшего агента — единственный драйвер
// выхода из этого статуса (EvUserAnswered) публиковался лишь human-путём
// (NotifyAnswer через HTTP). Итог: стадия висела навсегда, даже рестарт afm не
// спасал. Теперь авто-ответ симметричен human-пути: публикует EventUserAnswered
// в critical-шину, откуда onUserAnswered перезапускает вышедшего агента.
func TestPollQuestions_ParkedNonInteractiveStageIsUnparked(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Autonomous", Agents: []flow.AgentType{flow.AgentAuto}}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	// Агент задал вопрос и вышел → стадия запаркована в awaiting_user_input,
	// агентской горутины больше нет (IsActive == false).
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusRunning, To: state.StatusAwaitingUserInput, Event: "test_park"}); err != nil {
		t.Fatal(err)
	}

	stageDir := filepath.Join(runDir, stage.ID)
	writeQuestionFile(t, stageDir, "autonomous_execution", "q1", []string{"Вариант A (recommended)"})

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	o.pollQuestions(map[string]bool{}, map[string]*malformedQuestionState{})

	if _, err := os.Stat(filepath.Join(stageDir, "autonomous_execution.q1.answer.json")); err != nil {
		t.Fatalf("answer.json not written: %v", err)
	}
	// Ключевой инвариант фикса: авто-ответ опубликовал EventUserAnswered в
	// critical-шину — сигнал, по которому onUserAnswered перезапускает вышедшего
	// агента. Без него стадия зависла бы в awaiting_user_input навсегда.
	select {
	case ev := <-o.critical.Recv():
		if ev.Type != bus.EventUserAnswered {
			t.Fatalf("critical event = %s, want %s", ev.Type, bus.EventUserAnswered)
		}
		if ev.StageID != stage.ID {
			t.Fatalf("critical event stage = %s, want %s", ev.StageID, stage.ID)
		}
	default:
		t.Fatal("ожидался EventUserAnswered в critical-шине для вывода стадии из awaiting_user_input")
	}
}

// TestPollQuestions_FailedAutoAnswerWriteIsRetried закрывает баг порядка
// "пометил-до-записи": раньше processed[key] ставился ДО WriteAnswer, поэтому
// упавшая запись answer.json (ошибка диска / O_EXCL-гонка) навсегда оставляла
// ключ помеченным без файла на диске — авто-ответ никогда не переретраивался, и
// bash-loop агента ждал answer.json до idle-таймаута исполнителя (~30 мин) →
// падение. Теперь ключ помечается только после успешной записи.
func TestPollQuestions_FailedAutoAnswerWriteIsRetried(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("read-only каталог не ограничивает root — проверка бессмысленна")
	}
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Backend", Agents: []flow.AgentType{flow.AgentImplementation}}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	stageDir := filepath.Join(runDir, stage.ID)
	writeQuestionFile(t, stageDir, "implementation", "q1", []string{"A (recommended)"})

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})

	// Каталог стадии только для чтения → создание answer.json упадёт.
	if err := os.Chmod(stageDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stageDir, 0o755) })

	processed := map[string]bool{}
	o.pollQuestions(processed, map[string]*malformedQuestionState{})

	key := "s1|implementation|q1"
	if processed[key] {
		t.Fatalf("processed[%q] не должен ставиться при неудачной записи — иначе авто-ответ не переретраится", key)
	}
	if _, err := os.Stat(filepath.Join(stageDir, "implementation.q1.answer.json")); !os.IsNotExist(err) {
		t.Fatalf("answer.json не должен существовать после неудачной записи: err=%v", err)
	}

	// Восстанавливаем права → следующий тик обязан успешно записать answer.json.
	if err := os.Chmod(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	o.pollQuestions(processed, map[string]*malformedQuestionState{})
	if _, err := os.Stat(filepath.Join(stageDir, "implementation.q1.answer.json")); err != nil {
		t.Fatalf("answer.json должен появиться на ретрае после восстановления прав: %v", err)
	}
}

// noticeEnvelopeData — поле "data" одной строки notices.jsonl для
// auto_answered-уведомления (см. TestPollQuestions_AutoAnswerPersistsToNotices).
type noticeEnvelopeData struct {
	ID     string `json:"id"`
	Phase  string `json:"phase"`
	Answer string `json:"answer"`
}

// noticeEnvelope — одна строка notices.jsonl (см. stagefiles.AppendNotice).
type noticeEnvelope struct {
	Type    string             `json:"type"`
	StageID string             `json:"stage_id"`
	Data    noticeEnvelopeData `json:"data"`
}

// TestPollQuestions_AutoAnswerPersistsToNotices закрывает баг, найденный
// вручную в браузере: EventAutoAnswered публикуется ТОЛЬКО живьём в UI-шину,
// поэтому клиент, подключившийся ПОСЛЕ авто-ответа, никогда не видит эту
// строку в ленте событий (durable events.jsonl эта фича сознательно не
// трогает — FSM-переход отсутствует). Как и EventAgentCompleted/
// EventContextWarning/EventScriptOutput (см. agents.go/retry.go/hooks.go),
// авто-ответ должен ТАКЖЕ дублироваться в notices.jsonl через
// stagefiles.AppendNotice — /api/events (reconstructNotices) реплеит его
// оттуда для клиентов, подключившихся позже.
func TestPollQuestions_AutoAnswerPersistsToNotices(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Backend", Agents: []flow.AgentType{flow.AgentImplementation}}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	stageDir := filepath.Join(runDir, stage.ID)
	writeQuestionFile(t, stageDir, "implementation", "q1", []string{"Вариант A", "Вариант B (recommended)"})

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	o.pollQuestions(map[string]bool{}, map[string]*malformedQuestionState{})

	noticesData, err := os.ReadFile(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		t.Fatalf("notices.jsonl not written: %v", err)
	}
	var notice noticeEnvelope
	if err := json.Unmarshal(noticesData, &notice); err != nil {
		t.Fatalf("invalid notices.jsonl line: %v (content: %s)", err, noticesData)
	}
	if notice.Type != string(bus.EventAutoAnswered) {
		t.Errorf("notice type = %q, want %q", notice.Type, bus.EventAutoAnswered)
	}
	if notice.StageID != stage.ID || notice.Data.ID != "q1" || notice.Data.Answer != "Вариант B" {
		t.Errorf("notice content mismatch: %+v", notice)
	}
}

// TestPollQuestions_InteractiveStageStillAsksUser — регрессионная гарантия:
// interactive-стадия НЕ получает авто-ответ, поведение (EvAskUser →
// awaiting_user_input) не меняется этой фичей.
func TestPollQuestions_InteractiveStageStillAsksUser(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Interactive", Agents: []flow.AgentType{flow.AgentImplementation}, Interactive: true}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	stageDir := filepath.Join(runDir, stage.ID)
	writeQuestionFile(t, stageDir, "implementation", "q1", []string{"Вариант A (recommended)"})

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	o.pollQuestions(map[string]bool{}, map[string]*malformedQuestionState{})

	if _, err := os.Stat(filepath.Join(stageDir, "implementation.q1.answer.json")); err == nil {
		t.Error("interactive-стадия не должна получать авто-ответ")
	}
	if got := store.Snapshot().Stages[stage.ID].Status; got != state.StatusAwaitingUserInput {
		t.Errorf("stage status = %s, want %s", got, state.StatusAwaitingUserInput)
	}
}

// TestPollQuestions_ReusedIDAfterAnswerAsksAgain is a regression test for a
// bug found in a real production log: the prompt tells agents "never reuse an
// ID within a phase", but a real agent (goga-brainstorm's revision loop) did
// reuse the same id ("q2") for a second, distinct question after the first
// "q2" was answered and the agent resumed. pollQuestions's `processed` map is
// keyed only by stageID|phase|id and, once marked true, was NEVER cleared —
// so the second, genuinely-unanswered "q2" was silently invisible to the
// poller forever: no EvAskUser, no dialog.jsonl entry, stage stuck at
// "running" with a real unanswered question.json sitting on disk. No restart
// of the browser fixes this (the bug is server-side, in-memory); only a full
// restart of the afm process happens to clear the map and rediscover it.
func TestPollQuestions_ReusedIDAfterAnswerAsksAgain(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Interactive", Agents: []flow.AgentType{flow.AgentImplementation}, Interactive: true}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	stageDir := filepath.Join(runDir, stage.ID)
	writeQuestionFile(t, stageDir, "implementation", "q1", []string{"A"})

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	// Round 1: question appears, poller asks the user.
	o.pollQuestions(processed, malformed)
	if got := store.Snapshot().Stages[stage.ID].Status; got != state.StatusAwaitingUserInput {
		t.Fatalf("round 1: status = %s, want awaiting_user_input", got)
	}

	// User answers; agent resumes (Running) and consumes+removes the answer,
	// mirroring the real bash loop's `cat` + later `rm -f` pattern.
	answerPath := filepath.Join(stageDir, "implementation.q1.answer.json")
	if err := os.WriteFile(answerPath, []byte(`{"id":"q1","answer":"A","from_options":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusAwaitingUserInput, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	o.pollQuestions(processed, malformed) // must prune the now-answered key from `processed`
	if err := os.Remove(answerPath); err != nil {
		t.Fatal(err)
	}

	// Agent reuses "q1" for a brand new, distinct question.
	writeQuestionFile(t, stageDir, "implementation", "q1", []string{"B"})

	o.pollQuestions(processed, malformed)
	if got := store.Snapshot().Stages[stage.ID].Status; got != state.StatusAwaitingUserInput {
		t.Errorf("round 2 (reused id): status = %s, want awaiting_user_input — reused question was never re-surfaced", got)
	}
}

// setupMalformedTestOrch builds an interactive stage in StatusRunning with a
// broken question.json, shared by the malformed-JSON retry tests below.
func setupMalformedTestOrch(t *testing.T, broken string) (*Orchestrator, *state.Store, string) {
	t.Helper()
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Interactive", Agents: []flow.AgentType{flow.AgentImplementation}, Interactive: true}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	stageDir := filepath.Join(runDir, stage.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	qPath := filepath.Join(stageDir, "implementation.q1.question.json")
	if err := os.WriteFile(qPath, []byte(broken), 0644); err != nil {
		t.Fatal(err)
	}

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	return o, store, stageDir
}

// setupMalformedTestOrchNI is setupMalformedTestOrch for a NON-interactive
// stage — the stage type where an unparseable question.json used to hang the
// stage forever (the whole reason the fresh-agent repair is no longer
// interactive-only).
func setupMalformedTestOrchNI(t *testing.T, broken string) (*Orchestrator, *state.Store, string) {
	t.Helper()
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Auto", Agents: []flow.AgentType{flow.AgentImplementation}}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(runDir, stage.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "implementation.q1.question.json"), []byte(broken), 0644); err != nil {
		t.Fatal(err)
	}
	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	return o, store, stageDir
}

// injectFixStub replaces the orchestrator's real fix-agent spawner with a
// synchronous test double, so unit tests never launch a real agent process.
// If fixed != "", the stub writes it to the question.json (a fix agent that
// succeeds); if "", it leaves the file broken (a fix agent that fails). It
// always returns an already-closed channel (the agent "finished"). The
// returned pointer counts how many times a fix agent was spawned.
func injectFixStub(t *testing.T, o *Orchestrator, fixed string) *int {
	t.Helper()
	calls := 0
	o.spawnJSONFix = func(s flow.Stage, phase, id string) <-chan struct{} {
		calls++
		if fixed != "" {
			qPath := filepath.Join(o.opts.RunDir, s.ID, phase+"."+id+".question.json")
			if err := os.WriteFile(qPath, []byte(fixed), 0644); err != nil {
				t.Errorf("fix stub write: %v", err)
			}
		}
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return &calls
}

// TestPollQuestions_MalformedQuestion_GraceTickHidesFromUser is a regression
// test for a bug found in a real production log, root-caused byte-for-byte:
// a question.json that fails to parse on its FIRST observation is very
// often a torn read — afm's poller caught the file while the agent's Write
// tool call was still landing on disk — not a genuine mistake. The very
// first sighting of broken content must never reach the user: no dialog
// entry, no synthetic answer, no EvAskUser, stage status untouched.
func TestPollQuestions_MalformedQuestion_GraceTickHidesFromUser(t *testing.T) {
	o, store, stageDir := setupMalformedTestOrch(t, `not json at all {{{`)

	o.pollQuestions(map[string]bool{}, map[string]*malformedQuestionState{})

	if got := store.Snapshot().Stages["s1"].Status; got != state.StatusRunning {
		t.Errorf("status = %s, want unchanged running (grace tick must not surface anything)", got)
	}
	if _, err := os.Stat(filepath.Join(stageDir, "implementation.q1.answer.json")); err == nil {
		t.Error("no answer.json should be written on the very first sighting of broken content")
	}
	if entries, _ := mcp.ReadDialog(filepath.Join(stageDir, "implementation.dialog.jsonl")); len(entries) != 0 {
		t.Errorf("no dialog entry should exist yet, got %+v", entries)
	}
}

// TestPollQuestions_MalformedQuestion_ResolvesSilentlyIfWriteCompletes proves
// the grace-tick mechanism actually fixes the real incident: once the
// (previously torn) file becomes valid, it must flow through completely
// normally on the next tick — no trace that anything was ever wrong, no
// agent involvement.
func TestPollQuestions_MalformedQuestion_ResolvesSilentlyIfWriteCompletes(t *testing.T) {
	o, store, stageDir := setupMalformedTestOrch(t, `not json at all {{{`)
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	o.pollQuestions(processed, malformed) // grace tick

	qPath := filepath.Join(stageDir, "implementation.q1.question.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"real question?"}`), 0644); err != nil {
		t.Fatal(err)
	}

	o.pollQuestions(processed, malformed)

	if got := store.Snapshot().Stages["s1"].Status; got != state.StatusAwaitingUserInput {
		t.Fatalf("status = %s, want awaiting_user_input once the write completed", got)
	}
	entries, err := mcp.ReadDialog(filepath.Join(stageDir, "implementation.dialog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Question != "real question?" {
		t.Fatalf("expected the real question text, got %+v", entries)
	}
}

// TestPollQuestions_MalformedQuestion_StableSpawnsFixAgent covers the
// genuinely-broken case: once the SAME broken content is observed on a second
// tick — proof the write is done, not still in flight — afm launches a fresh,
// clean-context fix agent to repair the file, still without bothering the user
// and without writing any synthetic answer.json.
func TestPollQuestions_MalformedQuestion_StableSpawnsFixAgent(t *testing.T) {
	o, store, stageDir := setupMalformedTestOrch(t, `not json at all {{{`)
	calls := injectFixStub(t, o, "") // a fix agent that does not repair the file
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	o.pollQuestions(processed, malformed) // grace tick: remembers the broken bytes
	if *calls != 0 {
		t.Fatalf("no fix agent should spawn on the very first sighting, got %d", *calls)
	}
	o.pollQuestions(processed, malformed) // same bytes again: stable → spawn fix agent

	if *calls != 1 {
		t.Fatalf("want exactly one fix-agent spawn, got %d", *calls)
	}
	if got := store.Snapshot().Stages["s1"].Status; got != state.StatusRunning {
		t.Errorf("status = %s, want unchanged running (still repairing, not asking the user)", got)
	}
	if _, err := os.Stat(filepath.Join(stageDir, "implementation.q1.answer.json")); err == nil {
		t.Error("fresh-agent repair must not write a synthetic answer.json")
	}
	if entries, _ := mcp.ReadDialog(filepath.Join(stageDir, "implementation.dialog.jsonl")); len(entries) != 0 {
		t.Errorf("no dialog entry should exist yet, got %+v", entries)
	}
}

// TestPollQuestions_MalformedQuestion_FixAgentRepairsQuestion proves the fix
// agent's repair is actually picked up: once it rewrites the SAME id's
// question.json with valid content, the question flows through completely
// normally on the next tick (surfaced to the user), the tracking entry is
// forgotten, and there is no trace the repair ever happened.
func TestPollQuestions_MalformedQuestion_FixAgentRepairsQuestion(t *testing.T) {
	o, store, stageDir := setupMalformedTestOrch(t, `not json at all {{{`)
	calls := injectFixStub(t, o, `{"id":"q1","question":"fixed now?"}`)
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	o.pollQuestions(processed, malformed) // grace tick
	o.pollQuestions(processed, malformed) // stable + broken: fix agent repairs synchronously
	if *calls != 1 {
		t.Fatalf("want one fix-agent spawn, got %d", *calls)
	}
	o.pollQuestions(processed, malformed) // repaired file now surfaces normally

	if got := store.Snapshot().Stages["s1"].Status; got != state.StatusAwaitingUserInput {
		t.Fatalf("status = %s, want awaiting_user_input once the repaired question is visible", got)
	}
	entries, err := mcp.ReadDialog(filepath.Join(stageDir, "implementation.dialog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Question != "fixed now?" {
		t.Fatalf("expected the repaired question text, got %+v", entries)
	}
	if _, stillTracked := malformed["s1|implementation|q1"]; stillTracked {
		t.Error("key should be forgotten once the file parses again")
	}
}

// TestPollQuestions_MalformedQuestion_RealAnswerSurvivesAfterRecovery guards
// the class of bug the old nudge mechanism had to work around: after a
// malformed question is repaired and a human answers it normally, later ticks
// must not touch that real answer.json. With the fresh-agent mechanism no
// synthetic answer.json is ever written and reconcileMalformedFixes forgets the
// key the moment the file parses, so the bug is impossible by construction —
// this test locks that in.
func TestPollQuestions_MalformedQuestion_RealAnswerSurvivesAfterRecovery(t *testing.T) {
	o, store, stageDir := setupMalformedTestOrch(t, `not json at all {{{`)
	injectFixStub(t, o, `{"id":"q1","question":"fixed?"}`)
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	o.pollQuestions(processed, malformed) // grace tick
	o.pollQuestions(processed, malformed) // fix agent repairs the file
	o.pollQuestions(processed, malformed) // surfaces the real question

	if got := store.Snapshot().Stages["s1"].Status; got != state.StatusAwaitingUserInput {
		t.Fatalf("status = %s, want awaiting_user_input after recovery", got)
	}

	// A human answers normally, exactly like handleDialogAnswer would.
	answerPath := filepath.Join(stageDir, "implementation.q1.answer.json")
	if err := os.WriteFile(answerPath, []byte(`{"id":"q1","answer":"yes","from_options":false}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusAwaitingUserInput, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	// Further ticks must leave the real answer alone.
	for i := 0; i < 3; i++ {
		o.pollQuestions(processed, malformed)
	}

	if _, err := os.Stat(answerPath); err != nil {
		t.Fatalf("real answer.json was deleted by stale malformed tracking: %v", err)
	}
}

// TestPollQuestions_MalformedQuestion_FixAgentExhaustionShowsStub covers the
// interactive terminal fallback: when every fix agent fails to repair the
// file, afm must stop spawning agents after maxJSONFixAttempts and surface a
// parseable stub (no options, free text) to the user instead of hanging.
func TestPollQuestions_MalformedQuestion_FixAgentExhaustionShowsStub(t *testing.T) {
	broken := `not json at all {{{`
	o, store, stageDir := setupMalformedTestOrch(t, broken)
	calls := injectFixStub(t, o, "") // every fix agent fails to repair
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	// grace tick + maxJSONFixAttempts spawns + an exhaustion tick, with margin.
	for i := 0; i < maxJSONFixAttempts+3; i++ {
		o.pollQuestions(processed, malformed)
	}

	if *calls != maxJSONFixAttempts {
		t.Fatalf("want exactly %d fix-agent spawns before giving up, got %d", maxJSONFixAttempts, *calls)
	}
	if got := store.Snapshot().Stages["s1"].Status; got != state.StatusAwaitingUserInput {
		t.Fatalf("status = %s, want awaiting_user_input once fix attempts are exhausted", got)
	}
	data, err := os.ReadFile(filepath.Join(stageDir, "implementation.q1.question.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stub struct {
		Options []string `json:"options"`
	}
	if err := json.Unmarshal(data, &stub); err != nil {
		t.Fatalf("persisted stub is not valid JSON: %v", err)
	}
	if len(stub.Options) != 0 {
		t.Errorf("stub must offer no options, got %v", stub.Options)
	}
}

// TestPollQuestions_MalformedQuestion_NonInteractiveFixThenAutoAnswer is the
// core of the incident this whole change fixes: a NON-interactive (agents:[auto])
// stage whose question.json is unparseable. Before, the malformed branch was
// interactive-only and such a stage's agent polled for an answer.json forever.
// Now a fix agent repairs the file and the normal non-interactive auto-answer
// path unblocks the stage — without ever awaiting a human.
func TestPollQuestions_MalformedQuestion_NonInteractiveFixThenAutoAnswer(t *testing.T) {
	o, store, stageDir := setupMalformedTestOrchNI(t, `not json at all {{{`)
	injectFixStub(t, o, `{"id":"q1","question":"which?","options":["A","B (recommended)"],"allow_custom":true}`)
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	o.pollQuestions(processed, malformed) // grace tick
	o.pollQuestions(processed, malformed) // fix agent repairs synchronously
	o.pollQuestions(processed, malformed) // repaired → auto-answered

	if got := store.Snapshot().Stages["s1"].Status; got != state.StatusRunning {
		t.Fatalf("status = %s, want running (non-interactive auto-answers, never awaits a human)", got)
	}
	data, err := os.ReadFile(filepath.Join(stageDir, "implementation.q1.answer.json"))
	if err != nil {
		t.Fatalf("answer.json not written: %v", err)
	}
	var got struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Answer != "B" {
		t.Errorf("auto-answer = %q, want recommended option %q", got.Answer, "B")
	}
}

// TestPollQuestions_MalformedQuestion_NonInteractiveFixFailsThenAutoAnswerFallback
// covers the non-interactive terminal fallback: even when no fix agent can
// repair the file, the stage must be unblocked by an auto-answer rather than
// left polling forever (the exact hang seen in the production log).
func TestPollQuestions_MalformedQuestion_NonInteractiveFixFailsThenAutoAnswerFallback(t *testing.T) {
	o, store, stageDir := setupMalformedTestOrchNI(t, `not json at all {{{`)
	calls := injectFixStub(t, o, "") // never repairs
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	for i := 0; i < maxJSONFixAttempts+3; i++ {
		o.pollQuestions(processed, malformed)
	}

	if *calls != maxJSONFixAttempts {
		t.Fatalf("want %d fix attempts, got %d", maxJSONFixAttempts, *calls)
	}
	if got := store.Snapshot().Stages["s1"].Status; got != state.StatusRunning {
		t.Fatalf("status = %s, want running (auto-answered fallback, never awaits)", got)
	}
	data, err := os.ReadFile(filepath.Join(stageDir, "implementation.q1.answer.json"))
	if err != nil {
		t.Fatalf("fallback answer.json not written — stage would hang forever: %v", err)
	}
	var got struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Answer == "" {
		t.Error("fallback answer must be non-empty so the agent's bash loop unblocks")
	}
}

// TestHandleMalformedQuestion_ExhaustedShowsRawTextNoOptions covers the
// user's explicit fallback: once retries are exhausted, afm stops asking the
// agent for valid JSON, persists a real parseable stub (handleDialogAnswer
// re-parses question.json strictly on every answer submission), and shows
// the raw text to the user with NO options — free text only.
func TestHandleMalformedQuestion_ExhaustedShowsRawTextNoOptions(t *testing.T) {
	broken := `not json at all {{{`
	o, store, stageDir := setupMalformedTestOrch(t, broken)
	qPath := filepath.Join(stageDir, "implementation.q1.question.json")

	// Seed state as if maxJSONFixAttempts fix agents already failed and this
	// tick's content is stable — the exact state pollQuestions would have built
	// up, without simulating each round here.
	malformed := map[string]*malformedQuestionState{
		"s1|implementation|q1": {lastRaw: []byte(broken), attempts: maxJSONFixAttempts},
	}

	o.pollQuestions(map[string]bool{}, malformed)

	if got := store.Snapshot().Stages["s1"].Status; got != state.StatusAwaitingUserInput {
		t.Fatalf("status = %s, want awaiting_user_input once retries are exhausted", got)
	}
	if _, stillTracked := malformed["s1|implementation|q1"]; stillTracked {
		t.Error("key should be removed from `malformed` once given up on")
	}

	// The persisted stub must be valid, parseable JSON with no options.
	data, err := os.ReadFile(qPath)
	if err != nil {
		t.Fatal(err)
	}
	var stub struct {
		ID          string   `json:"id"`
		Question    string   `json:"question"`
		Options     []string `json:"options"`
		AllowCustom bool     `json:"allow_custom"`
	}
	if err := json.Unmarshal(data, &stub); err != nil {
		t.Fatalf("persisted stub is not valid JSON: %v\nraw: %s", err, data)
	}
	if len(stub.Options) != 0 {
		t.Errorf("stub must offer no options (free text only), got %v", stub.Options)
	}
	if !stub.AllowCustom {
		t.Error("stub must allow a custom (free-text) answer")
	}
	if !strings.Contains(stub.Question, broken) {
		t.Errorf("stub question should include the raw broken content for the user to read, got %q", stub.Question)
	}

	entries, err := mcp.ReadDialog(filepath.Join(stageDir, "implementation.dialog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 dialog entry, got %+v", entries)
	}
}
