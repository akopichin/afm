package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/orchestrator"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/web"
)

// Server is the HTTP server for the dashboard and API.
type Server struct {
	runDir    string
	stateFile string
	bus       *orchestrator.EventBus
	approveFn func(stageID string)
	reviseFn  func(stageID, feedback string)
	retryFn   func(stageID string)
	httpSrv   *http.Server
}

// Config holds server settings.
type Config struct {
	Port      int
	RunDir    string
	StateFile string
	Bus       *orchestrator.EventBus
	ApproveFn func(stageID string)
	ReviseFn  func(stageID, feedback string)
	RetryFn   func(stageID string)
}

// New creates a Server.
func New(cfg Config) *Server {
	s := &Server{
		runDir:    cfg.RunDir,
		stateFile: cfg.StateFile,
		bus:       cfg.Bus,
		approveFn: cfg.ApproveFn,
		reviseFn:  cfg.ReviseFn,
		retryFn:   cfg.RetryFn,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/stages/", s.routeStages)
	mux.HandleFunc("/ws", s.handleWebSocket)
	mux.Handle("/", http.FileServer(http.FS(web.FS)))

	s.httpSrv = &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

func (s *Server) routeStages(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/plan"):
		s.handlePlan(w, r)
	case strings.HasSuffix(path, "/log"):
		s.handleLog(w, r)
	case strings.HasSuffix(path, "/approve") && r.Method == http.MethodPost:
		s.handleApprove(w, r)
	case strings.HasSuffix(path, "/revise") && r.Method == http.MethodPost:
		s.handleRevise(w, r)
	case strings.HasSuffix(path, "/retry") && r.Method == http.MethodPost:
		s.handleRetry(w, r)
	default:
		http.NotFound(w, r)
	}
}

// Handler returns the HTTP handler for testing.
func (s *Server) Handler() http.Handler {
	return s.httpSrv.Handler
}

// Start starts the HTTP server. Returns actual address.
func (s *Server) Start() (string, error) {
	ln, err := net.Listen("tcp", s.httpSrv.Addr)
	if err != nil {
		return "", fmt.Errorf("listen: %w", err)
	}
	go func() { _ = s.httpSrv.Serve(ln) }()
	return ln.Addr().String(), nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}
