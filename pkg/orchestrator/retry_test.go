package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

func TestBuildRetryContext_FullActionNotTruncated(t *testing.T) {
	stageDir := t.TempDir()
	longOutput := strings.Repeat("output-line ", 100) // far longer than any sane truncate_output limit
	line := fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"%s"}}]}}`, longOutput)
	if err := os.WriteFile(filepath.Join(stageDir, "implementation.jsonl"), []byte(line+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got := buildRetryContext(stageDir, phaseImplementation)
	if !strings.Contains(got, longOutput) {
		t.Errorf("retry context does not contain full action text — got truncated/missing content: %q", got)
	}
}

func TestBuildRetryContext_MissingLogReturnsEmpty(t *testing.T) {
	stageDir := t.TempDir()
	if got := buildRetryContext(stageDir, phaseImplementation); got != "" {
		t.Errorf("expected empty context for missing jsonl, got %q", got)
	}
}

// TestIsRetryableError: перенесён из completion_test.go при выносе
// completion.go в pkg/orchestrator/stagefiles (Task 3 orchestrator-split) —
// isRetryableError и константы match* остаются в этом пакете (retry.go,
// errors.go), поэтому тест здесь, а не в stagefiles.
func TestIsRetryableError(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"You've hit your limit", true},
		{matchRateLimit + " exceeded", true},
		{matchTooManyRequests, true},
		{matchOverloaded, true},
		{matchAtCapacity, true},
		{"500 Internal Server Error", true},
		{matchInternalServerError, true},
		{"something went wrong", false},
		{"", false},
	}
	for _, c := range cases {
		var err error
		if c.msg != "" {
			err = fmt.Errorf("%s", c.msg)
		}
		if got := isRetryableError(err); got != c.want {
			t.Errorf("isRetryableError(%q) = %v, want %v", c.msg, got, c.want)
		}
	}

	if isRetryableError(nil) {
		t.Error("nil should not be retryable")
	}
}

// TestRunWithRetry_SkipsIfAlreadyPausedBeforeStart is a regression test for a
// second instance of the same race class as withBeforeHook's paused-recheck
// (hooks_test.go): SpawnAgent queues a goroutine behind the concurrency
// semaphore (e.g. Continue() resumed a stage into a full max_parallel
// bucket), and the user re-Pauses the stage while that goroutine is still
// waiting for a slot — a legitimate window, since Pause() only needs
// interruptChans to be registered to signal an *already-running* agent, and
// interruptChans isn't registered until runWithRetry actually starts. Every
// agent-driven phase (planning/implementation/review/autonomous, fresh or
// *WithFeedback resume) funnels through runWithRetry regardless of whether
// its caller was wrapped in withBeforeHook, so this is the one place that
// protects all of them uniformly: runWithRetry must not start a real agent
// call if the stage is already paused by the time it gets its turn.
func TestRunWithRetry_SkipsIfAlreadyPausedBeforeStart(t *testing.T) {
	o, _ := setupHookOrch(t, "s1") // seeds status = running
	if _, ok := o.Trigger("s1", bus.EvPause, bus.GuardCtx{}, "test"); !ok {
		t.Fatal("EvPause should be allowed from running")
	}

	called := false
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(retryContext string) error {
			called = true
			return nil
		},
		nil,
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if called {
		t.Error("agentFn must not run — the stage was already paused before runWithRetry started")
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusPaused {
		t.Errorf("status = %v, want paused (unchanged)", got)
	}
}
