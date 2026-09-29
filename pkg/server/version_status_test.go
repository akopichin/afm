package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestStatus_VersionSurfaced проверяет сквозной путь Config → New → Server →
// /api/status: заданная версия бинарника доходит до JSON под ключом "version".
func TestStatus_VersionSurfaced(t *testing.T) {
	srv := newTestServer(t, Config{Version: "v1.2.3"})
	if got := decodeStatus(t, srv).Version; got != "v1.2.3" {
		t.Errorf("version: expected %q, got %q", "v1.2.3", got)
	}
}

// TestStatus_VersionOmitEmpty проверяет omitempty на уровне СЫРОГО JSON: при
// пустой версии ключ "version" вообще отсутствует, при непустой — присутствует
// со значением. Декодирование в statusResponse не отличает отсутствие от
// пустой строки, поэтому нужен map[string]any.
func TestStatus_VersionOmitEmpty(t *testing.T) {
	raw := func(t *testing.T, cfg Config) map[string]any {
		t.Helper()
		srv := newTestServer(t, cfg)
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /api/status: expected 200, got %d", w.Code)
		}
		var m map[string]any
		if err := json.NewDecoder(w.Body).Decode(&m); err != nil {
			t.Fatalf("decode /api/status body: %v", err)
		}
		return m
	}

	empty := raw(t, Config{})
	if _, ok := empty["version"]; ok {
		t.Errorf("empty version: expected key %q absent, got %v", "version", empty["version"])
	}

	set := raw(t, Config{Version: "v9.9.9"})
	if got, ok := set["version"]; !ok || got != "v9.9.9" {
		t.Errorf("set version: expected key present with %q, got %v (present=%v)", "v9.9.9", got, ok)
	}
}
