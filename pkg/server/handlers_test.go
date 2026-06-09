package server

import (
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

	rs := state.NewRunState([]string{testStageID})
	rs.SetStageStatus(testStageID, state.StatusAwaitingApproval)
	stateFile := filepath.Join(runDir, "state.json")
	if err := rs.Save(stateFile); err != nil {
		t.Fatalf("save state: %v", err)
	}

	bus := orchestrator.NewEventBus()
	srv := New(Config{
		Port:      0,
		RunDir:    runDir,
		StateFile: stateFile,
		Bus:       bus,
		ApproveFn: func(id string) {},
		ReviseFn:  func(id, fb string) {},
		RetryFn:   func(id string) {},
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
	srv.approveFn = func(id string) { approved = id }

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
	srv.reviseFn = func(id, fb string) { revisedID = id; revisedFB = fb }

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
	rs, _ := state.Load(srv.stateFile)
	rs.SetStageStatus(testStageID, state.StatusFailed)
	if err := rs.Save(srv.stateFile); err != nil {
		t.Fatal(err)
	}
	srv.retryFn = func(id string) { retriedID = id }

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
	srv.retryFn = func(id string) {}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/retry", nil)
	w := httptest.NewRecorder()
	srv.handleRetry(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-failed stage, got %d", w.Code)
	}
}

func TestDialogGet(t *testing.T) {
	runDir := t.TempDir()
	stateFile := filepath.Join(runDir, "state.json")
	stageDir := filepath.Join(runDir, testStageID)
	os.MkdirAll(stageDir, 0755)
	dialogPath := filepath.Join(stageDir, "implementation.dialog.jsonl")
	if err := mcp.AppendQuestion(dialogPath, mcp.Question{ID: testQuestionID, Question: "x?"}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{ID: testQuestionID, Answer: "yes"}); err != nil {
		t.Fatal(err)
	}

	rs := state.NewRunState([]string{testStageID})
	if err := rs.Save(stateFile); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{
		RunDir:    runDir,
		StateFile: stateFile,
		Bus:       orchestrator.NewEventBus(),
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

func TestDialogAnswer(t *testing.T) {
	runDir := t.TempDir()
	stateFile := filepath.Join(runDir, "state.json")
	stageDir := filepath.Join(runDir, testStageID)
	os.MkdirAll(stageDir, 0755)

	dialogPath := filepath.Join(stageDir, "implementation.dialog.jsonl")
	if err := mcp.AppendQuestion(dialogPath, mcp.Question{ID: testQuestionID, Question: "test?"}); err != nil {
		t.Fatal(err)
	}

	rs := state.NewRunState([]string{testStageID})
	if err := rs.Save(stateFile); err != nil {
		t.Fatal(err)
	}

	called := struct {
		stage, phase, id, answer string
		fromOptions              bool
	}{}
	srv := New(Config{
		RunDir:    runDir,
		StateFile: stateFile,
		Bus:       orchestrator.NewEventBus(),
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
	stateFile := filepath.Join(runDir, "state.json")
	rs := state.NewRunState([]string{testStageID})
	rs.SetStageStatus(testStageID, state.StatusRunning)
	if err := rs.Save(stateFile); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{
		RunDir:    runDir,
		StateFile: stateFile,
		Bus:       orchestrator.NewEventBus(),
	})

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/dialog/cancel", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for non-awaiting stage, got %d", w.Code)
	}
}
