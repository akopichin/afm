package mcp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func askPayload(text string, options ...string) QuestionPayload {
	return QuestionPayload{Question: text, Options: options}
}

func mustAsk(t *testing.T, dir, phase string, q QuestionPayload) AskResult {
	t.Helper()
	res, err := AskQuestion(dir, phase, q)
	if err != nil {
		t.Fatalf("AskQuestion: %v", err)
	}
	return res
}

func askErrorCode(t *testing.T, err error) *AskError {
	t.Helper()
	var ae *AskError
	if !errors.As(err, &ae) {
		t.Fatalf("error %v is not *AskError", err)
	}
	return ae
}

func TestAskQuestion_AssignsSequentialIDsAndWritesRecoverableFile(t *testing.T) {
	dir := t.TempDir()
	first := mustAsk(t, dir, "planning", askPayload("Which database?", "pg", "sqlite"))
	if first.ID != "q1" || !first.Created {
		t.Fatalf("first = %+v, want new q1", first)
	}
	if err := WriteAnswerFile(dir, "planning", "q1", "pg", true); err != nil {
		t.Fatal(err)
	}
	second := mustAsk(t, dir, "planning", askPayload("Which cache?"))
	if second.ID != "q2" {
		t.Fatalf("second id = %q, want q2", second.ID)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "planning.q1.question.json"))
	if err != nil {
		t.Fatal(err)
	}
	q, fixed, _, err := RecoverQuestion(raw)
	if err != nil || !bytes.Equal(fixed, raw) {
		t.Fatalf("RecoverQuestion changed the written bytes (err=%v):\n%s\n---\n%s", err, raw, fixed)
	}
	if q.ID != "q1" || q.Question != "Which database?" || len(q.Options) != 2 {
		t.Fatalf("decoded = %+v", q)
	}
	if q.AllowCustom == nil || !*q.AllowCustom {
		t.Fatalf("allow_custom must default to true: %+v", q.AllowCustom)
	}
}

func TestAskQuestion_NumberingIgnoresOtherPhasesAndFollowsMax(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"implementation.q7.question.json", "planning.q2.answer.json", "planning.notes.question.json", "planning.notes.answer.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"id":"x","question":"old"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// planning has q2 answered (answer file only) and a non-numeric manual id: next is q3.
	if got := mustAsk(t, dir, "planning", askPayload("next?")).ID; got != "q3" {
		t.Fatalf("id = %q, want q3", got)
	}
}

func TestAskQuestion_IdenticalOpenQuestionReturnsSameID(t *testing.T) {
	dir := t.TempDir()
	first := mustAsk(t, dir, "planning", askPayload("Proceed?\n", "yes", "no"))
	again := mustAsk(t, dir, "planning", askPayload("Proceed?", "yes", "no"))
	if again.ID != first.ID || again.Created {
		t.Fatalf("again = %+v, want existing %s", again, first.ID)
	}
	entries, _ := filepath.Glob(filepath.Join(dir, "*.question.json"))
	if len(entries) != 1 {
		t.Fatalf("question files = %v, want exactly one", entries)
	}
}

func TestAskQuestion_DifferentTextWhileOpenIsPending(t *testing.T) {
	dir := t.TempDir()
	mustAsk(t, dir, "planning", askPayload("first?"))
	_, err := AskQuestion(dir, "planning", askPayload("second?"))
	ae := askErrorCode(t, err)
	if ae.Code != AskCodeQuestionPending || ae.ID != "q1" || !strings.Contains(ae.Message, "q1") {
		t.Fatalf("err = %+v", ae)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.question.json")); len(files) != 1 {
		t.Fatalf("a rejected ask must not write a file: %v", files)
	}
}

func TestAskQuestion_AnsweredLastIdenticalReturnsSameID(t *testing.T) {
	dir := t.TempDir()
	first := mustAsk(t, dir, "planning", askPayload("Proceed?"))
	if err := WriteAnswerFile(dir, "planning", first.ID, "yes", false); err != nil {
		t.Fatal(err)
	}
	// The command was cut off after the answer arrived: a retry must not start a new q2.
	again := mustAsk(t, dir, "planning", askPayload("Proceed?"))
	if again.ID != "q1" || again.Created {
		t.Fatalf("again = %+v, want existing q1", again)
	}
	// A different question after the answer moves on.
	if next := mustAsk(t, dir, "planning", askPayload("Another?")); next.ID != "q2" {
		t.Fatalf("next = %+v, want q2", next)
	}
}

func TestAskQuestion_AnsweredEarlierIdenticalIsNewQuestion(t *testing.T) {
	dir := t.TempDir()
	mustAsk(t, dir, "planning", askPayload("Same?"))
	_ = WriteAnswerFile(dir, "planning", "q1", "a", false)
	mustAsk(t, dir, "planning", askPayload("Other?"))
	_ = WriteAnswerFile(dir, "planning", "q2", "b", false)
	if got := mustAsk(t, dir, "planning", askPayload("Same?")).ID; got != "q3" {
		t.Fatalf("id = %q, want q3 (only the LAST answered question is deduplicated)", got)
	}
}

// writeQuestionFile lays out a question number AskQuestion would only reach after many turns.
func writeQuestionFile(t *testing.T, dir, phase, id, text string) {
	t.Helper()
	raw, err := encodeQuestion(id, askPayload(text))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, phase+"."+id+".question.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Questions order by number, not by file name: q10 comes after q9 and q2.
func TestAskQuestion_OrderIsNumericPastNine(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"q2", "q9", "q10"} {
		writeQuestionFile(t, dir, "planning", id, id+"?")
	}
	if err := WriteAnswerFile(dir, "planning", "q2", "a", false); err != nil {
		t.Fatal(err)
	}
	if id, ok := LastQuestionID(dir, "planning"); !ok || id != "q10" {
		t.Fatalf("LastQuestionID = %q, %v, want q10", id, ok)
	}
	_, err := AskQuestion(dir, "planning", askPayload("Something else?"))
	if ae := askErrorCode(t, err); ae.Code != AskCodeQuestionPending || ae.ID != "q9" {
		t.Fatalf("err = %+v, want question_pending naming the oldest open q9", ae)
	}

	for _, id := range []string{"q9", "q10"} {
		if err := WriteAnswerFile(dir, "planning", id, "b", false); err != nil {
			t.Fatal(err)
		}
	}
	if again := mustAsk(t, dir, "planning", askPayload("q10?")); again.ID != "q10" || again.Created {
		t.Fatalf("again = %+v, want the last answered q10", again)
	}
}

func TestAskQuestion_InvalidInputNamesField(t *testing.T) {
	no := false
	tests := []struct {
		name  string
		q     QuestionPayload
		field string
	}{
		{"blank question", askPayload("  \n"), "question"},
		{"blank option", askPayload("q?", "a", " "), "options[1]"},
		{"duplicate option", askPayload("q?", "a", "a"), "options[1]"},
		{"no way to answer", QuestionPayload{Question: "q?", AllowCustom: &no}, "allow_custom"},
		{"too large", askPayload(strings.Repeat("x", MaxQuestionBytes)), "question"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			_, err := AskQuestion(dir, "planning", tc.q)
			ae := askErrorCode(t, err)
			if ae.Code != AskCodeInvalidQuestion || !strings.Contains(ae.Message, tc.field) {
				t.Fatalf("err = %+v, want invalid_question naming %q", ae, tc.field)
			}
			if files, _ := filepath.Glob(filepath.Join(dir, "*")); len(files) != 0 {
				t.Fatalf("invalid ask left files: %v", files)
			}
		})
	}
}

func TestAskQuestion_InvalidPhase(t *testing.T) {
	_, err := AskQuestion(t.TempDir(), "../x", askPayload("q?"))
	if ae := askErrorCode(t, err); ae.Code != AskCodeInvalidQuestion || !strings.Contains(ae.Message, "phase") {
		t.Fatalf("err = %+v", ae)
	}
}

func TestAskQuestion_ConcurrentAsksNeverOverwrite(t *testing.T) {
	dir := t.TempDir()
	const n = 8
	var wg sync.WaitGroup
	ids := make(chan string, n)
	for i := range n {
		wg.Go(func() {
			// Different texts: all but one lose to the open question, but the winners must be distinct files.
			res, err := AskQuestion(dir, "planning", askPayload("question "+string(rune('a'+i))))
			if err == nil {
				ids <- res.ID
			}
		})
	}
	wg.Wait()
	close(ids)
	var got []string
	for id := range ids {
		got = append(got, id)
	}
	if len(got) < 1 {
		t.Fatal("no ask succeeded")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "planning.*.question.json"))
	if len(files) != len(got) {
		t.Fatalf("files %v vs successful asks %v: a question was overwritten or lost", files, got)
	}
	if tmp, _ := filepath.Glob(filepath.Join(dir, ".ask-*")); len(tmp) != 0 {
		t.Fatalf("temp files left: %v", tmp)
	}
}

func TestAskQuestion_NumberRaceRetriesNextID(t *testing.T) {
	dir := t.TempDir()
	// A foreign writer takes q1 between our scan and our link.
	askBeforeLink = func() {
		askBeforeLink = nil
		if err := os.WriteFile(filepath.Join(dir, "planning.q1.question.json"), []byte(`{"id":"q1","question":"other"}`), 0o600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { askBeforeLink = nil })
	res, err := AskQuestion(dir, "planning", askPayload("mine"))
	// The foreign q1 is open with another text, so the retry reports it instead of clobbering it.
	if err == nil {
		t.Fatalf("res = %+v, want question_pending for the foreign q1", res)
	}
	if ae := askErrorCode(t, err); ae.Code != AskCodeQuestionPending || ae.ID != "q1" {
		t.Fatalf("err = %+v", ae)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "planning.q1.question.json"))
	if !strings.Contains(string(raw), "other") {
		t.Fatalf("foreign question was overwritten: %s", raw)
	}
}

func TestReadAnswer_PartialInvalidOrMissingMeansNotYet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "planning.q1.answer.json")
	if _, ok := ReadAnswer(dir, "planning", "q1"); ok {
		t.Fatal("missing file reported as answered")
	}
	for _, partial := range []string{"", `{"id":"q1","ans`, `{"id":"q1"}`, `not json`} {
		if err := os.WriteFile(path, []byte(partial), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := ReadAnswer(dir, "planning", "q1"); ok {
			t.Fatalf("partial %q reported as answered", partial)
		}
	}
	if err := os.WriteFile(path, []byte(`{"id":"q1","answer":"yes","from_options":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a, ok := ReadAnswer(dir, "planning", "q1")
	if !ok || a.ID != "q1" || a.Answer != "yes" || !a.FromOptions {
		t.Fatalf("answer = %+v ok=%v", a, ok)
	}
}

func TestReadAnswer_EmptyAnswerStringIsAnswer(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAnswerFile(dir, "planning", "q1", "", false); err != nil {
		t.Fatal(err)
	}
	if a, ok := ReadAnswer(dir, "planning", "q1"); !ok || a.Answer != "" {
		t.Fatalf("answer = %+v ok=%v", a, ok)
	}
}

func TestWaitForAnswer_ReturnsAnswerThatArrivesLater(t *testing.T) {
	dir := t.TempDir()
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = WriteAnswerFile(dir, "planning", "q1", "go", false)
	}()
	a, ok, err := WaitForAnswer(context.Background(), dir, "planning", "q1", 5*time.Second, 5*time.Millisecond)
	if err != nil || !ok || a.Answer != "go" {
		t.Fatalf("a=%+v ok=%v err=%v", a, ok, err)
	}
}

func TestWaitForAnswer_TimeoutIsNotAnError(t *testing.T) {
	start := time.Now()
	_, ok, err := WaitForAnswer(context.Background(), t.TempDir(), "planning", "q1", 40*time.Millisecond, 5*time.Millisecond)
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v, want a plain timeout", ok, err)
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Fatal("returned before the timeout")
	}
}

func TestWaitForAnswer_ContextCancelStopsWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := WaitForAnswer(ctx, t.TempDir(), "planning", "q1", time.Minute, time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestLastQuestionID(t *testing.T) {
	dir := t.TempDir()
	if _, ok := LastQuestionID(dir, "planning"); ok {
		t.Fatal("empty dir has no last question")
	}
	mustAsk(t, dir, "planning", askPayload("one?"))
	_ = WriteAnswerFile(dir, "planning", "q1", "a", false)
	mustAsk(t, dir, "planning", askPayload("two?"))
	mustAsk(t, dir, "implementation", askPayload("other phase?"))
	if id, ok := LastQuestionID(dir, "planning"); !ok || id != "q2" {
		t.Fatalf("id=%q ok=%v, want q2", id, ok)
	}
}

func TestValidQuestionID(t *testing.T) {
	for id, want := range map[string]bool{"q1": true, "q12": true, "manual_1": true, "": false, "../q1": false, "a.b": false, "q 1": false} {
		if got := ValidQuestionID(id); got != want {
			t.Errorf("ValidQuestionID(%q) = %v, want %v", id, got, want)
		}
	}
}
