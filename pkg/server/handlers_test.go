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
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__flowmanager__ask_user","input":{"id":"q1","question":"Дизайн ок?"}}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Финальный штрих."}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__flowmanager__ask_user","input":{"id":"q2","question":"Утверждаем?"}}]}}
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
