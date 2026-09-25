package main

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/server"
)

var (
	_ server.StageActions     = (*orchestrator.Orchestrator)(nil)
	_ server.SecondaryActions = (*orchestrator.Orchestrator)(nil)
)

func dashboardStageConfig(stages []flow.Stage) map[string]server.StageConfig {
	configs := make(map[string]server.StageConfig, len(stages))
	for _, s := range stages {
		configs[s.ID] = server.StageConfig{
			Interactive: s.Interactive,
			AutoApprove: s.AutoApprove,
			IsScript:    s.IsScript(),
			DependsOn:   s.DependsOn,
			Buttons:     s.Buttons.Labels(),
		}
	}
	return configs
}

// startDashboard transfers workspace ownership to the server, including on failure.
func startDashboard(cfg config.Config, sc server.Config, orch *orchestrator.Orchestrator) (*server.Server, error) {
	if cfg.Server.GetPort() <= 0 {
		return nil, nil
	}
	sc.Port = cfg.Server.GetPort()
	sc.Theme = cfg.EffectiveTheme()
	sc.SkinDir = cfg.SkinDir
	sc.ShowMoney = cfg.Accounting.ShowMoneyEnabled()
	sc.UIBus = orch.UIBus()
	sc.Actions, sc.Secondary, sc.FlowActions = orch, orch, orch
	sc.ReviewState = orch.ReviewState
	srv := server.New(sc)
	addr, err := srv.Start()
	if err != nil {
		_ = srv.Shutdown(context.Background())
		return nil, fmt.Errorf("start dashboard: %w", err)
	}
	// Listener addresses such as [::]:port are replaced with a client-facing URL.
	_, port, _ := net.SplitHostPort(addr)
	dashURL := fmt.Sprintf("http://localhost:%s", port) //nolint:revive // local dashboard is http
	orch.SetDashboardURL(dashURL)
	fmt.Printf("  dashboard: %s\n", dashURL)
	if cfg.Server.IsOpenBrowser() {
		if !config.InContainer() {
			openBrowser(dashURL)
		}
	} else {
		fmt.Println("  → open this URL in your browser to follow the run")
	}
	return srv, nil
}

// browserCmd возвращает команду открытия браузера для текущей ОС
// ("open" на macOS, "xdg-open" на Linux) или "" для неподдерживаемой ОС.
func browserCmd() string {
	switch runtime.GOOS {
	case "darwin":
		return "open"
	case "linux":
		return "xdg-open"
	default:
		return ""
	}
}

func openBrowser(url string) {
	cmd := browserCmd()
	if cmd == "" {
		return
	}
	//nolint:gosec // opening a local URL in the browser is safe
	_ = exec.Command(cmd, url).Start()
}

// launchHostBrowserOpener запускает на хосте фоновый помощник, который ждёт,
// пока dashboard поднимется на port, и открывает URL в браузере хоста.
// Нужен только для Docker-режима: afm внутри Linux-контейнера сам открыть
// браузер на macOS-хосте не может. Помощник — отдельный процесс (Start без
// Wait), поэтому он переживает syscall.Exec родителя, заменяющего afm на docker.
func launchHostBrowserOpener(port int) {
	openCmd := browserCmd()
	if openCmd == "" {
		return
	}
	url := fmt.Sprintf("http://localhost:%d", port)
	// Опрашиваем порт до ~60с; открываем браузер при первом ответе и выходим.
	script := fmt.Sprintf(`for i in $(seq 1 60); do curl -sf -m 1 %s >/dev/null 2>&1 && %s %s && break; sleep 1; done`, url, openCmd, url)
	c := exec.Command("sh", "-c", script)
	c.Stdin = nil
	c.Stdout = nil
	c.Stderr = nil
	//nolint:gosec // скрипт собран из констант и int-порта, не из пользовательского ввода
	_ = c.Start()
}

const extYAML = ".yaml"
const extYML = ".yml"

// dashboardExitGraceMinimum — безусловная пауза перед завершением процесса после
// успеха флоу, если поднят дашборд. Фронтенд опрашивает /api/status каждые 3с
// (POLL_INTERVAL_MS в use-status.ts) и обновляется по WS; 5с хватает, чтобы UI
// гарантированно увидел терминальный статус (done/failed), пока вкладка
// браузера активна.
const dashboardExitGraceMinimum = 5 * time.Second

// dashboardExitGraceMaximum — верхняя граница суммарного ожидания, пока к
// дашборду подключён хотя бы один WS-клиент. Свёрнутая/неактивная вкладка
// браузера троттлится браузером сильнее для setInterval-поллинга /api/status,
// чем для уже открытого WS-соединения — dashboardExitGraceMinimum один в этом
// случае недостаточен, UI «залипает» на последнем статусе (см. use-status.ts).
// Ограничена сверху, чтобы процесс (и, в Docker-режиме, контейнер) не завис
// навсегда из-за незакрытой вкладки.
const dashboardExitGraceMaximum = 2 * time.Minute

// dashboardDrainPoll — как часто проверять число подключённых WS-клиентов
// в течение dashboardExitGraceMaximum.
const dashboardDrainPoll = 2 * time.Second

// waitForDashboardDrain держит дашборд открытым после успешного завершения
// флоу: сначала dashboardExitGraceMinimum безусловно, затем — пока
// connectedClients() > 0, но не дольше dashboardExitGraceMaximum суммарно.
// Ctrl-C (ctx.Done()) прерывает ожидание немедленно.
func waitForDashboardDrain(ctx context.Context, connectedClients func() int) {
	waitForDashboardDrainWithTiming(ctx, connectedClients, dashboardExitGraceMinimum, dashboardExitGraceMaximum, dashboardDrainPoll)
}

// waitForDashboardDrainWithTiming — тело waitForDashboardDrain с
// параметризованными длительностями (тесты подставляют миллисекунды вместо
// реальных minGrace/maxGrace/pollInterval).
func waitForDashboardDrainWithTiming(ctx context.Context, connectedClients func() int, minGrace, maxGrace, pollInterval time.Duration) {
	select {
	case <-time.After(minGrace):
	case <-ctx.Done():
		return
	}

	deadline := time.Now().Add(maxGrace)
	for connectedClients() > 0 && time.Now().Before(deadline) {
		select {
		case <-time.After(pollInterval):
		case <-ctx.Done():
			return
		}
	}
}

// serverAccountingProvider decides what the dashboard server sees as its
// accounting.CostProvider: the live *Store when accounting.Open succeeded
// (acct != nil), accounting.StaticUnavailable() when it hard-failed to open
// (acct == nil, acctErr != nil — e.g. permission denied), or nil (accounting
// unsupported for this server) in the (currently unreachable in practice,
// see accounting.Open's doc) case of neither. nil vs StaticUnavailable are
// deliberately distinct in the API: nil omits every accounting field from
// /api/status, StaticUnavailable reports health:"unavailable" explicitly.
//
// display=false (accounting.enabled: false / AFM_ACCOUNTING=0) returns nil
// regardless of the ledger: the dashboard omits every cost field and renders
// no tile/tab/rail, while collection into usage.jsonl (via Options.Accounting)
// keeps running untouched — the switch hides the display, not the data.
func serverAccountingProvider(display bool, acct *accounting.Store, acctErr error) accounting.CostProvider {
	if !display {
		return nil
	}
	switch {
	case acct != nil:
		return acct
	case acctErr != nil:
		return accounting.StaticUnavailable()
	default:
		return nil
	}
}
