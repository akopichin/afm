package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
)

// getVerifyReport вызывает handleVerifyReport напрямую (без routeFiles/mux) —
// тот же паттерн, что getArtifact в artifacts_test.go.
func getVerifyReport(t *testing.T, srv *Server, stageID, verID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/stages/"+stageID+"/verify/"+verID+"/report", nil)
	w := httptest.NewRecorder()
	srv.handleVerifyReport(w, req)
	return w
}

func writeVerifyReport(t *testing.T, runDir, stageID, verID, content string) {
	t.Helper()
	path := filepath.Join(runDir, stageID, stagefiles.ReportRelPath(verID))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write report: %v", err)
	}
}

func TestHandleVerifyReport_ReturnsContent(t *testing.T) {
	srv, runDir := setupTestServer(t)
	writeVerifyReport(t, runDir, testStageID, "v-20260101-020304-abcd", "# Отчёт проверки\n\nВердикт: pass\n")

	w := getVerifyReport(t, srv, testStageID, "v-20260101-020304-abcd")
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: got %q, want application/json", ct)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff: got %q", got)
	}

	var body struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v, body=%s", err, w.Body.String())
	}
	if body.Content != "# Отчёт проверки\n\nВердикт: pass\n" {
		t.Errorf("content: got %q", body.Content)
	}
}

func TestHandleVerifyReport_RejectsNonGET(t *testing.T) {
	srv, runDir := setupTestServer(t)
	writeVerifyReport(t, runDir, testStageID, "v-1", "report")

	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/verify/v-1/report", nil)
	w := httptest.NewRecorder()
	srv.handleVerifyReport(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 405", w.Code)
	}
}

func TestHandleVerifyReport_MissingReport404(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := getVerifyReport(t, srv, testStageID, "v-does-not-exist")
	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

func TestHandleVerifyReport_RejectsInvalidVerificationID(t *testing.T) {
	srv, runDir := setupTestServer(t)
	writeVerifyReport(t, runDir, testStageID, "v-1", "report")

	cases := []string{
		"..",
		"v..1",      // contains ".."
		"v-1%2fetc", // encoded slash -> charset reject once decoded into the path segment
		"v 1",       // space out of charset
		"",          // empty
	}
	for _, verID := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/stages/"+testStageID+"/verify/x/report", nil)
		req.URL.Path = "/api/stages/" + testStageID + "/verify/" + verID + "/report"
		w := httptest.NewRecorder()
		srv.handleVerifyReport(w, req)
		if w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound {
			t.Errorf("verID %q: status got %d, want 400 or 404", verID, w.Code)
		}
	}
}

func TestHandleVerifyReport_RejectsInvalidStageID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/stages/x/verify/v-1/report", nil)
	req.URL.Path = "/api/stages/../escape/verify/v-1/report"
	w := httptest.NewRecorder()
	srv.handleVerifyReport(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

// TestHandleVerifyReport_RejectsSymlinkEscape — тот же P0, что у артефактов
// (TestHandleArtifact_RejectsSymlinkEscape): report.md внутри verify/<verID>/
// не может быть симлинком наружу из runDir.
func TestHandleVerifyReport_RejectsSymlinkEscape(t *testing.T) {
	srv, runDir := setupTestServer(t)

	secret := filepath.Join(runDir, "secret.md")
	if err := os.WriteFile(secret, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}

	verDir := filepath.Join(runDir, testStageID, "verify", "v-1")
	if err := os.MkdirAll(verDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(verDir, "report.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	w := getVerifyReport(t, srv, testStageID, "v-1")
	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404 (symlink escape must be rejected)", w.Code)
	}
}

// TestHandleVerifyReport_ViaRouteFiles — конец-в-конец через настоящий mux
// routeStages, а не прямой вызов метода, чтобы зафиксировать реальный
// маршрут /api/stages/{id}/verify/{verID}/report.
func TestHandleVerifyReport_ViaRouteStages(t *testing.T) {
	srv, runDir := setupTestServer(t)
	writeVerifyReport(t, runDir, testStageID, "v-1", "ok")

	req := httptest.NewRequest(http.MethodGet, "/api/stages/"+testStageID+"/verify/v-1/report", nil)
	w := httptest.NewRecorder()
	srv.routeStages(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body=%s", w.Code, w.Body.String())
	}
}
