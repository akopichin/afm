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
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// pcScriptAuthor is the author of an interactive stage as a script: each run logs the session it resumed ("none" for a
// fresh one) and writes the summary that completes its turn. A run that finds ask-once in the stage directory asks its
// own question and exits unfinished instead; one that finds ask-wait asks it and waits for the answer before finishing.
type pcScriptAuthor struct {
	t       *testing.T
	command string
	runsLog string
}

const pcLeftQuestion = "left" // the id of the question the reviewed author left behind

func newPCScriptAuthor(t *testing.T) *pcScriptAuthor {
	t.Helper()
	dir := t.TempDir()
	a := &pcScriptAuthor{t: t, command: filepath.Join(dir, "author.sh"), runsLog: filepath.Join(dir, "runs.log")}
	script := fmt.Sprintf(`#!/bin/sh
resume=none
prev=
for a in "$@"; do
  if [ "$prev" = --resume ]; then resume=$a; fi
  prev=$a
done
echo "resume=$resume" >> %q
question="$AFM_STAGE_DIR/%[2]s.own.question.json"
if [ -f "$AFM_STAGE_DIR/ask-once" ]; then
  rm -f "$AFM_STAGE_DIR/ask-once" "$AFM_STAGE_DIR/execution_summary.md"
  printf '{"id":"own","question":"which way?"}' > "$question"
  printf '{"type":"result","subtype":"success"}\n'
  exit 0
fi
if [ -f "$AFM_STAGE_DIR/ask-wait" ]; then
  rm -f "$AFM_STAGE_DIR/ask-wait"
  printf '{"id":"own","question":"which way?"}' > "$question"
  i=0
  while [ ! -f "$AFM_STAGE_DIR/%[2]s.own.answer.json" ] && [ $i -lt 400 ]; do sleep 0.05; i=$((i+1)); done
fi
printf '## Summary\nrun %%s\n' "$$" > "$AFM_STAGE_DIR/execution_summary.md"
printf '{"type":"result","subtype":"success"}\n'
`, a.runsLog, phaseAutonomous)
	if err := os.WriteFile(a.command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return a
}

// runs is what each run logged, in order.
func (a *pcScriptAuthor) runs() []string {
	a.t.Helper()
	data, err := os.ReadFile(a.runsLog)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		a.t.Fatal(err)
	}
	return strings.Fields(string(data))
}

// awaitRuns blocks until the author has run n times.
func (a *pcScriptAuthor) awaitRuns(n int) {
	a.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(a.runs()) >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.t.Fatalf("the author ran %d times, want %d: %v", len(a.runs()), n, a.runs())
}

// pcSessionID is the session the stage's author resumes next; "" without one.
func pcSessionID(t *testing.T, runDir string) string {
	t.Helper()
	data, err := os.ReadFile(stagefiles.SessionFile(filepath.Join(runDir, pcStageID), phaseAutonomous))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return s.SessionID
}

// pcClaims records whether each correction's turn owned its correction.
type pcClaims struct {
	mu    sync.Mutex
	owned []bool
}

func (c *pcClaims) add(owned bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.owned = append(c.owned, owned)
}

func (c *pcClaims) list() []bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.owned)
}

// correctionResume is how the correction's run should start: fresh, or in the reviewed session.
func correctionResume(fresh bool, reviewed string) string {
	if fresh {
		return "resume=none"
	}
	return "resume=" + reviewed
}

// askedAfter reports whether the journal moved the stage to the user after seq.
func askedAfter(t *testing.T, store *state.Store, seq uint64) bool {
	t.Helper()
	history, err := store.History()
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(history, func(tr state.Transition) bool {
		return tr.StageID == pcStageID && tr.Seq > seq && tr.Event == string(bus.EvAskUser)
	})
}

// ---- the question the reviewed author left waits for the correction ----

// A restarted poller finds the question the reviewed author left behind after the round asked for changes and before the
// correction's author runs. It is not asked, so the user's answer starts no turn beside the correction: the author runs
// once, prepared by the policy, and the next round is reviewed without it.
func TestPreCompletion_AnswerToALeftQuestionStartsNoSecondAuthor(t *testing.T) {
	for _, fresh := range []bool{true, false} {
		t.Run(fmt.Sprintf("fresh_correction=%v", fresh), func(t *testing.T) {
			author := newPCScriptAuthor(t)
			var claims pcClaims
			var once sync.Once
			decided, release := make(chan struct{}), make(chan struct{})
			r := newPCRig(t, pcRigOptions{freshCorrection: &fresh, interactive: true, authorCommand: author.command, answers: []pcAnswer{pcFixIt, pcPass}, beforeRun: func(r *pcRig) {
				r.o.testAfterRoundDecided = func() {
					once.Do(func() {
						close(decided)
						awaitRelease(t, release)
					})
				}
				r.o.testAfterCorrectionClaim = claims.add
			}})
			select {
			case <-decided:
			case <-time.After(10 * time.Second):
				t.Fatal("the round was not decided")
			}
			reviewed, decision := pcSessionID(t, r.dir), r.stage()
			stageDir := filepath.Join(r.dir, pcStageID)
			writeQuestionFile(t, stageDir, phaseAutonomous, pcLeftQuestion, nil)

			r.o.pollQuestions(map[string]bool{}, map[string]*malformedQuestionState{}) // a restarted poller knows no question
			if st := r.stage(); st.Status != state.StatusRunning || st.Epoch != decision.Epoch {
				t.Errorf("after the poll: status %s, epoch %d; want the question to wait in running, epoch %d", st.Status, st.Epoch, decision.Epoch)
			}
			if err := mcp.WriteAnswer(stageDir, phaseAutonomous, pcLeftQuestion, "go on", false, false); err != nil {
				t.Fatal(err)
			}
			answered := bus.Event{Type: bus.EventUserAnswered, StageID: pcStageID, Data: map[string]any{keyID: pcLeftQuestion, keyPhase: phaseAutonomous, keyAnswer: "go on"}}
			if err := r.o.onUserAnswered(r.o.runContext(context.Background()), answered); err != nil {
				t.Fatalf("onUserAnswered: %v", err)
			}
			close(release)
			r.waitStatus(state.StatusDone)

			if got, want := author.runs(), []string{"resume=none", correctionResume(fresh, reviewed)}; !slices.Equal(got, want) {
				t.Errorf("author runs = %v, want the reviewed turn and one correction %v", got, want)
			}
			if got := claims.list(); !slices.Equal(got, []bool{true}) {
				t.Errorf("correction claims = %v, want one owned correction", got)
			}
			if got, want := r.order(), "helper:Reviewer:1,helper:Reviewer:2,verify"; got != want {
				t.Errorf("order = %s, want %s", got, want)
			}
			if askedAfter(t, r.store, decision.Seq) {
				t.Error("the left question moved the stage to the user between the decision and the correction")
			}
		})
	}
}

// A process that stops between the decision and the correction's start, after a restarted poller found the question the
// reviewed author left behind, leaves the stage running in the journal: the next process starts the correction it owes,
// prepared by the policy, not a resume of the reviewed author.
func TestPreCompletion_RestartInTheGapStartsThePreparedCorrection(t *testing.T) {
	for _, fresh := range []bool{true, false} {
		t.Run(fmt.Sprintf("fresh_correction=%v", fresh), func(t *testing.T) {
			captured := t.TempDir()
			var once sync.Once
			decided := make(chan struct{})
			first := newPCRig(t, pcRigOptions{freshCorrection: &fresh, interactive: true, authorCommand: newPCScriptAuthor(t).command, answers: []pcAnswer{pcFixIt, pcPass}, beforeRun: func(r *pcRig) {
				r.o.testAfterRoundDecided = func() {
					once.Do(func() {
						writeQuestionFile(t, filepath.Join(r.dir, pcStageID), phaseAutonomous, pcLeftQuestion, nil)
						r.o.pollQuestions(map[string]bool{}, map[string]*malformedQuestionState{})
						if err := os.CopyFS(captured, os.DirFS(r.dir)); err != nil {
							t.Errorf("capture the journal: %v", err)
						}
						close(decided)
					})
				}
			}})
			select {
			case <-decided:
			case <-time.After(10 * time.Second):
				t.Fatal("the round was not decided")
			}
			first.stop()

			replayed, err := state.LoadRunState(captured)
			if err != nil {
				t.Fatal(err)
			}
			if st := replayed.Stages[pcStageID]; st.Status != state.StatusRunning || st.PreCompletion.Decision != state.PreCompletionNeedsChanges {
				t.Errorf("replayed gap: status %s, round %+v; want running with the correction owed", st.Status, st.PreCompletion)
			}
			reviewed := pcSessionID(t, captured)
			author := newPCScriptAuthor(t)
			var claims pcClaims
			second := newPCRig(t, pcRigOptions{dir: captured, freshCorrection: &fresh, interactive: true, authorCommand: author.command, answers: []pcAnswer{pcPass}, beforeRun: func(r *pcRig) {
				r.o.testAfterCorrectionClaim = claims.add
			}})
			author.awaitRuns(1)
			second.stop()
			if got, want := author.runs()[0], correctionResume(fresh, reviewed); got != want {
				t.Errorf("the restarted correction started with %s, want %s", got, want)
			}
			if got := claims.list(); !slices.Equal(got, []bool{true}) {
				t.Errorf("correction claims = %v, want the restart to start the owed correction", got)
			}
		})
	}
}

// The counterpart: a question the correction's own author asked survives a restart as that author's. The next process
// resumes the correction's session and does not prepare the correction again.
func TestPreCompletion_RestartResumesTheCorrectionsOwnQuestion(t *testing.T) {
	fresh := true
	captured := t.TempDir()
	var once sync.Once
	reviewed := make(chan string, 1)
	first := newPCRig(t, pcRigOptions{freshCorrection: &fresh, interactive: true, authorCommand: newPCScriptAuthor(t).command, answers: []pcAnswer{pcFixIt, pcPass}, beforeRun: func(r *pcRig) {
		r.o.testAfterRoundDecided = func() {
			once.Do(func() {
				reviewed <- pcSessionID(t, r.dir)
				if err := os.WriteFile(filepath.Join(r.dir, pcStageID, "ask-once"), nil, 0o644); err != nil {
					t.Errorf("ask-once: %v", err)
				}
			})
		}
	}})
	first.waitFor("the correction's question", func(st state.StageState) bool {
		return st.Status == state.StatusAwaitingUserInput && st.PreCompletion != nil && st.PreCompletion.Decision == state.PreCompletionNeedsChanges
	})
	first.awaitStageIdle()
	if err := os.CopyFS(captured, os.DirFS(first.dir)); err != nil {
		t.Fatal(err)
	}
	first.stop()
	own := pcSessionID(t, captured)
	if own == "" || own == <-reviewed {
		t.Fatalf("the correction's session = %q, want its own fresh one", own)
	}

	author := newPCScriptAuthor(t)
	var claims pcClaims
	second := newPCRig(t, pcRigOptions{dir: captured, freshCorrection: &fresh, interactive: true, authorCommand: author.command, answers: []pcAnswer{pcPass}, beforeRun: func(r *pcRig) {
		r.o.testAfterCorrectionClaim = claims.add
	}})
	author.awaitRuns(1)
	second.stop()
	if got, want := author.runs()[0], "resume="+own; got != want {
		t.Errorf("the restart started the correction's author with %s, want its own session (%s)", got, want)
	}
	if got := claims.list(); len(got) != 0 {
		t.Errorf("correction claims = %v, want none: the correction had started", got)
	}
}

// The correction's own author is the one who reads an answer: its question is asked while it runs, and its answer goes to
// that author instead of starting another turn.
func TestPreCompletion_CorrectionAuthorsQuestionIsAskedWhileItRuns(t *testing.T) {
	fresh := true
	author := newPCScriptAuthor(t)
	var once sync.Once
	r := newPCRig(t, pcRigOptions{freshCorrection: &fresh, interactive: true, authorCommand: author.command, answers: []pcAnswer{pcFixIt, pcPass}, beforeRun: func(r *pcRig) {
		r.o.testAfterRoundDecided = func() {
			once.Do(func() {
				if err := os.WriteFile(filepath.Join(r.dir, pcStageID, "ask-wait"), nil, 0o644); err != nil {
					t.Errorf("ask-wait: %v", err)
				}
			})
		}
	}})
	r.waitFor("the correction's question", func(st state.StageState) bool {
		return st.Status == state.StatusAwaitingUserInput && st.PreCompletion != nil && st.PreCompletion.Decision == state.PreCompletionNeedsChanges
	})
	stageDir := filepath.Join(r.dir, pcStageID)
	if err := mcp.WriteAnswer(stageDir, phaseAutonomous, "own", "this way", false, false); err != nil {
		t.Fatal(err)
	}
	if err := r.o.NotifyAnswer(pcStageID, phaseAutonomous, "own", "this way", false); err != nil {
		t.Fatalf("NotifyAnswer: %v", err)
	}
	r.waitStatus(state.StatusDone)
	if got := author.runs(); len(got) != 2 {
		t.Fatalf("author runs = %v, want the reviewed turn and the correction that read its answer", got)
	}
}

// ---- a turn left over from the correction's epoch ----

// A turn granted in the correction's epoch that gets the stage only after the correction handed it to the next round
// does not run the author during that round's review.
func TestPreCompletion_StaleTurnDoesNotRunTheAuthorDuringTheNextRound(t *testing.T) {
	r := newPCRig(t, pcRigOptions{holdHelper: true, holdHelperFrom: 2, answers: []pcAnswer{pcFixIt, pcPass}})
	r.waitFor("the second round", func(st state.StageState) bool {
		return st.Status == state.StatusAwaitingPreCompletion && st.PreCompletion.Round == 2
	})
	epoch := r.stage().Epoch
	stale := withTurnEpoch(r.o.runContext(context.Background()), epoch)
	r.o.spawnKind(stale, *r.o.reg().Stage(pcStageID), kindAutonomous, r.o.plainRunner(kindAutonomous))
	r.awaitStageIdle()
	if st := r.stage(); st.Status != state.StatusAwaitingPreCompletion || st.PreCompletion.Round != 2 || !st.PreCompletion.Open() || r.author.turns.Load() != 2 {
		t.Fatalf("after the stale turn: status %s, round %+v, author turns %d; want the second round under review and no author run",
			st.Status, st.PreCompletion, r.author.turns.Load())
	}
	r.releaseHelpers()
	r.waitStatus(state.StatusDone)
	if got, want := r.order(), "author:1,helper:Reviewer:1,author:2,helper:Reviewer:2,verify"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}
