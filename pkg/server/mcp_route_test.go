package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeMCP struct {
	served chan struct{}
	closed chan struct{}
}

func newFakeMCP() *fakeMCP {
	return &fakeMCP{served: make(chan struct{}, 4), closed: make(chan struct{})}
}

// ServeHTTP holds the request open like an SSE stream until Close.
func (f *fakeMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.served <- struct{}{}
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	select {
	case <-f.closed:
	case <-r.Context().Done():
	}
}

func (f *fakeMCP) Close(context.Context) error {
	close(f.closed)
	return nil
}

func TestMCPRouteDisabledAnswersJSON404(t *testing.T) {
	uiHit := false
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uiHit = true
		_, _ = w.Write([]byte("<html>"))
	})
	for name, cfg := range map[string]Config{"with ui": {UI: ui}, "without ui": {}} {
		t.Run(name, func(t *testing.T) {
			srv := newTestServer(t, cfg)
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
			req.Host = "localhost:8080"
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"mcp_disabled"`) {
				t.Fatalf("got %d %q, want 404 mcp_disabled", w.Code, w.Body.String())
			}
			if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") || uiHit {
				t.Fatalf("content-type %q, ui hit %v", w.Header().Get("Content-Type"), uiHit)
			}
		})
	}
}

func TestMCPRouteGuardRunsBeforeHandler(t *testing.T) {
	fake := newFakeMCP()
	srv := newTestServer(t, Config{MCP: fake})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	req.Host = "evil.example"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("foreign host got %d, want 403", w.Code)
	}
	select {
	case <-fake.served:
		t.Fatal("MCP handler reached through the guard")
	default:
	}
}

func TestMCPRouteShutdownClosesMCPBeforeHTTPServer(t *testing.T) {
	fake := newFakeMCP()
	srv := newTestServer(t, Config{MCP: fake})
	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.Config.ReadHeaderTimeout = 10 * time.Second
	ts.Start()
	defer ts.Close()
	srv.httpSrv = ts.Config

	resp, err := http.Get(ts.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	<-fake.served

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown with an open MCP stream: %v", err)
	}
	select {
	case <-fake.closed:
	default:
		t.Fatal("MCP.Close was not called")
	}
}
