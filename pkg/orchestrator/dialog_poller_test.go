package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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

func newDialogPollCharacterization(t *testing.T, status state.StageStatus, interactive bool) (*Orchestrator, string) {
	t.Helper()
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Interactive: interactive, Agents: []flow.AgentType{flow.AgentImplementation}}
	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if status != state.StatusPending {
		if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: status, Event: "test_setup"}); err != nil {
			t.Fatal(err)
		}
	}
	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	t.Cleanup(o.concurrency.WaitAgents)
	stageDir := filepath.Join(runDir, stage.ID)
	writeQuestionFile(t, stageDir, "implementation", "q1", []string{"A", "B (recommended)"})
	return o, stageDir
}

func TestPollQuestions_ActiveStatusCharacterization(t *testing.T) {
	active := map[state.StageStatus]bool{
		state.StatusPlanning: true, state.StatusRunning: true, state.StatusRevising: true,
		state.StatusRetrying: true, state.StatusAwaitingUserInput: true,
	}
	for _, status := range state.AllStatuses() {
		t.Run(string(status), func(t *testing.T) {
			o, stageDir := newDialogPollCharacterization(t, status, false)
			processed := map[string]bool{}
			o.pollQuestions(processed, map[string]*malformedQuestionState{})
			if got := processed["s1|implementation|q1"]; got != active[status] {
				t.Errorf("processed = %v, want %v", got, active[status])
			}
			_, err := os.Stat(filepath.Join(stageDir, "implementation.q1.answer.json"))
			if active[status] && err != nil || !active[status] && !os.IsNotExist(err) {
				t.Errorf("answer stat = %v, want answer present = %v", err, active[status])
			}
			if got := o.currentStatus("s1"); got != status {
				t.Errorf("status = %s, want unchanged %s", got, status)
			}
		})
	}
}

func TestPollQuestions_AutoAnswerDeferredCharacterization(t *testing.T) {
	for _, cause := range []string{"review transaction", "exclusive write conflict"} {
		t.Run(cause, func(t *testing.T) {
			o, stageDir := newDialogPollCharacterization(t, state.StatusAwaitingUserInput, false)
			answerPath := filepath.Join(stageDir, "implementation.q1.answer.json")
			unblock := func() { o.reviewMarker.Store(nil) }
			if cause == "review transaction" {
				o.reviewMarker.Store(&state.PauseMarker{})
			} else {
				// A dangling link stays unanswered to Stat but forces O_EXCL to fail, even as root.
				if err := os.Symlink(filepath.Join(stageDir, "missing-answer"), answerPath); err != nil {
					t.Fatal(err)
				}
				unblock = func() {
					if err := os.Remove(answerPath); err != nil {
						t.Fatal(err)
					}
				}
			}
			subID, events := o.ui.Subscribe(16)
			defer o.ui.Unsubscribe(subID)
			processed := map[string]bool{}
			malformed := map[string]*malformedQuestionState{}
			o.pollQuestions(processed, malformed)
			if processed["s1|implementation|q1"] {
				t.Fatal("deferred question was marked processed")
			}
			if got := o.currentStatus("s1"); got != state.StatusAwaitingUserInput {
				t.Fatalf("status before answer = %s, want awaiting_user_input", got)
			}
			select {
			case event := <-o.critical.Recv():
				t.Fatalf("resumed before a successful answer write: %+v", event)
			default:
			}
			if got := drainAllEvents(events); len(got) != 0 {
				t.Fatalf("live events before answer = %+v, want none", got)
			}
			if got := readNotices(t, o.opts.RunDir); len(got) != 0 {
				t.Fatalf("durable notices before answer = %+v, want none", got)
			}
			entries, err := mcp.ReadDialog(mcp.DialogPath(stageDir, "implementation"))
			if err != nil || len(entries) != 1 || entries[0].Question != "which?" || entries[0].Answer != nil {
				t.Fatalf("deferred question history = %+v, %v", entries, err)
			}

			unblock()
			o.pollQuestions(processed, malformed)
			if !processed["s1|implementation|q1"] {
				t.Fatal("successful retry was not marked processed")
			}
			data, err := os.ReadFile(answerPath)
			if err != nil {
				t.Fatal(err)
			}
			var answer map[string]any
			if err := json.Unmarshal(data, &answer); err != nil {
				t.Fatal(err)
			}
			if want := (map[string]any{"id": "q1", "answer": "B", "from_options": true}); !reflect.DeepEqual(answer, want) {
				t.Fatalf("answer = %+v, want %+v", answer, want)
			}
			select {
			case event := <-o.critical.Recv():
				if event.Type != bus.EventUserAnswered || event.StageID != "s1" || !reflect.DeepEqual(event.Data, map[string]any{"id": "q1", "phase": "implementation", "answer": "B"}) {
					t.Fatalf("resume event = %+v", event)
				}
			default:
				t.Fatal("successful answer did not resume the parked stage")
			}
			live := drainAllEvents(events)
			notices := readNotices(t, o.opts.RunDir)
			if len(live) != 2 || live[0].Type != bus.EventDialogQuestion || live[1].Type != bus.EventAutoAnswered {
				t.Fatalf("live order = %+v, want exactly question then auto-answer", live)
			}
			if len(notices) != 2 || notices[0].Type != string(bus.EventDialogQuestion) || notices[1].Type != string(bus.EventAutoAnswered) {
				t.Fatalf("durable order = %+v, want exactly question then auto-answer", notices)
			}
			entries, err = mcp.ReadDialog(mcp.DialogPath(stageDir, "implementation"))
			if err != nil || len(entries) != 1 || entries[0].Answer == nil || *entries[0].Answer != "B" || !entries[0].AutoAnswered {
				t.Fatalf("answered history = %+v, %v", entries, err)
			}
		})
	}
}

func TestPollQuestions_InteractiveSequenceAndPhaseCharacterization(t *testing.T) {
	for _, tc := range []struct {
		status state.StageStatus
		phase  string
	}{
		{state.StatusPlanning, "planning"},
		{state.StatusRunning, "implementation"},
		{state.StatusAwaitingUserInput, "implementation"},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			o, _ := newDialogPollCharacterization(t, tc.status, true)
			subID, events := o.ui.Subscribe(16)
			defer o.ui.Unsubscribe(subID)
			processed := map[string]bool{}
			o.pollQuestions(processed, map[string]*malformedQuestionState{})
			if !processed["s1|implementation|q1"] || o.currentStatus("s1") != state.StatusAwaitingUserInput {
				t.Fatal("question was not processed and handed to the user")
			}
			if got, ok := o.preAskPhase.Load("s1"); !ok || got != tc.phase {
				t.Errorf("preAskPhase = %v, %v; want %s", got, ok, tc.phase)
			}
			history, err := o.opts.Store.History()
			if err != nil {
				t.Fatal(err)
			}
			var wantSeq uint64
			if tc.status != state.StatusAwaitingUserInput {
				last := history[len(history)-1]
				if last.Event != string(bus.EvAskUser) || last.From != tc.status || last.To != state.StatusAwaitingUserInput || last.Seq == 0 {
					t.Fatalf("ask transition = %+v", last)
				}
				wantSeq = last.Seq
			} else if len(history) != 1 {
				t.Fatalf("already awaiting question added a transition: %+v", history)
			}
			var dialog []bus.Event
			for _, event := range drainAllEvents(events) {
				if event.Type == bus.EventDialogQuestion || event.Type == bus.EventAskUser {
					dialog = append(dialog, event)
				}
			}
			if len(dialog) != 2 || dialog[0].Type != bus.EventDialogQuestion || dialog[1].Type != bus.EventAskUser || dialog[1].Seq != wantSeq {
				t.Fatalf("dialog events = %+v, want question then ask with seq %d", dialog, wantSeq)
			}
			wantData := map[string]any{"id": "q1", "phase": "implementation", "question": "which?", "options": []string{"A", "B (recommended)"}, "allow_custom": true}
			if !reflect.DeepEqual(dialog[1].Data, wantData) {
				t.Errorf("ask data = %+v, want %+v", dialog[1].Data, wantData)
			}
			notices := readNotices(t, o.opts.RunDir)
			if len(notices) != 1 || notices[0].Type != string(bus.EventDialogQuestion) {
				t.Fatalf("durable dialog notices = %+v", notices)
			}
			o.pollQuestions(processed, map[string]*malformedQuestionState{})
			if got := drainAllEvents(events); len(got) != 0 {
				t.Errorf("second scan duplicated events: %+v", got)
			}
		})
	}
}

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

	sawAutoAnswered := false
	for _, ev := range drainAllEvents(events) {
		if ev.Type == bus.EventAutoAnswered {
			sawAutoAnswered = true
		}
	}
	if !sawAutoAnswered {
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
	// Ключевой инвариант фикса: авто-ответ опубликовал EventUserAnswered в critical-шину — сигнал, по
	// которому onUserAnswered перезапускает вышедшего агента.
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
	// The feed now persists both a dialog_question and an auto_answered line;
	// locate the auto_answered one.
	var notice noticeEnvelope
	found := false
	for _, line := range strings.Split(strings.TrimSpace(string(noticesData)), "\n") {
		if line == "" {
			continue
		}
		var n noticeEnvelope
		if err := json.Unmarshal([]byte(line), &n); err != nil {
			t.Fatalf("invalid notices.jsonl line: %v (content: %s)", err, line)
		}
		if n.Type == string(bus.EventAutoAnswered) {
			notice = n
			found = true
		}
	}
	if !found {
		t.Fatalf("no auto_answered line in notices.jsonl (content: %s)", noticesData)
	}
	if notice.StageID != stage.ID || notice.Data.ID != "q1" || notice.Data.Answer != "Вариант B" {
		t.Errorf("notice content mismatch: %+v", notice)
	}
}

// rawNotice — одна строка notices.jsonl с data как произвольной картой, чтобы
// проверять наличие/отсутствие ключей (напр. dialog:true).
type rawNotice struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// readNotices читает все строки notices.jsonl в исходном порядке.
func readNotices(t *testing.T, runDir string) []rawNotice {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []rawNotice
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var n rawNotice
		if err := json.Unmarshal([]byte(line), &n); err != nil {
			t.Fatalf("invalid notices.jsonl line: %v (%s)", err, line)
		}
		out = append(out, n)
	}
	return out
}

// drainAllEvents non-blockingly drains every currently-buffered event from a UI
// bus subscription channel, preserving publish order.
func drainAllEvents(events <-chan bus.Event) []bus.Event {
	var got []bus.Event
	for {
		select {
		case ev := <-events:
			got = append(got, ev)
		default:
			return got
		}
	}
}

func TestPollQuestions_NonInteractive_EmitsQuestionThenNavigableAutoAnswer(t *testing.T) {
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
	subID, events := o.ui.Subscribe(64)
	defer o.ui.Unsubscribe(subID)

	o.pollQuestions(map[string]bool{}, map[string]*malformedQuestionState{})

	// dialog.jsonl carries both the question and the auto-answer.
	entries, err := mcp.ReadDialog(filepath.Join(stageDir, "implementation.dialog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Question != "which?" || entries[0].Answer == nil || *entries[0].Answer != "Вариант B" || !entries[0].AutoAnswered {
		t.Fatalf("dialog entry must carry question + auto-answer: %+v", entries)
	}

	// Live events: dialog_question BEFORE auto_answered, same phase+id.
	all := drainAllEvents(events)
	qIdx, aIdx := -1, -1
	var autoEv bus.Event
	for i, ev := range all {
		switch ev.Type {
		case bus.EventDialogQuestion:
			if qIdx == -1 {
				qIdx = i
			}
		case bus.EventAutoAnswered:
			if aIdx == -1 {
				aIdx = i
				autoEv = ev
			}
		default:
		}
	}
	if qIdx == -1 || aIdx == -1 {
		t.Fatalf("want both dialog_question and auto_answered, got events %+v", all)
	}
	if qIdx > aIdx {
		t.Errorf("dialog_question must be published before auto_answered, got q@%d a@%d", qIdx, aIdx)
	}
	qData, _ := all[qIdx].Data.(map[string]any)
	aData, _ := autoEv.Data.(map[string]any)
	if qData["phase"] != "implementation" || qData["id"] != "q1" {
		t.Errorf("dialog_question data mismatch: %+v", qData)
	}
	if aData["phase"] != "implementation" || aData["id"] != "q1" {
		t.Errorf("auto_answered data mismatch: %+v", aData)
	}
	if dialog, _ := aData["dialog"].(bool); !dialog {
		t.Errorf("auto_answered must carry dialog:true (navigable), got %v", aData["dialog"])
	}
	if title, _ := aData["question_title"].(string); title == "" {
		t.Errorf("auto_answered must carry a non-empty question_title, got %+v", aData)
	}
	if aData["answer"] != "Вариант B" {
		t.Errorf("auto_answered answer = %v, want %q", aData["answer"], "Вариант B")
	}

	// Durable notices.jsonl: same order + navigable marker survives replay.
	notices := readNotices(t, runDir)
	nq, na := -1, -1
	for i, n := range notices {
		switch n.Type {
		case string(bus.EventDialogQuestion):
			if nq == -1 {
				nq = i
			}
		case string(bus.EventAutoAnswered):
			if na == -1 {
				na = i
			}
		default:
		}
	}
	if nq == -1 || na == -1 {
		t.Fatalf("want both dialog_question and auto_answered in notices.jsonl, got %+v", notices)
	}
	if nq > na {
		t.Errorf("notices.jsonl order: dialog_question must precede auto_answered, got q@%d a@%d", nq, na)
	}
	if dialog, _ := notices[na].Data["dialog"].(bool); !dialog {
		t.Errorf("durable auto_answered must carry dialog:true, got %v", notices[na].Data["dialog"])
	}
}

func TestPollQuestions_MalformedRepairProgress_NotNavigable(t *testing.T) {
	o, _, _ := setupMalformedTestOrchNI(t, `not json at all {{{`)
	injectFixStub(t, o, "") // stays broken → a fix agent is spawned + progress notice emitted
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	o.pollQuestions(processed, malformed) // grace tick: remembers broken bytes
	o.pollQuestions(processed, malformed) // stable → spawn fix agent + ⚙️ progress notice

	notices := readNotices(t, o.opts.RunDir)
	var progress []rawNotice
	for _, n := range notices {
		if n.Type == string(bus.EventAutoAnswered) {
			progress = append(progress, n)
		}
	}
	if len(progress) == 0 {
		t.Fatal("expected at least one ⚙️ repair-progress auto_answered notice")
	}
	for _, n := range progress {
		if dialog, _ := n.Data["dialog"].(bool); dialog {
			t.Errorf("repair-progress notice must not be navigable (dialog:true), got %+v", n.Data)
		}
	}
}

func TestPollQuestions_MalformedNonInteractive_TerminalFallbackNavigable(t *testing.T) {
	o, store, stageDir := setupMalformedTestOrchNI(t, `not json at all {{{`)
	injectFixStub(t, o, "") // never repairs → terminal fallback
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	subID, events := o.ui.Subscribe(256)
	defer o.ui.Unsubscribe(subID)

	for i := 0; i < maxJSONFixAttempts+3; i++ {
		o.pollQuestions(processed, malformed)
	}

	if got := store.Snapshot().Stages["s1"].Status; got != state.StatusRunning {
		t.Fatalf("status = %s, want running (non-interactive never awaits a human)", got)
	}

	// A dialog row (synthetic question + auto-answer) exists.
	entries, err := mcp.ReadDialog(filepath.Join(stageDir, "implementation.dialog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Answer == nil || !entries[0].AutoAnswered {
		t.Fatalf("want 1 auto-answered dialog entry in the terminal fallback, got %+v", entries)
	}

	// A navigable dialog_question + auto_answered pair was published.
	all := drainAllEvents(events)
	sawDialogQ := false
	sawNavigableAuto := false
	for _, ev := range all {
		if ev.Type == bus.EventDialogQuestion {
			sawDialogQ = true
		}
		if ev.Type == bus.EventAutoAnswered {
			data, _ := ev.Data.(map[string]any)
			if dialog, _ := data["dialog"].(bool); dialog {
				sawNavigableAuto = true
			}
		}
	}
	if !sawDialogQ {
		t.Error("terminal fallback must publish a dialog_question")
	}
	if !sawNavigableAuto {
		t.Error("terminal fallback must publish a navigable (dialog:true) auto_answered")
	}

	// Durable copies survive replay.
	dq := readDialogQuestionNotices(t, o.opts.RunDir)
	if len(dq) == 0 {
		t.Error("terminal fallback dialog_question must be persisted to notices.jsonl")
	}
}

func TestPollQuestions_MalformedParkedNonInteractiveStageIsUnparked(t *testing.T) {
	o, store, _ := setupMalformedTestOrchNI(t, `not json at all {{{`)
	// Агент задал (битый) вопрос и вышел → стадия запаркована.
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusRunning, To: state.StatusAwaitingUserInput, Event: "test_park"}); err != nil {
		t.Fatal(err)
	}
	injectFixStub(t, o, "") // никогда не чинит → терминальный fallback
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	for i := 0; i < maxJSONFixAttempts+3; i++ {
		o.pollQuestions(processed, malformed)
	}

	select {
	case ev := <-o.critical.Recv():
		if ev.Type != bus.EventUserAnswered {
			t.Fatalf("critical event = %s, want %s (autoAnswerMalformed must resume a parked stage)", ev.Type, bus.EventUserAnswered)
		}
		if ev.StageID != "s1" {
			t.Fatalf("critical event stage = %s, want s1", ev.StageID)
		}
	default:
		t.Fatal("autoAnswerMalformed did not publish EventUserAnswered — a parked stage would hang forever")
	}
}

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

func TestPollQuestions_InteractiveQuestion_EmitsDialogQuestionOnce(t *testing.T) {
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
	question := "Which approach should we take?\nsome extra detail on a second line"
	payload, _ := json.Marshal(map[string]any{"id": "q1", "question": question, "options": []string{}, "allow_custom": true})
	if err := os.WriteFile(filepath.Join(stageDir, "implementation.q1.question.json"), payload, 0644); err != nil {
		t.Fatal(err)
	}

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	subID, events := o.ui.Subscribe(64)
	defer o.ui.Unsubscribe(subID)

	o.pollQuestions(map[string]bool{}, map[string]*malformedQuestionState{})

	wantTitle := mcp.DialogSnippet(question)
	dialogEvents := drainDialogQuestionEvents(events)
	if len(dialogEvents) != 1 {
		t.Fatalf("want exactly 1 EventDialogQuestion, got %d: %+v", len(dialogEvents), dialogEvents)
	}
	data, ok := dialogEvents[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("event Data has unexpected type: %T", dialogEvents[0].Data)
	}
	if data["phase"] != "implementation" || data["id"] != "q1" || data["title"] != wantTitle {
		t.Errorf("event data = %+v, want phase=implementation id=q1 title=%q", data, wantTitle)
	}

	notices := readDialogQuestionNotices(t, runDir)
	if len(notices) != 1 {
		t.Fatalf("want exactly 1 dialog_question line in notices.jsonl, got %d: %+v", len(notices), notices)
	}
	if notices[0].Phase != "implementation" || notices[0].ID != "q1" || notices[0].Title != wantTitle {
		t.Errorf("notice data = %+v, want phase=implementation id=q1 title=%q", notices[0], wantTitle)
	}

	// Interactive path is unchanged by the auto-answer feature: no auto_answered
	// event at all (the human answers), so certainly no navigable dialog:true one.
	for _, ev := range drainAllEvents(events) {
		if ev.Type == bus.EventAutoAnswered {
			t.Errorf("interactive stage must not publish auto_answered, got %+v", ev)
		}
	}
}

func TestPollQuestions_InteractiveBatch_SurfacesOldestOnly(t *testing.T) {
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

	// Agent wrote q1 and q2 at once (a batch); it polls q1.
	writeQuestionFile(t, stageDir, "implementation", "q1", []string{"A"})
	writeQuestionFile(t, stageDir, "implementation", "q2", []string{"A"})

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	subID, events := o.ui.Subscribe(64)
	defer o.ui.Unsubscribe(subID)

	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}
	o.pollQuestions(processed, malformed)

	first := drainDialogQuestionEvents(events)
	if len(first) != 1 || first[0].Data.(map[string]any)["id"] != "q1" {
		t.Fatalf("expected only q1 surfaced first, got %+v", first)
	}

	// Answer q1 → next tick surfaces q2.
	if err := os.WriteFile(filepath.Join(stageDir, "implementation.q1.answer.json"),
		[]byte(`{"id":"q1","answer":"A","from_options":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusAwaitingUserInput, To: state.StatusRunning, Event: "user_answered"}); err != nil {
		t.Fatal(err)
	}
	o.pollQuestions(processed, malformed)
	second := drainDialogQuestionEvents(events)
	if len(second) != 1 || second[0].Data.(map[string]any)["id"] != "q2" {
		t.Fatalf("expected q2 surfaced after answering q1, got %+v", second)
	}
}

func TestPollQuestions_Regression_BatchOfEight_NoDeadlock(t *testing.T) {
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

	// Agent wrote q1..q8 at once (a batch); it polls only q1.
	for i := 1; i <= 8; i++ {
		writeQuestionFile(t, stageDir, "autonomous_execution", "q"+strconv.Itoa(i), []string{"A"})
	}

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	subID, events := o.ui.Subscribe(64)
	defer o.ui.Unsubscribe(subID)

	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}
	o.pollQuestions(processed, malformed)

	ev := drainDialogQuestionEvents(events)
	if len(ev) != 1 || ev[0].Data.(map[string]any)["id"] != "q1" {
		t.Fatalf("batch of 8 must surface only q1, got %+v", ev)
	}

	// Answer q1 → the very next question (q2), not q3..q8, becomes current.
	if err := os.WriteFile(filepath.Join(stageDir, "autonomous_execution.q1.answer.json"),
		[]byte(`{"id":"q1","answer":"A","from_options":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusAwaitingUserInput, To: state.StatusRunning, Event: "user_answered"}); err != nil {
		t.Fatal(err)
	}
	o.pollQuestions(processed, malformed)
	ev = drainDialogQuestionEvents(events)
	if len(ev) != 1 || ev[0].Data.(map[string]any)["id"] != "q2" {
		t.Fatalf("after answering q1, only q2 must surface next, got %+v", ev)
	}
}

func TestPollQuestions_MalformedQuestion_GiveUpEmitsDialogQuestionImmediately(t *testing.T) {
	broken := `not json at all {{{`
	o, store, _ := setupMalformedTestOrch(t, broken)
	injectFixStub(t, o, "") // every fix agent fails to repair
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	subID, events := o.ui.Subscribe(256)
	defer o.ui.Unsubscribe(subID)

	// grace tick + maxJSONFixAttempts spawn/complete cycles + the exhaustion tick itself — exactly enough to
	// reach give-up, no extra re-poll ticks.
	for i := 0; i < maxJSONFixAttempts+2; i++ {
		o.pollQuestions(processed, malformed)
	}

	if got := store.Snapshot().Stages["s1"].Status; got != state.StatusAwaitingUserInput {
		t.Fatalf("status = %s, want awaiting_user_input", got)
	}

	dialogEvents := drainDialogQuestionEvents(events)
	if len(dialogEvents) != 1 {
		t.Fatalf("want exactly 1 EventDialogQuestion published on the exhaustion tick itself (no repoll), got %d: %+v", len(dialogEvents), dialogEvents)
	}

	notices := readDialogQuestionNotices(t, o.opts.RunDir)
	if len(notices) != 1 {
		t.Fatalf("want exactly 1 dialog_question line in notices.jsonl right after the exhaustion tick, got %d: %+v", len(notices), notices)
	}
}

func TestPollQuestions_MalformedQuestion_GiveUpThenRepollEmitIdenticalDialogQuestion(t *testing.T) {
	broken := `not json at all {{{`
	o, store, _ := setupMalformedTestOrch(t, broken)
	injectFixStub(t, o, "") // every fix agent fails to repair
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	subID, events := o.ui.Subscribe(256)
	defer o.ui.Unsubscribe(subID)

	// grace tick + maxJSONFixAttempts spawns + exhaustion tick + several
	// re-polls of the now-valid stub, with margin.
	for i := 0; i < maxJSONFixAttempts+5; i++ {
		o.pollQuestions(processed, malformed)
	}

	if got := store.Snapshot().Stages["s1"].Status; got != state.StatusAwaitingUserInput {
		t.Fatalf("status = %s, want awaiting_user_input", got)
	}

	dialogEvents := drainDialogQuestionEvents(events)
	if len(dialogEvents) == 0 {
		t.Fatal("want at least 1 EventDialogQuestion across the whole malformed→stub→repoll sequence")
	}
	for _, ev := range dialogEvents[1:] {
		if !reflect.DeepEqual(dialogEvents[0].Data, ev.Data) {
			t.Errorf("dialog_question payloads must be content-identical so read-side dedup collapses them: first=%+v other=%+v", dialogEvents[0].Data, ev.Data)
		}
	}

	notices := readDialogQuestionNotices(t, o.opts.RunDir)
	if len(notices) == 0 {
		t.Fatal("want at least 1 dialog_question line in notices.jsonl")
	}
	for _, n := range notices[1:] {
		if n != notices[0] {
			t.Errorf("notices.jsonl dialog_question lines must be content-identical, got %+v and %+v", notices[0], n)
		}
	}
}

// drainDialogQuestionEvents non-blockingly drains every currently-buffered
// EventDialogQuestion from a UI bus subscription channel.
func drainDialogQuestionEvents(events <-chan bus.Event) []bus.Event {
	var got []bus.Event
	for {
		select {
		case ev := <-events:
			if ev.Type == bus.EventDialogQuestion {
				got = append(got, ev)
			}
		default:
			return got
		}
	}
}

// dialogQuestionNotice — поле "data" одной строки notices.jsonl для
// dialog_question-уведомления (см. mcp.DialogFeedNotice).
type dialogQuestionNotice struct {
	Phase string `json:"phase"`
	ID    string `json:"id"`
	Title string `json:"title"`
}

// readDialogQuestionNotices читает notices.jsonl и возвращает данные всех
// строк с type == dialog_question.
func readDialogQuestionNotices(t *testing.T, runDir string) []dialogQuestionNotice {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []dialogQuestionNotice
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var envelope struct {
			Type string               `json:"type"`
			Data dialogQuestionNotice `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("invalid notices.jsonl line: %v (%s)", err, line)
		}
		if envelope.Type == string(bus.EventDialogQuestion) {
			out = append(out, envelope.Data)
		}
	}
	return out
}

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

func TestPollQuestions_ReusedID_ReopensDialogHistory(t *testing.T) {
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
	dialogPath := filepath.Join(stageDir, "implementation.dialog.jsonl")
	writeQ := func(question string) {
		payload, _ := json.Marshal(map[string]any{"id": "q1", "question": question, "allow_custom": true})
		if err := os.WriteFile(qPath, payload, 0644); err != nil {
			t.Fatal(err)
		}
	}

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	// Round 1: first question asked.
	writeQ("first?")
	o.pollQuestions(processed, malformed)

	// User answers through the real path (writes answer.json AND the dialog
	// answer line); agent resumes and consumes the answer file.
	if err := mcp.WriteAnswer(stageDir, "implementation", "q1", "A", false, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusAwaitingUserInput, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	o.pollQuestions(processed, malformed) // prune the answered key
	if err := os.Remove(filepath.Join(stageDir, "implementation.q1.answer.json")); err != nil {
		t.Fatal(err)
	}

	// Round 2: same id reused for a genuinely new question.
	writeQ("second?")
	o.pollQuestions(processed, malformed)

	entries, err := mcp.ReadDialog(dialogPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 collapsed entry for q1, got %d: %+v", len(entries), entries)
	}
	if entries[0].Question != "second?" {
		t.Errorf("dialog history must show the reused question's NEW text, got %q — poller did not re-append it", entries[0].Question)
	}
	if entries[0].Answer != nil {
		t.Errorf("dialog history must read the reused question as unanswered, got answer=%q", *entries[0].Answer)
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

// setupMalformedTestOrchNI is setupMalformedTestOrch for a NON-interactive stage — the stage type where an
// unparseable question.json used to hang the stage forever.
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

// injectFixStub replaces the orchestrator's real fix-agent spawner with a synchronous test double, so unit
// tests never launch a real agent process.
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

func TestHandleMalformedQuestion_ExhaustedShowsRawTextNoOptions(t *testing.T) {
	broken := `not json at all {{{`
	o, store, stageDir := setupMalformedTestOrch(t, broken)
	qPath := filepath.Join(stageDir, "implementation.q1.question.json")

	// Seed state as if maxJSONFixAttempts fix agents already failed and this tick's content is stable — the
	// exact state pollQuestions would have built up, without simulating each round here.
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
