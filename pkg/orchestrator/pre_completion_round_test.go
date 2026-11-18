package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

func pcLateAttach(r *pcRig, opID string) error {
	_, err := r.o.Attach(context.Background(), AttachRequest{
		OperationID: opID, StageID: pcStageID, ProfileID: "late", Instruction: "review", Mode: assistants.ModeRequired,
		Trigger: assistants.TriggerPreCompletion, Actor: state.Actor{Kind: "user"},
	})
	return err
}

func pcEvents(t *testing.T, r *pcRig) []state.Transition {
	t.Helper()
	history, err := r.store.History()
	if err != nil {
		t.Fatal(err)
	}
	var out []state.Transition
	for _, tr := range history {
		if tr.StageID == pcStageID {
			out = append(out, tr)
		}
	}
	return out
}

// ---- the gate fails closed ----

// An author that left an abandoned question behind is complete: the gate records the empty acceptance without
// leaving awaiting_user_input, and verify runs only under it — a helper attached meanwhile is refused.
func TestPreCompletion_AbandonedQuestionRecordsEmptyAcceptance(t *testing.T) {
	var refused bool
	r := newPCRig(t, pcRigOptions{noHelper: true, beforeRun: func(r *pcRig) {
		r.author.onTurn = func(n int, logFile string) {
			stageDir := filepath.Dir(logFile)
			if err := os.WriteFile(filepath.Join(stageDir, "autonomous_execution.q1.question.json"), []byte(`{"id":"q1","question":"left behind"}`), 0o644); err != nil {
				t.Error(err)
			}
			if _, ok := r.o.Trigger(pcStageID, bus.EvAskUser, bus.GuardCtx{Phase: phaseAutonomous}, ""); !ok {
				t.Error("ask_user refused")
			}
		}
		r.o.runVerifyShell = func(context.Context, string, string, string) (string, error) {
			r.log.add("verify")
			var rej *state.OperationRejectedError
			refused = errors.As(pcLateAttach(r, "op-late"), &rej) && rej.Code == assistants.CodeInvalidStatus
			return "", nil
		}
	}})
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	if !refused {
		t.Fatal("a helper attached during verify must be refused: the acceptance was recorded")
	}
	events := pcEvents(t, r)
	i := slices.IndexFunc(events, func(tr state.Transition) bool { return tr.Event == "pre_completion_accept_empty" })
	if i < 0 {
		t.Fatalf("no empty acceptance in the journal: %v", events)
	}
	if tr := events[i]; tr.From != state.StatusAwaitingUserInput || tr.To != state.StatusAwaitingUserInput {
		t.Fatalf("empty acceptance moved the stage %s -> %s, want it to stay in awaiting_user_input", tr.From, tr.To)
	}
}

// An author that left an abandoned question behind with a pre_completion helper attached goes through the round: the
// stage parks from awaiting_user_input, the helper reviews, and verify starts only after the acceptance.
func TestPreCompletion_AbandonedQuestionWithHelperGoesThroughTheRound(t *testing.T) {
	r := newPCRig(t, pcRigOptions{holdHelper: true, answers: []pcAnswer{pcPass}, beforeRun: func(r *pcRig) {
		r.author.onTurn = func(_ int, logFile string) {
			stageDir := filepath.Dir(logFile)
			if err := os.WriteFile(filepath.Join(stageDir, "autonomous_execution.q1.question.json"), []byte(`{"id":"q1","question":"left behind"}`), 0o644); err != nil {
				t.Error(err)
			}
			if _, ok := r.o.Trigger(pcStageID, bus.EvAskUser, bus.GuardCtx{Phase: phaseAutonomous}, ""); !ok {
				t.Error("ask_user refused")
			}
		}
	}})
	r.waitFor("the helper's turn", func(state.StageState) bool { return r.calls.Load() == 1 })
	if st := r.stage(); st.Status != state.StatusAwaitingPreCompletion || strings.Contains(r.order(), "verify") {
		t.Fatalf("status %s, order %s: the round must hold the stage and verify must wait for its acceptance", st.Status, r.order())
	}
	r.releaseHelpers()
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	events := pcEvents(t, r)
	if slices.ContainsFunc(events, func(tr state.Transition) bool { return tr.Event == "pre_completion_accept_empty" }) {
		t.Fatalf("an empty acceptance skipped the helper: %v", events)
	}
	i := slices.IndexFunc(events, func(tr state.Transition) bool { return tr.Event == "await_pre_completion" })
	if i < 0 || events[i].From != state.StatusAwaitingUserInput {
		t.Fatalf("events = %v, want the round to start from awaiting_user_input", events)
	}
	if p := r.stage().PreCompletion; p.Round != 1 || p.Decision != state.PreCompletionAccepted {
		t.Fatalf("round record = %+v, want the one round accepted", p)
	}
}

// The question the reviewed author left behind, found again by a restarted process's poller while the accepted round's
// verify runs, is not asked: the acceptance stays, a helper attached now is refused, and the stage completes with the one
// helper that reviewed it.
func TestPreCompletion_RestartedPollerKeepsTheAcceptedRound(t *testing.T) {
	firstVerify, replayedVerify, releaseVerify := make(chan struct{}), make(chan struct{}), make(chan struct{})
	// The fake clock keeps the background poller still: the test runs the scan itself.
	clk := newPCWakeClock(time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC))
	r := newPCRig(t, pcRigOptions{clock: clk, answers: []pcAnswer{pcPass}, beforeRun: func(r *pcRig) {
		resumed := r.restarted
		if !resumed {
			r.author.onTurn = func(_ int, logFile string) {
				if err := os.WriteFile(filepath.Join(filepath.Dir(logFile), "autonomous_execution.q1.question.json"), []byte(`{"id":"q1","question":"left behind"}`), 0o644); err != nil {
					t.Error(err)
				}
				if _, ok := r.o.Trigger(pcStageID, bus.EvAskUser, bus.GuardCtx{Phase: phaseAutonomous}, ""); !ok {
					t.Error("ask_user refused")
				}
			}
		}
		r.o.runVerifyShell = func(ctx context.Context, _, _, _ string) (string, error) {
			r.log.add("verify")
			if !resumed {
				close(firstVerify)
				<-ctx.Done()
				return "", ctx.Err()
			}
			close(replayedVerify)
			select {
			case <-releaseVerify:
				return "", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}})
	awaitSignal(t, firstVerify, "the accepted round's verify")
	r.restarted, r.opts.interactive = true, true
	r.restart()
	awaitSignal(t, replayedVerify, "the replayed verify")
	before := r.stage()
	if !questionsSettledByRound(before) {
		t.Fatalf("round record = %+v, want the replayed round accepted", before.PreCompletion)
	}

	sub, events := r.o.ui.Subscribe(32)
	defer r.o.ui.Unsubscribe(sub)
	r.o.pollQuestions(map[string]bool{}, map[string]*malformedQuestionState{})
	if st := r.stage(); st.Status != state.StatusRunning || !st.PreCompletion.Accepted || st.PreCompletion.RoundSeq != before.PreCompletion.RoundSeq {
		t.Fatalf("after the scan: status %s, round %+v; want the accepted round to keep the stage", st.Status, st.PreCompletion)
	}
	for _, ev := range drainAllEvents(events) {
		if ev.Type == bus.EventAskUser || ev.Type == bus.EventDialogQuestion {
			t.Fatalf("the left-behind question was published again: %s", ev.Type)
		}
	}
	var rej *state.OperationRejectedError
	if err := pcLateAttach(r, "op-late"); !errors.As(err, &rej) || rej.Code != assistants.CodeInvalidStatus {
		t.Fatalf("Attach during verify = %v, want invalid_status", err)
	}

	close(releaseVerify)
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,verify,verify"; got != want {
		t.Fatalf("order = %s, want %s: neither the author nor a helper runs again", got, want)
	}
	if tasks := r.store.Snapshot().Assistants.ByRootStage[pcStageID]; len(tasks) != 1 {
		t.Fatalf("helper tasks = %v, want only the one that reviewed", tasks)
	}
}

// A gate whose empty acceptance cannot be committed ends the turn's check with an error: verify never starts on an
// acceptance the journal does not hold.
func TestPreCompletion_GateFailsClosedWhenAcceptanceNotRecorded(t *testing.T) {
	r := newPCRig(t, pcRigOptions{noHelper: true, hold: true})
	r.waitFor("the author's turn", func(st state.StageState) bool { return st.Status == state.StatusRunning && r.author.turns.Load() == 1 })
	if err := r.o.Pause(context.Background(), pcStageID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	r.waitStatus(state.StatusPaused)
	err := r.o.preCompletionGate(*r.o.reg().Stage(pcStageID), phaseAutonomous)
	if !errors.Is(err, errEmptyAcceptanceNotRecorded) {
		t.Fatalf("gate = %v, want errEmptyAcceptanceNotRecorded", err)
	}
	if p := r.stage().PreCompletion; p != nil && p.Accepted {
		t.Fatalf("round record = %+v, want no acceptance", p)
	}
	if strings.Contains(r.order(), "verify") {
		t.Fatalf("order = %s, verify must not run", r.order())
	}
}

// ---- a restart with collaboration off ----

// A helper that was attached while collaboration was on still reviews after a restart that turns it off: the stage
// does not complete by its files past a required helper that never ran.
func TestPreCompletion_RestartWithoutCollaborationKeepsRequiredHelper(t *testing.T) {
	r := newPCRig(t, pcRigOptions{noVerify: true, answers: []pcAnswer{pcPass}, beforeRun: func(r *pcRig) {
		if !r.restarted {
			r.author.blockAfterWriting.Store(1)
		}
	}})
	r.awaitSummary()
	r.restarted = true
	r.opts.noAssistants = true
	r.restart()
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1"; got != want {
		t.Fatalf("order = %s, want %s: the helper reviews", got, want)
	}
	if r.author.turns.Load() != 1 {
		t.Fatalf("author turns = %d, want the restart not to run the author again", r.author.turns.Load())
	}
}

// ---- finishing a round ----

// Finishers that race for one round, and a late one that comes while the next round is open, write one record and
// start one correction: the next round's files are not replaced and the stage does not fail over a file.
func TestPreCompletion_ConcurrentFinishersAcrossTwoRounds(t *testing.T) {
	r := newPCRig(t, pcRigOptions{holdHelper: true, holdHelperFrom: 2, answers: []pcAnswer{pcFixIt, pcPass}})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 8 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					r.o.maybeDecideRound(context.Background(), pcStageID)
				}
			}
		})
	}
	r.waitFor("round 2 under way", func(st state.StageState) bool {
		return r.calls.Load() == 2 && st.Status == state.StatusAwaitingPreCompletion && st.PreCompletion.Round == 2
	})
	stageDir := filepath.Join(r.dir, pcStageID)
	if _, err := os.Stat(stagefiles.PreCompletionFeedbackPath(stageDir)); err == nil {
		t.Fatal("a late finisher of round 1 left its feedback as the active one of round 2")
	}
	if _, err := os.Stat(stagefiles.PreCompletionReportPath(stageDir, r.stage().PreCompletion.RoundSeq)); err == nil {
		t.Fatal("a finisher wrote the report of a round that is still open")
	}
	r.releaseHelpers()
	r.waitStatus(state.StatusDone)
	close(stop)
	wg.Wait()
	if got, want := r.order(), "author:1,helper:Reviewer:1,author:2,helper:Reviewer:2,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	for _, tr := range pcEvents(t, r) {
		if tr.Event == "fail" {
			t.Fatalf("the stage failed: %+v", tr)
		}
	}
	if p := r.stage().PreCompletion; p.Corrections != 1 {
		t.Fatalf("corrections = %d, want 1", p.Corrections)
	}
}

// ---- the continuation owns the decision's epoch ----

// A Pause and Continue between the committed decision and the spawn start the turn once: the spawn that decided
// before them finds the stage taken over.
func TestPreCompletion_PauseContinueBetweenDecisionAndSpawn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []pcAnswer
		want    string
	}{
		{"correction", []pcAnswer{pcFixIt, pcPass}, "author:1,helper:Reviewer:1,author:2,helper:Reviewer:2,verify"},
		{"accepted", []pcAnswer{pcPass}, "author:1,helper:Reviewer:1,verify"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var once sync.Once
			r := newPCRig(t, pcRigOptions{answers: tc.answers, beforeRun: func(r *pcRig) {
				r.o.testAfterRoundDecided = func() {
					once.Do(func() {
						if err := r.o.Pause(context.Background(), pcStageID); err != nil {
							t.Errorf("Pause: %v", err)
						}
						if err := r.o.Continue(context.Background(), pcStageID); err != nil {
							t.Errorf("Continue: %v", err)
						}
					})
				}
			}})
			r.waitStatus(state.StatusDone)
			if got := r.order(); got != tc.want {
				t.Fatalf("order = %s, want %s", got, tc.want)
			}
		})
	}
}

// ---- a crash between the decision and the author's start ----

// The decision is fsynced and the process dies before the correction starts: the replay starts it in the session the
// policy picks, and the author still reads what the helpers said.
func TestPreCompletion_CrashAfterDecisionReplaysFreshCorrection(t *testing.T) {
	fresh := true
	captured := t.TempDir()
	var once sync.Once
	first := newPCRig(t, pcRigOptions{freshCorrection: &fresh, answers: []pcAnswer{pcFixIt, pcPass}, beforeRun: func(r *pcRig) {
		r.author.onTurn = func(n int, logFile string) {
			if n == 1 {
				_ = os.WriteFile(stagefiles.SessionFile(filepath.Dir(logFile), phaseAutonomous), []byte(`{"session_id":"s-1"}`), 0o644)
			}
		}
		r.o.testAfterRoundDecided = func() {
			once.Do(func() {
				if err := os.CopyFS(captured, os.DirFS(r.dir)); err != nil {
					t.Errorf("capture the journal: %v", err)
				}
			})
		}
	}})
	first.waitStatus(state.StatusDone)
	first.stop()

	var sessionAtStart bool
	second := newPCRig(t, pcRigOptions{dir: captured, freshCorrection: &fresh, answers: []pcAnswer{pcPass}, beforeRun: func(r *pcRig) {
		r.author.onTurn = func(_ int, logFile string) {
			_, err := os.Stat(stagefiles.SessionFile(filepath.Dir(logFile), phaseAutonomous))
			sessionAtStart = err == nil
		}
	}})
	second.waitStatus(state.StatusDone)
	if sessionAtStart {
		t.Fatal("the replayed correction resumed the dropped session")
	}
	if got := second.author.prompt(0); !strings.Contains(got, pcFixIt.suggested) {
		t.Fatalf("the replayed correction lost the helper's remarks:\n%s", got)
	}
	if got := second.author.turns.Load(); got != 1 {
		t.Fatalf("author turns after the replay = %d, want the one correction", got)
	}
}

// ---- helper requests across Retry and CancelTask ----

// A helper cancelled while its turn is in flight answers nothing: its late result is no answer to the round, which
// waits for a human and starts neither verify nor the author; the human's waiver then continues the stage once.
func TestPreCompletion_CancelledInFlightHelperDoesNotAnswerTheRound(t *testing.T) {
	r := newPCRig(t, pcRigOptions{holdHelper: true, answers: []pcAnswer{pcPass}})
	r.waitFor("the helper's turn", func(state.StageState) bool { return r.calls.Load() == 1 })
	helper := r.store.Snapshot().Assistants.ByRootStage[pcStageID][0]
	if err := r.o.CancelTask(context.Background(), helper); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	r.releaseHelpers()
	r.waitFor("a blocked round", func(st state.StageState) bool {
		rs := r.store.Snapshot()
		return st.Status == state.StatusAwaitingPreCompletion && state.PreCompletionNeedsHuman(&rs, pcStageID)
	})
	rs := r.store.Snapshot()
	task := rs.Assistants.Tasks[helper]
	if _, outcome := state.PreCompletionTaskOutcome(rs, task); outcome != state.RoundFailed {
		t.Fatalf("outcome = %q, want failed: a cancelled helper's late result must not pass the round", outcome)
	}
	if strings.Contains(r.order(), "verify") || r.author.turns.Load() != 1 {
		t.Fatalf("order = %s: a blocked round starts neither verify nor the author", r.order())
	}
	if err := r.o.WaiveTask(context.Background(), helper, state.Actor{Kind: "user"}, "cancelled on purpose"); err != nil {
		t.Fatalf("WaiveTask: %v", err)
	}
	r.waitStatus(state.StatusDone)
	if got, want := strings.Count(r.order(), "verify"), 1; got != want {
		t.Fatalf("verify ran %d times in %s, want %d", got, r.order(), want)
	}
}

// A helper that failed to start and was retried answers the same round's request: the answer is bound to it and the stage goes on once.
func TestPreCompletion_RetriedHelperAnswersTheRoundRequest(t *testing.T) {
	r := newPCRig(t, pcRigOptions{answers: []pcAnswer{{err: executor.ErrAgentStart}, pcPass}})
	r.waitFor("a blocked round", func(st state.StageState) bool {
		rs := r.store.Snapshot()
		return st.Status == state.StatusAwaitingPreCompletion && state.PreCompletionNeedsHuman(&rs, pcStageID)
	})
	helper := r.store.Snapshot().Assistants.ByRootStage[pcStageID][0]
	if err := r.o.RetryTask(context.Background(), helper); err != nil {
		t.Fatalf("RetryTask: %v", err)
	}
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,helper:Reviewer:2,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	p := r.stage().PreCompletion
	if _, _, ok := roundResultOf(r.store.TaskEvents(helper), state.PreCompletionRequestID(p.RoundSeq)); !ok {
		t.Fatalf("no result of the helper is bound to the request of round %d", p.Round)
	}
}

// ---- a helper held by PauseTask ----

// pcTaskByTitle is the stage's helper with the given title.
func pcTaskByTitle(t *testing.T, r *pcRig, title string) assistants.TaskID {
	t.Helper()
	rs := r.store.Snapshot()
	for _, id := range rs.Assistants.ByRootStage[pcStageID] {
		if rs.Assistants.Tasks[id].Title == title {
			return id
		}
	}
	t.Fatalf("no helper %q", title)
	return ""
}

// A helper paused while it reviews has not answered the round, whatever its mode: the round waits for a human and
// starts neither verify nor the correction the other helper asked for. Continue lets it answer, and the round goes on.
func TestPreCompletion_PausedHelperHoldsTheRound(t *testing.T) {
	for _, tc := range []struct {
		name       string
		helpers    []pcHelperSpec
		paused     string
		authorRuns int32 // once the stage is done
	}{
		{"paused advisory beside a passing required", []pcHelperSpec{{title: "Reviewer", answers: []pcAnswer{pcPass}}, {title: "Advisor", advisory: true}}, "Advisor", 1},
		{"paused required beside a required needs_changes", []pcHelperSpec{{title: "Reviewer", answers: []pcAnswer{pcFixIt, pcPass}}, {title: "Second"}}, "Second", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPCRig(t, pcRigOptions{holdHelper: true, helpers: tc.helpers})
			r.waitFor("both helpers' turns", func(state.StageState) bool { return r.calls.Load() == 2 })
			paused, other := pcTaskByTitle(t, r, tc.paused), pcTaskByTitle(t, r, "Reviewer")
			if err := r.o.PauseTask(context.Background(), paused); err != nil {
				t.Fatalf("PauseTask: %v", err)
			}
			r.awaitTaskIdle(paused)
			r.releaseHelpers()
			r.waitFor("the other helper's answer", func(state.StageState) bool {
				rs := r.store.Snapshot()
				_, outcome := state.PreCompletionTaskOutcome(rs, rs.Assistants.Tasks[other])
				return outcome != state.RoundPending
			})
			r.awaitTaskIdle(other)
			rs := r.store.Snapshot()
			if st := r.stage(); st.Status != state.StatusAwaitingPreCompletion || !st.PreCompletion.Open() || !state.PreCompletionNeedsHuman(&rs, pcStageID) {
				t.Fatalf("status %s, round %+v: the round must wait for a human while %s is paused", st.Status, st.PreCompletion, tc.paused)
			}
			if strings.Contains(r.order(), "verify") || r.author.turns.Load() != 1 {
				t.Fatalf("order = %s: a held round starts neither verify nor the author", r.order())
			}
			if err := r.o.ContinueTask(context.Background(), paused); err != nil {
				t.Fatalf("ContinueTask: %v", err)
			}
			r.waitStatus(state.StatusDone)
			if got := r.author.turns.Load(); got != tc.authorRuns || strings.Count(r.order(), "verify") != 1 {
				t.Fatalf("author turns %d, order %s; want %d author turns and one verify", got, r.order(), tc.authorRuns)
			}
		})
	}
}

// ---- artifacts ----

// An author that is alive after writing its summary has not finished: nobody reviews until its turn ends.
func TestPreCompletion_SummaryWrittenWhileAuthorAliveStartsNoRound(t *testing.T) {
	hold := make(chan struct{})
	r := newPCRig(t, pcRigOptions{answers: []pcAnswer{pcPass}, beforeRun: func(r *pcRig) { r.author.holdAfterSummary = hold }})
	r.awaitSummary()
	if st := r.stage(); st.PreCompletion != nil || r.calls.Load() != 0 {
		t.Fatalf("round %+v, helper turns %d while the author still works", st.PreCompletion, r.calls.Load())
	}
	close(hold)
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

// A missing or empty summary is the author's to fix first: no round exists before the file does.
func TestPreCompletion_NoRoundUntilTheSummaryIsValid(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first func(int) (string, bool)
	}{
		{"missing", func(n int) (string, bool) { return "## Summary\nturn\n", n > 1 }},
		{"empty", func(n int) (string, bool) { return map[bool]string{true: "## Summary\nturn\n", false: ""}[n > 1], true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPCRig(t, pcRigOptions{answers: []pcAnswer{pcPass}, beforeRun: func(r *pcRig) { r.author.summaryFor = tc.first }})
			r.waitStatus(state.StatusDone)
			order := r.order()
			if !strings.HasPrefix(order, "author:1,author:2,helper:Reviewer:1") {
				t.Fatalf("order = %s, want the summary correction before the first round", order)
			}
			if p := r.stage().PreCompletion; p.Round != 1 {
				t.Fatalf("round record = %+v, want exactly one round", p)
			}
		})
	}
}

// A helper's answer that is no valid result is a protocol error: it never passes the round.
func TestPreCompletion_ProtocolErrorNeverPasses(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"not json", "I looked at it and it is fine."},
		{"unknown verdict", `{"schema_version":1,"kind":"result","verdict":"maybe","summary":"hm"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPCRig(t, pcRigOptions{answers: []pcAnswer{{raw: tc.raw}}})
			r.waitFor("a blocked round", func(st state.StageState) bool {
				rs := r.store.Snapshot()
				return st.Status == state.StatusAwaitingPreCompletion && state.PreCompletionNeedsHuman(&rs, pcStageID)
			})
			if strings.Contains(r.order(), "verify") || r.author.turns.Load() != 1 {
				t.Fatalf("order = %s: a protocol error must start neither verify nor the author", r.order())
			}
		})
	}
}

// ---- budgets ----

// A positive correction budget is spent one round at a time and then ends the stage.
func TestPreCompletion_PositiveBudgetSpentToExhaustion(t *testing.T) {
	two := 2
	r := newPCRig(t, pcRigOptions{maxFailures: &two, answers: []pcAnswer{pcFixIt}})
	r.waitStatus(state.StatusFailed)
	if got, want := r.order(), "author:1,helper:Reviewer:1,author:2,helper:Reviewer:2,author:3,helper:Reviewer:3"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	p := r.stage().PreCompletion
	if p.Corrections != 2 || p.Decision != state.PreCompletionExhausted {
		t.Fatalf("round record = %+v, want two corrections and an exhausted budget", p)
	}
}

// A round that cannot go on loses to a Pause committed while it was being recorded: the stage stays paused with the round
// open, live and in the journal, and Continue decides the round again.
func TestPreCompletion_PauseWinsOverTerminalRoundDecision(t *testing.T) {
	zero := 0
	for _, tc := range []struct {
		name        string
		opts        pcRigOptions
		kind        state.RoundDecisionKind // the decision the finisher takes
		blockReport bool                    // the round's report cannot be written
		want        string                  // the decision Continue ends the stage with
	}{
		{name: "budget exhausted", opts: pcRigOptions{maxFailures: &zero, answers: []pcAnswer{pcFixIt}},
			kind: state.RoundDecisionExhausted, want: state.PreCompletionExhausted},
		{name: "report not written", opts: pcRigOptions{answers: []pcAnswer{pcPass}},
			kind: state.RoundDecisionAccepted, blockReport: true, want: state.PreCompletionFailed},
		{name: "required helper died under exit_on_fail", opts: pcRigOptions{exitOnFail: true, answers: []pcAnswer{{err: executor.ErrAgentStart}}},
			kind: state.RoundDecisionFailed, want: state.PreCompletionFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.holdHelper = true
			r := newPCRig(t, opts)
			r.waitFor("the helper's turn", func(state.StageState) bool { return r.calls.Load() == 1 })
			s := *r.o.reg().Stage(pcStageID)
			lim := r.o.preCompletionLimits(s)

			// The test is the finisher: it holds roundMu, so the helper's own finisher waits behind it.
			r.o.roundMu.Lock()
			locked := true
			defer func() {
				if locked {
					r.o.roundMu.Unlock()
				}
			}()
			r.releaseHelpers()
			r.waitFor("the round's decision", func(state.StageState) bool {
				return state.PreCompletionDecision(r.store.Snapshot(), pcStageID, lim).Kind == tc.kind
			})
			rs := r.store.Snapshot() // what the finisher read while the stage still waited for the round
			decision := state.PreCompletionDecision(rs, pcStageID, lim)
			roundSeq := rs.Stages[pcStageID].PreCompletion.RoundSeq
			if err := r.o.Pause(context.Background(), pcStageID); err != nil {
				t.Fatalf("Pause: %v", err)
			}
			if tc.blockReport {
				roundDir := filepath.Dir(stagefiles.PreCompletionReportPath(filepath.Join(r.dir, pcStageID), roundSeq))
				if err := os.MkdirAll(filepath.Dir(roundDir), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(roundDir, []byte("not a directory"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			unchanged := func(cur *state.RunState) bool {
				p := cur.Stages[pcStageID].PreCompletion
				return p.Open() && p.RoundSeq == roundSeq && state.PreCompletionDecision(*cur, pcStageID, lim).Kind == decision.Kind
			}
			_, committed := r.o.finishRound(s, rs, decision, unchanged)
			r.o.roundMu.Unlock()
			locked = false
			if committed {
				t.Fatal("the round's decision was committed over the Pause")
			}

			replayed, err := state.LoadRunState(r.dir)
			if err != nil {
				t.Fatal(err)
			}
			for where, st := range map[string]state.StageState{"live": r.stage(), "replayed": replayed.Stages[pcStageID]} {
				if st.Status != state.StatusPaused || !st.PreCompletion.Open() || st.PreCompletion.RoundSeq != roundSeq {
					t.Fatalf("%s: status %s, round %+v; want the stage paused with the round open", where, st.Status, st.PreCompletion)
				}
			}
			if err := r.o.Continue(context.Background(), pcStageID); err != nil {
				t.Fatalf("Continue: %v", err)
			}
			r.waitStatus(state.StatusFailed)
			if p := r.stage().PreCompletion; p.Decision != tc.want || p.RoundSeq != roundSeq {
				t.Fatalf("round record = %+v, want the round decided %s", p, tc.want)
			}
			if got, want := r.order(), "author:1,helper:Reviewer:1"; got != want {
				t.Fatalf("order = %s, want %s: neither a correction nor verify", got, want)
			}
		})
	}
}

// The author's overall attempt ceiling bounds the corrections whatever the correction budget says.
func TestPreCompletion_AttemptCeilingEndsTheStage(t *testing.T) {
	many := 10
	r := newPCRig(t, pcRigOptions{maxFailures: &many, answers: []pcAnswer{pcFixIt}, beforeRun: func(r *pcRig) { r.o.maxRetries = 1 }})
	r.waitStatus(state.StatusFailed)
	if got, want := r.order(), "author:1,helper:Reviewer:1,author:2,helper:Reviewer:2"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	history := pcEvents(t, r)
	if last := history[len(history)-1]; !strings.Contains(last.Reason, "retries exhausted") {
		t.Fatalf("failure = %+v, want the attempt ceiling named", last)
	}
}

// ---- verify and waits ----

// A verify rejection repeats the round even when the correction leaves the summary byte for byte as it was.
func TestPreCompletion_VerifyRejectRepeatsRoundWithConstantSummary(t *testing.T) {
	verifies := 0
	r := newPCRig(t, pcRigOptions{constantSummary: true, answers: []pcAnswer{pcPass}, beforeRun: func(r *pcRig) {
		r.o.runVerifyShell = func(context.Context, string, string, string) (string, error) {
			verifies++
			r.log.add("verify")
			if verifies == 1 {
				return "", errors.New("exit status 1")
			}
			return "", nil
		}
	}})
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,verify,author:2,helper:Reviewer:2,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

// A wait that reaches the author through another agent is refused like a direct one: the pre_completion helper at the
// end of the chain starts only after the author's turn ends.
func TestPreCompletion_TransitiveWaitCycleRefused(t *testing.T) {
	o, store := pcAdmissionOrchestrator(t)
	countTurns(t, o, make(chan struct{}))
	pcStartStage(t, o, pcAdmitStage)
	if err := pcAttach(o, pcAdmitStage, "op-helper"); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	middle, err := o.Attach(context.Background(), AttachRequest{
		OperationID: "op-middle", StageID: pcAdmitStage, ProfileID: "reviewer", Instruction: "relay", Mode: assistants.ModeAdvisory,
		Trigger: assistants.TriggerOnMessage, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("Attach (middle): %v", err)
	}
	var reviewer assistants.TaskID
	for _, id := range store.Snapshot().Assistants.ByRootStage[pcAdmitStage] {
		if id != middle {
			reviewer = id
		}
	}
	middleP, author := meshHelperParticipantID(middle), meshStageParticipantID(pcAdmitStage)
	if _, err := o.Wait(context.Background(), WaitRequest{
		OperationID: "op-middle-waits", Waiter: middleP, Actor: helperActor(middleP),
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnTaskResult, TaskID: string(reviewer)},
	}); err != nil {
		t.Fatalf("the middle agent's wait for the reviewer: %v", err)
	}
	_, err = o.Wait(context.Background(), WaitRequest{
		OperationID: "op-author-waits", Waiter: author, Actor: helperActor(author),
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnTaskResult, TaskID: string(middle)},
	})
	if reason := cycleReason(t, err); !strings.Contains(reason, "pre_completion") {
		t.Fatalf("author waits through the middle agent: reason = %q, want the pre_completion start condition named", reason)
	}
}
