package server

import (
	"net"
	"net/http"
	"testing"
	"time"
)

func TestStartOnServesHandlerOnGivenListener(t *testing.T) {
	srv := newTestServer(t, Config{Runtime: RuntimeInfo{Mode: "core"}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr, err := srv.StartOn(ln)
	if err != nil {
		t.Fatalf("StartOn: %v", err)
	}
	if addr != ln.Addr().String() {
		t.Errorf("addr = %q, want %q", addr, ln.Addr())
	}
	t.Cleanup(func() { _ = srv.Shutdown(t.Context()) })

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + addr + "/api/runtime")
	if err != nil {
		t.Fatalf("GET /api/runtime: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
