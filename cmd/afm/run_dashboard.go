package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
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
	_ server.GraphActions     = (*orchestrator.Orchestrator)(nil)
)

func dashboardStageConfig(stages []flow.Stage) map[string]server.StageConfig {
	configs := make(map[string]server.StageConfig, len(stages))
	for _, s := range stages {
		configs[s.ID] = server.StageConfig{
			Name:        s.Name,
			Interactive: s.Interactive,
			AutoApprove: s.AutoApprove,
			IsScript:    s.IsScript(),
			DependsOn:   s.DependsOn,
			Buttons:     s.Buttons.Labels(),
		}
	}
	return configs
}

// registryStageProvider implements server.RuntimeStageProvider by re-deriving the dashboard-facing config
// from the orchestrator's runtime registry (orchestrator.Orchestrator.Stages) on EVERY call, instead of a
// map frozen at startup (T8.3). Today (Этап 8, no dynamic stage edits wired in yet) orch.Stages() always
// returns the same set the run started with; this adapter is what lets a future registry revision (Этап 9)
// become visible to the dashboard without restarting it.
type registryStageProvider struct {
	orch *orchestrator.Orchestrator
}

// StageConfigs implements server.RuntimeStageProvider.
func (p registryStageProvider) StageConfigs() map[string]server.StageConfig {
	return dashboardStageConfig(p.orch.Stages())
}

// ownerTokenFDEnv names an output descriptor (the write end of a private pipe whose reader is the operator) that
// receives the run owner's token on a headless launch. Only its number is passed; the token never is.
const ownerTokenFDEnv = "AFM_OWNER_TOKEN_FD" //nolint:gosec // an env var name, not a credential

// ownerTokenPipe is the pipe AFM_OWNER_TOKEN_FD named, checked and secured during package initialization: the
// variable is gone before afm starts anything, and the descriptor is not inherited by it. nil — no usable pipe.
var ownerTokenPipe = takeOwnerTokenFD()

// controllingTerminal is where the owner token is shown to an operator at a terminal; tests point it elsewhere.
var controllingTerminal = "/dev/tty"

// takeOwnerTokenFD reads AFM_OWNER_TOKEN_FD and removes it from the environment, valid or not. Descriptors 0-2
// are stdio, where the token would reach the run log, so they are never accepted.
func takeOwnerTokenFD() *os.File {
	raw, set := os.LookupEnv(ownerTokenFDEnv)
	if !set {
		return nil
	}
	_ = os.Unsetenv(ownerTokenFDEnv)
	fd, err := strconv.Atoi(raw)
	if err != nil || fd <= 2 {
		_, _ = fmt.Fprintf(os.Stderr, "warning: %s must be a descriptor number above 2; ignoring it\n", ownerTokenFDEnv)
		return nil
	}
	pipe, err := adoptOwnerTokenFD(fd)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "warning: %s: descriptor %d %v; ignoring it\n", ownerTokenFDEnv, fd, err)
		return nil
	}
	return pipe
}

// releaseOwnerTokenForDocker closes the descriptor before the run moves into a container, which it cannot cross.
func releaseOwnerTokenForDocker(out io.Writer) {
	if ownerTokenPipe == nil {
		return
	}
	_ = ownerTokenPipe.Close()
	ownerTokenPipe = nil
	_, _ = fmt.Fprintf(out, "afm: %s is not supported in Docker mode; owner wait routes need a native run\n", ownerTokenFDEnv)
}

// newOwnerToken is 256 bits of randomness, hex-encoded.
func newOwnerToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// provisionOwnerToken hands a fresh token over once, through pipe or else the controlling terminal. "" (routes
// stay closed) when no channel works; notice on stdout says how, never what.
func provisionOwnerToken(pipe *os.File, notice io.Writer) string {
	token, err := newOwnerToken()
	if err != nil {
		_, _ = fmt.Fprintln(notice, "  owner wait recovery unavailable: cannot generate a token")
		return ""
	}
	channel, err := deliverOwnerToken(pipe, token)
	if err != nil {
		_, _ = fmt.Fprintf(notice, "  owner wait recovery unavailable: %v\n", err)
		return ""
	}
	_, _ = fmt.Fprintf(notice, "  owner token delivered once through %s\n", channel)
	return token
}

// deliverOwnerToken writes token to its one channel and closes it. The errors never carry the token. Inside afm's
// own container /dev/tty is the container's stdout, which the host copies into its log, so there is no terminal
// channel there.
func deliverOwnerToken(pipe *os.File, token string) (channel string, err error) {
	if pipe != nil {
		return "the descriptor in " + ownerTokenFDEnv, writeOnce(pipe, token+"\n")
	}
	if config.ReExecedIntoContainer() {
		return "", errors.New("owner wait routes need a native run (token cannot be delivered safely from the container)")
	}
	tty, err := os.OpenFile(controllingTerminal, os.O_WRONLY, 0)
	if err != nil {
		return "", fmt.Errorf("no controlling terminal and %s is not set", ownerTokenFDEnv)
	}
	return "the controlling terminal", writeOnce(tty, "  owner token (shown once; for the owner wait recovery API): "+token+"\n")
}

func writeOnce(f *os.File, text string) error {
	_, writeErr := f.WriteString(text)
	return errors.Join(writeErr, f.Close())
}

// openBrowser — seam для тестов: вкладку открывает только первый цикл процесса.
var openBrowser = openBrowserTab

// startDashboard transfers workspace ownership to the server, including on failure. announce — вкладку открывают
// и подсказку об URL печатают один раз за процесс (первый цикл); следующий цикл поднимает dashboard тихо.
func startDashboard(cfg config.Config, sc server.Config, orch *orchestrator.Orchestrator, announce bool) (*server.Server, error) {
	// One delivery per process: a later dashboard of a repeated run reaches the operator through the terminal.
	tokenPipe := ownerTokenPipe
	ownerTokenPipe = nil
	if cfg.Server.GetPort() <= 0 {
		if tokenPipe != nil {
			_ = tokenPipe.Close() // nobody will write it; do not hold the operator's pipe open
		}
		return nil, nil
	}
	sc.Port = cfg.Server.GetPort()
	sc.Theme = cfg.EffectiveTheme()
	sc.Version = version
	sc.SkinDir = cfg.SkinDir
	sc.ShowMoney = cfg.Accounting.ShowMoneyEnabled()
	sc.UIBus = orch.UIBus()
	sc.Actions, sc.Secondary, sc.FlowActions = orch, orch, orch
	// Выключенный add_stage: GraphActions остаётся nil-интерфейсом (capability=false, маршруты графа 404).
	if cfg.AddStage.IsEnabled() {
		sc.GraphActions = orch
	}
	sc.AgentMesh = agentMeshServerActions(orch)
	sc.Mesh = meshServerActions(orch)
	if sc.OwnerToken = provisionOwnerToken(tokenPipe, os.Stdout); sc.OwnerToken != "" {
		sc.OwnerWaits = ownerWaitActions{orch}
	}
	sc.RedactSecrets = orch.RedactSecrets
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
	if !announce {
		return srv, nil
	}
	if cfg.Server.IsOpenBrowser() {
		openBrowser(dashURL)
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

func openBrowserTab(url string) {
	cmd := browserCmd()
	if cmd == "" || config.InContainer() {
		return
	}
	//nolint:gosec // opening a local URL in the browser is safe
	_ = exec.Command(cmd, url).Start()
}

// launchHostBrowserOpener запускает на хосте фоновый помощник, который ждёт, пока dashboard поднимется на
// port, и открывает URL в браузере хоста.
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

// dashboardExitGraceMinimum — безусловная пауза перед завершением процесса после успеха флоу, если поднят
// дашборд.
const dashboardExitGraceMinimum = 5 * time.Second

// dashboardExitGraceMaximum — верхняя граница суммарного ожидания, пока к дашборду подключён хотя бы один
// WS-клиент.
const dashboardExitGraceMaximum = 2 * time.Minute

// dashboardDrainPoll — как часто проверять число подключённых WS-клиентов
// в течение dashboardExitGraceMaximum.
const dashboardDrainPoll = 2 * time.Second

// waitForDashboardDrain ждёт минимум graceMinimum, затем подключённых клиентов до graceMaximum; Ctrl+C
// прерывает ожидание.
func waitForDashboardDrain(ctx context.Context, connectedClients func() int) {
	waitForDashboardDrainWithTiming(ctx, connectedClients, dashboardExitGraceMinimum, dashboardExitGraceMaximum, dashboardDrainPoll)
}

// drainDashboard — seam для тестов: финальное ожидание dashboard после последнего цикла.
var drainDashboard = waitForDashboardDrain

// waitForDashboardGrace держит dashboard открытым ровно grace; Ctrl+C прерывает ожидание.
func waitForDashboardGrace(ctx context.Context, grace time.Duration) {
	select {
	case <-time.After(grace):
	case <-ctx.Done():
	}
}

// waitForDashboardDrainWithTiming — тело waitForDashboardDrain с параметризованными длительностями (тесты
// подставляют миллисекунды вместо реальных minGrace/maxGrace/pollInterval).
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

// serverAccountingProvider возвращает live Store или StaticUnavailable при ошибке; nil скрывает cost API,
// сохраняя сбор usage.
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
