package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// ---- a round's side effects belong to the round ----

// A finisher delayed after its decision, while a Pause and Continue started the correction, leaves that correction
// alone when its own turn gets the stage: the session the continued author wrote stays, and the stage is corrected once.
func TestPreCompletion_StaleFinisherKeepsTheContinuedCorrection(t *testing.T) {
	for _, fresh := range []bool{true, false} {
		t.Run(fmt.Sprintf("fresh_correction=%v", fresh), func(t *testing.T) {
			started, hold := make(chan struct{}), make(chan struct{})
			claims := make(chan bool, 8)
			var once sync.Once
			r := newPCRig(t, pcRigOptions{freshCorrection: &fresh, holdHelper: true, holdHelperFrom: 2, answers: []pcAnswer{pcFixIt, pcPass}, beforeRun: func(r *pcRig) {
				r.author.onTurn = func(n int, logFile string) {
					session := stagefiles.SessionFile(filepath.Dir(logFile), phaseAutonomous)
					switch n {
					case 1:
						if err := os.WriteFile(session, []byte(`{"session_id":"reviewed"}`), 0o644); err != nil {
							t.Error(err)
						}
					case 2:
						if err := os.WriteFile(session, []byte(`{"session_id":"continued"}`), 0o644); err != nil {
							t.Error(err)
						}
						close(started)
						<-hold
					default:
					}
				}
				r.o.testAfterCorrectionClaim = func(owned bool) { claims <- owned }
				r.o.testAfterRoundDecided = func() {
					once.Do(func() {
						if err := r.o.Pause(context.Background(), pcStageID); err != nil {
							t.Errorf("Pause: %v", err)
						}
						if err := r.o.Continue(context.Background(), pcStageID); err != nil {
							t.Errorf("Continue: %v", err)
						}
						select {
						case <-started:
						case <-time.After(10 * time.Second):
							t.Error("the continued correction did not start")
						}
					})
				}
			}})
			select {
			case <-started:
			case <-time.After(10 * time.Second):
				t.Fatal("the continued correction did not start")
			}
			close(hold)
			// The delayed finisher's turn gets the stage once the continued author has ended its turn.
			awaitClaim(t, claims, false)
			// The claim is reported before the turn prepares anything: the session is looked at once that turn is over.
			r.awaitStageIdle()
			session := stagefiles.SessionFile(filepath.Join(r.dir, pcStageID), phaseAutonomous)
			if data, err := os.ReadFile(session); err != nil || !strings.Contains(string(data), "continued") {
				t.Fatalf("session after the delayed finisher = %q (err %v), want the continued author's", data, err)
			}
			r.releaseHelpers()
			r.waitStatus(state.StatusDone)
			if got, want := r.order(), "author:1,helper:Reviewer:1,author:2,helper:Reviewer:2,verify"; got != want {
				t.Fatalf("order = %s, want %s", got, want)
			}
			if p := r.stage().PreCompletion; p.Corrections != 1 {
				t.Fatalf("round record = %+v, want one correction", p)
			}
		})
	}
}

// awaitClaim blocks until a correction turn reports the ownership want.
func awaitClaim(t *testing.T, claims <-chan bool, want bool) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case owned := <-claims:
			if owned == want {
				return
			}
		case <-deadline:
			t.Fatalf("no correction turn reported owned=%v", want)
		}
	}
}

// A finisher that loses its decision to a Revise may leave its feedback on disk, but the journal holds no such decision:
// no author of the new cycle reads it, neither the live process's nor the one a restart replays.
func TestPreCompletion_InvalidatedFeedbackReachesNoAuthor(t *testing.T) {
	r := newPCRig(t, pcRigOptions{holdHelper: true, answers: []pcAnswer{pcFixIt, pcPass}, beforeRun: func(r *pcRig) {
		if !r.restarted {
			r.author.blockTurn.Store(2)
		}
	}})
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
	r.waitFor("the helper's needs_changes", func(state.StageState) bool {
		return state.PreCompletionDecision(r.store.Snapshot(), pcStageID, lim).Kind == state.RoundDecisionCorrect
	})
	rs := r.store.Snapshot() // what the finisher read before the Revise took the stage
	decision := state.PreCompletionDecision(rs, pcStageID, lim)
	oldRound := rs.Stages[pcStageID].PreCompletion.RoundSeq
	if applied, _, err := r.o.Revise(context.Background(), pcStageID, "start over"); err != nil || !applied {
		t.Fatalf("Revise: applied=%v err=%v", applied, err)
	}
	r.waitFor("the revised author", func(state.StageState) bool { return r.author.turns.Load() == 2 })
	unchanged := func(cur *state.RunState) bool {
		p := cur.Stages[pcStageID].PreCompletion
		return p.Open() && p.RoundSeq == oldRound && state.PreCompletionDecision(*cur, pcStageID, lim).Kind == decision.Kind
	}
	_, committed := r.o.finishRound(s, rs, decision, unchanged)
	r.o.roundMu.Unlock()
	locked = false
	if committed {
		t.Fatal("the invalidated decision was committed")
	}
	stageDir := filepath.Join(r.dir, pcStageID)
	if data, err := os.ReadFile(stagefiles.PreCompletionFeedbackPath(stageDir)); err != nil || !strings.Contains(string(data), pcFixIt.suggested) {
		t.Fatalf("no feedback of the lost decision on disk (err %v): the race did not happen", err)
	}
	if block := r.o.machineFeedbackBlock(pcStageID); strings.Contains(block, pcFixIt.suggested) {
		t.Fatalf("the prompt block carries feedback of a round the journal never decided:\n%s", block)
	}

	r.restarted = true
	r.restart()
	r.waitStatus(state.StatusDone)
	if got := r.author.turns.Load(); got != 3 {
		t.Fatalf("author turns = %d, want the replayed author once more", got)
	}
	if got := r.author.prompt(2); strings.Contains(got, pcFixIt.suggested) {
		t.Fatalf("the replayed author read feedback of a round the journal never decided:\n%s", got)
	}
	if p := r.stage().PreCompletion; p.Cycle != 1 || p.Corrections != 0 || p.Decision != state.PreCompletionAccepted {
		t.Fatalf("round record = %+v, want the new cycle accepted without a correction", p)
	}
}

// A Pause and Continue right after a round opened let its helper decide it before the original start goes on: the
// remarks still reach the correction, and the round's start is announced before its decision.
func TestPreCompletion_RoundDecidedBeforeItsStartWentOn(t *testing.T) {
	decided := make(chan struct{}, 4)
	var once sync.Once
	r := newPCRig(t, pcRigOptions{answers: []pcAnswer{pcFixIt, pcPass}, beforeRun: func(r *pcRig) {
		r.o.testAfterRoundDecided = func() { decided <- struct{}{} }
		r.o.testAfterRoundOpened = func() {
			once.Do(func() {
				if err := r.o.Pause(context.Background(), pcStageID); err != nil {
					t.Errorf("Pause: %v", err)
					return
				}
				if err := r.o.Continue(context.Background(), pcStageID); err != nil {
					t.Errorf("Continue: %v", err)
					return
				}
				select {
				case <-decided:
				case <-time.After(10 * time.Second):
					t.Error("the helper did not decide the round before its start went on")
				}
			})
		}
	}})
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,author:2,helper:Reviewer:2,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	if prompt := r.author.prompt(1); !strings.Contains(prompt, pcFixIt.suggested) {
		t.Fatalf("the correction ran without the committed remarks:\n%s", prompt)
	}
	var got []string
	for _, n := range pcNotices(t, r.dir) {
		got = append(got, fmt.Sprintf("%s:%v", n["type"], n["data"].(map[string]any)["round"]))
	}
	want := []string{"pre_completion_round_started:1", "pre_completion_decided:1", "pre_completion_round_started:2", "pre_completion_decided:2"}
	if !slices.Equal(got, want) {
		t.Fatalf("notices = %v, want %v", got, want)
	}
}

// ---- late helper results across control operations ----

// holdCommit makes the helper's nth commit attempt wait at the commit point until the test releases it: the answer is
// decoded and its turn's files are written, the commit is not made yet.
func holdCommit(t *testing.T, o *Orchestrator, nth int32) (atCommit, release chan struct{}) {
	atCommit, release = make(chan struct{}), make(chan struct{})
	var commits atomic.Int32
	o.helperOutputCommitHook = func() {
		if commits.Add(1) == nth {
			close(atCommit)
			awaitRelease(t, release)
		}
	}
	return atCommit, release
}

func awaitCommitPoint(t *testing.T, atCommit <-chan struct{}) {
	t.Helper()
	select {
	case <-atCommit:
	case <-time.After(10 * time.Second):
		t.Fatal("the helper's answer did not reach its commit")
	}
}

// resultsFor is every result the task's journal binds to request.
func resultsFor(t *testing.T, events []state.TaskEvent, request agentcomm.MessageID) []state.TaskResultPayload {
	t.Helper()
	var out []state.TaskResultPayload
	for _, ev := range events {
		if ev.Kind != taskResultEffectKind {
			continue
		}
		var p state.TaskResultPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.RequestID == string(request) {
			out = append(out, p)
		}
	}
	return out
}

// A helper cancelled after it answered — the answer decoded, the commit not made — answers nothing: the commit is
// refused, the latest result keeps the previous round's bytes, live and replayed state agree that the round waits for a
// human, and the waiver then continues the stage once.
func TestPreCompletion_CancelledTurnLateResultIsRefused(t *testing.T) {
	var atCommit, release chan struct{}
	r := newPCRig(t, pcRigOptions{answers: []pcAnswer{pcFixIt, pcPass}, beforeRun: func(r *pcRig) {
		atCommit, release = holdCommit(t, r.o, 2) // the second round's answer
	}})
	awaitCommitPoint(t, atCommit)
	helper := r.store.Snapshot().Assistants.ByRootStage[pcStageID][0]
	latest := filepath.Join(r.o.taskDir(helper), "result.json")
	before, err := os.ReadFile(latest)
	if err != nil {
		t.Fatalf("the first round's result: %v", err)
	}
	if err := r.o.CancelTask(context.Background(), helper); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	close(release)
	r.awaitTaskIdle(helper)

	r.waitFor("a blocked round", func(st state.StageState) bool {
		rs := r.store.Snapshot()
		return st.Status == state.StatusAwaitingPreCompletion && state.PreCompletionNeedsHuman(&rs, pcStageID)
	})
	if after, err := os.ReadFile(latest); err != nil || string(after) != string(before) {
		t.Fatalf("latest result after the refused commit = %q (err %v), want the first round's %q", after, err, before)
	}
	p := r.stage().PreCompletion
	request := state.PreCompletionRequestID(p.RoundSeq)
	if got := resultsFor(t, r.store.TaskEvents(helper), request); len(got) != 0 {
		t.Fatalf("results bound to round %d = %+v, want none: the cancelled turn answers nothing", p.Round, got)
	}
	replayed, err := state.LoadRunState(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	for name, rs := range map[string]state.RunState{"live": r.store.Snapshot(), "replayed": replayed} {
		if got := rs.Stages[pcStageID].PreCompletion; got.RoundSeq != p.RoundSeq || got.Decision != "" {
			t.Fatalf("%s round record = %+v, want round %d open", name, got, p.Round)
		}
		if _, outcome := state.PreCompletionTaskOutcome(rs, rs.Assistants.Tasks[helper]); outcome != state.RoundFailed {
			t.Fatalf("%s outcome = %q, want failed", name, outcome)
		}
	}
	if strings.Contains(r.order(), "verify") || r.author.turns.Load() != 2 {
		t.Fatalf("order = %s: a blocked round starts neither verify nor the author", r.order())
	}

	if err := r.o.WaiveTask(context.Background(), helper, state.Actor{Kind: "user"}, "cancelled on purpose"); err != nil {
		t.Fatalf("WaiveTask: %v", err)
	}
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,author:2,helper:Reviewer:2,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

// A helper's answer held at its commit while the stage fails and is retried answers at most its own old request: the
// new cycle's round is answered by the helper's next turn, the old remarks reach no author, and the stage goes on once.
func TestPreCompletion_LateResultAcrossStageRetryAnswersOnlyItsRequest(t *testing.T) {
	old := pcAnswer{verdict: assistants.VerdictNeedsChanges, summary: "stale", suggested: "a remark of the failed attempt"}
	var atCommit, release chan struct{}
	r := newPCRig(t, pcRigOptions{keepAlive: true, answers: []pcAnswer{old, pcPass}, beforeRun: func(r *pcRig) {
		atCommit, release = holdCommit(t, r.o, 1)
	}})
	awaitCommitPoint(t, atCommit)
	helper := r.store.Snapshot().Assistants.ByRootStage[pcStageID][0]
	oldRequest := state.PreCompletionRequestID(r.stage().PreCompletion.RoundSeq)
	if _, ok := r.o.Trigger(pcStageID, bus.EvFail, bus.GuardCtx{}, "failed elsewhere"); !ok {
		t.Fatal("the stage did not fail")
	}
	if err := r.o.Retry(context.Background(), pcStageID); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	r.waitFor("the new cycle's round", func(st state.StageState) bool {
		return st.Status == state.StatusAwaitingPreCompletion && st.PreCompletion.Cycle == 1 && st.PreCompletion.Round == 1
	})
	newRequest := state.PreCompletionRequestID(r.stage().PreCompletion.RoundSeq)
	close(release)
	r.waitStatus(state.StatusDone)
	r.awaitTaskIdle(helper)

	if got, want := r.order(), "author:1,helper:Reviewer:1,author:2,helper:Reviewer:2,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	for i := range int(r.author.turns.Load()) {
		if strings.Contains(r.author.prompt(i), old.suggested) {
			t.Fatalf("author turn %d read the failed attempt's remark:\n%s", i+1, r.author.prompt(i))
		}
	}
	events := r.store.TaskEvents(helper)
	if got := resultsFor(t, events, newRequest); len(got) != 1 || got[0].Verdict != assistants.VerdictPass || got[0].Turn != 2 {
		t.Fatalf("results bound to the new round = %+v, want the one pass of the helper's second turn", got)
	}
	for _, res := range resultsFor(t, events, oldRequest) {
		if res.Turn != 1 {
			t.Fatalf("result bound to the old round = %+v, want only the old turn's own", res)
		}
	}
	replayed, err := state.LoadRunState(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	live := r.store.Snapshot()
	for name, rs := range map[string]state.RunState{"live": live, "replayed": replayed} {
		p := rs.Stages[pcStageID].PreCompletion
		if p.Cycle != 1 || p.Round != 1 || p.Corrections != 0 || p.Decision != state.PreCompletionAccepted || state.PreCompletionRequestID(p.RoundSeq) != newRequest {
			t.Fatalf("%s round record = %+v, want the new cycle's round accepted without a correction", name, p)
		}
		if _, outcome := state.PreCompletionTaskOutcome(rs, rs.Assistants.Tasks[helper]); outcome != state.RoundPass {
			t.Fatalf("%s outcome of the new round = %q, want pass", name, outcome)
		}
	}
}

// ---- the correction keeps its preparation beside a left question ----

// The reviewed author left a question behind and the round decided needs_changes. A restarted poller does not ask it
// before the correction's turn claims the stage, and one asked in that epoch anyway does not take the correction from the
// turn: it is prepared either way (resume context armed, session dropped iff fresh_correction). Last: no question.
func TestPreCompletion_CorrectionPreparedBesideALeftQuestion(t *testing.T) {
	const (
		polled = "poll"
		asked  = "ask"
	)
	cases := []struct {
		name  string
		fresh bool
		left  string
	}{
		{"fresh_correction=true/poll", true, polled},
		{"fresh_correction=false/poll", false, polled},
		{"fresh_correction=true/asked", true, asked},
		{"fresh_correction=false/asked", false, asked},
		{"fresh_correction=true/no-question", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fresh := tc.fresh
			stage := flow.Stage{ID: pcAdmitStage, Name: "Interactive", Interactive: true, Agents: []flow.AgentType{flow.AgentAuto}}
			stage.Verify.FreshCorrection = &fresh
			o, store := newAssistantsTestOrchestrator(t, true, stage)
			pcStartStage(t, o, stage.ID)
			if err := pcAttach(o, stage.ID, "op-helper"); err != nil {
				t.Fatal(err)
			}
			pcOpenRound(t, o, stage.ID)
			step := state.PreCompletionStep{Phase: phaseAutonomous, Decision: state.PreCompletionNeedsChanges, Attempt: 1}
			epoch, ok := o.grantTurn(stage.ID, bus.EvPreCompletionDecided, bus.GuardCtx{PreCompletion: &step}, "needs_changes")
			if !ok {
				t.Fatal("decision refused")
			}
			round := *store.Snapshot().Stages[stage.ID].PreCompletion
			stageDir := filepath.Join(o.opts.RunDir, stage.ID)
			if err := os.MkdirAll(stageDir, 0o755); err != nil {
				t.Fatal(err)
			}
			session := stagefiles.SessionFile(stageDir, phaseAutonomous)
			if err := os.WriteFile(session, []byte(`{"session_id":"reviewed"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.left != "" {
				writeQuestionFile(t, stageDir, phaseAutonomous, "left behind by the reviewed author", nil)
			}
			want := state.StatusRunning
			switch tc.left {
			case polled:
				o.pollQuestions(map[string]bool{}, map[string]*malformedQuestionState{}) // a restarted poller knows no processed question
			case asked:
				if _, ok := o.Trigger(stage.ID, bus.EvAskUser, bus.GuardCtx{Phase: phaseAutonomous}, ""); !ok {
					t.Fatal("ask_user refused")
				}
				want = state.StatusAwaitingUserInput
			default: // no question: the control
			}
			if st := store.Snapshot().Stages[stage.ID]; st.Status != want || st.Epoch != epoch || st.PreCompletion.RoundSeq != round.RoundSeq {
				t.Fatalf("before the correction: status=%s epoch=%d round=%+v; want %s in epoch %d of the same round", st.Status, st.Epoch, st.PreCompletion, want, epoch)
			}
			type prepared struct {
				owned, armed, sessionKept bool
			}
			claims, result := make(chan bool, 1), make(chan prepared, 1)
			o.testAfterCorrectionClaim = func(owned bool) { claims <- owned }
			run := o.correctionRunner(round, func(_ context.Context, s flow.Stage) {
				_, armed := o.resumeContextOnce.LoadAndDelete(s.ID)
				result <- prepared{owned: <-claims, armed: armed, sessionKept: stagefiles.SessionExists(stageDir, phaseAutonomous)}
			})
			o.spawnAgentLeased(withTurnEpoch(o.runContext(context.Background()), epoch), stage, run)
			var got prepared
			select {
			case got = <-result:
			case <-time.After(10 * time.Second):
				t.Fatal("the correction's turn did not run")
			}
			o.concurrency.WaitAgents()
			if want := (prepared{owned: true, armed: true, sessionKept: !tc.fresh}); got != want {
				t.Fatalf("correction turn prepared = %+v, want %+v", got, want)
			}
		})
	}
}
