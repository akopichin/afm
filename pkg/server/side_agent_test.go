package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/sideagent"
)

// fakeSideAgent подменяет методы сервиса, записывает порядок Subscribe/History и аргументы Send.
type fakeSideAgent struct {
	mu sync.Mutex

	history     []sideagent.Event
	command     string
	convID      string
	unavailable bool

	enqueueFn func(clientMessageID, text string, target sideagent.TurnTarget) (sideagent.MessageReceipt, error)
	cancelFn  func(messageID string) (sideagent.MessageReceipt, error)
	stopFn    func(turnID string) error

	// subCh — канал, который возвращает Subscribe. Тест заполняет/закрывает его,
	// чтобы смоделировать live-поток и overflow-drop.
	subCh chan sideagent.Event

	callOrder    []string
	cancelCalled bool
	sentCMID     string
	sentText     string
	sentTarget   sideagent.TurnTarget
	cancelledID  string
}

func (f *fakeSideAgent) record(name string) {
	f.mu.Lock()
	f.callOrder = append(f.callOrder, name)
	f.mu.Unlock()
}

func (f *fakeSideAgent) History() []sideagent.Event {
	f.record("history")
	return f.history
}

func (f *fakeSideAgent) Enqueue(clientMessageID, text string, target sideagent.TurnTarget) (sideagent.MessageReceipt, error) {
	f.mu.Lock()
	f.sentCMID = clientMessageID
	f.sentText = text
	f.sentTarget = target
	f.mu.Unlock()
	if f.enqueueFn != nil {
		return f.enqueueFn(clientMessageID, text, target)
	}
	return sideagent.MessageReceipt{MessageID: "m-1", State: sideagent.MessageStateQueued}, nil
}

func (f *fakeSideAgent) CancelQueued(messageID string) (sideagent.MessageReceipt, error) {
	f.mu.Lock()
	f.cancelledID = messageID
	f.mu.Unlock()
	if f.cancelFn != nil {
		return f.cancelFn(messageID)
	}
	return sideagent.MessageReceipt{MessageID: messageID, State: sideagent.MessageStateCancelled}, nil
}

func (f *fakeSideAgent) Stop(turnID string) error {
	if f.stopFn != nil {
		return f.stopFn(turnID)
	}
	return nil
}

func (f *fakeSideAgent) Subscribe() (<-chan sideagent.Event, func()) {
	f.record("subscribe")
	ch := f.subCh
	if ch == nil {
		ch = make(chan sideagent.Event)
	}
	return ch, func() {
		f.mu.Lock()
		f.cancelCalled = true
		f.mu.Unlock()
	}
}

func (f *fakeSideAgent) ConversationID() string { return f.convID }
func (f *fakeSideAgent) Command() string        { return f.command }
func (f *fakeSideAgent) Unavailable() bool      { return f.unavailable }

// ev — краткий конструктор события журнала для тестов.
func ev(seq uint64, typ sideagent.EventType, turnID string) sideagent.Event {
	return sideagent.Event{SchemaVersion: sideagent.SchemaVersion, Seq: seq, Type: typ, TurnID: turnID}
}

func TestSideAgent_CapabilityAbsentWhenNil(t *testing.T) {
	srv := newTestServer(t, Config{}) // no SideAgent wired
	resp := decodeStatus(t, srv)
	if resp.Capabilities.SideAgent {
		t.Error("expected capabilities.side_agent=false when service nil")
	}
	// Team keys its page-local unread cursors by run_id, so the status reports the run even with no side agent.
	if want := filepath.Base(srv.runDir); resp.RunID != want {
		t.Errorf("run_id = %q, want the run directory name %q", resp.RunID, want)
	}
	// Routes must 404 (old server unaffected).
	for _, path := range []string{"/api/side-agent", "/api/side-agent/messages", "/api/side-agent/stream"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: expected 404 when side-agent off, got %d", path, w.Code)
		}
	}
}

func TestSideAgent_CapabilityPresent(t *testing.T) {
	fake := &fakeSideAgent{convID: "run-xyz", command: "claude"}
	srv := newTestServer(t, Config{SideAgent: fake})
	resp := decodeStatus(t, srv)
	if !resp.Capabilities.SideAgent {
		t.Error("expected capabilities.side_agent=true")
	}
	if resp.RunID != "run-xyz" {
		t.Errorf("run_id = %q, want run-xyz", resp.RunID)
	}
}

func TestSideAgent_ErrorCodeMapping(t *testing.T) {
	cases := []struct {
		code       sideagent.ErrorCode
		wantStatus int
	}{
		{sideagent.ErrCodeBusy, http.StatusConflict},
		{sideagent.ErrCodeStaleTurn, http.StatusConflict},
		{sideagent.ErrCodeInvalidMessage, http.StatusBadRequest},
		{sideagent.ErrCodeMessageTooLarge, http.StatusRequestEntityTooLarge},
		{sideagent.ErrCodeContextLimit, http.StatusRequestEntityTooLarge},
		{sideagent.ErrCodeShuttingDown, http.StatusServiceUnavailable},
		{sideagent.ErrCodeStorageUnavailable, http.StatusServiceUnavailable},
		{sideagent.ErrCodeAgentError, http.StatusInternalServerError},
		{sideagent.ErrCodeClientMessageIDConflict, http.StatusConflict},
		{sideagent.ErrCodeMessageNotQueued, http.StatusConflict},
		{sideagent.ErrCodeMessageNotFound, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			fake := &fakeSideAgent{
				enqueueFn: func(_, _ string, _ sideagent.TurnTarget) (sideagent.MessageReceipt, error) {
					return sideagent.MessageReceipt{}, &sideagent.Error{Code: tc.code, Message: "boom"}
				},
			}
			srv := newTestServer(t, Config{SideAgent: fake})
			body, _ := json.Marshal(map[string]string{"client_message_id": "c1", "text": "hi"})
			req := httptest.NewRequest(http.MethodPost, "/api/side-agent/messages", bytes.NewReader(body))
			req.Header.Set("Origin", "http://"+req.Host)
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			if w.Code != tc.wantStatus {
				t.Errorf("code %s: HTTP %d, want %d (body=%s)", tc.code, w.Code, tc.wantStatus, w.Body.String())
			}
			var got map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if got["error"] != string(tc.code) {
				t.Errorf("body error = %q, want %q", got["error"], tc.code)
			}
		})
	}
}

func postMessage(t *testing.T, srv *Server, body map[string]string) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/side-agent/messages", bytes.NewReader(raw))
	req.Header.Set("Origin", "http://"+req.Host)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func TestSideAgent_SendReturnsReceiptForQueuedMessage(t *testing.T) {
	fake := &fakeSideAgent{
		history: []sideagent.Event{
			ev(1, sideagent.EventTurnAccepted, "t1"),
			ev(2, sideagent.EventTurnStarted, "t1"),
			{Seq: 3, Type: sideagent.EventMessageQueued, Data: json.RawMessage(`{"message_id":"m-1","client_message_id":"c1","message":"hello"}`)},
		},
		enqueueFn: func(string, string, sideagent.TurnTarget) (sideagent.MessageReceipt, error) {
			return sideagent.MessageReceipt{MessageID: "m-1", State: sideagent.MessageStateQueued}, nil
		},
	}
	srv := newTestServer(t, Config{SideAgent: fake})
	for i := 0; i < 2; i++ { // повтор безопасен: сервер просто возвращает квитанцию
		code, m := postMessage(t, srv, map[string]string{"client_message_id": "c1", "text": "hello"})
		if code != http.StatusAccepted {
			t.Fatalf("HTTP %d, want 202", code)
		}
		if m["message_id"] != "m-1" || m["message_state"] != "queued" {
			t.Fatalf("receipt = %v", m)
		}
		if _, has := m["turn_id"]; has {
			t.Errorf("queued message has no turn: %v", m)
		}
		if m["state"] != "running" || m["cursor"] != float64(3) {
			t.Errorf("state/cursor = %v/%v, want running/3 (conversation state, not message state)", m["state"], m["cursor"])
		}
	}
}

func TestSideAgent_SendReceiptVariants(t *testing.T) {
	idle := []sideagent.Event{
		ev(1, sideagent.EventTurnAccepted, "t1"),
		ev(2, sideagent.EventTurnStarted, "t1"),
		ev(3, sideagent.EventTurnCompleted, "t1"),
	}
	cases := []struct {
		name    string
		receipt sideagent.MessageReceipt
		want    map[string]any
	}{
		{"running turn", sideagent.MessageReceipt{MessageID: "m-1", State: sideagent.MessageStateRunning, TurnID: "t9"},
			map[string]any{"message_id": "m-1", "message_state": "running", "turn_id": "t9", "state": "idle"}},
		{"failed with code", sideagent.MessageReceipt{MessageID: "m-2", State: sideagent.MessageStateFailed, ErrorCode: sideagent.ErrCodeContextLimit},
			map[string]any{"message_id": "m-2", "message_state": "failed", "error_code": "context_limit"}},
		{"cancelled repeat", sideagent.MessageReceipt{MessageID: "m-3", State: sideagent.MessageStateCancelled},
			map[string]any{"message_id": "m-3", "message_state": "cancelled"}},
		{"legacy turn", sideagent.MessageReceipt{State: sideagent.MessageStateCompleted, TurnID: "t1"},
			map[string]any{"message_id": "", "message_state": "completed", "turn_id": "t1", "state": "idle"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeSideAgent{history: idle, enqueueFn: func(string, string, sideagent.TurnTarget) (sideagent.MessageReceipt, error) {
				return tc.receipt, nil
			}}
			code, m := postMessage(t, newTestServer(t, Config{SideAgent: fake}), map[string]string{"client_message_id": "c", "text": "x"})
			if code != http.StatusAccepted {
				t.Fatalf("HTTP %d", code)
			}
			for k, want := range tc.want {
				if m[k] != want {
					t.Errorf("%s = %v, want %v (body %v)", k, m[k], want, m)
				}
			}
		})
	}
}

func TestSideAgent_SendPassesStageContext(t *testing.T) {
	fake := &fakeSideAgent{}
	srv := newTestServer(t, Config{SideAgent: fake})
	postMessage(t, srv, map[string]string{"client_message_id": "c", "text": "x", "stage_id": "impl"})
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.sentTarget.StageID != "impl" || fake.sentTarget.SourceMessageID != "" {
		t.Fatalf("target = %+v", fake.sentTarget)
	}
}

func deleteMessage(srv *Server, id string, origin bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, "/api/side-agent/messages/"+id, nil)
	if origin {
		req.Header.Set("Origin", "http://"+req.Host)
	}
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w
}

func TestSideAgent_DeleteQueuedMessage(t *testing.T) {
	fake := &fakeSideAgent{history: []sideagent.Event{ev(7, sideagent.EventTurnCompleted, "t1")}}
	srv := newTestServer(t, Config{SideAgent: fake})
	for i := 0; i < 2; i++ { // повтор тоже 200
		w := deleteMessage(srv, "m-123-ab", true)
		if w.Code != http.StatusOK {
			t.Fatalf("HTTP %d (%s)", w.Code, w.Body.String())
		}
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		if m["message_id"] != "m-123-ab" || m["message_state"] != "cancelled" || m["cursor"] != float64(7) {
			t.Fatalf("body = %v", m)
		}
	}
	if fake.cancelledID != "m-123-ab" {
		t.Fatalf("cancelled %q", fake.cancelledID)
	}
}

func TestSideAgent_DeleteErrors(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		origin bool
		err    sideagent.ErrorCode
		status int
		code   string
	}{
		{"claimed message", "m-1", true, sideagent.ErrCodeMessageNotQueued, http.StatusConflict, "message_not_queued"},
		{"unknown message", "m-2", true, sideagent.ErrCodeMessageNotFound, http.StatusNotFound, "message_not_found"},
		{"foreign origin", "m-3", false, "", http.StatusForbidden, "forbidden_origin"},
		{"unsafe segment", "a%2Fb", true, "", http.StatusNotFound, "message_not_found"},
		{"nested path", "m-1/extra", true, "", http.StatusNotFound, ""},
		{"storage down", "m-4", true, sideagent.ErrCodeStorageUnavailable, http.StatusServiceUnavailable, "storage_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeSideAgent{cancelFn: func(string) (sideagent.MessageReceipt, error) {
				if tc.err == "" {
					t.Error("CancelQueued must not run for a rejected request")
					return sideagent.MessageReceipt{}, nil
				}
				return sideagent.MessageReceipt{}, &sideagent.Error{Code: tc.err}
			}}
			w := deleteMessage(newTestServer(t, Config{SideAgent: fake}), tc.id, tc.origin)
			if w.Code != tc.status {
				t.Fatalf("HTTP %d, want %d (%s)", w.Code, tc.status, w.Body.String())
			}
			if tc.code != "" && !strings.Contains(w.Body.String(), `"`+tc.code+`"`) {
				t.Fatalf("body %s lacks %s", w.Body.String(), tc.code)
			}
		})
	}
}

func TestSideAgent_DeleteRouteAbsentWhenCapabilityOff(t *testing.T) {
	w := deleteMessage(newTestServer(t, Config{}), "m-1", true)
	if w.Code != http.StatusNotFound {
		t.Fatalf("HTTP %d, want 404", w.Code)
	}
}

// Снимок: очередь, активный ход и источник берутся из той же копии журнала, что state и last_seq.
func TestSideAgent_SnapshotHasQueueAndActiveTurn(t *testing.T) {
	queued := func(seq uint64, id, stage string) sideagent.Event {
		return sideagent.Event{Seq: seq, Type: sideagent.EventMessageQueued,
			Data: json.RawMessage(`{"message_id":"` + id + `","client_message_id":"c-` + id + `","message":"text ` + id + `","stage_id":"` + stage + `"}`)}
	}
	fake := &fakeSideAgent{convID: "run-1", history: []sideagent.Event{
		{Seq: 1, Type: sideagent.EventTurnAccepted, TurnID: "t1", Data: json.RawMessage(`{"message":"ping","client_message_id":"mesh:x","source_message_id":"x","source_sender_id":"stage:a","source_sender_name":"Alice"}`)},
		ev(2, sideagent.EventTurnStarted, "t1"),
		queued(3, "m-1", "impl"),
		queued(4, "m-2", ""),
		{Seq: 5, Type: sideagent.EventMessageCancelled, Data: json.RawMessage(`{"message_id":"m-2"}`)},
	}}
	srv := newTestServer(t, Config{SideAgent: fake})
	req := httptest.NewRequest(http.MethodGet, "/api/side-agent?after_seq=4&limit=1", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	var m struct {
		State        string         `json:"state"`
		ActiveTurnID string         `json:"active_turn_id"`
		ActiveSource map[string]any `json:"active_source"`
		Queued       []map[string]any
		LastSeq      uint64 `json:"last_seq"`
		Events       []any  `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.State != "running" || m.ActiveTurnID != "t1" || m.LastSeq != 5 || len(m.Events) != 1 {
		t.Fatalf("snapshot = %+v", m)
	}
	if m.ActiveSource["kind"] != "mesh" || m.ActiveSource["sender_id"] != "stage:a" || m.ActiveSource["sender_name"] != "Alice" {
		t.Fatalf("active_source = %v", m.ActiveSource)
	}
	if len(m.Queued) != 1 || m.Queued[0]["message_id"] != "m-1" || m.Queued[0]["text"] != "text m-1" ||
		m.Queued[0]["stage_id"] != "impl" || m.Queued[0]["client_message_id"] != "c-m-1" || m.Queued[0]["seq"] != float64(3) {
		t.Fatalf("queued = %v", m.Queued)
	}
}

func TestSideAgent_SnapshotIdleWithQueueAndModalSource(t *testing.T) {
	fake := &fakeSideAgent{history: []sideagent.Event{
		{Seq: 1, Type: sideagent.EventMessageQueued, Data: json.RawMessage(`{"message_id":"m-1","message":"x"}`)},
	}}
	srv := newTestServer(t, Config{SideAgent: fake})
	req := httptest.NewRequest(http.MethodGet, "/api/side-agent", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	if m["state"] != "idle" || m["active_turn_id"] != "" || m["active_source"] != nil {
		t.Fatalf("idle snapshot = %v", m)
	}
	if q, _ := m["queued"].([]any); len(q) != 1 {
		t.Fatalf("idle state must not hide the queue: %v", m["queued"])
	}

	empty := newTestServer(t, Config{SideAgent: &fakeSideAgent{}})
	w = httptest.NewRecorder()
	empty.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/side-agent", nil))
	if !strings.Contains(w.Body.String(), `"queued":[]`) {
		t.Fatalf("empty queue must be [] not null: %s", w.Body.String())
	}
}

func TestSideAgent_SnapshotModalActiveSource(t *testing.T) {
	fake := &fakeSideAgent{history: []sideagent.Event{
		{Seq: 1, Type: sideagent.EventMessageQueued, Data: json.RawMessage(`{"message_id":"m-1","message":"x"}`)},
		{Seq: 2, Type: sideagent.EventTurnAccepted, TurnID: "t1", Data: json.RawMessage(`{"message_ids":["m-1"]}`)},
	}}
	w := httptest.NewRecorder()
	newTestServer(t, Config{SideAgent: fake}).Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/side-agent", nil))
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	src, _ := m["active_source"].(map[string]any)
	if m["active_turn_id"] != "t1" || src["kind"] != "modal" || len(src) != 1 {
		t.Fatalf("snapshot = %v", m)
	}
	if q, _ := m["queued"].([]any); len(q) != 0 {
		t.Fatalf("claimed message must leave the queue: %v", q)
	}
}

// Тело сверх HTTP-капа (MaxBytesReader) должно давать 413 message_too_large, а
// не 400 invalid_body: сообщение слишком велико, а не битый JSON.
func TestSideAgent_OversizeBodyIsMessageTooLarge(t *testing.T) {
	sendCalled := false
	fake := &fakeSideAgent{enqueueFn: func(_, _ string, _ sideagent.TurnTarget) (sideagent.MessageReceipt, error) {
		sendCalled = true
		return sideagent.MessageReceipt{}, nil
	}}
	srv := newTestServer(t, Config{SideAgent: fake})

	huge := strings.Repeat("x", int(maxSideAgentBodyBytes)+4096)
	body, _ := json.Marshal(map[string]string{"client_message_id": "c1", "text": huge})
	req := httptest.NewRequest(http.MethodPost, "/api/side-agent/messages", bytes.NewReader(body))
	req.Header.Set("Origin", "http://"+req.Host)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize body: got %d, want 413", w.Code)
	}
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	if m["error"] != "message_too_large" {
		t.Errorf("error code = %v, want message_too_large", m["error"])
	}
	if sendCalled {
		t.Error("Enqueue не должен вызываться для тела сверх капа")
	}
}

func TestDashboard_ListenAddrMatrix(t *testing.T) {
	cases := []struct {
		name        string
		inContainer bool
		want        string
	}{
		// The dashboard serves unauthenticated mutating stage/flow APIs, so on a
		// native host it ALWAYS binds loopback — independent of the side-agent.
		{"native host", false, "127.0.0.1:8900"},
		// In-container it must be 0.0.0.0 so the host `docker -p` can reach it;
		// the host side publishes to 127.0.0.1 (see launcher).
		{"in container", true, ":8900"},
	}
	for _, tc := range cases {
		if got := dashboardListenAddr(8900, tc.inContainer); got != tc.want {
			t.Errorf("%s: dashboardListenAddr(8900,%v) = %q, want %q", tc.name, tc.inContainer, got, tc.want)
		}
	}
}

func TestSideAgent_ForeignOriginRejected(t *testing.T) {
	fake := &fakeSideAgent{}
	srv := newTestServer(t, Config{SideAgent: fake})
	body, _ := json.Marshal(map[string]string{"client_message_id": "c1", "text": "hi"})

	// Foreign Origin.
	req := httptest.NewRequest(http.MethodPost, "/api/side-agent/messages", bytes.NewReader(body))
	req.Header.Set("Origin", "http://evil.example.com")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("foreign origin: HTTP %d, want 403", w.Code)
	}

	// Missing Origin on POST is also rejected (defence-in-depth).
	req2 := httptest.NewRequest(http.MethodPost, "/api/side-agent/messages", bytes.NewReader(body))
	w2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w2, req2)
	if w2.Code != http.StatusForbidden {
		t.Errorf("missing origin: HTTP %d, want 403", w2.Code)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.sentText != "" {
		t.Error("Send must not run for a rejected-origin request")
	}
}

func TestSideAgent_RejectsReservedMeshClientMessageID(t *testing.T) {
	fake := &fakeSideAgent{}
	srv := newTestServer(t, Config{SideAgent: fake})
	body, _ := json.Marshal(map[string]string{"client_message_id": "mesh:preclaim", "text": "same body"})
	req := httptest.NewRequest(http.MethodPost, "/api/side-agent/messages", bytes.NewReader(body))
	req.Header.Set("Origin", "http://"+req.Host)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("reserved id: HTTP %d, want 400", w.Code)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.sentCMID != "" {
		t.Fatalf("reserved id reached Manager: %q", fake.sentCMID)
	}
}

func TestSideAgent_IgnoresCommandArgsCwd(t *testing.T) {
	fake := &fakeSideAgent{}
	srv := newTestServer(t, Config{SideAgent: fake})
	// Body carries extra privileged-looking fields the handler must NOT read.
	body, _ := json.Marshal(map[string]any{
		"client_message_id": "c1",
		"text":              "legit text",
		"command":           "rm",
		"args":              []string{"-rf", "/"},
		"cwd":               "/etc",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/side-agent/messages", bytes.NewReader(body))
	req.Header.Set("Origin", "http://"+req.Host)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("HTTP %d, want 202 (body=%s)", w.Code, w.Body.String())
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.sentText != "legit text" || fake.sentCMID != "c1" {
		t.Errorf("Send got (cmid=%q text=%q); the client must control only id/text", fake.sentCMID, fake.sentText)
	}
}

func TestSideAgent_StatePaginationCursor(t *testing.T) {
	fake := &fakeSideAgent{
		convID:  "run-1",
		command: "claude",
		history: []sideagent.Event{
			ev(1, sideagent.EventTurnAccepted, "t1"),
			ev(2, sideagent.EventTurnStarted, "t1"),
			ev(3, sideagent.EventAssistantText, "t1"),
			ev(4, sideagent.EventTurnCompleted, "t1"),
		},
	}
	srv := newTestServer(t, Config{SideAgent: fake})

	get := func(query string) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/side-agent"+query, nil)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: HTTP %d", query, w.Code)
		}
		var m map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return m
	}

	// Page from the start with a limit of 2: cursor advances to seq 2, not to
	// the global last_seq (4); has_more true.
	m := get("?after_seq=0&limit=2")
	if int(m["next_cursor"].(float64)) != 2 {
		t.Errorf("next_cursor = %v, want 2", m["next_cursor"])
	}
	if int(m["last_seq"].(float64)) != 4 {
		t.Errorf("last_seq = %v, want 4", m["last_seq"])
	}
	if hasMore, ok := m["has_more"].(bool); !ok || !hasMore {
		t.Errorf("has_more = %v, want true", m["has_more"])
	}
	if events, _ := m["events"].([]any); len(events) != 2 {
		t.Errorf("events len = %d, want 2", len(events))
	}

	// Resume from the returned cursor: get the rest, has_more false, idle state.
	m2 := get("?after_seq=2")
	if int(m2["next_cursor"].(float64)) != 4 {
		t.Errorf("next_cursor = %v, want 4", m2["next_cursor"])
	}
	if hasMore, ok := m2["has_more"].(bool); !ok || hasMore {
		t.Errorf("has_more = %v, want false", m2["has_more"])
	}
	if m2["state"] != "idle" {
		t.Errorf("state = %v, want idle (last turn completed)", m2["state"])
	}
	if m2["run_id"] != "run-1" {
		t.Errorf("run_id = %v, want run-1", m2["run_id"])
	}
}
