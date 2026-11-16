package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm-engine/engine"
	"github.com/akopichin/afm/contracts/state"
)

// fakeReader fails the test when the run is read after the service was closed.
type fakeReader struct {
	t      *testing.T
	closed atomic.Bool
}

func (f *fakeReader) Snapshot() state.RunState {
	if f.closed.Load() {
		f.t.Error("Reader.Snapshot called after Close returned")
	}
	return state.RunState{}
}

func (f *fakeReader) History() ([]state.Transition, error) { return nil, nil }

func newTestService(t *testing.T) (*Service, *fakeReader) {
	t.Helper()
	reader := &fakeReader{t: t}
	svc, err := New(Config{RunID: "run-1", Reader: reader})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc, reader
}

func post(t *testing.T, url, session, body string) *http.Response {
	t.Helper()
	resp, err := postRequest(url, session, body)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func postRequest(url, session, body string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	}
	return http.DefaultClient.Do(req)
}

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

// openSession initializes a session and returns its id.
func openSession(t *testing.T, url string) string {
	t.Helper()
	resp := post(t, url, "", initializeBody)
	defer resp.Body.Close()
	id := resp.Header.Get("Mcp-Session-Id")
	if resp.StatusCode != http.StatusOK || id == "" {
		t.Fatalf("initialize: status %d, session %q", resp.StatusCode, id)
	}
	return id
}

func TestCloseIsIdempotentWithoutTraffic(t *testing.T) {
	svc, _ := newTestService(t)
	for i := 0; i < 3; i++ {
		if err := svc.Close(context.Background()); err != nil {
			t.Fatalf("Close #%d: %v", i, err)
		}
	}
}

func TestCloseRejectsNewRequests(t *testing.T) {
	svc, _ := newTestService(t)
	ts := httptest.NewServer(svc)
	defer ts.Close()
	if err := svc.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	resp := post(t, ts.URL, "", initializeBody)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("after Close got %d, want 503", resp.StatusCode)
	}
	if _, err := svc.enter(); !errors.Is(err, errClosed) {
		t.Fatalf("enter after Close = %v, want errClosed", err)
	}
}

func TestCloseEndsOpenSSEStream(t *testing.T) {
	svc, _ := newTestService(t)
	ts := httptest.NewServer(svc)
	defer ts.Close()
	session := openSession(t, ts.URL)

	req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", session)
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	streamEnded := make(chan struct{})
	go func() {
		buf := make([]byte, 512)
		for {
			if _, err := resp.Body.Read(buf); err != nil {
				close(streamEnded)
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Close(ctx); err != nil {
		t.Fatalf("Close with an open stream: %v", err)
	}
	select {
	case <-streamEnded:
	case <-time.After(5 * time.Second):
		t.Fatal("SSE stream still open after Close")
	}
}

func TestCloseWaitsForRunningHandler(t *testing.T) {
	svc, reader := newTestService(t)
	release, err := svc.enter()
	if err != nil {
		t.Fatal(err)
	}

	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := svc.Close(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close with a running handler = %v, want deadline exceeded", err)
	}

	reader.Snapshot() // the handler may still read the run
	release()
	if err := svc.Close(context.Background()); err != nil {
		t.Fatalf("Close after the handler returned: %v", err)
	}
	reader.closed.Store(true)
}

func TestCloseDuringToolCallWakesBlockedHandler(t *testing.T) {
	svc, reader := newTestService(t)
	started := make(chan struct{})
	type in struct{}
	type out struct{}
	sdkmcp.AddTool(svc.server, &sdkmcp.Tool{Name: "block"}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, _ in) (*sdkmcp.CallToolResult, out, error) {
		release, err := svc.enter()
		if err != nil {
			return errorResult(err), out{}, nil
		}
		defer release()
		close(started)
		<-svc.done
		reader.Snapshot()
		return nil, out{}, nil
	})
	ts := httptest.NewServer(svc)
	defer ts.Close()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "t", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &sdkmcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	go func() { _, _ = session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "block"}) }()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Close(ctx); err != nil {
		t.Fatalf("Close with a blocked tool call: %v", err)
	}
	reader.closed.Store(true)
}

func TestNewRequiresReaderAndRun(t *testing.T) {
	if _, err := New(Config{RunID: "r"}); err == nil {
		t.Error("New without Reader succeeded")
	}
	if _, err := New(Config{Reader: &fakeReader{t: t}}); err == nil {
		t.Error("New without run identity succeeded")
	}
	svc, err := New(Config{RunDir: "/tmp/x/run-42", Reader: &fakeReader{t: t}})
	if err != nil || svc.RunID() != "run-42" {
		t.Fatalf("RunID from RunDir = %v, %v", svc, err)
	}
}

func TestSessionCeiling(t *testing.T) {
	svc, _ := newTestService(t)
	ts := httptest.NewServer(svc)
	defer ts.Close()
	for i := 0; i < maxSessions; i++ {
		openSession(t, ts.URL)
	}
	resp := post(t, ts.URL, "", initializeBody)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("session over the ceiling got %d, want 503", resp.StatusCode)
	}
}

func TestCheckRunRejectsOtherRun(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.checkRun("run-1"); err != nil {
		t.Fatal(err)
	}
	if got := toolError(svc.checkRun("run-2")); got.Code != "run_changed" {
		t.Fatalf("code = %q, want run_changed", got.Code)
	}
}

func TestToolErrorUsesRESTClassifier(t *testing.T) {
	err := &engine.Error{Kind: engine.KindConflict, Code: "approval_stale", Message: "plan changed"}
	got := toolError(err)
	if got.Code != "approval_stale" || got.Kind != string(engine.KindConflict) {
		t.Fatalf("toolError = %+v", got)
	}
	res := errorResult(err)
	if !res.IsError || res.StructuredContent == nil {
		t.Fatalf("errorResult = %+v", res)
	}
	if got := toolError(errors.New("boom")); got.Code != codeInternal {
		t.Fatalf("plain error code = %q, want %q", got.Code, codeInternal)
	}
}

// gateCreations makes every session creation pause until release is closed and reports each arrival.
func gateCreations(svc *Service) (arrived chan struct{}, release chan struct{}) {
	arrived, release = make(chan struct{}, 64), make(chan struct{})
	svc.onNewSession = func() {
		arrived <- struct{}{}
		<-release
	}
	return arrived, release
}

func TestConcurrentSessionCreationsRespectCeiling(t *testing.T) {
	svc, _ := newTestService(t)
	ts := httptest.NewServer(svc)
	defer ts.Close()
	arrived, release := gateCreations(svc)
	const attempts = maxSessions + 1
	statuses := make(chan int, attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			resp := post(t, ts.URL, "", initializeBody)
			resp.Body.Close()
			statuses <- resp.StatusCode
		}()
	}
	// Exactly one request is refused while the other sixteen hold their reservation inside the SDK.
	select {
	case got := <-statuses:
		if got != http.StatusServiceUnavailable {
			t.Fatalf("first answer = %d, want the refusal of the 17th creation", got)
		}
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("no creation was refused: the ceiling is not reserved atomically")
	}
	for i := 0; i < maxSessions; i++ {
		<-arrived
	}
	close(release)
	for i := 0; i < maxSessions; i++ {
		if got := <-statuses; got != http.StatusOK {
			t.Fatalf("admitted creation got %d", got)
		}
	}
	if n := svc.sessionCount(); n != maxSessions {
		t.Fatalf("sessions = %d, want %d", n, maxSessions)
	}
}

// initializeRequest is a session-creating POST for calling ServeHTTP without a network.
func initializeRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(initializeBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	return req
}

// Close must not sweep sessions while an admitted creation is still inside the SDK. In the bubble, synctest.Wait
// returns only once Close has gone as far as it can without the creation, so a sweep that does not wait is seen.
func TestCloseDrainsAdmittedSessionCreation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, _ := newTestService(t)
		arrived, release := gateCreations(svc)
		answered := make(chan struct{})
		go func() {
			defer close(answered)
			svc.ServeHTTP(httptest.NewRecorder(), initializeRequest())
		}()
		<-arrived

		swept := make(chan struct{})
		svc.onDrained = func() { close(swept) }
		closed := make(chan error, 1)
		go func() { closed <- svc.Close(t.Context()) }()
		synctest.Wait()
		select {
		case <-swept:
			close(release)
			t.Fatal("Close swept sessions before the admitted creation finished")
		default:
		}
		close(release)
		if err := <-closed; err != nil {
			t.Fatal(err)
		}
		<-answered
		if n := svc.sessionCount(); n != 0 {
			t.Fatalf("%d session(s) survived Close", n)
		}
		rec := httptest.NewRecorder()
		svc.ServeHTTP(rec, initializeRequest())
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("creation after Close got %d, want 503", rec.Code)
		}
	})
}

func TestSessionPolicyDefaults(t *testing.T) {
	svc, _ := newTestService(t)
	if svc.sessionTimeout() != 30*time.Minute || svc.maxSessions() != 16 {
		t.Fatalf("policy = %v / %d, want 30m / 16", svc.sessionTimeout(), svc.maxSessions())
	}
}

func TestIdleSessionExpiresAndFreesCapacity(t *testing.T) {
	svc, err := New(Config{RunID: "run-1", Reader: &fakeReader{t: t}, SessionTimeout: 20 * time.Millisecond, MaxSessions: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	ts := httptest.NewServer(svc)
	defer ts.Close()
	openSession(t, ts.URL)
	deadline := time.Now().Add(10 * time.Second)
	for svc.sessionCount() > 0 {
		if time.Now().After(deadline) {
			t.Fatal("idle session did not expire")
		}
		runtime.Gosched()
	}
	openSession(t, ts.URL)
}

// closeGuard bounds a Close that is expected to return promptly; reaching it is the failure, not the evidence.
const closeGuard = 10 * time.Second

func closeWithin(t *testing.T, svc *Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), closeGuard)
	defer cancel()
	if err := svc.Close(ctx); err != nil {
		t.Fatalf("Close did not interrupt the admitted request: %v (live sessions %d)", err, svc.sessionCount())
	}
}

// handlerReturned fails the test when the handler is still running after Close returned.
func handlerReturned(t *testing.T, returned <-chan struct{}) {
	t.Helper()
	select {
	case <-returned:
	case <-time.After(closeGuard):
		t.Fatal("Close returned while the request handler was still running")
	}
}

// readSignal reports the first Read of a request body, the point where the handler waits on the client.
type readSignal struct {
	io.ReadCloser
	once    sync.Once
	reading chan struct{}
}

func (b *readSignal) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.reading) })
	return b.ReadCloser.Read(p)
}

// A client that sent its headers but not the whole body holds the handler in a network read; Close must end that
// read, for a new session and inside an existing one.
func TestCloseInterruptsUnfinishedRequestBody(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint("existing session ", existing), func(t *testing.T) {
			reader := &fakeReader{t: t}
			svc, err := New(Config{RunID: "run-1", Reader: reader})
			if err != nil {
				t.Fatal(err)
			}
			reading, returned := make(chan struct{}), make(chan struct{})
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Test-Stall") != "" {
					r.Body = &readSignal{ReadCloser: r.Body, reading: reading}
					defer close(returned)
				}
				svc.ServeHTTP(w, r)
			}))
			defer ts.Close()
			headers := ""
			if existing {
				headers = "Mcp-Session-Id: " + openSession(t, ts.URL) + "\r\nMCP-Protocol-Version: 2025-06-18\r\n"
			}
			conn, err := net.Dial("tcp", ts.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			// 500 bytes announced, one sent: the server blocks reading the rest.
			_, err = fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: application/json\r\n"+
				"Accept: application/json, text/event-stream\r\nX-Test-Stall: 1\r\n%sContent-Length: 500\r\n\r\n{", headers)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-reading:
			case <-time.After(closeGuard):
				t.Fatal("the request never reached its body read")
			}

			closeWithin(t, svc)
			reader.closed.Store(true)
			handlerReturned(t, returned)
			if n := svc.sessionCount(); n != 0 {
				t.Fatalf("%d session(s) survived Close", n)
			}
		})
	}
}

// stalledWriter is a client that stopped reading its answer: Write blocks until a write deadline is set.
type stalledWriter struct {
	header   http.Header
	writing  chan struct{}
	deadline chan struct{}
	once     sync.Once
	deadOnce sync.Once
}

func newStalledWriter() *stalledWriter {
	return &stalledWriter{header: http.Header{}, writing: make(chan struct{}), deadline: make(chan struct{})}
}

func (w *stalledWriter) Header() http.Header { return w.header }
func (w *stalledWriter) WriteHeader(int)     {}
func (w *stalledWriter) Flush()              {}
func (w *stalledWriter) Write([]byte) (int, error) {
	w.once.Do(func() { close(w.writing) })
	<-w.deadline
	return 0, os.ErrDeadlineExceeded
}
func (w *stalledWriter) SetReadDeadline(time.Time) error { return nil }
func (w *stalledWriter) SetWriteDeadline(time.Time) error {
	w.deadOnce.Do(func() { close(w.deadline) })
	return nil
}

func TestCloseInterruptsBlockedResponseWrite(t *testing.T) {
	reader := &fakeReader{t: t}
	svc, err := New(Config{RunID: "run-1", Reader: reader})
	if err != nil {
		t.Fatal(err)
	}
	w := newStalledWriter()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(initializeBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		svc.ServeHTTP(w, req)
	}()
	select {
	case <-w.writing:
	case <-time.After(closeGuard):
		t.Fatal("the answer was never written")
	}

	closeWithin(t, svc)
	reader.closed.Store(true)
	handlerReturned(t, returned)
	if n := svc.sessionCount(); n != 0 {
		t.Fatalf("%d session(s) survived Close", n)
	}
}

func TestCloseConcurrentCallsAllReturn(t *testing.T) {
	svc, _ := newTestService(t)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() { errs <- svc.Close(context.Background()) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// A creation the SDK rejects gives its reservation back, so the only slot is free for the next client.
func TestInvalidInitializationReleasesItsReservation(t *testing.T) {
	svc, err := New(Config{RunID: "run-1", Reader: &fakeReader{t: t}, MaxSessions: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	ts := httptest.NewServer(svc)
	defer ts.Close()
	resp := post(t, ts.URL, "", "not json")
	resp.Body.Close()
	if resp.StatusCode < http.StatusBadRequest || resp.StatusCode == http.StatusServiceUnavailable {
		t.Fatalf("invalid initialize got %d, want a client error", resp.StatusCode)
	}
	openSession(t, ts.URL)
}
