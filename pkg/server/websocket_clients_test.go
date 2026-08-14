package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestServer_ConnectedClients_TracksOpenConnections(t *testing.T) {
	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if got := srv.ConnectedClients(); got != 0 {
		t.Fatalf("before connect: ConnectedClients() = %d, want 0", got)
	}

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	waitForCondition(t, func() bool { return srv.ConnectedClients() == 1 })

	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	waitForCondition(t, func() bool { return srv.ConnectedClients() == 0 })
}

func waitForCondition(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}
