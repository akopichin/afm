package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// summaryRun is one autonomous author loop of stage s1 over its real execution_summary.md probe. The agent
// script plays what the author does on each call; prompts keep the retry context every call was given.
type summaryRun struct {
	t       *testing.T
	o       *Orchestrator
	runDir  string
	dir     string // the stage directory
	stage   flow.Stage
	calls   int
	prompts []string
}

func newSummaryRun(t *testing.T) *summaryRun {
	t.Helper()
	o, runDir := setupHookOrch(t, "s1")
	withRetryBudgets(o)
	o.maxRetries, o.maxUnknownRetries = 5, 3
	o.jitterFrac = 0
	dir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &summaryRun{t: t, o: o, runDir: runDir, dir: dir, stage: flow.Stage{ID: "s1"}}
}

func (r *summaryRun) summaryPath() string { return filepath.Join(r.dir, stagefiles.SummaryFile) }

func (r *summaryRun) writeSummary() {
	r.t.Helper()
	if err := os.WriteFile(r.summaryPath(), []byte("## Summary\nDone.\n"), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *summaryRun) probe() error { return stagefiles.CheckAutonomousCompletion(r.dir) }

// run drives the loop; check nil is the plain summary probe.
func (r *summaryRun) run(agent func(call int) error, check func() error) {
	r.t.Helper()
	if check == nil {
		check = r.probe
	}
	r.o.runWithRetry(context.Background(), r.stage, phaseAutonomous, func(retryContext string) error {
		r.calls++
		r.prompts = append(r.prompts, retryContext)
		return agent(r.calls)
	}, check, func() { r.t.Error("onUserInterrupted should not be called") })
}

func (r *summaryRun) summary() *state.SummaryCorrections {
	return r.o.opts.Store.Snapshot().Stages["s1"].Summary
}

func (r *summaryRun) history() []state.Transition {
	r.t.Helper()
	h, err := r.o.opts.Store.History()
	if err != nil {
		r.t.Fatal(err)
	}
	return h
}

// failReason is the reason of the stage's fail transition.
func (r *summaryRun) failReason() string {
	for _, tr := range r.history() {
		if tr.Event == string(bus.EvFail) || tr.Event == string(bus.EvVerifyFail) {
			return tr.Reason
		}
	}
	return ""
}

type summaryNotice struct {
	Type    string            `json:"type"`
	StageID string            `json:"stage_id"`
	Data    map[string]string `json:"data"`
}

// notices returns the durable notices of the given types from notices.jsonl.
func (r *summaryRun) notices(types ...bus.EventType) []summaryNotice {
	r.t.Helper()
	return summaryNoticesIn(r.t, r.runDir, types...)
}

// summaryNoticesIn reads the durable notices of the given types from the notices.jsonl of runDir.
func summaryNoticesIn(t *testing.T, runDir string, types ...bus.EventType) []summaryNotice {
	t.Helper()
	f, err := os.Open(filepath.Join(runDir, "notices.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []summaryNotice
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var n summaryNotice
		if json.Unmarshal(sc.Bytes(), &n) != nil {
			continue // a notice with a non-string payload is not ours
		}
		for _, typ := range types {
			if n.Type == string(typ) {
				out = append(out, n)
			}
		}
	}
	return out
}

func (r *summaryRun) requireDone() {
	r.t.Helper()
	completeViaOnAgentCompleted(r.t, r.o, "s1")
	if got := r.o.opts.Store.Get("s1"); got != state.StatusDone {
		r.t.Fatalf("status = %v, want done", got)
	}
}

func (r *summaryRun) requireFailed() {
	r.t.Helper()
	if got := r.o.opts.Store.Get("s1"); got != state.StatusFailed {
		r.t.Fatalf("status = %v, want failed", got)
	}
}

func TestSummaryRecovery_TwoPostExitCorrectionsThenTheAuthorWritesIt(t *testing.T) {
	r := newSummaryRun(t)
	r.run(func(call int) error {
		if call == 3 {
			r.writeSummary()
		}
		return nil
	}, nil)

	if r.calls != 3 {
		t.Fatalf("author calls = %d, want the run and two corrections", r.calls)
	}
	r.requireDone()
	if got := r.summary(); got == nil || got.PostExit != 2 || got.Hook != 0 {
		t.Fatalf("summary = %+v, want two post-exit corrections", got)
	}
	if strings.Contains(r.prompts[0], "Summary correction") {
		t.Errorf("the first run got a correction:\n%s", r.prompts[0])
	}
	for i, want := range []string{"(1 of 2)", "(2 of 2)"} {
		got := r.prompts[i+1]
		if !strings.Contains(got, want) || !strings.Contains(got, r.summaryPath()) || !strings.Contains(got, "missing") {
			t.Errorf("correction %d lacks %q, the path or the reason:\n%s", i+1, want, got)
		}
	}
	if !strings.Contains(r.prompts[2], "last correction") {
		t.Errorf("the second correction does not say it is the last:\n%s", r.prompts[2])
	}

	var schedules, resumes int
	for _, tr := range r.history() {
		switch tr.Event {
		case string(bus.EvScheduleRetry):
			schedules++
			if tr.SummaryStep == nil || tr.SummaryStep.Source != state.SummarySourcePostExit || tr.SummaryStep.Reason != "missing" {
				t.Errorf("schedule_retry step = %+v, want a post-exit correction for a missing file", tr.SummaryStep)
			}
		case string(bus.EvResumeAfterRetry):
			resumes++
		default:
			// Other transitions do not carry a summary step.
		}
	}
	if schedules != 2 || resumes != 2 {
		t.Errorf("schedule/resume transitions = %d/%d, want 2/2", schedules, resumes)
	}
}

func TestSummaryRecovery_DurableNoticesOncePerEvent(t *testing.T) {
	r := newSummaryRun(t)
	r.run(func(call int) error {
		if call == 3 {
			r.writeSummary()
		}
		return nil
	}, nil)
	r.requireDone()

	started := r.notices(eventSummaryCorrectionStarted)
	if len(started) != 2 || started[0].Data["attempt"] != "1/2" || started[1].Data["attempt"] != "2/2" {
		t.Fatalf("start notices = %+v, want 1/2 and 2/2", started)
	}
	for _, n := range started {
		if n.StageID != "s1" || n.Data["path"] != r.summaryPath() || n.Data["reason"] != "missing" {
			t.Errorf("start notice = %+v, want stage, path and reason", n)
		}
	}
	if got := r.notices(eventSummaryRecovered); len(got) != 1 {
		t.Fatalf("recovered notices = %+v, want exactly one", got)
	}
	if got := r.notices(eventSummaryCorrectionExhausted); len(got) != 0 {
		t.Fatalf("exhausted notices = %+v, want none", got)
	}
}

func TestSummaryRecovery_ThirdMissingFailsWithPathAndReason(t *testing.T) {
	r := newSummaryRun(t)
	r.run(func(int) error { return nil }, nil)

	if r.calls != 3 {
		t.Fatalf("author calls = %d, want the run and exactly two corrections", r.calls)
	}
	r.requireFailed()
	reason := r.failReason()
	for _, want := range []string{r.summaryPath(), "missing", "2"} {
		if !strings.Contains(reason, want) {
			t.Errorf("fail reason %q lacks %q", reason, want)
		}
	}
	exhausted := r.notices(eventSummaryCorrectionExhausted)
	if len(exhausted) != 1 || exhausted[0].Data["path"] != r.summaryPath() || exhausted[0].Data["reason"] != "missing" {
		t.Fatalf("exhausted notices = %+v, want one with the path and the file reason", exhausted)
	}
	if got := r.notices(eventSummaryRecovered); len(got) != 0 {
		t.Fatalf("recovered notices = %+v, want none", got)
	}
}

func TestSummaryRecovery_EmptyFileIsCorrectedWithItsReason(t *testing.T) {
	r := newSummaryRun(t)
	r.run(func(call int) error {
		if call == 1 {
			return os.WriteFile(r.summaryPath(), []byte("  \n\t"), 0o644)
		}
		r.writeSummary()
		return nil
	}, nil)

	if r.calls != 2 {
		t.Fatalf("author calls = %d, want the run and one correction", r.calls)
	}
	r.requireDone()
	if !strings.Contains(r.prompts[1], "empty") {
		t.Errorf("the correction does not name the empty file:\n%s", r.prompts[1])
	}
	if got := r.summary(); got == nil || got.PostExit != 1 || got.Reason != "empty" {
		t.Fatalf("summary = %+v, want one correction for an empty file", got)
	}
}

// A transport retry spends none of the summary corrections, whatever the transport budget is.
func TestSummaryRecovery_TransportRetryBeforeMissingKeepsBothCorrections(t *testing.T) {
	for _, maxRetries := range []int{0, 1, 5} {
		t.Run("maxRetries="+string(rune('0'+maxRetries)), func(t *testing.T) {
			r := newSummaryRun(t)
			r.o.maxRetries = maxRetries
			wantCalls := 3
			failFirst := maxRetries > 0
			if failFirst {
				wantCalls = 4
			}
			r.run(func(call int) error {
				if failFirst && call == 1 {
					return errors.New("read: connection reset by peer")
				}
				if call == wantCalls {
					r.writeSummary()
				}
				return nil
			}, nil)

			if r.calls != wantCalls {
				t.Fatalf("author calls = %d, want %d", r.calls, wantCalls)
			}
			r.requireDone()
			if got := r.summary(); got == nil || got.PostExit != 2 {
				t.Fatalf("summary = %+v, want both corrections available after the transport retry", got)
			}
		})
	}
}

func TestSummaryRecovery_VerifyRejectThenMissingStillGetsBothCorrections(t *testing.T) {
	r := newSummaryRun(t)
	rejected := false
	check := func() error {
		if err := r.probe(); err != nil {
			return err
		}
		if !rejected {
			rejected = true
			return newVerifyRejected(1)
		}
		return nil
	}
	r.run(func(call int) error {
		switch call {
		case 1, 4: // verify rejects the first one; the fourth call finishes the run
			r.writeSummary()
		case 2:
			return os.Remove(r.summaryPath()) // the correction lost the file
		default:
			// The third call leaves the missing file as it is.
		}
		return nil
	}, check)

	if r.calls != 4 {
		t.Fatalf("author calls = %d, want the run, a verify correction and two summary corrections", r.calls)
	}
	r.requireDone()
	if got := r.summary(); got == nil || got.PostExit != 2 {
		t.Fatalf("summary = %+v, want the two corrections that the verify one did not spend", got)
	}
}

func TestSummaryRecovery_UnreadableSummaryFailsWithTheFileError(t *testing.T) {
	r := newSummaryRun(t)
	r.run(func(int) error { return os.Mkdir(r.summaryPath(), 0o755) }, nil)

	if r.calls != 1 {
		t.Fatalf("author calls = %d, want no model retry for a file the author cannot repair", r.calls)
	}
	r.requireFailed()
	if reason := r.failReason(); !strings.Contains(reason, "cannot read") || !strings.Contains(reason, r.summaryPath()) {
		t.Errorf("fail reason %q, want the exact file error", reason)
	}
	if got := r.summary(); got != nil {
		t.Errorf("summary = %+v, want no correction spent", got)
	}
}

func TestSummaryRecovery_MissingArtifactIsNotASummaryRepair(t *testing.T) {
	r := newSummaryRun(t)
	r.run(func(int) error { return nil }, func() error { return &stagefiles.MissingArtifactError{Name: "report"} })

	if r.calls != 1 {
		t.Fatalf("author calls = %d, want the missing artifact to fail at once", r.calls)
	}
	r.requireFailed()
	if got := r.summary(); got != nil {
		t.Errorf("summary = %+v, want no correction spent", got)
	}
}

// .done keeps its retry: one incomplete retry at the first attempt only, unreadable included.
func TestSummaryRecovery_DoneMarkerKeepsItsRetryBehaviour(t *testing.T) {
	for _, tc := range []struct {
		name      string
		firstCall error
		prepare   func(done string) error
		wantCalls int
	}{
		{"missing at the first attempt is retried once", nil, nil, 2},
		{"missing after a transport retry fails", errors.New("connection reset by peer"), nil, 2},
		{"unreadable at the first attempt is retried once", nil, func(done string) error { return os.Mkdir(done, 0o755) }, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSummaryRun(t)
			done := filepath.Join(r.dir, ".done")
			calls := 0
			r.o.runWithRetry(context.Background(), r.stage, phaseImplementation, func(string) error {
				calls++
				if calls > 1 {
					return nil
				}
				if tc.prepare != nil {
					return tc.prepare(done)
				}
				return tc.firstCall
			}, func() error { return stagefiles.CheckCompletion(r.dir, r.dir, r.stage) },
				func() { t.Error("onUserInterrupted should not be called") })

			if calls != tc.wantCalls {
				t.Fatalf("author calls = %d, want %d", calls, tc.wantCalls)
			}
			r.requireFailed()
			if got := r.summary(); got != nil {
				t.Errorf("summary = %+v, want .done to spend no summary correction", got)
			}
		})
	}
}

// reserveCorrections makes the journal look as if n post-exit corrections were reserved before a restart and the
// stage then went through its recovery to a fresh running turn.
func reserveCorrections(t *testing.T, r *summaryRun, n int) {
	t.Helper()
	store := r.o.opts.Store
	step := &state.SummaryStep{Source: state.SummarySourcePostExit, Reason: "missing"}
	for range n {
		for _, tr := range []state.Transition{
			{From: state.StatusRunning, To: state.StatusRetrying, Event: string(bus.EvScheduleRetry), SummaryStep: step},
			{From: state.StatusRetrying, To: state.StatusReady, Event: string(bus.EvReady)},
			{From: state.StatusReady, To: state.StatusRunning, Event: string(bus.EvStartRun)},
		} {
			tr.StageID = "s1"
			if err := store.Apply(&tr); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// markIssued records, as the retry loop does before the author starts, that the latest post-exit correction was
// launched.
func markIssued(t *testing.T, r *summaryRun) {
	t.Helper()
	step := &state.SummaryStep{Source: state.SummarySourcePostExitIssued}
	if err := r.o.opts.Store.Apply(&state.Transition{
		StageID: "s1", From: state.StatusRunning, To: state.StatusRunning,
		Event: string(bus.EvReserveSummaryHook), SummaryStep: step,
	}); err != nil {
		t.Fatal(err)
	}
}

// runUntilCrash runs the loop with an author whose call panics: AFM dying while the author runs.
func (r *summaryRun) runUntilCrash() {
	r.t.Helper()
	defer func() {
		if recover() == nil {
			r.t.Fatal("the loop returned, want it to be cut short by the crash")
		}
	}()
	r.run(func(int) error { panic("afm crashed") }, nil)
}

// The outcome of a launched correction is unknown after a crash, so a restart does not run the same one again:
// it weighs the file and spends what is left of the budget.
func TestSummaryRecovery_RestartAfterIssuedCorrectionSpendsTheRemainder(t *testing.T) {
	r := newSummaryRun(t)
	reserveCorrections(t, r, 1)
	markIssued(t, r)

	r.run(func(int) error { return nil }, nil)

	if r.calls != 1 {
		t.Fatalf("author calls after the restart = %d, want only the second correction", r.calls)
	}
	if !strings.Contains(r.prompts[0], "(2 of 2)") || strings.Contains(r.prompts[0], "(1 of 2)") {
		t.Errorf("the restarted turn repeats the issued correction:\n%s", r.prompts[0])
	}
	r.requireFailed()
	if got := r.summary(); got == nil || got.PostExit != 2 {
		t.Fatalf("summary = %+v, want the budget of two exactly spent", got)
	}
	if got := r.notices(eventSummaryCorrectionStarted); len(got) != 1 || got[0].Data["attempt"] != "2/2" {
		t.Errorf("start notices = %+v, want the one for the second correction", got)
	}
}

func TestSummaryRecovery_RestartAfterIssuedLastCorrectionIsExhausted(t *testing.T) {
	r := newSummaryRun(t)
	reserveCorrections(t, r, 2)
	markIssued(t, r)

	r.run(func(int) error { return nil }, nil)

	if r.calls != 0 {
		t.Fatalf("author calls after the restart = %d, want none: the budget is spent", r.calls)
	}
	r.requireFailed()
	if got := r.notices(eventSummaryCorrectionExhausted); len(got) != 1 {
		t.Errorf("exhausted notices = %+v, want one", got)
	}
}

// The summary the issued correction wrote before the crash is no correction target: the author runs as for any
// restart with the file in place, and the stage completes.
func TestSummaryRecovery_RestartAfterIssuedCorrectionFindsTheSummary(t *testing.T) {
	r := newSummaryRun(t)
	reserveCorrections(t, r, 2)
	markIssued(t, r)
	r.writeSummary()

	r.run(func(int) error { return nil }, nil)

	if r.calls != 1 || strings.Contains(r.prompts[0], "Summary correction") {
		t.Fatalf("author calls = %d prompt:\n%s\nwant one ordinary turn without a correction", r.calls, r.prompts[0])
	}
	r.requireDone()
	if got := r.notices(eventSummaryRecovered); len(got) != 1 {
		t.Errorf("recovered notices = %+v, want one", got)
	}
}

// Crashes inside the corrections, restart after restart: every launched correction is spent once, the loop ends
// in the exhaustion path, and each reservation is announced once.
func TestSummaryRecovery_RepeatedCrashesInsideCorrectionsAreBounded(t *testing.T) {
	r := newSummaryRun(t)
	reserveCorrections(t, r, 1)

	r.runUntilCrash() // the first correction starts and the process dies
	r.runUntilCrash() // its outcome is unknown: the second one starts and dies too
	crashedCalls := r.calls
	r.run(func(int) error { return nil }, nil)

	if crashedCalls != 2 || r.calls != 2 {
		t.Fatalf("author calls = %d (%d before the last restart), want one per correction and none after", r.calls, crashedCalls)
	}
	r.requireFailed()
	if got := r.notices(eventSummaryCorrectionStarted); len(got) != 2 {
		t.Errorf("start notices = %+v, want one per reservation", got)
	}
	if got := r.notices(eventSummaryCorrectionExhausted); len(got) != 1 {
		t.Errorf("exhausted notices = %+v, want one", got)
	}
}

// A transport failure inside a post-exit correction retries that same correction: it carries the instruction
// again, charges no second one and is announced once.
func TestSummaryRecovery_TransportFailureInsideACorrectionRepeatsItsInstruction(t *testing.T) {
	r := newSummaryRun(t)
	r.run(func(call int) error {
		switch call {
		case 2:
			return errors.New("read: connection reset by peer")
		case 4:
			r.writeSummary()
		default:
			// The remaining calls end without a summary.
		}
		return nil
	}, nil)

	if r.calls != 4 {
		t.Fatalf("author calls = %d, want the run, the correction, its retry and the second correction", r.calls)
	}
	r.requireDone()
	for call, want := range []string{"", "(1 of 2)", "(1 of 2)", "(2 of 2)"} {
		if want != "" && !strings.Contains(r.prompts[call], want) {
			t.Errorf("call %d lacks %q:\n%s", call+1, want, r.prompts[call])
		}
	}
	if got := r.summary(); got == nil || got.PostExit != 2 {
		t.Fatalf("summary = %+v, want exactly two corrections charged", got)
	}
	started := r.notices(eventSummaryCorrectionStarted)
	if len(started) != 2 || started[0].Data["attempt"] != "1/2" || started[1].Data["attempt"] != "2/2" {
		t.Errorf("start notices = %+v, want one per reservation, the retry not announced again", started)
	}
}

// A crash between the reservation and the spawn leaves the correction spent: the restarted turn is that
// correction, with its instruction, and not a free extra one.
func TestSummaryRecovery_RestartAfterReservationKeepsTheSpentCorrection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reserved  int
		wantCalls int
		wantFirst string
	}{
		{"one reserved", 1, 2, "(1 of 2)"},
		{"both reserved", 2, 1, "(2 of 2)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSummaryRun(t)
			reserveCorrections(t, r, tc.reserved)

			r.run(func(int) error { return nil }, nil)

			if r.calls != tc.wantCalls {
				t.Fatalf("author calls after the restart = %d, want %d", r.calls, tc.wantCalls)
			}
			if !strings.Contains(r.prompts[0], tc.wantFirst) || !strings.Contains(r.prompts[0], r.summaryPath()) {
				t.Errorf("the restarted turn lacks its correction %s:\n%s", tc.wantFirst, r.prompts[0])
			}
			r.requireFailed()
			if got := r.summary(); got == nil || got.PostExit != 2 {
				t.Fatalf("summary = %+v, want the budget of two exactly spent", got)
			}
			// The notices of corrections reserved before the restart are already in the journal of notices.
			if got := r.notices(eventSummaryCorrectionStarted); len(got) != tc.wantCalls {
				t.Errorf("start notices = %+v, want one per correction that started after the restart", got)
			}
		})
	}
}

// A restart whose summary is already there starts no correction and announces nothing it did not do.
func TestSummaryRecovery_RestartWithTheSummaryPresentIsNotACorrection(t *testing.T) {
	r := newSummaryRun(t)
	reserveCorrections(t, r, 1)
	r.writeSummary()

	r.run(func(int) error { return nil }, nil)

	if strings.Contains(r.prompts[0], "Summary correction") {
		t.Errorf("a turn with the summary in place got a correction:\n%s", r.prompts[0])
	}
	if got := r.notices(eventSummaryCorrectionStarted); len(got) != 0 {
		t.Errorf("start notices = %+v, want none", got)
	}
	r.requireDone()
	if got := r.notices(eventSummaryRecovered); len(got) != 1 {
		t.Errorf("recovered notices = %+v, want the one for the reserved correction", got)
	}
}

// Only an explicit Retry or Revise starts a new cycle with the full budget.
func TestSummaryRecovery_ManualRetryStartsANewCycle(t *testing.T) {
	r := newSummaryRun(t)
	reserveCorrections(t, r, 2)
	for _, tr := range []state.Transition{
		{From: state.StatusRunning, To: state.StatusFailed, Event: string(bus.EvFail)},
		{From: state.StatusFailed, To: state.StatusPending, Event: string(bus.EvManualRetry)},
		{From: state.StatusPending, To: state.StatusReady, Event: string(bus.EvReady)},
		{From: state.StatusReady, To: state.StatusRunning, Event: string(bus.EvStartRun)},
	} {
		tr.StageID = "s1"
		if err := r.o.opts.Store.Apply(&tr); err != nil {
			t.Fatal(err)
		}
	}

	r.run(func(int) error { return nil }, nil)

	if r.calls != 3 {
		t.Fatalf("author calls = %d, want the run and two fresh corrections", r.calls)
	}
}

// The marker appears while the correction is being reserved: no author run is wasted on it, the usual checks go on.
func TestSummaryRecovery_MarkerAppearingDuringReservationSkipsTheRun(t *testing.T) {
	r := newSummaryRun(t)
	checks := 0
	r.run(func(int) error { return nil }, func() error {
		checks++
		err := r.probe()
		if err != nil && checks == 1 {
			// A background process writes the file right after the probe failed, before the correction starts.
			r.writeSummary()
		}
		return err
	})

	if r.calls != 1 {
		t.Fatalf("author calls = %d, want no correction run once the file is there", r.calls)
	}
	r.requireDone()
	if got := r.notices(eventSummaryRecovered); len(got) != 1 {
		t.Errorf("recovered notices = %+v, want one", got)
	}
}

// The turn yields on a wait that ended before the process exited: the stage continues as for a peer, and the
// missing summary is neither probed nor spent.
func TestSummaryRecovery_ResolvedYieldSpendsNoCorrection(t *testing.T) {
	f := newExitFixture(t)
	dir := filepath.Join(f.o.opts.RunDir, assistantsTestStageID)
	checked := false

	f.o.runWithRetry(context.Background(), flow.Stage{ID: assistantsTestStageID}, phaseAutonomous, func(string) error {
		_, wait := f.askUser(t, "op-ask")
		return f.o.CancelWait(context.Background(), CancelWaitRequest{
			OperationID: "op-cancel", WaitID: wait, Reason: "no longer needed", Actor: state.Actor{Kind: "user"},
		})
	}, func() error {
		checked = true
		return stagefiles.CheckAutonomousCompletion(dir)
	}, func() { t.Error("onUserInterrupted should not be called") })

	if checked {
		t.Fatal("the completion check ran for a turn that yielded")
	}
	if got := f.store.Snapshot().Stages[assistantsTestStageID].Summary; got != nil {
		t.Fatalf("summary = %+v, want no correction spent on a yield", got)
	}
	if got := f.o.currentStatus(assistantsTestStageID); got == state.StatusFailed {
		t.Fatalf("status = %s, want the yield to continue the stage", got)
	}
}

// A terminal CLI error does not hide a finished summary: the usual checks run and the error is announced once.
func TestSummaryRecovery_TerminalErrorWithFreshSummaryGoesToTheUsualChecks(t *testing.T) {
	r := newSummaryRun(t)
	terminal := &executor.TerminalResultError{Subtype: "error_during_execution", APIErrorStatus: 529, Message: "secret-looking text"}
	r.run(func(int) error {
		r.writeSummary()
		return terminal
	}, nil)

	if r.calls != 1 {
		t.Fatalf("author calls = %d, want the summary to stand", r.calls)
	}
	r.requireDone()
	got := r.notices(eventAgentResultError)
	if len(got) != 1 || got[0].Data["cli_outcome"] == "" {
		t.Fatalf("agent error notices = %+v, want exactly one naming the CLI outcome", got)
	}
	if strings.Contains(got[0].Data["message"], "secret-looking text") {
		t.Errorf("the notice repeats the CLI's raw message: %+v", got[0])
	}
}

// Without a finished summary the CLI error stays the primary cause and goes through the usual classification.
func TestSummaryRecovery_TerminalErrorWithoutSummaryKeepsItsClassification(t *testing.T) {
	r := newSummaryRun(t)
	r.run(func(int) error {
		return &executor.TerminalResultError{Subtype: "error_during_execution", Message: "boom"}
	}, nil)

	if r.calls != 1+r.o.maxUnknownRetries {
		t.Fatalf("author calls = %d, want the bounded unknown-error budget", r.calls)
	}
	r.requireFailed()
	if got := r.summary(); got != nil {
		t.Errorf("summary = %+v, want a CLI error not to spend a summary correction", got)
	}
}

// A stale summary of an earlier turn is not proof that this failed turn finished its work.
func TestSummaryRecovery_TerminalErrorWithStaleSummaryIsNotIgnored(t *testing.T) {
	r := newSummaryRun(t)
	r.writeSummary()
	r.run(func(int) error {
		return &executor.TerminalResultError{Subtype: "error_max_turns"}
	}, nil)

	if r.calls <= 1 {
		t.Fatalf("author calls = %d, want the error of a turn that wrote nothing to be retried", r.calls)
	}
	if got := r.notices(eventAgentResultError); len(got) != 0 {
		t.Fatalf("agent error notices = %+v, want none: the summary was not written by this turn", got)
	}
}

// When the corrections run out, the notice also names the CLI outcome that preceded.
func TestSummaryRecovery_ExhaustedNoticeNamesTheClassifiedCLIOutcome(t *testing.T) {
	r := newSummaryRun(t)
	r.run(func(call int) error {
		if call == 1 {
			return &executor.TerminalResultError{Subtype: "error_during_execution", APIErrorStatus: 500}
		}
		return nil
	}, nil)

	r.requireFailed()
	exhausted := r.notices(eventSummaryCorrectionExhausted)
	if len(exhausted) != 1 || !strings.Contains(exhausted[0].Data["cli_outcome"], "error_during_execution") {
		t.Fatalf("exhausted notices = %+v, want the CLI outcome named", exhausted)
	}
	if reason := r.failReason(); !strings.Contains(reason, "error_during_execution") {
		t.Errorf("fail reason %q lacks the CLI outcome", reason)
	}
}

// A Pause that lands after the probe failed takes the turn's stage: no correction is reserved or started.
func TestSummaryRecovery_PauseAfterTheProbeSpendsNoCorrection(t *testing.T) {
	r := newSummaryRun(t)
	r.run(func(int) error { return nil }, func() error {
		err := r.probe()
		if _, ok := r.o.Trigger("s1", bus.EvPause, bus.GuardCtx{}, "manual pause"); !ok {
			t.Error("EvPause failed")
		}
		return err
	})

	if r.calls != 1 {
		t.Fatalf("author calls = %d, want none after the pause", r.calls)
	}
	if got := r.o.opts.Store.Get("s1"); got != state.StatusPaused {
		t.Fatalf("status = %v, want paused", got)
	}
	if got := r.summary(); got != nil {
		t.Fatalf("summary = %+v, want no correction spent by a stale turn", got)
	}
}

// The reservation is fenced by the epoch of the turn: a turn that lost the stage between its checks and the
// commit reserves nothing, even when its probe still says the file is missing.
func TestSummaryRecovery_StaleTurnReservesNothing(t *testing.T) {
	r := newSummaryRun(t)
	stale := r.o.stageEpoch("s1")
	if _, ok := r.o.Trigger("s1", bus.EvPause, bus.GuardCtx{}, "manual pause"); !ok {
		t.Fatal("EvPause failed")
	}
	if _, ok := r.o.Trigger("s1", bus.EvContinue, bus.GuardCtx{PausedFrom: state.StatusRunning}, ""); !ok {
		t.Fatal("EvContinue failed")
	}

	verdict, instruction := r.o.decideSummaryFailure(r.stage, phaseAutonomous, r.dir, stale, r.probe(), "", func() {})

	if verdict != summarySettled || instruction != "" {
		t.Fatalf("verdict = %v instruction = %q, want the stale turn settled without a correction", verdict, instruction)
	}
	if got := r.summary(); got != nil {
		t.Fatalf("summary = %+v, want none", got)
	}
	if got := r.o.opts.Store.Get("s1"); got != state.StatusRunning {
		t.Fatalf("status = %v, want the continued stage untouched", got)
	}
}

// wake is the transition by which a parked stage returns to running when what it waited for is over.
func (r *summaryRun) wake(from state.StageStatus, event bus.FSMEvent) {
	r.t.Helper()
	if err := r.o.opts.Store.Apply(&state.Transition{StageID: "s1", From: from, To: state.StatusRunning, Event: string(event)}); err != nil {
		r.t.Fatal(err)
	}
}

// usageLimit is a provider refusal. The park arms a wake timer: it needs the concurrency manager, and a clock that
// does not run by itself, so that the test, not the timer, wakes the stage.
func (r *summaryRun) usageLimit() error {
	if r.o.concurrency == nil {
		r.o.concurrency = concurrency.NewWithSemaphores(r.o.critical, map[string]concurrency.Semaphore{}, "")
		r.o.clock = clock.NewFake(time.Unix(0, 0))
		ctx, cancel := context.WithCancel(context.Background())
		r.o.runCtx = ctx
		r.t.Cleanup(cancel)
	}
	return &executor.UsageLimitError{
		Provider: "claude", ResetAt: r.o.clk().Now().Add(time.Hour), Window: "five_hour", Message: "usage limit",
		Source: executor.UsageLimitSourceStructured,
	}
}

// requireSameCorrectionRun checks that every given call carried the first correction's instruction, that no
// second correction was charged and that the correction was announced once.
func (r *summaryRun) requireSameCorrectionRun(calls ...int) {
	r.t.Helper()
	for _, call := range calls {
		if got := r.prompts[call-1]; !strings.Contains(got, "(1 of 2)") || strings.Contains(got, "(2 of 2)") {
			r.t.Errorf("call %d is not the first correction:\n%s", call, got)
		}
	}
	if got := r.summary(); got == nil || got.PostExit != 1 {
		r.t.Errorf("summary = %+v, want exactly one correction charged", got)
	}
	if got := r.notices(eventSummaryCorrectionStarted); len(got) != 1 || got[0].Data["attempt"] != "1/2" {
		r.t.Errorf("start notices = %+v, want the first correction announced once", got)
	}
}

// A usage limit inside a post-exit correction is a known park: the wake runs the same correction, it does not
// look like a crash and does not spend the next one.
func TestSummaryRecovery_QuotaParkInsideACorrectionResumesTheSameCorrection(t *testing.T) {
	r := newSummaryRun(t)
	r.run(func(call int) error {
		if call == 2 {
			return r.usageLimit()
		}
		return nil
	}, nil)
	if got := r.o.opts.Store.Get("s1"); got != state.StatusAwaitingQuota {
		t.Fatalf("status = %v, want awaiting_quota", got)
	}

	r.wake(state.StatusAwaitingQuota, bus.EvQuotaReset)
	r.run(func(call int) error {
		r.writeSummary()
		return nil
	}, nil)

	if r.calls != 3 {
		t.Fatalf("author calls = %d, want the run, the parked correction and its resumption", r.calls)
	}
	r.requireDone()
	r.requireSameCorrectionRun(2, 3)
}

// Denials in a row park and wake the same correction every time: only a correction turn that ended spends the
// next one.
func TestSummaryRecovery_RepeatedQuotaDenialsDoNotExhaustTheCorrections(t *testing.T) {
	r := newSummaryRun(t)
	refuse := func(call int) error {
		if call <= 3 && call >= 2 {
			return r.usageLimit()
		}
		if call == 4 {
			r.writeSummary()
		}
		return nil
	}
	r.run(refuse, nil)
	r.wake(state.StatusAwaitingQuota, bus.EvQuotaReset)
	r.run(refuse, nil)
	r.wake(state.StatusAwaitingQuota, bus.EvQuotaReset)
	r.run(refuse, nil)

	if r.calls != 4 {
		t.Fatalf("author calls = %d, want the run, two denied corrections and the one that passed", r.calls)
	}
	r.requireDone()
	r.requireSameCorrectionRun(2, 3, 4)
}

// The author asks a question inside a correction and leaves: the answer continues that correction.
func TestSummaryRecovery_AnsweredQuestionInsideACorrectionResumesTheSameCorrection(t *testing.T) {
	r := newSummaryRun(t)
	r.run(func(call int) error {
		if call == 2 {
			return os.WriteFile(filepath.Join(r.dir, "autonomous_execution.q1.question.json"), []byte(`{"id":"q1","question":"which one?"}`), 0o644)
		}
		return nil
	}, nil)
	if got := r.o.opts.Store.Get("s1"); got != state.StatusAwaitingUserInput {
		t.Fatalf("status = %v, want awaiting_user_input", got)
	}

	if err := os.WriteFile(filepath.Join(r.dir, "autonomous_execution.q1.answer.json"), []byte(`{"answer":"this"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r.wake(state.StatusAwaitingUserInput, bus.EvUserAnswered)
	r.run(func(call int) error {
		r.writeSummary()
		return nil
	}, nil)

	if r.calls != 3 {
		t.Fatalf("author calls = %d, want the run, the correction with the question and its resumption", r.calls)
	}
	r.requireDone()
	r.requireSameCorrectionRun(2, 3)
}

// A crash inside the resumed correction is an unknown outcome again: the restart does not repeat it and the
// budget stays bounded.
func TestSummaryRecovery_CrashAfterAQuotaWakeIsBounded(t *testing.T) {
	r := newSummaryRun(t)
	r.run(func(call int) error {
		if call == 2 {
			return r.usageLimit()
		}
		return nil
	}, nil)
	r.wake(state.StatusAwaitingQuota, bus.EvQuotaReset)
	r.runUntilCrash() // the woken correction starts and the process dies

	r.run(func(int) error { return nil }, nil)

	if r.calls != 4 {
		t.Fatalf("author calls = %d, want the run, the parked correction, the crashed one and the second correction", r.calls)
	}
	if got := r.prompts[3]; !strings.Contains(got, "(2 of 2)") {
		t.Errorf("the restart repeats the crashed correction:\n%s", got)
	}
	r.requireFailed()
	if got := r.summary(); got == nil || got.PostExit != 2 {
		t.Fatalf("summary = %+v, want the budget of two exactly spent", got)
	}
}

// A Stop hook reminder inside a post-exit correction does not rename the correction: a transport retry of it
// is not announced a second time, and the reminder is announced once.
func TestSummaryRecovery_HookReminderThenTransportRetryAnnouncesTheCorrectionOnce(t *testing.T) {
	r := newSummaryRun(t)
	r.run(func(call int) error {
		switch call {
		case 2:
			if !r.o.reserveSummaryHook("s1", r.o.stageEpoch("s1"), stagefiles.MarkerMissing) {
				t.Error("the hook reminder was not reserved")
			}
			r.o.noteSummaryHookReminder("s1", &stagefiles.MarkerFailure{Problem: stagefiles.MarkerMissing, Path: r.summaryPath()})
			return errors.New("read: connection reset by peer")
		case 3:
			r.writeSummary()
		default:
			// The first call ends without a summary.
		}
		return nil
	}, nil)

	r.requireDone()
	r.requireSameCorrectionRun(2, 3)
	if got := r.notices(eventSummaryHookReminder); len(got) != 1 {
		t.Errorf("hook reminder notices = %+v, want one", got)
	}
}

// The author yields on a colleague inside a correction: the reply continues that correction.
func TestSummaryRecovery_PeerYieldInsideACorrectionResumesTheSameCorrection(t *testing.T) {
	f := newExitFixture(t)
	stage := flow.Stage{ID: assistantsTestStageID}
	dir := filepath.Join(f.o.opts.RunDir, assistantsTestStageID)
	var prompts []string
	run := func(author func(call int)) {
		f.o.runWithRetry(context.Background(), stage, phaseAutonomous, func(retryContext string) error {
			prompts = append(prompts, retryContext)
			author(len(prompts))
			return nil
		}, func() error { return stagefiles.CheckAutonomousCompletion(dir) }, func() { t.Error("onUserInterrupted should not be called") })
	}

	var wait agentcomm.WaitID
	run(func(call int) {
		if call == 2 {
			_, wait = f.askUser(t, "op-ask")
		}
	})
	if got := f.o.currentStatus(assistantsTestStageID); got != state.StatusAwaitingPeer {
		t.Fatalf("status = %v, want awaiting_peer", got)
	}

	if err := f.o.CancelWait(context.Background(), CancelWaitRequest{
		OperationID: "op-cancel", WaitID: wait, Reason: "answered elsewhere", Actor: state.Actor{Kind: "user"},
	}); err != nil {
		t.Fatalf("CancelWait: %v", err)
	}
	if err := f.store.Apply(&state.Transition{
		StageID: assistantsTestStageID, From: state.StatusAwaitingPeer, To: state.StatusRunning, Event: string(bus.EvPeerWaitEnded),
	}); err != nil {
		t.Fatal(err)
	}
	run(func(int) {
		if err := os.WriteFile(filepath.Join(dir, stagefiles.SummaryFile), []byte("## Summary\nDone.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})

	if len(prompts) != 3 {
		t.Fatalf("author calls = %d, want the run, the correction with the yield and its resumption", len(prompts))
	}
	for _, call := range []int{2, 3} {
		if got := prompts[call-1]; !strings.Contains(got, "(1 of 2)") || strings.Contains(got, "(2 of 2)") {
			t.Errorf("call %d is not the first correction:\n%s", call, got)
		}
	}
	if got := f.store.Snapshot().Stages[assistantsTestStageID].Summary; got == nil || got.PostExit != 1 {
		t.Errorf("summary = %+v, want exactly one correction charged", got)
	}
	if got := f.o.currentStatus(assistantsTestStageID); got == state.StatusFailed {
		t.Errorf("status = %v, want the stage not to fail", got)
	}
}

// seedStepsOf turns the journal of one stage into the steps a restart test seeds.
func seedStepsOf(history []state.Transition, stageID string) []seedStep {
	var steps []seedStep
	for _, tr := range history {
		if tr.StageID == stageID {
			steps = append(steps, seedStep{to: tr.To, event: bus.FSMEvent(tr.Event), summary: tr.SummaryStep, quota: tr.Quota})
		}
	}
	return steps
}

// A crash during the backoff of a transport retry inside a correction leaves the journal exactly as the loop wrote it
// up to there. The restart continues that same correction: the transport failure charged nothing.
func TestSummaryRecovery_CrashDuringTransportBackoffRestartsTheSameCorrection(t *testing.T) {
	r := newSummaryRun(t)
	r.o.clock = clock.NewFake(time.Unix(0, 0)) // the backoff never ends: the process dies inside it
	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		calls := 0
		r.o.runWithRetry(ctx, r.stage, phaseAutonomous, func(string) error {
			calls++
			if calls == 2 {
				return errors.New("read: connection reset by peer")
			}
			return nil
		}, r.probe, func() {})
	}()
	t.Cleanup(func() {
		cancel()
		<-loopDone
	})
	wantEvents := []string{"test_setup", "schedule_retry", "resume_after_retry", "reserve_summary_hook", "schedule_retry"}
	waitUntilFor(t, 10*time.Second, "the transport retry of the correction to be scheduled", func() bool {
		history, err := r.o.opts.Store.History()
		return err == nil && len(history) == len(wantEvents)
	})
	history := r.history()
	for i, want := range wantEvents {
		if history[i].Event != want {
			t.Fatalf("the loop wrote %v, want %v", seedStepsOf(history, "s1"), wantEvents)
		}
	}

	runRestartRow(t, restartRow{
		name: "crash during the transport backoff", journal: seedStepsOf(history, "s1"), wantTurn: firstCorrect, wantPostExit: 1,
	})
}

// Pause during the backoff of a correction's retry and Continue: the correction goes on from retrying, with its own
// instruction and without a second charge.
func TestSummaryRecovery_PauseDuringCorrectionBackoffThenContinueRunsTheSameCorrection(t *testing.T) {
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{restartStage, restartAfter})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stages := seedRestartRun(t, store, runDir, restartRow{})
	author := &restartAuthor{store: store, turn: func(n int) (bool, error) {
		switch n {
		case 1:
			return false, nil // the author exits without a summary: the first correction
		case 2:
			return false, errors.New("read: connection reset by peer") // the transport fails inside the correction
		}
		return true, nil
	}}
	o := startRestartRun(t, store, runDir, stages, author, func(o *Orchestrator) {
		o.retryBackoff, o.retryBackoffMax = time.Hour, time.Hour
	})

	retries := func() int {
		history, err := store.History()
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, tr := range history {
			if tr.Event == string(bus.EvScheduleRetry) {
				n++
			}
		}
		return n
	}
	waitUntilFor(t, 10*time.Second, "the correction to be in its backoff", func() bool {
		return retries() == 2 && store.Get(restartStage) == state.StatusRetrying
	})
	if err := o.Pause(context.Background(), restartStage); err != nil {
		t.Fatal(err)
	}
	waitUntilFor(t, 10*time.Second, "the stage to be paused", func() bool { return store.Get(restartStage) == state.StatusPaused })
	if err := o.Continue(context.Background(), restartStage); err != nil {
		t.Fatal(err)
	}
	waitRestartDone(t, store)

	author.mu.Lock()
	defer author.mu.Unlock()
	if len(author.prompts) != 3 {
		t.Fatalf("author turns = %d, want the run, the correction, and its continuation", len(author.prompts))
	}
	requireCorrectionPrompt(t, author.prompts[1], firstCorrect)
	requireCorrectionPrompt(t, author.prompts[2], firstCorrect)
	if got := store.Snapshot().Stages[restartStage].Summary; got == nil || got.PostExit != 1 {
		t.Errorf("summary = %+v, want exactly one correction charged", got)
	}
	if got := summaryNoticesIn(t, runDir, eventSummaryCorrectionStarted); len(got) != 1 {
		t.Errorf("correction start notices = %+v, want one", got)
	}
}
