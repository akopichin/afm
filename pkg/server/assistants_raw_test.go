package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/orchestrator"
)

func getAssistantRaw(t *testing.T, srv *Server, taskID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/assistants/"+taskID+"/raw", nil)
	w := httptest.NewRecorder()
	srv.routeAssistants(w, req)
	return w
}

func writeAssistantRaw(t *testing.T, runDir, taskID, content string) {
	t.Helper()
	path := filepath.Join(runDir, orchestrator.TaskRawResultRelPath(taskID))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write raw answer: %v", err)
	}
}

func TestAssistantRaw_ServesRawAnswer(t *testing.T) {
	srv := newTestServer(t, Config{AssistantsReader: &fakeAssistantsReader{}})
	writeAssistantRaw(t, srv.runDir, "t-1", "Проверил. Всё плохо, вот пять замечаний...")

	w := getAssistantRaw(t, srv, "t-1")
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff: got %q", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control: got %q, want no-store", got)
	}

	var body struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v, body=%s", err, w.Body.String())
	}
	if body.Content != "Проверил. Всё плохо, вот пять замечаний..." {
		t.Errorf("content: got %q", body.Content)
	}
}

func TestAssistantRaw_404WhenMissing(t *testing.T) {
	srv := newTestServer(t, Config{AssistantsReader: &fakeAssistantsReader{}})
	w := getAssistantRaw(t, srv, "t-does-not-exist")
	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

func TestAssistantRaw_404WhenCollaborationOff(t *testing.T) {
	srv := newTestServer(t, Config{}) // no AssistantsReader
	writeAssistantRaw(t, srv.runDir, "t-1", "some raw text")

	w := getAssistantRaw(t, srv, "t-1")
	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

func TestAssistantRaw_RejectsUnsafeTaskID(t *testing.T) {
	srv := newTestServer(t, Config{AssistantsReader: &fakeAssistantsReader{}})

	req := httptest.NewRequest(http.MethodGet, "/api/assistants/x/raw", nil)
	req.URL.Path = "/api/assistants/../raw"
	w := httptest.NewRecorder()
	srv.routeAssistants(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestAssistantRaw_413WhenTooLarge(t *testing.T) {
	srv := newTestServer(t, Config{AssistantsReader: &fakeAssistantsReader{}})
	writeAssistantRaw(t, srv.runDir, "t-1", strings.Repeat("a", maxAssistantRawBytes+1))

	w := getAssistantRaw(t, srv, "t-1")
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status: got %d, want 413", w.Code)
	}
}
