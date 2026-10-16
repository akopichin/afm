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
	"sync"
	"sync/atomic"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/server/workspace"
	"github.com/akopichin/afm/pkg/state"
	"github.com/akopichin/afm/pkg/web"
)

// themeCustom — активный skin_dir (не значение EffectiveTheme, поэтому не в
// pkg/config; goga/novacorps/graphite берутся из config.Theme*).
const themeCustom = "custom"

// Имена файлов внутри директории скина (встроенной или skin_dir).
const skinIndexFile = "index.css"

// skinFaviconCandidates — имена favicon-файлов скина в порядке поиска;
// расширение первого найденного определяет <link type="..."> (svg/png).
var skinFaviconCandidates = []string{"favicon.svg", "favicon.png"}

const skinTitleFile = "title.txt"

// mimeSVG — MIME-тип favicon по умолчанию (favicon.svg и общий дефолт).
const mimeSVG = "image/svg+xml"

// MIME-типы растровых картинок, используемые в нескольких местах пакета — вынесены в константы, чтобы
// литералы не дублировались.
const (
	mimePNG  = "image/png"
	mimeJPEG = "image/jpeg"
	mimeGIF  = "image/gif"
	mimeWebP = "image/webp"
)

// faviconMIME возвращает MIME-тип favicon по имени файла (расширению).
func faviconMIME(name string) string {
	if strings.HasSuffix(name, ".png") {
		return mimePNG
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

// findSkinFavicon ищет favicon скина через statFn (embed или диск) по skinFaviconCandidates. Возвращает
// относительный href, MIME и true, если нашёл; иначе false — вызывающий использует дефолт.
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
	runDir            string
	Description       string // корневой description флоу (для хедера дашборда)
	stages            RuntimeStageProvider
	store             *state.Store
	uiBus             *bus.UIBus
	accounting        accounting.CostProvider   // nil = accounting unsupported (server built without it); see handleStatus
	showMoney         bool                      // показывать ли денежные ($) величины в дашборде (accounting.show_money); токены показываются всегда
	actions           StageActions              // never nil in practice — see StageActions doc
	secondary         SecondaryActions          // may be nil — see SecondaryActions doc
	flowActions       FlowActions               // review-pause commands; nil = respond 404 (see routeFlow)
	reviewState       func() (string, []string) // lock-free read of flow_pause_state/flow_paused_stages; nil = "none"
	workspace         workspace.FS              // Docker project file browser backend; nil = capability off
	sideAgent         SideAgentService          // «боковой» разговор; nil = capability off (маршруты /api/side-agent/* не регистрируются)
	assistantProfiles AssistantProfilesService  // реестр помощников (collaboration); nil = capability off (/api/assistant-profiles не регистрируется)
	assistantActions  AssistantActions          // write-команды задач помощников; nil = capability off (POST/pause/continue/cancel 404)
	assistantsReader  AssistantsReader          // read-сторона: дерево задач/история событий; nil = capability off (GET 404)
	discussionService DiscussionService         // обсуждение стадии (T6.7); nil = capability off (GET|POST .../discussion 404)
	proposalActions   ProposalActions           // предложения (T6.7); nil = capability off (GET .../proposals, /api/proposals/* 404)
	graphActions      GraphActions              // динамические стадии (Этап 9); nil = capability off (/api/run/graph/* 404)
	theme             string                    // "goga" или "" (default graphite)
	version           string                    // версия afm-бинарника, отдаётся в /api/status для футера дашборда
	indexBytes        []byte                    // предподготовленный index.html (с заменами скина/favicon)
	fileServer        http.Handler              // отдаёт встроенную статику (skins/, assets, ...)
	customSkinServer  http.Handler              // отдаёт /skins/custom/* с диска; nil, если skin_dir не активен
	httpSrv           *http.Server
	// Keepalive-таймауты вебсокета.
	wsPongWait   time.Duration
	wsPingPeriod time.Duration
	wsWriteWait  time.Duration
	// wsClients — число открытых сейчас /ws-соединений. Используется CLI при завершении run'а, чтобы не
	// гасить дашборд, пока за ним кто-то следит (см. waitForDashboardDrain в cmd/afm/run.go).
	wsClients atomic.Int64

	// done закрывается в Shutdown — server-level сигнал для долгоживущих стрим-хендлеров, чтобы они не
	// держали http.Server.Shutdown открытым бесконечно.
	done         chan struct{}
	shutdownOnce sync.Once
}

// ConnectedClients возвращает текущее число открытых /ws-соединений.
func (s *Server) ConnectedClients() int {
	return int(s.wsClients.Load())
}

// stageConfigs returns ONE consistent snapshot of every stage's static config — callers building a response
// that spans multiple stages (handleStatus/buildStageViews) must call this exactly once and reuse the
// result (see RuntimeStageProvider's doc comment).
func (s *Server) stageConfigs() map[string]StageConfig {
	if s.stages == nil {
		return nil
	}
	return s.stages.StageConfigs()
}

// stageConfig returns one stage's static config (zero value if unknown or no provider is configured).
func (s *Server) stageConfig(id string) StageConfig {
	return s.stageConfigs()[id]
}

// StageConfig is the immutable, per-stage configuration used by the dashboard.
// Runtime status and filesystem-derived flags belong to StageView.
type StageConfig struct {
	Interactive bool
	AutoApprove bool
	IsScript    bool
	DependsOn   []string
	Buttons     []string // Labels only; the orchestrator resolves prompts on click.
}

// RuntimeStageProvider is the dashboard's one source of per-stage static configuration (T8.3). Unlike a bare
// map captured once at startup, an implementation backed by the orchestrator's runtime registry
// (cmd/afm/run_dashboard.go) re-derives its answer from the registry's CURRENT revision on every call — so a
// dashboard built before a registry change (Этап 9: dynamic stages) still reflects it, without this package
// needing to know anything about registries or revisions.
//
// A single call must return every stage's config computed from ONE consistent registry revision: a caller
// building a response that joins several stages (buildStageViews) must call StageConfigs() exactly ONCE and
// reuse the result, never once per stage — otherwise two stages in the same response could be drawn from two
// different registry revisions (TestStatusRead_NeverMixesTwoGraphVersions).
type RuntimeStageProvider interface {
	StageConfigs() map[string]StageConfig
}

// StaticStageConfigs adapts a fixed map to RuntimeStageProvider — for tests, and for any caller that has no
// versioned registry to back it.
type StaticStageConfigs map[string]StageConfig

// StageConfigs implements RuntimeStageProvider.
func (m StaticStageConfigs) StageConfigs() map[string]StageConfig { return map[string]StageConfig(m) }

// Config holds server settings.
type Config struct {
	Port        int
	RunDir      string
	Description string // корневой description флоу (для хедера дашборда)
	// Stages — provider of per-stage static config; nil means no stages (e.g. no flow loaded yet). Production
	// wiring passes an orchestrator-registry-backed provider (cmd/afm/run_dashboard.go); tests can use a plain
	// StaticStageConfigs map.
	Stages RuntimeStageProvider
	Store  *state.Store
	// Accounting — источник данных о стоимости/токенах run'а для /api/status.
	Accounting accounting.CostProvider
	// ShowMoney включает показ денежных ($) величин в дашборде
	// (accounting.show_money). Токены отображаются всегда, независимо от флага.
	ShowMoney   bool
	UIBus       *bus.UIBus
	Actions     StageActions
	Secondary   SecondaryActions
	FlowActions FlowActions
	ReviewState func() (string, []string)
	Workspace   workspace.FS     // Docker project file browser backend; nil = capability off
	SideAgent   SideAgentService // «боковой» разговор; nil = capability off
	// AssistantProfiles — реестр помощников (collaboration); nil = capability off.
	AssistantProfiles AssistantProfilesService
	// AssistantActions/AssistantsReader — write/read стороны задач помощников; nil = capability off.
	AssistantActions AssistantActions
	AssistantsReader AssistantsReader
	// DiscussionService/ProposalActions — обсуждения и предложения (T6.7); nil = capability off.
	DiscussionService DiscussionService
	ProposalActions   ProposalActions
	// GraphActions — Этап 9's dynamic-stage API; nil = capability off.
	GraphActions GraphActions
	Theme        string
	// Version — версия afm-бинарника, отдаётся в /api/status для футера дашборда.
	Version string
	SkinDir string
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
		runDir:            cfg.RunDir,
		Description:       cfg.Description,
		stages:            cfg.Stages,
		store:             cfg.Store,
		uiBus:             cfg.UIBus,
		accounting:        cfg.Accounting,
		showMoney:         cfg.ShowMoney,
		actions:           cfg.Actions,
		secondary:         cfg.Secondary,
		flowActions:       cfg.FlowActions,
		reviewState:       cfg.ReviewState,
		workspace:         cfg.Workspace,
		sideAgent:         cfg.SideAgent,
		assistantProfiles: cfg.AssistantProfiles,
		assistantActions:  cfg.AssistantActions,
		assistantsReader:  cfg.AssistantsReader,
		discussionService: cfg.DiscussionService,
		proposalActions:   cfg.ProposalActions,
		graphActions:      cfg.GraphActions,
		theme:             cfg.Theme,
		version:           cfg.Version,
		wsPongWait:        pongWait,
		wsPingPeriod:      pingPeriod,
		wsWriteWait:       writeWait,
		fileServer:        http.FileServer(http.FS(web.FS)),
		done:              make(chan struct{}),
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

	// skin_dir полностью подменяет активный скин: нужен index.css внутри директории, иначе — предупреждение и
	// fallback на встроенный скин.
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

	// Предподготовка index.html: подменяем ссылку на CSS, класс body, favicon и, если у скина есть title.txt,
	// <title>. Замены строковые — сами файлы скина отдаст fileServer/customSkinServer позже.
	indexBytes, err := fs.ReadFile(web.FS, "index.html")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: read embedded index.html: %v\n", err)
	} else {
		indexBytes = bytes.ReplaceAll(indexBytes,
			[]byte(`href="`+skinHrefFor(config.ThemeGraphite)+`"`), []byte(`href="`+skinHref+`"`))
		indexBytes = bytes.ReplaceAll(indexBytes,
			[]byte(`class="theme-`+config.ThemeGraphite+`"`), []byte(`class="theme-`+skinName+`"`))
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
	mux.HandleFunc("/api/files/", s.routeFiles)
	mux.HandleFunc("/api/flow/", s.routeFlow)
	// Маршруты бокового агента регистрируются только при включённой фиче — иначе запрос доходит до статики и
	// возвращает 404. Нужны обе формы: точный путь и поддерево (/stream, /messages…).
	if s.sideAgent != nil {
		mux.HandleFunc("/api/side-agent", s.routeSideAgent)
		mux.HandleFunc("/api/side-agent/", s.routeSideAgent)
	}
	// Маршрут профилей помощников — только при включённой collaboration (capability off → 404, та же схема,
	// что у бокового агента выше).
	if s.assistantProfiles != nil {
		mux.HandleFunc("/api/assistant-profiles", s.handleAssistantProfiles)
	}
	// /api/stages/{id}/assistants is handled inside routeStages (always registered) — capability-off is
	// checked per-handler, same as every other stage sub-route. /api/assistants/{id}/... (task-scoped
	// actions/events) gets its own mux entry, same conditional-registration convention as side-agent above.
	if s.assistantActions != nil || s.assistantsReader != nil {
		mux.HandleFunc("/api/assistants/", s.routeAssistants)
	}
	// /api/stages/{id}/discussion and /api/stages/{id}/proposals are handled inside routeStages (always
	// registered) — capability-off is checked per-handler, same as /assistants above. /api/proposals/{id}/
	// {accept|dismiss} gets its own mux entry, same conditional-registration convention as side-agent/assistants.
	if s.proposalActions != nil {
		mux.HandleFunc("/api/proposals/", s.routeProposals)
	}
	// /api/run/graph/{preview|commit} (Этап 9) — same conditional-registration convention as above.
	if s.graphActions != nil {
		mux.HandleFunc("/api/run/graph/", s.routeRunGraph)
	}
	mux.HandleFunc("/ws", s.handleWebSocket)
	if s.customSkinServer != nil {
		// custom skin CSS — тоже фиксированное имя, не кэшируем (см. serveStatic).
		mux.Handle(customSkinRoute, http.StripPrefix(customSkinRoute, noStore(s.customSkinServer)))
	}
	mux.HandleFunc("/", s.serveStatic)

	s.httpSrv = &http.Server{
		Addr:              dashboardListenAddr(cfg.Port, config.ReExecedIntoContainer()),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// dashboardListenAddr выбирает адрес прослушивания.
func dashboardListenAddr(port int, inContainer bool) string {
	if inContainer {
		return fmt.Sprintf(":%d", port)
	}
	return fmt.Sprintf("127.0.0.1:%d", port)
}

// builtinSkinName нормализует Theme до имени встроенного скина: "goga", "novacorps" или дефолт "graphite"
// (сюда же попадает устаревший алиас "coffee", пустое и неизвестные значения).
func (s *Server) builtinSkinName() string {
	switch s.theme {
	case config.ThemeGoga:
		return config.ThemeGoga
	case config.ThemeNovacorps:
		return config.ThemeNovacorps
	default:
		return config.ThemeGraphite
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

// serveStatic отдаёт index.html для "/" и "/index.html", остальную статику делегирует на FileServer.
func noStore(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, must-revalidate")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.URL.Path == "/index.html" {
		s.serveIndex(w, r)
		return
	}
	// Скины отдаются с ФИКСИРОВАННЫМИ именами, в отличие от контент-хэшированного JS-бандла. no-store
	// заставляет браузер всегда брать свежий CSS; для локального дашборда это дёшево.
	if strings.HasPrefix(r.URL.Path, "/skins/") {
		w.Header().Set("Cache-Control", "no-store, must-revalidate")
	}
	s.fileServer.ServeHTTP(w, r)
}

// dashboardCSP — Content-Security-Policy для документа дашборда.
const dashboardCSP = "default-src 'self'; " +
	"img-src 'self' data: blob:; " +
	"style-src 'self' 'unsafe-inline'; " +
	"script-src 'self' 'sha256-lZhgtiuhrcX+JCIbk+3Pi4xUGHjw107eJYKPorMPpdQ='; " +
	"connect-src 'self' ws: wss:; " +
	"font-src 'self' data:; " +
	"base-uri 'self'; object-src 'none'; frame-ancestors 'none'"

// serveIndex отдаёт предподготовленный index.html. Если embed-чтение не удалось
// (indexBytes пуст), fallback на FileServer — защита от регрессии embed.
func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	// index.html тоже с фиксированным именем и подменяется на лету (скин/ favicon) — не должен кэшироваться,
	// иначе браузер отдаст старую ссылку на assets/skins после пересборки.
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	w.Header().Set("Content-Security-Policy", dashboardCSP)
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
	case strings.HasSuffix(path, "/approve") && r.Method == http.MethodPost:
		s.handleApprove(w, r)
	case strings.HasSuffix(path, "/revise") && r.Method == http.MethodPost:
		s.handleRevise(w, r)
	case strings.HasSuffix(path, "/note") && r.Method == http.MethodPost:
		s.handleStageNote(w, r)
	case strings.HasSuffix(path, "/button") && r.Method == http.MethodPost:
		s.handleStageButton(w, r)
	case strings.HasSuffix(path, "/retry") && r.Method == http.MethodPost:
		s.handleRetry(w, r)
	case strings.HasSuffix(path, "/pause") && r.Method == http.MethodPost:
		s.handlePause(w, r)
	case strings.HasSuffix(path, "/continue") && r.Method == http.MethodPost:
		s.handleContinue(w, r)
	case strings.HasSuffix(path, "/retry-hook") && r.Method == http.MethodPost:
		s.handleRetryHook(w, r)
	case strings.HasSuffix(path, "/skip-hook") && r.Method == http.MethodPost:
		s.handleSkipHook(w, r)
	case strings.HasSuffix(path, "/attachments") && r.Method == http.MethodPost:
		s.handleUploadAttachment(w, r)
	case strings.Contains(path, "/artifacts/") && r.Method == http.MethodGet:
		s.handleArtifact(w, r)
	case strings.Contains(path, "/verify/") && strings.HasSuffix(path, "/report") && r.Method == http.MethodGet:
		s.handleVerifyReport(w, r)
	case strings.HasSuffix(path, "/dialog") && r.Method == http.MethodGet:
		s.handleDialogGet(w, r)
	case strings.HasSuffix(path, "/dialog/answer") && r.Method == http.MethodPost:
		s.handleDialogAnswer(w, r)
	case strings.HasSuffix(path, "/dialog/cancel") && r.Method == http.MethodPost:
		s.handleDialogCancel(w, r)
	case strings.HasSuffix(path, "/assistants") && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		s.routeStageAssistants(w, r, extractStageID(path, "/api/stages/", "/assistants"))
	case strings.HasSuffix(path, "/discussion") && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		s.routeStageDiscussion(w, r, extractStageID(path, "/api/stages/", "/discussion"))
	case strings.HasSuffix(path, "/proposals") && r.Method == http.MethodGet:
		s.routeStageProposals(w, r, extractStageID(path, "/api/stages/", "/proposals"))
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
	s.shutdownOnce.Do(func() { close(s.done) })
	err := s.httpSrv.Shutdown(ctx)
	if s.workspace != nil {
		_ = s.workspace.Close()
	}
	return err
}
