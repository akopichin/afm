package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/akopichin/afm/pkg/orchestrator/bus"
)

func TestWebSocket_ReceivesEvents(t *testing.T) {
	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Give the subscription a moment to register, then publish.
	time.Sleep(50 * time.Millisecond)
	srv.uiBus.Publish(bus.Event{
		Type:    bus.EventStageStatusChanged,
		StageID: "s1",
		Data:    "awaiting_approval",
	})

	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(msg), "s1") {
		t.Errorf("unexpected message: %s", msg)
	}
	if !strings.Contains(string(msg), "stage_status_changed") {
		t.Errorf("event type missing in message: %s", msg)
	}
}
