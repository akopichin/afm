package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

const (
	mcpRefusalGrace   = 2 * time.Second
	mcpTransportStdio = "stdio"
	mcpDefaultPath    = "/mcp"
	// Newer protocol versions have no standalone GET stream, so the bridge must not open one.
	mcpNoStandaloneStreamFrom = "2026-07-28"
	mcpMaxEventBytes          = 1 << 20
	mcpErrorBodyBytes         = 1 << 10
	mcpMethodCancelled        = "notifications/cancelled"
	// A cancelled call may never be answered, so only the latest ones keep their IDs. MCP forbids reusing an ID
	// within a session, so only a client that breaks this rule can reuse an ID that a late answer may still carry.
	mcpCancelledCallsKept = 256
)

func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "MCP integration for a running afm run",
	}
	cmd.AddCommand(newMCPServeCmd())
	return cmd
}

func newMCPServeCmd() *cobra.Command {
	var transport, connect string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Bridge stdio MCP to the /mcp endpoint of a running afm run (started with --mcp)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if transport != mcpTransportStdio {
				return fmt.Errorf("--transport %q: only %s is supported", transport, mcpTransportStdio)
			}
			endpoint, err := parseMCPConnect(connect)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(commandContext(cmd), os.Interrupt)
			defer stop()
			// stdout carries protocol messages only; diagnostics go to stderr.
			if err := serveMCPBridge(ctx, endpoint, http.DefaultTransport, os.Stdin, os.Stdout, os.Stderr); err != nil {
				fmt.Fprintf(os.Stderr, "afm mcp: %v\n", err)
				return &ExitError{Code: 1, Silent: true}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", mcpTransportStdio, "client-side transport (only stdio)")
	cmd.Flags().StringVar(&connect, "connect", "", "loopback URL of the run's HTTP API, e.g. http://127.0.0.1:8080")
	_ = cmd.MarkFlagRequired("connect")
	return cmd
}

// parseMCPConnect accepts only loopback http: the endpoint has no authentication, so the bridge must never
// carry a session to a remote host. A bare host:port URL means the default /mcp path.
func parseMCPConnect(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return "", fmt.Errorf("--connect %q: want a loopback http URL such as http://127.0.0.1:8080", raw)
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", fmt.Errorf("--connect %q: only loopback hosts (127.0.0.1, localhost, [::1]) are allowed", raw)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = mcpDefaultPath
	}
	return u.String(), nil
}

// serveMCPBridge forwards JSON-RPC messages between stdio and the Streamable HTTP endpoint until stdin ends
// (nil, the session is deleted) or the session breaks (error). base carries the HTTP requests.
func serveMCPBridge(ctx context.Context, endpoint string, base http.RoundTripper, stdin io.ReadCloser, stdout io.WriteCloser, stderr io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	b := &mcpBridge{endpoint: endpoint, stderr: stderr}
	// A redirect would carry the JSON-RPC body and session headers off the validated loopback endpoint.
	b.client = &http.Client{
		Transport:     &bridgeRoundTripper{bridge: b, base: base},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	stdio, err := (&sdkmcp.IOTransport{Reader: stdin, Writer: stdout}).Connect(ctx)
	if err != nil {
		return err
	}
	remote, err := (&sdkmcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: b.client, DisableStandaloneSSE: true}).Connect(ctx)
	if err != nil {
		return err
	}

	// Two pumps and the notification stream; the first failure ends the bridge.
	errs := make(chan error, 3)
	b.fail = func(err error) {
		select {
		case errs <- err:
		default:
		}
	}
	b.stdio, b.remote = stdio, remote
	var wg sync.WaitGroup
	wg.Go(func() { errs <- b.toServer(ctx) })
	wg.Go(func() { errs <- b.fromServer(ctx) })

	result := <-errs
	if result != nil && !errors.Is(result, io.EOF) {
		b.awaitForwards()
	}
	// On stdin EOF cancel straight away: a parked request must not delay the session cleanup.
	cancel()
	// Closing the remote connection deletes the session; closing stdio unblocks its reader.
	_ = remote.Close()
	_ = stdio.Close()
	wg.Wait()
	b.forwards.Wait()
	if b.disabled() {
		return b.describe(result)
	}
	if result == nil || errors.Is(result, io.EOF) || errors.Is(result, context.Canceled) {
		return nil
	}
	return b.describe(result)
}

type mcpBridge struct {
	endpoint string
	stderr   io.Writer
	client   *http.Client
	stdio    sdkmcp.Connection
	remote   sdkmcp.Connection
	fail     func(error)

	// forwards tracks messages being sent on; each runs on its own goroutine so a parked call blocks nothing.
	forwards sync.WaitGroup

	mu       sync.Mutex
	initID   jsonrpc.ID
	version  string
	rejected string // last non-2xx answer of the endpoint, for the exit message
	listenOn sync.Once
	// calls are the calls sent on and not answered yet, by JSON-RPC ID. A cancelled call stays here too: its late
	// answer carries only the ID, so the ID cannot go to another call before that answer.
	calls map[jsonrpc.ID]*bridgeCall
	// cancelled lists cancelled calls, oldest first and at most mcpCancelledCallsKept; the oldest gives up its ID first.
	cancelled []*bridgeCall
}

// bridgeCall owns the HTTP exchange of one call: cancelling its context stops the call from being sent, or stops
// reading an SSE answer, without touching other calls.
type bridgeCall struct {
	id        jsonrpc.ID
	ctx       context.Context
	cancel    context.CancelFunc
	cancelled bool // guarded by mcpBridge.mu
}

// awaitForwards lets a refusal being written to stdio finish before the bridge closes it: the SDK connection
// reports the failed request from Read as well, and that error can win the race against the refusal.
func (b *mcpBridge) awaitForwards() {
	settled := make(chan struct{})
	go func() {
		b.forwards.Wait()
		close(settled)
	}()
	select {
	case <-settled:
	case <-time.After(mcpRefusalGrace):
	}
}

// disabled reports that the endpoint answered mcp_disabled; stdout then stays empty so the client sees EOF
// and the reason goes to stderr.
func (b *mcpBridge) disabled() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rejected == "mcp_disabled"
}

func (b *mcpBridge) protocolVersion() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.version
}

// toServer sends stdio messages to the endpoint without waiting for the answers, so stdin EOF and
// cancellations are seen while a call is parked. A call is registered here, before its own goroutine starts, so
// a cancellation read next always finds it, even while the call is still queued.
func (b *mcpBridge) toServer(ctx context.Context) error {
	for {
		msg, err := b.stdio.Read(ctx)
		if err != nil {
			return err
		}
		req, isRequest := msg.(*jsonrpc.Request)
		var call *bridgeCall
		switch {
		case isRequest && req.IsCall():
			if call = b.register(ctx, req); call == nil {
				b.forwards.Go(func() {
					b.answerLocally(ctx, req.ID, jsonrpc.CodeInvalidRequest, "the request id is in use by an earlier call that has not been answered yet")
				})
				continue
			}
		case isRequest && req.Method == mcpMethodCancelled:
			b.cancelCall(req.Params)
		default:
			// Other notifications and the client's responses are forwarded as they are.
		}
		b.forwards.Go(func() { b.forward(ctx, msg, call) })
	}
}

// register makes req the owner of its ID until its answer or failed send; nil means an earlier call, pending or
// cancelled, still owns the ID. MCP forbids reusing an ID within a session, so a compliant client loses nothing.
func (b *mcpBridge) register(ctx context.Context, req *jsonrpc.Request) *bridgeCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, taken := b.calls[req.ID]; taken {
		return nil
	}
	callCtx, cancel := context.WithCancel(ctx) //nolint:gosec // retire and cancelCall call cancel
	call := &bridgeCall{id: req.ID, ctx: callCtx, cancel: cancel}
	if b.calls == nil {
		b.calls = map[jsonrpc.ID]*bridgeCall{}
	}
	b.calls[req.ID] = call
	if req.Method == "initialize" {
		b.initID = req.ID
	}
	return call
}

// retire ends call's registration and its HTTP exchange; a later call that reused the ID is left alone.
func (b *mcpBridge) retire(call *bridgeCall) {
	b.mu.Lock()
	if b.calls[call.id] == call {
		delete(b.calls, call.id)
	}
	b.mu.Unlock()
	call.cancel()
}

func (b *mcpBridge) callFor(id jsonrpc.ID) *bridgeCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls[id]
}

// cancelCall stops the exchange of a call the client cancelled, so a queued call is never sent; the notification is
// forwarded for a call the server already runs. The call keeps its ID, as the SDK may still deliver an answer it read.
func (b *mcpBridge) cancelCall(params json.RawMessage) {
	var p sdkmcp.CancelledParams
	if json.Unmarshal(params, &p) != nil {
		return
	}
	id, err := jsonrpc.MakeID(p.RequestID)
	if err != nil {
		return
	}
	if call := b.markCancelled(id); call != nil {
		call.cancel()
	}
}

// markCancelled returns the call with id and counts its first cancellation; past mcpCancelledCallsKept cancelled
// calls the oldest one leaves the registry.
func (b *mcpBridge) markCancelled(id jsonrpc.ID) *bridgeCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	call := b.calls[id]
	if call == nil || call.cancelled {
		return call
	}
	call.cancelled = true
	b.cancelled = append(b.cancelled, call)
	if len(b.cancelled) > mcpCancelledCallsKept {
		oldest := b.cancelled[0]
		b.cancelled = b.cancelled[1:]
		if b.calls[oldest.id] == oldest {
			delete(b.calls, oldest.id)
		}
	}
	return call
}

// forward sends one stdio message on; call is its registration when the message is a call. A call whose send
// fails is answered on stdio unless it was cancelled; other failures end the bridge.
func (b *mcpBridge) forward(ctx context.Context, msg jsonrpc.Message, call *bridgeCall) {
	sendCtx := ctx
	if call != nil {
		sendCtx = call.ctx
	}
	err := b.remote.Write(sendCtx, msg)
	if err == nil {
		// Writing returns with the answer's headers: the call stays registered until fromServer sees its answer.
		if req, ok := msg.(*jsonrpc.Request); ok && req.Method == "notifications/initialized" {
			b.listenOn.Do(func() { go b.listen(ctx) })
		}
		return
	}
	// A failed write reads no answer, so even a cancelled call can give up its ID here.
	if call != nil {
		cancelled := call.ctx.Err() != nil
		b.retire(call)
		if cancelled {
			return
		}
	}
	if call == nil || b.disabled() {
		b.fail(err)
		return
	}
	b.answerLocally(ctx, call.id, jsonrpc.CodeInternalError, b.describe(err).Error())
}

// answerLocally answers a call on stdio that the endpoint will not answer, so the client does not wait forever.
func (b *mcpBridge) answerLocally(ctx context.Context, id jsonrpc.ID, code int64, message string) {
	if err := b.stdio.Write(ctx, &jsonrpc.Response{ID: id, Error: &jsonrpc.Error{Code: code, Message: message}}); err != nil {
		b.fail(err)
	}
}

// fromServer sends endpoint messages to stdio and learns the negotiated protocol version from the initialize
// result: the SDK connection only knows it when driven by its own client.
func (b *mcpBridge) fromServer(ctx context.Context) error {
	for {
		msg, err := b.remote.Read(ctx)
		if err != nil {
			return err
		}
		if resp, ok := msg.(*jsonrpc.Response); ok {
			b.noteInitializeResult(resp)
			if call := b.callFor(resp.ID); call != nil {
				b.retire(call)
			}
		}
		if err := b.stdio.Write(ctx, msg); err != nil {
			return err
		}
	}
}

func (b *mcpBridge) noteInitializeResult(resp *jsonrpc.Response) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if resp.ID != b.initID || resp.Error != nil {
		return
	}
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(resp.Result, &result) == nil {
		b.version = result.ProtocolVersion
	}
}

// listen opens the session's standalone GET stream, the only path for server notifications such as
// resources/updated. The SDK opens it only for its own client, so the bridge does it itself.
func (b *mcpBridge) listen(ctx context.Context) {
	sessionID := b.remote.SessionID()
	version := b.protocolVersion()
	if sessionID == "" || version >= mcpNoStandaloneStreamFrom {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.endpoint, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", sessionID)
	resp, err := b.client.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			_, _ = fmt.Fprintf(b.stderr, "afm mcp: notification stream: %v\n", err)
		}
		return
	}
	defer resp.Body.Close() //nolint:errcheck
	switch {
	case resp.StatusCode == http.StatusNotFound:
		b.fail(sdkmcp.ErrSessionMissing)
		return
	case resp.StatusCode != http.StatusOK:
		_, _ = fmt.Fprintf(b.stderr, "afm mcp: notification stream refused (HTTP %d): notifications will not arrive\n", resp.StatusCode)
		return
	}
	b.forwardEvents(ctx, resp.Body)
}

// forwardEvents decodes SSE data events into JSON-RPC messages for stdio.
func (b *mcpBridge) forwardEvents(ctx context.Context, body io.Reader) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), mcpMaxEventBytes)
	var data []string
	flush := func() {
		if len(data) == 0 {
			return
		}
		payload := strings.Join(data, "\n")
		data = nil
		msg, err := jsonrpc.DecodeMessage([]byte(payload))
		if err != nil {
			_, _ = fmt.Fprintf(b.stderr, "afm mcp: dropped an undecodable notification: %v\n", err)
			return
		}
		if err := b.stdio.Write(ctx, msg); err != nil {
			b.fail(err)
		}
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			// Other SSE fields (event, id, comments) carry nothing the bridge forwards.
		}
	}
	flush()
	if ctx.Err() == nil {
		_, _ = fmt.Fprintln(b.stderr, "afm mcp: notification stream ended")
	}
}

// describe turns a transport failure into an actionable message.
func (b *mcpBridge) describe(err error) error {
	if errors.Is(err, sdkmcp.ErrSessionMissing) {
		return errors.New("the MCP session is gone: the run ended, moved to a new repeat cycle, or afm restarted; start the bridge again")
	}
	if b.disabled() {
		return errors.New("the run serves no MCP endpoint: start it with `afm run --mcp`")
	}
	b.mu.Lock()
	rejected := b.rejected
	b.mu.Unlock()
	if rejected != "" {
		return fmt.Errorf("%w (endpoint answered %s)", err, rejected)
	}
	return err
}

// bridgeRoundTripper adds the negotiated MCP-Protocol-Version to requests the SDK connection sends without it,
// and remembers why the endpoint refused a request.
type bridgeRoundTripper struct {
	bridge *mcpBridge
	base   http.RoundTripper
}

func (t *bridgeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("MCP-Protocol-Version") == "" {
		if version := t.bridge.protocolVersion(); version != "" {
			req = req.Clone(req.Context())
			req.Header.Set("MCP-Protocol-Version", version)
		}
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp.StatusCode < http.StatusBadRequest {
		return resp, err
	}
	head, _ := io.ReadAll(io.LimitReader(resp.Body, mcpErrorBodyBytes))
	resp.Body = readCloser{Reader: io.MultiReader(bytes.NewReader(head), resp.Body), Closer: resp.Body}
	var body struct {
		Error string `json:"error"`
	}
	reason := fmt.Sprintf("HTTP %d", resp.StatusCode)
	if json.Unmarshal(head, &body) == nil && body.Error != "" {
		reason = body.Error
	}
	t.bridge.mu.Lock()
	t.bridge.rejected = reason
	t.bridge.mu.Unlock()
	return resp, nil
}

type readCloser struct {
	io.Reader
	io.Closer
}
