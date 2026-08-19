package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/orchestrator"
)

// runOrchestratorAsync starts orch.Run(ctx) in the background and registers a
// t.Cleanup that cancels ctx and waits for Run to actually RETURN before any
// earlier-registered cleanup (typically store.Close()) runs — t.Cleanup fires
// in LIFO order, so a cleanup registered here always runs before one
// registered earlier in the same test.
//
// Found via a widespread, previously undiagnosed flake: dozens of tests across
// this package used to fire orch.Run(ctx) in a bare goroutine and just wait
// for a stage status to look terminal (via waitForStatus or an event
// subscription) before returning — never for Run itself to return. Run's own
// shutdown sequence (cancel → concurrency.WaitAgents → return, see
// Orchestrator.Run) is what actually guarantees every agent goroutine has
// stopped touching the store; a terminal-looking stage status doesn't. Under
// system load, the agent goroutine's own EvFail-on-cancellation Store.Apply
// call could still be in flight when the test function returned and
// store.Close() ran — nil-ing s.eventsLog. Store.Apply on a nilled-out
// eventsLog doesn't panic (os.File's methods are nil-receiver safe): it
// silently returns fs.ErrInvalid, whose text is "invalid argument",
// indistinguishable at a glance from a genuine OS-level EINVAL. That
// misdirection is what made this look like unexplained environment
// flakiness (`storage failure: write events.jsonl: invalid argument`) across
// many unrelated-looking tests for a long time. Waiting on Run's own
// completion signal here removes the guesswork.
//
// This alone wasn't the whole story: the actual reason Run() could take so
// long to return after cancellation was a separate, real bug in
// pkg/executor.(*Executor).run — cmd.Process.Kill() only killed the direct
// child, not its process group, so a script whose last line spawns a
// grandchild without exec'ing into it (e.g. a trailing `sleep N`) left that
// grandchild orphaned, holding the inherited stdout pipe open — the
// executor's own <-done wouldn't unblock until the orphan exited on its
// own. Fixed there via killProcessGroup + Setpgid; this helper's honest
// timeout is what made that delay visible instead of silently corrupting
// the store.
func runOrchestratorAsync(ctx context.Context, t *testing.T, orch *orchestrator.Orchestrator, cancel context.CancelFunc) {
	t.Helper()
	done := make(chan struct{})
	go func() { _ = orch.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("orch.Run did not return within 10s of cancellation")
		}
	})
}
