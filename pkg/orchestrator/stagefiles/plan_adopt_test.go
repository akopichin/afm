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

// TestAdoptWrittenPlan: агент сохранил план в файл с произвольным именем
// (описание стадии этого попросило), а в outFile попало текстовое резюме.
// adoptWrittenPlan должен найти валидный план среди записанных файлов
// и скопировать его в outFile.
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

	if !AdoptWrittenPlan(logFile, outFile) {
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

	if AdoptWrittenPlan(logFile, outFile) {
		t.Error("adoptWrittenPlan=true, want false (outFile is not a candidate)")
	}
}

// TestAdoptWrittenPlanNoLog: отсутствие stream-json лога — не ошибка, просто
// нет кандидатов.
func TestAdoptWrittenPlanNoLog(t *testing.T) {
	dir := t.TempDir()
	if AdoptWrittenPlan(filepath.Join(dir, "planning.log"), filepath.Join(dir, "plan.md")) {
		t.Error("adoptWrittenPlan=true, want false (no jsonl log)")
	}
}
