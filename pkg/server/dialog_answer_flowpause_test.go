package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// TestHandleDialogAnswer_RejectedWhileFlowPaused закрывает review-finding P1 #4:
// пока flow держится на review-паузе, POST /dialog/answer обязан отклониться
// ДО любой durable-мутации — answer.json НЕ должен появиться на диске, а ответ
// должен быть машиночитаемым 409 {"error":"flow_paused"}. Иначе ответ пишется,
// вопрос перестаёт считаться unanswered, но resume-событие не публикуется, и
// не-owned стадия в awaiting_user_input зависает навсегда.
func TestHandleDialogAnswer_RejectedWhileFlowPaused(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	// A real question the agent wrote and is polling an answer for.
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

	notifyCalled := false
	srv := New(Config{
		Port: 0, RunDir: runDir, Store: store, UIBus: bus.NewUIBus(),
		Actions: fakeStageActions{},
		Secondary: fakeSecondaryActions{notifyAnswer: func(string, string, string, string, bool) error {
			notifyCalled = true
			return nil
		}},
		// Flow is held for review.
		ReviewState: func() (string, []string) { return "paused", []string{"other"} },
	})

	body, _ := json.Marshal(dialogAnswerRequest{ID: testQuestionID, Phase: "planning", Answer: "yes"})
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/dialog/answer", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleDialogAnswer(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp["error"] != "flow_paused" {
		t.Fatalf("body = %s, want {\"error\":\"flow_paused\"}", w.Body.String())
	}
	// The durable mutation must NOT have happened.
	answerPath := filepath.Join(stageDir, "planning."+testQuestionID+".answer.json")
	if _, err := os.Stat(answerPath); !os.IsNotExist(err) {
		t.Fatalf("answer.json must not be written while flow is paused, stat err=%v", err)
	}
	if notifyCalled {
		t.Fatal("NotifyAnswer must not be reached when the up-front guard rejects")
	}
}

// TestHandleDialogAnswer_TOCTOURollsBackBothDurableWrites закрывает re-review
// P1 (TOCTOU): если review pause стартует в окне МЕЖДУ up-front guard'ом и
// NotifyAnswer (эмулируется NotifyAnswer → ErrFlowPaused при reviewState
// "none"), handler обязан откатить answer.json И не оставить запись в
// dialog.jsonl. Иначе вопрос числится answered в истории, но unanswered на
// диске, и стадия зависает без формы. dialog.jsonl пишется ТОЛЬКО после успеха
// NotifyAnswer, поэтому при отказе его вообще не должно существовать.
func TestHandleDialogAnswer_TOCTOURollsBackBothDurableWrites(t *testing.T) {
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

	srv := New(Config{
		Port: 0, RunDir: runDir, Store: store, UIBus: bus.NewUIBus(),
		Actions: fakeStageActions{},
		// Guard passes (reviewState "none"), but a pause races in before notify.
		Secondary: fakeSecondaryActions{notifyAnswer: func(string, string, string, string, bool) error {
			return orchestrator.ErrFlowPaused
		}},
		ReviewState: func() (string, []string) { return "none", nil },
	})

	body, _ := json.Marshal(dialogAnswerRequest{ID: testQuestionID, Phase: "planning", Answer: "yes"})
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/dialog/answer", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleDialogAnswer(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp["error"] != "flow_paused" {
		t.Fatalf("body = %s, want flow_paused", w.Body.String())
	}
	// answer.json rolled back.
	if _, err := os.Stat(filepath.Join(stageDir, "planning."+testQuestionID+".answer.json")); !os.IsNotExist(err) {
		t.Fatalf("answer.json must be rolled back on the TOCTOU rejection, stat err=%v", err)
	}
	// dialog.jsonl must NOT exist — no durable history entry for a rejected answer.
	if _, err := os.Stat(filepath.Join(stageDir, "planning.dialog.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("dialog.jsonl must not be written for a rejected answer, stat err=%v", err)
	}
}

// TestHandleDialogAnswer_AllowedWhenNotPaused — контрольный кейс: без review-
// паузы тот же запрос проходит, answer.json пишется, NotifyAnswer вызывается.
func TestHandleDialogAnswer_AllowedWhenNotPaused(t *testing.T) {
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

	notifyCalled := false
	srv := New(Config{
		Port: 0, RunDir: runDir, Store: store, UIBus: bus.NewUIBus(),
		Actions: fakeStageActions{},
		Secondary: fakeSecondaryActions{notifyAnswer: func(string, string, string, string, bool) error {
			notifyCalled = true
			return nil
		}},
		ReviewState: func() (string, []string) { return "none", nil },
	})

	body, _ := json.Marshal(dialogAnswerRequest{ID: testQuestionID, Phase: "planning", Answer: "yes"})
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/dialog/answer", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleDialogAnswer(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	answerPath := filepath.Join(stageDir, "planning."+testQuestionID+".answer.json")
	if _, err := os.Stat(answerPath); err != nil {
		t.Fatalf("answer.json must be written when not paused: %v", err)
	}
	if !notifyCalled {
		t.Fatal("NotifyAnswer must be reached in the happy path")
	}
}

// TestHandleDialogAnswer_RejectsOutOfOrder закрывает iter3-M1: для интерактивной
// стадии POST /dialog/answer должен принимать ответ ТОЛЬКО на текущий
// (старейший unanswered) вопрос — ответ на более позднюю q2, пока q1 ещё не
// отвечен, должен быть отклонён 409 без записи answer.json на диск, иначе
// polling-цикл агента, который ждёт ответ именно на q1, зависает навсегда.
func TestHandleDialogAnswer_RejectsOutOfOrder(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"q1", "q2"} {
		body := `{"id":"` + id + `","question":"Q","options":["A"],"allow_custom":true}`
		if err := os.WriteFile(filepath.Join(stageDir, "autonomous_execution."+id+".question.json"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: testStageID, From: state.StatusPending, To: state.StatusAwaitingUserInput, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{
		Port: 0, RunDir: runDir, Store: store, UIBus: bus.NewUIBus(),
		Actions: fakeStageActions{},
		Secondary: fakeSecondaryActions{notifyAnswer: func(string, string, string, string, bool) error {
			return nil
		}},
		ReviewState:      func() (string, []string) { return "none", nil },
		StageInteractive: map[string]bool{testStageID: true},
	})

	postAnswer := func(id string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(dialogAnswerRequest{ID: id, Phase: "autonomous_execution", Answer: "x"})
		req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/dialog/answer", bytes.NewReader(body))
		w := httptest.NewRecorder()
		srv.handleDialogAnswer(w, req)
		return w
	}

	rec := postAnswer("q2")
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp["error"] != "answer_out_of_order" {
		t.Fatalf("body = %s, want {\"error\":\"answer_out_of_order\"}", rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(stageDir, "autonomous_execution.q2.answer.json")); !os.IsNotExist(err) {
		t.Fatalf("q2.answer.json must NOT be written on rejection, stat err=%v", err)
	}

	rec = postAnswer("q1")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for current question, got %d; body=%s", rec.Code, rec.Body.String())
	}
}
