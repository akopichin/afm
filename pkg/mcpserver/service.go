// Package mcpserver exposes one running AFM run to an MCP client over Streamable HTTP.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm/contracts/accounting"
	"github.com/akopichin/afm/contracts/clock"
	"github.com/akopichin/afm/pkg/server"
)

const codeSessionLimit = "mcp_session_limit"

const (
	sessionIdleTimeout = 30 * time.Minute
	maxSessions        = 16
)

// StageCommands is the part of the engine's stage control MCP may call. Legacy Approve is left out on purpose:
// approval goes through Approval.ApproveExpected only.
type StageCommands interface {
	Revise(ctx context.Context, stageID, feedback string) error
	CheckRevise(stageID string) error
	Retry(ctx context.Context, stageID string) error
	Pause(ctx context.Context, stageID string) error
	Continue(ctx context.Context, stageID string) error
	Answer(stageID, phase, qID, answer string, fromOptions bool) error
}

// Config wires a Service to one run. The service is bound to that run for its whole life; a repeat cycle gets a
// new Service.
type Config struct {
	// RunID defaults to the base name of RunDir.
	RunID  string
	RunDir string
	// Reader and Events are the same read side the dashboard uses.
	Reader server.RunStateReader
	Events server.EventStream
	Stages server.RuntimeStageProvider
	// Commands, Approval and Budgets may be nil; the matching tools then fail with not_allowed.
	Commands StageCommands
	Approval server.ApprovalActions
	Budgets  server.BudgetActions
	Cost     accounting.CostProvider
	// ShowMoney mirrors accounting.show_money; tokens are always reported.
	ShowMoney     bool
	RedactSecrets func(string) string
	Groups        Groups
	Version       string
	// SessionTimeout and MaxSessions bound retained MCP sessions; zero means 30 minutes and 16.
	SessionTimeout time.Duration
	MaxSessions    int
	// Clock defaults to the real clock.
	Clock clock.Clock
}

// Service is the MCP endpoint of one run. It implements server.MCPHandler.
type Service struct {
	cfg    Config
	specs  map[string]ToolSpec
	runID  string
	server *sdkmcp.Server
	http   *sdkmcp.StreamableHTTPHandler

	mu     sync.Mutex
	closed bool
	// creating counts admitted session creations; a reservation holds a slot until its request returns, and
	// Close drains them before it sweeps sessions so none appears after Close.
	creating     int
	creatingWG   sync.WaitGroup
	onNewSession func() // test seam: runs while a creation holds its reservation
	onDrained    func() // test seam: Close has drained admitted creations and is about to sweep
	done         chan struct{}
	// ctx lives until Close and bounds the notifier's reads and notifications.
	ctx    context.Context
	cancel context.CancelFunc

	// notifying, waiting, changed, lastCursor and lastCost belong to the notifier and waits (notify.go, wait.go).
	notifying  bool
	ready      chan struct{} // closed once the notifier has subscribed to the bus and taken its baseline
	waiting    int
	changed    chan struct{}
	lastCursor string
	lastCost   string

	// wg counts running handlers and background goroutines; Close waits for it so nothing reads the run's
	// state after Close returns.
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeDone chan struct{}
}

// New builds the service. It starts no goroutines.
func New(cfg Config) (*Service, error) {
	if cfg.Reader == nil {
		return nil, errors.New("mcpserver: Reader is required")
	}
	runID := cfg.RunID
	if runID == "" && cfg.RunDir != "" {
		runID = filepath.Base(cfg.RunDir)
	}
	if runID == "" {
		return nil, errors.New("mcpserver: RunID or RunDir is required")
	}
	s := &Service{cfg: cfg, specs: catalogByName(), runID: runID, done: make(chan struct{}), closeDone: make(chan struct{}), changed: make(chan struct{}), ready: make(chan struct{})}
	s.ctx, s.cancel = context.WithCancel(context.Background()) //nolint:gosec // Close calls cancel
	s.server = sdkmcp.NewServer(&sdkmcp.Implementation{Name: "afm", Version: cfg.Version}, &sdkmcp.ServerOptions{
		SubscribeHandler:   s.subscribeHandler,
		UnsubscribeHandler: s.unsubscribeHandler,
	})
	s.registerStatusTool()
	s.registerEventsTool()
	s.registerArtifactTool()
	s.registerWaitTool()
	s.registerControlTools()
	s.registerResources()
	s.http = sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server {
			if s.onNewSession != nil {
				s.onNewSession()
			}
			return s.server
		},
		&sdkmcp.StreamableHTTPOptions{SessionTimeout: s.sessionTimeout()},
	)
	return s, nil
}

func (s *Service) sessionTimeout() time.Duration {
	if s.cfg.SessionTimeout > 0 {
		return s.cfg.SessionTimeout
	}
	return sessionIdleTimeout
}

func (s *Service) maxSessions() int {
	if s.cfg.MaxSessions > 0 {
		return s.cfg.MaxSessions
	}
	return maxSessions
}

func (s *Service) clk() clock.Clock {
	if s.cfg.Clock == nil {
		return clock.Real()
	}
	return s.cfg.Clock
}

// redact masks the run's secret values; without a redactor the text is returned as is.
func (s *Service) redact(text string) string {
	if s.cfg.RedactSecrets == nil {
		return text
	}
	return s.cfg.RedactSecrets(text)
}

func (s *Service) redactAll(texts []string) []string {
	out := make([]string, len(texts))
	for i, t := range texts {
		out[i] = s.redact(t)
	}
	return out
}

func validStage(id string) bool { return server.ValidStageID(id) }

// RunID is the run this service is bound to.
func (s *Service) RunID() string { return s.runID }

// enter registers a running handler. It fails once Close has begun, so no handler starts after Close returns.
func (s *Service) enter() (release func(), err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	s.wg.Add(1)
	return s.wg.Done, nil
}

// checkRun rejects a control call that was made against another run.
func (s *Service) checkRun(runID string) error {
	if runID != s.runID {
		return &ToolError{Code: codeRunChanged, Kind: codeConflict, Message: "run_id does not match the run served by this endpoint"}
	}
	return nil
}

// ServeHTTP serves one MCP request, refusing new ones after Close and new sessions beyond the ceiling. The
// request counts as a running handler until it returns, and Close interrupts its network I/O.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	release, err := s.enter()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, codeClosed)
		return
	}
	defer release()
	if r.Method == http.MethodPost && r.Header.Get("Mcp-Session-Id") == "" {
		code, releaseSlot := s.admitSession()
		if code != "" {
			if code == codeSessionLimit {
				w.Header().Set("Retry-After", "5")
			}
			writeError(w, http.StatusServiceUnavailable, code)
			return
		}
		defer releaseSlot()
	}
	r, stopInterrupt := s.interruptible(w, r)
	defer stopInterrupt()
	s.http.ServeHTTP(w, r)
}

// interruptible ties r to the service: Close cancels its context and fails its pending network reads and
// writes, since an unfinished request body or a client that stopped reading would otherwise hold Close forever.
// stop must run before the handler returns, so the interruption never touches a finished ResponseWriter.
func (s *Service) interruptible(w http.ResponseWriter, r *http.Request) (_ *http.Request, stop func()) {
	ctx, cancel := context.WithCancel(r.Context())
	interrupted := make(chan struct{})
	stopAfter := context.AfterFunc(s.ctx, func() {
		defer close(interrupted)
		cancel()
		// A deadline in the past fails the blocked read or write at once; closing the body first could wait
		// behind that read.
		past := time.Unix(1, 0)
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(past)
		_ = rc.SetWriteDeadline(past)
		_ = r.Body.Close()
	})
	return r.WithContext(ctx), func() {
		if !stopAfter() {
			<-interrupted
		}
		cancel()
	}
}

// admitSession reserves a slot for a session creation, atomically with the closed and capacity checks. A
// non-empty code means refused.
func (s *Service) admitSession() (code string, release func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return codeClosed, nil
	}
	// A creation that has already registered its session is briefly counted twice, which only refuses early.
	if s.sessionCount()+s.creating >= s.maxSessions() {
		return codeSessionLimit, nil
	}
	s.creating++
	s.creatingWG.Add(1)
	return "", func() {
		s.mu.Lock()
		s.creating--
		s.mu.Unlock()
		s.creatingWG.Done()
	}
}

func (s *Service) sessionCount() int {
	n := 0
	for range s.server.Sessions() {
		n++
	}
	return n
}

// Close stops the service: new requests get 503, admitted ones are interrupted, waits and streams are released,
// and every handler has returned before Close does. It is idempotent, safe without any traffic, and bounded by ctx.
func (s *Service) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.done)
		s.cancel()
		go func() {
			defer close(s.closeDone)
			s.creatingWG.Wait()
			if s.onDrained != nil {
				s.onDrained()
			}
			for ss := range s.server.Sessions() {
				_ = ss.Close()
			}
			s.wg.Wait()
		}()
	})
	select {
	case <-s.closeDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}
