package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// TestStartQuestionPoller_TicksMatchClockAdvances asserts the poller ticks exactly once per Advance(1s) —
// not "eventually", by the clock (T7, dev-plan.md). The stage stays pending so each tick's pollQuestions
// call is a cheap no-op; pollTickHook is the only way to observe the tick count from outside the goroutine.
func TestStartQuestionPoller_TicksMatchClockAdvances(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{ID: "s1"}}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	fc := clock.NewFake(time.Unix(0, 0))
	o := &Orchestrator{
		opts:  Options{RunDir: runDir, Store: store, Stages: stages},
		graph: graph.NewStore(graph.NewRegistry(stages)),
		clock: fc,
	}

	var ticks int32
	o.pollTickHook = func() { atomic.AddInt32(&ticks, 1) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o.startQuestionPoller(ctx)

	const n = 3
	for i := 1; i <= n; i++ {
		fc.BlockUntil(1)
		fc.Advance(time.Second)
		waitForTickCount(t, &ticks, int32(i))
	}

	// No Advance since the last tick: the count must not keep climbing on its own.
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt32(&ticks); got != n {
		t.Fatalf("ticks = %d after %d Advance(1s) calls, want exactly %d", got, n, n)
	}
}

func waitForTickCount(t *testing.T, counter *int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(counter) >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("tick counter did not reach %d within 1s (got %d)", want, atomic.LoadInt32(counter))
}

// TestRelocateMisplacedQuestions_ThrottlesRootScanByClock asserts the root_dir scan throttle
// (relocateScanInterval) is driven by the clock: a second scan inside the window is skipped, and advancing
// the clock past the window lets the next scan run (T7, dev-plan.md).
func TestRelocateMisplacedQuestions_ThrottlesRootScanByClock(t *testing.T) {
	root := t.TempDir()
	stageDir := t.TempDir() // sibling, not nested under root — pathInside(root, stageDir) is false
	stageID := "s1"

	// activeDialogPhase needs a <phase>.jsonl file in stageDir to pick a phase to relocate into.
	if err := os.WriteFile(filepath.Join(stageDir, "implementation.jsonl"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	fc := clock.NewFake(time.Unix(0, 0))
	o := &Orchestrator{
		opts:         Options{RootDir: root},
		lastRootScan: map[string]time.Time{},
		clock:        fc,
	}

	write := func(name string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(`{"id":"`+name+`","question":"q?"}`), 0644); err != nil {
			t.Fatal(err)
		}
	}

	write("implementation.q1.question.json")
	o.relocateMisplacedQuestions(stageID, stageDir, true)
	if _, err := os.Stat(filepath.Join(stageDir, "implementation.q1.question.json")); err != nil {
		t.Fatalf("first scan should have relocated q1: %v", err)
	}

	write("implementation.q2.question.json")
	o.relocateMisplacedQuestions(stageID, stageDir, true) // no Advance — still inside the throttle window
	if _, err := os.Stat(filepath.Join(stageDir, "implementation.q2.question.json")); err == nil {
		t.Fatal("second scan inside relocateScanInterval must be throttled — q2 should not be relocated yet")
	}

	fc.Advance(relocateScanInterval)
	o.relocateMisplacedQuestions(stageID, stageDir, true)
	if _, err := os.Stat(filepath.Join(stageDir, "implementation.q2.question.json")); err != nil {
		t.Fatalf("scan after relocateScanInterval elapsed should have relocated q2: %v", err)
	}
}
