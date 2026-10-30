package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A Stop hook asks whether the agent CLI may end its turn; only the orchestrator knows the live epoch and the
// durable budget. The hook command (RunStopHook) therefore talks to a loopback bridge that exists for one process
// invocation and forwards each request to the capability the orchestrator minted for it (StopHookClient).

// StopHookDescriptorEnv names the env var holding the PATH of the 0600 descriptor file — never the token.
const StopHookDescriptorEnv = "AFM_STOP_HOOK_DESCRIPTOR"

const (
	stopHookURLPath     = "/stop"
	maxStopRequestBytes = 4 << 10
	maxStopRequestIDLen = 128
	maxStopDecisions    = 64      // distinct requests one invocation may have decided; the rest are allowed to stop
	maxHookInputBytes   = 1 << 20 // the hook event the CLI writes to the command's stdin
	stopHookCallTimeout = 10 * time.Second
	stopHookAttempts    = 3
)

// errNoStopHook is returned when the minted capability cannot answer requests.
var errNoStopHook = errors.New("executor: stop hook client did not mint a usable capability")

// StopRequest is one Stop of the agent's session. RequestID is chosen by the hook command and stays the same
// when it repeats a request, so a decision that spends a budget is made once.
type StopRequest struct {
	RequestID  string
	HookActive bool // the CLI says the session already continues because of a Stop hook
}

// StopDecision is the orchestrator's answer: Block keeps the session going with Reason as the next instruction.
type StopDecision struct {
	Block  bool
	Reason string
}

// StopHookClient is the capability to open a Stop hook bridge for an author runner. Like MeshClient it is minted
// per process invocation inside run(): Decide answers the requests of that invocation only, and revoke makes it
// inert when the invocation ends.
type StopHookClient struct {
	Mint func() (decide func(StopRequest) StopDecision, revoke func())
	// Runtime selects how the CLI is told to run the hook; Launcher is the absolute path of the afm binary it runs.
	Runtime  StopHookRuntime
	Launcher string
	// OnUnavailable, if set, is told why a hook that was meant to run will not (see StopHookUnavailable).
	OnUnavailable func(StopHookUnavailable)
}

// StopHookDescriptor is the file the hook command reads: where the bridge listens and the secrets that bind a
// request to this invocation.
type StopHookDescriptor struct {
	URL        string `json:"url"`
	Token      string `json:"token"`
	Invocation string `json:"invocation"`
}

type stopWireRequest struct {
	Invocation string `json:"invocation"`
	RequestID  string `json:"request_id"`
	HookActive bool   `json:"stop_hook_active"`
}

type stopWireResponse struct {
	Block  bool   `json:"block"`
	Reason string `json:"reason,omitempty"`
}

// IsStopHookTransportEnv reports whether an env entry is a Stop hook transport variable, stripped from every
// child environment and re-added only for the author runner that opened a bridge.
func IsStopHookTransportEnv(kv string) bool {
	return strings.HasPrefix(kv, "AFM_STOP_HOOK_")
}

// stopBridge serves the requests of one invocation. Requests are decided one at a time, and a repeated request
// id gets the earlier answer.
type stopBridge struct {
	token      string
	invocation string
	decide     func(StopRequest) StopDecision
	asked      *atomic.Bool // set by the first request that proves the CLI ran the hook command

	mu      sync.Mutex
	answers map[string]StopDecision
}

func (b *stopBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != stopHookURLPath {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	given := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(given), []byte(b.token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req stopWireRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxStopRequestBytes)).Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Invocation), []byte(b.invocation)) != 1 {
		http.Error(w, "wrong invocation", http.StatusForbidden)
		return
	}
	b.asked.Store(true)
	if req.RequestID == "" || len(req.RequestID) > maxStopRequestIDLen {
		http.Error(w, "bad request id", http.StatusBadRequest)
		return
	}
	decision := b.answer(StopRequest{RequestID: req.RequestID, HookActive: req.HookActive})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stopWireResponse(decision))
}

func (b *stopBridge) answer(req StopRequest) StopDecision {
	b.mu.Lock()
	defer b.mu.Unlock()
	if decision, seen := b.answers[req.RequestID]; seen {
		return decision
	}
	if len(b.answers) >= maxStopDecisions {
		return StopDecision{}
	}
	decision := b.decide(req)
	b.answers[req.RequestID] = decision
	return decision
}

// openedStopHook is one invocation's open bridge: the env that points the hook command at it, whether the CLI has
// asked it yet, and the cleanup that ends it.
type openedStopHook struct {
	env     []string
	asked   *atomic.Bool
	cleanup func()
}

// setupStopHook mints the invocation's capability, opens its loopback listener and writes the 0600 descriptor.
// cleanup closes the listener, revokes the capability and removes the descriptor — whatever way the invocation ends.
func (e *Executor) setupStopHook() (*openedStopHook, error) {
	decide, revoke := e.cfg.StopHook.Mint()
	if revoke == nil {
		revoke = func() {}
	}
	if decide == nil {
		revoke()
		return nil, errNoStopHook
	}
	token, err := randomHex(32)
	if err != nil {
		revoke()
		return nil, err
	}
	invocation, err := randomHex(16)
	if err != nil {
		revoke()
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		revoke()
		return nil, err
	}
	dir, err := os.MkdirTemp("", "afm-stop-hook-")
	if err != nil {
		_ = listener.Close()
		revoke()
		return nil, err
	}

	asked := new(atomic.Bool)
	srv := &http.Server{
		Handler:           &stopBridge{token: token, invocation: invocation, decide: decide, asked: asked, answers: map[string]StopDecision{}},
		ReadHeaderTimeout: stopHookCallTimeout,
		ReadTimeout:       stopHookCallTimeout,
		WriteTimeout:      stopHookCallTimeout,
	}
	go func() { _ = srv.Serve(listener) }()
	cleanup := sync.OnceFunc(func() {
		revoke() // an answer still in flight finds the capability inert
		_ = srv.Close()
		_ = os.RemoveAll(dir)
	})

	descriptor := StopHookDescriptor{URL: "http://" + listener.Addr().String() + stopHookURLPath, Token: token, Invocation: invocation}
	path := filepath.Join(dir, "descriptor.json")
	if werr := writeStopHookDescriptor(path, descriptor); werr != nil {
		cleanup()
		return nil, werr
	}
	return &openedStopHook{env: []string{StopHookDescriptorEnv + "=" + path}, asked: asked, cleanup: cleanup}, nil
}

func writeStopHookDescriptor(path string, d StopHookDescriptor) error {
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func randomHex(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// RunStopHook is the body of the hook command: it reads the CLI's hook event from stdin, asks the bridge named by
// the descriptor and returns what the CLI expects on stdout — {"decision":"block","reason":...} to continue the
// session, {} to let it stop. Whatever fails, the output is {} (a failed hook must never block a stop) and err says why.
func RunStopHook(ctx context.Context, descriptorPath string, stdin io.Reader) (output []byte, err error) {
	allow := []byte("{}\n")
	if descriptorPath == "" {
		return allow, fmt.Errorf("%s is not set", StopHookDescriptorEnv)
	}
	event, err := readStopEvent(stdin)
	if err != nil {
		return allow, err
	}
	data, err := os.ReadFile(descriptorPath)
	if err != nil {
		return allow, fmt.Errorf("read stop hook descriptor: %w", err)
	}
	var descriptor StopHookDescriptor
	if err := json.Unmarshal(data, &descriptor); err != nil {
		return allow, fmt.Errorf("parse stop hook descriptor: %w", err)
	}
	requestID, err := randomHex(16)
	if err != nil {
		return allow, err
	}
	body, err := json.Marshal(stopWireRequest{Invocation: descriptor.Invocation, RequestID: requestID, HookActive: event.HookActive})
	if err != nil {
		return allow, err
	}
	decision, err := askStopBridge(ctx, descriptor, body)
	if err != nil {
		return allow, err
	}
	if !decision.Block {
		return allow, nil
	}
	out, err := json.Marshal(map[string]string{"decision": "block", "reason": decision.Reason})
	if err != nil {
		return allow, err
	}
	return append(out, '\n'), nil
}

type stopEvent struct {
	HookActive bool `json:"stop_hook_active"`
}

// readStopEvent decodes the part of the CLI's hook event the gate uses; the rest is ignored.
func readStopEvent(stdin io.Reader) (stopEvent, error) {
	raw, err := io.ReadAll(io.LimitReader(stdin, maxHookInputBytes+1))
	if err != nil {
		return stopEvent{}, fmt.Errorf("read hook event: %w", err)
	}
	if len(raw) > maxHookInputBytes {
		return stopEvent{}, errors.New("hook event is too large")
	}
	var event stopEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return stopEvent{}, fmt.Errorf("parse hook event: %w", err)
	}
	return event, nil
}

// askStopBridge posts the request, repeating it with the same request id when the transport fails: a lost answer
// is received again, not decided twice.
func askStopBridge(ctx context.Context, d StopHookDescriptor, body []byte) (StopDecision, error) {
	ctx, cancel := context.WithTimeout(ctx, stopHookCallTimeout)
	defer cancel()
	var lastErr error
	for attempt := range stopHookAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return StopDecision{}, ctx.Err()
			case <-time.After(time.Duration(attempt) * 100 * time.Millisecond):
			}
		}
		decision, retry, err := postStopRequest(ctx, d, body)
		if err == nil {
			return decision, nil
		}
		lastErr = err
		if !retry {
			break
		}
	}
	return StopDecision{}, lastErr
}

// postStopRequest reports whether a failure is worth repeating: a transport failure is, an answer is not.
func postStopRequest(ctx context.Context, d StopHookDescriptor, body []byte) (decision StopDecision, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(body))
	if err != nil {
		return StopDecision{}, false, err
	}
	req.Header.Set("Authorization", "Bearer "+d.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return StopDecision{}, true, fmt.Errorf("stop hook bridge: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return StopDecision{}, false, fmt.Errorf("stop hook bridge answered %s", resp.Status)
	}
	var wire stopWireResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxStopRequestBytes)).Decode(&wire); err != nil {
		return StopDecision{}, false, fmt.Errorf("parse stop hook answer: %w", err)
	}
	return StopDecision(wire), false, nil
}
