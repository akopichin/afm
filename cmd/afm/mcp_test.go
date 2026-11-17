package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm/contracts/state"
	"github.com/akopichin/afm/pkg/mcpserver"
)

const testStatusURI = "afm://runs/run-1/status"

// bridgeBackend is an MCP endpoint with one tool and one subscribable resource; it records what the bridge
// sends and can pretend that the session vanished.
type bridgeBackend struct {
	server *sdkmcp.Server
	http   *httptest.Server

	mu       sync.Mutex
	versions []string // MCP-Protocol-Version of every POST after initialize
	deletes  int
	gone     atomic.Bool

	parked  chan struct{} // receives one value when afm_park is waiting on the server
	release chan struct{} // closed on cleanup so a parked call never outlives the test
}

func newBridgeBackend(t *testing.T) *bridgeBackend {
	t.Helper()
	b := &bridgeBackend{parked: make(chan struct{}, 4), release: make(chan struct{})}
	b.server = sdkmcp.NewServer(&sdkmcp.Implementation{Name: "fake-afm", Version: "0"}, &sdkmcp.ServerOptions{
		SubscribeHandler:   func(context.Context, *sdkmcp.SubscribeRequest) error { return nil },
		UnsubscribeHandler: func(context.Context, *sdkmcp.UnsubscribeRequest) error { return nil },
	})
	type echoIn struct {
		Text string `json:"text"`
	}
	sdkmcp.AddTool(b.server, &sdkmcp.Tool{Name: "afm_echo", Description: "echo"}, func(_ context.Context, _ *sdkmcp.CallToolRequest, in echoIn) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "echo:" + in.Text}}}, nil, nil
	})
	sdkmcp.AddTool(b.server, &sdkmcp.Tool{Name: "afm_park", Description: "blocks like a long afm_run_wait"}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, _ struct{}) (*sdkmcp.CallToolResult, any, error) {
		b.parked <- struct{}{}
		select {
		case <-ctx.Done():
		case <-b.release:
		}
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "released"}}}, nil, nil
	})
	b.server.AddResource(&sdkmcp.Resource{URI: testStatusURI, Name: "status"}, func(context.Context, *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
		return &sdkmcp.ReadResourceResult{Contents: []*sdkmcp.ResourceContents{{URI: testStatusURI, Text: "ok"}}}, nil
	})
	streamable := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return b.server }, nil)
	b.http = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b.gone.Load() && r.Header.Get("Mcp-Session-Id") != "" && r.Method != http.MethodDelete {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		b.record(r)
		streamable.ServeHTTP(w, r)
	}))
	t.Cleanup(b.http.Close)
	t.Cleanup(func() { close(b.release) })
	return b
}

func (b *bridgeBackend) record(r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch r.Method {
	case http.MethodDelete:
		b.deletes++
	case http.MethodPost:
		if r.Header.Get("Mcp-Session-Id") != "" {
			b.versions = append(b.versions, r.Header.Get("MCP-Protocol-Version"))
		}
	default:
		// Other methods (the notification GET) are not recorded.
	}
}

func (b *bridgeBackend) endpoint() string { return b.http.URL + "/mcp" }

func (b *bridgeBackend) recorded() (versions []string, deletes int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.versions), b.deletes
}

// recordingWriter keeps everything the bridge wrote to stdout.
type recordingWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
	w   io.WriteCloser
}

func (r *recordingWriter) Write(p []byte) (int, error) {
	r.mu.Lock()
	r.buf.Write(p)
	r.mu.Unlock()
	return r.w.Write(p)
}

func (r *recordingWriter) Close() error { return r.w.Close() }

func (r *recordingWriter) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// bridgeRun is one bridge between an in-process MCP client (over io.Pipe) and an endpoint.
type bridgeRun struct {
	session *sdkmcp.ClientSession
	stdout  *recordingWriter
	stderr  *lockedBuffer
	done    chan struct{} // closed when serveMCPBridge returned; err is then set
	err     error
	updates chan string
	stdin   *io.PipeWriter
}

// openBridge returns the client's connect error instead of failing the test.
func openBridge(t *testing.T, endpoint string) (*bridgeRun, error) {
	t.Helper()
	return openBridgeVia(t, endpoint, http.DefaultTransport)
}

// openBridgeVia is openBridge whose HTTP requests go through base.
func openBridgeVia(t *testing.T, endpoint string, base http.RoundTripper) (*bridgeRun, error) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	r := &bridgeRun{stdout: &recordingWriter{w: outW}, stderr: &lockedBuffer{}, done: make(chan struct{}), updates: make(chan string, 16), stdin: inW}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(r.done)
		r.err = serveMCPBridge(ctx, endpoint, base, inR, r.stdout, r.stderr)
	}()
	t.Cleanup(func() {
		cancel()
		_ = inW.Close()
		select {
		case <-r.done:
		case <-time.After(10 * time.Second):
			t.Error("bridge did not stop after cleanup")
		}
	})
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "stdio-client", Version: "0"}, &sdkmcp.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, req *sdkmcp.ResourceUpdatedNotificationRequest) {
			select {
			case r.updates <- req.Params.URI:
			default:
			}
		},
	})
	connectCtx, stop := context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	session, err := client.Connect(connectCtx, &sdkmcp.IOTransport{Reader: outR, Writer: inW}, nil)
	r.session = session
	return r, err
}

func startBridge(t *testing.T, endpoint string) *bridgeRun {
	t.Helper()
	r, err := openBridge(t, endpoint)
	if err != nil {
		t.Fatalf("connect through the bridge: %v\nstderr: %s", err, r.stderr.String())
	}
	return r
}

func toolNames(t *testing.T, session *sdkmcp.ClientSession) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var names []string
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("list tools: %v", err)
		}
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

func directToolNames(t *testing.T, endpoint string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "direct", Version: "0"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatalf("direct connect: %v", err)
	}
	defer session.Close() //nolint:errcheck
	return toolNames(t, session)
}

func TestMCPServeForwardsToolsAndProtocolVersion(t *testing.T) {
	backend := newBridgeBackend(t)
	bridge := startBridge(t, backend.endpoint())

	if got, want := toolNames(t, bridge.session), directToolNames(t, backend.endpoint()); !slices.Equal(got, want) || len(got) == 0 {
		t.Fatalf("tools through the bridge = %v, direct = %v", got, want)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := bridge.session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "afm_echo", Arguments: map[string]any{"text": "hi"}})
	if err != nil || len(res.Content) != 1 {
		t.Fatalf("call tool: %v %+v", err, res)
	}
	if text, _ := res.Content[0].(*sdkmcp.TextContent); text == nil || text.Text != "echo:hi" {
		t.Errorf("tool result = %+v, want echo:hi", res.Content[0])
	}

	versions, _ := backend.recorded()
	if len(versions) == 0 {
		t.Fatal("no request after initialize reached the backend")
	}
	for i, v := range versions {
		if v == "" {
			t.Errorf("request %d after initialize carried no MCP-Protocol-Version (all: %v)", i, versions)
		}
	}
}

func TestMCPServeKeepsStdoutProtocolOnly(t *testing.T) {
	backend := newBridgeBackend(t)
	bridge := startBridge(t, backend.endpoint())
	toolNames(t, bridge.session)

	lines := strings.Split(strings.TrimSpace(bridge.stdout.text()), "\n")
	if len(lines) < 2 {
		t.Fatalf("stdout = %q, want JSON-RPC lines", bridge.stdout.text())
	}
	for _, line := range lines {
		var msg struct {
			JSONRPC string `json:"jsonrpc"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil || msg.JSONRPC != "2.0" {
			t.Errorf("stdout line is not a JSON-RPC message: %q", line)
		}
	}
}

func TestMCPServeDeliversResourceUpdates(t *testing.T) {
	backend := newBridgeBackend(t)
	bridge := startBridge(t, backend.endpoint())

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := bridge.session.Subscribe(ctx, &sdkmcp.SubscribeParams{URI: testStatusURI}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	// The bridge opens its notification stream right after initialized; repeat until it is attached.
	waitFor(t, "a resources/updated notification on stdio", func() bool {
		_ = backend.server.ResourceUpdated(ctx, &sdkmcp.ResourceUpdatedNotificationParams{URI: testStatusURI})
		select {
		case uri := <-bridge.updates:
			return uri == testStatusURI
		case <-time.After(50 * time.Millisecond):
			return false
		}
	})
}

func TestMCPServeStdinEOFDeletesSession(t *testing.T) {
	backend := newBridgeBackend(t)
	bridge := startBridge(t, backend.endpoint())
	toolNames(t, bridge.session)

	if err := bridge.session.Close(); err != nil {
		t.Logf("client close: %v", err)
	}
	select {
	case <-bridge.done:
		if bridge.err != nil {
			t.Fatalf("bridge after stdin EOF = %v, want nil (exit 0)", bridge.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bridge did not stop after stdin EOF")
	}
	if _, deletes := backend.recorded(); deletes != 1 {
		t.Errorf("DELETE requests = %d, want 1", deletes)
	}
}

func TestMCPServeSessionGoneFailsClearly(t *testing.T) {
	backend := newBridgeBackend(t)
	bridge := startBridge(t, backend.endpoint())
	toolNames(t, bridge.session)

	backend.gone.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, callErr := bridge.session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "afm_echo", Arguments: map[string]any{"text": "x"}})
	if callErr == nil {
		t.Error("tool call succeeded on a vanished session")
	}
	select {
	case <-bridge.done:
		if bridge.err == nil || !strings.Contains(bridge.err.Error(), "MCP session is gone") {
			t.Fatalf("bridge error = %v, want the session-gone message", bridge.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bridge kept running after the session vanished")
	}
}

func TestMCPServeMCPDisabledFailsClearly(t *testing.T) {
	disabled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"mcp_disabled"}`))
	}))
	t.Cleanup(disabled.Close)

	bridge, err := openBridge(t, disabled.URL+"/mcp")
	if err == nil {
		t.Fatal("client connected through a bridge to a disabled endpoint")
	}
	select {
	case <-bridge.done:
		if bridge.err == nil || !strings.Contains(bridge.err.Error(), "--mcp-off") {
			t.Errorf("bridge error = %v, want the mcp_disabled hint", bridge.err)
		}
		if out := bridge.stdout.text(); out != "" {
			t.Errorf("stdout = %q, want it empty so the client sees EOF", out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bridge kept running against a disabled endpoint")
	}
}

// Stdin EOF must end the session even while a call is parked on the server, and other messages keep flowing.
func TestMCPServeStdinEOFWithParkedCallDeletesSession(t *testing.T) {
	backend := newBridgeBackend(t)
	bridge := startBridge(t, backend.endpoint())
	toolNames(t, bridge.session)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _, _ = bridge.session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "afm_park"}) }()
	<-backend.parked

	res, err := bridge.session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "afm_echo", Arguments: map[string]any{"text": "while parked"}})
	if err != nil || len(res.Content) == 0 {
		t.Fatalf("call behind a parked call: %v", err)
	}
	_ = bridge.stdin.Close()
	select {
	case <-bridge.done:
		if bridge.err != nil {
			t.Fatalf("bridge after stdin EOF = %v, want nil (exit 0)", bridge.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stdin EOF did not end the bridge while a call was parked")
	}
	if _, deletes := backend.recorded(); deletes != 1 {
		t.Errorf("DELETE requests = %d, want 1", deletes)
	}
}

// A redirect away from the loopback endpoint must not receive the session's JSON-RPC bodies.
func TestMCPServeRefusesRedirects(t *testing.T) {
	var reached atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Add(1) }))
	t.Cleanup(target.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/mcp", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)

	bridge, err := openBridge(t, redirector.URL+"/mcp")
	if err == nil {
		t.Fatal("client connected through a bridge that was redirected")
	}
	select {
	case <-bridge.done:
	case <-time.After(10 * time.Second):
		t.Fatal("bridge kept running after a redirect")
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("redirect destination received %d requests, want 0", n)
	}
}

type staticReader struct{}

func (staticReader) Snapshot() state.RunState             { return state.RunState{} }
func (staticReader) History() ([]state.Transition, error) { return nil, nil }

// The real service behind the bridge lists the same tools as over plain HTTP.
func TestMCPServeListsRealServiceTools(t *testing.T) {
	svc, err := mcpserver.New(mcpserver.Config{RunID: "run-1", Reader: staticReader{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	srv := httptest.NewServer(svc)
	t.Cleanup(srv.Close)

	bridge := startBridge(t, srv.URL)
	if got, want := toolNames(t, bridge.session), directToolNames(t, srv.URL); !slices.Equal(got, want) {
		t.Fatalf("tools through the bridge = %v, direct = %v", got, want)
	}
}

func TestParseMCPConnect(t *testing.T) {
	tests := []struct {
		in, want, wantErr string
	}{
		{in: "http://127.0.0.1:8080", want: "http://127.0.0.1:8080/mcp"},
		{in: "http://localhost:8080/", want: "http://localhost:8080/mcp"},
		{in: "http://[::1]:8080/mcp", want: "http://[::1]:8080/mcp"},
		{in: "https://127.0.0.1:8080", wantErr: "loopback http URL"},
		{in: "http://192.168.1.5:8080", wantErr: "only loopback hosts"},
		{in: "http://example.com", wantErr: "only loopback hosts"},
		{in: "127.0.0.1:8080", wantErr: "loopback http URL"},
	}
	for _, tt := range tests {
		got, err := parseMCPConnect(tt.in)
		switch {
		case tt.wantErr != "":
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("parseMCPConnect(%q) error = %v, want %q", tt.in, err, tt.wantErr)
			}
		case err != nil || got != tt.want:
			t.Errorf("parseMCPConnect(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		default:
			// Accepted and normalized as expected.
		}
	}
}

// bridgeGuard bounds waits for events that must happen; reaching it is the failure, not the evidence.
const bridgeGuard = 10 * time.Second

func within(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(bridgeGuard):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// holdingTransport holds the POST of one tool call before it is dispatched and reports whether its request was
// cancelled by then; it also reports when a cancellation notification has been delivered.
type holdingTransport struct {
	base       http.RoundTripper
	tool       string
	held       chan struct{}
	release    chan struct{}
	decided    chan bool
	cancelSent chan struct{}
	once       sync.Once
}

func newHoldingTransport(tool string) *holdingTransport {
	return &holdingTransport{
		base: http.DefaultTransport, tool: tool, held: make(chan struct{}), release: make(chan struct{}),
		decided: make(chan bool, 1), cancelSent: make(chan struct{}),
	}
}

func (h *holdingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	if r.Method == http.MethodPost && bytes.Contains(body, []byte(`"`+h.tool+`"`)) {
		close(h.held)
		select {
		case <-h.release:
		case <-r.Context().Done():
		}
		cancelled := r.Context().Err() != nil
		h.decided <- cancelled
		if cancelled {
			return nil, r.Context().Err()
		}
	}
	resp, err := h.base.RoundTrip(r)
	if bytes.Contains(body, []byte(mcpMethodCancelled)) {
		h.once.Do(func() { close(h.cancelSent) })
	}
	return resp, err
}

// parkTool adds a tool that reports its start and then waits for cancellation or the end of the test.
func (b *bridgeBackend) parkTool(name string) (entered, cancelled chan struct{}) {
	entered, cancelled = make(chan struct{}), make(chan struct{})
	sdkmcp.AddTool(b.server, &sdkmcp.Tool{Name: name, Description: "parks"}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, _ struct{}) (*sdkmcp.CallToolResult, any, error) {
		close(entered)
		select {
		case <-ctx.Done():
			close(cancelled)
		case <-b.release:
		}
		return &sdkmcp.CallToolResult{}, nil, nil
	})
	return entered, cancelled
}

// A call cancelled while it still waits to be sent is never sent: the server has already consumed the
// cancellation by then and would ignore it for a call it does not know.
func TestMCPServeCancelledCallIsNotSentLater(t *testing.T) {
	backend := newBridgeBackend(t)
	entered, _ := backend.parkTool("queued_park")
	hold := newHoldingTransport("queued_park")
	bridge, err := openBridgeVia(t, backend.endpoint(), hold)
	if err != nil {
		t.Fatalf("connect through the bridge: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	callDone := make(chan error, 1)
	go func() {
		_, err := bridge.session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "queued_park"})
		callDone <- err
	}()
	within(t, "the call to be held before dispatch", hold.held)

	cancel()
	within(t, "the cancellation to reach the server", hold.cancelSent)
	// An unrelated round trip after the cancellation: the server has processed it.
	if err := bridge.session.Ping(t.Context(), nil); err != nil {
		t.Fatalf("ping: %v", err)
	}
	close(hold.release)
	if cancelled := <-hold.decided; !cancelled {
		t.Fatal("the call was sent after its cancellation had reached the server")
	}
	select {
	case <-entered:
		t.Fatal("the server ran a cancelled call")
	default:
	}
	if err := <-callDone; err == nil {
		t.Error("the cancelled call reported success")
	}

	res, err := bridge.session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "afm_echo", Arguments: map[string]any{"text": "alive"}})
	if err != nil || res.IsError {
		t.Fatalf("call after a cancellation: %v %+v", err, res)
	}
	_ = bridge.stdin.Close()
	within(t, "the bridge to stop after stdin EOF", bridge.done)
	if bridge.err != nil {
		t.Fatalf("bridge after stdin EOF = %v, want nil", bridge.err)
	}
	if _, deletes := backend.recorded(); deletes != 1 {
		t.Errorf("DELETE requests = %d, want 1", deletes)
	}
}

// A call the server already runs is cancelled there by the forwarded notification; the session stays usable.
func TestMCPServeCancelsAdmittedCall(t *testing.T) {
	backend := newBridgeBackend(t)
	entered, cancelled := backend.parkTool("active_park")
	bridge := startBridge(t, backend.endpoint())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	callDone := make(chan error, 1)
	go func() {
		_, err := bridge.session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "active_park"})
		callDone <- err
	}()
	within(t, "the call to start on the server", entered)

	cancel()
	within(t, "the server to cancel the call", cancelled)
	if err := <-callDone; err == nil {
		t.Error("the cancelled call reported success")
	}
	res, err := bridge.session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "afm_echo", Arguments: map[string]any{"text": "alive"}})
	if err != nil || res.IsError {
		t.Fatalf("call after a cancellation: %v %+v", err, res)
	}
}

// scriptConn is an sdkmcp.Connection that reads a fixed script and records what is written to it.
type scriptConn struct {
	mu       sync.Mutex
	incoming []jsonrpc.Message
	written  []jsonrpc.Message
	ctxs     map[jsonrpc.ID]context.Context
	write    func(ctx context.Context, msg jsonrpc.Message) error
}

func (c *scriptConn) Read(context.Context) (jsonrpc.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.incoming) == 0 {
		return nil, io.EOF
	}
	msg := c.incoming[0]
	c.incoming = c.incoming[1:]
	return msg, nil
}

func (c *scriptConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	c.mu.Lock()
	c.written = append(c.written, msg)
	if req, ok := msg.(*jsonrpc.Request); ok && req.IsCall() {
		if c.ctxs == nil {
			c.ctxs = map[jsonrpc.ID]context.Context{}
		}
		c.ctxs[req.ID] = ctx
	}
	write := c.write
	c.mu.Unlock()
	if write != nil {
		return write(ctx, msg)
	}
	return nil
}

func (c *scriptConn) Close() error      { return nil }
func (c *scriptConn) SessionID() string { return "session" }

func (c *scriptConn) script(msgs ...jsonrpc.Message) {
	c.mu.Lock()
	c.incoming = append(c.incoming, msgs...)
	c.mu.Unlock()
}

func (c *scriptConn) sent() []jsonrpc.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.written)
}

func (c *scriptConn) ctxOf(id jsonrpc.ID) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ctxs[id]
}

func mustID(t *testing.T, v any) jsonrpc.ID {
	t.Helper()
	id, err := jsonrpc.MakeID(v)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func callMsg(id jsonrpc.ID) *jsonrpc.Request { return &jsonrpc.Request{ID: id, Method: "tools/call"} }

func cancelMsg(t *testing.T, requestID any) *jsonrpc.Request {
	t.Helper()
	params, err := json.Marshal(map[string]any{"requestId": requestID})
	if err != nil {
		t.Fatal(err)
	}
	return &jsonrpc.Request{Method: mcpMethodCancelled, Params: params}
}

// scriptBridge is a bridge between two script connections; it fails the test on any bridge failure.
func scriptBridge(t *testing.T) (*mcpBridge, *scriptConn, *scriptConn) {
	t.Helper()
	local, remote := &scriptConn{}, &scriptConn{}
	b := &mcpBridge{stdio: local, remote: remote, fail: func(err error) { t.Errorf("bridge failure: %v", err) }}
	return b, local, remote
}

// pump runs the stdin side over the scripted messages and waits for every forward.
func pump(ctx context.Context, t *testing.T, b *mcpBridge) {
	t.Helper()
	if err := b.toServer(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("toServer = %v, want EOF", err)
	}
	b.forwards.Wait()
}

func (b *mcpBridge) pending(id jsonrpc.ID) *bridgeCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls[id]
}

// Answer headers do not end a call: it stays registered, with a live exchange, until its response arrives;
// a refused call is answered locally and forgotten; stopping the bridge ends every exchange.
func TestMCPBridgeCallRegistryLifetime(t *testing.T) {
	t.Run("answered", func(t *testing.T) {
		b, local, remote := scriptBridge(t)
		id := mustID(t, "call-A")
		local.script(callMsg(id))
		parent, stop := context.WithCancel(t.Context())
		defer stop()
		pump(parent, t, b)
		if b.pending(id) == nil || remote.ctxOf(id).Err() != nil {
			t.Fatal("a call whose answer is pending was retired")
		}
		remote.script(&jsonrpc.Response{ID: id})
		if err := b.fromServer(parent); !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
		if b.pending(id) != nil || remote.ctxOf(id).Err() == nil {
			t.Fatal("the response did not retire the call")
		}
	})
	t.Run("refused", func(t *testing.T) {
		b, local, remote := scriptBridge(t)
		remote.write = func(context.Context, jsonrpc.Message) error { return errors.New("refused") }
		id := mustID(t, 3.0)
		local.script(callMsg(id))
		pump(t.Context(), t, b)
		out := local.sent()
		if b.pending(id) != nil || len(out) != 1 {
			t.Fatalf("pending %v, stdio %v", b.pending(id), out)
		}
		if resp, ok := out[0].(*jsonrpc.Response); !ok || resp.ID != id || resp.Error == nil {
			t.Fatalf("stdio got %+v, want the call's refusal", out[0])
		}
	})
	t.Run("bridge stops", func(t *testing.T) {
		b, local, remote := scriptBridge(t)
		id := mustID(t, 4.0)
		local.script(callMsg(id))
		parent, stop := context.WithCancel(t.Context())
		pump(parent, t, b)
		stop()
		if remote.ctxOf(id).Err() == nil {
			t.Fatal("a pending exchange outlived the bridge")
		}
	})
}

// A cancellation names one call by its typed ID: string "1" is not numeric 1, an unknown or repeated
// cancellation changes nothing, and every notification still reaches the server.
func TestMCPBridgeCancellationMatchesTypedID(t *testing.T) {
	b, local, remote := scriptBridge(t)
	numeric, text := mustID(t, 1.0), mustID(t, "1")
	local.script(callMsg(numeric), callMsg(text), cancelMsg(t, "1"), cancelMsg(t, 99), cancelMsg(t, "1"))
	pump(t.Context(), t, b)

	if b.pending(numeric) == nil || remote.ctxOf(numeric).Err() != nil {
		t.Fatal("cancelling string id \"1\" ended numeric id 1")
	}
	// The cancelled call keeps its ID until its answer, but its exchange has stopped.
	if b.pending(text) == nil || remote.ctxOf(text).Err() == nil {
		t.Fatal("the cancelled call gave up its ID or is still live")
	}
	notifications := 0
	for _, msg := range remote.sent() {
		if req, ok := msg.(*jsonrpc.Request); ok && req.Method == mcpMethodCancelled {
			notifications++
		}
	}
	if notifications != 3 {
		t.Fatalf("forwarded %d cancellations, want 3", notifications)
	}
	if out := local.sent(); len(out) != 0 {
		t.Fatalf("a cancellation was answered on stdio: %v", out)
	}
}

// A second call with the ID of a pending one is refused locally and never sent; the first keeps the ID until its
// answer, after which the ID may be used again.
func TestMCPBridgeDuplicateIDIsRefusedLocally(t *testing.T) {
	b, local, remote := scriptBridge(t)
	id := mustID(t, 7.0)
	local.script(callMsg(id), callMsg(id))
	pump(t.Context(), t, b)
	first := b.pending(id)
	if first == nil || len(remote.sent()) != 1 {
		t.Fatalf("pending %v, sent %v", first, remote.sent())
	}
	out := local.sent()
	var wire *jsonrpc.Error
	if len(out) != 1 || !errors.As(out[0].(*jsonrpc.Response).Error, &wire) || wire.Code != jsonrpc.CodeInvalidRequest {
		t.Fatalf("stdio = %+v, want an invalid-request answer for the duplicate", out)
	}

	remote.script(&jsonrpc.Response{ID: id})
	if err := b.fromServer(t.Context()); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	local.script(callMsg(id))
	pump(t.Context(), t, b)
	if again := b.pending(id); again == nil || again == first || len(remote.sent()) != 2 {
		t.Fatal("the ID could not be used again after its answer")
	}
}

// A response retires its call whether or not the call was cancelled, a cancellation after the response finds
// nothing to do, and an old exchange ending late never retires a newer call that reused its ID.
func TestMCPBridgeResponseAndCancelRace(t *testing.T) {
	b, local, remote := scriptBridge(t)
	id := mustID(t, 5.0)
	local.script(callMsg(id))
	pump(t.Context(), t, b)
	old := b.pending(id)

	// Cancellation first, then the late response.
	local.script(cancelMsg(t, 5))
	pump(t.Context(), t, b)
	remote.script(&jsonrpc.Response{ID: id})
	if err := b.fromServer(t.Context()); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	// Response first, then the late cancellation, for a new call with the same ID.
	local.script(callMsg(id))
	pump(t.Context(), t, b)
	newer := b.pending(id)
	if newer == nil || newer == old {
		t.Fatal("the reused ID was not registered")
	}
	b.retire(old)
	if b.pending(id) != newer {
		t.Fatal("the old exchange retired the newer call")
	}
	remote.script(&jsonrpc.Response{ID: id})
	if err := b.fromServer(t.Context()); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	local.script(cancelMsg(t, 5))
	pump(t.Context(), t, b)
	if b.pending(id) != nil {
		t.Fatal("a call is still registered")
	}
}

func callsWithID(msgs []jsonrpc.Message, id jsonrpc.ID) int {
	n := 0
	for _, msg := range msgs {
		if req, ok := msg.(*jsonrpc.Request); ok && req.IsCall() && req.ID == id {
			n++
		}
	}
	return n
}

// A response names its call by ID only, so a cancelled call that was sent keeps its ID until its late answer: a
// call reusing the ID before that is refused locally, and the late answer cannot retire or cancel any other call.
func TestMCPBridgeCancelledCallKeepsIDUntilAnswer(t *testing.T) {
	t.Run("late answer", func(t *testing.T) {
		b, local, remote := scriptBridge(t)
		parent, stop := context.WithCancel(t.Context())
		defer stop()
		id := mustID(t, 5.0)
		local.script(callMsg(id))
		pump(parent, t, b)
		first := b.pending(id)

		// Cancel A, then a new call B with A's ID, then A's late answer.
		local.script(cancelMsg(t, 5))
		pump(parent, t, b)
		local.script(callMsg(id))
		pump(parent, t, b)
		if b.pending(id) != first || remote.ctxOf(id).Err() == nil {
			t.Fatal("the cancellation released the ID or left the exchange live")
		}
		if n := callsWithID(remote.sent(), id); n != 1 {
			t.Fatalf("calls with the reused ID sent to the server = %d, want only the cancelled one", n)
		}
		var wire *jsonrpc.Error
		if out := local.sent(); len(out) != 1 || !errors.As(out[0].(*jsonrpc.Response).Error, &wire) || wire.Code != jsonrpc.CodeInvalidRequest {
			t.Fatalf("stdio = %+v, want an invalid-request answer for the reused ID", out)
		}
		late := &jsonrpc.Response{ID: id, Result: json.RawMessage(`{"late":true}`)}
		remote.script(late)
		if err := b.fromServer(parent); !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
		if b.pending(id) != nil {
			t.Fatal("the late answer did not end the cancelled call")
		}

		// After the late answer the ID is free, and the new call's exchange stays live.
		local.script(callMsg(id))
		pump(parent, t, b)
		newer := b.pending(id)
		if newer == nil || newer == first || newer.ctx.Err() != nil || callsWithID(remote.sent(), id) != 2 {
			t.Fatal("the ID could not be used again after the late answer")
		}
	})
	t.Run("never sent", func(t *testing.T) {
		b, local, remote := scriptBridge(t)
		var holdFirst atomic.Bool
		holdFirst.Store(true)
		remote.write = func(ctx context.Context, msg jsonrpc.Message) error {
			if req, ok := msg.(*jsonrpc.Request); ok && req.IsCall() && holdFirst.CompareAndSwap(true, false) {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		}
		id := mustID(t, 6.0)
		// The send of A fails on its cancellation, so no answer can come and B may take the ID at once.
		local.script(callMsg(id), cancelMsg(t, 6))
		pump(t.Context(), t, b)
		if b.pending(id) != nil {
			t.Fatal("a call whose send failed kept its ID")
		}
		local.script(callMsg(id))
		pump(t.Context(), t, b)
		if b.pending(id) == nil || len(local.sent()) != 0 {
			t.Fatalf("the ID of an unsent cancelled call was not reusable: stdio %v", local.sent())
		}
	})
}

// A cancelled call may never be answered, so past the bound the oldest cancelled call gives up its ID: a long-lived
// bridge does not keep every cancelled call, while the latest ones still keep their IDs for a late answer.
func TestMCPBridgeBoundsCancelledCalls(t *testing.T) {
	exchangeEnded := make(chan struct{}, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Answer headers, then no answer until the bridge ends the exchange.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		exchangeEnded <- struct{}{}
	}))
	t.Cleanup(endpoint.Close)
	remote, err := (&sdkmcp.StreamableClientTransport{Endpoint: endpoint.URL, DisableStandaloneSSE: true}).Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	b := &mcpBridge{stdio: &scriptConn{}, remote: remote, fail: func(err error) { t.Errorf("bridge failure: %v", err) }}
	ctx, _ := goJoined(t.Context(), t, func(ctx context.Context) { _ = b.fromServer(ctx) })

	const overflow = 3
	ids := make([]jsonrpc.ID, mcpCancelledCallsKept+overflow)
	for i := range ids {
		ids[i] = mustID(t, float64(i))
		req := callMsg(ids[i])
		call := b.register(ctx, req)
		b.forward(ctx, req, call)
		if b.pending(ids[i]) != call || call.ctx.Err() != nil {
			t.Fatalf("call %d did not get its answer headers", i)
		}
		b.cancelCall(cancelMsg(t, i).Params)
		within(t, "the cancelled exchange to end", exchangeEnded)
	}

	b.mu.Lock()
	kept := len(b.calls)
	b.mu.Unlock()
	if kept != mcpCancelledCallsKept {
		t.Fatalf("registry holds %d calls after %d unanswered cancellations, want the bound %d", kept, len(ids), mcpCancelledCallsKept)
	}
	for i, id := range ids {
		if gaveUp, oldest := b.pending(id) == nil, i < overflow; gaveUp != oldest {
			t.Fatalf("cancelled call %d gave up its ID = %v, want %v", i, gaveUp, oldest)
		}
	}
	if b.register(ctx, callMsg(ids[0])) == nil {
		t.Error("the ID given up by the oldest cancelled call is still refused")
	}
	if b.register(ctx, callMsg(ids[len(ids)-1])) != nil {
		t.Error("the latest cancelled call lost its ID to a new call")
	}
}
