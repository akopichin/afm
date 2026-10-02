package browser

import (
	"sync"

	"github.com/gorilla/websocket"
)

// session is one stage's dedicated Chrome plus its connected WebSocket clients.
// Chrome is launched once (sync.Once); reconnects (retry/resume) reuse it.
type session struct {
	key        string
	profileDir string
	mgr        *Manager

	once     sync.Once
	launched chan struct{} // closed when the launch attempt completes (success or fail)
	conn     *chromeConn
	err      error

	proxy   *proxy
	writeCh chan []byte // serialized writes to Chrome (bounded)

	clientsMu sync.Mutex
	clients   map[*wsClient]struct{}

	closeOnce sync.Once
	done      chan struct{}
}

// wsClient is one connected WebSocket, with its own bounded outbound queue.
type wsClient struct {
	ws   *websocket.Conn
	send chan []byte
}

// serveClient registers a client, pumps WebSocket ⇄ Chrome, and unregisters on
// disconnect. It blocks until the client disconnects.
func (s *session) serveClient(ws *websocket.Conn) {
	c := &wsClient{ws: ws, send: make(chan []byte, writeQueueSize)}
	s.clientsMu.Lock()
	s.clients[c] = struct{}{}
	s.clientsMu.Unlock()

	// Writer goroutine: client send queue → WebSocket.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for {
			select {
			case msg, ok := <-c.send:
				if !ok {
					return
				}
				if err := ws.WriteMessage(websocket.TextMessage, msg); err != nil {
					return
				}
			case <-s.done:
				return
			}
		}
	}()

	// Reader loop (this goroutine): WebSocket → proxy → Chrome write queue.
	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			break
		}
		out := s.proxy.fromClient(c, msg)
		select {
		case s.writeCh <- out:
		case <-s.done:
			goto cleanup
		default:
			// Backpressure: Chrome write queue full — drop the slow session.
			logf("session %s write queue full, dropping message", s.key)
		}
	}

cleanup:
	s.clientsMu.Lock()
	delete(s.clients, c)
	s.clientsMu.Unlock()
	s.proxy.disconnect(c)
	close(c.send)
	_ = ws.Close()
	<-writerDone
}

// writeLoop drains the serialized Chrome write queue into the CDP pipe.
func (s *session) writeLoop() {
	for {
		select {
		case msg, ok := <-s.writeCh:
			if !ok {
				return
			}
			if _, err := s.conn.toChrome.Write(encodeFrame(msg)); err != nil {
				logf("session %s write to chrome failed: %v", s.key, err)
				return
			}
		case <-s.done:
			return
		}
	}
}

// readFromChrome reads CDP messages from the pipe, reassembles NUL-terminated
// frames, routes responses to the originating client, and broadcasts events.
func (s *session) readFromChrome() {
	fr := newPipeFramer()
	buf := make([]byte, 64*1024)
	for {
		n, err := s.conn.fromChrome.Read(buf)
		if n > 0 {
			msgs, ferr := fr.feedChecked(buf[:n])
			for _, msg := range msgs {
				s.route(msg)
			}
			if ferr != nil {
				logf("session %s framing error: %v", s.key, ferr)
				s.close()
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// route sends a Chrome message to its destination client, or broadcasts an event.
func (s *session) route(msg []byte) {
	r := s.proxy.fromChrome(msg)
	if r.conn != nil {
		s.deliver(r.conn.(*wsClient), r.payload)
		return
	}
	s.clientsMu.Lock()
	clients := make([]*wsClient, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.clientsMu.Unlock()
	for _, c := range clients {
		s.deliver(c, r.payload)
	}
}

// deliver enqueues a message to a client without blocking the reader on a slow
// client (drops with a log when the client's queue is full).
func (s *session) deliver(c *wsClient, msg []byte) {
	defer func() { _ = recover() }() // send may be closed concurrently on disconnect
	select {
	case c.send <- msg:
	case <-s.done:
	default:
		logf("session %s client queue full, dropping message", s.key)
	}
}

// close tears the session down once: signal done, kill Chrome, close the pipes.
func (s *session) close() {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.conn != nil {
			s.conn.close()
		}
	})
}
