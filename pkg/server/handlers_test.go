package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

const testStageID = "s1"
const testQuestionID = "q1"

func setupTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	return setupTestServerWithWS(t, 0, 0)
}

// setupTestServerWithWS — как setupTestServer, но с явными keepalive-таймаутами
// вебсокета (нужны websocket-тестам; 0 → дефолты из websocket.go).
func setupTestServerWithWS(t *testing.T, pongWait, pingPeriod time.Duration) (*Server, string) {
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
	if err := store.Apply(&state.Transition{StageID: testStageID, From: state.StatusPending, To: state.StatusAwaitingApproval, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	uiBus := bus.NewUIBus()
	srv := New(Config{
		Port:         0,
		RunDir:       runDir,
		Store:        store,
		UIBus:        uiBus,
		ApproveFn:    func(ctx context.Context, id string) error { return nil },
		ReviseFn:     func(ctx context.Context, id, fb string) error { return nil },
		RetryFn:      func(ctx context.Context, id string) error { return nil },
		WSPongWait:   pongWait,
		WSPingPeriod: pingPeriod,
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

func TestHandleStatus_IncludesInteractiveAndAutonomous(t *testing.T) {
	srv, runDir := setupTestServer(t)
	srv.stageInteractive = map[string]bool{testStageID: true}
	// пометить стадию автономной
	if err := os.WriteFile(filepath.Join(runDir, testStageID, "autonomous.flag"), nil, 0644); err != nil {
		t.Fatalf("write flag: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var resp statusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp.Stages[testStageID]; !ok {
		t.Error("embedded RunState: stage missing from status")
	}
	if !resp.StageInteractive[testStageID] {
		t.Errorf("stage_interactive[%q] = false, want true", testStageID)
	}
	if !resp.StageAutonomous[testStageID] {
		t.Errorf("stage_autonomous[%q] = false, want true", testStageID)
	}
}

func TestHandleStatus_IncludesAutoApprove(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.stageAutoApprove = map[string]bool{testStageID: true}

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var resp statusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.StageAutoApprove[testStageID] {
		t.Errorf("stage_auto_approve[%q] = false, want true", testStageID)
	}
}

func TestHandleStatus_IncludesDescription(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.Description = "Мой флоу для тестов"

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var resp statusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Description != "Мой флоу для тестов" {
		t.Errorf("description = %q, want %q", resp.Description, "Мой флоу для тестов")
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

// TestHandleLog_IncludesFeedbackVariantPhases guards the actual gap the
// flow.Phases()+PhaseLogFiles refactor closed: the OLD hardcoded list in
// handleLog was {"planning.log", "planning-revision.log",
// "implementation.log", "review.log", "autonomous.log"} — it already
// included bare "review.log"/"autonomous.log" literally, so a test only
// checking those would pass against the old buggy code too (false
// positive). What the old list never had, at all, were the
// feedback/reprompt-variant filenames: "implementation-feedback.log" and
// "review-feedback.log" (also "planning-reprompt.log",
// "autonomous-feedback.log" — see flow.PhaseLogFiles). This test writes
// into two of those previously-invisible files and asserts their content
// reaches the /log response, which only flow.PhaseLogFiles-driven
// iteration can satisfy.
func TestHandleLog_IncludesFeedbackVariantPhases(t *testing.T) {
	srv, runDir := setupTestServer(t)
	stageDir := filepath.Join(runDir, testStageID)
	if err := os.WriteFile(filepath.Join(stageDir, "implementation-feedback.log"), []byte("implementation feedback output"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "review-feedback.log"), []byte("review feedback output"), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/stages/"+testStageID+"/log", nil)
	w := httptest.NewRecorder()
	srv.handleLog(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "implementation feedback output") {
		t.Errorf("expected implementation-feedback.log content in response, got: %s", body)
	}
	if !strings.Contains(body, "review feedback output") {
		t.Errorf("expected review-feedback.log content in response, got: %s", body)
	}
}

// TestHandleLog_ConcatenatesHookLogs проверяет, что /log отдаёт логи
// script_before/script_after хуков вместе с основным логом стадии, в порядке
// before → main → after (before.log пишется до planning.log, after.log —
// после; порядок проверяется по позиции содержимого в теле ответа).
func TestHandleLog_ConcatenatesHookLogs(t *testing.T) {
	srv, runDir := setupTestServer(t)
	stageDir := filepath.Join(runDir, testStageID)
	if err := os.WriteFile(filepath.Join(stageDir, "before.log"), []byte("BEFORE-CONTENT\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "script.log"), []byte("SCRIPT-CONTENT\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "after.log"), []byte("AFTER-CONTENT\n"), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/stages/"+testStageID+"/log", nil)
	w := httptest.NewRecorder()
	srv.routeStages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	beforeIdx := strings.Index(body, "BEFORE-CONTENT")
	scriptIdx := strings.Index(body, "SCRIPT-CONTENT")
	afterIdx := strings.Index(body, "AFTER-CONTENT")
	if beforeIdx == -1 || scriptIdx == -1 || afterIdx == -1 {
		t.Fatalf("log body missing hook content: %q", body)
	}
	if beforeIdx >= scriptIdx || scriptIdx >= afterIdx {
		t.Errorf("expected order before < script < after, got positions %d, %d, %d", beforeIdx, scriptIdx, afterIdx)
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
	if err := srv.store.Apply(&state.Transition{StageID: testStageID, From: state.StatusAwaitingApproval, To: state.StatusFailed, Event: "test_fail"}); err != nil {
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

func TestHandleRetryHook_Success(t *testing.T) {
	srv, _ := setupTestServer(t)
	called := ""
	srv.retryHookFn = func(stageID string) error {
		called = stageID
		return nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/retry-hook", nil)
	w := httptest.NewRecorder()
	srv.routeStages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if called != testStageID {
		t.Errorf("retryHookFn called with %q, want %q", called, testStageID)
	}
}

func TestHandleSkipHook_Success(t *testing.T) {
	srv, _ := setupTestServer(t)
	called := ""
	srv.skipHookFn = func(stageID string) error {
		called = stageID
		return nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/skip-hook", nil)
	w := httptest.NewRecorder()
	srv.routeStages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if called != testStageID {
		t.Errorf("skipHookFn called with %q, want %q", called, testStageID)
	}
}

func TestHandleRetryHook_FnReturnsError(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.retryHookFn = func(stageID string) error {
		return fmt.Errorf("stage %q has no hook awaiting a decision", stageID)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/retry-hook", nil)
	w := httptest.NewRecorder()
	srv.routeStages(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", w.Code, w.Body.String())
	}
}

func TestHandleSkipHook_FnReturnsError(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.skipHookFn = func(stageID string) error {
		return fmt.Errorf("stage %q has no hook awaiting a decision", stageID)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/skip-hook", nil)
	w := httptest.NewRecorder()
	srv.routeStages(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", w.Code, w.Body.String())
	}
}

func TestHandleRetryHook_NotConfigured(t *testing.T) {
	srv, _ := setupTestServer(t)
	// retryHookFn intentionally left nil.

	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/retry-hook", nil)
	w := httptest.NewRecorder()
	srv.routeStages(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501, body = %s", w.Code, w.Body.String())
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
		UIBus:  bus.NewUIBus(),
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
		UIBus:  bus.NewUIBus(),
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
	if err := store.Apply(&state.Transition{StageID: testStageID, From: state.StatusPending, To: state.StatusAwaitingUserInput, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	cancelled := ""
	srv := New(Config{
		RunDir:         runDir,
		Store:          store,
		UIBus:          bus.NewUIBus(),
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
	if err := store.Apply(&state.Transition{StageID: testStageID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{
		RunDir: runDir,
		Store:  store,
		UIBus:  bus.NewUIBus(),
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
		UIBus:  bus.NewUIBus(),
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

func TestHandleRevise_RunningAllowed(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: testStageID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	var reviseCalled bool
	srv := New(Config{
		RunDir: runDir,
		Store:  store,
		UIBus:  bus.NewUIBus(),
		ReviseFn: func(_ context.Context, _, _ string) error {
			reviseCalled = true
			return nil
		},
	})

	body, _ := json.Marshal(map[string]string{"feedback": "note"})
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/revise", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleRevise(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !reviseCalled {
		t.Error("reviseFn should be called for a running stage")
	}
}
