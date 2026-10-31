package executor

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/agentjobs"
	"github.com/akopichin/afm/pkg/clock"
)

// memorySink records every published job fact in order.
type memorySink struct {
	mu   sync.Mutex
	recs []agentjobs.Record
	err  error // returned from every Append, to prove a failing journal never fails a turn
}

func (s *memorySink) Append(r agentjobs.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, r)
	return s.err
}

func (s *memorySink) records() []agentjobs.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agentjobs.Record(nil), s.recs...)
}

func (s *memorySink) types() []agentjobs.RecordType {
	var out []agentjobs.RecordType
	for _, r := range s.records() {
		out = append(out, r.Type)
	}
	return out
}

// jobTurn is a whole Run* call seen from outside: what was published, which usage was reported.
type jobTurn struct {
	sink   *memorySink
	usages []accounting.Observation
}

func (r *jobTurn) started(t *testing.T) agentjobs.Record {
	t.Helper()
	return r.only(t, agentjobs.TypeStarted)
}

func (r *jobTurn) finished(t *testing.T) agentjobs.Record {
	t.Helper()
	return r.only(t, agentjobs.TypeFinished)
}

func (r *jobTurn) only(t *testing.T, typ agentjobs.RecordType) agentjobs.Record {
	t.Helper()
	var found []agentjobs.Record
	for _, rec := range r.sink.records() {
		if rec.Type == typ {
			found = append(found, rec)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one %s record, got %d (all types: %v)", typ, len(found), r.sink.types())
	}
	return found[0]
}

func newJobTurn(script string, mutate func(*Config)) (*Executor, *jobTurn) {
	run := &jobTurn{sink: &memorySink{}}
	cfg := Config{
		Command:     "bash",
		ExtraArgs:   []string{"-c", script},
		IdleTimeout: 5 * time.Second,
		Job: &JobConfig{
			Meta: agentjobs.JobMeta{AgentID: "stage:impl", StageID: "impl", Title: "Impl · run"},
			Sink: run.sink,
		},
		OnUsage: func(o accounting.Observation) { run.usages = append(run.usages, o) },
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return New(cfg), run
}

// runPath is one public Run* entry point, driven against a log file.
type runPath struct {
	name string
	run  func(ex *Executor, ctx context.Context, dir string) error
}

func runPaths() []runPath {
	return []runPath{
		{"planning", func(ex *Executor, ctx context.Context, dir string) error {
			return ex.RunPlanning(ctx, "s1", "plan", filepath.Join(dir, "out.md"), filepath.Join(dir, "plan.log"))
		}},
		{"author", func(ex *Executor, ctx context.Context, dir string) error {
			return ex.RunAgent(ctx, "implementation", "s1", "go", filepath.Join(dir, "agent.log"))
		}},
		{"verify", func(ex *Executor, ctx context.Context, dir string) error {
			out, err := ex.RunVerifyAgent(ctx, "claude", "s1", "check", filepath.Join(dir, "v.log"), filepath.Join(dir, "r.json"))
			return turnFailure(out.ProcessOK, err)
		}},
		{"helper", func(ex *Executor, ctx context.Context, dir string) error {
			out, err := ex.RunHelperAgent(ctx, "t-1", "reviewer", "help", filepath.Join(dir, "h.log"), filepath.Join(dir, "r.json"))
			return turnFailure(out.ProcessOK, err)
		}},
	}
}

// turnFailure folds verify/helper outcomes, which report a failed process in the outcome rather than as an error.
func turnFailure(processOK bool, err error) error {
	if err == nil && !processOK {
		return errors.New("process did not finish cleanly")
	}
	return err
}

func TestJob_SuccessfulTurnOnEveryPath(t *testing.T) {
	for _, p := range runPaths() {
		t.Run(p.name, func(t *testing.T) {
			ex, run := newJobTurn(emitScript("0", terminalSuccessResult), nil)
			if err := p.run(ex, context.Background(), t.TempDir()); err != nil {
				t.Fatalf("Run = %v", err)
			}

			if got := run.sink.types(); len(got) != 2 || got[0] != agentjobs.TypeStarted || got[1] != agentjobs.TypeFinished {
				t.Fatalf("published %v, want [started finished]", got)
			}
			started, finished := run.started(t), run.finished(t)
			if started.JobID == "" || finished.JobID != started.JobID {
				t.Fatalf("job IDs: started %q, finished %q", started.JobID, finished.JobID)
			}
			if started.AgentID != "stage:impl" || started.StageID != "impl" || started.Title != "Impl · run" {
				t.Errorf("started lost the configured meta: %+v", started.JobMeta)
			}
			if finished.Outcome != agentjobs.OutcomeOK || finished.DurationMS < 0 {
				t.Errorf("finished = outcome %q duration %d, want ok and non-negative", finished.Outcome, finished.DurationMS)
			}
			if len(run.usages) != 1 || run.usages[0].JobID != started.JobID {
				t.Fatalf("usage = %+v, want exactly one observation carrying job %s", run.usages, started.JobID)
			}
		})
	}
}

func TestJob_EveryCallGetsItsOwnID(t *testing.T) {
	ex, run := newJobTurn(emitScript("0", terminalSuccessResult), nil)
	dir := t.TempDir()
	for range 3 {
		if err := ex.RunAgent(context.Background(), "implementation", "s1", "go", filepath.Join(dir, "agent.log")); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for _, u := range run.usages {
		if u.JobID == "" || seen[u.JobID] {
			t.Fatalf("job IDs must be unique and set, got %+v", run.usages)
		}
		seen[u.JobID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("want 3 distinct jobs, got %d", len(seen))
	}
}

func TestJob_StartIsFixedBeforeOnDeliveredAndEndBeforeUsage(t *testing.T) {
	t0 := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	fc := clock.NewFake(t0)
	var run *jobTurn
	var publishedAtDelivery int
	ex, run := newJobTurn(emitScript("0", terminalSuccessResult), func(c *Config) {
		c.Clock = fc
		c.OnDelivered = func() {
			publishedAtDelivery = len(run.sink.records())
			fc.Advance(2 * time.Minute)
		}
		c.OnUsage = func(o accounting.Observation) {
			fc.Advance(10 * time.Minute) // time spent after the process ended must not count
			run.usages = append(run.usages, o)
		}
	})
	if err := ex.RunAgent(context.Background(), "implementation", "s1", "go", filepath.Join(t.TempDir(), "a.log")); err != nil {
		t.Fatal(err)
	}

	if publishedAtDelivery != 1 {
		t.Fatalf("started must be published before OnDelivered, %d records were visible there", publishedAtDelivery)
	}
	started, finished := run.started(t), run.finished(t)
	if !started.At.Equal(t0) {
		t.Errorf("started at %v, want the clock reading right after Start: %v", started.At, t0)
	}
	if want := t0.Add(2 * time.Minute); !finished.At.Equal(want) {
		t.Errorf("finished at %v, want %v (before OnUsage)", finished.At, want)
	}
	if finished.DurationMS != (2 * time.Minute).Milliseconds() {
		t.Errorf("duration = %d ms, want 120000", finished.DurationMS)
	}
}

func TestJob_UsageIsReportedBeforeJobIsClosed(t *testing.T) {
	var visibleAtUsage []agentjobs.RecordType
	var run *jobTurn
	ex, run := newJobTurn(emitScript("0", terminalSuccessResult), func(c *Config) {
		c.OnUsage = func(o accounting.Observation) {
			visibleAtUsage = run.sink.types()
			run.usages = append(run.usages, o)
		}
	})
	if err := ex.RunAgent(context.Background(), "implementation", "s1", "go", filepath.Join(t.TempDir(), "a.log")); err != nil {
		t.Fatal(err)
	}
	if len(visibleAtUsage) != 1 || visibleAtUsage[0] != agentjobs.TypeStarted {
		t.Fatalf("at OnUsage the journal had %v, want only [started]", visibleAtUsage)
	}
	if got := run.sink.types(); len(got) != 2 {
		t.Fatalf("finished must be published before Run returns, journal = %v", got)
	}
}

func TestJob_SegmentIsBuiltFromTheFixedStartTime(t *testing.T) {
	t0 := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	var gotAt time.Time
	ex, run := newJobTurn(emitScript("0", terminalSuccessResult), func(c *Config) {
		c.Clock = clock.NewFake(t0)
		c.Job.Segment = func(at time.Time) agentjobs.Segment {
			gotAt = at
			return agentjobs.Segment{RunStartedAt: t0.Add(-time.Hour), BaseMS: 5, OffsetMS: 7}
		}
	})
	if err := ex.RunAgent(context.Background(), "implementation", "s1", "go", filepath.Join(t.TempDir(), "a.log")); err != nil {
		t.Fatal(err)
	}
	if !gotAt.Equal(t0) {
		t.Errorf("Segment got %v, want the start time %v", gotAt, t0)
	}
	if seg := run.started(t).Segment; seg.BaseMS != 5 || seg.OffsetMS != 7 {
		t.Errorf("started segment = %+v", seg)
	}
	if !run.finished(t).Segment.IsZero() {
		t.Error("finished must not carry a segment")
	}
}

func TestJob_OutcomesOfStartedProcess(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		mutate  func(c *Config)
		want    agentjobs.Outcome
		wantErr func(error) bool
	}{
		{name: "non-zero exit", script: "exit 3", want: agentjobs.OutcomeError,
			wantErr: func(err error) bool { return err != nil }},
		{name: "usage limit", script: emitScript("1", rejectedEventSec, limitAssistant, limitResult), want: agentjobs.OutcomeUsageLimit,
			wantErr: func(err error) bool { var u *UsageLimitError; return errors.As(err, &u) }},
		{name: "error terminal result with exit 0", script: emitScript("0", terminalErrorResult), want: agentjobs.OutcomeError,
			wantErr: func(err error) bool { var u *TerminalResultError; return errors.As(err, &u) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TZ", "UTC")
			ex, run := newJobTurn(tc.script, tc.mutate)
			err := ex.RunAgent(context.Background(), "implementation", "s1", "go", filepath.Join(t.TempDir(), "a.log"))
			if !tc.wantErr(err) {
				t.Fatalf("unexpected error %v", err)
			}
			if got := run.finished(t).Outcome; got != tc.want {
				t.Fatalf("outcome = %q, want %q", got, tc.want)
			}
			if len(run.usages) != 1 || run.usages[0].JobID != run.started(t).JobID {
				t.Fatalf("want one usage observation linked to the job, got %+v", run.usages)
			}
		})
	}
}

func TestJob_IdleTimeoutDurationOnFakeClock(t *testing.T) {
	t0 := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	fc := clock.NewFake(t0)
	ex, run := newJobTurn("sleep 300", func(c *Config) { c.Clock = fc; c.IdleTimeout = 30 * time.Minute })

	done := make(chan error, 1)
	go func() {
		done <- ex.RunAgent(context.Background(), "implementation", "s1", "go", filepath.Join(t.TempDir(), "a.log"))
	}()
	fc.BlockUntil(1)
	fc.Advance(30 * time.Minute)
	if err := <-done; !errors.Is(err, ErrIdleTimeout) {
		t.Fatalf("Run = %v, want idle timeout", err)
	}

	finished := run.finished(t)
	if finished.Outcome != agentjobs.OutcomeIdleTimeout || finished.DurationMS != (30*time.Minute).Milliseconds() {
		t.Fatalf("finished = %q after %d ms, want idle_timeout after 1800000", finished.Outcome, finished.DurationMS)
	}
}

func TestJob_InterruptAndCancelDuringRun(t *testing.T) {
	t.Run("interrupt", func(t *testing.T) {
		interrupt := make(chan struct{}, 1)
		ready := filepath.Join(t.TempDir(), "ready")
		ex, run := newJobTurn("touch "+ready+"\nsleep 300", func(c *Config) { c.InterruptCh = interrupt })
		done := make(chan error, 1)
		go func() {
			done <- ex.RunAgent(context.Background(), "implementation", "s1", "go", filepath.Join(t.TempDir(), "a.log"))
		}()
		waitForFile(t, ready)
		interrupt <- struct{}{}
		if err := <-done; !errors.Is(err, ErrUserInterrupted) {
			t.Fatalf("Run = %v, want ErrUserInterrupted", err)
		}
		if got := run.finished(t).Outcome; got != agentjobs.OutcomeInterrupted {
			t.Fatalf("outcome = %q", got)
		}
	})
	t.Run("context cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		ready := filepath.Join(t.TempDir(), "ready")
		ex, run := newJobTurn("touch "+ready+"\nsleep 300", nil)
		done := make(chan error, 1)
		go func() { done <- ex.RunAgent(ctx, "implementation", "s1", "go", filepath.Join(t.TempDir(), "a.log")) }()
		waitForFile(t, ready)
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
		if got := run.finished(t).Outcome; got != agentjobs.OutcomeCancelled {
			t.Fatalf("outcome = %q", got)
		}
	})
}

func TestJob_FailureBeforeStartIsStartFailedNotAJob(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "no-such-dir")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name       string
		mutate     func(c *Config)
		ctx        context.Context
		dir        func(t *testing.T) string
		wantReason string
	}{
		{name: "binary cannot start", mutate: func(c *Config) { c.Command = "afm-no-such-binary-xyz" }, wantReason: "agent_start"},
		{name: "log cannot be opened", dir: func(*testing.T) string { return missingDir }, wantReason: "setup"},
		{name: "context already cancelled", ctx: cancelled, wantReason: "cancelled_before_start"},
		{name: "interrupt already pending", mutate: func(c *Config) {
			ch := make(chan struct{}, 1)
			ch <- struct{}{}
			c.InterruptCh = ch
		}, wantReason: "interrupted_before_start"},
	}
	for _, p := range runPaths() {
		for _, tc := range tests {
			t.Run(p.name+"/"+tc.name, func(t *testing.T) {
				ex, run := newJobTurn(emitScript("0", terminalSuccessResult), tc.mutate)
				ctx, dir := tc.ctx, t.TempDir()
				if ctx == nil {
					ctx = context.Background()
				}
				if tc.dir != nil {
					dir = tc.dir(t)
				}
				if err := p.run(ex, ctx, dir); err == nil {
					t.Fatal("want an error")
				}

				if got := run.sink.types(); len(got) != 1 || got[0] != agentjobs.TypeStartFailed {
					t.Fatalf("published %v, want only [start_failed]", got)
				}
				rec := run.only(t, agentjobs.TypeStartFailed)
				if rec.Reason != tc.wantReason || rec.JobID == "" || rec.DurationMS != 0 {
					t.Fatalf("start_failed = reason %q job %q duration %d, want reason %q", rec.Reason, rec.JobID, rec.DurationMS, tc.wantReason)
				}
				if strings.ContainsAny(rec.Reason, " :/") {
					t.Errorf("reason must be a machine code, got %q", rec.Reason)
				}
				for _, u := range run.usages {
					if u.JobID != rec.JobID {
						t.Errorf("usage reported for an unstarted job must still name it: %+v", u)
					}
				}
			})
		}
	}
}

func TestJob_FailingJournalNeverFailsTheTurn(t *testing.T) {
	ex, run := newJobTurn(emitScript("0", terminalSuccessResult), nil)
	run.sink.err = errors.New("disk full")
	if err := ex.RunAgent(context.Background(), "implementation", "s1", "go", filepath.Join(t.TempDir(), "a.log")); err != nil {
		t.Fatalf("a journal failure must not fail the turn: %v", err)
	}
	if len(run.usages) != 1 {
		t.Fatalf("usage must still be reported, got %d", len(run.usages))
	}
}

func TestJob_NotConfiguredMeansNoJobAndNoJobID(t *testing.T) {
	var usages []accounting.Observation
	ex := New(Config{
		Command: "bash", ExtraArgs: []string{"-c", emitScript("0", terminalSuccessResult)}, IdleTimeout: 5 * time.Second,
		OnUsage: func(o accounting.Observation) { usages = append(usages, o) },
	})
	if err := ex.RunAgent(context.Background(), "implementation", "s1", "go", filepath.Join(t.TempDir(), "a.log")); err != nil {
		t.Fatal(err)
	}
	if len(usages) != 1 || usages[0].JobID != "" {
		t.Fatalf("legacy path must keep reporting usage without a job ID, got %+v", usages)
	}
}

func TestJob_ScriptsAreNotJobs(t *testing.T) {
	ex, run := newJobTurn("echo hi", nil)
	if err := ex.RunScript(context.Background(), 5*time.Second, filepath.Join(t.TempDir(), "s.log")); err != nil {
		t.Fatal(err)
	}
	if got := run.sink.types(); len(got) != 0 {
		t.Fatalf("RunScript published %v, want nothing", got)
	}
}

func TestJobOutcomeOf(t *testing.T) {
	wrapped := func(err error) error { return errors.Join(errors.New("ctx"), err) }
	tests := []struct {
		err  error
		want agentjobs.Outcome
	}{
		{nil, agentjobs.OutcomeOK},
		{&UsageLimitError{}, agentjobs.OutcomeUsageLimit},
		{ErrUserInterrupted, agentjobs.OutcomeInterrupted},
		{wrapped(ErrUserInterrupted), agentjobs.OutcomeInterrupted},
		{ErrIdleTimeout, agentjobs.OutcomeIdleTimeout},
		{context.Canceled, agentjobs.OutcomeCancelled},
		{context.DeadlineExceeded, agentjobs.OutcomeCancelled},
		{&TerminalResultError{}, agentjobs.OutcomeError},
		{errors.New("exit status 1"), agentjobs.OutcomeError},
	}
	for _, tc := range tests {
		if got := jobOutcomeOf(tc.err); got != tc.want {
			t.Errorf("jobOutcomeOf(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestJob_JobIDIsAValueNotAPath(t *testing.T) {
	ex, run := newJobTurn(emitScript("0", terminalSuccessResult), nil)
	if err := ex.RunAgent(context.Background(), "implementation", "s1", "go", filepath.Join(t.TempDir(), "a.log")); err != nil {
		t.Fatal(err)
	}
	id := run.started(t).JobID
	if strings.ContainsAny(id, `/\.`) || len(id) > 40 {
		t.Fatalf("job ID %q must be a short opaque value", id)
	}
}

// A turn that ends cleanly asks the caller whether it yielded into a mesh wait instead of answering. A helper
// hands over its final text, which tells an answer from a yield; the text itself never reaches the journal.
func TestJob_YieldedDecidesACleanTurn(t *testing.T) {
	const waitCall = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"afm agent wait"}}]}}`
	const answer = `{"type":"assistant","message":{"content":[{"type":"text","text":"secret answer"}]}}`
	helper, author := runPaths()[3], runPaths()[1]
	tests := []struct {
		name      string
		path      runPath
		script    string
		wantAsked []string
		want      agentjobs.Outcome
	}{
		{"helper ends on a wait", helper, emitScript("0", waitCall, terminalSuccessResult), []string{""}, agentjobs.OutcomeYielded},
		{"helper answers", helper, emitScript("0", waitCall, answer, terminalSuccessResult), []string{"secret answer"}, agentjobs.OutcomeOK},
		{"author ends on a wait", author, emitScript("0", waitCall, terminalSuccessResult), []string{""}, agentjobs.OutcomeYielded},
		{"failed turn is never asked", author, "exit 3", nil, agentjobs.OutcomeError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var asked []string
			ex, run := newJobTurn(tc.script, func(c *Config) {
				c.Job.Yielded = func(finalText string) bool {
					asked = append(asked, finalText)
					return strings.TrimSpace(finalText) == ""
				}
			})
			_ = tc.path.run(ex, context.Background(), t.TempDir())

			if got := run.finished(t).Outcome; got != tc.want {
				t.Fatalf("outcome = %q, want %q", got, tc.want)
			}
			if !slices.Equal(asked, tc.wantAsked) {
				t.Fatalf("asked with %q, want %q", asked, tc.wantAsked)
			}
			for _, rec := range run.sink.records() {
				if raw, _ := json.Marshal(rec); strings.Contains(string(raw), "secret answer") {
					t.Fatalf("the final text reached the journal: %s", raw)
				}
			}
		})
	}
}
