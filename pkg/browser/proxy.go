package browser

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync"
)

// maxFrameBytes bounds a single CDP message so a malformed/stalled Chrome cannot
// grow memory unboundedly. It is also applied as the WebSocket read limit.
const maxFrameBytes = 8 << 20 // 8 MiB

// ErrFrameTooLarge is returned when an unterminated CDP frame exceeds maxFrameBytes.
var ErrFrameTooLarge = errors.New("browser: CDP frame exceeds max size")

// pipeFramer reassembles a byte stream of NUL-terminated CDP messages into whole
// messages, buffering any partial trailing fragment across reads.
type pipeFramer struct {
	buf []byte
}

func newPipeFramer() *pipeFramer { return &pipeFramer{} }

// feed appends data and returns any complete messages. It panics on an oversized
// unterminated frame only via feedChecked; feed itself ignores that error (used in
// tests where the input is well-formed).
func (f *pipeFramer) feed(data []byte) [][]byte {
	msgs, _ := f.feedChecked(data)
	return msgs
}

// feedChecked appends data and returns any complete (NUL-terminated) messages.
// If the buffered-but-unterminated remainder exceeds maxFrameBytes it returns
// ErrFrameTooLarge rather than buffering forever.
func (f *pipeFramer) feedChecked(data []byte) ([][]byte, error) {
	f.buf = append(f.buf, data...)
	var out [][]byte
	for {
		i := bytes.IndexByte(f.buf, 0)
		if i < 0 {
			if len(f.buf) > maxFrameBytes {
				return out, ErrFrameTooLarge
			}
			break
		}
		msg := make([]byte, i)
		copy(msg, f.buf[:i])
		out = append(out, msg)
		f.buf = f.buf[i+1:]
	}
	if len(f.buf) == 0 {
		f.buf = f.buf[:0]
	}
	return out, nil
}

// encodeFrame appends the NUL terminator for a CDP pipe message.
func encodeFrame(msg []byte) []byte {
	out := make([]byte, len(msg)+1)
	copy(out, msg)
	out[len(msg)] = 0
	return out
}

// pendingReq remembers which client issued a request and its local id, so a
// Chrome response can be routed back and the local id restored.
type pendingReq struct {
	conn    *wsClient
	localID json.RawMessage
}

// routed is the destination of a Chrome message: a specific client (response) or
// all clients when conn is nil (a CDP event with no id — broadcast).
type routed struct {
	conn    *wsClient
	payload []byte
}

// proxy remaps CDP request ids so that reconnecting clients (retry/resume) never
// collide on a prior connection's in-flight ids. One proxy serves one Chrome.
type proxy struct {
	mu      sync.Mutex
	nextID  int64
	pending map[int64]pendingReq
}

func newProxy() *proxy { return &proxy{pending: map[int64]pendingReq{}} }

// fromClient rewrites a client CDP message's id to a globally-unique id, records
// the mapping, and returns the message to forward to Chrome. A message without an
// id (rare) is forwarded unchanged.
func (p *proxy) fromClient(conn *wsClient, msg []byte) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(msg, &m); err != nil {
		return msg // not an object we understand; forward verbatim
	}
	raw, ok := m["id"]
	if !ok {
		return msg
	}
	p.mu.Lock()
	p.nextID++
	gid := p.nextID
	p.pending[gid] = pendingReq{conn: conn, localID: raw}
	p.mu.Unlock()
	m["id"], _ = json.Marshal(gid)
	out, err := json.Marshal(m)
	if err != nil {
		return msg
	}
	return out
}

// fromChrome maps a Chrome message back to its originating client, restoring the
// client's local id. A message with no matching pending id (an event, or a dropped
// request) routes to nil (broadcast to all clients of the session).
func (p *proxy) fromChrome(msg []byte) *routed {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(msg, &m); err != nil {
		return &routed{conn: nil, payload: msg}
	}
	raw, ok := m["id"]
	if !ok {
		return &routed{conn: nil, payload: msg} // CDP event → broadcast
	}
	var gid int64
	if err := json.Unmarshal(raw, &gid); err != nil {
		return &routed{conn: nil, payload: msg}
	}
	p.mu.Lock()
	req, found := p.pending[gid]
	if found {
		delete(p.pending, gid)
	}
	p.mu.Unlock()
	if !found {
		return &routed{conn: nil, payload: msg}
	}
	m["id"] = req.localID
	out, err := json.Marshal(m)
	if err != nil {
		out = msg
	}
	return &routed{conn: req.conn, payload: out}
}

// disconnect drops all pending requests issued by a client that has gone away, so
// a late Chrome response for it is treated as unroutable (broadcast/ignored)
// rather than delivered to a reused connection object.
func (p *proxy) disconnect(conn *wsClient) {
	p.mu.Lock()
	for gid, req := range p.pending {
		if req.conn == conn {
			delete(p.pending, gid)
		}
	}
	p.mu.Unlock()
}
