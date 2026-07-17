package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWebSocket_SendsHeartbeat(t *testing.T) {
	srv, _ := setupTestServerWithWS(t, 0, 40*time.Millisecond)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if strings.Contains(string(msg), `"type":"heartbeat"`) {
			return // success
		}
	}
	t.Fatal("heartbeat not received within timeout")
}

func TestWebSocket_ClosesSilentClient(t *testing.T) {
	srv, _ := setupTestServerWithWS(t, 150*time.Millisecond, 50*time.Millisecond)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Намеренно не читаем > pongWait → сервер не получает pong → рвёт соединение.
	// (gorilla отвечает на ping только из read-цикла приложения, поэтому пока
	// мы спим, pong на сервер не уходит.)
	time.Sleep(500 * time.Millisecond)

	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	// Пока мы спали, сервер успел наваливать heartbeat-текстовых фреймов в буфер;
	// вычитываем их, пока не наткнёмся на закрытие соединения.
	for i := 0; i < 1000; i++ {
		if _, _, err := conn.ReadMessage(); err != nil {
			return // success: сервер закрыл «молчаливого» клиента
		}
	}
	t.Fatal("expected server to close silent client, but read kept succeeding")
}
