package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	implorchestrator "github.com/akopichin/afm-engine/testkit/orchestrator"
	implstate "github.com/akopichin/afm-engine/testkit/state"
	"github.com/akopichin/afm/contracts/agentcomm"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/pkg/server"
)

// liveStageAgent is a running stage "impl" whose agent holds a real executor-minted mesh token, the one an agent
// process reads from its descriptor, until release is called.
type liveStageAgent struct {
	orch    *implorchestrator.Orchestrator
	store   *implstate.Store
	ctx     context.Context
	token   string
	release func()
}

func startLiveStageAgent(t *testing.T) liveStageAgent {
	t.Helper()
	return startLiveStageAgentWith(t, func(*implorchestrator.Options) {})
}

// startLiveStageAgentWith is startLiveStageAgent with mutate applied to the orchestrator options, which already
// hold the run's store, before the orchestrator is built.
func startLiveStageAgentWith(t *testing.T, mutate func(*implorchestrator.Options)) liveStageAgent {
	t.Helper()
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	releasePath := filepath.Join(dir, "release")
	script := filepath.Join(dir, "stage-agent.sh")
	scriptBody := `#!/bin/sh
set -eu
sed -n 's/.*"token":"\([^"]*\)".*/\1/p' "$AFM_MESH_DESCRIPTOR" > "$AFM_TEST_TOKEN_PATH.tmp"
mv "$AFM_TEST_TOKEN_PATH.tmp" "$AFM_TEST_TOKEN_PATH"
while [ ! -e "$AFM_TEST_RELEASE_PATH" ]; do sleep 0.02; done
echo done > "$AFM_STAGE_DIR/.done"
echo summary > "$AFM_STAGE_DIR/execution_summary.md"
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"stage text"}]}}'
`
	if err := os.WriteFile(script, []byte(scriptBody), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AFM_TEST_TOKEN_PATH", tokenPath)
	t.Setenv("AFM_TEST_RELEASE_PATH", releasePath)

	runDir := t.TempDir()
	store, err := implstate.Open(runDir, []string{"impl"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	opts := implorchestrator.Options{
		RunDir: runDir, RunID: "wait-token-test", HumanControl: true, Store: store, Config: config.Default(), Prompts: implorchestrator.DefaultPrompts(),
		Stages: []flow.Stage{{ID: "impl", Name: "impl", Agents: []flow.AgentType{flow.AgentAuto}, Command: script}},
	}
	mutate(&opts)
	orch := implorchestrator.New(opts)
	httpServer := httptest.NewServer(server.New(server.Config{AgentMesh: agentMeshActions{Mesh: orch}, Mesh: meshActions{orch}}).Handler())
	t.Cleanup(httpServer.Close)
	orch.SetAgentAPIURL(httpServer.URL) // without a mesh endpoint the agent gets no descriptor
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	runDone := make(chan struct{})
	go func() { _ = orch.Run(ctx); close(runDone) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return within 10s of cancellation")
		}
	})

	var token string
	waitSideMesh(t, func() bool {
		raw, err := os.ReadFile(tokenPath)
		token = string(raw)
		return err == nil && token != ""
	})
	return liveStageAgent{
		orch: orch, store: store, ctx: ctx, token: strings.TrimSpace(token),
		release: func() {
			if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
}

// The wait of a stage agent is decided against the invocation whose token authenticated it.
func TestAgentMeshWait_IsDecidedAgainstTheInvocationOfItsToken(t *testing.T) {
	agent := startLiveStageAgent(t)
	orch, store, ctx, token := agent.orch, agent.store, agent.ctx, agent.token
	actions := agentMeshActions{Mesh: orch}
	deadline := time.Now().Add(time.Hour).Unix()
	boardWait := func(key string) server.MeshWaitRequest {
		return server.MeshWaitRequest{
			Condition:    agentcomm.WaitCondition{Kind: agentcomm.WaitOnBoardKey, Key: key, Status: "ready"},
			DeadlineUnix: &deadline,
		}
	}

	live, err := actions.Wait(server.WithMeshToken(ctx, token), "req-live", "stage:impl", boardWait("nobody-publishes-this"))
	if err != nil || live.Status != string(agentcomm.WaitPending) {
		t.Fatalf("wait of the live invocation = %+v, %v; want pending", live, err)
	}

	agent.release()
	waitSideMesh(t, func() bool { _, valid := orch.ResolveMeshToken(token); return !valid })

	late, err := actions.Wait(server.WithMeshToken(ctx, token), "req-late", "stage:impl", boardWait("nobody-publishes-this-either"))
	if err == nil || !strings.Contains(err.Error(), "invocation") || !strings.Contains(err.Error(), "has ended") {
		t.Fatalf("wait of the ended invocation = %+v, %v; want rejected because its invocation has ended", late, err)
	}
	for _, w := range store.Snapshot().Assistants.Mesh.Waits {
		if w.Condition.Key == "nobody-publishes-this-either" {
			t.Fatalf("the ended invocation's wait registered: %+v", w)
		}
	}
}
