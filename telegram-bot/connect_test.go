package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func statusServer(t *testing.T, started string) (*httptest.Server, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprintf(w, `{"flow_name":"demo","started_at":%q,"run_status":"running","stages":[]}`, started)
	}))
	return srv, srv.Listener.Addr().(*net.TCPAddr).Port
}

func TestRegisterPersistsAndRejectsReusedPort(t *testing.T) {
	srv, port := statusServer(t, "2026-10-20T06:00:00Z")
	defer srv.Close()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(store)
	first, err := reg.Register(context.Background(), port)
	if err != nil {
		t.Fatal(err)
	}
	again, err := reg.Register(context.Background(), port)
	if err != nil || again.Key != first.Key {
		t.Fatalf("duplicate: %+v %v", again, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, ok := store.Run(first.Key)
	if !ok || !got.StartedAt.Equal(time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("persisted run: %+v", got)
	}
}

func TestRegisterHTTPRejectsNonLoopbackAndBadPort(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewRegistry(store).Handler()
	for _, body := range []string{`{"dashboard_port":0}`, `{"dashboard_port":65536}`, `{"dashboard_port":9876,"url":"http://example.org"}`} {
		req := httptest.NewRequest(http.MethodPost, "/v1/runs/register", strings.NewReader(body))
		req.RemoteAddr = "192.0.2.4:1234"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("remote %q: %d", body, rr.Code)
		}
	}
}

func TestRegisterHTTPRejectsBrowserOriginAndSimpleContentType(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewRegistry(store).Handler()
	for _, tc := range []struct {
		origin, contentType string
		want                int
	}{
		{"https://foreign.example", "text/plain", http.StatusForbidden},
		{"", "text/plain", http.StatusUnsupportedMediaType},
		{"", "", http.StatusUnsupportedMediaType},
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/runs/register", strings.NewReader(`{"dashboard_port":9876}`))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Content-Type", tc.contentType)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("origin=%q type=%q: HTTP %d", tc.origin, tc.contentType, w.Code)
		}
	}
}
