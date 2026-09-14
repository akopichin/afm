package orchestrator_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// usageAgentScript is a real (bash) agent used via Config.Client.Command —
// unlike the mockRunner/scriptedRunner helpers elsewhere in this package
// (which inject an executor.Runner directly and therefore bypass
// executor.Config entirely: runnerFor short-circuits an injected Runner
// straight to o.runner for a stage with no Command), this script goes
// through a REAL *executor.Executor built by runnerFor per phase, which is
// the only thing that ever calls Config.OnUsage. Phase is distinguished by
// which artifacts already exist in $AFM_STAGE_DIR: plan.md means planning
// already ran (so this call is implementation); a first-attempt marker
// distinguishes an incomplete first implementation attempt (no .done — the
// stage retries, mirrors TestIntegration_IncompleteRetry) from the
// successful second attempt.
const usageAgentScript = `
if [ -f "$AFM_STAGE_DIR/plan.md" ]; then
  if [ ! -f "$AFM_STAGE_DIR/.impl_attempt" ]; then
    touch "$AFM_STAGE_DIR/.impl_attempt"
    echo '{"type":"assistant","message":{"model":"claude-opus-4-8","content":[{"type":"text","text":"working on it"}]}}'
    echo '{"type":"result","subtype":"success","usage":{"input_tokens":11,"output_tokens":7,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}'
    exit 0
  fi
  echo '{"type":"assistant","message":{"model":"claude-opus-4-8","content":[{"type":"text","text":"implementation done"}]}}'
  echo '{"type":"result","subtype":"success","usage":{"input_tokens":22,"output_tokens":13,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}'
  echo done > "$AFM_STAGE_DIR/.done"
  exit 0
fi
echo '{"type":"assistant","message":{"model":"claude-opus-4-8","content":[{"type":"text","text":"## Tasks\n\n- [ ] Step 1: implement feature\n\n## Assumptions\n\n- none\n\n## Acceptance Criteria\n\n- [ ] feature works\n"}]}}'
echo '{"type":"result","subtype":"success","usage":{"input_tokens":33,"output_tokens":19,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}'
`

// usageStages returns a single non-interactive, auto-approved stage wired to
// run usageAgentScript for both its planning and implementation phases.
func usageStages() []flow.Stage {
	return []flow.Stage{
		{
			ID:          "backend",
			Name:        "Backend",
			Description: "usage accounting smoke test",
			Agents:      []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
			AutoApprove: true,
		},
	}
}

// setupUsageOrchestrator opens both the FSM store and an accounting.Store
// against the SAME fresh runDir, then builds a real (non-injected-Runner)
// orchestrator backed by usageAgentScript via Config.Client.Command. Returns
// the orchestrator, runDir and the accounting store so callers can close it
// and inspect Unavailable()/Snapshot() after Run.
func setupUsageOrchestrator(t *testing.T) (orch *orchestrator.Orchestrator, runDir, stateFile string, acct *accounting.Store) {
	t.Helper()
	stages := usageStages()
	runDir = t.TempDir()
	stageIDs := []string{"backend"}

	store, err := state.Open(runDir, stageIDs)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	resolver := accounting.NewResolver(accounting.PricingConfig{})
	acct, err = accounting.Open(runDir, resolver)
	if err != nil {
		t.Fatalf("accounting.Open: %v", err)
	}

	cfg := config.Default()
	cfg.Client.Command = "bash"
	cfg.Client.ExtraArgs = []string{"-c", usageAgentScript}

	orch = orchestrator.New(orchestrator.Options{
		RunDir:     runDir,
		Stages:     stages,
		Store:      store,
		Config:     cfg,
		Prompts:    orchestrator.DefaultPrompts(),
		Accounting: acct,
	})
	return orch, runDir, filepath.Join(runDir, "state.json"), acct
}

// readUsageRecords parses <runDir>/usage.jsonl directly (the durable ledger
// Store.Append writes to) — the black-box equivalent of loadStateJSON for
// accounting, since accounting.Ledger's own record accessor is unexported.
func readUsageRecords(t *testing.T, runDir string) []accounting.UsageRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "usage.jsonl"))
	if err != nil {
		t.Fatalf("read usage.jsonl: %v", err)
	}
	var recs []accounting.UsageRecord
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec accounting.UsageRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("unmarshal usage record %q: %v", line, err)
		}
		recs = append(recs, rec)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan usage.jsonl: %v", err)
	}
	return recs
}

// countPhase returns how many records match phase.
func countPhase(recs []accounting.UsageRecord, phase string) int {
	n := 0
	for _, r := range recs {
		if r.Phase == phase {
			n++
		}
	}
	return n
}

// TestIntegration_UsageRecordedAcrossPlanningImplementationRetry drives one
// stage through planning -> (auto-approve) -> implementation, where the
// first implementation attempt is incomplete (no .done) and triggers a
// retry (mirrors TestIntegration_IncompleteRetry) before the second attempt
// succeeds. It asserts the accounting store received exactly the three
// usage lines this produces, each attributed to the right stage/phase, and
// that the run-level summary sums them.
func TestIntegration_UsageRecordedAcrossPlanningImplementationRetry(t *testing.T) {
	orch, runDir, stateFile, acct := setupUsageOrchestrator(t)
	defer acct.Close()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["backend"].Status != state.StatusDone {
		t.Fatalf("expected done, got %v", final.Stages["backend"].Status)
	}

	recs := readUsageRecords(t, runDir)
	if len(recs) != 3 {
		t.Fatalf("expected 3 usage records (1 planning + 2 implementation attempts), got %d: %+v", len(recs), recs)
	}

	if n := countPhase(recs, "planning"); n != 1 {
		t.Errorf("expected 1 planning usage record, got %d", n)
	}
	if n := countPhase(recs, "implementation"); n != 2 {
		t.Errorf("expected 2 implementation usage records (incomplete attempt + retry), got %d", n)
	}
	for _, rec := range recs {
		if rec.StageID != "backend" {
			t.Errorf("record phase=%s: expected stage_id=backend, got %q", rec.Phase, rec.StageID)
		}
		if rec.Scope != "" {
			t.Errorf("record phase=%s: expected empty scope (stage-attributed), got %q", rec.Phase, rec.Scope)
		}
		if !rec.Metered {
			t.Errorf("record phase=%s: expected Metered=true (usageAgentScript always emits a terminal result), got false (reason=%s)", rec.Phase, rec.Reason)
		}
	}

	// Run-level summary sums every record's tokens (11+7 + 22+13 + 33+19).
	summary := acct.Snapshot().RunSummary()
	if summary.Metered != 3 {
		t.Errorf("RunSummary.Metered = %d, want 3", summary.Metered)
	}
	wantTotal := uint64((11 + 7) + (22 + 13) + (33 + 19))
	if got := summary.Tokens.Total(); got != wantTotal {
		t.Errorf("RunSummary.Tokens.Total() = %d, want %d", got, wantTotal)
	}

	// Per-stage summary must attribute the same three records to "backend".
	byStage := acct.Snapshot().SummaryByStage()
	if s, ok := byStage["backend"]; !ok || s.Metered != 3 {
		t.Errorf("SummaryByStage()[backend].Metered = %+v, want 3 records present", s)
	}
}

// TestIntegration_UsageStorageFailureDoesNotAffectRun proves that a broken
// accounting store never affects the run itself: pre-closing the store's
// underlying file (the black-box way to force every subsequent Append to
// fail, since Store's internals are unexported) simulates a storage-fatal
// condition on the accounting side only. The stage must still reach `done`
// exactly as if accounting had never been wired in, and the store must
// report Unavailable() — recordUsage logs and swallows the error, never
// calls setFatal, never touches the FSM.
func TestIntegration_UsageStorageFailureDoesNotAffectRun(t *testing.T) {
	orch, _, stateFile, acct := setupUsageOrchestrator(t)

	// Force every subsequent Append to fail: Append's first Write goes to an
	// already-closed *os.File, which errors and permanently marks the store
	// unavailable (see Store.Append/markUnavailable) — all through the public
	// API, no accounting-internal access needed.
	if err := acct.Close(); err != nil {
		t.Fatalf("acct.Close: %v", err)
	}

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v (a storage-only failure must never fail the run)", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["backend"].Status != state.StatusDone {
		t.Fatalf("expected done despite accounting failure, got %v", final.Stages["backend"].Status)
	}

	if !acct.Unavailable() {
		t.Error("expected accounting store to report Unavailable() after a write failure")
	}
}
