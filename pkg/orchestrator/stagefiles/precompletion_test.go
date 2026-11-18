package stagefiles

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Two finishers of one round write the same report: each uses a temp file of its own, so neither fails over the
// other's rename and the report is always one writer's complete text.
func TestWritePreCompletionRound_ConcurrentWritersNeverCollide(t *testing.T) {
	stageDir := t.TempDir()
	const writers = 16
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			text := strings.Repeat(fmt.Sprintf("writer %d\n", i), 200)
			errs[i] = WritePreCompletionRound(stageDir, 7, text, text)
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}
	data, err := os.ReadFile(PreCompletionReportPath(stageDir, 7))
	if err != nil {
		t.Fatal(err)
	}
	first := strings.SplitN(string(data), "\n", 2)[0]
	if string(data) != strings.Repeat(first+"\n", 200) {
		t.Fatalf("report is a mix of writers:\n%.200s", data)
	}
	left, err := filepath.Glob(filepath.Join(PreCompletionDir(stageDir), "*", "*.tmp"))
	if err != nil || len(left) != 0 {
		t.Fatalf("temp files left: %v (err %v)", left, err)
	}
}

// The active feedback is a round's only when its mark names that round: the number alone, the same in every cycle,
// does not make another round's file this one's.
func TestLoadPreCompletionFeedbackOf_OnlyTheRoundThatWroteIt(t *testing.T) {
	stageDir := t.TempDir()
	if _, ok := LoadPreCompletionFeedbackOf(stageDir, 1, 5); ok {
		t.Fatal("feedback read with no file")
	}
	text := PreCompletionFeedbackMark(1, 5) + "\nfix it\n"
	if err := WritePreCompletionRound(stageDir, 5, "report", text); err != nil {
		t.Fatal(err)
	}
	if got, ok := LoadPreCompletionFeedbackOf(stageDir, 1, 5); !ok || got != text {
		t.Fatalf("feedback of its own round = %q, %v", got, ok)
	}
	for _, other := range []struct {
		round int
		seq   uint64
	}{{1, 9}, {2, 5}} {
		if _, ok := LoadPreCompletionFeedbackOf(stageDir, other.round, other.seq); ok {
			t.Fatalf("round %d (seq %d) read the feedback of round 1 (seq 5)", other.round, other.seq)
		}
	}
}

// A round recorded without feedback leaves no active one: the next author reads nothing an earlier round said.
func TestWritePreCompletionRound_EmptyFeedbackDropsTheActiveOne(t *testing.T) {
	stageDir := t.TempDir()
	if err := WritePreCompletionRound(stageDir, 5, "report", PreCompletionFeedbackMark(1, 5)+"fix it\n"); err != nil {
		t.Fatal(err)
	}
	if err := WritePreCompletionRound(stageDir, 8, "report", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(PreCompletionFeedbackPath(stageDir)); !os.IsNotExist(err) {
		t.Fatalf("active feedback after a round without one: %v", err)
	}
	if _, err := os.Stat(PreCompletionReportPath(stageDir, 5)); err != nil {
		t.Fatalf("the earlier report is gone: %v", err)
	}
}
