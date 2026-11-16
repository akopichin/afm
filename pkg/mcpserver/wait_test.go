package mcpserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/akopichin/afm/contracts/state"
)

type waitOutcome struct {
	res waitResult
	err error
}

// startWait runs a wait in the background and returns where its outcome arrives.
func (h *harness) startWait(ctx context.Context, cursor string, seconds int) <-chan waitOutcome {
	// The notifier takes its baseline before registering its ticker; starting the wait after that keeps a
	// baseline broadcast from waking it spuriously.
	if err := h.svc.ensureNotifier(); err != nil {
		h.t.Fatal(err)
	}
	h.clk.BlockUntil(1)
	out := make(chan waitOutcome, 1)
	go func() {
		res, err := h.svc.wait(ctx, runWaitInput{RunID: "run-1", Cursor: cursor, TimeoutSeconds: seconds})
		out <- waitOutcome{res, err}
	}()
	return out
}

// parked waits until the notifier ticker and the wait's own timer are both registered on the fake clock.
func (h *harness) parked() { h.clk.BlockUntil(2) }

func recv(t *testing.T, ch <-chan waitOutcome) waitOutcome {
	t.Helper()
	select {
	case got := <-ch:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("wait did not return")
		return waitOutcome{}
	}
}

func (h *harness) cursor() string { return h.svc.buildStatus(context.Background()).Cursor }

func TestWaitImmediateOnStaleCursor(t *testing.T) {
	h := newHarness(t, "")
	h.run.setStatus("a", state.StatusFailed)
	got := recv(t, h.startWait(context.Background(), "v1:run-1:0123456789abcdef", 30))
	if got.err != nil || !got.res.Changed || got.res.Reset || got.res.TimedOut || len(got.res.PendingActions) != 1 {
		t.Fatalf("outcome = %+v", got)
	}
}

func TestWaitResetOnMalformedDigest(t *testing.T) {
	h := newHarness(t, "")
	for _, cursor := range []string{
		"v1:run-1:not-a-digest", "v1:run-1:0123456789abcde", "v1:run-1:0123456789abcdef0",
		"v1:run-1:0123456789abcdeg", "v1:run-1:0123456789ABCDEF", "v1:run-1:",
	} {
		got := recv(t, h.startWait(context.Background(), cursor, 30))
		if got.err != nil || !got.res.Reset {
			t.Fatalf("cursor %q: %+v", cursor, got)
		}
	}
}

func TestWaitResetOnForeignOrBrokenCursor(t *testing.T) {
	h := newHarness(t, "")
	for _, cursor := range []string{"v1:other-run:abc", "garbage"} {
		got := recv(t, h.startWait(context.Background(), cursor, 30))
		if got.err != nil || !got.res.Reset || got.res.Cursor == "" {
			t.Fatalf("cursor %q: %+v", cursor, got)
		}
	}
	res, err := h.svc.wait(context.Background(), runWaitInput{RunID: "another", Cursor: h.cursor()})
	if err != nil || !res.Reset {
		t.Fatalf("foreign run id: %+v, %v", res, err)
	}
}

func TestWaitWakesOnBusImpulse(t *testing.T) {
	h := newHarness(t, "")
	h.run.setStatus("a", state.StatusRunning)
	out := h.startWait(context.Background(), h.cursor(), 30)
	h.parked()

	h.run.setStatus("a", state.StatusFailed)
	h.bus.impulse()
	h.clk.BlockUntil(3) // the coalescing timer
	select {
	case <-out:
		t.Fatal("woke before the coalescing delay passed")
	default:
	}
	h.clk.Advance(coalesceDelay)

	got := recv(t, out)
	if got.err != nil || !got.res.Changed || got.res.TimedOut || len(got.res.PendingActions) != 1 || got.res.PendingActions[0].Type != actionFailed {
		t.Fatalf("outcome = %+v", got)
	}
}

func TestWaitCoalescesBurstIntoOneWake(t *testing.T) {
	h := newHarness(t, "")
	h.run.setStatus("a", state.StatusRunning)
	out := h.startWait(context.Background(), h.cursor(), 30)
	h.parked()
	h.run.setStatus("a", state.StatusFailed)
	for range 5 {
		h.bus.impulse()
	}
	h.clk.BlockUntil(3)
	h.clk.Advance(coalesceDelay)
	if got := recv(t, out); !got.res.Changed {
		t.Fatalf("outcome = %+v", got)
	}
}

func TestWaitWakesOnDroppedEventsWithoutDelay(t *testing.T) {
	h := newHarness(t, "")
	h.run.setStatus("a", state.StatusRunning)
	out := h.startWait(context.Background(), h.cursor(), 30)
	h.parked()

	h.run.setStatus("a", state.StatusFailed)
	h.bus.dropped.Add(3)
	h.bus.impulse() // no clock advance: lost events leave nothing to coalesce

	if got := recv(t, out); got.err != nil || !got.res.Changed {
		t.Fatalf("outcome = %+v", got)
	}
}

func TestWaitWakesOnTickForFileChanges(t *testing.T) {
	h := newHarness(t, "reply")
	h.run.setStatus("a", state.StatusAwaitingUserInput)
	out := h.startWait(context.Background(), h.cursor(), 30)
	h.parked()

	// A question file is written by the agent: no bus event, only the periodic recompute can see it.
	h.ask("a", "q1", "now?")
	h.clk.Advance(pollInterval)

	got := recv(t, out)
	if got.err != nil || !got.res.Changed || len(actionsOf(t, got.res.statusResult, actionQuestion)) != 1 {
		t.Fatalf("outcome = %+v", got)
	}
}

func TestWaitTimesOutUnchanged(t *testing.T) {
	h := newHarness(t, "")
	h.run.setStatus("a", state.StatusRunning)
	cursor := h.cursor()
	out := h.startWait(context.Background(), cursor, 5)
	h.parked()

	h.clk.Advance(5 * time.Second)

	got := recv(t, out)
	if got.err != nil || got.res.Changed || !got.res.TimedOut || got.res.Cursor != cursor {
		t.Fatalf("outcome = %+v", got)
	}
}

func TestWaitTimeoutIsCapped(t *testing.T) {
	h := newHarness(t, "")
	h.run.setStatus("a", state.StatusRunning)
	out := h.startWait(context.Background(), h.cursor(), 100000)
	h.parked()
	h.clk.Advance(maxWaitSeconds * time.Second)
	if got := recv(t, out); !got.res.TimedOut {
		t.Fatalf("outcome = %+v", got)
	}
}

func TestWaitCancelEndsOnlyTheWait(t *testing.T) {
	h := newHarness(t, "")
	h.run.setStatus("a", state.StatusRunning)
	ctx, cancel := context.WithCancel(context.Background())
	out := h.startWait(ctx, h.cursor(), 30)
	h.parked()

	cancel()

	if got := recv(t, out); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("outcome = %+v", got)
	}
	if calls := h.cmds.callList(); len(calls) != 0 {
		t.Fatalf("a cancelled wait must not touch the run: %v", calls)
	}
}

func TestWaitEndsOnClose(t *testing.T) {
	h := newHarness(t, "")
	h.run.setStatus("a", state.StatusRunning)
	out := h.startWait(context.Background(), h.cursor(), 30)
	h.parked()

	if err := h.svc.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := recv(t, out); !errors.Is(got.err, errClosed) {
		t.Fatalf("outcome = %+v", got)
	}
	if h.bus.unsubs.Load() != 1 {
		t.Fatalf("notifier unsubscribed %d times", h.bus.unsubs.Load())
	}
}

func TestWaitCapacityLimit(t *testing.T) {
	h := newHarness(t, "")
	h.run.setStatus("a", state.StatusRunning)
	cursor := h.cursor()
	var parked []<-chan waitOutcome
	for range maxWaits {
		parked = append(parked, h.startWait(context.Background(), cursor, 30))
	}
	h.clk.BlockUntil(1 + maxWaits)

	_, err := h.svc.wait(context.Background(), runWaitInput{RunID: "run-1", Cursor: cursor})
	if got := toolError(err).Code; got != codeWaitCapacity {
		t.Fatalf("ninth wait: %v", err)
	}
	h.clk.Advance(30 * time.Second)
	for _, ch := range parked {
		if got := recv(t, ch); !got.res.TimedOut {
			t.Fatalf("outcome = %+v", got)
		}
	}
	// The slots are free again.
	if _, err := h.svc.wait(context.Background(), runWaitInput{RunID: "run-1", Cursor: "v1:run-1:stale"}); err != nil {
		t.Fatalf("after release: %v", err)
	}
}

func TestWaitTimeoutRereadsStatus(t *testing.T) {
	h := newHarness(t, "")
	h.run.setStatus("a", state.StatusRunning)
	cursor := h.cursor()
	// The timeout is shorter than the notifier tick, so only the final read can see the change.
	out := h.startWait(context.Background(), cursor, 1)
	h.parked()

	h.run.setStatus("a", state.StatusFailed)
	h.clk.Advance(time.Second)

	got := recv(t, out)
	if got.err != nil || !got.res.TimedOut || !got.res.Changed || got.res.Cursor == cursor || len(actionsOf(t, got.res.statusResult, actionFailed)) != 1 {
		t.Fatalf("outcome = %+v", got)
	}
}

func TestWaitWakesOnPlanRevisionChange(t *testing.T) {
	h := newHarness(t, "approve")
	h.run.setStatus("plan", state.StatusAwaitingApproval)
	awaitingPlan(h, "first plan")
	cursor := h.cursor()
	out := h.startWait(context.Background(), cursor, 30)
	h.parked()

	// The plan is revised while the stage stays awaiting approval: no status change and no bus event.
	h.writeFile("plan/plan.md", "second plan")
	h.approval.snaps["plan"] = controlSnapWithPlan("rev-2", "second plan")
	h.clk.Advance(pollInterval)

	got := recv(t, out)
	approvals := actionsOf(t, got.res.statusResult, actionApproval)
	if got.err != nil || !got.res.Changed || got.res.TimedOut || got.res.Cursor == cursor || len(approvals) != 1 || approvals[0].Revision != "rev-2" {
		t.Fatalf("outcome = %+v", got)
	}
}
