package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	implorchestrator "github.com/akopichin/afm-engine/testkit/orchestrator"
	implstate "github.com/akopichin/afm-engine/testkit/state"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/pkg/server"
)

// startGraphTestDashboard поднимает настоящий dashboard через startServer и возвращает его базовый URL.
// Оркестратор не запускается (Run не вызывается): для проверки capability и маршрутов графа агенты не нужны.
func startGraphTestDashboard(t *testing.T, cfg config.Config) string {
	t.Helper()
	return startTestDashboard(t, cfg, nil)
}

// startTestDashboard is startGraphTestDashboard with a hook that completes the server config before the start.
func startTestDashboard(t *testing.T, cfg config.Config, extend func(sc *server.Config, runDir string, store *implstate.Store, orch *implorchestrator.Orchestrator)) string {
	t.Helper()
	runDir := t.TempDir()
	stages := []flow.Stage{{ID: "work", Name: "Work", Agents: []flow.AgentType{flow.AgentAuto}}}
	store, err := implstate.Open(runDir, []string{"work"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	orch := implorchestrator.New(implorchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: cfg, Prompts: implorchestrator.DefaultPrompts(),
		AddStageDisabled: !cfg.AddStage.IsEnabled(),
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	cfg.Server.Port = &port

	sc := server.Config{RunDir: runDir, Store: store, Stages: registryStageProvider{reader: orchStages{orch}}}
	if extend != nil {
		extend(&sc, runDir, store, orch)
	}
	srv, _, err := startServer(cfg, sc, orch, launchDashboard, false)
	if err != nil {
		t.Fatalf("startServer: %v", err)
	}
	// Cleanup идут в обратном порядке: сервер останавливается раньше, чем закроется Store.
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

func addStageCapability(t *testing.T, baseURL string) bool {
	t.Helper()
	resp, err := http.Get(baseURL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	defer resp.Body.Close()
	var status struct {
		Capabilities map[string]bool `json:"capabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode /api/status: %v", err)
	}
	return status.Capabilities["add_stage"]
}

func postGraph(t *testing.T, baseURL, action string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/run/graph/"+action, strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST graph/%s: %v", action, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestStartDashboard_AddStageDisabledHidesCapabilityAndGraphAPI(t *testing.T) {
	disabled := false
	cfg := config.Default()
	cfg.AddStage.Enabled = &disabled
	baseURL := startGraphTestDashboard(t, cfg)

	if addStageCapability(t, baseURL) {
		t.Fatal("capabilities.add_stage = true with add_stage disabled, want false")
	}
	for _, action := range []string{"preview", "commit"} {
		if got := postGraph(t, baseURL, action); got != http.StatusNotFound {
			t.Fatalf("POST /api/run/graph/%s with add_stage disabled = %d, want 404", action, got)
		}
	}
}

func TestStartDashboard_AddStageEnabledByDefault(t *testing.T) {
	baseURL := startGraphTestDashboard(t, config.Default())

	if !addStageCapability(t, baseURL) {
		t.Fatal("capabilities.add_stage = false by default, want true")
	}
	// Пустой черновик отклоняется валидацией, но маршрут обязан существовать (не 404).
	for _, action := range []string{"preview", "commit"} {
		if got := postGraph(t, baseURL, action); got == http.StatusNotFound {
			t.Fatalf("POST /api/run/graph/%s by default = 404, want the route registered", action)
		}
	}
}
