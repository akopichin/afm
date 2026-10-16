package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

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
		ReviewState: func() (string, []string) { return "none", nil },
		Stages:      StaticStageConfigs{testStageID: {Interactive: true}},
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

func TestHandleDialogAnswer_RegressionBatchOfEight_NoDeadlock(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 8; i++ {
		id := "q" + strconv.Itoa(i)
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
		ReviewState: func() (string, []string) { return "none", nil },
		Stages:      StaticStageConfigs{testStageID: {Interactive: true}},
	})

	postAnswer := func(id string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(dialogAnswerRequest{ID: id, Phase: "autonomous_execution", Answer: "x"})
		req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/dialog/answer", bytes.NewReader(body))
		w := httptest.NewRecorder()
		srv.handleDialogAnswer(w, req)
		return w
	}

	rec := postAnswer("q8")
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp["error"] != "answer_out_of_order" {
		t.Fatalf("body = %s, want {\"error\":\"answer_out_of_order\"}", rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(stageDir, "autonomous_execution.q8.answer.json")); !os.IsNotExist(err) {
		t.Fatalf("q8.answer.json must NOT be written on rejection, stat err=%v", err)
	}

	rec = postAnswer("q1")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for the actual current question q1, got %d; body=%s", rec.Code, rec.Body.String())
	}
}
