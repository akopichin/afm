package mcp_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/mcp"
)

func newTestServer(t *testing.T) (*mcp.Server, string) {
	t.Helper()
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "stage-1")
	if err := osMkdirAll(stageDir); err != nil {
		t.Fatal(err)
	}
	s := mcp.NewServer(runDir, nil)
	return s, runDir
}

func osMkdirAll(p string) error {
	return os.MkdirAll(p, 0755)
}

func rpc(t *testing.T, s *mcp.Server, urlPath string, method string, params any, id int) map[string]any {
	t.Helper()
	req := map[string]any{"jsonrpc": "2.0", tID: id, "method": method}
	if params != nil {
		req["params"] = params
	}
	body, _ := json.Marshal(req)
	r := httptest.NewRequest("POST", urlPath, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("rpc %s: HTTP %d, body %s", method, w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v, body: %s", err, w.Body.String())
	}
	return out
}

func TestToolsList(t *testing.T) {
	s, _ := newTestServer(t)
	resp := rpc(t, s, "/mcp/stage-1/implementation", "tools/list", nil, 1)
	result, _ := resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool, _ := tools[0].(map[string]any)
	if tool[tName] != tAskUser {
		t.Errorf("tool name: got %v", tool[tName])
	}
}

func TestToolsCallReplay(t *testing.T) {
	s, runDir := newTestServer(t)
	dialogPath := filepath.Join(runDir, "stage-1", "implementation.dialog.jsonl")
	if err := mcp.AppendQuestion(dialogPath, mcp.Question{ID: testQ1, Question: testQuestionX}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{ID: testQ1, Answer: answerYes, FromOptions: true}); err != nil {
		t.Fatal(err)
	}

	resp := rpc(t, s, "/mcp/stage-1/implementation", "tools/call",
		map[string]any{
			tName: tAskUser,
			tArguments: map[string]any{
				tID:       testQ1,
				tQuestion: testQuestionX,
			},
		}, 2)

	result, _ := resp["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(content))
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	var payload struct {
		Answer      string `json:"answer"`
		FromOptions bool   `json:"from_options"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Answer != answerYes || !payload.FromOptions {
		t.Errorf("replay wrong: %+v", payload)
	}
}

func TestToolsCallBlocksUntilAnswered(t *testing.T) {
	s, runDir := newTestServer(t)

	done := make(chan map[string]any, 1)
	go func() {
		done <- rpc(t, s, "/mcp/stage-1/implementation", "tools/call",
			map[string]any{
				tName:      tAskUser,
				tArguments: map[string]any{tID: testQ1, tQuestion: testQuestionX},
			}, 3)
	}()

	time.Sleep(50 * time.Millisecond)

	select {
	case <-done:
		t.Fatal("tools/call returned before answer was provided")
	default:
	}

	dialogPath := filepath.Join(runDir, "stage-1", "implementation.dialog.jsonl")
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{ID: testQ1, Answer: "hello", FromOptions: false}); err != nil {
		t.Fatal(err)
	}
	if err := s.NotifyAnswer("stage-1", "implementation", testQ1, "hello", false); err != nil {
		t.Fatal(err)
	}

	select {
	case resp := <-done:
		result, _ := resp["result"].(map[string]any)
		content, _ := result["content"].([]any)
		text, _ := content[0].(map[string]any)["text"].(string)
		if !strings.Contains(text, "hello") {
			t.Errorf("tool result missing answer: %s", text)
		}
	case <-time.After(time.Second):
		t.Fatal("tools/call did not return after answer")
	}
}

func TestToolsCallPendingTimeout(t *testing.T) {
	s, _ := newTestServer(t)
	s.SetPollingTimeout(50 * time.Millisecond)

	resp := rpc(t, s, "/mcp/stage-1/implementation", "tools/call",
		map[string]any{
			tName:      tAskUser,
			tArguments: map[string]any{tID: "q-pending", tQuestion: testQuestionX},
		}, 5)

	if errObj, hasErr := resp["error"]; hasErr {
		t.Fatalf("expected success result with pending payload, got error: %v", errObj)
	}
	result, _ := resp["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("pending result must not be isError: %+v", result)
	}
	content, _ := result["content"].([]any)
	text, _ := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "\"status\":\"pending\"") {
		t.Errorf("expected pending status marker in payload: %s", text)
	}
	if !strings.Contains(text, "q-pending") {
		t.Errorf("expected question id echoed back so agent retries with same id: %s", text)
	}
}

func TestToolsCallCancel(t *testing.T) {
	s, _ := newTestServer(t)

	done := make(chan map[string]any, 1)
	go func() {
		done <- rpc(t, s, "/mcp/stage-1/implementation", "tools/call",
			map[string]any{
				tName:      tAskUser,
				tArguments: map[string]any{tID: testQ1, tQuestion: testQuestionX},
			}, 4)
	}()

	time.Sleep(50 * time.Millisecond)
	if err := s.CancelStage("stage-1"); err != nil {
		t.Fatal(err)
	}

	select {
	case resp := <-done:
		if errObj, hasErr := resp["error"]; hasErr {
			_ = errObj
			return
		}
		if result, _ := resp["result"].(map[string]any); result != nil {
			if isErr, _ := result["isError"].(bool); isErr {
				return
			}
		}
		t.Errorf("expected error result: %+v", resp)
	case <-time.After(time.Second):
		t.Fatal("cancel did not unblock tools/call")
	}
}

func TestInitialize(t *testing.T) {
	s, _ := newTestServer(t)
	resp := rpc(t, s, "/mcp/stage-1/implementation", "initialize", nil, 1)
	result, _ := resp["result"].(map[string]any)
	if result["protocolVersion"] == nil {
		t.Error("expected protocolVersion in initialize response")
	}
}

func TestServeHTTPBadPath(t *testing.T) {
	s, _ := newTestServer(t)
	body := []byte(`{"jsonrpc":"2.0",tID:1,"method":"initialize"}`)
	r := httptest.NewRequest("POST", "/mcp/onlyonepart", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestServeHTTPMethodNotAllowed(t *testing.T) {
	s, _ := newTestServer(t)
	r := httptest.NewRequest("GET", "/mcp/stage-1/implementation", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}
