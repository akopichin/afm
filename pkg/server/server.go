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
	theme          string       // "goga" или "" (default novacorps)
	indexBytes     []byte       // предподготовленный index.html (с заменами для goga)
	fileServer     http.Handler // отдаёт статику (style.css, app.js, ...)
	httpSrv        *http.Server
	// Keepalive-таймауты вебсокета. Immutable: задаются один раз в New и не
	// мутируются после (хранение в полях, а не в глобальных переменных, убирает
	// data race между тестами и readPump/writePump — см. websocket.go).
	wsPongWait   time.Duration
	wsPingPeriod time.Duration
	wsWriteWait  time.Duration
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
	Theme          string
	// Keepalive-таймауты вебсокета. Нулевые значения → дефолты из websocket.go.
	WSPongWait   time.Duration
	WSPingPeriod time.Duration
	WSWriteWait  time.Duration
}

// New creates a Server.
func New(cfg Config) *Server {
	pongWait := cfg.WSPongWait
	if pongWait == 0 {
		pongWait = defaultWSPongWait
	}
	pingPeriod := cfg.WSPingPeriod
	if pingPeriod == 0 {
		pingPeriod = defaultWSPingPeriod
	}
	writeWait := cfg.WSWriteWait
	if writeWait == 0 {
		writeWait = defaultWSWriteWait
	}

	s := &Server{
		runDir:         cfg.RunDir,
		store:          cfg.Store,
		uiBus:          cfg.UIBus,
		approveFn:      cfg.ApproveFn,
		reviseFn:       cfg.ReviseFn,
		retryFn:        cfg.RetryFn,
		dialogAnswerFn: cfg.DialogAnswerFn,
		dialogCancelFn: cfg.DialogCancelFn,
		theme:          cfg.Theme,
		wsPongWait:     pongWait,
		wsPingPeriod:   pingPeriod,
		wsWriteWait:    writeWait,
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
			// Vite собирает index.html с относительными путями (base: './'),
			// поэтому ссылка на стиль — href="./style.css". Подменяем на goga-стиль.
			indexBytes = bytes.ReplaceAll(indexBytes,
				[]byte(`href="./style.css"`), []byte(`href="./style-goga.css"`))
			indexBytes = bytes.ReplaceAll(indexBytes,
				[]byte(`class="theme-novacorps"`), []byte(`class="theme-goga"`))
			indexBytes = bytes.ReplaceAll(indexBytes,
				[]byte(`<title>afm Dashboard</title>`), []byte(`<title>QArium</title>`))
			indexBytes = bytes.ReplaceAll(indexBytes,
				[]byte(`type="image/svg+xml" href="./favicon.svg"`),
				[]byte(`type="image/png" href="./quarium-logo.png"`))
		}
		s.indexBytes = indexBytes
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/stages/", s.routeStages)
	mux.HandleFunc("/ws", s.handleWebSocket)
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
	case strings.HasSuffix(path, "/supervisor"):
		s.handleSupervisor(w, r)
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
