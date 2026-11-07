package main

import (
	implexecutor "github.com/akopichin/afm-engine/testkit/executor"
	implsideagent "github.com/akopichin/afm-engine/testkit/sideagent"

	implorchestrator "github.com/akopichin/afm-engine/testkit/orchestrator"

	implstate "github.com/akopichin/afm-engine/testkit/state"

	orchestrator "github.com/akopichin/afm/contracts/control"

	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm-engine/testkit/engine"
	"github.com/akopichin/afm/contracts/agentcomm"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/docker"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/state"
)

type meshSideRunner struct {
	cfg           implexecutor.Config
	started       chan struct{}
	release       chan struct{}
	answer        string
	interruptible bool
}

func (r *meshSideRunner) RunAgent(ctx context.Context, _, _, _, _ string) error {
	if r.cfg.OnDelivered != nil {
		r.cfg.OnDelivered()
	}
	r.started <- struct{}{}
	if r.release != nil {
		select {
		case <-r.release:
		case <-r.cfg.InterruptCh:
			return implexecutor.ErrUserInterrupted
		case <-ctx.Done():
			return ctx.Err()
		}
	} else if r.interruptible {
		select {
		case <-r.cfg.InterruptCh:
			return implexecutor.ErrUserInterrupted
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if r.answer != "" {
		r.cfg.OnAction("text", r.answer)
	}
	return nil
}

func meshSideHarness(t *testing.T, runner *meshSideRunner, builders ...implsideagent.ContextBuilder) (*implorchestrator.Orchestrator, *implstate.Store, *engine.AssistantMeshBridge, *implsideagent.Manager) {
	t.Helper()
	orch, meshStore := newSideMeshOrch(t)
	sideStore, err := implsideagent.Open(t.TempDir(), "side-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sideStore.Close() })
	var builder implsideagent.ContextBuilder
	if len(builders) > 0 {
		builder = builders[0]
	}
	mgr := newStartedSideManager(implsideagent.Config{
		Store:       sideStore,
		Command:     "codex",
		Context:     builder,
		SourceReply: orch.SideAgentExplicitReply,
		OnSourceDelivered: func(id string) {
			if err := orch.MarkMessageDelivered(context.Background(), agentcomm.MessageID(id), orch.SideAgentParticipantID()); err != nil {
				t.Errorf("deliver: %v", err)
			}
		},
		NewExecutor: func(cfg implexecutor.Config) implsideagent.AgentRunner {
			return &meshSideRunner{cfg: cfg, started: runner.started, release: runner.release, answer: runner.answer, interruptible: runner.interruptible}
		},
	})
	b := engine.StartAssistantMesh(orch, mgr)
	t.Cleanup(func() {
		if runner.release != nil {
			select {
			case <-runner.release:
			default:
				close(runner.release)
			}
		}
		b.CloseInput()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if !b.Drain(ctx) {
			t.Error("bridge did not drain")
		}
		if !mgr.Drain(ctx) {
			t.Error("manager did not drain")
		}
	})
	return orch, meshStore, b, mgr
}

func newSideMeshOrch(t *testing.T) (*implorchestrator.Orchestrator, *implstate.Store) {
	t.Helper()
	return newSideMeshOrchAt(t, t.TempDir())
}

func newSideMeshOrchAt(t *testing.T, runDir string) (*implorchestrator.Orchestrator, *implstate.Store) {
	t.Helper()
	meshStore, err := implstate.Open(runDir, []string{"impl"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = meshStore.Close() })
	orch := implorchestrator.New(implorchestrator.Options{
		RunDir: runDir, RunID: "side-test", HumanControl: true, Store: meshStore, Config: config.Default(), Prompts: implorchestrator.DefaultPrompts(),
		Stages: []flow.Stage{{ID: "impl", Name: "impl", Agents: []flow.AgentType{flow.AgentAuto}}},
	})
	if _, err := orch.RegisterSideAgent(context.Background(), orchestrator.RegisterSideAgentRequest{Command: "codex", Actor: state.Actor{Kind: "system"}}); err != nil {
		t.Fatal(err)
	}
	return orch, meshStore
}

func waitSideMesh(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second) // запас на полный прогон с -race: тесты с реальным подпроцессом
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("side Mesh state did not converge")
}

func sideReplyCount(rs state.RunState, source agentcomm.MessageID) int {
	n := 0
	for _, m := range rs.Assistants.Mesh.Messages {
		if m.InReplyTo != nil && *m.InReplyTo == source && m.AutoResult {
			n++
		}
	}
	return n
}

// seedSavedSideParticipant журналирует регистрацию бокового агента так, как её записывали раньше: display_name
// — команда исполнителя.
func seedSavedSideParticipant(t *testing.T, store *implstate.Store, runID, displayName string) {
	t.Helper()
	payload, err := json.Marshal(implstate.MeshParticipantAddedPayload{
		Kind: string(agentcomm.KindSideAgent), Scope: string(agentcomm.ScopeRun), Alias: "assistant", DisplayName: displayName,
	})
	if err != nil {
		t.Fatal(err)
	}
	op := implstate.Operation{
		ID: "register-side-agent:" + runID, Kind: "mesh_register_side_agent", Actor: state.Actor{Kind: "system"},
		Effects: []implstate.Effect{{Kind: "mesh_participant_added", Target: implstate.Target{ParticipantID: "side:" + runID}, Payload: payload}},
	}
	if _, err := store.ApplyRunOperation(op, func(implstate.OperationResult) {}); err != nil {
		t.Fatal(err)
	}
}

// newStartedSideManager builds a Manager with queue execution enabled, as startup does once the dashboard is up.
func newStartedSideManager(cfg implsideagent.Config) *implsideagent.Manager {
	m := implsideagent.New(cfg)
	m.Start()
	return m
}

// checkedAtReExec emulates the libraries the host verified and passed to the container at re-exec.
func checkedAtReExec(t *testing.T, dirs ...string) {
	t.Helper()
	t.Setenv(docker.SkillLibrariesEnvVar, strings.Join(dirs, string(os.PathListSeparator)))
}

func writeCLISkill(t *testing.T, lib, name string) {
	t.Helper()
	dir := filepath.Join(lib, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: d\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlinkTo(t *testing.T, target, link string) string {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	return link
}

func mkdirAll(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// orchStages lists an orchestrator's runtime stages the way engine.Reader does.
type orchStages struct {
	orch *implorchestrator.Orchestrator
}

func (o orchStages) StageConfigs() []flow.Stage { return o.orch.Stages() }
