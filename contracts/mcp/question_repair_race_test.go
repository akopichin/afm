package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQuestionRepairSkipsSameIDRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "implementation.q1.question.json")
	if err := os.WriteFile(path, []byte(`{"id":"q1","question":"Old",options":["Old option"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	newRaw := `{"id":"q1","question":"New","options":["New option"]}`
	questions, err := scanUnansweredQuestions(dir, func() {
		if err := os.WriteFile(path, []byte(newRaw), 0600); err != nil {
			t.Fatal(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range questions {
		if q.Question != "New" || len(q.Options) != 1 || q.Options[0] != "New option" {
			t.Fatalf("returned replaced question: %+v", q)
		}
	}
	questions, err = FindUnansweredQuestions(dir)
	if err != nil || len(questions) != 1 || questions[0].Question != "New" {
		t.Fatalf("next scan lost new question: %+v, %v", questions, err)
	}
}
