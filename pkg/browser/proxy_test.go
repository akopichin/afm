package browser

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestPipeFramer_SplitsOnNUL_AcrossReads(t *testing.T) {
	// one Read returns 1.5 messages, next read the remainder
	fr := newPipeFramer()
	got := fr.feed([]byte(`{"id":1}` + "\x00" + `{"id":`))
	got = append(got, fr.feed([]byte(`2}`+"\x00"))...)
	if len(got) != 2 || string(got[0]) != `{"id":1}` || string(got[1]) != `{"id":2}` {
		t.Fatalf("framing wrong: %q", got)
	}
}

func TestPipeFramer_OversizedUnterminatedFrameErrors(t *testing.T) {
	fr := newPipeFramer()
	_, err := fr.feedChecked(bytes.Repeat([]byte("A"), maxFrameBytes+1)) // no NUL
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatal("unterminated oversized frame must error, not buffer forever")
	}
}

func extractID(t *testing.T, msg []byte) int64 {
	t.Helper()
	var m struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(msg, &m); err != nil {
		t.Fatalf("bad message %q: %v", msg, err)
	}
	return m.ID
}

func TestProxy_RemapsIDsAcrossReconnect(t *testing.T) {
	px := newProxy()
	connA := &wsClient{}
	connB := &wsClient{}

	outA := px.fromClient(connA, []byte(`{"id":1,"method":"Page.enable"}`))
	gidA := extractID(t, outA)
	outB := px.fromClient(connB, []byte(`{"id":1,"method":"Page.enable"}`))
	gidB := extractID(t, outB)
	if gidA == gidB {
		t.Fatalf("global ids must be unique across connections, both %d", gidA)
	}

	// Chrome responds to gidA → routes back to connA with its local id 1.
	r := px.fromChrome([]byte(fmt.Sprintf(`{"id":%d,"result":{}}`, gidA)))
	if r == nil || r.conn != connA {
		t.Fatalf("response must route to connA, got %+v", r)
	}
	if extractID(t, r.payload) != 1 {
		t.Fatalf("connA local id must be restored to 1, got %q", r.payload)
	}

	// connB reusing local id 1 must not receive connA's pending reply.
	r2 := px.fromChrome([]byte(fmt.Sprintf(`{"id":%d,"result":{}}`, gidB)))
	if r2 == nil || r2.conn != connB || extractID(t, r2.payload) != 1 {
		t.Fatalf("connB response must route to connB local id 1, got %+v", r2)
	}
}

func TestProxy_EventWithoutIDBroadcasts(t *testing.T) {
	px := newProxy()
	r := px.fromChrome([]byte(`{"method":"Page.frameNavigated","params":{}}`))
	if r == nil || r.conn != nil {
		t.Fatalf("an event (no id) must broadcast (conn nil), got %+v", r)
	}
}

func TestProxy_DropsPendingOnDisconnect(t *testing.T) {
	px := newProxy()
	connA := &wsClient{}
	out := px.fromClient(connA, []byte(`{"id":1,"method":"Page.enable"}`))
	gid := extractID(t, out)
	px.disconnect(connA)
	// A late reply for a disconnected client must be DROPPED, not broadcast: a
	// reconnected client reusing the same local id would otherwise accept it as
	// its own answer.
	r := px.fromChrome([]byte(fmt.Sprintf(`{"id":%d,"result":{}}`, gid)))
	if r != nil {
		t.Fatalf("a response for an expired pending id must be dropped, got %+v", r)
	}
}

func TestProxy_UnknownIDResponseIsDropped(t *testing.T) {
	px := newProxy()
	// A response whose id was never issued (no pending entry) must not be
	// broadcast to clients — it is an unroutable response, not an event.
	r := px.fromChrome([]byte(`{"id":999,"result":{}}`))
	if r != nil {
		t.Fatalf("unknown-id response must be dropped (nil), got %+v", r)
	}
}
