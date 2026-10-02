package browser

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func waitEvicted(t *testing.T, m *Manager, key string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		m.mu.Lock()
		_, ok := m.sessions[key]
		m.mu.Unlock()
		if !ok {
			return
		}
		select {
		case <-deadline:
			t.Fatal("session was not evicted after Chrome death")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestManager_ChromeDeathRelaunchesOnReconnect proves that when Chrome exits
// mid-stage the session is torn down and evicted, so a retry/resume reconnect
// launches a fresh Chrome instead of proxying to a dead one.
func TestManager_ChromeDeathRelaunchesOnReconnect(t *testing.T) {
	var launches int32
	deaths := make(chan *os.File, 4) // Chrome stdout write-ends; close to simulate death
	m := newTestManager(t, nil)
	m.launch = func(_ context.Context, _, dir string) (*chromeConn, error) {
		atomic.AddInt32(&launches, 1)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		inR, inW, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		outR, outW, err := os.Pipe()
		if err != nil {
			inR.Close()
			inW.Close()
			return nil, err
		}
		go func() { _, _ = io.Copy(io.Discard, inR); inR.Close() }()
		deaths <- outW
		return &chromeConn{toChrome: inW, fromChrome: outR}, nil
	}

	dialAuthed(t, m, "ep1", "build")
	(<-deaths).Close() // kill Chrome: its stdout closes → readFromChrome EOF → evict
	waitEvicted(t, m, "ep1/build")

	dialAuthed(t, m, "ep1", "build") // reconnect
	if atomic.LoadInt32(&launches) != 2 {
		t.Fatalf("Chrome death must relaunch on reconnect, got %d launches", launches)
	}
}

// fakeCDPManager wires the Manager's launch seam to an in-process fake Chrome that
// speaks the CDP pipe protocol: it reads NUL-terminated requests and echoes a
// result for each id. This exercises the whole stack (listener → auth → session →
// write queue → pipe framing → proxy id-remap → client delivery) without a real
// browser, so it runs in CI.
func fakeCDPManager(t *testing.T) *Manager {
	t.Helper()
	m := newTestManager(t, nil)
	m.launch = func(_ context.Context, _, dir string) (*chromeConn, error) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		inR, inW, err := os.Pipe() // afm writes inW (toChrome); fake reads inR
		if err != nil {
			return nil, err
		}
		outR, outW, err := os.Pipe() // fake writes outW; afm reads outR (fromChrome)
		if err != nil {
			inR.Close()
			inW.Close()
			return nil, err
		}
		go func() {
			defer inR.Close()
			defer outW.Close()
			fr := newPipeFramer()
			buf := make([]byte, 4096)
			for {
				n, rerr := inR.Read(buf)
				for _, msg := range fr.feed(buf[:n]) {
					var obj map[string]json.RawMessage
					if json.Unmarshal(msg, &obj) != nil {
						continue
					}
					if id, ok := obj["id"]; ok {
						resp := []byte(`{"id":` + string(id) + `,"result":{"ok":true}}`)
						if _, werr := outW.Write(encodeFrame(resp)); werr != nil {
							return
						}
					}
				}
				if rerr != nil {
					return
				}
			}
		}()
		return &chromeConn{toChrome: inW, fromChrome: outR}, nil
	}
	return m
}

func TestManager_EndToEnd_RequestResponseRouted(t *testing.T) {
	m := fakeCDPManager(t)
	ws := dialAuthed(t, m, "ep1", "build")

	if err := ws.WriteMessage(websocket.TextMessage, []byte(`{"id":7,"method":"Page.enable"}`)); err != nil {
		t.Fatal(err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var resp struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("bad response %q: %v", data, err)
	}
	if resp.ID != 7 {
		t.Fatalf("response must carry the client's local id 7, got %s", data)
	}
	if len(resp.Result) == 0 {
		t.Fatalf("response must carry a result, got %s", data)
	}
}

func TestManager_EndToEnd_TwoStagesIndependent(t *testing.T) {
	m := fakeCDPManager(t)
	wsA := dialAuthed(t, m, "ep1", "build")
	wsB := dialAuthed(t, m, "ep1", "test")

	// Both reuse local id 1; each must get its own routed reply.
	for _, ws := range []*websocket.Conn{wsA, wsB} {
		if err := ws.WriteMessage(websocket.TextMessage, []byte(`{"id":1,"method":"Page.enable"}`)); err != nil {
			t.Fatal(err)
		}
		_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var resp struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(data, &resp); err != nil || resp.ID != 1 {
			t.Fatalf("want id 1, got %s (err %v)", data, err)
		}
	}
	if profileDirOf(m, "ep1", "build") == profileDirOf(m, "ep1", "test") {
		t.Fatal("two stages must have separate profiles end-to-end")
	}
}
