package mcp_test

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/mcp"
)

const (
	answerYes       = "yes"
	testQ1          = "q1"
	testQ2          = "q2"
	testQuestionX   = "x?"
	testQuestionDoX = "do X?"
	testQuestionY   = "y?"
	tArguments      = "arguments"
	tAskUser        = "ask_user"
	tID             = "id"
	tName           = "name"
	tQuestion       = "question"
)

func TestAppendAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "implementation.dialog.jsonl")

	if err := mcp.AppendQuestion(path, mcp.Question{
		ID: testQ1, Question: testQuestionDoX, Options: []string{answerYes, "no"}, AllowCustom: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(path, mcp.Answer{
		ID: testQ1, Answer: answerYes, FromOptions: true,
	}); err != nil {
		t.Fatal(err)
	}

	entries, err := mcp.ReadDialog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.ID != testQ1 || e.Question != testQuestionDoX || e.Answer == nil || *e.Answer != answerYes || !e.FromOptions {
		t.Errorf("entry mismatch: %+v", e)
	}
}

func TestReadOpenQuestion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "d.jsonl")
	if err := mcp.AppendQuestion(path, mcp.Question{ID: testQ1, Question: testQuestionX}); err != nil {
		t.Fatal(err)
	}

	entries, err := mcp.ReadDialog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Answer != nil {
		t.Errorf("open question should have nil Answer: %+v", entries)
	}
}

func TestFindEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "d.jsonl")
	if err := mcp.AppendQuestion(path, mcp.Question{ID: testQ1, Question: testQuestionX}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(path, mcp.Answer{ID: testQ1, Answer: answerYes}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendQuestion(path, mcp.Question{ID: testQ2, Question: testQuestionY}); err != nil {
		t.Fatal(err)
	}

	got, err := mcp.FindEntry(path, testQ1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Answer == nil || *got.Answer != answerYes {
		t.Errorf("q1 should have answer %q: %+v", answerYes, got)
	}

	got2, err := mcp.FindEntry(path, "q2")
	if err != nil {
		t.Fatal(err)
	}
	if got2 == nil || got2.Answer != nil {
		t.Errorf("q2 should be found and open: %+v", got2)
	}

	notFound, err := mcp.FindEntry(path, "q-nope")
	if err != nil {
		t.Fatal(err)
	}
	if notFound != nil {
		t.Errorf("nonexistent should return nil: %+v", notFound)
	}
}

func TestHasOpenQuestions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "d.jsonl")

	hasOpen, err := mcp.HasOpenQuestions(path)
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if hasOpen {
		t.Error("empty/missing dialog should have no open questions")
	}

	if err := mcp.AppendQuestion(path, mcp.Question{ID: testQ1, Question: testQuestionX}); err != nil {
		t.Fatal(err)
	}
	hasOpen, err = mcp.HasOpenQuestions(path)
	if err != nil {
		t.Fatal(err)
	}
	if !hasOpen {
		t.Error("unanswered question should be reported as open")
	}

	if err := mcp.AppendAnswer(path, mcp.Answer{ID: testQ1, Answer: answerYes}); err != nil {
		t.Fatal(err)
	}
	hasOpen, err = mcp.HasOpenQuestions(path)
	if err != nil {
		t.Fatal(err)
	}
	if hasOpen {
		t.Error("answered question should not be open anymore")
	}

	if err := mcp.AppendQuestion(path, mcp.Question{ID: testQ2, Question: testQuestionY}); err != nil {
		t.Fatal(err)
	}
	hasOpen, err = mcp.HasOpenQuestions(path)
	if err != nil {
		t.Fatal(err)
	}
	if !hasOpen {
		t.Error("new unanswered question after an answered one should be open")
	}
}

func TestConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "d.jsonl")

	var mu sync.Mutex
	var errs []error
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(id int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				if err := mcp.AppendQuestion(path, mcp.Question{
					ID:       fmt.Sprintf("q-%d-%d", id, j),
					Question: "?",
				}); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			}
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	if len(errs) > 0 {
		t.Fatalf("append errors: %v", errs)
	}

	entries, err := mcp.ReadDialog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 500 {
		t.Errorf("expected 500 entries, got %d (some appends were corrupted)", len(entries))
	}
}
