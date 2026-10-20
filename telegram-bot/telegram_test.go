package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestTelegramAuthorizationAndDurableOffset(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var mu sync.Mutex
	var offsets []int64
	var sent int
	tgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			var body struct {
				Offset int64 `json:"offset"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			offsets = append(offsets, body.Offset)
			mu.Unlock()
			if body.Offset == 0 {
				_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":1,"message":{"from":{"id":17},"chat":{"id":-1,"type":"group"},"text":"/runs"}},{"update_id":2,"message":{"from":{"id":18},"chat":{"id":18,"type":"private"},"text":"/runs"}},{"update_id":3,"message":{"from":{"id":17},"chat":{"id":17,"type":"private"},"text":"/runs"}}]}`))
			} else {
				_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
			}
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			mu.Lock()
			sent++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer tgServer.Close()
	tg := NewTelegramTransport("dummy")
	tg.base = tgServer.URL
	service := NewService(NewRegistry(store), tg, map[int64]bool{17: true})
	if err := service.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot().UpdateOffset; got != 4 {
		t.Fatalf("offset %d", got)
	}
	if err := service.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if sent != 1 || len(offsets) != 2 || offsets[1] != 4 {
		t.Fatalf("sent=%d offsets=%v", sent, offsets)
	}
}
func TestTelegramStartupRejectsMissingCredentials(t *testing.T) {
	if err := run(context.Background(), []string{"--port", "18080"}, func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN") {
		t.Fatalf("missing token: %v", err)
	}
	if _, err := ParseAllowedUsers("1,broken"); err == nil {
		t.Fatal("invalid allowlist accepted")
	}
}
func TestListenerPortDefaultsAndRejectsInvalidValues(t *testing.T) {
	defaultOptions, err := parseRunOptions(nil)
	if err != nil || defaultOptions.port != 8080 {
		t.Fatalf("default port: %+v, %v", defaultOptions, err)
	}
	for _, arg := range []string{"0", "65536", "garbage"} {
		if _, err := parseRunOptions([]string{"--port", arg}); err == nil {
			t.Fatalf("invalid listener port %q accepted", arg)
		}
	}
	custom, err := parseRunOptions([]string{"--port", "18080"})
	if err != nil || custom.port != 18080 {
		t.Fatalf("custom port: %+v, %v", custom, err)
	}
}
func TestStoreSingleOwnerAndRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	one, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path); err == nil {
		t.Fatal("second owner accepted")
	}
	if err := one.Update(func(st *State) error { st.UpdateOffset = 42; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := one.Close(); err != nil {
		t.Fatal(err)
	}
	two, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	if two.Snapshot().UpdateOffset != 42 {
		t.Fatal("offset not durable")
	}
}
func TestRegisterReusedPortCreatesNewKey(t *testing.T) {
	var mu sync.Mutex
	started := "2026-10-20T06:00:00Z"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		v := started
		mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"flow_name":"same label","started_at":%q,"stages":[]}`, v)
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registry := NewRegistry(store)
	first, err := registry.Register(context.Background(), port)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	started = "2026-10-20T07:00:00Z"
	mu.Unlock()
	second, err := registry.Register(context.Background(), port)
	if err != nil {
		t.Fatal(err)
	}
	if first.Key == second.Key || !store.Snapshot().Runs[first.Key].Invalidated {
		t.Fatalf("bindings %+v %+v", first, second)
	}
}
func TestRegisterHTTPValidatesLoopbackAndShape(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewRegistry(store).Handler()
	for _, body := range []string{`{"dashboard_port":0}`, `{"dashboard_port":65536}`, `{"dashboard_port":9876,"url":"http://example.org"}`, `{"dashboard_port":9876} trailing`} {
		req := httptest.NewRequest(http.MethodPost, "/v1/runs/register", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%q: HTTP %d", body, w.Code)
		}
	}
}
