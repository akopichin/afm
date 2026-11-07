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

// Без UI сервер — чистый API: UI-пути дают 404, API отвечает.
func TestServer_WithoutUIServesNoWebPaths(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	for _, path := range []string{"/", "/index.html", "/favicon.svg", "/assets/index.js", "/skins/graphite/index.css"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("GET %s без UI: got %d, want 404", path, w.Code)
		}
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if w.Code != http.StatusOK {
		t.Errorf("GET /api/status: got %d, want 200", w.Code)
	}
}

func TestServer_UIHandlerServesRoot(t *testing.T) {
	ui := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("stub ui"))
	})
	handler := New(Config{UI: ui}).Handler()

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK || w.Body.String() != "stub ui" {
		t.Errorf("GET /: got %d %q, want UI stub", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/files/x", nil))
	if strings.Contains(w.Body.String(), "stub ui") {
		t.Error("GET /api/files/x с UI: API-маршрут не должен уходить в UI")
	}
}
