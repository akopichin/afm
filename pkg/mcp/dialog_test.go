package mcp_test

import (
	"encoding/json"
	"fmt"
	"os"
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

func TestFindUnansweredQuestions(t *testing.T) {
	dir := t.TempDir()

	// Empty directory → empty result.
	got, err := mcp.FindUnansweredQuestions(dir)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty dir: want [], got %v, err %v", got, err)
	}

	// Single unanswered question.
	q1 := filepath.Join(dir, "planning.q1.question.json")
	if err := os.WriteFile(q1, []byte(`{"id":"q1","question":"proceed?","options":["yes","no"],"allow_custom":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = mcp.FindUnansweredQuestions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 unanswered, got %d", len(got))
	}
	if got[0].Phase != "planning" || got[0].ID != "q1" || got[0].Question != "proceed?" {
		t.Fatalf("unexpected entry: %+v", got[0])
	}
	if !got[0].AllowCustom || len(got[0].Options) != 2 {
		t.Fatalf("allow_custom or options mismatch: %+v", got[0])
	}

	// Second question from a different phase.
	q2 := filepath.Join(dir, "implementation.q1.question.json")
	if err := os.WriteFile(q2, []byte(`{"id":"q1","question":"how?"}`), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = mcp.FindUnansweredQuestions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 unanswered, got %d", len(got))
	}

	// Answer planning.q1 → should disappear from results.
	a1 := filepath.Join(dir, "planning.q1.answer.json")
	if err := os.WriteFile(a1, []byte(`{"id":"q1","answer":"yes"}`), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = mcp.FindUnansweredQuestions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Phase != "implementation" {
		t.Fatalf("want 1 unanswered (implementation), got %v", got)
	}

	// Malformed, unrepairable JSON → surfaced as a fallback stub, not
	// dropped silently (a dropped question hangs the stage forever with no
	// diagnostic — see TestFindUnansweredQuestions_UnrepairableJSON_FallbackStub).
	bad := filepath.Join(dir, "review.q1.question.json")
	if err := os.WriteFile(bad, []byte(`not json`), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = mcp.FindUnansweredQuestions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("malformed JSON must surface as a fallback stub; want 2, got %d: %+v", len(got), got)
	}

	// allow_custom defaults to true when omitted.
	if !got[0].AllowCustom {
		t.Error("allow_custom should default to true when not present in JSON")
	}
}

// TestFindUnansweredQuestions_RepairsUnescapedQuote locks in the repair for
// a common agent authoring mistake: a literal, unescaped '"' inside a JSON
// string value (agent hand-writes the question file and quotes a word
// instead of escaping it). Before this fix, such a file failed
// json.Unmarshal and was skipped silently — the question never surfaced to
// the poller or the UI, and the stage hung forever with no diagnostic.
func TestFindUnansweredQuestions_RepairsUnescapedQuote(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "planning.q1.question.json")
	broken := `{"id":"q1","question":"нужно решить, должно ли "скрытие" сохраняться","options":["да","нет"],"allow_custom":true}`
	if err := os.WriteFile(path, []byte(broken), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := mcp.FindUnansweredQuestions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 repaired question, got %d: %v", len(got), got)
	}
	want := `нужно решить, должно ли "скрытие" сохраняться`
	if got[0].Question != want {
		t.Fatalf("question text mismatch:\n got:  %q\n want: %q", got[0].Question, want)
	}
	if got[0].ID != "q1" || len(got[0].Options) != 2 {
		t.Fatalf("unexpected entry: %+v", got[0])
	}

	// The fix is persisted back to disk — a second read parses cleanly
	// without needing the repair path again.
	fixed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(fixed, &probe); err != nil {
		t.Fatalf("repaired file on disk is still invalid JSON: %v", err)
	}
	if probe.Question != want {
		t.Fatalf("persisted question text mismatch: %q", probe.Question)
	}
}

// TestFindUnansweredQuestions_MissingKeyQuote_Repaired locks in the repair
// for the actual incident that motivated this fix: the agent dropped the
// opening quote before the "options" key
// (`...,options":[...]` instead of `...,"options":[...]`). This is a
// different failure shape from the unescaped-quote case above — it must be
// repaired without corrupting the surrounding Cyrillic text.
func TestFindUnansweredQuestions_MissingKeyQuote_Repaired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "planning.q10.question.json")
	broken := `{"id":"q10","question":"...последний cell Phase 7.",options":["Утверждаю","Правки"]}`
	if err := os.WriteFile(path, []byte(broken), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := mcp.FindUnansweredQuestions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 repaired question, got %d: %+v", len(got), got)
	}
	if got[0].ID != "q10" || got[0].Question != "...последний cell Phase 7." {
		t.Fatalf("unexpected entry: %+v", got[0])
	}
	if want := []string{"Утверждаю", "Правки"}; len(got[0].Options) != 2 || got[0].Options[0] != want[0] || got[0].Options[1] != want[1] {
		t.Fatalf("options mismatch: got %v, want %v", got[0].Options, want)
	}

	// The fix is persisted back to disk.
	fixed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Options []string `json:"options"`
	}
	if err := json.Unmarshal(fixed, &probe); err != nil {
		t.Fatalf("repaired file on disk is still invalid JSON: %v", err)
	}
	if len(probe.Options) != 2 {
		t.Fatalf("persisted options mismatch: %v", probe.Options)
	}
}

// TestFindUnansweredQuestions_UnrepairableJSON_FallbackStub locks in the
// last-resort fallback: a question file so broken that even jsonrepair
// cannot recover it must still surface to the poller as a stub question
// instead of vanishing. Before this fix, an unrepairable file was silently
// dropped (continue) and the stage hung in "running" forever with no trace
// in the UI or logs.
func TestFindUnansweredQuestions_UnrepairableJSON_FallbackStub(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "planning.q5.question.json")
	if err := os.WriteFile(path, []byte(`not json at all {{{`), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := mcp.FindUnansweredQuestions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 fallback stub, got %d: %+v", len(got), got)
	}
	if got[0].ID != "q5" {
		t.Fatalf("fallback stub must keep the id from the filename, got %q", got[0].ID)
	}
	if got[0].Question == "" {
		t.Fatal("fallback stub must carry a non-empty explanatory question")
	}
	if len(got[0].Options) != 2 {
		t.Fatalf("fallback stub must offer Continue/Cancel options, got %v", got[0].Options)
	}
	if !got[0].AllowCustom {
		t.Fatal("fallback stub must allow a custom answer")
	}
}

// TestFindUnansweredQuestions_UnknownPhaseSkipped locks in the phase
// whitelist: only planning/implementation/review question files are
// recognized, any other phase prefix is silently skipped.
func TestFindUnansweredQuestions_UnknownPhaseSkipped(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "bogus.q1.question.json"), []byte(`{"id":"q1","question":"proceed?"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "planning.q1.question.json"), []byte(`{"id":"q1","question":"proceed?"}`), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := mcp.FindUnansweredQuestions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Phase != "planning" {
		t.Fatalf("unknown phase must be skipped; want 1 (planning), got %+v", got)
	}
}

// TestReadDialog_AnswerBeforeQuestion exercises the merge branch where the
// answer line is appended before the question line (the question poller and
// the HTTP answer handler write to dialog.jsonl from separate goroutines).
// ReadDialog must fold them into a single entry carrying both sets of fields.
func TestReadDialog_AnswerBeforeQuestion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "d.jsonl")

	if err := mcp.AppendAnswer(path, mcp.Answer{ID: testQ1, Answer: answerYes, FromOptions: true}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendQuestion(path, mcp.Question{
		ID: testQ1, Question: testQuestionDoX, Options: []string{answerYes, "no"}, AllowCustom: true,
	}); err != nil {
		t.Fatal(err)
	}

	entries, err := mcp.ReadDialog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 merged entry, got %d", len(entries))
	}
	e := entries[0]
	if e.ID != testQ1 || e.Question != testQuestionDoX || len(e.Options) != 2 || !e.AllowCustom {
		t.Errorf("question fields not merged onto answer-first entry: %+v", e)
	}
	if e.Answer == nil || *e.Answer != answerYes || !e.FromOptions {
		t.Errorf("answer fields lost during merge: %+v", e)
	}
}

// TestFindUnansweredQuestions_SamePhaseAndFallback covers cases the basic
// test does not: multiple unanswered questions within a single phase, an
// explicit allow_custom:false, and an empty JSON "id" that falls back to the
// id parsed from the filename.
func TestFindUnansweredQuestions_SamePhaseAndFallback(t *testing.T) {
	// Two unanswered questions in the SAME phase are both returned.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "planning.q1.question.json"),
		[]byte(`{"id":"q1","question":"first?"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "planning.q2.question.json"),
		[]byte(`{"id":"q2","question":"second?"}`), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := mcp.FindUnansweredQuestions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 unanswered in same phase, got %d: %+v", len(got), got)
	}

	// allow_custom:false is honored (not forced to the default true).
	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir2, "planning.q1.question.json"),
		[]byte(`{"id":"q1","question":"which?","allow_custom":false}`), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = mcp.FindUnansweredQuestions(dir2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].AllowCustom {
		t.Fatalf("allow_custom=false not honored: %+v", got)
	}

	// Empty JSON "id" → falls back to the id parsed from the filename.
	dir3 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir3, "planning.qX.question.json"),
		[]byte(`{"id":"","question":"no id in json?"}`), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = mcp.FindUnansweredQuestions(dir3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "qX" {
		t.Fatalf("empty JSON id should fall back to filename id, got: %+v", got)
	}
}
