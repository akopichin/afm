package stagefiles

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validPlanMD = "## Tasks\n\n- [ ] step\n\n## Assumptions\n\n- none\n\n## Acceptance Criteria\n\n- [ ] works\n"

func writeWriteEvent(t *testing.T, sb *strings.Builder, path string) {
	t.Helper()
	fmt.Fprintf(sb, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"%s","content":"..."}}]}}`+"\n", path)
}

func TestAdoptWrittenPlan(t *testing.T) {
	dir := t.TempDir()

	planFile := filepath.Join(dir, "my-custom-plan.md")
	if err := os.WriteFile(planFile, []byte(validPlanMD), 0644); err != nil {
		t.Fatal(err)
	}
	notesFile := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(notesFile, []byte("just notes, not a plan"), 0644); err != nil {
		t.Fatal(err)
	}

	var jsonl strings.Builder
	writeWriteEvent(t, &jsonl, planFile)
	writeWriteEvent(t, &jsonl, notesFile) // записан позже плана, но невалиден — должен быть пропущен
	logFile := filepath.Join(dir, "planning.log")
	if err := os.WriteFile(filepath.Join(dir, "planning.jsonl"), []byte(jsonl.String()), 0644); err != nil {
		t.Fatal(err)
	}

	outFile := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(outFile, []byte("план сохранён в файл, всё готово"), 0644); err != nil {
		t.Fatal(err)
	}

	if !AdoptWrittenPlan(logFile, outFile, "") {
		t.Fatal("adoptWrittenPlan=false, want true")
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "## Tasks") {
		t.Errorf("outFile not replaced with plan: %q", string(data))
	}
}

// TestAdoptWrittenPlanSkipsOutFile: запись агентом самого outFile не считается
// кандидатом — его содержимое уже проверено валидацией.
func TestAdoptWrittenPlanSkipsOutFile(t *testing.T) {
	dir := t.TempDir()

	outFile := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(outFile, []byte("not a plan"), 0644); err != nil {
		t.Fatal(err)
	}

	var jsonl strings.Builder
	writeWriteEvent(t, &jsonl, outFile)
	logFile := filepath.Join(dir, "planning.log")
	if err := os.WriteFile(filepath.Join(dir, "planning.jsonl"), []byte(jsonl.String()), 0644); err != nil {
		t.Fatal(err)
	}

	if AdoptWrittenPlan(logFile, outFile, "") {
		t.Error("adoptWrittenPlan=true, want false (outFile is not a candidate)")
	}
}

// TestAdoptWrittenPlanNoLog: отсутствие stream-json лога — не ошибка, просто
// нет кандидатов.
func TestAdoptWrittenPlanNoLog(t *testing.T) {
	dir := t.TempDir()
	if AdoptWrittenPlan(filepath.Join(dir, "planning.log"), filepath.Join(dir, "plan.md"), "") {
		t.Error("adoptWrittenPlan=true, want false (no jsonl log)")
	}
}

// A relative path in the agent's Write event means a file in the agent's own directory (a worktree), not in
// afm's working directory.
func TestAdoptWrittenPlan_RelativePathIsTheAgentsFile(t *testing.T) {
	runDir := t.TempDir()
	agentDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(agentDir, "docs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "docs", "plan.md"), []byte(validPlanMD), 0644); err != nil {
		t.Fatal(err)
	}
	var jsonl strings.Builder
	writeWriteEvent(t, &jsonl, "docs/plan.md")
	logFile := filepath.Join(runDir, "planning.log")
	if err := os.WriteFile(filepath.Join(runDir, "planning.jsonl"), []byte(jsonl.String()), 0644); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(runDir, "plan.md")

	if !AdoptWrittenPlan(logFile, outFile, agentDir) {
		t.Fatal("the plan the agent wrote in its own directory must be adopted")
	}
	if got, _ := os.ReadFile(outFile); string(got) != validPlanMD {
		t.Errorf("adopted plan = %q, want the agent's file", got)
	}
}
