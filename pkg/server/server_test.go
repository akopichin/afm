package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServerRouteStages(t *testing.T) {
	srv, _ := setupTestServer(t)

	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/api/stages/s1/plan", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code == http.StatusNotFound {
		t.Error("route should match /api/stages/s1/plan")
	}
}

func TestServerServesMarkdownIt(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/markdown-it.min.js", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /markdown-it.min.js: ожидался 200, получен %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "markdownit") {
		t.Error("в теле ответа нет глобала markdownit")
	}
}
