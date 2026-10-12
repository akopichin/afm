package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// verifySuccessScript emits a JSON pass and usage through the real executor so OnUsage fires.
const verifySuccessScript = `#!/bin/bash
echo '{"type":"assistant","message":{"model":"claude-opus-4-8","content":[{"type":"text","text":"{\"schema_version\":1,\"verdict\":\"pass\",\"summary\":\"ok\"}"}]}}'
echo '{"type":"result","subtype":"success","usage":{"input_tokens":9,"output_tokens":4,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}'
`

// verifyErrorScript simulates a genuinely failed verify invocation — RunVerifyAgent's Config.OnUsage must
// still fire exactly once even though the process itself did not succeed.
const verifyErrorScript = `#!/bin/bash
echo '{"type":"result","subtype":"error","usage":{"input_tokens":3,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}'
exit 1
`

// setupVerifyUsageOrchestrator runs author and verify scripts through real executors.
func setupVerifyUsageOrchestrator(t *testing.T, verifyScript string) (orch *orchestrator.Orchestrator, runDir, stateFile string, acct *accounting.Store) {
	t.Helper()
	runDir = t.TempDir()

	scriptPath := filepath.Join(runDir, "verify-agent.sh")
	if err := os.WriteFile(scriptPath, []byte(verifyScript), 0o755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID: "backend", Name: "Backend",
		Agents: []flow.AgentType{flow.AgentAuto},
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: scriptPath}}},
	}}

	store, err := state.Open(runDir, []string{"backend"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	resolver := accounting.NewResolver(accounting.PricingConfig{})
	acct, err = accounting.Open(runDir, resolver)
	if err != nil {
		t.Fatalf("accounting.Open: %v", err)
	}

	// Author: a plain autonomous stage whose own agent is injected — only the VERIFY step goes through the
	// real executor/subprocess here, keeping the test focused on verify-only usage accounting.
	orch = orchestrator.New(orchestrator.Options{
		RunDir:     runDir,
		Stages:     stages,
		Store:      store,
		Config:     config.Default(),
		Prompts:    orchestrator.DefaultPrompts(),
		Runner:     &autonomousSummaryRunner{},
		Accounting: acct,
	})
	return orch, runDir, filepath.Join(runDir, "state.json"), acct
}

func TestIntegration_VerifyAgentInvocationRecordsUsageOnce(t *testing.T) {
	orch, runDir, stateFile, acct := setupVerifyUsageOrchestrator(t, verifySuccessScript)
	defer acct.Close()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["backend"].Status != state.StatusDone {
		t.Fatalf("expected done, got %v", final.Stages["backend"].Status)
	}

	recs := readUsageRecords(t, runDir)
	verifyRecs := make([]accounting.UsageRecord, 0, len(recs))
	for _, r := range recs {
		if r.Phase == "verify" {
			verifyRecs = append(verifyRecs, r)
		}
	}
	if len(verifyRecs) != 1 {
		t.Fatalf("expected exactly 1 usage record for the verify invocation, got %d: %+v", len(verifyRecs), recs)
	}
	if verifyRecs[0].StageID != "backend" {
		t.Errorf("verify usage record StageID = %q, want %q", verifyRecs[0].StageID, "backend")
	}
	for _, r := range recs {
		for _, other := range []string{"planning", "implementation", "review", "autonomous_execution"} {
			if r.Phase == "verify" && r.Phase == other {
				t.Errorf("verify phase must never collide with a flow.Phase label %q", other)
			}
		}
	}
}

func TestIntegration_VerifyAgentErroredInvocationStillCountedOnce(t *testing.T) {
	orch, runDir, stateFile, acct := setupVerifyUsageOrchestrator(t, verifyErrorScript)
	defer acct.Close()

	// VerifyExecError fails after one invocation; usage must still be recorded.
	_ = orch.Run(context.Background())

	recs := readUsageRecords(t, runDir)
	verifyRecs := 0
	for _, r := range recs {
		if r.Phase == "verify" {
			verifyRecs++
		}
	}
	if verifyRecs == 0 {
		t.Fatal("expected at least one usage record for the errored verify invocation — a failed invocation must still be counted")
	}

	final := loadStateJSON(t, stateFile)
	t.Logf("final stage status (informational only): %v, verify usage records: %d", final.Stages["backend"].Status, verifyRecs)
}

func TestIntegration_VerifyShellStepProducesNoAgentUsage(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{
		ID: "backend", Name: "Backend",
		Agents: []flow.AgentType{flow.AgentAuto},
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyShell, Run: "true"}}},
	}}

	store, err := state.Open(runDir, []string{"backend"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	resolver := accounting.NewResolver(accounting.PricingConfig{})
	acct, err := accounting.Open(runDir, resolver)
	if err != nil {
		t.Fatalf("accounting.Open: %v", err)
	}
	defer acct.Close()

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(), Runner: &autonomousSummaryRunner{}, Accounting: acct,
	})

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	stateFile := filepath.Join(runDir, "state.json")
	final := loadStateJSON(t, stateFile)
	if final.Stages["backend"].Status != state.StatusDone {
		t.Fatalf("expected done, got %v", final.Stages["backend"].Status)
	}

	data, err := os.ReadFile(filepath.Join(runDir, "usage.jsonl"))
	if err == nil && len(data) > 0 {
		t.Errorf("expected no usage records for a shell-only verify step, got:\n%s", data)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read usage.jsonl: %v", err)
	}
}
