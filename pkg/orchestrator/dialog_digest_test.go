package orchestrator

import (
	"fmt"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/mcp"
)

func TestDialogDigest_NoFile(t *testing.T) {
	stageDir := t.TempDir()
	if got := dialogDigestBlock(stageDir, "implementation"); got != "" {
		t.Fatalf("expected empty digest for missing dialog file, got %q", got)
	}
}

func TestDialogDigest_SkipsUnanswered(t *testing.T) {
	stageDir := t.TempDir()
	path := mcp.DialogPath(stageDir, "implementation")
	if err := mcp.AppendQuestion(path, mcp.Question{ID: "q1", Question: "Which approach?"}); err != nil {
		t.Fatal(err)
	}
	if got := dialogDigestBlock(stageDir, "implementation"); got != "" {
		t.Fatalf("expected empty digest for unanswered question, got %q", got)
	}
}

func TestDialogDigest_RendersPairsInOrder(t *testing.T) {
	stageDir := t.TempDir()
	path := mcp.DialogPath(stageDir, "implementation")
	q1, a1 := "Should we use REST or gRPC?", "Use REST for now."
	q2, a2 := "Should we add caching?", "Yes, add a Redis cache."
	if err := mcp.AppendQuestion(path, mcp.Question{ID: "q1", Question: q1}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(path, mcp.Answer{ID: "q1", Answer: a1}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendQuestion(path, mcp.Question{ID: "q2", Question: q2}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(path, mcp.Answer{ID: "q2", Answer: a2}); err != nil {
		t.Fatal(err)
	}

	got := dialogDigestBlock(stageDir, "implementation")
	if !strings.Contains(got, dialogDigestHeading) {
		t.Fatalf("expected heading in digest, got %q", got)
	}
	for _, want := range []string{q1, a1, q2, a2} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected digest to contain %q, got %q", want, got)
		}
	}
	if strings.Index(got, q1) >= strings.Index(got, q2) {
		t.Fatalf("expected chronological order (q1 before q2), got %q", got)
	}
}

func TestDialogDigest_MarksAutoAnswered(t *testing.T) {
	stageDir := t.TempDir()
	path := mcp.DialogPath(stageDir, "implementation")
	if err := mcp.AppendQuestion(path, mcp.Question{ID: "q1", Question: "Auto question?"}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(path, mcp.Answer{ID: "q1", Answer: "auto answer", AutoAnswered: true}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendQuestion(path, mcp.Question{ID: "q2", Question: "Manual question?"}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(path, mcp.Answer{ID: "q2", Answer: "manual answer", AutoAnswered: false}); err != nil {
		t.Fatal(err)
	}

	got := dialogDigestBlock(stageDir, "implementation")
	if !strings.Contains(got, "(auto-answered)") {
		t.Fatalf("expected auto-answered marker in digest, got %q", got)
	}

	// The manual pair's own line must not carry the marker.
	manualIdx := strings.Index(got, "manual answer")
	if manualIdx < 0 {
		t.Fatalf("expected manual answer in digest, got %q", got)
	}
	manualLineEnd := strings.IndexByte(got[manualIdx:], '\n')
	var manualLine string
	if manualLineEnd < 0 {
		manualLine = got[manualIdx:]
	} else {
		manualLine = got[manualIdx : manualIdx+manualLineEnd]
	}
	if strings.Contains(manualLine, "(auto-answered)") {
		t.Fatalf("manual answer line should not carry auto-answered marker, got %q", manualLine)
	}
}

func TestDialogDigest_RendersOptions(t *testing.T) {
	stageDir := t.TempDir()
	path := mcp.DialogPath(stageDir, "implementation")
	if err := mcp.AppendQuestion(path, mcp.Question{ID: "q1", Question: "Pick one", Options: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(path, mcp.Answer{ID: "q1", Answer: "a", FromOptions: true}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendQuestion(path, mcp.Question{ID: "q2", Question: "Open question, no options"}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(path, mcp.Answer{ID: "q2", Answer: "free-form answer"}); err != nil {
		t.Fatal(err)
	}

	got := dialogDigestBlock(stageDir, "implementation")
	if !strings.Contains(got, "(options: a | b)") {
		t.Fatalf("expected options line in digest, got %q", got)
	}

	// The no-options pair must not render an (empty) options line of its own.
	q2Idx := strings.Index(got, "Open question, no options")
	if q2Idx < 0 {
		t.Fatalf("expected q2 in digest, got %q", got)
	}
	rest := got[q2Idx:]
	nextOptionsIdx := strings.Index(rest, "(options:")
	nextAnswerIdx := strings.Index(rest, "A: free-form answer")
	if nextOptionsIdx >= 0 && nextOptionsIdx < nextAnswerIdx {
		t.Fatalf("q2 (no options) should not have its own options line, got %q", rest)
	}
}

func TestDialogDigest_DropsOldestOverBudget(t *testing.T) {
	stageDir := t.TempDir()
	path := mcp.DialogPath(stageDir, "implementation")

	filler := strings.Repeat("x", 900)
	const n = 50
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("q%d", i)
		q := fmt.Sprintf("question-%03d %s", i, filler)
		a := fmt.Sprintf("answer-%03d", i)
		if err := mcp.AppendQuestion(path, mcp.Question{ID: id, Question: q}); err != nil {
			t.Fatal(err)
		}
		if err := mcp.AppendAnswer(path, mcp.Answer{ID: id, Answer: a}); err != nil {
			t.Fatal(err)
		}
	}

	got := dialogDigestBlock(stageDir, "implementation")
	overhead := len(dialogDigestHeading) + len("\n\n\n\n") + len(dialogDigestTruncationMarker) + 256
	if len(got) > dialogDigestBudgetBytes+overhead {
		t.Fatalf("digest exceeds budget: got %d bytes (budget %d, overhead allowance %d)", len(got), dialogDigestBudgetBytes, overhead)
	}
	newest := fmt.Sprintf("question-%03d", n-1)
	oldest := fmt.Sprintf("question-%03d", 0)
	if !strings.Contains(got, newest) {
		t.Fatalf("expected newest pair %q to be present in digest", newest)
	}
	if strings.Contains(got, oldest) {
		t.Fatalf("expected oldest pair %q to be dropped from digest", oldest)
	}
}

func TestDialogDigest_TruncatesSingleOversizedPair(t *testing.T) {
	stageDir := t.TempDir()
	path := mcp.DialogPath(stageDir, "implementation")

	bigQuestion := "big question: " + strings.Repeat("y", 50*1024)
	if err := mcp.AppendQuestion(path, mcp.Question{ID: "q1", Question: bigQuestion}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(path, mcp.Answer{ID: "q1", Answer: "ok"}); err != nil {
		t.Fatal(err)
	}

	got := dialogDigestBlock(stageDir, "implementation")
	if got == "" {
		t.Fatal("expected non-empty digest for oversized single pair")
	}
	overhead := len(dialogDigestHeading) + len("\n\n\n\n")
	if len(got) > dialogDigestBudgetBytes+overhead {
		t.Fatalf("digest exceeds budget: got %d bytes (budget %d, overhead allowance %d)", len(got), dialogDigestBudgetBytes, overhead)
	}
	if !strings.Contains(got, strings.TrimSpace(dialogDigestTruncationMarker)) {
		t.Fatalf("expected truncation marker in digest, got last 200 chars: %q", got[max(0, len(got)-200):])
	}
}
