package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/akopichin/afm/pkg/proxy"
)

func TestProxy_StartShutdown(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	p := proxy.New(upstream.URL, nil)
	addr, err := p.Start(0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if addr == "" {
		t.Fatal("addr is empty after Start")
	}
	if p.Addr() != addr {
		t.Errorf("Addr() mismatch: %q vs %q", p.Addr(), addr)
	}

	resp, err := http.Get(addr + "/v1/messages")
	if err != nil {
		t.Fatalf("proxy GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body: got %s", body)
	}

	if err := p.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}
