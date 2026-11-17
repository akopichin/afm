package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP flags are validated before anything is created: no run directory and no stage side effects.
func TestRunMCPRefusedFlagsCreateNothing(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "mcp without a port", args: []string{"--mcp", "--port", "0"}, want: "--mcp needs the HTTP API"},
		{name: "allow without a port", args: []string{"--mcp-allow", "reply", "--port", "0"}, want: "--mcp-allow needs the HTTP API"},
		{name: "off with mcp", args: []string{"--mcp-off", "--mcp"}, want: "--mcp-off cannot be combined"},
		{name: "off with allow", args: []string{"--mcp-off", "--mcp-allow", "read"}, want: "--mcp-off cannot be combined"},
		{name: "read mixed with groups", args: []string{"--mcp-allow", "read,reply"}, want: "cannot be combined"},
		{name: "unknown group", args: []string{"--mcp", "--port", "8080", "--mcp-allow", "reply,all"}, want: `unknown MCP permission group "all"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newCycleFixture(t)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("AFM_EXIT_ON_FAIL", "")
			t.Setenv("AFM_DIR", "")
			t.Setenv("AFM_WORKTREES", "0")
			t.Setenv("AFM_USE_DOCKER", "0")
			t.Setenv("AFM_BROWSER", "0")
			fx.write("flow.yaml", fx.flowYAML(flowSpec{}))

			args := append([]string{"run", fx.flowPath, "--dir", fx.root}, tt.args...)
			var code int
			_, stderr := captureOutput(t, func() { code = runMain(args) })
			if code == 0 {
				t.Fatal("runMain exit code = 0, want non-zero")
			}
			if !strings.Contains(stderr, tt.want) {
				t.Fatalf("stderr = %q, want %q", stderr, tt.want)
			}
			if _, err := os.Stat(filepath.Join(fx.root, ".afm", "runs")); !os.IsNotExist(err) {
				t.Errorf("runs directory exists after refused flags (stat err = %v)", err)
			}
			if _, err := os.Stat(fx.tickLog); !os.IsNotExist(err) {
				t.Errorf("stage script ran: tick marker exists (stat err = %v)", err)
			}
		})
	}
}

// /mcp is mounted by default in both modes with every permission group.
func TestRunMCPMountsByDefault(t *testing.T) {
	for _, mode := range bothModes {
		t.Run(mode, func(t *testing.T) {
			fx := newModeFixture(t, mode, "auto.yaml", "")
			child := fx.start()
			fx.waitReady(child)
			child.waitOutput(t, "mcp: "+fx.base()+"/mcp")

			names := mcpToolNames(t, fx.base()+"/mcp")
			for _, write := range []string{"afm_interaction_reply", "afm_stage_decide", "afm_stage_pause", "afm_budget_adjust"} {
				if !slices.Contains(names, write) {
					t.Errorf("tool %s missing by default: %v", write, names)
				}
			}
		})
	}
}

func TestRunMCPReadOnlyKeyword(t *testing.T) {
	fx := newModeFixture(t, modeCore, "auto.yaml", "")
	child := fx.start("--mcp-allow", "read")
	fx.waitReady(child)

	names := mcpToolNames(t, fx.base()+"/mcp")
	for _, write := range []string{"afm_interaction_reply", "afm_stage_decide", "afm_stage_pause", "afm_budget_adjust"} {
		if slices.Contains(names, write) {
			t.Errorf("tool %s listed with --mcp-allow read: %v", write, names)
		}
	}
}

func TestRunMCPAcceptsNamedGroups(t *testing.T) {
	fx := newModeFixture(t, modeCore, "auto.yaml", "")
	child := fx.start("--mcp", "--mcp-allow", "reply,stages,approve,budget")
	fx.waitReady(child)
	mcpToolNames(t, fx.base()+"/mcp")
}

// With --mcp-off the route answers JSON, not the SPA fallback.
func TestRunMCPOff(t *testing.T) {
	for _, mode := range bothModes {
		t.Run(mode, func(t *testing.T) {
			fx := newModeFixture(t, mode, "auto.yaml", "")
			child := fx.start("--mcp-off")
			fx.waitReady(child)

			code, body, header := fx.call(http.MethodPost, "/mcp", "{}", nil)
			if code != http.StatusNotFound || !strings.Contains(body, "mcp_disabled") || !strings.Contains(header.Get("Content-Type"), "json") {
				t.Errorf("POST /mcp = %d %q (%s), want 404 JSON mcp_disabled", code, body, header.Get("Content-Type"))
			}
			if strings.Contains(child.out.String(), "mcp:") {
				t.Errorf("mcp line announced with --mcp-off:\n%s", child.out.String())
			}
		})
	}
}

// mcpToolNames connects a go-sdk client over HTTP and lists the tools.
func mcpToolNames(t *testing.T, endpoint string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatalf("connect %s: %v", endpoint, err)
	}
	defer session.Close() //nolint:errcheck
	var names []string
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("list tools: %v", err)
		}
		names = append(names, tool.Name)
	}
	return names
}
