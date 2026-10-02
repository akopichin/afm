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
	err      error

	// mu guards conn and closed — the ownership handoff between the launch
	// goroutine and close(). Whoever observes the other's flag owns killing Chrome,
	// so a launch that finishes AFTER close() kills its own Chrome instead of
	// leaking it, and close() after a finished launch kills the stored conn.
	mu     sync.Mutex
	conn   *chromeConn
	closed bool

	proxy   *proxy
	writeCh chan []byte // serialized writes to Chrome (bounded)

	clientsMu     sync.Mutex
	clients       map[*wsClient]struct{}
	clientsClosed bool // set by close() under clientsMu; rejects late registrations

	closeOnce sync.Once
	done      chan struct{}
}

// wsClient is one connected WebSocket, with its own bounded outbound queue.
// send is NEVER closed — teardown signals via done instead, so deliver (which runs
// on the readFromChrome goroutine) can never send on a closed channel. That removes
// the send/close data race the recover() previously only masked.
type wsClient struct {
	ws   *websocket.Conn
	send chan []byte
	done chan struct{} // closed by serveClient cleanup; gates the writer and deliver
}

// serveClient registers a client, pumps WebSocket ⇄ Chrome, and unregisters on
// disconnect. It blocks until the client disconnects.
func (s *session) serveClient(ws *websocket.Conn) {
	c := &wsClient{ws: ws, send: make(chan []byte, writeQueueSize), done: make(chan struct{})}
	s.clientsMu.Lock()
	if s.clientsClosed {
		// The session was torn down (Chrome died / run ending) between firstConnect
		// succeeding and this registration. Don't attach to a dead session — close
		// the socket so the client sees a clean disconnect instead of hanging.
		s.clientsMu.Unlock()
		_ = ws.Close()
		return
	}
	s.clients[c] = struct{}{}
	s.clientsMu.Unlock()

	// Writer goroutine: client send queue → WebSocket.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for {
			select {
			case msg := <-c.send:
				if err := ws.WriteMessage(websocket.TextMessage, msg); err != nil {
					return
				}
			case <-c.done:
				return
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
		// Real backpressure: block until Chrome's write queue accepts the request
		// (or the session ends). Dropping here would strand the pending-request
		// entry fromClient just recorded — Chrome would never answer and the
		// agent's tool-call would hang forever. Blocking is bounded in practice by
		// writeLoop, which tears the session down (closing s.done) the moment the
		// CDP pipe write fails.
		select {
		case s.writeCh <- out:
		case <-s.done:
			goto cleanup
		}
	}

cleanup:
	s.clientsMu.Lock()
	delete(s.clients, c)
	s.clientsMu.Unlock()
	s.proxy.disconnect(c)
	close(c.done) // signal the writer and any in-flight deliver; send is never closed
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
				s.close() // Chrome pipe is gone → tear down so a reconnect relaunches
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
			// EOF/read error = Chrome exited or crashed. Tear the session down and
			// evict it (via close) so a retry/resume reconnect relaunches a fresh
			// Chrome instead of proxying to a dead one forever.
			s.close()
			return
		}
	}
}

// route sends a Chrome message to its destination client, or broadcasts an event.
// A nil result means the message is an unroutable (stale/duplicate) response that
// must be dropped — never broadcast (see proxy.fromChrome).
func (s *session) route(msg []byte) {
	r := s.proxy.fromChrome(msg)
	if r == nil {
		return // unknown-id response: drop
	}
	if r.conn != nil {
		// A response is owed to exactly one client: if its queue is full, close
		// that slow client rather than drop the frame (a dropped response would
		// hang the client's tool-call forever).
		s.deliver(r.conn, r.payload, true)
		return
	}
	s.clientsMu.Lock()
	clients := make([]*wsClient, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.clientsMu.Unlock()
	for _, c := range clients {
		s.deliver(c, r.payload, false) // broadcast event: best-effort
	}
}

// deliver enqueues a message to a client without blocking the reader on a slow
// client. When mustDeliver is true (a response), a full queue closes the client's
// socket so its serveClient loop unblocks, disconnects, and the tool-call errors
// instead of hanging. For a best-effort broadcast event, a full queue just drops.
func (s *session) deliver(c *wsClient, msg []byte, mustDeliver bool) {
	select {
	case c.send <- msg:
	case <-c.done: // this client is gone — nothing to deliver
	case <-s.done: // whole session tearing down
	default:
		if mustDeliver {
			logf("session %s client queue full, closing slow client", s.key)
			_ = c.ws.Close()
		} else {
			logf("session %s client queue full, dropping event", s.key)
		}
	}
}

// close tears the session down once: signal done, close connected clients' sockets
// (so their serveClient readers unblock instead of hanging on a dead session), kill
// Chrome, and evict the session from the manager so a reconnect relaunches.
func (s *session) close() {
	s.closeOnce.Do(func() {
		// Mark closed and grab the conn under mu. If the launch goroutine has not
		// stored conn yet, it will observe closed==true and kill its own Chrome;
		// if it already stored one, we kill it here. Exactly one side cleans up.
		s.mu.Lock()
		s.closed = true
		c := s.conn
		s.mu.Unlock()

		close(s.done)
		// Evict FIRST (before the blocking conn.close()/cmd.Wait()) so a reconnect
		// arriving while Chrome is being reaped doesn't bind to this dead session;
		// it will miss the map and relaunch a fresh one.
		if s.mgr != nil {
			s.mgr.evictSession(s.key, s)
		}
		s.clientsMu.Lock()
		s.clientsClosed = true // any registration after this point is rejected
		for cl := range s.clients {
			_ = cl.ws.Close()
		}
		s.clientsMu.Unlock()
		if c != nil {
			c.close()
		}
	})
}
