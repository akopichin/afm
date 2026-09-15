package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// dialogAnswerNotice — поле "data" одной строки notices.jsonl для
// dialog_answer-уведомления (см. mcp.DialogFeedNotice).
type dialogAnswerNotice struct {
	Phase string `json:"phase"`
	ID    string `json:"id"`
	Title string `json:"title"`
}

// readDialogAnswerNotices читает notices.jsonl и возвращает данные всех строк
// с type == dialog_answer.
func readDialogAnswerNotices(t *testing.T, runDir string) []dialogAnswerNotice {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []dialogAnswerNotice
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var envelope struct {
			Type string             `json:"type"`
			Data dialogAnswerNotice `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("invalid notices.jsonl line: %v (%s)", err, line)
		}
		if envelope.Type == string(bus.EventDialogAnswer) {
			out = append(out, envelope.Data)
		}
	}
	return out
}

// drainDialogAnswerEvents non-blockingly drains every currently-buffered
// EventDialogAnswer from a UI bus subscription channel.
func drainDialogAnswerEvents(events <-chan bus.Event) []bus.Event {
	var got []bus.Event
	for {
		select {
		case ev := <-events:
			if ev.Type == bus.EventDialogAnswer {
				got = append(got, ev)
			}
		default:
			return got
		}
	}
}

// TestHandleDialogAnswer_EmitsDialogAnswerOnSuccess закрывает T3: успешный
// человеческий ответ должен опубликовать РОВНО ОДНО событие dialog_answer
// живьём в UI-шину И продублировать ту же полезную нагрузку в notices.jsonl
// (тот же паттерн "одна карта, два получателя", что и у dialog_question из
// T2) — с {phase,id,title}, где title == mcp.DialogSnippet(answer).
func TestHandleDialogAnswer_EmitsDialogAnswerOnSuccess(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	questionPath := filepath.Join(stageDir, "planning."+testQuestionID+".question.json")
	if err := os.WriteFile(questionPath, []byte(`{"id":"q1","question":"?","allow_custom":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: testStageID, From: state.StatusPending, To: state.StatusAwaitingUserInput, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	uiBus := bus.NewUIBus()
	srv := New(Config{
		Port: 0, RunDir: runDir, Store: store, UIBus: uiBus,
		Actions: fakeStageActions{},
		Secondary: fakeSecondaryActions{notifyAnswer: func(string, string, string, string, bool) error {
			return nil
		}},
		ReviewState: func() (string, []string) { return "none", nil },
	})

	subID, events := uiBus.Subscribe(64)
	defer uiBus.Unsubscribe(subID)

	answer := "Let's go with option B\nsome extra detail on a second line"
	body, _ := json.Marshal(dialogAnswerRequest{ID: testQuestionID, Phase: "planning", Answer: answer})
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/dialog/answer", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleDialogAnswer(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	wantTitle := mcp.DialogSnippet(answer)

	dialogEvents := drainDialogAnswerEvents(events)
	if len(dialogEvents) != 1 {
		t.Fatalf("want exactly 1 EventDialogAnswer, got %d: %+v", len(dialogEvents), dialogEvents)
	}
	if dialogEvents[0].StageID != testStageID {
		t.Errorf("event StageID = %q, want %q", dialogEvents[0].StageID, testStageID)
	}
	data, ok := dialogEvents[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("event Data has unexpected type: %T", dialogEvents[0].Data)
	}
	if data["phase"] != "planning" || data["id"] != testQuestionID || data["title"] != wantTitle {
		t.Errorf("event data = %+v, want phase=planning id=%s title=%q", data, testQuestionID, wantTitle)
	}

	notices := readDialogAnswerNotices(t, runDir)
	if len(notices) != 1 {
		t.Fatalf("want exactly 1 dialog_answer line in notices.jsonl, got %d: %+v", len(notices), notices)
	}
	if notices[0].Phase != "planning" || notices[0].ID != testQuestionID || notices[0].Title != wantTitle {
		t.Errorf("notice data = %+v, want phase=planning id=%s title=%q", notices[0], testQuestionID, wantTitle)
	}
}

// TestHandleDialogAnswer_RejectedAnswerDoesNotEmitDialogAnswer покрывает
// негативный путь T3: если ответ отклоняется (здесь — flow держится на
// review-паузе, тот же 409-путь, что уже покрыт
// TestHandleDialogAnswer_RejectedWhileFlowPaused), dialog_answer НЕ должен
// публиковаться ни живьём, ни в notices.jsonl — иначе в ленте появится
// запись об ответе, которого на самом деле не произошло.
func TestHandleDialogAnswer_RejectedAnswerDoesNotEmitDialogAnswer(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	questionPath := filepath.Join(stageDir, "planning."+testQuestionID+".question.json")
	if err := os.WriteFile(questionPath, []byte(`{"id":"q1","question":"?","allow_custom":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: testStageID, From: state.StatusPending, To: state.StatusAwaitingUserInput, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	uiBus := bus.NewUIBus()
	notifyCalled := false
	srv := New(Config{
		Port: 0, RunDir: runDir, Store: store, UIBus: uiBus,
		Actions: fakeStageActions{},
		Secondary: fakeSecondaryActions{notifyAnswer: func(string, string, string, string, bool) error {
			notifyCalled = true
			return nil
		}},
		// Flow is held for review — the up-front guard rejects before any
		// durable mutation, exactly as TestHandleDialogAnswer_RejectedWhileFlowPaused
		// verifies.
		ReviewState: func() (string, []string) { return "paused", []string{"other"} },
	})

	subID, events := uiBus.Subscribe(64)
	defer uiBus.Unsubscribe(subID)

	body, _ := json.Marshal(dialogAnswerRequest{ID: testQuestionID, Phase: "planning", Answer: "yes"})
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/dialog/answer", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleDialogAnswer(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	if notifyCalled {
		t.Fatal("NotifyAnswer must not be reached when the up-front guard rejects")
	}
	if got := drainDialogAnswerEvents(events); len(got) != 0 {
		t.Fatalf("rejected answer must not publish EventDialogAnswer, got %+v", got)
	}
	if notices := readDialogAnswerNotices(t, runDir); len(notices) != 0 {
		t.Fatalf("rejected answer must not write a dialog_answer notice, got %+v", notices)
	}
}

// TestHandleDialogAnswer_TOCTOURollbackDoesNotEmitDialogAnswer — тот же
// негативный инвариант, но на TOCTOU-пути (NotifyAnswer возвращает
// ErrFlowPaused и answer.json откатывается): dialog_answer тоже не должен
// публиковаться.
func TestHandleDialogAnswer_TOCTOURollbackDoesNotEmitDialogAnswer(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	questionPath := filepath.Join(stageDir, "planning."+testQuestionID+".question.json")
	if err := os.WriteFile(questionPath, []byte(`{"id":"q1","question":"?","allow_custom":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: testStageID, From: state.StatusPending, To: state.StatusAwaitingUserInput, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	uiBus := bus.NewUIBus()
	srv := New(Config{
		Port: 0, RunDir: runDir, Store: store, UIBus: uiBus,
		Actions: fakeStageActions{},
		Secondary: fakeSecondaryActions{notifyAnswer: func(string, string, string, string, bool) error {
			return orchestrator.ErrFlowPaused
		}},
		ReviewState: func() (string, []string) { return "none", nil },
	})

	subID, events := uiBus.Subscribe(64)
	defer uiBus.Unsubscribe(subID)

	body, _ := json.Marshal(dialogAnswerRequest{ID: testQuestionID, Phase: "planning", Answer: "yes"})
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/dialog/answer", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleDialogAnswer(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	if got := drainDialogAnswerEvents(events); len(got) != 0 {
		t.Fatalf("TOCTOU-rejected answer must not publish EventDialogAnswer, got %+v", got)
	}
	if notices := readDialogAnswerNotices(t, runDir); len(notices) != 0 {
		t.Fatalf("TOCTOU-rejected answer must not write a dialog_answer notice, got %+v", notices)
	}
}
