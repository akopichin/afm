package prompts

import (
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
)

func TestCompletionContract_Implementation(t *testing.T) {
	stageDir := "/abs/run/stage1"
	got := CompletionContract(flow.PhaseImplementation, stageDir)

	donePath := stageDir + "/.done"
	if !strings.Contains(got, donePath) {
		t.Fatalf("expected contract to contain the absolute .done path %q, got:\n%s", donePath, got)
	}
	if !strings.Contains(got, "cat >") || !strings.Contains(got, "<<'EOF'") {
		t.Fatalf("expected a heredoc-style example of writing the file, got:\n%s", got)
	}
	if !strings.Contains(strings.ToLower(got), "non-empty") {
		t.Fatalf("expected an explicit non-empty requirement, got:\n%s", got)
	}
	if !strings.Contains(strings.ToLower(got), "only after the task is actually done and any checks have passed") {
		t.Fatalf("expected the explicit done+checks-passed condition, got:\n%s", got)
	}
	if !strings.Contains(got, "touch") {
		t.Fatalf("expected an explicit warning that a bare touch is insufficient, got:\n%s", got)
	}
}

func TestCompletionContract_Review(t *testing.T) {
	stageDir := "/abs/run/stage1"
	got := CompletionContract(flow.PhaseReview, stageDir)

	donePath := stageDir + "/.done"
	if !strings.Contains(got, donePath) {
		t.Fatalf("expected contract to contain the absolute .done path %q, got:\n%s", donePath, got)
	}
	if !strings.Contains(got, "cat >") || !strings.Contains(got, "<<'EOF'") {
		t.Fatalf("expected a heredoc-style example of writing the file, got:\n%s", got)
	}
	if !strings.Contains(strings.ToLower(got), "non-empty") {
		t.Fatalf("expected an explicit non-empty requirement, got:\n%s", got)
	}
	if !strings.Contains(strings.ToLower(got), "only after the task is actually done and any checks have passed") {
		t.Fatalf("expected the explicit done+checks-passed condition, got:\n%s", got)
	}
	if !strings.Contains(got, "touch") {
		t.Fatalf("expected an explicit warning that a bare touch is insufficient, got:\n%s", got)
	}
}

func TestCompletionContract_Autonomous(t *testing.T) {
	stageDir := "/abs/run/stage1"
	got := CompletionContract(flow.PhaseAutonomous, stageDir)

	summaryPath := stageDir + "/execution_summary.md"
	if !strings.Contains(got, summaryPath) {
		t.Fatalf("expected contract to contain the absolute execution_summary.md path %q, got:\n%s", summaryPath, got)
	}
	if strings.Contains(got, ".done") {
		t.Fatalf("autonomous contract must not mention .done, got:\n%s", got)
	}
}

func TestCompletionContract_PlanningIsEmpty(t *testing.T) {
	if got := CompletionContract(flow.PhasePlanning, "/abs/run/stage1"); got != "" {
		t.Fatalf("planning contract must be empty (planning has its own contract), got:\n%s", got)
	}
}

func TestCompletionContract_PathWithSpacesIsQuoted(t *testing.T) {
	stageDir := "/abs/run/my stage"
	got := CompletionContract(flow.PhaseImplementation, stageDir)

	quotedDone := `"` + stageDir + `/.done"`
	if !strings.Contains(got, quotedDone) {
		t.Fatalf("expected the example command to quote the path with a space (%q), got:\n%s", quotedDone, got)
	}
}

func TestCompletionContract_NoEnvVarIndirection(t *testing.T) {
	got := CompletionContract(flow.PhaseImplementation, "/abs/run/stage1")
	if strings.Contains(got, "$AFM_STAGE_DIR") {
		t.Fatalf("D5: contract must use a literal absolute path, not $AFM_STAGE_DIR indirection, got:\n%s", got)
	}
}
