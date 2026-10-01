package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/sideagent"
)

// streamIDs runs the stream handler against a recorder and returns the ordered
// `id:` values written as SSE events.
func streamIDs(body string) []uint64 {
	var out []uint64
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "id: ") {
			if n, err := strconv.ParseUint(strings.TrimPrefix(line, "id: "), 10, 64); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

func TestSideAgentStream_HistoryThenLive(t *testing.T) {
	ch := make(chan sideagent.Event, 1)
	ch <- ev(4, sideagent.EventAssistantText, "t1")
	close(ch) // after the live event, the subscription ends → handler returns
	fake := &fakeSideAgent{
		history: []sideagent.Event{
			ev(1, sideagent.EventTurnAccepted, "t1"),
			ev(2, sideagent.EventTurnStarted, "t1"),
			ev(3, sideagent.EventAssistantText, "t1"),
		},
		subCh: ch,
	}
	srv := newTestServer(t, Config{SideAgent: fake})

	req := httptest.NewRequest(http.MethodGet, "/api/side-agent/stream?after_seq=0", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	got := streamIDs(w.Body.String())
	want := []uint64{1, 2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ids = %v, want %v", got, want)
		}
	}
}

func TestSideAgentStream_SubscribeBeforeHistory(t *testing.T) {
	// The no-lost-tail guarantee depends on registering the live subscription
	// BEFORE snapshotting history.
	ch := make(chan sideagent.Event)
	close(ch)
	fake := &fakeSideAgent{subCh: ch}
	srv := newTestServer(t, Config{SideAgent: fake})

	req := httptest.NewRequest(http.MethodGet, "/api/side-agent/stream", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.callOrder) < 2 || fake.callOrder[0] != "subscribe" || fake.callOrder[1] != "history" {
		t.Fatalf("call order = %v, want subscribe before history", fake.callOrder)
	}
	if !fake.cancelCalled {
		t.Error("expected the subscription cancel to run on handler exit")
	}
}

func TestSideAgentStream_DedupReplayLiveOverlap(t *testing.T) {
	// The live channel re-delivers events that are also in history (2,3): the
	// monotonic seq filter drops them, and 4 is the only new one.
	ch := make(chan sideagent.Event, 3)
	ch <- ev(2, sideagent.EventTurnStarted, "t1")
	ch <- ev(3, sideagent.EventAssistantText, "t1")
	ch <- ev(4, sideagent.EventTurnCompleted, "t1")
	close(ch)
	fake := &fakeSideAgent{
		history: []sideagent.Event{
			ev(1, sideagent.EventTurnAccepted, "t1"),
			ev(2, sideagent.EventTurnStarted, "t1"),
			ev(3, sideagent.EventAssistantText, "t1"),
		},
		subCh: ch,
	}
	srv := newTestServer(t, Config{SideAgent: fake})

	req := httptest.NewRequest(http.MethodGet, "/api/side-agent/stream?after_seq=0", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	got := streamIDs(w.Body.String())
	want := []uint64{1, 2, 3, 4}
	if strings.Count(w.Body.String(), "id: 2\n") != 1 {
		t.Errorf("seq 2 appears more than once (dedup failed):\n%s", w.Body.String())
	}
	if len(got) != len(want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
}

func TestSideAgentStream_ReconnectLastEventID(t *testing.T) {
	ch := make(chan sideagent.Event)
	close(ch) // no live events — reconnect just re-reads the missed tail
	fake := &fakeSideAgent{
		history: []sideagent.Event{
			ev(1, sideagent.EventTurnAccepted, "t1"),
			ev(2, sideagent.EventTurnStarted, "t1"),
			ev(3, sideagent.EventAssistantText, "t1"),
			ev(4, sideagent.EventAssistantText, "t1"),
			ev(5, sideagent.EventTurnCompleted, "t1"),
		},
		subCh: ch,
	}
	srv := newTestServer(t, Config{SideAgent: fake})

	req := httptest.NewRequest(http.MethodGet, "/api/side-agent/stream", nil)
	req.Header.Set("Last-Event-ID", "3") // reconnect: already has up to seq 3
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	got := streamIDs(w.Body.String())
	want := []uint64{4, 5}
	if len(got) != len(want) || got[0] != 4 || got[1] != 5 {
		t.Fatalf("ids = %v, want %v (no dups of already-seen tail)", got, want)
	}
}

func TestSideAgentStream_OverflowDropsConnection(t *testing.T) {
	// A closed channel models the manager dropping a slow subscriber; the
	// handler returns gracefully (client reconnects and re-reads history).
	ch := make(chan sideagent.Event)
	close(ch)
	fake := &fakeSideAgent{
		history: []sideagent.Event{ev(1, sideagent.EventTurnAccepted, "t1")},
		subCh:   ch,
	}
	srv := newTestServer(t, Config{SideAgent: fake})

	done := make(chan struct{})
	req := httptest.NewRequest(http.MethodGet, "/api/side-agent/stream", nil)
	w := httptest.NewRecorder()
	go func() {
		srv.Handler().ServeHTTP(w, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after subscription drop")
	}
	if ids := streamIDs(w.Body.String()); len(ids) != 1 || ids[0] != 1 {
		t.Errorf("history ids = %v, want [1]", ids)
	}
}

func TestSideAgentStream_Heartbeat(t *testing.T) {
	// Shorten the heartbeat for the test, restore afterwards.
	orig := sideAgentHeartbeatPeriod
	sideAgentHeartbeatPeriod = 15 * time.Millisecond
	t.Cleanup(func() { sideAgentHeartbeatPeriod = orig })

	fake := &fakeSideAgent{subCh: make(chan sideagent.Event)} // never sends, never closes
	srv := newTestServer(t, Config{SideAgent: fake})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/side-agent/stream", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), ": heartbeat") {
			return // saw a heartbeat comment line
		}
	}
	t.Fatal("no heartbeat comment observed on an idle stream")
}

func TestSideAgentStream_ShutdownReturns(t *testing.T) {
	// A long-lived stream must not hold Shutdown open forever: closing the
	// server-level done signal unblocks the handler.
	fake := &fakeSideAgent{subCh: make(chan sideagent.Event)} // never sends/closes
	srv := newTestServer(t, Config{SideAgent: fake})

	handlerDone := make(chan struct{})
	req := httptest.NewRequest(http.MethodGet, "/api/side-agent/stream", nil)
	w := httptest.NewRecorder()
	go func() {
		srv.Handler().ServeHTTP(w, req)
		close(handlerDone)
	}()

	// Give the handler a moment to enter its live loop, then shut down.
	time.Sleep(20 * time.Millisecond)
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not return after Shutdown")
	}
}
