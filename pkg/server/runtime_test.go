package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func getRuntime(t *testing.T, srv *Server) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/runtime", nil))
	var body map[string]any
	if w.Code == http.StatusOK {
		if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
			t.Fatalf("decode /api/runtime: %v", err)
		}
	}
	return w, body
}

func TestRuntime_ExactFieldSet(t *testing.T) {
	srv := newTestServer(t, Config{})
	_, body := getRuntime(t, srv)
	if len(body) != 4 {
		t.Fatalf("fields = %v, want exactly protocol_version, mode, run_id, ready", body)
	}
	if body["protocol_version"] != float64(1) {
		t.Errorf("protocol_version = %v, want 1", body["protocol_version"])
	}
	for _, key := range []string{"mode", "run_id", "ready"} {
		if _, ok := body[key]; !ok {
			t.Errorf("field %q missing", key)
		}
	}
}

func TestRuntime_RunIDPresentWhenEmpty(t *testing.T) {
	srv := newTestServer(t, Config{})
	srv.runDir = ""
	_, body := getRuntime(t, srv)
	id, ok := body["run_id"]
	if !ok || id != "" {
		t.Errorf("run_id = %v (present=%v), want empty string", id, ok)
	}
}

func TestRuntime_ModeDefaultsToDashboard(t *testing.T) {
	_, body := getRuntime(t, newTestServer(t, Config{}))
	if body["mode"] != "dashboard" {
		t.Errorf("mode = %v, want dashboard", body["mode"])
	}
	_, body = getRuntime(t, newTestServer(t, Config{Runtime: RuntimeInfo{Mode: "core"}}))
	if body["mode"] != "core" {
		t.Errorf("mode = %v, want core", body["mode"])
	}
}

func TestRuntime_Ready(t *testing.T) {
	cases := map[string]struct {
		ready func() bool
		want  bool
	}{
		"nil":   {nil, false},
		"false": {func() bool { return false }, false},
		"true":  {func() bool { return true }, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, body := getRuntime(t, newTestServer(t, Config{Runtime: RuntimeInfo{Ready: tc.ready}}))
			if body["ready"] != tc.want {
				t.Errorf("ready = %v, want %v", body["ready"], tc.want)
			}
		})
	}
}

func TestRuntime_PostRejected(t *testing.T) {
	srv := newTestServer(t, Config{})
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/runtime", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestRuntime_RunIDMatchesStatus(t *testing.T) {
	cases := map[string]Config{
		"without side agent": {},
		"with side agent":    {SideAgent: &fakeSideAgent{convID: "conv-42"}},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			srv := newTestServer(t, cfg)
			_, body := getRuntime(t, srv)
			status := decodeStatus(t, srv)
			if body["run_id"] != status.RunID || status.RunID == "" {
				t.Errorf("runtime run_id = %v, status run_id = %q", body["run_id"], status.RunID)
			}
			if cfg.SideAgent == nil && status.RunID != filepath.Base(srv.runDir) {
				t.Errorf("run_id = %q, want base of run dir", status.RunID)
			}
		})
	}
}
