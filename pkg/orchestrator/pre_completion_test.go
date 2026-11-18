package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/state"
)

// The author finishes, the helper passes, then verify runs and the stage is done: the order the feature promises.
func TestPreCompletion_PassOrder(t *testing.T) {
	r := newPCRig(t, pcRigOptions{answers: []pcAnswer{pcPass}})
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	p := r.stage().PreCompletion
	if p == nil || p.Round != 1 || p.Corrections != 0 || p.Accepted || p.Decision != state.PreCompletionAccepted {
		t.Fatalf("round record = %+v, want one accepted round, its intent used up", p)
	}
}

// Without verify the accepted round goes straight to completion.
func TestPreCompletion_NoVerifyOrder(t *testing.T) {
	r := newPCRig(t, pcRigOptions{noVerify: true, answers: []pcAnswer{pcPass}})
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

// needs_changes never reaches verify: the author corrects once, a new round reviews the correction, and only the
// accepted work is verified.
func TestPreCompletion_NeedsChangesSkipsVerify(t *testing.T) {
	r := newPCRig(t, pcRigOptions{answers: []pcAnswer{pcFixIt, pcPass}})
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,author:2,helper:Reviewer:2,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	p := r.stage().PreCompletion
	if p.Round != 2 || p.Corrections != 1 {
		t.Fatalf("round record = %+v, want round 2 after one correction", p)
	}
}

// The author reads the helper's words as they were written, in a block of their own, in the correction turn only.
func TestPreCompletion_FeedbackVerbatimInTheCorrectionPrompt(t *testing.T) {
	r := newPCRig(t, pcRigOptions{answers: []pcAnswer{pcFixIt, pcPass}})
	r.waitStatus(state.StatusDone)
	first, second := r.author.prompt(0), r.author.prompt(1)
	if strings.Contains(first, pcFixIt.suggested) {
		t.Fatalf("the first turn already carries feedback:\n%s", first)
	}
	for _, want := range []string{preCompletionFeedbackHeading, pcFixIt.summary, pcFixIt.suggested} {
		if !strings.Contains(second, want) {
			t.Fatalf("correction prompt lacks %q:\n%s", want, second)
		}
	}
	stageDir := filepath.Join(r.dir, pcStageID)
	if _, err := os.Stat(filepath.Join(stageDir, "feedback.md")); err == nil {
		t.Fatal("the helper's words must not go through the user's feedback.md")
	}
	if _, err := os.Stat(filepath.Join(stageDir, "verify", "feedback.md")); err == nil {
		t.Fatal("the helper's words must not go through verify/feedback.md")
	}
}

// With no budget for corrections the first needs_changes ends the stage, with the report named in its reason.
func TestPreCompletion_BudgetExhaustedFails(t *testing.T) {
	zero := 0
	r := newPCRig(t, pcRigOptions{maxFailures: &zero, answers: []pcAnswer{pcFixIt}})
	r.waitStatus(state.StatusFailed)
	if got, want := r.order(), "author:1,helper:Reviewer:1"; got != want {
		t.Fatalf("order = %s, want %s (no correction, no verify)", got, want)
	}
	history, err := r.store.History()
	if err != nil {
		t.Fatal(err)
	}
	last := history[len(history)-1]
	if last.To != state.StatusFailed || !strings.Contains(last.Reason, "budget") || !strings.Contains(last.Reason, "report.md") {
		t.Fatalf("failure = %+v, want the exhausted budget and the report", last)
	}
	if p := r.stage().PreCompletion; p.Decision != state.PreCompletionExhausted {
		t.Fatalf("decision = %q, want exhausted", p.Decision)
	}
}

// A stage in a run with collaboration but without any pre_completion helper records its empty acceptance and goes on.
func TestPreCompletion_EmptyRoundAcceptsBeforeVerify(t *testing.T) {
	r := newPCRig(t, pcRigOptions{noHelper: true})
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	history, err := r.store.History()
	if err != nil {
		t.Fatal(err)
	}
	empty := 0
	for _, tr := range history {
		if tr.Event == "pre_completion_accept_empty" {
			empty++
		}
	}
	if empty != 1 {
		t.Fatalf("empty acceptances = %d, want 1", empty)
	}
}

// A verify rejection after an accepted round sends the work back to the author, and the next completion is reviewed
// again — an old pass is no pass for new work.
func TestPreCompletion_VerifyRejectRepeatsRound(t *testing.T) {
	var verifies int
	r := newPCRig(t, pcRigOptions{answers: []pcAnswer{pcPass}, beforeRun: func(r *pcRig) {
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

// ---- admission ----

const pcAdmitStage = "auto"

func pcAdmissionOrchestrator(t *testing.T) (*Orchestrator, *state.Store) {
	t.Helper()
	return newAssistantsTestOrchestrator(t, true,
		flow.Stage{ID: pcAdmitStage, Name: "Auto", Agents: []flow.AgentType{flow.AgentAuto}},
		flow.Stage{ID: "planonly", Name: "Plan", Agents: []flow.AgentType{flow.AgentPlanning}},
		flow.Stage{ID: "scripted", Name: "Script", Script: "true"},
	)
}

func pcAttach(o *Orchestrator, stageID, opID string) error {
	_, err := o.Attach(context.Background(), AttachRequest{
		OperationID: opID, StageID: stageID, ProfileID: "reviewer", Instruction: "review", Mode: assistants.ModeRequired,
		Trigger: assistants.TriggerPreCompletion, Actor: state.Actor{Kind: "user"},
	})
	return err
}

func pcStartStage(t *testing.T, o *Orchestrator, stageID string) {
	t.Helper()
	activateStage(t, o, stageID)
}

// A pre_completion helper is a top-level helper of an execution stage that already runs: nothing else is admitted.
func TestPreCompletion_AdmissionRefusals(t *testing.T) {
	o, store := pcAdmissionOrchestrator(t)
	pcStartStage(t, o, pcAdmitStage)
	activateStage(t, o, "scripted")

	t.Run("planning-only stage", func(t *testing.T) {
		assertRejected(t, pcAttach(o, "planonly", "op-plan"), assistants.CodeInvalidRequest)
	})
	t.Run("script stage", func(t *testing.T) {
		assertRejected(t, pcAttach(o, "scripted", "op-script"), assistants.CodeInvalidRequest)
	})
	t.Run("stage that has not started", func(t *testing.T) {
		o2, _ := pcAdmissionOrchestrator(t)
		assertRejected(t, pcAttach(o2, pcAdmitStage, "op-pending"), assistants.CodeInvalidStatus)
	})
	t.Run("run scope", func(t *testing.T) {
		_, err := o.AttachAndSend(context.Background(), AttachAndSendRequest{
			OperationID: "op-run", ProfileID: "reviewer", Scope: assistants.ScopeRun, Instruction: "review",
			Trigger: assistants.TriggerPreCompletion, Actor: state.Actor{Kind: "user"},
		})
		assertRejected(t, err, assistants.CodeInvalidRequest)
	})
	t.Run("stage scope with a first message", func(t *testing.T) {
		msg := "hello"
		_, err := o.AttachAndSend(context.Background(), AttachAndSendRequest{
			OperationID: "op-first", ProfileID: "reviewer", StageID: pcAdmitStage, Instruction: "review", FirstMessage: &msg,
			Trigger: assistants.TriggerPreCompletion, Actor: state.Actor{Kind: "user"},
		})
		assertRejected(t, err, assistants.CodeInvalidRequest)
	})
	t.Run("delegated child and continuation", func(t *testing.T) {
		assertRejected(t, rejectOnMessageDelegation([]assistants.DelegateRequest{{Trigger: assistants.TriggerPreCompletion}}, nil), assistants.CodeInvalidRequest)
		assertRejected(t, rejectOnMessageDelegation(nil, &assistants.Continuation{Trigger: assistants.TriggerPreCompletion}), assistants.CodeInvalidRequest)
	})
	t.Run("spawned by the stage agent", func(t *testing.T) {
		_, err := o.SpawnHelper(context.Background(), SpawnHelperRequest{
			OperationID: "op-spawn", ProfileID: "reviewer", Instruction: "review", Trigger: assistants.TriggerPreCompletion,
			Actor: boardTaskActor(meshStageParticipantID(pcAdmitStage)),
		})
		assertRejected(t, err, assistants.CodeInvalidRequest)
	})
	t.Run("finished stage", func(t *testing.T) {
		o3, _ := pcAdmissionOrchestrator(t)
		pcStartStage(t, o3, pcAdmitStage)
		o3.Trigger(pcAdmitStage, bus.EvComplete, bus.GuardCtx{}, "")
		assertRejected(t, pcAttach(o3, pcAdmitStage, "op-done"), assistants.CodeInvalidStatus)
	})
	if rs := store.Snapshot(); len(rs.Assistants.Tasks) != 0 {
		t.Fatalf("a refused attach left tasks behind: %v", rs.Assistants.Tasks)
	}

	if err := pcAttach(o, pcAdmitStage, "op-ok"); err != nil {
		t.Fatalf("Attach to a running execution stage: %v", err)
	}
	tasks := store.Snapshot().Assistants.ByRootStage[pcAdmitStage]
	if len(tasks) != 1 {
		t.Fatalf("tasks = %v, want the helper", tasks)
	}
	if tk := store.Snapshot().Assistants.Tasks[tasks[0]]; tk.Status != assistants.StatusWatching || tk.Trigger != assistants.TriggerPreCompletion {
		t.Fatalf("helper = %s/%s, want watching pre_completion", tk.Trigger, tk.Status)
	}
}

// openRound parks the stage in an unresolved round the way the author's exit does.
func pcOpenRound(t *testing.T, o *Orchestrator, stageID string) {
	t.Helper()
	_, ok := o.Trigger(stageID, bus.EvAwaitPreCompletion, bus.GuardCtx{PreCompletion: &state.PreCompletionStep{Phase: phaseAutonomous}}, "")
	if !ok {
		t.Fatalf("await_pre_completion refused for %s (status %s)", stageID, o.currentStatus(stageID))
	}
}

// While a round is unresolved no helper may join, whatever the stage's status; once the round yields a correction,
// the correcting author takes new helpers again.
func TestPreCompletion_AttachRefusedWhileRoundOpen(t *testing.T) {
	for _, tc := range []struct {
		name string
		move func(*testing.T, *Orchestrator)
	}{
		{"awaiting_pre_completion", func(*testing.T, *Orchestrator) {}},
		{"paused", func(t *testing.T, o *Orchestrator) {
			if _, ok := o.Trigger(pcAdmitStage, bus.EvPause, bus.GuardCtx{}, ""); !ok {
				t.Fatal("pause refused")
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _ := pcAdmissionOrchestrator(t)
			pcStartStage(t, o, pcAdmitStage)
			pcOpenRound(t, o, pcAdmitStage)
			tc.move(t, o)
			err := pcAttach(o, pcAdmitStage, "op-late")
			assertRejected(t, err, assistants.CodeInvalidStatus)
			if !strings.Contains(err.Error(), "round 1") {
				t.Fatalf("refusal = %v, want the round named", err)
			}
		})
	}
}

func TestPreCompletion_AttachAllowedBeforeARoundAndInTheCorrection(t *testing.T) {
	o, store := pcAdmissionOrchestrator(t)
	pcStartStage(t, o, pcAdmitStage)
	if err := pcAttach(o, pcAdmitStage, "op-before"); err != nil {
		t.Fatalf("before any round: %v", err)
	}
	pcOpenRound(t, o, pcAdmitStage)
	if _, ok := o.Trigger(pcAdmitStage, bus.EvPreCompletionDecided, bus.GuardCtx{PreCompletion: &state.PreCompletionStep{Decision: state.PreCompletionNeedsChanges}}, ""); !ok {
		t.Fatal("decision refused")
	}
	if err := pcAttach(o, pcAdmitStage, "op-correction"); err != nil {
		t.Fatalf("in the correcting turn: %v", err)
	}
	if got := len(store.Snapshot().Assistants.ByRootStage[pcAdmitStage]); got != 2 {
		t.Fatalf("helpers = %d, want both", got)
	}
}

// From the accepted round (or an empty one) to verify and completion the window stays shut, and so does a verify park.
func TestPreCompletion_AttachRefusedBetweenAcceptanceAndVerify(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *Orchestrator)
	}{
		{"accepted round", func(t *testing.T, o *Orchestrator) {
			pcOpenRound(t, o, pcAdmitStage)
			if _, ok := o.Trigger(pcAdmitStage, bus.EvPreCompletionDecided, bus.GuardCtx{PreCompletion: &state.PreCompletionStep{Decision: state.PreCompletionAccepted}}, ""); !ok {
				t.Fatal("decision refused")
			}
		}},
		{"empty round before verify", func(t *testing.T, o *Orchestrator) {
			if _, ok := o.Trigger(pcAdmitStage, bus.EvPreCompletionAcceptEmpty, bus.GuardCtx{PreCompletion: &state.PreCompletionStep{}}, ""); !ok {
				t.Fatal("empty acceptance refused")
			}
		}},
		{"verify parked on a usage limit", func(t *testing.T, o *Orchestrator) {
			pcOpenRound(t, o, pcAdmitStage)
			o.Trigger(pcAdmitStage, bus.EvPreCompletionDecided, bus.GuardCtx{PreCompletion: &state.PreCompletionStep{Decision: state.PreCompletionAccepted}}, "")
			q := state.QuotaWait{Target: state.QuotaTargetVerify, Until: time.Now().Add(time.Hour)}
			if _, ok := o.Trigger(pcAdmitStage, bus.EvAwaitQuota, bus.GuardCtx{Quota: &q}, ""); !ok {
				t.Fatal("park refused")
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _ := pcAdmissionOrchestrator(t)
			pcStartStage(t, o, pcAdmitStage)
			tc.setup(t, o)
			assertRejected(t, pcAttach(o, pcAdmitStage, "op-late"), assistants.CodeInvalidStatus)
		})
	}
}

// A question is not asked against a round of helpers accepted after the question was found open: the acceptance keeps
// the stage, so no helper can be attached past it. An empty acceptance holds no question back.
func TestPreCompletion_QuestionWaitsForTheAcceptedRound(t *testing.T) {
	for _, tc := range []struct {
		name       string
		withHelper bool
		accept     bus.FSMEvent
		wantMove   questionMove
		wantStatus state.StageStatus
	}{
		{"accepted round of helpers", true, bus.EvPreCompletionDecided, questionDeferred, state.StatusRunning},
		{"empty acceptance", false, bus.EvPreCompletionAcceptEmpty, questionAsked, state.StatusAwaitingUserInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store := pcAdmissionOrchestrator(t)
			pcStartStage(t, o, pcAdmitStage)
			if tc.withHelper {
				if err := pcAttach(o, pcAdmitStage, "op-helper"); err != nil {
					t.Fatalf("Attach: %v", err)
				}
			}
			writeQuestionFile(t, filepath.Join(o.opts.RunDir, pcAdmitStage), phaseAutonomous, "q1", nil)
			o.testBeforeQuestionCommit = func() {
				step := state.PreCompletionStep{Phase: phaseAutonomous}
				if tc.withHelper {
					pcOpenRound(t, o, pcAdmitStage)
					step = state.PreCompletionStep{Decision: state.PreCompletionAccepted}
				}
				if _, ok := o.Trigger(pcAdmitStage, tc.accept, bus.GuardCtx{PreCompletion: &step}, ""); !ok {
					t.Fatalf("%s refused", tc.accept)
				}
			}
			_, move := o.surfaceQuestion(pcAdmitStage, phaseAutonomous, "q1")
			if st := store.Snapshot().Stages[pcAdmitStage]; move != tc.wantMove || st.Status != tc.wantStatus {
				t.Fatalf("move %d, status %s, round %+v; want move %d, status %s", move, st.Status, st.PreCompletion, tc.wantMove, tc.wantStatus)
			}
			if tc.withHelper && !questionsSettledByRound(store.Snapshot().Stages[pcAdmitStage]) {
				t.Fatal("the question dropped the round's acceptance")
			}
		})
	}
}

// ---- order, rounds and budgets ----

// Nobody reviews before the author's turn has ended: a held author keeps its helpers at rest.
func TestPreCompletion_StartsOnlyAfterTheAuthorTurnEnds(t *testing.T) {
	r := newPCRig(t, pcRigOptions{hold: true, answers: []pcAnswer{pcPass}})
	r.waitFor("the author's turn", func(st state.StageState) bool { return st.Status == state.StatusRunning && r.author.turns.Load() == 1 })
	if got := r.calls.Load(); got != 0 {
		t.Fatalf("helper turns while the author works = %d, want 0", got)
	}
	if st := r.stage(); st.PreCompletion != nil {
		t.Fatalf("round record while the author works = %+v, want none", st.PreCompletion)
	}
	r.releaseAuthor()
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

// A correction that leaves the summary as it was is still reviewed: the new round is owed to the new turn, not to a
// changed file.
func TestPreCompletion_NewRoundAfterCorrectionWithTheSameSummary(t *testing.T) {
	r := newPCRig(t, pcRigOptions{constantSummary: true, answers: []pcAnswer{pcFixIt, pcPass}})
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,author:2,helper:Reviewer:2,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

// Several helpers answer one round: the required ones that asked for changes cost one correction, in the order their tasks
// were created, and an advisory helper's words come apart as non-blocking.
func TestPreCompletion_MixedHelpersOneCorrection(t *testing.T) {
	r := newPCRig(t, pcRigOptions{helpers: []pcHelperSpec{
		{title: "Alpha", answers: []pcAnswer{{verdict: assistants.VerdictNeedsChanges, summary: "alpha says fix the lock", suggested: "take the lock first"}, pcPass}},
		{title: "Beta", answers: []pcAnswer{{verdict: assistants.VerdictNeedsChanges, summary: "beta says fix the cache"}, pcPass}},
		{title: "Gamma", advisory: true, answers: []pcAnswer{{verdict: assistants.VerdictNeedsChanges, summary: "gamma would rename it"}, pcPass}},
	}})
	r.waitStatus(state.StatusDone)
	if p := r.stage().PreCompletion; p.Corrections != 1 || p.Round != 2 {
		t.Fatalf("round record = %+v, want one correction for three helpers", p)
	}
	prompt := r.author.prompt(1)
	alpha, beta, advisory, gamma := strings.Index(prompt, "alpha says"), strings.Index(prompt, "beta says"), strings.Index(prompt, "Рекомендации (не блокируют)"), strings.Index(prompt, "gamma would")
	if alpha < 0 || beta < alpha || advisory < beta || gamma < advisory {
		t.Fatalf("correction prompt order: alpha %d, beta %d, advisory block %d, gamma %d\n%s", alpha, beta, advisory, gamma, prompt)
	}
	if verifies := strings.Count(r.order(), "verify"); verifies != 1 {
		t.Fatalf("verify ran %d times in %s, want once after the accepted round", verifies, r.order())
	}
}

// A helper that gives no verdict, or whose turn fails, never passes the round: it waits for a human, and the human's
// waiver of that round lets it go on.
func TestPreCompletion_NonCodeOutcomesNeverPass(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer pcAnswer
	}{
		{"inconclusive", pcAnswer{verdict: assistants.VerdictInconclusive, summary: "cannot tell"}},
		{"process failure", pcAnswer{err: errors.New("exit status 1")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPCRig(t, pcRigOptions{answers: []pcAnswer{tc.answer}})
			r.waitFor("a blocked round", func(st state.StageState) bool {
				rs := r.store.Snapshot()
				return st.Status == state.StatusAwaitingPreCompletion && state.PreCompletionNeedsHuman(&rs, pcStageID)
			})
			if strings.Contains(r.order(), "verify") || r.author.turns.Load() != 1 {
				t.Fatalf("order = %s: a blocked round must start neither verify nor the author", r.order())
			}
			ids := r.store.Snapshot().Assistants.ByRootStage[pcStageID]
			if err := r.o.WaiveTask(context.Background(), ids[0], state.Actor{Kind: "user"}, "reviewed by hand"); err != nil {
				t.Fatalf("WaiveTask: %v", err)
			}
			r.waitStatus(state.StatusDone)
			if got, want := strings.Count(r.order(), "verify"), 1; got != want {
				t.Fatalf("verify ran %d times, want %d", got, want)
			}
		})
	}
}

// The waiver covers the round it was given in only: after verify sends the work back, the next round asks the helper again.
func TestPreCompletion_RoundScopedWaiver(t *testing.T) {
	verifies := 0
	r := newPCRig(t, pcRigOptions{answers: []pcAnswer{{verdict: assistants.VerdictInconclusive, summary: "cannot tell"}}, beforeRun: func(r *pcRig) {
		r.o.runVerifyShell = func(context.Context, string, string, string) (string, error) {
			verifies++
			r.log.add("verify")
			if verifies == 1 {
				return "", errors.New("exit status 1")
			}
			return "", nil
		}
	}})
	blocked := func(round int) func(state.StageState) bool {
		return func(st state.StageState) bool {
			rs := r.store.Snapshot()
			return st.Status == state.StatusAwaitingPreCompletion && st.PreCompletion.Round == round && state.PreCompletionNeedsHuman(&rs, pcStageID)
		}
	}
	r.waitFor("a blocked round 1", blocked(1))
	helper := r.store.Snapshot().Assistants.ByRootStage[pcStageID][0]
	if err := r.o.WaiveTask(context.Background(), helper, state.Actor{Kind: "user"}, "seen it"); err != nil {
		t.Fatalf("WaiveTask round 1: %v", err)
	}
	r.waitFor("a blocked round 2", blocked(2))
	if task := r.store.Snapshot().Assistants.Tasks[helper]; task.Waived {
		t.Fatal("a round waiver must not mark the helper as waived for good")
	}
	if err := r.o.WaiveTask(context.Background(), helper, state.Actor{Kind: "user"}, "and this one"); err != nil {
		t.Fatalf("WaiveTask round 2: %v", err)
	}
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,verify,author:2,helper:Reviewer:2,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

// ---- control: exit_on_fail, Pause/Continue, Revise, Retry ----

// Under exit_on_fail a required helper that died ends the stage at once: the round's other helper asking for changes is
// not corrected first.
func TestPreCompletion_ExitOnFailDeadRequiredBeforeCorrection(t *testing.T) {
	r := newPCRig(t, pcRigOptions{exitOnFail: true, helpers: []pcHelperSpec{
		{title: "Asker", answers: []pcAnswer{pcFixIt}},
		{title: "Dying", answers: []pcAnswer{{err: errors.New("exit status 1")}}},
	}})
	r.waitStatus(state.StatusFailed)
	if r.author.turns.Load() != 1 || strings.Contains(r.order(), "verify") {
		t.Fatalf("order = %s, want no correction and no verify", r.order())
	}
	history, err := r.store.History()
	if err != nil {
		t.Fatal(err)
	}
	if last := history[len(history)-1]; !strings.Contains(last.Reason, "required pre_completion helper") {
		t.Fatalf("failure reason = %q, want the dead helper named", last.Reason)
	}
	if p := r.stage().PreCompletion; p.Decision != state.PreCompletionFailed {
		t.Fatalf("decision = %q, want failed", p.Decision)
	}
}

// The decision waits for Continue when the round's last answer came during a Pause, and then it is made once.
func TestPreCompletion_PauseContinueDecides(t *testing.T) {
	r := newPCRig(t, pcRigOptions{holdHelper: true, answers: []pcAnswer{pcPass}})
	r.waitFor("the helper's turn", func(state.StageState) bool { return r.calls.Load() == 1 })
	if err := r.o.Pause(context.Background(), pcStageID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	r.waitStatus(state.StatusPaused)
	r.releaseHelpers()
	r.waitFor("the helper's answer", func(state.StageState) bool {
		rs := r.store.Snapshot()
		task := rs.Assistants.Tasks[rs.Assistants.ByRootStage[pcStageID][0]]
		return task.LastOutcome == state.RequestOutcomeResult
	})
	if st := r.stage(); st.Status != state.StatusPaused || strings.Contains(r.order(), "verify") {
		t.Fatalf("status %s, order %s: a paused stage must not go on", st.Status, r.order())
	}
	if err := r.o.Continue(context.Background(), pcStageID); err != nil {
		t.Fatalf("Continue: %v", err)
	}
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

// A Revise during the round starts the author's feedback run without bumping the helpers' generation, opens a new cycle,
// and the answer of the old round's request decides nothing of the new one.
func TestPreCompletion_ReviseFromRoundNoBump(t *testing.T) {
	r := newPCRig(t, pcRigOptions{holdHelper: true, answers: []pcAnswer{pcFixIt, pcPass, pcPass}})
	r.waitFor("the helper's turn", func(state.StageState) bool { return r.calls.Load() == 1 })
	applied, _, err := r.o.Revise(context.Background(), pcStageID, "use the cache instead")
	if err != nil || !applied {
		t.Fatalf("Revise: applied=%v err=%v", applied, err)
	}
	r.waitFor("the second author turn", func(state.StageState) bool { return r.author.turns.Load() >= 2 })
	rs := r.store.Snapshot()
	if gen := rs.Assistants.RootGenerations[pcStageID]; gen != 0 {
		t.Fatalf("root generation = %d, want no bump", gen)
	}
	if !strings.Contains(r.author.prompt(1), "use the cache instead") {
		t.Fatalf("feedback run prompt lacks the note:\n%s", r.author.prompt(1))
	}
	r.releaseHelpers()
	r.waitStatus(state.StatusDone)
	p := r.stage().PreCompletion
	if p.Cycle != 1 || p.Corrections != 0 {
		t.Fatalf("round record = %+v, want a new cycle with no correction (the stale needs_changes was no answer to its round)", p)
	}
}

// A failed stage retried starts a new cycle with a fresh budget.
func TestPreCompletion_RetryStartsNewCycle(t *testing.T) {
	zero := 0
	r := newPCRig(t, pcRigOptions{keepAlive: true, maxFailures: &zero, answers: []pcAnswer{pcFixIt, pcPass}})
	r.waitStatus(state.StatusFailed)
	if err := r.o.Retry(context.Background(), pcStageID); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	r.waitStatus(state.StatusDone)
	p := r.stage().PreCompletion
	if p.Cycle != 1 || p.Round != 1 || p.Corrections != 0 {
		t.Fatalf("round record = %+v, want cycle 1, round 1 and no corrections", p)
	}
	if got, want := r.order(), "author:1,helper:Reviewer:1,author:2,helper:Reviewer:2,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

// ---- restarts ----

// A restart while the helpers review keeps the round: the helper's cut turn runs again, the author does not.
func TestPreCompletion_RestartDuringTheReview(t *testing.T) {
	r := newPCRig(t, pcRigOptions{holdHelper: true, answers: []pcAnswer{pcPass}})
	r.waitFor("the helper's turn", func(state.StageState) bool { return r.calls.Load() == 1 })
	r.restart()
	r.releaseHelpers()
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,helper:Reviewer:2,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

// A restart between the accepted round and the end of verify repeats verify only: neither the author nor the round.
func TestPreCompletion_RestartAfterPassBeforeVerify(t *testing.T) {
	r := newPCRig(t, pcRigOptions{blockVerify: true, answers: []pcAnswer{pcPass}})
	r.waitFor("verify to start", func(state.StageState) bool { return strings.Contains(r.order(), "verify") })
	if p := r.stage().PreCompletion; !p.Accepted {
		t.Fatalf("round record = %+v, want the accepted intent while verify runs", p)
	}
	r.restart()
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,verify,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

// A restart in the correction turn goes on with the correction: the old marker on disk does not finish the stage, and the
// author still gets what the helpers said.
func TestPreCompletion_RestartInTheCorrection(t *testing.T) {
	r := newPCRig(t, pcRigOptions{answers: []pcAnswer{pcFixIt, pcPass}, beforeRun: func(r *pcRig) {
		if !r.restarted {
			r.author.blockTurn.Store(2)
		}
	}})
	r.waitFor("the second author turn", func(state.StageState) bool { return r.author.turns.Load() == 2 })
	r.restarted = true
	r.restart()
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,author:2,author:3,helper:Reviewer:2,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	if !strings.Contains(r.author.prompt(2), pcFixIt.suggested) {
		t.Fatalf("the restarted correction lost the feedback:\n%s", r.author.prompt(2))
	}
}

// A restart after verify sent the work back runs the correction and reviews it again.
func TestPreCompletion_RestartAfterVerifyRejected(t *testing.T) {
	verifies := 0
	r := newPCRig(t, pcRigOptions{answers: []pcAnswer{pcPass}, beforeRun: func(r *pcRig) {
		r.o.runVerifyShell = func(context.Context, string, string, string) (string, error) {
			verifies++
			r.log.add("verify")
			if verifies == 1 {
				return "", errors.New("exit status 1")
			}
			return "", nil
		}
		if !r.restarted {
			r.author.blockTurn.Store(2)
		}
	}})
	r.waitFor("the correction turn", func(state.StageState) bool { return r.author.turns.Load() == 2 })
	if p := r.stage().PreCompletion; p.Accepted || p.VerifyFailures != 1 {
		t.Fatalf("round record = %+v, want the intent used up by the rejection and its counter carried", p)
	}
	r.restarted = true
	r.restart()
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,verify,author:2,author:3,helper:Reviewer:2,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

// ---- usage limits ----

var pcQuotaStart = time.Date(2026, 10, 23, 23, 48, 0, 0, time.UTC)

// Verify parked on a usage limit after an accepted round wakes into verify alone: no new round, no author, and the window
// stays shut to Attach for the whole park.
func TestPreCompletion_VerifyQuotaWakeResumesVerify(t *testing.T) {
	clk := newPCWakeClock(pcQuotaStart)
	r := newPCRig(t, pcRigOptions{clock: clk, answers: []pcAnswer{pcPass}, verifyAgent: func(call int) (verify.RunOutcome, error) {
		if call == 1 {
			return verify.RunOutcome{}, &executor.UsageLimitError{Provider: "codex", ResetAt: clk.Now().Add(2 * time.Minute), Source: executor.UsageLimitSourceText, Message: "limit"}
		}
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok"}}, nil
	}})
	r.waitStatus(state.StatusAwaitingQuota)
	if p := r.stage().PreCompletion; !p.Accepted {
		t.Fatalf("round record = %+v, want the accepted intent kept through the verify park", p)
	}
	_, err := r.o.Attach(context.Background(), AttachRequest{
		OperationID: "op-late", StageID: pcStageID, ProfileID: "late", Instruction: "review", Mode: assistants.ModeRequired,
		Trigger: assistants.TriggerPreCompletion, Actor: state.Actor{Kind: "user"},
	})
	assertRejected(t, err, assistants.CodeInvalidStatus)

	clk.awaitWakeTimer(t)
	clk.Advance(5 * time.Minute)
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,verify,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

// A helper's own usage-limit park costs the stage nothing: the round waits for it, and the budgets are as they were.
func TestPreCompletion_HelperQuotaKeepsBudgets(t *testing.T) {
	clk := newPCWakeClock(pcQuotaStart)
	r := newPCRig(t, pcRigOptions{clock: clk, answers: []pcAnswer{
		{err: &executor.UsageLimitError{Provider: "codex", ResetAt: pcQuotaStart.Add(2 * time.Minute), Source: executor.UsageLimitSourceText, Message: "limit"}},
		pcPass,
	}})
	r.waitFor("the helper's park", func(state.StageState) bool {
		rs := r.store.Snapshot()
		ids := rs.Assistants.ByRootStage[pcStageID]
		return len(ids) == 1 && rs.Assistants.Tasks[ids[0]].Quota != nil && !rs.Assistants.Tasks[ids[0]].Quota.Until.IsZero()
	})
	st := r.stage()
	if st.Status != state.StatusAwaitingPreCompletion || st.PreCompletion.Attempt != 0 || st.PreCompletion.Corrections != 0 {
		t.Fatalf("stage %s, round %+v: a helper's park must leave the round open and the budgets untouched", st.Status, st.PreCompletion)
	}
	clk.awaitWakeTimer(t)
	clk.Advance(5 * time.Minute)
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,helper:Reviewer:2,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

// ---- notices, fail-closed records, races ----

func pcNotices(t *testing.T, runDir string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if json.Unmarshal([]byte(line), &rec) == nil && strings.HasPrefix(rec.Type, "pre_completion_") {
			out = append(out, map[string]any{"type": rec.Type, "data": rec.Data})
		}
	}
	return out
}

// Each round announces its start and its end in notices.jsonl, which a reconnecting dashboard reads.
func TestPreCompletion_NoticesDurable(t *testing.T) {
	r := newPCRig(t, pcRigOptions{answers: []pcAnswer{pcFixIt, pcPass}})
	r.waitStatus(state.StatusDone)
	got := pcNotices(t, r.dir)
	var types []string
	for _, n := range got {
		types = append(types, n["type"].(string))
	}
	want := []string{"pre_completion_round_started", "pre_completion_decided", "pre_completion_round_started", "pre_completion_decided"}
	if !slices.Equal(types, want) {
		t.Fatalf("notices = %v, want %v", types, want)
	}
	first, last := got[1]["data"].(map[string]any), got[3]["data"].(map[string]any)
	if first["decision"] != "needs_changes" || first["round"] != float64(1) || first["corrections"] != float64(1) || first["max_failures"] != float64(1) {
		t.Fatalf("first decision notice = %v", first)
	}
	if last["decision"] != "accepted" || last["round"] != float64(2) {
		t.Fatalf("last decision notice = %v", last)
	}
	if started := got[0]["data"].(map[string]any); started["round"] != float64(1) || started["helpers"] != float64(1) {
		t.Fatalf("start notice = %v", started)
	}
}

// A report or feedback that cannot be written fails the stage: neither the author nor verify goes on without it.
func TestPreCompletion_FeedbackWriteFailureFailsClosed(t *testing.T) {
	r := newPCRig(t, pcRigOptions{answers: []pcAnswer{pcFixIt}, beforeRun: func(r *pcRig) {
		stageDir := filepath.Join(r.dir, pcStageID)
		if err := os.MkdirAll(stageDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stageDir, "pre-completion"), nil, 0o644); err != nil { // a file where the directory belongs
			t.Fatal(err)
		}
	}})
	r.waitStatus(state.StatusFailed)
	if r.author.turns.Load() != 1 || strings.Contains(r.order(), "verify") {
		t.Fatalf("order = %s, want neither a correction nor verify", r.order())
	}
	history, err := r.store.History()
	if err != nil {
		t.Fatal(err)
	}
	if last := history[len(history)-1]; !strings.Contains(last.Reason, "pre-completion") {
		t.Fatalf("failure reason = %q, want the record named", last.Reason)
	}
}

// Every trigger of a decision may fire at once; the stage still goes on exactly once.
func TestPreCompletion_ConcurrentTriggersOneContinuation(t *testing.T) {
	r := newPCRig(t, pcRigOptions{holdHelper: true, answers: []pcAnswer{pcFixIt, pcPass}})
	r.waitFor("the helper's turn", func(state.StageState) bool { return r.calls.Load() == 1 })
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
					r.o.resumePreCompletionRound(context.Background(), pcStageID)
				}
			}
		})
	}
	r.releaseHelpers()
	r.waitStatus(state.StatusDone)
	close(stop)
	wg.Wait()
	if got, want := r.order(), "author:1,helper:Reviewer:1,author:2,helper:Reviewer:2,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	if p := r.stage().PreCompletion; p.Corrections != 1 {
		t.Fatalf("corrections = %d, want 1", p.Corrections)
	}
}

// A helper attached just as the gate looks finds a round, not a verify that skips it: the gate's empty acceptance loses to
// the attach, and the round that follows holds the helper.
func TestPreCompletion_AttachRacesGateAttachFirst(t *testing.T) {
	var attached atomic.Bool
	r := newPCRig(t, pcRigOptions{noHelper: true, beforeRun: func(r *pcRig) {
		r.o.testAfterPreCompletionSnapshot = func() {
			if !attached.CompareAndSwap(false, true) {
				return
			}
			if _, err := r.o.Attach(context.Background(), AttachRequest{
				OperationID: "op-race", StageID: pcStageID, ProfileID: "late", Instruction: "review", Mode: assistants.ModeRequired,
				Trigger: assistants.TriggerPreCompletion, Actor: state.Actor{Kind: "user"},
			}); err != nil {
				t.Errorf("Attach between the gate's look and its commit: %v", err)
			}
		}
	}})
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Late:1,verify"; got != want {
		t.Fatalf("order = %s, want %s: verify must wait for the helper that got in", got, want)
	}
}

// From the empty acceptance on, a helper is refused: verify is under way without it.
func TestPreCompletion_AttachRacesGateGateFirst(t *testing.T) {
	var refused atomic.Bool
	r := newPCRig(t, pcRigOptions{noHelper: true, beforeRun: func(r *pcRig) {
		r.o.runVerifyShell = func(context.Context, string, string, string) (string, error) {
			r.log.add("verify")
			_, err := r.o.Attach(context.Background(), AttachRequest{
				OperationID: "op-late", StageID: pcStageID, ProfileID: "late", Instruction: "review", Mode: assistants.ModeRequired,
				Trigger: assistants.TriggerPreCompletion, Actor: state.Actor{Kind: "user"},
			})
			var rej *state.OperationRejectedError
			refused.Store(errors.As(err, &rej) && rej.Code == assistants.CodeInvalidStatus)
			return "", nil
		}
	}})
	r.waitStatus(state.StatusDone)
	if !refused.Load() {
		t.Fatal("a helper attached during verify of an empty round must be refused")
	}
	if got, want := r.order(), "author:1,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

// Without the collaboration capability the gate and the journal are what they were: no pre-completion record at all.
func TestPreCompletion_NoAssistantsNoJournalChange(t *testing.T) {
	r := newPCRig(t, pcRigOptions{noAssistants: true, noHelper: true})
	r.waitStatus(state.StatusDone)
	history, err := r.store.History()
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range history {
		if strings.Contains(tr.Event, "pre_completion") || tr.PreCompletion != nil {
			t.Fatalf("journal carries a pre-completion record without collaboration: %+v", tr)
		}
	}
	if r.stage().PreCompletion != nil {
		t.Fatalf("round record = %+v, want none", r.stage().PreCompletion)
	}
}

// ---- recovery through the gate ----

// A stage whose files say it is done, found by a restart, is not completed by them: its helpers review it first.
func TestPreCompletion_RecoveryProbeGoesThroughTheGate(t *testing.T) {
	r := newPCRig(t, pcRigOptions{noVerify: true, answers: []pcAnswer{pcPass}, beforeRun: func(r *pcRig) {
		if !r.restarted {
			r.author.blockAfterWriting.Store(1)
		}
	}})
	r.awaitSummary()
	r.restarted = true
	r.restart()
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1"; got != want {
		t.Fatalf("order = %s, want %s: the helper reviews, the author is not run again", got, want)
	}
}

// With no helper the restart records the empty acceptance before it completes the stage, so no attach slips in.
func TestPreCompletion_RecoveryProbeRecordsTheEmptyAcceptance(t *testing.T) {
	r := newPCRig(t, pcRigOptions{noVerify: true, noHelper: true, beforeRun: func(r *pcRig) {
		if !r.restarted {
			r.author.blockAfterWriting.Store(1)
		}
	}})
	r.awaitSummary()
	r.restarted = true
	r.restart()
	r.waitStatus(state.StatusDone)
	history, err := r.store.History()
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	for _, tr := range history {
		if tr.StageID == pcStageID && tr.Seq > 4 {
			events = append(events, tr.Event)
		}
	}
	if i := slices.Index(events, "pre_completion_accept_empty"); i < 0 || i > slices.Index(events, "complete") {
		t.Fatalf("events after the restart = %v, want the empty acceptance before complete", events)
	}
	if r.author.turns.Load() != 1 {
		t.Fatalf("author turns = %d, want the restart not to run the author again", r.author.turns.Load())
	}
}

// ---- delegation, order of helpers, wait cycles, slots ----

// A helper that hands its request to children has not answered it: the round waits for the parent's own result.
func TestPreCompletion_DelegateWaitsForParentResult(t *testing.T) {
	delegate := pcAnswer{raw: `{"schema_version":1,"kind":"delegate","children":[{"profile_id":"late","title":"child","instruction":"check the edge cases"}]}`}
	r := newPCRig(t, pcRigOptions{helpers: []pcHelperSpec{{title: "Reviewer", delegate: true, answers: []pcAnswer{delegate, pcPass}}}})
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,helper:child:1,helper:Reviewer:2,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	if p := r.stage().PreCompletion; p.Round != 1 {
		t.Fatalf("round = %d, want the delegation inside round 1", p.Round)
	}
}

// With one slot per command the author's lease is free during the review: the helper takes the slot the author used.
func TestPreCompletion_MaxParallelOneNoDeadlock(t *testing.T) {
	r := newPCRig(t, pcRigOptions{sharedSlot: true, answers: []pcAnswer{pcFixIt, pcPass}})
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,author:2,helper:Reviewer:2,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

// The run does not end while a round or the correction it asked for is still to come.
func TestPreCompletion_RunWaitsForRoundAndCorrection(t *testing.T) {
	r := newPCRig(t, pcRigOptions{holdHelper: true, answers: []pcAnswer{pcFixIt, pcPass}})
	r.waitFor("the helper's turn", func(state.StageState) bool { return r.calls.Load() == 1 })
	select {
	case <-r.runDone:
		t.Fatal("the run ended while the round was open")
	default:
	}
	r.releaseHelpers()
	select {
	case <-r.runDone:
	case <-time.After(10 * time.Second):
		t.Fatalf("the run did not end after the stage: status %s, order %s", r.stage().Status, r.order())
	}
	if got := r.stage().Status; got != state.StatusDone {
		t.Fatalf("stage = %s, want done", got)
	}
	if got, want := r.order(), "author:1,helper:Reviewer:1,author:2,helper:Reviewer:2,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

// A pre_completion helper starts only when the author's turn has ended, so the author's wait for it never ends; and while
// a round runs, the author does not work, so a helper's wait for it never ends either. Admission refuses both.
func TestPreCompletion_WaitCycleBothDirections(t *testing.T) {
	o, store := pcAdmissionOrchestrator(t)
	countTurns(t, o, make(chan struct{}))
	pcStartStage(t, o, pcAdmitStage)
	if err := pcAttach(o, pcAdmitStage, "op-helper"); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	helperID := store.Snapshot().Assistants.ByRootStage[pcAdmitStage][0]
	author, helper := meshStageParticipantID(pcAdmitStage), meshHelperParticipantID(helperID)

	_, err := o.Wait(context.Background(), WaitRequest{
		OperationID: "op-author-waits", Waiter: author, Actor: helperActor(author),
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnTaskResult, TaskID: string(helperID)},
	})
	if reason := cycleReason(t, err); !strings.Contains(reason, "pre_completion") || !strings.Contains(reason, "finished their turn") {
		t.Fatalf("author waits for its helper: reason = %q, want the pre_completion start condition named", reason)
	}

	pcOpenRound(t, o, pcAdmitStage)
	_, err = o.Wait(context.Background(), WaitRequest{
		OperationID: "op-helper-waits", Waiter: helper, Actor: helperActor(helper),
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: pcAdmitStage, Status: string(state.StatusDone)},
	})
	if reason := cycleReason(t, err); !strings.Contains(reason, "needs_changes with your question") {
		t.Fatalf("helper waits for the author during the round: reason = %q, want the advice to ask through needs_changes", reason)
	}
	if n := len(store.Snapshot().Assistants.Mesh.Waits); n != 0 {
		t.Fatalf("waits recorded = %d, want none", n)
	}
}

// ---- carried budgets, sessions ----

// A rejection that leaves the tree as it was counts toward review_patience across the rounds between the corrections:
// the counters travel with the round, so the third unchanged rejection accepts the stage.
func TestPreCompletion_ReviewPatienceSurvivesRounds(t *testing.T) {
	patience, failures := 2, 6
	// Stalemate counts only agent-step rejections.
	reject := func(int) (verify.RunOutcome, error) {
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictNeedsChanges, Summary: "again"}}, nil
	}
	r := newPCRig(t, pcRigOptions{patience: &patience, verifyFailures: &failures, answers: []pcAnswer{pcPass}, verifyAgent: reject, beforeRun: func(r *pcRig) {
		r.o.fingerprintFn = func(string) (string, bool) { return "same-tree", true }
	}})
	r.waitStatus(state.StatusDone)
	if got, want := strings.Count(r.order(), "verify"), 3; got != want {
		t.Fatalf("verify ran %d times in %s, want %d: the stalemate counter must survive the rounds", got, r.order(), want)
	}
	if r.author.turns.Load() != 3 {
		t.Fatalf("author turns = %d, want 3", r.author.turns.Load())
	}
}

// The correction prompt carries the helpers' feedback whether the author's session is dropped (fresh correction, the
// default) or kept, and the policy decides the session's fate before the author starts.
func TestPreCompletion_FeedbackFreshAndResumedSessions(t *testing.T) {
	for _, tc := range []struct {
		name        string
		fresh       bool
		sessionLeft bool
	}{{"fresh correction drops the session", true, false}, {"resumed correction keeps it", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			var sessionAtSecondTurn bool
			fresh := tc.fresh
			r := newPCRig(t, pcRigOptions{freshCorrection: &fresh, answers: []pcAnswer{pcFixIt, pcPass}, beforeRun: func(r *pcRig) {
				r.author.onTurn = func(n int, logFile string) {
					stageDir := filepath.Dir(logFile)
					session := stagefiles.SessionFile(stageDir, phaseAutonomous)
					if n == 1 {
						_ = os.WriteFile(session, []byte(`{"session_id":"s-1"}`), 0o644)
					}
					if n == 2 {
						_, err := os.Stat(session)
						sessionAtSecondTurn = err == nil
					}
				}
			}})
			r.waitStatus(state.StatusDone)
			if sessionAtSecondTurn != tc.sessionLeft {
				t.Fatalf("session kept for the correction = %v, want %v", sessionAtSecondTurn, tc.sessionLeft)
			}
			if second := r.author.prompt(1); !strings.Contains(second, pcFixIt.suggested) {
				t.Fatalf("correction prompt lacks the feedback:\n%s", second)
			}
		})
	}
}
