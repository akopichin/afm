package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/pkg/server"
)

var ansiSequence = regexp.MustCompile("\033\\[[0-9;]*m")

func TestPrintDashboardURL(t *testing.T) {
	const url = "http://localhost:8001"
	const plainBlock = "\n  ➜  dashboard: http://localhost:8001\n\n"
	const plainBlockWithHint = "\n  ➜  dashboard: http://localhost:8001\n" +
		"     open this URL in your browser to follow the run\n\n"

	tests := []struct {
		name  string
		hint  bool
		color bool
		plain string
	}{
		{"plain without hint", false, false, plainBlock},
		{"plain with hint", true, false, plainBlockWithHint},
		{"color without hint", false, true, plainBlock},
		{"color with hint", true, true, plainBlockWithHint},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			printDashboardURL(&buf, url, tt.hint, tt.color)
			got := buf.String()

			if !tt.color {
				if got != tt.plain {
					t.Fatalf("output = %q, want %q", got, tt.plain)
				}
				return
			}
			if stripped := ansiSequence.ReplaceAllString(got, ""); stripped != tt.plain {
				t.Fatalf("output without ANSI = %q, want %q", stripped, tt.plain)
			}
			wantStyled := []string{
				colorGreen + "➜" + colorReset,
				colorBold + "dashboard:" + colorReset,
				colorBold + colorCyan + url + colorReset,
			}
			if tt.hint {
				wantStyled = append(wantStyled, colorGray+"open this URL in your browser to follow the run"+colorReset)
			}
			for _, want := range wantStyled {
				if !strings.Contains(got, want) {
					t.Errorf("colored output %q lacks %q", got, want)
				}
			}
		})
	}
}

func TestDashboardColorEnabled(t *testing.T) {
	t.Run("NO_COLOR set", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		if dashboardColorEnabled() {
			t.Fatal("colour enabled although NO_COLOR is set")
		}
	})
	t.Run("stdout is a pipe", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		captureStdout(t, func() {
			if dashboardColorEnabled() {
				t.Error("colour enabled although stdout is not a terminal")
			}
		})
	})
}

// The run's config reaches the dashboard: configured names label the roster and the fallback prefix is sent.
func TestStartDashboard_PassesCommandNamesToServer(t *testing.T) {
	cfg := config.Default()
	cfg.CommandNames = map[string]string{"claude": "House Claude"}
	cfg.UnknownCommandName = "test"
	baseURL := startGraphTestDashboard(t, cfg)

	var status struct {
		UnknownCommandName string `json:"unknown_command_name"`
	}
	decodeDashboardJSON(t, baseURL+"/api/status", &status)
	if status.UnknownCommandName != "test" {
		t.Errorf("unknown_command_name = %q, want test", status.UnknownCommandName)
	}

	type participant struct {
		ID          string `json:"id"`
		Command     string `json:"command"`
		CommandName string `json:"command_name"`
	}
	var roster struct {
		Participants []participant `json:"participants"`
	}
	decodeDashboardJSON(t, baseURL+"/api/mesh/participants", &roster)
	for _, p := range roster.Participants {
		if p.Command == "claude" {
			if p.CommandName != "House Claude" {
				t.Errorf("participant %s command_name = %q, want House Claude", p.ID, p.CommandName)
			}
			return
		}
	}
	t.Fatalf("no participant runs claude in %+v", roster.Participants)
}

func decodeDashboardJSON(t *testing.T, url string, into any) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec // test-only, url is built from a loopback test dashboard address
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
}

// TestStartDashboard_HideAttachHelpersInStatus: startServer resolves config and env once and the result reaches
// /api/status; an explicit env overrides the config value in both directions.
func TestStartDashboard_HideAttachHelpersInStatus(t *testing.T) {
	hide, show := true, false
	tests := []struct {
		name   string
		env    string
		config *bool
		want   bool
	}{
		{name: "default", want: false},
		{name: "config true", config: &hide, want: true},
		{name: "config false", config: &show, want: false},
		{name: "env 1 over config false", env: "1", config: &show, want: true},
		{name: "env 0 over config true", env: "0", config: &hide, want: false},
		{name: "empty env keeps config true", env: "", config: &hide, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AFM_HIDE_ATTACH_HELPERS", tt.env)
			cfg := config.Default()
			cfg.Collaboration.HideAttachHelpers = tt.config
			base := startGraphTestDashboard(t, cfg)

			code, body := getBody(t, base+"/api/status")
			if code != 200 {
				t.Fatalf("GET /api/status = %d: %s", code, body)
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(body), &raw); err != nil {
				t.Fatal(err)
			}
			got, ok := raw["hide_attach_helpers"]
			if !ok {
				t.Fatalf("hide_attach_helpers missing from /api/status: %s", body)
			}
			if want := map[bool]string{true: "true", false: "false"}[tt.want]; string(got) != want {
				t.Errorf("hide_attach_helpers = %s, want %s", got, want)
			}
		})
	}
}

// The dashboard launcher mounts the Web UI; the API server alone does not serve it.
func TestStartDashboard_ServesWebUI(t *testing.T) {
	base := startGraphTestDashboard(t, config.Default())

	code, body := getBody(t, base+"/")
	if code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", code)
	}
	if !strings.Contains(body, `id="root"`) {
		t.Errorf("GET / is not the dashboard index: %.200s", body)
	}
}

func TestOpensHostBrowser(t *testing.T) {
	port, none := 8080, 0
	yes, no := true, false
	tests := []struct {
		name   string
		cfg    config.ServerConfig
		launch launchMode
		want   bool
	}{
		{"dashboard with open_browser", config.ServerConfig{Port: &port, OpenBrowser: &yes}, launchDashboard, true},
		{"core never opens", config.ServerConfig{Port: &port, OpenBrowser: &yes}, launchCore, false},
		{"open_browser off", config.ServerConfig{Port: &port, OpenBrowser: &no}, launchDashboard, false},
		{"no API", config.ServerConfig{Port: &none, OpenBrowser: &yes}, launchDashboard, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := opensHostBrowser(config.Config{Server: tt.cfg}, tt.launch); got != tt.want {
				t.Errorf("opensHostBrowser = %v, want %v", got, tt.want)
			}
		})
	}
}

// Core runs the same composition without a page: the API answers, the UI path does not, and nothing opens a tab.
func TestExecuteFlow_CoreServesAPIWithoutUI(t *testing.T) {
	fx := newExitOnFailFixture(t)
	fx.write("flow.yaml", interactiveFlow)
	f, err := flow.ParseFile(fx.flowPath)
	if err != nil {
		t.Fatal(err)
	}
	port, err := pickFreeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	openTab := true
	cfg := config.Config{
		Client: config.ClientConfig{Command: writePolicyAgent(t)},
		Server: config.ServerConfig{Port: &port, OpenBrowser: &openTab},
	}

	var opened atomic.Int32
	prevOpen := openBrowser
	openBrowser = func(string) { opened.Add(1) }
	t.Cleanup(func() { openBrowser = prevOpen })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout, _ := captureOutput(t, func() {
		flowRun := startBackgroundFlow(ctx, f, cfg, runOptions{launch: launchCore, runName: f.Name})
		defer flowRun.stop(t, cancel)
		// An interactive stage waits for the user's answer: the policies come from the API, not from a page.
		waitFor(t, "the plan to wait for approval", func() bool {
			dirs := fx.runDirs()
			return len(dirs) == 1 && slices.Contains(stageEventsSoFar(filepath.Join(fx.root, ".afm", "runs", dirs[0])), "plan_ready:awaiting_approval")
		})
		base := fmt.Sprintf("http://localhost:%d", port)

		var rt struct {
			Mode  string `json:"mode"`
			Ready bool   `json:"ready"`
		}
		decodeDashboardJSON(t, base+"/api/runtime", &rt)
		if rt.Mode != "core" || !rt.Ready {
			t.Errorf("/api/runtime = %+v, want mode core and ready", rt)
		}
		if code, _ := getBody(t, base+"/"); code != http.StatusNotFound {
			t.Errorf("GET / = %d, want 404 without a Web UI", code)
		}
	})

	if !strings.Contains(stdout, "api: http://localhost:") {
		t.Errorf("stdout lacks the api line: %q", stdout)
	}
	if strings.Contains(stdout, "dashboard:") {
		t.Errorf("core output mentions the dashboard: %q", stdout)
	}
	if got := opened.Load(); got != 0 {
		t.Errorf("browser tab opened %d times in core, want 0", got)
	}
}

func TestStartServer_CoreStartFailureNamesTheAPI(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = busy.Close() })
	port := busy.Addr().(*net.TCPAddr).Port
	cfg := config.Config{Server: config.ServerConfig{Port: &port}}

	_, _, err = startServer(cfg, server.Config{}, nil, launchCore, true)
	if err == nil || !strings.HasPrefix(err.Error(), "start API server:") {
		t.Fatalf("startServer error = %v, want a %q prefix", err, "start API server:")
	}
}

// stageEventsSoFar — stageEvents для журнала, который ран ещё пишет: пустой или оборванный хвост не ошибка.
func stageEventsSoFar(runDir string) []string {
	var out []string
	for _, tr := range eventsSoFar(runDir) {
		if tr.StageID == "build" {
			out = append(out, tr.Event+":"+string(tr.To))
		}
	}
	return out
}
