package server

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
	"github.com/akopichin/afm/pkg/web"
)

// themeCustom — активный skin_dir (не значение EffectiveTheme, поэтому не в
// pkg/config; goga/novacorps/coffee берутся из config.Theme*).
const themeCustom = "custom"

// Имена файлов внутри директории скина (встроенной или skin_dir).
const skinIndexFile = "index.css"

// skinFaviconCandidates — имена favicon-файлов скина в порядке поиска;
// расширение первого найденного определяет <link type="..."> (svg/png).
var skinFaviconCandidates = []string{"favicon.svg", "favicon.png"}

const skinTitleFile = "title.txt"

// mimeSVG — MIME-тип favicon по умолчанию (favicon.svg и общий дефолт).
const mimeSVG = "image/svg+xml"

// faviconMIME возвращает MIME-тип favicon по имени файла (расширению).
func faviconMIME(name string) string {
	if strings.HasSuffix(name, ".png") {
		return "image/png"
	}
	return mimeSVG
}

// defaultFaviconHref — общий дефолтный favicon, используется, когда у активного
// скина нет своего favicon.svg/favicon.png.
const defaultFaviconHref = "./favicon.svg"

// customSkinRoute — префикс маршрута, отдающего skin_dir с диска.
const customSkinRoute = "/skins/custom/"

// skinHrefFor возвращает относительный href на стиль встроенного скина по имени.
func skinHrefFor(name string) string {
	return "./skins/" + name + "/" + skinIndexFile
}

// findSkinFavicon ищет favicon скина через statFn (embed или диск) по
// skinFaviconCandidates. Возвращает относительный href, MIME и true, если
// нашёл; иначе false — вызывающий использует дефолт.
func findSkinFavicon(base string, statFn func(name string) bool) (href, mime string, found bool) {
	for _, name := range skinFaviconCandidates {
		if statFn(name) {
			return base + "/" + name, faviconMIME(name), true
		}
	}
	return "", "", false
}

// Server is the HTTP server for the dashboard and API.
type Server struct {
	runDir           string
	Description      string          // корневой description флоу (для хедера дашборда)
	stageInteractive map[string]bool // id стадии → interactive (статический конфиг флоу)
	stageAutoApprove map[string]bool // id стадии → auto_approve (статический конфиг флоу)
	store            *state.Store
	uiBus            *bus.UIBus
	approveFn        func(ctx context.Context, stageID string) error
	reviseFn         func(ctx context.Context, stageID, feedback string) error
	retryFn          func(ctx context.Context, stageID string) error
	retryHookFn      func(stageID string) error
	skipHookFn       func(stageID string) error
	dialogAnswerFn   func(stageID, phase, qID, answer string, fromOptions bool) error
	dialogCancelFn   func(stageID string) error
	theme            string       // "goga" или "" (default coffee)
	indexBytes       []byte       // предподготовленный index.html (с заменами скина/favicon)
	fileServer       http.Handler // отдаёт встроенную статику (skins/, assets, ...)
	customSkinServer http.Handler // отдаёт /skins/custom/* с диска; nil, если skin_dir не активен
	httpSrv          *http.Server
	// Keepalive-таймауты вебсокета. Immutable: задаются один раз в New и не
	// мутируются после (хранение в полях, а не в глобальных переменных, убирает
	// data race между тестами и readPump/writePump — см. websocket.go).
	wsPongWait   time.Duration
	wsPingPeriod time.Duration
	wsWriteWait  time.Duration
}

// Config holds server settings.
type Config struct {
	Port             int
	RunDir           string
	Description      string // корневой description флоу (для хедера дашборда)
	StageInteractive map[string]bool
	StageAutoApprove map[string]bool
	Store            *state.Store
	UIBus            *bus.UIBus
	ApproveFn        func(ctx context.Context, stageID string) error
	ReviseFn         func(ctx context.Context, stageID, feedback string) error
	RetryFn          func(ctx context.Context, stageID string) error
	RetryHookFn      func(stageID string) error
	SkipHookFn       func(stageID string) error
	DialogAnswerFn   func(stageID, phase, qID, answer string, fromOptions bool) error
	DialogCancelFn   func(stageID string) error
	Theme            string
	SkinDir          string
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
		runDir:           cfg.RunDir,
		Description:      cfg.Description,
		stageInteractive: cfg.StageInteractive,
		stageAutoApprove: cfg.StageAutoApprove,
		store:            cfg.Store,
		uiBus:            cfg.UIBus,
		approveFn:        cfg.ApproveFn,
		reviseFn:         cfg.ReviseFn,
		retryFn:          cfg.RetryFn,
		retryHookFn:      cfg.RetryHookFn,
		skipHookFn:       cfg.SkipHookFn,
		dialogAnswerFn:   cfg.DialogAnswerFn,
		dialogCancelFn:   cfg.DialogCancelFn,
		theme:            cfg.Theme,
		wsPongWait:       pongWait,
		wsPingPeriod:     pingPeriod,
		wsWriteWait:      writeWait,
		fileServer:       http.FileServer(http.FS(web.FS)),
	}

	skinName := s.builtinSkinName()
	skinHref := skinHrefFor(skinName)
	faviconHref, faviconMimeType, faviconFound := s.embeddedFavicon(skinName)
	if !faviconFound {
		faviconHref, faviconMimeType = defaultFaviconHref, mimeSVG
	}
	titleText := ""
	if data, err := fs.ReadFile(web.FS, "skins/"+skinName+"/"+skinTitleFile); err == nil {
		titleText = strings.TrimSpace(string(data))
	}

	// skin_dir полностью подменяет активный скин (аналогично prompts_dir):
	// нужен index.css внутри директории, иначе — предупреждение и fallback на
	// встроенный скин (theme/coffee). Дашборд не критичен для работы флоу
	// (в отличие от промптов), поэтому сервер не падает при плохом skin_dir.
	if cfg.SkinDir != "" {
		if _, err := os.Stat(filepath.Join(cfg.SkinDir, skinIndexFile)); err != nil {
			fmt.Fprintf(os.Stderr, "warning: skin_dir %q has no %s, using skin %q\n", cfg.SkinDir, skinIndexFile, skinName)
		} else {
			s.customSkinServer = http.FileServer(http.FS(os.DirFS(cfg.SkinDir)))
			skinName = themeCustom
			skinHref = skinHrefFor(themeCustom)
			faviconHref, faviconMimeType = defaultFaviconHref, mimeSVG
			if href, mime, ok := findSkinFavicon("./skins/custom", func(name string) bool {
				_, err := os.Stat(filepath.Join(cfg.SkinDir, name))
				return err == nil
			}); ok {
				faviconHref, faviconMimeType = href, mime
			}
			titleText = ""
			if data, err := os.ReadFile(filepath.Join(cfg.SkinDir, skinTitleFile)); err == nil {
				titleText = strings.TrimSpace(string(data))
			}
		}
	}

	// Предподготовка index.html: подменяем ссылку на CSS, класс body, favicon
	// (href + type) и, если у скина есть title.txt, <title>. Замены строковые —
	// сами файлы скина отдаст fileServer/customSkinServer позже. Ошибка чтения
	// embed невозможна на практике, но обрабатываем: при nil serveIndex
	// делегирует на fileServer.
	indexBytes, err := fs.ReadFile(web.FS, "index.html")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: read embedded index.html: %v\n", err)
	} else {
		indexBytes = bytes.ReplaceAll(indexBytes,
			[]byte(`href="`+skinHrefFor(config.ThemeCoffee)+`"`), []byte(`href="`+skinHref+`"`))
		indexBytes = bytes.ReplaceAll(indexBytes,
			[]byte(`class="theme-`+config.ThemeCoffee+`"`), []byte(`class="theme-`+skinName+`"`))
		indexBytes = bytes.ReplaceAll(indexBytes,
			[]byte(`type="`+mimeSVG+`" href="`+defaultFaviconHref+`"`),
			[]byte(`type="`+faviconMimeType+`" href="`+faviconHref+`"`))
		if titleText != "" {
			indexBytes = bytes.ReplaceAll(indexBytes,
				[]byte(`<title>afm Dashboard</title>`), []byte(`<title>`+titleText+`</title>`))
		}
		s.indexBytes = indexBytes
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/stages/", s.routeStages)
	mux.HandleFunc("/ws", s.handleWebSocket)
	if s.customSkinServer != nil {
		mux.Handle(customSkinRoute, http.StripPrefix(customSkinRoute, s.customSkinServer))
	}
	mux.HandleFunc("/", s.serveStatic)

	s.httpSrv = &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// builtinSkinName нормализует Theme до имени встроенного скина: "goga",
// "novacorps" или дефолт "coffee".
func (s *Server) builtinSkinName() string {
	switch s.theme {
	case config.ThemeGoga:
		return config.ThemeGoga
	case config.ThemeNovacorps:
		return config.ThemeNovacorps
	default:
		return config.ThemeCoffee
	}
}

// embeddedFavicon ищет favicon встроенного скина среди skinFaviconCandidates
// (skins/<name>/favicon.svg или favicon.png в embed).
func (s *Server) embeddedFavicon(skinName string) (href, mime string, found bool) {
	return findSkinFavicon("./skins/"+skinName, func(name string) bool {
		_, err := fs.Stat(web.FS, "skins/"+skinName+"/"+name)
		return err == nil
	})
}

// serveStatic отдаёт index.html (с подставленным скином) для "/" и "/index.html",
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
	case strings.HasSuffix(path, "/retry-hook") && r.Method == http.MethodPost:
		s.handleRetryHook(w, r)
	case strings.HasSuffix(path, "/skip-hook") && r.Method == http.MethodPost:
		s.handleSkipHook(w, r)
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
