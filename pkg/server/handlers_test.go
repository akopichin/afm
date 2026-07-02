package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

const testStageID = "s1"
const testQuestionID = "q1"

func setupTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Test Plan"), 0644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "planning.log"), []byte("test log line\n"), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(state.Transition{StageID: testStageID, From: state.StatusPending, To: state.StatusAwaitingApproval, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	bus := orchestrator.NewUIBus()
	srv := New(Config{
		Port:      0,
		RunDir:    runDir,
		Store:     store,
		UIBus:     bus,
		ApproveFn: func(ctx context.Context, id string) error { return nil },
		ReviseFn:  func(ctx context.Context, id, fb string) error { return nil },
		RetryFn:   func(ctx context.Context, id string) error { return nil },
	})
	return srv, runDir
}

func TestHandleStatus(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	var rs state.RunState
	if err := json.NewDecoder(w.Body).Decode(&rs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := rs.Stages[testStageID]; !ok {
		t.Error("stage s1 missing from status")
	}
}

func TestHandleStatus_IncludesStageNames(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.store.SetStageNames(map[string]string{testStageID: "Backend Stage"})

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var rs state.RunState
	if err := json.NewDecoder(w.Body).Decode(&rs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := rs.StageNames[testStageID]; got != "Backend Stage" {
		t.Errorf("stage_names[%q] = %q, want %q", testStageID, got, "Backend Stage")
	}
}

func TestHandlePlan(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/stages/"+testStageID+"/plan", nil)
	w := httptest.NewRecorder()
	srv.handlePlan(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "# Test Plan") {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

func TestHandleLog(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/stages/"+testStageID+"/log", nil)
	w := httptest.NewRecorder()
	srv.handleLog(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "test log line") {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

func TestHandleApprove(t *testing.T) {
	approved := ""
	srv, _ := setupTestServer(t)
	srv.approveFn = func(ctx context.Context, id string) error { approved = id; return nil }

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/approve", nil)
	w := httptest.NewRecorder()
	srv.handleApprove(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if approved != testStageID {
		t.Errorf("approve not called with s1, got %q", approved)
	}
}

func TestHandleRevise(t *testing.T) {
	var revisedID, revisedFB string
	srv, _ := setupTestServer(t)
	srv.reviseFn = func(ctx context.Context, id, fb string) error { revisedID = id; revisedFB = fb; return nil }

	body := `{"feedback":"Добавь Redis"}`
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/revise", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleRevise(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if revisedID != testStageID || revisedFB != "Добавь Redis" {
		t.Errorf("revise: id=%q fb=%q", revisedID, revisedFB)
	}
}

func TestHandleRetry(t *testing.T) {
	var retriedID string
	srv, _ := setupTestServer(t)
	if err := srv.store.Apply(state.Transition{StageID: testStageID, From: state.StatusAwaitingApproval, To: state.StatusFailed, Event: "test_fail"}); err != nil {
		t.Fatal(err)
	}
	srv.retryFn = func(ctx context.Context, id string) error { retriedID = id; return nil }

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/retry", nil)
	w := httptest.NewRecorder()
	srv.handleRetry(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if retriedID != testStageID {
		t.Errorf("retry not called with s1, got %q", retriedID)
	}
}

func TestHandleRetryNotFailed(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.retryFn = func(ctx context.Context, id string) error { return nil }

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/retry", nil)
	w := httptest.NewRecorder()
	srv.handleRetry(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-failed stage, got %d", w.Code)
	}
}

func TestDialogGet(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	os.MkdirAll(stageDir, 0755)
	dialogPath := filepath.Join(stageDir, "implementation.dialog.jsonl")
	if err := mcp.AppendQuestion(dialogPath, mcp.Question{ID: testQuestionID, Question: "x?"}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{ID: testQuestionID, Answer: "yes"}); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	srv := New(Config{
		RunDir: runDir,
		Store:  store,
		UIBus:  orchestrator.NewUIBus(),
	})

	req := httptest.NewRequest("GET", "/api/stages/"+testStageID+"/dialog", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status %d, body %s", w.Code, w.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 || got[0]["id"] != testQuestionID || got[0]["phase"] != "implementation" {
		t.Errorf("dialog entries wrong: %+v", got)
	}
}

// TestDialogGetWithTranscript проверяет, что тексты агента из stream-json
// лога перемежают вопросы диалога в порядке появления.
func TestDialogGetWithTranscript(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	os.MkdirAll(stageDir, 0755)

	dialogPath := filepath.Join(stageDir, "planning.dialog.jsonl")
	if err := mcp.AppendQuestion(dialogPath, mcp.Question{ID: "q1", Question: "Дизайн ок?"}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{ID: "q1", Answer: "да"}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendQuestion(dialogPath, mcp.Question{ID: "q2", Question: "Утверждаем?"}); err != nil {
		t.Fatal(err)
	}

	jsonl := `{"type":"assistant","message":{"content":[{"type":"text","text":"## Дизайн расширения"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__afm__ask_user","input":{"id":"q1","question":"Дизайн ок?"}}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Финальный штрих."}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__afm__ask_user","input":{"id":"q2","question":"Утверждаем?"}}]}}
`
	if err := os.WriteFile(filepath.Join(stageDir, "planning.jsonl"), []byte(jsonl), 0644); err != nil {
		t.Fatal(err)
	}

	got := buildDialogEntries(stageDir)
	if len(got) != 4 {
		t.Fatalf("got %d entries, want 4: %+v", len(got), got)
	}
	if got[0].Type != typeAgentText || got[0].Text != "## Дизайн расширения" {
		t.Errorf("entry 0: %+v", got[0])
	}
	if got[1].ID != "q1" || got[1].Answer == nil || *got[1].Answer != "да" {
		t.Errorf("entry 1: %+v", got[1])
	}
	if got[2].Type != typeAgentText || got[2].Text != "Финальный штрих." {
		t.Errorf("entry 2: %+v", got[2])
	}
	if got[3].ID != "q2" || got[3].Answer != nil {
		t.Errorf("entry 3: %+v", got[3])
	}
}

// TestDialogGetNoDialogFile проверяет, что без диалога тексты агента
// не показываются (неинтерактивные стейджи не раздувают панель).
func TestDialogGetNoDialogFile(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	os.MkdirAll(stageDir, 0755)
	jsonl := `{"type":"assistant","message":{"content":[{"type":"text","text":"просто текст"}]}}
`
	if err := os.WriteFile(filepath.Join(stageDir, "planning.jsonl"), []byte(jsonl), 0644); err != nil {
		t.Fatal(err)
	}
	if got := buildDialogEntries(stageDir); len(got) != 0 {
		t.Errorf("expected no entries, got %+v", got)
	}
}

func TestDialogAnswer(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	os.MkdirAll(stageDir, 0755)

	dialogPath := filepath.Join(stageDir, "implementation.dialog.jsonl")
	if err := mcp.AppendQuestion(dialogPath, mcp.Question{ID: testQuestionID, Question: "test?"}); err != nil {
		t.Fatal(err)
	}
	// question.json written by the agent (file-based dialog protocol).
	qPath := filepath.Join(stageDir, "implementation."+testQuestionID+".question.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"test?"}`), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	called := struct {
		stage, phase, id, answer string
		fromOptions              bool
	}{}
	srv := New(Config{
		RunDir: runDir,
		Store:  store,
		UIBus:  orchestrator.NewUIBus(),
		DialogAnswerFn: func(s, p, q, a string, fo bool) error {
			called.stage, called.phase, called.id, called.answer, called.fromOptions = s, p, q, a, fo
			return nil
		},
	})

	body := `{"id":"` + testQuestionID + `","phase":"implementation","answer":"hello","from_options":false}`
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/dialog/answer", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d, body %s", w.Code, w.Body.String())
	}
	if called.stage != testStageID || called.id != testQuestionID || called.answer != "hello" {
		t.Errorf("answerFn called with wrong args: %+v", called)
	}

	// The answer file must be written so the agent's bash loop can pick it up.
	// Without this assertion the write could be silently dropped (regression).
	answerPath := filepath.Join(stageDir, "implementation."+testQuestionID+".answer.json")
	data, err := os.ReadFile(answerPath)
	if err != nil {
		t.Fatalf("answer.json not written: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("invalid answer.json: %v", err)
	}
	if got["answer"] != "hello" {
		t.Errorf("answer.json content mismatch: %v", got)
	}
}

// TestHandleDialogAnswer_InvalidID verifies that a question id containing path
// separators / traversal sequences is rejected before it is embedded in a
// filename. Guards against path traversal via a crafted id.
func TestHandleDialogAnswer_InvalidID(t *testing.T) {
	srv, runDir := setupTestServer(t)
	stageDir := filepath.Join(runDir, testStageID)
	// A benign question file exists, but the attacker POSTs a traversal id.
	if err := os.WriteFile(filepath.Join(stageDir, "planning.q1.question.json"),
		[]byte(`{"id":"q1","question":"x?"}`), 0644); err != nil {
		t.Fatal(err)
	}

	for _, badID := range []string{"../../foo", "a/b", "..", ""} {
		body := `{"id":"` + badID + `","phase":"planning","answer":"yes"}`
		req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/dialog/answer",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("id=%q: want 400, got %d: %s", badID, w.Code, w.Body.String())
		}
	}
}

// TestDialogCancelAwaiting verifies the success path: a stage in
// awaiting_user_input is cancelled and dialogCancelFn is invoked.
func TestDialogCancelAwaiting(t *testing.T) {
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(state.Transition{StageID: testStageID, From: state.StatusPending, To: state.StatusAwaitingUserInput, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	cancelled := ""
	srv := New(Config{
		RunDir:         runDir,
		Store:          store,
		UIBus:          orchestrator.NewUIBus(),
		DialogCancelFn: func(id string) error { cancelled = id; return nil },
	})

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/dialog/cancel", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if cancelled != testStageID {
		t.Errorf("dialogCancelFn not called with %q, got %q", testStageID, cancelled)
	}
}

func TestDialogCancelRejectsNonAwaiting(t *testing.T) {
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(state.Transition{StageID: testStageID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{
		RunDir: runDir,
		Store:  store,
		UIBus:  orchestrator.NewUIBus(),
	})

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/dialog/cancel", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for non-awaiting stage, got %d", w.Code)
	}
}

func TestHandleDialogAnswer_WritesAnswerFile(t *testing.T) {
	srv, runDir := setupTestServer(t)
	stageDir := filepath.Join(runDir, testStageID)

	// Write a question file (agent-side).
	qPath := filepath.Join(stageDir, "planning."+testQuestionID+".question.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"proceed?","options":["yes"],"allow_custom":false}`), 0644); err != nil {
		t.Fatal(err)
	}
	// Pre-populate dialog.jsonl so AppendAnswer has a matching question.
	dialogPath := filepath.Join(stageDir, "planning.dialog.jsonl")
	if err := mcp.AppendQuestion(dialogPath, mcp.Question{ID: testQuestionID, Question: "proceed?"}); err != nil {
		t.Fatal(err)
	}

	body := `{"id":"q1","phase":"planning","answer":"yes","from_options":true}`
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/dialog/answer",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	// answer.json must exist with correct content.
	answerPath := filepath.Join(stageDir, "planning."+testQuestionID+".answer.json")
	data, err := os.ReadFile(answerPath)
	if err != nil {
		t.Fatalf("answer.json not created: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("invalid JSON in answer.json: %v", err)
	}
	if got["answer"] != "yes" {
		t.Errorf("answer.json answer mismatch: %v", got)
	}
	if v, _ := got["from_options"].(bool); !v {
		t.Errorf("answer.json from_options mismatch: %v", got)
	}
}

// TestHandleDialogAnswer_AppendAnswerFailureStillNotifies verifies that when
// the dialog.jsonl append fails (here: the path is a directory), the handler
// still writes answer.json and calls dialogAnswerFn (the critical notify),
// returning 200. Failing the request here would leave the stage stuck in
// awaiting_user_input with the answer already delivered.
func TestHandleDialogAnswer_AppendAnswerFailureStillNotifies(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Agent-side question file exists.
	qPath := filepath.Join(stageDir, "planning."+testQuestionID+".question.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"proceed?"}`), 0644); err != nil {
		t.Fatal(err)
	}
	// Make AppendAnswer fail: planning.dialog.jsonl is a directory, so opening
	// it for writing returns an error.
	if err := os.MkdirAll(filepath.Join(stageDir, "planning.dialog.jsonl"), 0755); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	notifyCalled := false
	srv := New(Config{
		RunDir: runDir,
		Store:  store,
		UIBus:  orchestrator.NewUIBus(),
		DialogAnswerFn: func(s, p, q, a string, fo bool) error {
			notifyCalled = true
			return nil
		},
	})

	body := `{"id":"q1","phase":"planning","answer":"yes","from_options":false}`
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/dialog/answer",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("AppendAnswer failure must not fail the request: want 200, got %d: %s", w.Code, w.Body.String())
	}
	if !notifyCalled {
		t.Error("dialogAnswerFn (notify) was not called after AppendAnswer failure")
	}
	answerPath := filepath.Join(stageDir, "planning."+testQuestionID+".answer.json")
	if _, err := os.Stat(answerPath); err != nil {
		t.Errorf("answer.json not written despite notify: %v", err)
	}
}

func TestHandleDialogAnswer_QuestionNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	body := `{"id":"nonexistent","phase":"planning","answer":"yes"}`
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/dialog/answer",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", w.Code)
	}
}

func TestHandleDialogAnswer_DuplicateAnswer(t *testing.T) {
	srv, runDir := setupTestServer(t)
	stageDir := filepath.Join(runDir, testStageID)

	// Create question.json + answer.json (already answered).
	qPath := filepath.Join(stageDir, "planning.q1.question.json")
	aPath := filepath.Join(stageDir, "planning.q1.answer.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"x?"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aPath, []byte(`{"id":"q1","answer":"yes"}`), 0644); err != nil {
		t.Fatal(err)
	}

	body := `{"id":"q1","phase":"planning","answer":"no"}`
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/dialog/answer",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("want 409, got %d", w.Code)
	}
}
