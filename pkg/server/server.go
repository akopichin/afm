package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/akopichin/afm/contracts/accounting"
	"github.com/akopichin/afm/contracts/assistants"
	"github.com/akopichin/afm/contracts/config"
	bus "github.com/akopichin/afm/contracts/events"
	"github.com/akopichin/afm/contracts/state"
	"github.com/akopichin/afm/pkg/server/workspace"
)

// MIME-типы растровых картинок, используемые в нескольких местах пакета — вынесены в константы, чтобы
// литералы не дублировались.
const (
	mimePNG  = "image/png"
	mimeJPEG = "image/jpeg"
	mimeGIF  = "image/gif"
	mimeWebP = "image/webp"
)

// RunStateReader is the read-only view of the run's state the dashboard needs; the server has no writable store.
type RunStateReader interface {
	Snapshot() state.RunState
	History() ([]state.Transition, error)
}

// EventStream is a subscription to the dashboard event bus, without the right to publish.
type EventStream interface {
	Subscribe(bufSize int) (id uint64, events <-chan bus.Event)
	Unsubscribe(id uint64)
	SubscriberDroppedCount(id uint64) uint64
}

// Server is the HTTP server for the dashboard and API.
type Server struct {
	runDir             string
	Description        string // корневой description флоу (для хедера дашборда)
	stages             RuntimeStageProvider
	store              RunStateReader
	uiBus              EventStream
	accounting         accounting.CostProvider      // nil = accounting unsupported (server built without it); see handleStatus
	showMoney          bool                         // показывать ли денежные ($) величины в дашборде (accounting.show_money); токены показываются всегда
	usageAnalytics     UsageAnalyticsSource         // nil = capability off (GET /api/usage/analytics не регистрируется)
	usageMode          UsageAnalyticsMode           // режим показа аналитики; никогда не пустой (New подставляет time_only)
	usageNow           func() time.Time             // as_of ответа аналитики; подменяется в тестах
	actions            StageActions                 // never nil in practice — see StageActions doc
	secondary          SecondaryActions             // may be nil — see SecondaryActions doc
	flowActions        FlowActions                  // review-pause commands; nil = respond 404 (see routeFlow)
	reviewState        func() (string, []string)    // lock-free read of flow_pause_state/flow_paused_stages; nil = "none"
	workspace          workspace.FS                 // Docker project file browser backend; nil = capability off
	sideAgent          SideAgentService             // «боковой» разговор; nil = capability off (маршруты /api/side-agent/* не регистрируются)
	assistantProfiles  AssistantProfilesService     // реестр помощников (collaboration); nil = capability off (/api/assistant-profiles не регистрируется)
	collaborationMode  assistants.CollaborationMode // режим collaboration (раскладка dashboard, доступность Board); никогда не пустой (New подставляет "team")
	hideAttachHelpers  bool                         // скрыть кнопки ручного прикрепления helper в dashboard; только видимость, не capability
	assistantActions   AssistantActions             // write-команды задач помощников; nil = capability off (POST/pause/continue/cancel 404)
	assistantsReader   AssistantsReader             // read-сторона: дерево задач/история событий; nil = capability off (GET 404)
	discussionService  DiscussionService            // обсуждение стадии (T6.7); nil = capability off (GET|POST .../discussion 404)
	proposalActions    ProposalActions              // предложения (T6.7); nil = capability off (GET .../proposals, /api/proposals/* 404)
	graphActions       GraphActions                 // динамические стадии (Этап 9); nil = capability off (/api/run/graph/* 404)
	agentMesh          AgentMeshActions             // Agent Mesh Phase 3 (T3.1); nil = capability off (/api/agent-mesh/* 404)
	mesh               MeshActions                  // Agent Mesh Phase 7 (T7.1); nil = capability off (/api/mesh/* 404)
	budgets            BudgetActions                // лимиты расходов; nil = capability off (/api/budgets* 404)
	budgetExport       BudgetExportCheck            // проверка «Save to flow.yaml»; nil = save_to_yaml недоступен
	ownerWaits         OwnerWaitActions             // owner wait recovery; nil, or without ownerTokenDigest = its two routes 404
	ownerTokenDigest   *[sha256.Size]byte           // SHA-256 of the run owner's bearer token; the token itself is not kept
	redact             func(string) string          // removes the run's secret values from logged text; nil = environment secrets only
	commandNames       map[string]string            // config command_names; read by commandName
	unknownCommandName string                       // dashboard fallback label prefix, never empty (New defaults it)
	runtime            RuntimeInfo                  // режим и готовность для /api/runtime
	version            string                       // версия afm-бинарника, отдаётся в /api/status для футера дашборда
	ui                 http.Handler                 // Web UI на "/"; nil = API-only, "/" отвечает 404
	httpSrv            *http.Server
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
	// Name is the stage's display name from the runtime registry, so a stage added during the run has one too.
	Name        string
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
	Store  RunStateReader
	// Accounting — источник данных о стоимости/токенах run'а для /api/status.
	Accounting accounting.CostProvider
	// ShowMoney включает показ денежных ($) величин в дашборде
	// (accounting.show_money). Токены отображаются всегда, независимо от флага.
	ShowMoney bool
	// UsageAnalytics — read model «Models & agents»; nil = capability off. Не зависит от Accounting: при
	// выключенном accounting доступно время (UsageModeTimeOnly).
	UsageAnalytics UsageAnalyticsSource
	// UsageMode — режим показа аналитики (см. UsageModeFor); пустое значение читается как time_only.
	UsageMode   UsageAnalyticsMode
	UIBus       EventStream
	Actions     StageActions
	Secondary   SecondaryActions
	FlowActions FlowActions
	ReviewState func() (string, []string)
	Workspace   workspace.FS     // Docker project file browser backend; nil = capability off
	SideAgent   SideAgentService // «боковой» разговор; nil = capability off
	// AssistantProfiles — реестр помощников (collaboration); nil = capability off.
	AssistantProfiles AssistantProfilesService
	// CollaborationMode — режим collaboration (раскладка dashboard и доступность Board); нулевое значение
	// нормализуется в "team" в New. Не зависит от AssistantProfiles (capability).
	CollaborationMode assistants.CollaborationMode
	// HideAttachHelpers прячет в dashboard кнопки ручного прикрепления helper к стадии. Меняет только
	// видимость: attach через Feed, API и CLI остаются доступны.
	HideAttachHelpers bool
	// AssistantActions/AssistantsReader — write/read стороны задач помощников; nil = capability off.
	AssistantActions AssistantActions
	AssistantsReader AssistantsReader
	// DiscussionService/ProposalActions — обсуждения и предложения (T6.7); nil = capability off.
	DiscussionService DiscussionService
	ProposalActions   ProposalActions
	// GraphActions — Этап 9's dynamic-stage API; nil = capability off.
	GraphActions GraphActions
	// AgentMesh — Agent Mesh Phase 3 (T3.1) agent-facing adapter; nil = capability off (/api/agent-mesh/* 404).
	AgentMesh AgentMeshActions
	// Mesh — Agent Mesh Phase 7 (T7.1) UI-facing adapter; nil = capability off (/api/mesh/* 404).
	Mesh MeshActions
	// Budgets — spending limits of the run; nil = capability off (/api/budgets* 404).
	Budgets BudgetActions
	// BudgetExport describes whether a limit can be saved to the flow file; nil leaves the yaml block out.
	BudgetExport BudgetExportCheck
	// OwnerWaits and OwnerToken enable the owner's wait recovery routes under /api/mesh/stages/{id}; both are
	// required, and without the token the routes 404 rather than open to same-origin trust.
	OwnerWaits OwnerWaitActions
	OwnerToken string
	// RedactSecrets removes the run's secret values (environment, secrets.env, live mesh tokens) from text
	// the server logs; nil redacts only afm's environment secrets.
	RedactSecrets func(string) string
	// CommandNames are the configured dashboard names of agent commands (config command_names).
	CommandNames map[string]string
	// UnknownCommandName is the dashboard's fallback label prefix; empty means config.DefaultUnknownCommandName.
	UnknownCommandName string
	// Version — версия afm-бинарника, отдаётся в /api/status для футера дашборда.
	Version string
	// UI — обработчик Web UI на "/"; nil = только API, "/" отвечает 404.
	UI http.Handler
	// Runtime — режим процесса и признак готовности для /api/runtime.
	Runtime RuntimeInfo
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

	collaborationMode := cfg.CollaborationMode
	if collaborationMode == "" {
		collaborationMode = assistants.CollaborationModeTeam
	}
	unknownCommandName := cfg.UnknownCommandName
	if unknownCommandName == "" {
		unknownCommandName = config.DefaultUnknownCommandName
	}

	s := &Server{
		runDir:             cfg.RunDir,
		Description:        cfg.Description,
		stages:             cfg.Stages,
		store:              cfg.Store,
		uiBus:              cfg.UIBus,
		accounting:         cfg.Accounting,
		showMoney:          cfg.ShowMoney,
		usageAnalytics:     cfg.UsageAnalytics,
		usageMode:          usageModeOrClosed(cfg.UsageMode),
		usageNow:           time.Now,
		actions:            cfg.Actions,
		secondary:          cfg.Secondary,
		flowActions:        cfg.FlowActions,
		reviewState:        cfg.ReviewState,
		workspace:          cfg.Workspace,
		sideAgent:          cfg.SideAgent,
		assistantProfiles:  cfg.AssistantProfiles,
		collaborationMode:  collaborationMode,
		hideAttachHelpers:  cfg.HideAttachHelpers,
		assistantActions:   cfg.AssistantActions,
		assistantsReader:   cfg.AssistantsReader,
		discussionService:  cfg.DiscussionService,
		proposalActions:    cfg.ProposalActions,
		graphActions:       cfg.GraphActions,
		agentMesh:          cfg.AgentMesh,
		mesh:               cfg.Mesh,
		budgets:            cfg.Budgets,
		budgetExport:       cfg.BudgetExport,
		ownerWaits:         cfg.OwnerWaits,
		redact:             cfg.RedactSecrets,
		commandNames:       cfg.CommandNames,
		unknownCommandName: unknownCommandName,
		version:            cfg.Version,
		runtime:            cfg.Runtime,
		wsPongWait:         pongWait,
		wsPingPeriod:       pingPeriod,
		wsWriteWait:        writeWait,
		ui:                 cfg.UI,
		done:               make(chan struct{}),
	}

	if cfg.OwnerToken != "" {
		digest := sha256.Sum256([]byte(cfg.OwnerToken))
		s.ownerTokenDigest = &digest
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/runtime", s.handleRuntime)
	mux.HandleFunc("/api/events", s.handleEvents)
	if s.usageAnalytics != nil {
		mux.HandleFunc("/api/usage/analytics", s.handleUsageAnalytics)
	}
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
	// /api/agent-mesh/* (Agent Mesh Phase 3, T3.1) — same conditional-registration convention as above.
	if s.agentMesh != nil {
		mux.HandleFunc("/api/agent-mesh/", s.routeAgentMesh)
	}
	// /api/mesh/* (Agent Mesh Phase 7, T7.1) — UI-facing surface, same conditional-registration convention.
	if s.mesh != nil {
		mux.HandleFunc("/api/mesh/", s.routeMesh)
	}
	// /api/budgets* — same conditional-registration convention as above.
	if s.budgets != nil {
		mux.HandleFunc("/api/budgets", s.routeBudgets)
		mux.HandleFunc("/api/budgets/", s.routeBudgets)
	}
	mux.HandleFunc("/ws", s.handleWebSocket)
	if s.ui != nil {
		mux.Handle("/", s.ui)
	} else {
		mux.HandleFunc("/", http.NotFound)
	}

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
