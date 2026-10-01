package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/sideagent"
)

// fakeSideAgent — тест-дабл SideAgentService: все методы подменяемы через
// поля-замыкания; каждый вызов Subscribe/History протоколируется в callOrder,
// а аргументы Send фиксируются для проверки «клиент не управляет командой».
type fakeSideAgent struct {
	mu sync.Mutex

	history     []sideagent.Event
	command     string
	convID      string
	unavailable bool

	sendFn func(clientMessageID, text string) (string, error)
	stopFn func(turnID string) error

	// subCh — канал, который возвращает Subscribe. Тест заполняет/закрывает его,
	// чтобы смоделировать live-поток и overflow-drop.
	subCh chan sideagent.Event

	callOrder    []string
	cancelCalled bool
	sentCMID     string
	sentText     string
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

func (f *fakeSideAgent) Send(clientMessageID, text string) (string, error) {
	f.mu.Lock()
	f.sentCMID = clientMessageID
	f.sentText = text
	f.mu.Unlock()
	if f.sendFn != nil {
		return f.sendFn(clientMessageID, text)
	}
	return "t-1", nil
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
	if resp.RunID != "" {
		t.Errorf("expected empty run_id, got %q", resp.RunID)
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
	}
	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			fake := &fakeSideAgent{
				sendFn: func(_, _ string) (string, error) {
					return "", &sideagent.Error{Code: tc.code, Message: "boom"}
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

func TestSideAgent_SendSuccessAndIdempotent(t *testing.T) {
	// Fake returns the same turn id for a repeated client_message_id — handler
	// just forwards it; a repeat POST is safe and yields the same turn.
	fake := &fakeSideAgent{
		sendFn: func(cmid, _ string) (string, error) { return "turn-for-" + cmid, nil },
	}
	srv := newTestServer(t, Config{SideAgent: fake})
	post := func() (int, map[string]any) {
		body, _ := json.Marshal(map[string]string{"client_message_id": "c1", "text": "hello"})
		req := httptest.NewRequest(http.MethodPost, "/api/side-agent/messages", bytes.NewReader(body))
		req.Header.Set("Origin", "http://"+req.Host)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return w.Code, m
	}
	code1, m1 := post()
	code2, m2 := post()
	if code1 != http.StatusAccepted || code2 != http.StatusAccepted {
		t.Fatalf("expected 202 on both, got %d and %d", code1, code2)
	}
	if m1["turn_id"] != "turn-for-c1" || m2["turn_id"] != "turn-for-c1" {
		t.Errorf("turn_id mismatch: %v vs %v", m1["turn_id"], m2["turn_id"])
	}
}

// Идемпотентный повтор уже ЗАВЕРШЁННОГО хода должен отдавать state:"idle" (не
// хардкод "running"): агент не запускался, ход неактивен — клиент не должен
// считать его активным. Свежий send того же хендлера — "running".
func TestSideAgent_SendStateReflectsReturnedTurn(t *testing.T) {
	ev := func(typ sideagent.EventType, turnID string, seq uint64) sideagent.Event {
		return sideagent.Event{Type: typ, TurnID: turnID, Seq: seq}
	}
	post := func(fake *fakeSideAgent) string {
		srv := newTestServer(t, Config{SideAgent: fake})
		body, _ := json.Marshal(map[string]string{"client_message_id": "c1", "text": "hi"})
		req := httptest.NewRequest(http.MethodPost, "/api/side-agent/messages", bytes.NewReader(body))
		req.Header.Set("Origin", "http://"+req.Host)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("got %d, want 202", w.Code)
		}
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return m["state"].(string)
	}

	// Завершённый ход → idle.
	completed := &fakeSideAgent{
		history: []sideagent.Event{
			ev(sideagent.EventTurnAccepted, "t1", 1),
			ev(sideagent.EventTurnStarted, "t1", 2),
			ev(sideagent.EventTurnCompleted, "t1", 3),
		},
		sendFn: func(_, _ string) (string, error) { return "t1", nil },
	}
	if got := post(completed); got != "idle" {
		t.Errorf("повтор завершённого хода: state=%q, want idle", got)
	}

	// Активный ход → running.
	running := &fakeSideAgent{
		history: []sideagent.Event{
			ev(sideagent.EventTurnAccepted, "t2", 1),
			ev(sideagent.EventTurnStarted, "t2", 2),
		},
		sendFn: func(_, _ string) (string, error) { return "t2", nil },
	}
	if got := post(running); got != "running" {
		t.Errorf("активный ход: state=%q, want running", got)
	}
}

// Тело сверх HTTP-капа (MaxBytesReader) должно давать 413 message_too_large, а
// не 400 invalid_body: сообщение слишком велико, а не битый JSON.
func TestSideAgent_OversizeBodyIsMessageTooLarge(t *testing.T) {
	sendCalled := false
	fake := &fakeSideAgent{sendFn: func(_, _ string) (string, error) { sendCalled = true; return "t", nil }}
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
		t.Error("Send не должен вызываться для тела сверх капа")
	}
}

func TestSideAgent_ListenAddrMatrix(t *testing.T) {
	cases := []struct {
		enabled, inContainer bool
		want                 string
	}{
		{true, false, "127.0.0.1:8900"}, // native host: loopback only
		{true, true, ":8900"},           // in-container: must be 0.0.0.0 for docker -p
		{false, false, ":8900"},         // side-agent off: preserve today's behavior
		{false, true, ":8900"},
	}
	for _, tc := range cases {
		if got := sideAgentListenAddr(8900, tc.enabled, tc.inContainer); got != tc.want {
			t.Errorf("sideAgentListenAddr(8900,%v,%v) = %q, want %q", tc.enabled, tc.inContainer, got, tc.want)
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
