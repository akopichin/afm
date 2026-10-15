package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/state"
)

// countOccurrences reports how many non-overlapping times sub appears in s.
func countOccurrences(s, sub string) int {
	if sub == "" {
		return 0
	}
	return strings.Count(s, sub)
}

// ---- T6.6: §10.3's exact 9 crash-point/idempotency tests ----

// TestDoubleClick: the SAME accept request_id submitted twice in a row (e.g. a UI double-click re-sending the
// identical body) must deliver exactly once — the second call is a clean idempotent replay, not an error and
// not a second delivery.
func TestDoubleClick(t *testing.T) {
	o, store := newFeedbackIntentTestOrchestrator(t)
	ctx := context.Background()
	activateStage(t, o, feedbackIntentTestStageID)
	gen := store.StageGeneration(feedbackIntentTestStageID)
	proposalID := seedAcceptedFeedbackProposal(ctx, t, o, feedbackIntentTestStageID, "bound the retry loop", gen)

	if err := o.AcceptProposal(ctx, proposalID, "accept-dup", state.Actor{Kind: "user"}); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	waitForProposalStatus(t, o, proposalID, assistants.ProposalDelivered, time.Second)

	// Double-click: identical request id, resent.
	if err := o.AcceptProposal(ctx, proposalID, "accept-dup", state.Actor{Kind: "user"}); err != nil {
		t.Fatalf("second (duplicate) accept must succeed idempotently, got: %v", err)
	}

	body, err := os.ReadFile(feedbackMdPath(o, feedbackIntentTestStageID))
	if err != nil {
		t.Fatalf("read feedback.md: %v", err)
	}
	if n := countOccurrences(string(body), "bound the retry loop"); n != 1 {
		t.Fatalf("feedback must be delivered exactly once, found %d occurrences in: %s", n, body)
	}
}

// TestRetryAfterTimeout: the client never saw the first response (e.g. its HTTP call timed out) but the
// server-side commit actually succeeded; a retry with the SAME request_id must return the same success, not
// re-deliver.
func TestRetryAfterTimeout(t *testing.T) {
	o, store := newFeedbackIntentTestOrchestrator(t)
	ctx := context.Background()
	activateStage(t, o, feedbackIntentTestStageID)
	gen := store.StageGeneration(feedbackIntentTestStageID)
	proposalID := seedAcceptedFeedbackProposal(ctx, t, o, feedbackIntentTestStageID, "add a circuit breaker", gen)

	if err := o.AcceptProposal(ctx, proposalID, "accept-timeout", state.Actor{Kind: "user"}); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	waitForProposalStatus(t, o, proposalID, assistants.ProposalDelivered, time.Second)

	// Client believes the call timed out and retries with the SAME request id.
	if err := o.AcceptProposal(ctx, proposalID, "accept-timeout", state.Actor{Kind: "user"}); err != nil {
		t.Fatalf("retry-after-timeout with the same request id must succeed, got: %v", err)
	}
	p, _ := o.Proposal(proposalID)
	if p.Status != assistants.ProposalDelivered {
		t.Fatalf("status must remain delivered, got %s", p.Status)
	}
	body, _ := os.ReadFile(feedbackMdPath(o, feedbackIntentTestStageID))
	if n := countOccurrences(string(body), "add a circuit breaker"); n != 1 {
		t.Fatalf("expected exactly one delivery, found %d: %s", n, body)
	}
}

// TestAcceptFromTwoTabs: two DIFFERENT browser tabs race to accept the SAME proposal with two genuinely
// different request ids. Exactly one must win; the other gets a clean rejection, never a second delivery.
func TestAcceptFromTwoTabs(t *testing.T) {
	o, store := newFeedbackIntentTestOrchestrator(t)
	ctx := context.Background()
	activateStage(t, o, feedbackIntentTestStageID)
	gen := store.StageGeneration(feedbackIntentTestStageID)
	proposalID := seedAcceptedFeedbackProposal(ctx, t, o, feedbackIntentTestStageID, "cap the queue size", gen)

	err1 := o.AcceptProposal(ctx, proposalID, "accept-tab-1", state.Actor{Kind: "user"})
	err2 := o.AcceptProposal(ctx, proposalID, "accept-tab-2", state.Actor{Kind: "user"})

	if err1 == nil && err2 == nil {
		t.Fatal("exactly one of the two racing accepts must fail — both succeeded")
	}
	if err1 != nil && err2 != nil {
		t.Fatalf("exactly one of the two racing accepts must succeed — both failed: %v / %v", err1, err2)
	}
	winner, loserErr := err1, err2
	if err1 != nil {
		winner, loserErr = err2, err1
	}
	if winner != nil {
		t.Fatalf("the winning accept must not itself error: %v", winner)
	}
	var rej *state.OperationRejectedError
	if !isOperationRejected(loserErr, &rej) || rej.Code != assistants.CodeStaleRevision {
		t.Fatalf("the losing tab must get a clean stale_revision rejection, got: %v", loserErr)
	}

	waitForProposalStatus(t, o, proposalID, assistants.ProposalDelivered, time.Second)
	body, _ := os.ReadFile(feedbackMdPath(o, feedbackIntentTestStageID))
	if n := countOccurrences(string(body), "cap the queue size"); n != 1 {
		t.Fatalf("expected exactly one delivery from the two racing tabs, found %d: %s", n, body)
	}
}

func isOperationRejected(err error, target **state.OperationRejectedError) bool {
	rej, ok := err.(*state.OperationRejectedError)
	if ok {
		*target = rej
	}
	return ok
}

// TestCrashAfterAccepted simulates a process crash immediately after the "accepted + FeedbackIntent" commit,
// BEFORE any delivery attempt ever ran (externalWork never fired) — recovery (a fresh Store.Open + a fresh
// Orchestrator, exactly like a real restart) must still complete delivery.
func TestCrashAfterAccepted(t *testing.T) {
	o, store := newFeedbackIntentTestOrchestrator(t)
	ctx := context.Background()
	activateStage(t, o, feedbackIntentTestStageID)
	dir := o.opts.RunDir

	proposalID := seedAcceptedFeedbackProposal(ctx, t, o, feedbackIntentTestStageID, "add a timeout", store.StageGeneration(feedbackIntentTestStageID))
	p, _ := o.Proposal(proposalID)

	// Commit JUST the accept step directly (bypassing AcceptProposal's externalWork) — the exact state a crash
	// right after step 1 of §10.3's order would leave behind.
	intentID := deriveFeedbackIntentID("accept-crash-1")
	statusEff, err := proposalStatusEffect(proposalID, state.ProposalStatusChangedPayload{Status: string(assistants.ProposalAccepted), OperationID: "accept-crash-1"})
	if err != nil {
		t.Fatalf("build status effect: %v", err)
	}
	intentEff, err := feedbackIntentCreatedEffect(p.StageID, intentID, state.FeedbackIntentCreatedPayload{
		StageID: p.StageID, Feedback: "add a timeout", Origin: string(state.FeedbackOriginProposal),
		ProposalID: proposalID, ExpectedStageGeneration: p.ExpectedStageGeneration,
	})
	if err != nil {
		t.Fatalf("build intent effect: %v", err)
	}
	op := state.Operation{ID: "accept-crash-1", Expect: state.Preconditions{ProposalStatus: map[string]assistants.ProposalStatus{proposalID: assistants.ProposalPending}}, Effects: []state.Effect{statusEff, intentEff}}
	if _, err := store.ApplyRunOperation(op, nil); err != nil { // nil externalWork == "crashed before delivery ran"
		t.Fatalf("commit accept (pre-crash): %v", err)
	}
	store.Close() // release the flock, standing in for the OS reclaiming it on process death

	// "Restart": fresh Store.Open + fresh Orchestrator against the SAME run directory.
	store2, err := state.Open(dir, []string{feedbackIntentTestStageID})
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store2.Close()
	o2 := reopenFeedbackIntentOrchestrator(o, store2)

	o2.recoverFeedbackIntents(context.Background())

	waitForProposalStatus(t, o2, proposalID, assistants.ProposalDelivered, time.Second)
	body, err := os.ReadFile(feedbackMdPath(o2, feedbackIntentTestStageID))
	if err != nil {
		t.Fatalf("read feedback.md after recovery: %v", err)
	}
	if !strings.Contains(string(body), "add a timeout") {
		t.Fatalf("recovery must still deliver the feedback: %s", body)
	}
}

// reopenFeedbackIntentOrchestrator builds a fresh Orchestrator sharing o's options/graph shape but a NEW
// Store — the test-level stand-in for "afm restarted and re-opened the same run directory."
func reopenFeedbackIntentOrchestrator(o *Orchestrator, store2 *state.Store) *Orchestrator {
	opts := o.opts
	opts.Store = store2
	cb := bus.NewCriticalBus(16)
	return &Orchestrator{
		opts: opts, graph: o.graph, runner: o.runner,
		ui: bus.NewUIBus(), critical: cb, fsm: bus.NewFSM(store2),
		concurrency: concurrency.New(cb, opts.Stages, "claude", 0, nil),
	}
}

// TestCrashAfterTransitionBeforeFeedbackFile simulates a crash exactly between reviseRunning's durable
// EvRevise commit and its feedback.md write (§10.3's SECOND crash point) — the stage is left in "revising"
// with the intent still "accepted" and NO feedback.md. Once the stage naturally becomes interruptible again
// (the existing stage-recovery machinery resuming a stuck "revising" stage — simulated here by firing
// EvStartRun directly, exactly like runWithRetry's own resume loop would), delivery must complete.
func TestCrashAfterTransitionBeforeFeedbackFile(t *testing.T) {
	o, store := newFeedbackIntentTestOrchestrator(t)
	ctx := context.Background()
	activateStage(t, o, feedbackIntentTestStageID)
	gen := store.StageGeneration(feedbackIntentTestStageID)
	proposalID := seedAcceptedFeedbackProposal(ctx, t, o, feedbackIntentTestStageID, "add jitter", gen)
	p, _ := o.Proposal(proposalID)

	// Commit JUST the accept step directly (as TestCrashAfterAccepted does) — delivery must NOT run yet, so we
	// can then simulate reviseRunning's OWN EvRevise commit succeeding and crashing before save() ever runs.
	intentID := deriveFeedbackIntentID("accept-crash-2")
	statusEff, err := proposalStatusEffect(proposalID, state.ProposalStatusChangedPayload{Status: string(assistants.ProposalAccepted), OperationID: "accept-crash-2"})
	if err != nil {
		t.Fatalf("build status effect: %v", err)
	}
	intentEff, err := feedbackIntentCreatedEffect(p.StageID, intentID, state.FeedbackIntentCreatedPayload{
		StageID: p.StageID, Feedback: "add jitter", Origin: string(state.FeedbackOriginProposal),
		ProposalID: proposalID, ExpectedStageGeneration: p.ExpectedStageGeneration,
	})
	if err != nil {
		t.Fatalf("build intent effect: %v", err)
	}
	op := state.Operation{ID: "accept-crash-2", Expect: state.Preconditions{ProposalStatus: map[string]assistants.ProposalStatus{proposalID: assistants.ProposalPending}}, Effects: []state.Effect{statusEff, intentEff}}
	if _, err := store.ApplyRunOperation(op, nil); err != nil { // nil externalWork == delivery never attempted yet
		t.Fatalf("commit accept: %v", err)
	}

	// Simulate reviseRunning's OWN EvRevise commit succeeding, then crashing before save() ever runs — call the
	// transition directly rather than through reviseRunning/attemptFeedbackDelivery.
	if _, ok := o.Trigger(feedbackIntentTestStageID, bus.EvRevise, bus.GuardCtx{}, "feedback intent "+intentID); !ok {
		t.Fatal("EvRevise must apply from running")
	}
	if _, err := os.Stat(feedbackMdPath(o, feedbackIntentTestStageID)); err == nil {
		t.Fatal("test setup: feedback.md must not exist yet at this simulated crash point")
	}
	if p, _ := o.Proposal(proposalID); p.Status != assistants.ProposalAccepted {
		t.Fatalf("intent must still be accepted (deferred while revising), got proposal status %s", p.Status)
	}

	// The existing stage-recovery machinery resumes the stuck "revising" stage.
	if _, ok := o.Trigger(feedbackIntentTestStageID, bus.EvStartRun, bus.GuardCtx{}, "resumed"); !ok {
		t.Fatal("EvStartRun must apply from revising")
	}

	waitForProposalStatus(t, o, proposalID, assistants.ProposalDelivered, time.Second)
	body, err := os.ReadFile(feedbackMdPath(o, feedbackIntentTestStageID))
	if err != nil {
		t.Fatalf("read feedback.md: %v", err)
	}
	if n := countOccurrences(string(body), "add jitter"); n != 1 {
		t.Fatalf("expected exactly one write once recovery completes it, found %d: %s", n, body)
	}
}

// TestCrashAfterFeedbackBeforeSpawn simulates a crash AFTER feedback.md is durably on disk but BEFORE the
// final "mark delivered" commit (and, for a live process, before the interrupt signal/spawn actually fires) —
// §10.3's THIRD crash point. Recovery must notice feedback.md already carries the sentinel (SaveFeedbackOnce's
// own idempotency), avoid re-appending, and still complete the delivery.
func TestCrashAfterFeedbackBeforeSpawn(t *testing.T) {
	o, store := newFeedbackIntentTestOrchestrator(t)
	ctx := context.Background()
	activateStage(t, o, feedbackIntentTestStageID)
	gen := store.StageGeneration(feedbackIntentTestStageID)
	proposalID := seedAcceptedFeedbackProposal(ctx, t, o, feedbackIntentTestStageID, "add a deadline", gen)
	p, _ := o.Proposal(proposalID)

	// Commit the accept step (as TestCrashAfterAccepted does), then manually perform EXACTLY the write step —
	// simulating that reviseRunning got as far as SaveFeedbackOnce but crashed before attemptFeedbackDelivery
	// ever reached finishFeedbackDelivery.
	intentID := deriveFeedbackIntentID("accept-crash-3")
	statusEff, _ := proposalStatusEffect(proposalID, state.ProposalStatusChangedPayload{Status: string(assistants.ProposalAccepted), OperationID: "accept-crash-3"})
	intentEff, _ := feedbackIntentCreatedEffect(p.StageID, intentID, state.FeedbackIntentCreatedPayload{
		StageID: p.StageID, Feedback: "add a deadline", Origin: string(state.FeedbackOriginProposal),
		ProposalID: proposalID, ExpectedStageGeneration: p.ExpectedStageGeneration,
	})
	op := state.Operation{ID: "accept-crash-3", Expect: state.Preconditions{ProposalStatus: map[string]assistants.ProposalStatus{proposalID: assistants.ProposalPending}}, Effects: []state.Effect{statusEff, intentEff}}
	if _, err := store.ApplyRunOperation(op, nil); err != nil {
		t.Fatalf("commit accept: %v", err)
	}
	if wrote, err := state.SaveFeedbackOnce(o.stageDirFor(feedbackIntentTestStageID), intentID, "add a deadline"); err != nil || !wrote {
		t.Fatalf("simulate pre-crash feedback write: wrote=%v err=%v", wrote, err)
	}

	// Recovery re-attempts delivery; SaveFeedbackOnce must no-op on the already-written content.
	o.attemptFeedbackDelivery(ctx, intentID)

	waitForProposalStatus(t, o, proposalID, assistants.ProposalDelivered, time.Second)
	body, err := os.ReadFile(feedbackMdPath(o, feedbackIntentTestStageID))
	if err != nil {
		t.Fatalf("read feedback.md: %v", err)
	}
	if n := countOccurrences(string(body), "add a deadline"); n != 1 {
		t.Fatalf("expected exactly one write across the simulated crash, found %d: %s", n, body)
	}
}

// TestReviseConcurrentWithStageCompletion: the stage independently completes WHILE a FeedbackIntent delivery
// is in flight — a late/racing delivery attempt that read an earlier "running" status must not resurrect or
// silently "succeed" against an already-completed stage; it must land on a clean terminal rejection.
func TestReviseConcurrentWithStageCompletion(t *testing.T) {
	o, store := newFeedbackIntentTestOrchestrator(t)
	ctx := context.Background()
	activateStage(t, o, feedbackIntentTestStageID)
	gen := store.StageGeneration(feedbackIntentTestStageID)
	proposalID := seedAcceptedFeedbackProposal(ctx, t, o, feedbackIntentTestStageID, "note", gen)

	// Move to retrying first (a deferred bucket — delivery must NOT be attempted yet) so the accept itself does
	// not immediately deliver.
	o.Trigger(feedbackIntentTestStageID, bus.EvScheduleRetry, bus.GuardCtx{}, "")
	if err := o.AcceptProposal(ctx, proposalID, "accept-race", state.Actor{Kind: "user"}); err != nil {
		t.Fatalf("AcceptProposal: %v", err)
	}
	if p, _ := o.Proposal(proposalID); p.Status != assistants.ProposalAccepted {
		t.Fatalf("expected deferred delivery while retrying, got %s", p.Status)
	}

	// The stage's OWN retry loop independently completes it (EvComplete is legal directly from retrying) —
	// "concurrent with" our still-pending revise intent, which never got a chance to deliver. The hook on this
	// very transition must land the intent on a clean terminal rejection, never a silent "still accepted
	// forever".
	o.Trigger(feedbackIntentTestStageID, bus.EvComplete, bus.GuardCtx{}, "")
	waitForProposalStatus(t, o, proposalID, assistants.ProposalDeliveryFailed, time.Second)

	// A late, racing second attempt (as if some other goroutine had already read "running"/"paused" before the
	// completion committed) must be a safe no-op — never re-opening a completed stage.
	intentID := deriveFeedbackIntentID("accept-race")
	o.attemptFeedbackDelivery(ctx, intentID)
	rs := store.Snapshot()
	if rs.Stages[feedbackIntentTestStageID].Status != state.StatusDone {
		t.Fatalf("a late delivery attempt must never reopen a completed stage, got %s", rs.Stages[feedbackIntentTestStageID].Status)
	}
	if _, err := os.Stat(feedbackMdPath(o, feedbackIntentTestStageID)); err == nil {
		t.Fatal("feedback must never be written to a stage that already completed")
	}
}

// TestStaleGenerationModelAnswerAfterNewRevise: a model's (proposal-sourced) feedback, deferred while the
// stage was mid-attempt, must NOT be applied once a genuinely NEW revise/attempt has already run in the
// meantime — the generation check at delivery time, not just at accept time, is what catches this.
func TestStaleGenerationModelAnswerAfterNewRevise(t *testing.T) {
	o, store := newFeedbackIntentTestOrchestrator(t)
	ctx := context.Background()
	activateStage(t, o, feedbackIntentTestStageID)
	g1 := store.StageGeneration(feedbackIntentTestStageID)

	// The stage goes into a revise cycle (e.g. human-initiated) WHILE the model's proposal is still pending
	// acceptance — accept it now, deferred against g1.
	o.Trigger(feedbackIntentTestStageID, bus.EvRevise, bus.GuardCtx{}, "human note")
	proposalID := seedAcceptedFeedbackProposal(ctx, t, o, feedbackIntentTestStageID, "model's stale advice", g1)
	if err := o.AcceptProposal(ctx, proposalID, "accept-stale-gen", state.Actor{Kind: "user"}); err != nil {
		t.Fatalf("AcceptProposal: %v", err)
	}
	if p, _ := o.Proposal(proposalID); p.Status != assistants.ProposalAccepted {
		t.Fatal("expected deferred delivery while revising")
	}

	// A NEW revise attempt actually completes (the resumed agent restarts: Revising -> Running is a genuinely
	// new generation).
	if _, ok := o.Trigger(feedbackIntentTestStageID, bus.EvStartRun, bus.GuardCtx{}, ""); !ok {
		t.Fatal("EvStartRun must apply from revising")
	}
	g2 := store.StageGeneration(feedbackIntentTestStageID)
	if g2 == g1 {
		t.Fatal("test setup: expected a new generation after the revise cycle completed")
	}

	waitForProposalStatus(t, o, proposalID, assistants.ProposalDeliveryFailed, time.Second)
	body, err := os.ReadFile(feedbackMdPath(o, feedbackIntentTestStageID))
	if err == nil && strings.Contains(string(body), "model's stale advice") {
		t.Fatalf("the stale model answer must never reach the NEW attempt: %s", body)
	}
}

// TestFeedbackNotAppendedTwice: re-running the delivery dispatcher for an intent that has ALREADY been fully
// delivered (e.g. a redundant recovery sweep, or two overlapping triggers) must never append the feedback a
// second time.
func TestFeedbackNotAppendedTwice(t *testing.T) {
	o, store := newFeedbackIntentTestOrchestrator(t)
	ctx := context.Background()
	activateStage(t, o, feedbackIntentTestStageID)
	gen := store.StageGeneration(feedbackIntentTestStageID)
	proposalID := seedAcceptedFeedbackProposal(ctx, t, o, feedbackIntentTestStageID, "dedupe me", gen)
	if err := o.AcceptProposal(ctx, proposalID, "accept-nodupe", state.Actor{Kind: "user"}); err != nil {
		t.Fatalf("AcceptProposal: %v", err)
	}
	waitForProposalStatus(t, o, proposalID, assistants.ProposalDelivered, time.Second)

	// Re-run the dispatcher directly (simulating recoverFeedbackIntents or a redundant hook firing again) for
	// the ALREADY-delivered intent.
	intentID := deriveFeedbackIntentID("accept-nodupe")
	for i := 0; i < 3; i++ {
		o.attemptFeedbackDelivery(ctx, intentID)
	}

	body, err := os.ReadFile(feedbackMdPath(o, feedbackIntentTestStageID))
	if err != nil {
		t.Fatalf("read feedback.md: %v", err)
	}
	if n := countOccurrences(string(body), "dedupe me"); n != 1 {
		t.Fatalf("expected exactly one delivery after %d redundant dispatcher runs, found %d: %s", 3, n, body)
	}
}
