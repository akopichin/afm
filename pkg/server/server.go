package server

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
	"github.com/akopichin/afm/pkg/web"
)

// themeGoga — имя goga-темы (см. pkg/config.Config.EffectiveTheme).
// Любое другое значение cfg.Theme (включая пустую строку) — default novacorps.
const themeGoga = "goga"

// Server is the HTTP server for the dashboard and API.
type Server struct {
	runDir         string
	store          *state.Store
	uiBus          *orchestrator.UIBus
	approveFn      func(ctx context.Context, stageID string) error
	reviseFn       func(ctx context.Context, stageID, feedback string) error
	retryFn        func(ctx context.Context, stageID string) error
	dialogAnswerFn func(stageID, phase, qID, answer string, fromOptions bool) error
	dialogCancelFn func(stageID string) error
	accountant     Accountant
	theme          string       // "goga" или "" (default novacorps)
	indexBytes     []byte       // предподготовленный index.html (с заменами для goga)
	fileServer     http.Handler // отдаёт статику (style.css, app.js, ...)
	httpSrv        *http.Server
}

// Config holds server settings.
type Config struct {
	Port           int
	RunDir         string
	Store          *state.Store
	UIBus          *orchestrator.UIBus
	ApproveFn      func(ctx context.Context, stageID string) error
	ReviseFn       func(ctx context.Context, stageID, feedback string) error
	RetryFn        func(ctx context.Context, stageID string) error
	DialogAnswerFn func(stageID, phase, qID, answer string, fromOptions bool) error
	DialogCancelFn func(stageID string) error
	// Accountant — источник данных потребления рана для GET /api/usage. Локальный
	// интерфейс (см. usage_handler.go): *accounting.Accountant удовлетворяет ему
	// структурно. В New передаётся в UsageHandler как есть — тот же экземпляр, без
	// отдельного построения внутри Server.
	Accountant Accountant
	Theme      string
}

// New creates a Server.
func New(cfg Config) *Server {
	s := &Server{
		runDir:         cfg.RunDir,
		store:          cfg.Store,
		uiBus:          cfg.UIBus,
		approveFn:      cfg.ApproveFn,
		reviseFn:       cfg.ReviseFn,
		retryFn:        cfg.RetryFn,
		dialogAnswerFn: cfg.DialogAnswerFn,
		dialogCancelFn: cfg.DialogCancelFn,
		accountant:     cfg.Accountant,
		theme:          cfg.Theme,
		fileServer:     http.FileServer(http.FS(web.FS)),
	}

	// Предподготовка index.html: при theme=="goga" подменяем ссылку на CSS и класс
	// body. Замены строковые — файл style-goga.css для этого не нужен (embed отдаст
	// его позже через fileServer). Ошибка чтения embed невозможна на практике, но
	// обрабатываем: при nil serveIndex делегирует на fileServer.
	indexBytes, err := fs.ReadFile(web.FS, "index.html")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: read embedded index.html: %v\n", err)
	} else {
		if cfg.Theme == themeGoga {
			indexBytes = bytes.ReplaceAll(indexBytes,
				[]byte(`href="style.css"`), []byte(`href="style-goga.css"`))
			indexBytes = bytes.ReplaceAll(indexBytes,
				[]byte(`class="theme-novacorps"`), []byte(`class="theme-goga"`))
		}
		s.indexBytes = indexBytes
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/stages/", s.routeStages)
	mux.HandleFunc("/ws", s.handleWebSocket)
	mux.Handle("/api/usage", UsageHandler(cfg.Accountant))
	mux.HandleFunc("/", s.serveStatic)

	s.httpSrv = &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// serveStatic отдаёт index.html (с подставленной темой) для "/" и "/index.html",
// остальную статику делегирует на FileServer.
func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.URL.Path == "/index.html" {
		s.serveIndex(w, r)
		return
	}
	s.fileServer.ServeHTTP(w, r)
}

// serveIndex отдаёт предподготовленный index.html. Если embed-чтение не удалось
// (indexBytes пуст), fallback на FileServer — защита от регрессии embed.
func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	if len(s.indexBytes) == 0 {
		s.fileServer.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(s.indexBytes)
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
	case strings.HasSuffix(path, "/dialog") && r.Method == http.MethodGet:
		s.handleDialogGet(w, r)
	case strings.HasSuffix(path, "/dialog/answer") && r.Method == http.MethodPost:
		s.handleDialogAnswer(w, r)
	case strings.HasSuffix(path, "/dialog/cancel") && r.Method == http.MethodPost:
		s.handleDialogCancel(w, r)
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
