package server

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/akopichin/afm/pkg/orchestrator"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Дефолтные keepalive-таймауты (применяются в New, если Config не задаёт).
// pingPeriod < pongWait, иначе сервер порвёт живых клиентов.
const (
	defaultWSPongWait   = 60 * time.Second
	defaultWSPingPeriod = 30 * time.Second
	defaultWSWriteWait  = 10 * time.Second
)

// handleWebSocket апгрейдит соединение и стримит UIBus-события клиенту.
// readPump детектит «мёртвого» клиента по таймауту pong и рвёт соединение;
// writePump — единственный писатель (gorilla требует одного писателя), шлёт
// события, ping (control-фрейм → browser auto-pong) и app-level heartbeat
// (text — JS-видимый сигнал для клиентского watchdog).
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade: %v", err)
		return
	}
	defer conn.Close()

	id, ch := s.uiBus.Subscribe(64)
	defer s.uiBus.Unsubscribe(id)

	done := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		s.writePump(conn, id, ch, done)
	}()
	s.readPump(conn)
	close(done)
	<-writerDone
}

// readPump дренирует входящие фреймы. Нужны сами read-вызовы, чтобы gorilla
// обрабатывала pong/close. PongHandler сбрасывает read-deadline; если pong
// перестал приходить (клиент умер) — дедлайн срабатывает, ReadMessage вернёт
// ошибку, readPump выйдет → соединение закроется.
func (s *Server) readPump(conn *websocket.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(s.wsPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(s.wsPongWait))
	})
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

// writePump — единственный писатель в conn. Select по событиям / тикеру / done.
func (s *Server) writePump(conn *websocket.Conn, id uint64, ch <-chan orchestrator.Event, done <-chan struct{}) {
	ticker := time.NewTicker(s.wsPingPeriod)
	defer ticker.Stop()

	prevDrops := uint64(0)
	heartbeat, _ := json.Marshal(map[string]string{"type": "heartbeat"})

	for {
		select {
		case <-done:
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if drops := s.uiBus.SubscriberDroppedCount(id); drops > prevDrops {
				prevDrops = drops
				if drops > 10 {
					_ = conn.SetWriteDeadline(time.Now().Add(s.wsWriteWait))
					_ = conn.WriteMessage(
						websocket.CloseMessage,
						websocket.FormatCloseMessage(1008, "event queue overflow"),
					)
					return
				}
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(s.wsWriteWait))
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-ticker.C:
			// ping — control-фрейм (клиент auto-pong); heartbeat — text (виден в JS).
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(s.wsWriteWait)); err != nil {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(s.wsWriteWait))
			if err := conn.WriteMessage(websocket.TextMessage, heartbeat); err != nil {
				return
			}
		}
	}
}
