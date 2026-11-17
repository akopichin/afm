package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	childMCPPath      = "/mcp"
	ChildStatusTool   = "afm_run_status"
	childWaitTool     = "afm_run_wait"
	maxForwardedWaits = 8
	connectTimeout    = 10 * time.Second
	closeGrace        = 2 * time.Second
)

// OutcomeUnknownError means a control call may or may not have been applied: the connection broke after the
// request was sent. It is never retried, because a second apply could be a second action.
type OutcomeUnknownError struct{ Tool string }

func (e *OutcomeUnknownError) Error() string {
	return fmt.Sprintf("the connection broke during %s; read the run's status to see whether it was applied", e.Tool)
}

// WaitCapacityError means too many waits are already parked on one run.
type WaitCapacityError struct{ RunID string }

func (e *WaitCapacityError) Error() string {
	return fmt.Sprintf("run %s already has %d waits in flight", e.RunID, maxForwardedWaits)
}

// ChildStatus is the part of a run's own afm_run_status the host reads itself.
type ChildStatus struct {
	RunID             string `json:"run_id"`
	RunStatus         string `json:"run_status"`
	AttentionRequired bool   `json:"attention_required"`
}

// Sessions keeps one MCP client session per child process and speaks to the run through it. A session is
// accepted only after the run's own status names the run the caller asked for, so a session never answers for
// another run than the one it was verified against.
type Sessions struct {
	// Updated is called when a resource the host's clients subscribed to may have changed.
	Updated func(uri string)
	// Probe asks an endpoint for its identity when a session is established; nil means ProbeRuntime.
	Probe func(ctx context.Context, endpoint string) (Runtime, error)

	mu      sync.Mutex
	connect sync.Mutex // one connect at a time keeps the cache free of duplicates
	// transitions orders subscription changes together with the run's answer to them, and the replay of a new
	// session: a last unsubscribe still in flight must not undo a newer subscribe. Taken after connect, never
	// before it, and never across a dial.
	transitions transitionLock
	sessions    map[string]*childSession
	subs        map[string]map[string]map[any]struct{} // session key -> resource URI -> subscribed host sessions
	closed      bool
	// root is cancelled by Close, which ends the forwarded calls still parked on a run.
	root       context.Context
	cancelRoot context.CancelFunc
	calls      sync.WaitGroup // forwarded calls in flight; Close waits for the cancelled ones to return
	// background is what runs outside every lock: closing retired sessions and telling subscribers of a loss.
	background sync.WaitGroup
}

type childSession struct {
	key      string
	endpoint string
	runID    string
	cs       *sdkmcp.ClientSession
	waits    chan struct{}
	gone     chan struct{}
	retired  bool // guarded by Sessions.mu: no longer offered, and its loss was handled once
}

// transitionLock is a mutex whose waiters give up with their context, so a subscription change that waits for
// another one stays within its own deadline.
type transitionLock struct {
	once sync.Once
	ch   chan struct{}
}

func (l *transitionLock) lock(ctx context.Context) error {
	l.once.Do(func() { l.ch = make(chan struct{}, 1) })
	select {
	case l.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *transitionLock) unlock() { <-l.ch }

// noTimeoutClient serves streaming responses, so it has no overall timeout, and never follows redirects.
var noTimeoutClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func sessionKey(v RunView) string {
	if v.InstanceID != "" {
		return v.InstanceID
	}
	return v.Endpoint
}

// Close ends every session. The runs behind them are not touched.
func (s *Sessions) Close() {
	s.rootContext() // so a Close before any call still leaves later calls cancelled
	s.mu.Lock()
	s.closed = true // from here on nothing new starts in the background, and a lost session tells nobody
	s.cancelRoot()
	var all []*childSession
	for _, sess := range s.sessions {
		sess.retired = true
		all = append(all, sess)
	}
	s.sessions = nil
	s.mu.Unlock()
	s.calls.Wait() // a session closes gracefully only once the calls on it have returned
	// A run that is slow to end its session must not hold up the host's shutdown: close them side by side and
	// stop waiting after closeGrace.
	for _, sess := range all {
		s.background.Go(func() { _ = sess.cs.Close() })
	}
	finished := make(chan struct{})
	go func() { s.background.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(closeGrace):
	}
}

// rootContext is cancelled when Close starts.
func (s *Sessions) rootContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil {
		s.root, s.cancelRoot = context.WithCancel(context.Background()) //nolint:gosec // Close calls cancelRoot
	}
	return s.root
}

var errShuttingDown = errors.New("the host is shutting down")

// enter registers a forwarded call; it fails once Close has started.
func (s *Sessions) enter() (leave func(), err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errShuttingDown
	}
	s.calls.Add(1)
	return s.calls.Done, nil
}

// lifetime registers a call that Close cancels and then waits for: a call parked on a run must not hold up the
// host's shutdown.
func (s *Sessions) lifetime(caller context.Context) (context.Context, func(), error) {
	leave, err := s.enter()
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(caller)
	stop := context.AfterFunc(s.rootContext(), cancel)
	return ctx, func() { stop(); cancel(); leave() }, nil
}

// session returns the live session verified for v.RunID, connecting when there is none.
func (s *Sessions) session(ctx context.Context, v RunView) (*childSession, error) {
	if sess := s.cached(v); sess != nil {
		return sess, nil
	}
	s.connect.Lock()
	defer s.connect.Unlock()
	if sess := s.cached(v); sess != nil {
		return sess, nil
	}
	return s.dial(ctx, v)
}

func (s *Sessions) cached(v RunView) *childSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[sessionKey(v)]
	if sess == nil || sess.runID != v.RunID {
		return nil
	}
	select {
	case <-sess.gone:
		return nil
	default:
		return sess
	}
}

func (s *Sessions) dial(ctx context.Context, v RunView) (*childSession, error) {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	key := sessionKey(v)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "afm-serve", Version: "1"}, &sdkmcp.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, req *sdkmcp.ResourceUpdatedNotificationRequest) {
			if s.Updated != nil {
				s.Updated(req.Params.URI)
			}
		},
	})
	cs, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: v.Endpoint + childMCPPath, HTTPClient: noTimeoutClient}, nil)
	if err != nil {
		return nil, remoteFailure("connect to the run", err)
	}
	status, _, err := callStatus(ctx, cs)
	if err == nil && status.RunID != v.RunID {
		err = &RunChangedError{CurrentRunID: status.RunID}
	}
	if err == nil {
		err = s.verifyInstance(ctx, v)
	}
	sess := &childSession{key: key, endpoint: v.Endpoint, runID: v.RunID, cs: cs, waits: make(chan struct{}, maxForwardedWaits), gone: make(chan struct{})}
	if err == nil {
		err = s.publish(ctx, v.RunID, sess)
	}
	if err != nil {
		s.mu.Lock()
		s.goLocked(func() { _ = cs.Close() })
		s.mu.Unlock()
		return nil, remoteFailure("connect to the run", err)
	}
	return sess, nil
}

// publish makes a verified session the one for its key once its run's subscriptions are replayed on it. A broken
// transport fails the session; a refused subscription is dropped and its subscribers told, so none stays without it.
func (s *Sessions) publish(ctx context.Context, runID string, sess *childSession) error {
	if err := s.transitions.lock(ctx); err != nil {
		return err
	}
	defer s.transitions.unlock()
	s.mu.Lock()
	var replay []string
	for uri := range s.subs[sess.key] {
		// A repeat cycle is another run: subscriptions to the old run's resources are not moved to it.
		if owner, ok := runOfURI(uri); ok && owner == runID {
			replay = append(replay, uri)
		}
	}
	s.mu.Unlock()
	var refused []string
	for _, uri := range replay {
		err := sess.cs.Subscribe(ctx, &sdkmcp.SubscribeParams{URI: uri})
		switch {
		case err == nil:
		case answeredByRun(err):
			refused = append(refused, uri)
		default:
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errShuttingDown
	}
	for _, uri := range refused {
		delete(s.subs[sess.key], uri)
	}
	s.notifyLocked(refused)
	if s.sessions == nil {
		s.sessions = map[string]*childSession{}
	}
	if old := s.sessions[sess.key]; old != nil {
		s.retireLocked(old)
	}
	s.sessions[sess.key] = sess
	go s.watchSession(sess)
	return nil
}

// verifyInstance proves that the process behind the new session is the instance the caller resolved: a run id
// alone does not, because another process can resume the same run on the same port.
func (s *Sessions) verifyInstance(ctx context.Context, v RunView) error {
	if v.InstanceID == "" {
		return nil // an afm that predates instance ids can only be told apart by its run id
	}
	probe := s.Probe
	if probe == nil {
		probe = ProbeRuntime
	}
	rt, err := probe(ctx, v.Endpoint)
	if err != nil {
		return remoteFailure("verify the run", err)
	}
	if rt.InstanceID != v.InstanceID {
		return fmt.Errorf("verify the run: %w", errIdentityChanged)
	}
	return nil
}

// watchSession notices a session that ended on its own (the run moved to a new cycle, or the process exited).
func (s *Sessions) watchSession(sess *childSession) {
	_ = sess.cs.Wait()
	close(sess.gone)
	s.retire(sess)
}

// retire stops offering a failed or ended session and tells its subscribers once, unless the host is shutting down.
// It is closed in the background: closing waits for every call on it, and no caller or lock may wait for those.
func (s *Sessions) retire(sess *childSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retireLocked(sess)
}

func (s *Sessions) retireLocked(sess *childSession) {
	if sess.retired {
		return
	}
	sess.retired = true
	if s.sessions[sess.key] == sess {
		delete(s.sessions, sess.key)
	}
	s.goLocked(func() { _ = sess.cs.Close() })
	s.notifyLocked(slices.Collect(maps.Keys(s.subs[sess.key])))
}

// notifyLocked tells the subscribers of uris that the resources may have changed, outside every lock: a host
// client may take seconds to accept a notification.
func (s *Sessions) notifyLocked(uris []string) {
	if len(uris) == 0 || s.closed || s.Updated == nil {
		return
	}
	s.goLocked(func() {
		for _, uri := range uris {
			s.Updated(uri)
		}
	})
}

// goLocked runs fn in the background; Close waits for it, up to closeGrace. Once Close started, fn is not tracked:
// Close is already waiting.
func (s *Sessions) goLocked(fn func()) {
	if s.closed {
		go fn()
		return
	}
	s.background.Go(fn)
}

func callStatus(ctx context.Context, cs *sdkmcp.ClientSession) (ChildStatus, json.RawMessage, error) {
	res, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{Name: ChildStatusTool})
	if err != nil {
		return ChildStatus{}, nil, err
	}
	raw, err := resultJSON(res)
	if err != nil {
		return ChildStatus{}, nil, err
	}
	var st ChildStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		return ChildStatus{}, nil, fmt.Errorf("read run status: %w", err)
	}
	return st, raw, nil
}

// resultJSON returns the structured body of a successful tool result.
func resultJSON(res *sdkmcp.CallToolResult) (json.RawMessage, error) {
	if res.IsError {
		return nil, errors.New("the run refused the call")
	}
	if res.StructuredContent != nil {
		return json.Marshal(res.StructuredContent)
	}
	for _, c := range res.Content {
		if text, ok := c.(*sdkmcp.TextContent); ok {
			return json.RawMessage(text.Text), nil
		}
	}
	return nil, errors.New("the run returned no content")
}

// Status reads the run's own status through its verified session.
func (s *Sessions) Status(ctx context.Context, v RunView) (ChildStatus, json.RawMessage, error) {
	var (
		st  ChildStatus
		raw json.RawMessage
	)
	err := s.withSession(ctx, v, ChildStatusTool, true, func(ctx context.Context, sess *childSession) error {
		var err error
		st, raw, err = callStatus(ctx, sess.cs)
		return err
	})
	return st, raw, err
}

// CallTool forwards one tool call. A session the child no longer knows (404) was not applied, so the call is
// re-sent on a fresh, re-verified session. Any other transport failure is retried only for read-only tools: a
// control call that may have been applied is reported as outcome unknown, never sent twice.
func (s *Sessions) CallTool(ctx context.Context, v RunView, name string, args json.RawMessage, readOnly bool) (*sdkmcp.CallToolResult, error) {
	var res *sdkmcp.CallToolResult
	err := s.withSession(ctx, v, name, readOnly, func(ctx context.Context, sess *childSession) error {
		if name == childWaitTool {
			select {
			case sess.waits <- struct{}{}:
				defer func() { <-sess.waits }()
			default:
				return &WaitCapacityError{RunID: v.RunID}
			}
		}
		var err error
		res, err = sess.cs.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
		return err
	})
	return res, err
}

// ReadResource reads a resource of the run through its verified session.
func (s *Sessions) ReadResource(ctx context.Context, v RunView, uri string) (*sdkmcp.ReadResourceResult, error) {
	var res *sdkmcp.ReadResourceResult
	err := s.withSession(ctx, v, "resources/read", true, func(ctx context.Context, sess *childSession) error {
		var err error
		res, err = sess.cs.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: uri})
		return err
	})
	return res, err
}

// withSession runs fn on a verified session and applies the retry rules above. A failure of the transport is
// reported as a *RemoteError, never with the transport's own words.
func (s *Sessions) withSession(caller context.Context, v RunView, what string, readOnly bool, fn func(context.Context, *childSession) error) error {
	// Closing the host ends every call parked on a run: closing a session waits for the calls running on it.
	ctx, done, err := s.lifetime(caller)
	if err != nil {
		return err
	}
	defer done()
	for attempt := 0; ; attempt++ {
		sess, err := s.session(ctx, v)
		if err != nil {
			return err
		}
		err = fn(ctx, sess)
		if err == nil || caller.Err() != nil {
			return remoteFailure(what, err)
		}
		if ctx.Err() != nil { // the host is closing
			if readOnly {
				return remoteFailure(what, err)
			}
			return &OutcomeUnknownError{Tool: what}
		}
		var capacity *WaitCapacityError
		if answeredByRun(err) || errors.As(err, &capacity) {
			return err // the run answered; nothing broke
		}
		s.retire(sess)
		retry := errors.Is(err, sdkmcp.ErrSessionMissing) || readOnly
		if !retry {
			return &OutcomeUnknownError{Tool: what}
		}
		if attempt > 0 {
			return remoteFailure(what, err)
		}
	}
}

// Codes the SDK gives to failures of its own connection, which the run never answered.
const (
	codeUnknown        = -32001
	codeClientClosing  = -32003
	codeRejectedByConn = -32005
)

// answeredByRun tells an error response of the run (invalid params, unknown resource) from a broken connection,
// which the SDK reports as a JSON-RPC error too.
func answeredByRun(err error) bool {
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) {
		return false
	}
	return rpcErr.Code != codeUnknown && rpcErr.Code != codeClientClosing && rpcErr.Code != codeRejectedByConn
}

// Subscribe asks the run to notify about uri on behalf of one host session. The run is asked once per URI
// however many host sessions subscribed, and the same session subscribing again changes nothing. A host session
// is told it subscribed only once the run's subscription exists.
func (s *Sessions) Subscribe(caller context.Context, v RunView, uri string, host any) error {
	ctx, done, err := s.lifetime(caller)
	if err != nil {
		return err
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, connectTimeout) // bounds how long the transition lock is held
	defer cancel()
	for attempt := 0; ; attempt++ {
		// Outside the transition lock: a new session replays the subscriptions under it.
		sess, err := s.session(ctx, v)
		if err != nil {
			return err
		}
		replaced, err := s.subscribeOn(ctx, sess, uri, host)
		if !replaced || attempt > 0 {
			return err
		}
	}
}

// subscribeOn joins host to uri's subscribers on sess and asks the run for the first of them. A host session is
// told it subscribed only while sess, which holds the run's subscription, is the run's session: one that is lost
// later tells its subscribers. replaced means sess stopped being the run's session; the caller takes the new one.
func (s *Sessions) subscribeOn(ctx context.Context, sess *childSession, uri string, host any) (replaced bool, err error) {
	if err := s.transitions.lock(ctx); err != nil {
		return false, remoteFailure("subscribe", err)
	}
	defer s.transitions.unlock()
	first, current := s.joinCurrent(sess, uri, host)
	if !current {
		return true, errSessionReplaced
	}
	if !first {
		return false, nil
	}
	err = sess.cs.Subscribe(ctx, &sdkmcp.SubscribeParams{URI: uri})
	if err == nil && s.isCurrent(sess) {
		return false, nil
	}
	s.leave(sess.key, uri, host)
	if err == nil {
		return true, errSessionReplaced // lost while the run subscribed: the next session subscribes again
	}
	if !answeredByRun(err) {
		s.retire(sess) // the run may or may not have subscribed; a new session subscribes only what is wanted
	}
	return false, remoteFailure("subscribe", err)
}

var errSessionReplaced = errors.New("the run's session was replaced; try again")

// isCurrent reports whether sess is still the live session of its key.
func (s *Sessions) isCurrent(sess *childSession) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isCurrentLocked(sess)
}

func (s *Sessions) isCurrentLocked(sess *childSession) bool {
	select {
	case <-sess.gone:
		return false
	default:
		return s.sessions[sess.key] == sess
	}
}

// joinCurrent adds host to uri's subscribers if sess is still the run's session, and reports whether it is the
// first one. Both happen under one lock, so a session lost right after it still counts host among its subscribers.
func (s *Sessions) joinCurrent(sess *childSession, uri string, host any) (first, current bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isCurrentLocked(sess) {
		return false, false
	}
	key := sess.key
	if s.subs == nil {
		s.subs = map[string]map[string]map[any]struct{}{}
	}
	if s.subs[key] == nil {
		s.subs[key] = map[string]map[any]struct{}{}
	}
	members := s.subs[key][uri]
	if members == nil {
		members = map[any]struct{}{}
		s.subs[key][uri] = members
	}
	first = len(members) == 0
	members[host] = struct{}{}
	return first, true
}

// Unsubscribe removes one host session; the run is told when the last one leaves.
func (s *Sessions) Unsubscribe(caller context.Context, v RunView, uri string, host any) error {
	ctx, done, err := s.lifetime(caller)
	if err != nil {
		s.leave(sessionKey(v), uri, host)
		return nil // the host is closing and with it every session to the run
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := s.transitions.lock(ctx); err != nil {
		return remoteFailure("unsubscribe", err)
	}
	defer s.transitions.unlock()
	if !s.leave(sessionKey(v), uri, host) {
		return nil
	}
	if sess := s.cached(v); sess != nil {
		s.unsubscribeOn(ctx, sess, uri)
	}
	return nil
}

// unsubscribeOn tells the run that nobody listens to uri anymore. When the run's answer is lost its side is
// unknown, so the session is retired: the next one subscribes only what is still wanted.
func (s *Sessions) unsubscribeOn(ctx context.Context, sess *childSession, uri string) {
	if err := sess.cs.Unsubscribe(ctx, &sdkmcp.UnsubscribeParams{URI: uri}); err != nil && !answeredByRun(err) {
		s.retire(sess)
	}
}

// ReleaseHost removes a host session that disconnected from every subscription, telling the runs that nobody
// listens to anymore.
func (s *Sessions) ReleaseHost(host any) {
	ctx, done, err := s.lifetime(context.Background())
	if err != nil {
		s.leaveAll(host) // the host is closing and with it every session to the runs
		return
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := s.transitions.lock(ctx); err != nil {
		s.leaveAll(host) // the run keeps a subscription nobody listens to; a missing one would be worse
		return
	}
	defer s.transitions.unlock()
	for _, o := range s.leaveAll(host) {
		if o.sess != nil {
			s.unsubscribeOn(ctx, o.sess, o.uri)
		}
	}
}

type orphanSubscription struct {
	uri  string
	sess *childSession
}

// leaveAll removes host from every subscription and returns those it was the last member of, with the live
// session each one was made on.
func (s *Sessions) leaveAll(host any) []orphanSubscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	var orphans []orphanSubscription
	for key, byURI := range s.subs {
		for uri := range byURI {
			if s.leaveLocked(key, uri, host) {
				orphans = append(orphans, orphanSubscription{uri: uri, sess: s.sessions[key]})
			}
		}
	}
	return orphans
}

// leave removes host from a URI's subscribers and reports whether it was the last one.
func (s *Sessions) leave(key, uri string, host any) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leaveLocked(key, uri, host)
}

func (s *Sessions) leaveLocked(key, uri string, host any) bool {
	members := s.subs[key][uri]
	if _, ok := members[host]; !ok {
		return false
	}
	delete(members, host)
	if len(members) > 0 {
		return false
	}
	delete(s.subs[key], uri)
	return true
}
