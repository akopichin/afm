package executor

import (
	"os"
	"path/filepath"
	"testing"
)

// assistantLine builds one minimal stream-json "assistant" event with a single text content block.
func assistantLine(t *testing.T, text string) string {
	t.Helper()
	return `{"type":"assistant","message":{"content":[{"type":"text","text":"` + text + `"}]}}`
}

func toolUseLine(t *testing.T, name, command string) string {
	t.Helper()
	return `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"` + name + `","input":{"command":"` + command + `"}}]}}`
}

// TestObservation_StableSourceIds verifies that the same underlying content always gets the same
// Observation.ID, including across a simulated process restart that replays an already-written jsonl log
// before continuing to append to it (dev-plan.md T5.1, §19.4).
func TestObservation_StableSourceIds(t *testing.T) {
	meta := observationMeta{runID: "run1", stageID: "impl", executionID: "impl/implementation"}

	// First "process lifetime": two lines land in the jsonl file.
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "implementation.jsonl")
	lines := []string{assistantLine(t, "hello"), toolUseLine(t, "Bash", "ls")}
	if err := os.WriteFile(jsonlPath, []byte(lines[0]+"\n"+lines[1]+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var firstRunIDs []string
	seq := 0
	for _, l := range lines {
		ev, ok := parseStreamEvent(l)
		if !ok {
			t.Fatalf("line should parse: %s", l)
		}
		for _, o := range buildObservationsFromEvent(meta, ev, &seq) {
			firstRunIDs = append(firstRunIDs, o.ID)
		}
	}
	if len(firstRunIDs) != 2 {
		t.Fatalf("expected 2 observations, got %d", len(firstRunIDs))
	}

	// "Restart": a new invocation reuses the SAME jsonl file (append-only) — the baseline seq must replay the
	// existing content so a THIRD line appended now continues numbering, not restart at 1.
	restartSeq := countExistingObservations(meta, jsonlPath)
	if restartSeq != 2 {
		t.Fatalf("restart baseline seq = %d, want 2", restartSeq)
	}
	thirdLine := assistantLine(t, "world")
	ev, ok := parseStreamEvent(thirdLine)
	if !ok {
		t.Fatal("third line should parse")
	}
	obs := buildObservationsFromEvent(meta, ev, &restartSeq)
	if len(obs) != 1 {
		t.Fatalf("expected 1 new observation, got %d", len(obs))
	}
	wantID := ComputeObservationID("run1", "impl", "impl/implementation", 3)
	if obs[0].ID != wantID {
		t.Fatalf("post-restart ID = %q, want %q", obs[0].ID, wantID)
	}

	// And re-deriving the FIRST two ids from scratch (as if nothing ever restarted) must match exactly what the
	// "first process lifetime" computed above — the id is a pure function of content, not of wall-clock time or
	// any random value.
	reSeq := 0
	var reIDs []string
	for _, l := range lines {
		ev, _ := parseStreamEvent(l)
		for _, o := range buildObservationsFromEvent(meta, ev, &reSeq) {
			reIDs = append(reIDs, o.ID)
		}
	}
	for i := range firstRunIDs {
		if firstRunIDs[i] != reIDs[i] {
			t.Fatalf("observation %d: id not stable: %q != %q", i, firstRunIDs[i], reIDs[i])
		}
	}
}

// TestObservation_IncompleteBlockMarked verifies that a torn/unparsable trailing line (the shape lineReader
// delivers when a subprocess dies mid-write, see linereader.go) is reported as an Observation with
// Complete==false, and that ObservationToAction — the checkpoint-eligibility boundary other code uses —
// refuses to treat it as a displayable/checkpoint-eligible action.
func TestObservation_IncompleteBlockMarked(t *testing.T) {
	meta := observationMeta{runID: "run1", stageID: "s1", executionID: "s1/implementation"}
	seq := 0
	torn := `{"type":"assistant","message":{"content":[{"type":"text","text":"unterminated`
	o, ok := incompleteObservation(meta, torn, &seq)
	if !ok {
		t.Fatal("expected torn line to be reported as incomplete")
	}
	if o.Complete {
		t.Fatal("torn observation must have Complete==false")
	}
	if o.Seq != 1 {
		t.Fatalf("seq = %d, want 1", o.Seq)
	}
	if _, _, ok := ObservationToAction(o, 0); ok {
		t.Fatal("ObservationToAction must reject an incomplete observation")
	}

	// A VALID JSON line that just isn't an assistant message (e.g. a "result" event) must NOT be reported as
	// incomplete at all — it carries no main-agent content, complete or otherwise.
	seq2 := 0
	validNonAssistant := `{"type":"result","subtype":"success"}`
	if _, ok := incompleteObservation(meta, validNonAssistant, &seq2); ok {
		t.Fatal("a valid non-assistant JSON line must not be reported as an incomplete observation")
	}
	if seq2 != 0 {
		t.Fatalf("seq must stay 0 for a valid non-assistant line, got %d", seq2)
	}
}

// TestObservation_FullContentNeverTruncated verifies buildObservationsFromEvent always carries full content,
// even for a long Bash command — only ObservationToAction (the display adapter) ever truncates.
func TestObservation_FullContentNeverTruncated(t *testing.T) {
	meta := observationMeta{runID: "r", stageID: "s", executionID: "s/implementation"}
	long := ""
	for i := 0; i < 500; i++ {
		long += "x"
	}
	ev, ok := parseStreamEvent(toolUseLine(t, "Bash", long))
	if !ok {
		t.Fatal("line should parse")
	}
	seq := 0
	obs := buildObservationsFromEvent(meta, ev, &seq)
	if len(obs) != 1 || len(obs[0].Content) != len(long) {
		t.Fatalf("observation content truncated: got len %d, want %d", len(obs[0].Content), len(long))
	}
	tool, detail, ok := ObservationToAction(obs[0], 50)
	if !ok || tool != "Bash" {
		t.Fatalf("ObservationToAction(bash) = %q,%q,%v", tool, detail, ok)
	}
	if len(detail) >= len(long) {
		t.Fatalf("ObservationToAction should truncate Bash detail at limit=50, got len %d", len(detail))
	}
}

// TestObservation_WriteToolNeverTruncatedByAdapter mirrors contentToAction's own carve-out: Write/Edit/Read/
// Glob/Grep detail (a file path) is never truncated, even with a small limit.
func TestObservation_WriteToolNeverTruncatedByAdapter(t *testing.T) {
	meta := observationMeta{runID: "r", stageID: "s", executionID: "s/implementation"}
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/a/very/long/path/that/would/otherwise/be/truncated/by/a/tiny/limit.go"}}]}}`
	ev, ok := parseStreamEvent(line)
	if !ok {
		t.Fatal("line should parse")
	}
	seq := 0
	obs := buildObservationsFromEvent(meta, ev, &seq)
	if len(obs) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(obs))
	}
	_, detail, ok := ObservationToAction(obs[0], 5)
	if !ok || detail != obs[0].Content {
		t.Fatalf("Write detail truncated: got %q, want full %q", detail, obs[0].Content)
	}
}
