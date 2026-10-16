package orchestrator

// T8.4 — аудит и миграция остальных потребителей stage-определений: verify context, end-of-run memory,
// lifecycle-хуки (stage info), CLI (см. cmd/afm's TestCli_FindsStageViaRegistry). Большая часть этого аудита
// внутри pkg/orchestrator уже закрыта миграцией T8.2 (verify.go/reflection.go читают o.graph.Stages() —
// registry_migration_test.go уже это доказывает для dependency-plan collection); тесты здесь добивают
// оставшиеся именованные пункты T8.4: verify agent context, memory end-of-run eligibility, и lifecycle
// payload's stage name resolution.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

// TestVerifyContext_UsesRegistry: the artifact context an AI-verify agent step receives (RunVerification →
// stagefiles.CollectArtifacts) must be assembled from o.graph — a dependency stage that exists ONLY in the
// registry (not in the deliberately stale opts.Stages) must still have its declared artifact show up in the
// verify prompt.
func TestVerifyContext_UsesRegistry(t *testing.T) {
	runDir := t.TempDir()
	dep := flow.Stage{ID: "dep", Artifacts: []flow.Artifact{{Name: "out", Path: "./out.txt"}}}
	main := flow.Stage{
		ID:     "s1",
		Inputs: []flow.Input{{Ref: "dep.out"}},
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}},
	}

	depDir := filepath.Join(runDir, "dep")
	if err := os.MkdirAll(depDir, 0755); err != nil {
		t.Fatal(err)
	}
	const marker = "DEP-ARTIFACT-MARKER"
	if err := os.WriteFile(filepath.Join(depDir, "out.txt"), []byte(marker), 0644); err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}

	o := &Orchestrator{
		// opts.Stages deliberately omits "dep" — if verify context collection regressed to reading it, the
		// artifact marker would never reach the prompt.
		opts:           Options{RunDir: runDir, Stages: []flow.Stage{main}, RunID: "run1"},
		ui:             bus.NewUIBus(),
		runVerifyShell: runVerifyShellCommand,
		graph:          graph.NewStore(graph.NewRegistry([]flow.Stage{dep, main})),
	}

	var gotPrompt string
	o.runVerifyAgent = func(_ context.Context, _ flow.Stage, _, prompt, _, _ string) (verify.RunOutcome, error) {
		gotPrompt = prompt
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	}

	if err := o.RunVerification(context.Background(), main, phaseImplementation); err != nil {
		t.Fatalf("RunVerification: %v", err)
	}
	if !strings.Contains(gotPrompt, marker) {
		t.Fatalf("verify agent prompt missing dependency artifact collected via registry; prompt:\n%s", gotPrompt)
	}
}

// TestMemoryEndOfRun_SeesSameDefinitions: eligibleMemoryDatasets (feeding runEndOfRunMemory, T8.4's
// "reflection and end-of-run memory" audit item) must enumerate reflect-enabled stages from o.graph — a
// write-reflect stage that exists only in the registry (opts.Stages is deliberately stale/empty) must still
// be picked up.
func TestMemoryEndOfRun_SeesSameDefinitions(t *testing.T) {
	runDir := t.TempDir()
	reflectStage := flow.Stage{ID: "reflecting", Reflect: &flow.Reflect{File: "reflecting.md", Mode: "rw"}}
	stageDir := filepath.Join(runDir, "reflecting")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "reflect_dataset.yaml"), []byte("project_level: []\nsession_level: []\n"), 0644); err != nil {
		t.Fatal(err)
	}

	o := &Orchestrator{
		opts:  Options{RunDir: runDir, Stages: nil, MemoryDir: filepath.Join(runDir, ".memory")}, // opts.Stages deliberately empty
		graph: graph.NewStore(graph.NewRegistry([]flow.Stage{reflectStage})),
	}

	stages, datasets := o.eligibleMemoryDatasets()
	if len(stages) != 1 || stages[0].ID != "reflecting" {
		t.Fatalf("eligibleMemoryDatasets stages = %+v, want [reflecting] (must read o.graph, not opts.Stages)", stages)
	}
	if len(datasets) != 1 || datasets[0].StageID != "reflecting" {
		t.Fatalf("eligibleMemoryDatasets datasets = %+v, want one dataset for reflecting", datasets)
	}
}

// TestLifecyclePayload_StageInfoFromRegistry: the lifecycle-hook payload's stage Name (BuildPayload's
// StageInfo, resolved via Orchestrator.stageName) must come from the registry — a stage known only to o.graph
// (not to the deliberately stale opts.Stages) must still report its real Name, not "" nor a graph lookup
// panic.
func TestLifecyclePayload_StageInfoFromRegistry(t *testing.T) {
	o := &Orchestrator{
		opts:  Options{Stages: nil}, // deliberately stale/empty — stageName must not depend on this
		graph: graph.NewStore(graph.NewRegistry([]flow.Stage{{ID: "build", Name: "Build the thing"}})),
	}
	if got := o.stageName("build"); got != "Build the thing" {
		t.Fatalf("stageName(build) = %q, want %q (must resolve via o.graph)", got, "Build the thing")
	}
	// A stage with no Name falls back to its id — same as before T8.2/T8.4's migration.
	oNoName := &Orchestrator{graph: graph.NewStore(graph.NewRegistry([]flow.Stage{{ID: "deploy"}}))}
	if got := oNoName.stageName("deploy"); got != "deploy" {
		t.Fatalf("stageName(deploy) = %q, want %q (id fallback)", got, "deploy")
	}
	// Unknown stage id: no panic, empty result.
	if got := oNoName.stageName("missing"); got != "" {
		t.Fatalf("stageName(missing) = %q, want \"\"", got)
	}
}
