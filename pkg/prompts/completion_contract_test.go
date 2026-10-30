package prompts

import (
	"os"
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

func TestSummaryCorrection_NamesPathReasonAndBudget(t *testing.T) {
	path := "/abs/run/my stage/execution_summary.md"
	got := SummaryCorrection(path, "empty", 1, 2)

	for _, want := range []string{path, "empty", "1 of 2", "Continue the current task", "do not start it over"} {
		if !strings.Contains(got, want) {
			t.Errorf("correction lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "last correction") {
		t.Errorf("the first of two corrections must not say it is the last:\n%s", got)
	}
	if last := SummaryCorrection(path, "missing", 2, 2); !strings.Contains(last, "last correction") {
		t.Errorf("the final correction must warn that the stage fails next:\n%s", last)
	}
}

func TestSummaryReminder_NamesPathAndReasonAndKeepsBackgroundWork(t *testing.T) {
	path := "/abs/run/my stage/execution_summary.md"
	got := SummaryReminder(path, "missing")

	for _, want := range []string{path, "missing", "Do not finish yet", "background", "only reminder"} {
		if !strings.Contains(got, want) {
			t.Errorf("reminder lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "kill") || strings.Contains(got, "terminate") {
		t.Errorf("the reminder must not ask to stop background work:\n%s", got)
	}
}

// Контракт не обещает retry за отсутствующие секции: проверка требует лишь непустой файл.
func TestAutonomousPrompt_DoesNotPromiseSectionRetries(t *testing.T) {
	data, err := os.ReadFile("../../assets/prompts/autonomous.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "causes a retry") {
		t.Error("autonomous.md still promises a retry for missing sections")
	}
}
