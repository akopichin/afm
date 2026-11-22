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

	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

const (
	strictQuestion = `{"id":"q1","question":"?","options":["A","B"],"allow_custom":false}`
	feedbackText   = "> first quoted line\nLine 1: change this\n\n> second\nLine 2: and this"
)

type feedbackHarness struct {
	srv         *Server
	stageDir    string
	notifyCalls []string
}

// newFeedbackHarness пишет вопрос q1 в planning и поднимает сервер; notifyErr задаёт ответ NotifyAnswer.
func newFeedbackHarness(t *testing.T, question string, reviewState string, stages RuntimeStageProvider, notifyErr error) *feedbackHarness {
	t.Helper()
	runDir := t.TempDir()
	h := &feedbackHarness{stageDir: filepath.Join(runDir, testStageID)}
	if err := os.MkdirAll(h.stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if question != "" {
		if err := os.WriteFile(filepath.Join(h.stageDir, "planning.q1.question.json"), []byte(question), 0644); err != nil {
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
	h.srv = New(Config{
		Port: 0, RunDir: runDir, Store: store, UIBus: bus.NewUIBus(),
		Actions: fakeStageActions{},
		Secondary: fakeSecondaryActions{notifyAnswer: func(_, _, _, answer string, fromOptions bool) error {
			h.notifyCalls = append(h.notifyCalls, answer+"|"+boolStr(fromOptions))
			return notifyErr
		}},
		ReviewState: func() (string, []string) { return reviewState, nil },
		Stages:      stages,
	})
	return h
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// post шлёт сырое тело, чтобы тесты проверяли wire-контракт, а не Go-структуру.
func (h *feedbackHarness) post(t *testing.T, id string, fields map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"id": id, "phase": "planning"}
	for k, v := range fields {
		body[k] = v
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/dialog/answer", bytes.NewReader(raw))
	w := httptest.NewRecorder()
	h.srv.handleDialogAnswer(w, req)
	return w
}

func (h *feedbackHarness) answerExists(id string) bool {
	_, err := os.Stat(filepath.Join(h.stageDir, "planning."+id+".answer.json"))
	return err == nil
}

func requireStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, want, w.Body.String())
	}
}

func TestHandleDialogAnswer_FeedbackAcceptedOnStrictQuestion(t *testing.T) {
	h := newFeedbackHarness(t, strictQuestion, "none", nil, nil)
	subID, events := h.srv.uiBus.Subscribe(64)
	defer h.srv.uiBus.Unsubscribe(subID)

	w := h.post(t, "q1", map[string]any{"answer": feedbackText, "from_options": false, "feedback": true})
	requireStatus(t, w, http.StatusOK)

	raw, err := os.ReadFile(filepath.Join(h.stageDir, "planning.q1.answer.json"))
	if err != nil {
		t.Fatalf("answer.json must be written: %v", err)
	}
	var saved struct {
		Answer      string `json:"answer"`
		FromOptions bool   `json:"from_options"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Answer != feedbackText || saved.FromOptions {
		t.Fatalf("answer.json = %+v, want exact feedback text and from_options=false", saved)
	}
	if len(h.notifyCalls) != 1 || h.notifyCalls[0] != feedbackText+"|false" {
		t.Fatalf("NotifyAnswer calls = %q, want exactly one with the feedback text and from_options=false", h.notifyCalls)
	}
	history, err := os.ReadFile(filepath.Join(h.stageDir, "planning.dialog.jsonl"))
	if err != nil {
		t.Fatalf("dialog history must be written: %v", err)
	}
	encoded, _ := json.Marshal(feedbackText)
	if !strings.Contains(string(history), strings.Trim(string(encoded), `"`)) {
		t.Fatalf("history lacks the exact feedback text: %s", history)
	}
	if got := drainDialogAnswerEvents(events); len(got) != 1 {
		t.Fatalf("live dialog_answer events = %d, want 1", len(got))
	}
	if got := readDialogAnswerNotices(t, h.srv.runDir); len(got) != 1 {
		t.Fatalf("dialog_answer notices = %d, want 1", len(got))
	}

	w = h.post(t, "q1", map[string]any{"answer": feedbackText, "feedback": true})
	requireStatus(t, w, http.StatusConflict)
	if len(h.notifyCalls) != 1 {
		t.Fatalf("NotifyAnswer calls after repeat = %d, want still 1", len(h.notifyCalls))
	}
}

func TestHandleDialogAnswer_StrictQuestionRulesUnchangedWithoutFeedback(t *testing.T) {
	h := newFeedbackHarness(t, strictQuestion, "none", nil, nil)

	w := h.post(t, "q1", map[string]any{"answer": "free text", "from_options": false})
	requireStatus(t, w, http.StatusBadRequest)
	if !strings.Contains(w.Body.String(), "answer not in allowed options") {
		t.Fatalf("body = %q, want 'answer not in allowed options'", w.Body.String())
	}
	if h.answerExists("q1") {
		t.Fatal("answer.json must not be written for a rejected free-text answer")
	}

	requireStatus(t, h.post(t, "q1", map[string]any{"answer": "A", "from_options": true}), http.StatusOK)
}

func TestHandleDialogAnswer_FeedbackRejectedWithoutMutation(t *testing.T) {
	cases := map[string]map[string]any{
		"from_options":    {"answer": "A", "from_options": true, "feedback": true},
		"whitespace-only": {"answer": " \n\t ", "feedback": true},
	}
	for name, fields := range cases {
		t.Run(name, func(t *testing.T) {
			h := newFeedbackHarness(t, strictQuestion, "none", nil, nil)
			requireStatus(t, h.post(t, "q1", fields), http.StatusBadRequest)
			if h.answerExists("q1") {
				t.Fatal("answer.json must not be written")
			}
			if len(h.notifyCalls) != 0 {
				t.Fatalf("NotifyAnswer must not be called, got %q", h.notifyCalls)
			}
		})
	}
}

func TestHandleDialogAnswer_FromOptionsFalseDoesNotImplyFeedback(t *testing.T) {
	h := newFeedbackHarness(t, strictQuestion, "none", nil, nil)
	requireStatus(t, h.post(t, "q1", map[string]any{"answer": feedbackText, "from_options": false}), http.StatusBadRequest)
	if h.answerExists("q1") {
		t.Fatal("answer.json must not be written without the feedback flag")
	}
}

func TestHandleDialogAnswer_FeedbackHonoursFlowPause(t *testing.T) {
	t.Run("guard before mutation", func(t *testing.T) {
		h := newFeedbackHarness(t, strictQuestion, "paused", nil, nil)
		w := h.post(t, "q1", map[string]any{"answer": feedbackText, "feedback": true})
		requireStatus(t, w, http.StatusConflict)
		if !strings.Contains(w.Body.String(), "flow_paused") {
			t.Fatalf("body = %s, want flow_paused", w.Body.String())
		}
		if h.answerExists("q1") || len(h.notifyCalls) != 0 {
			t.Fatal("paused flow must not mutate or notify")
		}
	})
	t.Run("notify race rolls back", func(t *testing.T) {
		h := newFeedbackHarness(t, strictQuestion, "none", nil, orchestrator.ErrFlowPaused)
		requireStatus(t, h.post(t, "q1", map[string]any{"answer": feedbackText, "feedback": true}), http.StatusConflict)
		if h.answerExists("q1") {
			t.Fatal("answer.json must be rolled back")
		}
		if _, err := os.Stat(filepath.Join(h.stageDir, "planning.dialog.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("dialog history must not be written, stat err=%v", err)
		}
	})
}

func TestHandleDialogAnswer_FeedbackOutOfOrder(t *testing.T) {
	h := newFeedbackHarness(t, strictQuestion, "none", StaticStageConfigs{testStageID: {Interactive: true}}, nil)
	if err := os.WriteFile(filepath.Join(h.stageDir, "planning.q2.question.json"), []byte(strictQuestion), 0644); err != nil {
		t.Fatal(err)
	}
	w := h.post(t, "q2", map[string]any{"answer": feedbackText, "feedback": true})
	requireStatus(t, w, http.StatusConflict)
	if !strings.Contains(w.Body.String(), "answer_out_of_order") {
		t.Fatalf("body = %s, want answer_out_of_order", w.Body.String())
	}
	if h.answerExists("q2") {
		t.Fatal("q2.answer.json must not be written")
	}
}

func TestHandleDialogAnswer_FeedbackMissingQuestion(t *testing.T) {
	h := newFeedbackHarness(t, "", "none", nil, nil)
	requireStatus(t, h.post(t, "q1", map[string]any{"answer": feedbackText, "feedback": true}), http.StatusNotFound)
	if h.answerExists("q1") {
		t.Fatal("answer.json must not be written")
	}
}

func TestHandleDialogAnswer_FeedbackOnStrictQuestionWithoutOptions(t *testing.T) {
	h := newFeedbackHarness(t, `{"id":"q1","question":"?","allow_custom":false}`, "none", nil, nil)

	w := h.post(t, "q1", map[string]any{"answer": "plain"})
	requireStatus(t, w, http.StatusBadRequest)
	if !strings.Contains(w.Body.String(), "question has no options") {
		t.Fatalf("body = %q, want 'question has no options…'", w.Body.String())
	}

	requireStatus(t, h.post(t, "q1", map[string]any{"answer": feedbackText, "feedback": true}), http.StatusOK)
}
