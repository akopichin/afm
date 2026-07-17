package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAutonomousDialog_FindAppendRead проверяет полную mcp-цепочку для autonomous
// фазы: агент пишет autonomous_execution.q<N>.question.json → poller находит его
// (FindUnansweredQuestions), пишет в <phase>.dialog.jsonl (AppendQuestion) → UI
// читает (ReadDialog). Регрессия на случай, когда autonomous-вопрос не виден в UI
// из-за того, что dialog.jsonl не формировался.
func TestAutonomousDialog_FindAppendRead(t *testing.T) {
	dir := t.TempDir()

	// 1. Агент пишет вопрос autonomous-фазы.
	qFile := filepath.Join(dir, "autonomous_execution.q1.question.json")
	if err := os.WriteFile(qFile, []byte(`{"id":"q1","question":"what feature?","options":["a","b"],"allow_custom":true}`), 0644); err != nil {
		t.Fatal(err)
	}

	// 2. Poller: FindUnansweredQuestions находит его (фаза autonomous_execution разрешена).
	questions, err := FindUnansweredQuestions(dir)
	if err != nil {
		t.Fatalf("FindUnansweredQuestions: %v", err)
	}
	if len(questions) != 1 {
		t.Fatalf("expected 1 unanswered question, got %d: %v", len(questions), questions)
	}
	if questions[0].Phase != "autonomous_execution" {
		t.Errorf("phase: got %q, want autonomous_execution", questions[0].Phase)
	}
	if questions[0].ID != "q1" {
		t.Errorf("id: got %q, want q1", questions[0].ID)
	}

	// 3. Poller: AppendQuestion пишет в <phase>.dialog.jsonl (откуда читает UI).
	dialogPath := filepath.Join(dir, "autonomous_execution.dialog.jsonl")
	if err := AppendQuestion(dialogPath, Question{
		ID: "q1", Question: "what feature?", Options: []string{"a", "b"}, AllowCustom: true,
	}); err != nil {
		t.Fatalf("AppendQuestion: %v", err)
	}

	// 4. UI: ReadDialog возвращает запись — вопрос виден в Communication channel.
	entries, err := ReadDialog(dialogPath)
	if err != nil {
		t.Fatalf("ReadDialog: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "q1" {
		t.Errorf("expected 1 entry q1, got %v", entries)
	}
	if entries[0].Question != "what feature?" {
		t.Errorf("question text: got %q", entries[0].Question)
	}
}
