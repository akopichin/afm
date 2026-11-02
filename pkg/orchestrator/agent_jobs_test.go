package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/agentjobs"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/memorypipeline"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/state"
	"github.com/akopichin/afm/schema"
)

// These tests run stage, verify, helper and formatter turns on stub agent scripts or fake runners: no real
// claude or codex process is ever started.

const jobsTestRunID = "run1"

// writeJobScript writes a stub agent that prints one metered Claude result. body runs before the result.
func writeJobScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.sh")
	script := "#!/bin/sh\ncat > /dev/null\n" + body + "\n" +
		`echo '{"type":"assistant","message":{"model":"claude-opus-4-8","content":[{"type":"text","text":"done"}]}}'` + "\n" +
		`echo '{"type":"result","subtype":"success","result":"done","usage":{"input_tokens":10,"output_tokens":5}}'` + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

type jobsTestRig struct {
	o     *Orchestrator
	store *state.Store
	jobs  *agentjobs.Store
	acct  *accounting.Store
	dir   string
}

// newJobsTestRig builds a production orchestrator (New) with a jobs journal and a usage ledger in a fresh run.
func newJobsTestRig(t *testing.T, stages ...flow.Stage) *jobsTestRig {
	t.Helper()
	dir := t.TempDir()
	ids := make([]string, len(stages))
	for i, s := range stages {
		ids[i] = s.ID
	}
	store, err := state.Open(dir, ids)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	jobs, err := agentjobs.Open(dir, agentjobs.Header{RecordingStartedAt: time.Now(), BaseOffsetMS: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jobs.Close() })
	acct, err := accounting.Open(dir, accounting.NewResolver(accounting.PricingConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = acct.Close() })
	o := New(Options{
		RunDir: dir, Stages: stages, Store: store, Config: config.Default(), Prompts: DefaultPrompts(),
		RunID: jobsTestRunID, Jobs: jobs, Accounting: acct,
	})
	return &jobsTestRig{o: o, store: store, jobs: jobs, acct: acct, dir: dir}
}

// runAsync runs the orchestrator; the cleanup cancels it and waits for Run before the stores close (LIFO).
func (r *jobsTestRig) runAsync(t *testing.T) (cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	finished := make(chan struct{})
	go func() { _ = r.o.Run(ctx); close(finished) }()
	t.Cleanup(func() {
		stop()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return within 10s of cancellation")
		}
	})
	return stop, finished
}

func recordsOf(snap agentjobs.Snapshot, typ agentjobs.RecordType) []agentjobs.Record {
	var out []agentjobs.Record
	for _, rec := range snap.Records {
		if rec.Type == typ {
			out = append(out, rec)
		}
	}
	return out
}

func usageRecords(t *testing.T, dir string) []accounting.UsageRecord {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "usage.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []accounting.UsageRecord
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec accounting.UsageRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("usage line %q: %v", sc.Text(), err)
		}
		out = append(out, rec)
	}
	return out
}

// publishStarted and publishFinished put a job on the journal exactly the way the executor does with job.
func publishStarted(t *testing.T, job *executor.JobConfig, id string, at time.Time) {
	t.Helper()
	meta := job.Meta
	meta.JobID = id
	if err := job.Sink.Append(agentjobs.Record{Type: agentjobs.TypeStarted, JobMeta: meta, At: at, Segment: job.Segment(at)}); err != nil {
		t.Fatal(err)
	}
}

func publishFinished(t *testing.T, job *executor.JobConfig, id string, at time.Time, d time.Duration) {
	t.Helper()
	rec := agentjobs.Record{Type: agentjobs.TypeFinished, JobMeta: agentjobs.JobMeta{JobID: id}, At: at,
		Outcome: agentjobs.OutcomeOK, DurationMS: d.Milliseconds()}
	if err := job.Sink.Append(rec); err != nil {
		t.Fatal(err)
	}
}

func useFakeStateClock(t *testing.T, start time.Time) *clock.FakeClock {
	t.Helper()
	fc := clock.NewFake(start)
	state.SetClockForTest(fc)
	t.Cleanup(func() { state.SetClockForTest(nil) })
	return fc
}

var jobsT0 = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

// A job's segment is read from the run state: the anchor of the live process and the active time of the
// earlier ones, so an offline gap between processes never lands on the timeline.
func TestJobSegment_ExcludesTheOfflineGap(t *testing.T) {
	fc := useFakeStateClock(t, jobsT0)
	store, err := state.Open(t.TempDir(), []string{"build"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	o := &Orchestrator{opts: Options{Store: store}}

	if seg := o.jobSegment(jobsT0); !seg.IsZero() {
		t.Fatalf("segment before the run began = %+v, want none", seg)
	}
	if err := store.BeginRun(); err != nil {
		t.Fatal(err)
	}
	fc.Advance(10 * time.Minute)
	if err := store.EndRun(state.RunStatusInterrupted); err != nil {
		t.Fatal(err)
	}
	if seg := o.jobSegment(fc.Now()); !seg.IsZero() {
		t.Fatalf("segment after the run ended = %+v, want none", seg)
	}
	fc.Advance(time.Hour) // afm is not running
	if err := store.BeginRun(); err != nil {
		t.Fatal(err)
	}

	got := o.jobSegment(jobsT0.Add(75 * time.Minute))
	want := agentjobs.Segment{RunStartedAt: jobsT0.Add(70 * time.Minute), BaseMS: 600_000, OffsetMS: 900_000}
	if !got.RunStartedAt.Equal(want.RunStartedAt) || got.BaseMS != want.BaseMS || got.OffsetMS != want.OffsetMS {
		t.Fatalf("segment = %+v, want %+v", got, want)
	}
}

// Ten turns of one stage agent are ten jobs of the same agent, each joined to its usage record.
func TestAgentJobs_StageTurnsAreJobsOfOneAgent(t *testing.T) {
	script := writeJobScript(t, "")
	stage := flow.Stage{ID: "build", Name: "Build", Agents: []flow.AgentType{flow.AgentAuto}, Command: script}
	rig := newJobsTestRig(t, stage)
	if err := rig.store.BeginRun(); err != nil {
		t.Fatal(err)
	}
	logDir := t.TempDir()
	for i := range 10 {
		r := rig.o.runnerFor(stage, phaseAutonomous)
		if err := r.RunAgent(context.Background(), phaseAutonomous, stage.Name, "prompt", filepath.Join(logDir, fmt.Sprintf("turn-%d.log", i))); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}

	snap := rig.jobs.Snapshot()
	started := recordsOf(snap, agentjobs.TypeStarted)
	if len(started) != 10 {
		t.Fatalf("started = %d, want 10", len(started))
	}
	ids := map[string]bool{}
	for _, rec := range started {
		ids[rec.JobID] = true
		if rec.AgentID != "stage:build" || rec.AgentKind != accounting.AgentKindStage || rec.StageID != "build" ||
			rec.RootStageID != "build" || rec.Phase != phaseAutonomous || rec.Title != "build · Autonomous execution" || rec.RunID != jobsTestRunID {
			t.Fatalf("stage job meta = %+v", rec.JobMeta)
		}
		if rec.Segment.IsZero() {
			t.Fatalf("job %s has no segment on a live run", rec.JobID)
		}
	}
	if len(ids) != 10 {
		t.Fatalf("distinct job IDs = %d, want 10", len(ids))
	}
	for _, rec := range recordsOf(snap, agentjobs.TypeFinished) {
		if rec.Outcome != agentjobs.OutcomeOK {
			t.Fatalf("finished %s outcome = %s", rec.JobID, rec.Outcome)
		}
	}
	usage := usageRecords(t, rig.dir)
	if len(usage) != 10 {
		t.Fatalf("usage records = %d, want 10", len(usage))
	}
	for _, rec := range usage {
		if !ids[rec.JobID] {
			t.Fatalf("usage job_id %q matches no started job", rec.JobID)
		}
	}

	facts := accounting.BuildFacts(accounting.AnalyticsInput{Jobs: snap, Meta: rig.o.AnalyticsMeta()})
	rows := facts.Select(accounting.Filter{}, time.Now()).Rows(accounting.GroupAgent, accounting.SortJobs, true)
	if len(rows) != 1 || rows[0].Key != "stage:build" || rows[0].Jobs != 10 {
		t.Fatalf("agent rows = %+v, want one stage:build row with 10 jobs", rows)
	}
}

// A turn that continues an interactive CLI session is a new job of that session, linked to the job it resumed.
func TestAgentJobs_ResumedSessionTurnIsANewJob(t *testing.T) {
	script := writeJobScript(t, "")
	stage := flow.Stage{ID: "build", Name: "Build", Agents: []flow.AgentType{flow.AgentImplementation}, Interactive: true, Command: script}
	rig := newJobsTestRig(t, stage)
	if err := os.MkdirAll(filepath.Join(rig.dir, stage.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	logDir := t.TempDir()
	for i := range 2 {
		r := rig.o.runnerFor(stage, phaseImplementation)
		if err := r.RunAgent(context.Background(), phaseImplementation, stage.Name, "prompt", filepath.Join(logDir, fmt.Sprintf("turn-%d.log", i))); err != nil {
			t.Fatal(err)
		}
	}
	started := recordsOf(rig.jobs.Snapshot(), agentjobs.TypeStarted)
	if len(started) != 2 {
		t.Fatalf("started = %d, want 2", len(started))
	}
	first, second := started[0], started[1]
	if first.SessionID == "" || second.SessionID != first.SessionID || second.JobID == first.JobID {
		t.Fatalf("session continuation: first=%+v second=%+v", first.JobMeta, second.JobMeta)
	}
	if first.PrevJobID != "" || second.PrevJobID != first.JobID {
		t.Fatalf("prev job: first=%q second=%q, want \"\" and %q", first.PrevJobID, second.PrevJobID, first.JobID)
	}
}

// Every AI verify step is an agent of its own, keyed by the stage and the step number.
func TestAgentJobs_VerifyStepsAreSeparateAgents(t *testing.T) {
	script := writeJobScript(t, "")
	stage := flow.Stage{ID: "build", Name: "Build", Command: script}
	rig := newJobsTestRig(t, stage)
	dir := t.TempDir()
	for _, step := range []int{1, 3} {
		logFile := filepath.Join(dir, fmt.Sprintf("v%d.log", step))
		if _, err := rig.o.execVerifyAgent(withVerifyStep(context.Background(), step), stage, script, "prompt", logFile, logFile+".json"); err != nil {
			t.Fatal(err)
		}
	}
	started := recordsOf(rig.jobs.Snapshot(), agentjobs.TypeStarted)
	if len(started) != 2 {
		t.Fatalf("started = %d, want 2", len(started))
	}
	for i, step := range []int{1, 3} {
		m := started[i].JobMeta
		if m.AgentID != fmt.Sprintf("verify:build#%d", step) || m.AgentKind != accounting.AgentKindVerify || m.VerifyStep != step ||
			m.StageID != "build" || m.RootStageID != "build" || m.Phase != verifyExecutionLabel || m.Title != fmt.Sprintf("Verify · step %d", step) {
			t.Fatalf("verify step %d meta = %+v", step, m)
		}
	}
}

// RunVerification hands each AI step its own number; shell steps count too, so the number matches the report.
func TestRunVerification_HandsTheStepNumberToTheAgentRun(t *testing.T) {
	stage := flow.Stage{ID: verifyTestStageID, Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
		{Kind: flow.VerifyShell, Run: "true"},
		{Kind: flow.VerifyAgent, Command: "codex"},
		{Kind: flow.VerifyAgent, Command: "codex"},
	}}}
	o, _ := newVerifyTestOrchestrator(t, stage)
	var steps []int
	o.runVerifyAgent = func(ctx context.Context, _ flow.Stage, _, _, _, _ string) (verify.RunOutcome, error) {
		steps = append(steps, verifyStepOf(ctx))
		return passOutcome(), nil
	}
	if err := o.RunVerification(context.Background(), stage, phaseImplementation); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(steps, []int{2, 3}) {
		t.Fatalf("verify steps = %v, want [2 3]", steps)
	}
}

// Helpers and run agents are keyed by their participant ID: two helpers of one profile and title stay two
// agents, a nested helper keeps its root stage, and a run agent from an old journal keeps "run:<profile>".
func TestAgentJobs_TaskIdentity(t *testing.T) {
	parent := assistants.TaskID("t-1")
	legacy := agentcomm.ParticipantID("run:qa")
	newRun := meshRunParticipantID("t-new")
	rs := state.RunState{Assistants: state.AssistantsState{
		Tasks: map[assistants.TaskID]*state.TaskState{
			"t-1":   {Task: assistants.Task{ID: "t-1", Scope: assistants.ScopeStage, RootStageID: "build", ProfileID: "reviewer", Title: "Review"}, Turn: 3},
			"t-2":   {Task: assistants.Task{ID: "t-2", Scope: assistants.ScopeStage, RootStageID: "build", ProfileID: "reviewer", Title: "Review"}},
			"t-3":   {Task: assistants.Task{ID: "t-3", Scope: assistants.ScopeStage, RootStageID: "build", ProfileID: "qa", ParentTaskID: &parent}},
			"t-old": {Task: assistants.Task{ID: "t-old", Scope: assistants.ScopeRun, ProfileID: "qa", Title: "QA"}},
			"t-new": {Task: assistants.Task{ID: "t-new", Scope: assistants.ScopeRun, ProfileID: "qa"}},
		},
		Mesh: state.MeshState{Participants: map[agentcomm.ParticipantID]*agentcomm.Participant{
			legacy: {ID: legacy, Kind: agentcomm.KindRunAgent, Scope: agentcomm.ScopeRun},
			newRun: {ID: newRun, Kind: agentcomm.KindRunAgent, Scope: agentcomm.ScopeRun},
		}},
	}}
	o := &Orchestrator{}
	for _, tc := range []struct {
		task assistants.TaskID
		want agentjobs.JobMeta
	}{
		{"t-1", agentjobs.JobMeta{AgentID: "helper:t-1", AgentKind: accounting.AgentKindHelper, StageID: "build", RootStageID: "build", TaskID: "t-1", ProfileID: "reviewer", Phase: helperJobPhase, Turn: 3, Title: "Review"}},
		{"t-2", agentjobs.JobMeta{AgentID: "helper:t-2", AgentKind: accounting.AgentKindHelper, StageID: "build", RootStageID: "build", TaskID: "t-2", ProfileID: "reviewer", Phase: helperJobPhase, Title: "Review"}},
		{"t-3", agentjobs.JobMeta{AgentID: "helper:t-3", AgentKind: accounting.AgentKindHelper, StageID: "build", RootStageID: "build", TaskID: "t-3", ParentTaskID: "t-1", ProfileID: "qa", Phase: helperJobPhase}},
		{"t-old", agentjobs.JobMeta{AgentID: "run:qa", AgentKind: accounting.AgentKindRunAgent, TaskID: "t-old", ProfileID: "qa", Phase: helperJobPhase, Title: "QA"}},
		{"t-new", agentjobs.JobMeta{AgentID: "run:t-new", AgentKind: accounting.AgentKindRunAgent, TaskID: "t-new", ProfileID: "qa", Phase: helperJobPhase}},
	} {
		if got := o.taskJobMeta(rs, tc.task, "helper-cmd"); got != tc.want {
			t.Errorf("task %s meta = %+v, want %+v", tc.task, got, tc.want)
		}
	}
}

// A helper turn that ends while its agent waits on a colleague is a yielded job; the turn after the reply is ok.
func TestAgentJobs_HelperTurnThatYieldsIsYielded(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	jobs, err := agentjobs.Open(o.opts.RunDir, agentjobs.Header{RecordingStartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jobs.Close() })
	o.opts.Jobs = jobs
	activateStage(t, o, assistantsTestStageID)
	agent := newYieldingAgent(t)
	agent.useRealExecutor(o)

	taskID := deriveTaskID("op-yield-jobs")
	helperID := meshHelperParticipantID(taskID)
	attachTestTask(context.Background(), t, o, "op-yield-jobs")
	agent.waitStarted()
	registerHelperWait(t, o, helperID, deriveMeshThreadID("op-yield-open"))
	agent.release()
	waitForTaskStatus(t, store, taskID, assistants.StatusWaitingPeer, 5*time.Second)
	replyToHelper(t, o, helperID)
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 5*time.Second)
	o.concurrency.WaitAgents()

	snap := jobs.Snapshot()
	started := recordsOf(snap, agentjobs.TypeStarted)
	if len(started) != 2 {
		t.Fatalf("started = %d, want 2", len(started))
	}
	for i, rec := range started {
		if rec.AgentID != string(helperID) || rec.TaskID != string(taskID) || rec.RootStageID != assistantsTestStageID || rec.Turn != i+1 {
			t.Fatalf("helper turn %d meta = %+v", i+1, rec.JobMeta)
		}
	}
	var outcomes []agentjobs.Outcome
	for _, rec := range recordsOf(snap, agentjobs.TypeFinished) {
		outcomes = append(outcomes, rec.Outcome)
	}
	if !slices.Equal(outcomes, []agentjobs.Outcome{agentjobs.OutcomeYielded, agentjobs.OutcomeOK}) {
		t.Fatalf("outcomes = %v, want [yielded ok]", outcomes)
	}
}

// Format repair is a job of the agent whose answer it repairs: the stage (question), a verify step or a helper.
func TestAgentJobs_FormatRepairBelongsToItsOwner(t *testing.T) {
	stage := flow.Stage{ID: "build", Name: "Build", Command: "codex"}
	rig := newJobsTestRig(t, stage)
	o := rig.o
	rs := state.RunState{Assistants: state.AssistantsState{Tasks: map[assistants.TaskID]*state.TaskState{
		"t-1": {Task: assistants.Task{ID: "t-1", Scope: assistants.ScopeStage, RootStageID: "build", ProfileID: "reviewer", Title: "Review"}},
	}}}

	for _, tc := range []struct {
		name string
		got  *executor.JobConfig
		want agentjobs.JobMeta
	}{
		{"stage", o.outputFormatterConfig("codex", "build").Job,
			agentjobs.JobMeta{AgentID: "stage:build", AgentKind: accounting.AgentKindStage, StageID: "build", RootStageID: "build", Title: "Format repair · build"}},
		{"verify", o.repairJob(o.verifyJobMeta(stage, 2, "codex"), "codex"),
			agentjobs.JobMeta{AgentID: "verify:build#2", AgentKind: accounting.AgentKindVerify, StageID: "build", RootStageID: "build", VerifyStep: 2, Title: "Format repair · Verify · step 2"}},
		{"helper", o.repairJob(o.taskJobMeta(rs, "t-1", "codex"), "codex"),
			agentjobs.JobMeta{AgentID: "helper:t-1", AgentKind: accounting.AgentKindHelper, StageID: "build", RootStageID: "build", TaskID: "t-1", ProfileID: "reviewer", Title: "Format repair · Review"}},
	} {
		if tc.got == nil {
			t.Fatalf("%s: no job config", tc.name)
		}
		tc.want.RunID, tc.want.Phase = jobsTestRunID, phaseJSONFix
		if tc.got.Meta != tc.want {
			t.Errorf("%s repair meta = %+v, want %+v", tc.name, tc.got.Meta, tc.want)
		}
	}
}

// The helper and verify formatters are measured as jobs of their owners.
func TestAgentJobs_FormatterCallSitesUseTheirOwner(t *testing.T) {
	t.Run("helper", func(t *testing.T) {
		o, _ := newAssistantsTestOrchestrator(t, true)
		jobs, err := agentjobs.Open(o.opts.RunDir, agentjobs.Header{RecordingStartedAt: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = jobs.Close() })
		o.opts.Jobs = jobs
		activateStage(t, o, assistantsTestStageID)
		var owner string
		o.runOutputFormat = func(_ context.Context, cfg executor.Config, _, _, _ string) ([]byte, error) {
			if cfg.Job != nil {
				owner = cfg.Job.Meta.AgentID
			}
			return nil, errors.New("formatter down")
		}
		o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
			return executor.HelperOutcome{ProcessOK: true, RawText: recoveryHelperRaw}, nil
		}
		taskID := attachTestTask(context.Background(), t, o, "op-format-owner")
		waitForTaskStatus(t, o.opts.Store, taskID, assistants.StatusFailed, 5*time.Second)
		if owner != string(meshHelperParticipantID(taskID)) {
			t.Fatalf("helper formatter owner = %q, want %s", owner, meshHelperParticipantID(taskID))
		}
	})
	t.Run("verify", func(t *testing.T) {
		stage := flow.Stage{ID: verifyTestStageID, Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
			{Kind: flow.VerifyShell, Run: "true"},
			{Kind: flow.VerifyAgent, Command: "codex"},
		}}}
		o, _ := newVerifyTestOrchestrator(t, stage)
		jobs, err := agentjobs.Open(o.opts.RunDir, agentjobs.Header{RecordingStartedAt: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = jobs.Close() })
		o.opts.Jobs = jobs
		var owner string
		o.runOutputFormat = func(_ context.Context, cfg executor.Config, _, _, _ string) ([]byte, error) {
			if cfg.Job != nil {
				owner = cfg.Job.Meta.AgentID
			}
			return nil, errors.New("formatter down")
		}
		o.runVerifyAgent = func(_ context.Context, _ flow.Stage, _, _, _, resultFile string) (verify.RunOutcome, error) {
			if err := os.WriteFile(resultFile, []byte(recoveryVerifyRaw), 0o644); err != nil {
				t.Error(err)
			}
			return verify.RunOutcome{ProcessOK: true, ProtocolErr: errors.New("bad json")}, nil
		}
		_ = o.RunVerification(context.Background(), stage, phaseImplementation)
		if owner != "verify:"+verifyTestStageID+"#2" {
			t.Fatalf("verify formatter owner = %q, want verify:%s#2", owner, verifyTestStageID)
		}
	})
}

// A native-schema attempt rejected by the CLI falls back to a prompt-only run: two processes, two jobs.
func TestAgentJobs_NativeFallbackIsTwoJobs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	for _, name := range []string{"ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		t.Setenv(name, "")
	}
	// The stub is named claude so the formatter asks for native mode; it is a shell script, not the CLI.
	stub := "#!/bin/sh\n" +
		`echo '{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":400,"result":"API Error: 400 schema rejected"}'` + "\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	stage := flow.Stage{ID: "build", Name: "Build", Command: "claude"}
	rig := newJobsTestRig(t, stage)
	cfg := rig.o.outputFormatterConfig("claude", "build")
	cfg.Dir = dir
	_, err := recoverOutput(context.Background(), schema.Question, "t", []byte(`{"question":"Pick","options":"one"}`),
		questionOutputValidator, 1, rig.o.outputFormatter(cfg, dir, "output", ""))
	if err == nil {
		t.Fatal("both formatter modes failed, want an error")
	}
	snap := rig.jobs.Snapshot()
	started := recordsOf(snap, agentjobs.TypeStarted)
	if len(started) != 2 || started[0].JobID == started[1].JobID {
		t.Fatalf("started = %+v, want two jobs", started)
	}
	for _, rec := range started {
		if rec.AgentID != "stage:build" || rec.Phase != phaseJSONFix {
			t.Fatalf("formatter job meta = %+v", rec.JobMeta)
		}
	}
	if finished := recordsOf(snap, agentjobs.TypeFinished); len(finished) != 2 {
		t.Fatalf("finished = %d, want 2", len(finished))
	}
}

// Reflect belongs to its stage; aggregate, prioritize and update are run overhead.
func TestAgentJobs_MemoryAttribution(t *testing.T) {
	rig := newJobsTestRig(t, flow.Stage{ID: "build", Name: "Build", Command: "codex"})
	reflect := rig.o.memoryJob(memorypipeline.AgentSpec{Kind: memorypipeline.KindReflect, StageID: "build", Phase: memorypipeline.PhaseReflect})
	if reflect == nil {
		t.Fatal("no job config for reflect")
	}
	want := agentjobs.JobMeta{RunID: jobsTestRunID, AgentID: "stage:build", AgentKind: accounting.AgentKindStage, StageID: "build", RootStageID: "build", Phase: memorypipeline.PhaseReflect, Title: "Memory · reflect"}
	if reflect.Meta != want {
		t.Fatalf("reflect meta = %+v, want %+v", reflect.Meta, want)
	}
	for _, kind := range []string{memorypipeline.KindAggregate, memorypipeline.KindPrioritize, memorypipeline.KindUpdate} {
		job := rig.o.memoryJob(memorypipeline.AgentSpec{Kind: kind, Phase: "memory_" + kind, Scope: memorypipeline.ScopeRunOverhead})
		want := agentjobs.JobMeta{RunID: jobsTestRunID, AgentID: accounting.RunOverheadAgentID, AgentKind: accounting.AgentKindRunOverhead, Phase: "memory_" + kind, Title: "Memory · " + kind}
		if job == nil || job.Meta != want {
			t.Fatalf("%s job = %+v, want %+v", kind, job, want)
		}
	}
}

// The Assistant is an agent of its own, outside the run total, and gets no timeline place once the flow ended.
func TestAgentJobs_AssistantJob(t *testing.T) {
	rig := newJobsTestRig(t, flow.Stage{ID: "build", Name: "Build", Command: "codex"})
	side, err := agentjobs.Open(t.TempDir(), agentjobs.Header{RecordingStartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = side.Close() })
	job := rig.o.AssistantJob(side)
	want := agentjobs.JobMeta{RunID: jobsTestRunID, AgentID: "side:" + jobsTestRunID, AgentKind: accounting.AgentKindAssistant}
	if job == nil || job.Meta != want || job.Sink != executor.JobSink(side) {
		t.Fatalf("assistant job = %+v, want meta %+v on its own journal", job, want)
	}
	if err := rig.store.BeginRun(); err != nil {
		t.Fatal(err)
	}
	if job.Segment(time.Now()).IsZero() {
		t.Fatal("an Assistant job during the flow has no timeline place")
	}
	if err := rig.store.EndRun(state.RunStatusFinished); err != nil {
		t.Fatal(err)
	}
	if seg := job.Segment(time.Now()); !seg.IsZero() {
		t.Fatalf("an Assistant job after the flow got segment %+v", seg)
	}
}

type countingRunner struct {
	executor.Runner
	calls int
}

func (r *countingRunner) RunAgent(context.Context, string, string, string, string) error {
	r.calls++
	return nil
}

// A runner injected by a test stands in for the executor: it is not measured and writes no jobs.
func TestAgentJobs_InjectedRunnerRecordsNoJobs(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(dir, []string{"build"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	jobs, err := agentjobs.Open(dir, agentjobs.Header{RecordingStartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jobs.Close() })
	runner := &countingRunner{}
	o := New(Options{RunDir: dir, Stages: []flow.Stage{{ID: "build"}}, Store: store, Config: config.Default(), Runner: runner, Jobs: jobs})
	if err := o.runnerFor(flow.Stage{ID: "build"}, phaseAutonomous).RunAgent(context.Background(), phaseAutonomous, "build", "p", filepath.Join(dir, "a.log")); err != nil {
		t.Fatal(err)
	}
	if runner.calls != 1 || len(jobs.Snapshot().Records) != 0 {
		t.Fatalf("calls=%d records=%d, want the injected runner used and no jobs", runner.calls, len(jobs.Snapshot().Records))
	}
}

// The stage completes only after its agent's process ended, even if the agent wrote its summary early: the
// terminal transition sees the closed job and its final usage.
func TestAgentJobs_CompletionSeesTheClosedJobAndItsUsage(t *testing.T) {
	script := writeJobScript(t, `printf '## Summary\ndone\n' > "$AFM_STAGE_DIR/execution_summary.md"`+"\nsleep 0.3")
	stage := flow.Stage{ID: "build", Name: "Build", Agents: []flow.AgentType{flow.AgentAuto}, Command: script}
	rig := newJobsTestRig(t, stage)
	type seen struct {
		jobs  agentjobs.Snapshot
		usage []accounting.UsageRecord
	}
	atCompletion := make(chan seen, 1)
	rig.o.stageCompletingHook = func(string) {
		atCompletion <- seen{jobs: rig.jobs.Snapshot(), usage: usageRecords(t, rig.dir)}
	}
	rig.runAsync(t)

	var got seen
	select {
	case got = <-atCompletion:
	case <-time.After(10 * time.Second):
		t.Fatal("the stage never completed")
	}
	started := recordsOf(got.jobs, agentjobs.TypeStarted)
	finished := recordsOf(got.jobs, agentjobs.TypeFinished)
	if len(started) != 1 || len(finished) != 1 || finished[0].JobID != started[0].JobID || finished[0].Outcome != agentjobs.OutcomeOK {
		t.Fatalf("at completion: started=%+v finished=%+v, want one closed ok job", started, finished)
	}
	if len(got.usage) != 1 || got.usage[0].JobID != started[0].JobID {
		t.Fatalf("at completion: usage=%+v, want the job's final usage", got.usage)
	}
}

// Shutdown cancels the run, waits for the agents and only then may the journal close: the job a cancelled
// agent was running is already finished when Run returns.
func TestAgentJobs_ShutdownFinishesJobsBeforeTheJournalCloses(t *testing.T) {
	script := writeJobScript(t, "sleep 30")
	stage := flow.Stage{ID: "build", Name: "Build", Agents: []flow.AgentType{flow.AgentAuto}, Command: script}
	rig := newJobsTestRig(t, stage)
	cancel, done := rig.runAsync(t)

	deadline := time.Now().Add(10 * time.Second)
	for len(recordsOf(rig.jobs.Snapshot(), agentjobs.TypeStarted)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the agent never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	finished := recordsOf(rig.jobs.Snapshot(), agentjobs.TypeFinished)
	if len(finished) != 1 || finished[0].Outcome != agentjobs.OutcomeCancelled {
		t.Fatalf("finished after Run returned = %+v, want one cancelled job", finished)
	}
	if err := rig.jobs.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rig.jobs.Append(agentjobs.Record{Type: agentjobs.TypeStartFailed, JobMeta: agentjobs.JobMeta{JobID: "late"}, At: time.Now(), Reason: "setup"}); !errors.Is(err, agentjobs.ErrClosed) {
		t.Fatalf("append after close = %v, want ErrClosed", err)
	}
}

// After a crash the open job of the dead process is interrupted with an unknown duration, a job that ended
// after the run's last durable record loses its timeline place, and the resumed process places its jobs after
// the active time of the dead one, without the offline gap.
func TestAgentJobs_CrashAndResumeKeepTheTimelineHonest(t *testing.T) {
	fc := useFakeStateClock(t, jobsT0)
	dir := t.TempDir()
	stage := flow.Stage{ID: "build", Name: "Build", Command: "agent"}
	open := func() (*state.Store, *agentjobs.Store, *Orchestrator) {
		store, err := state.Open(dir, []string{"build"})
		if err != nil {
			t.Fatal(err)
		}
		jobs, err := agentjobs.Open(dir, agentjobs.Header{RecordingStartedAt: fc.Now()})
		if err != nil {
			t.Fatal(err)
		}
		o := New(Options{RunDir: dir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default(), RunID: jobsTestRunID, Jobs: jobs, Clock: fc})
		if err := store.BeginRun(); err != nil {
			t.Fatal(err)
		}
		return store, jobs, o
	}

	store1, jobs1, o1 := open()
	job1 := o1.jobConfig(o1.stageJobMeta(stage, phaseImplementation, "agent"), "")
	publishStarted(t, job1, "job-a", jobsT0.Add(time.Minute))
	publishStarted(t, job1, "job-b", jobsT0.Add(2*time.Minute))
	fc.Advance(5 * time.Minute)
	if err := store1.Apply(&state.Transition{StageID: "build", From: state.StatusPending, To: state.StatusRunning, Event: "start_run"}); err != nil {
		t.Fatal(err)
	}
	publishFinished(t, job1, "job-b", jobsT0.Add(8*time.Minute), 6*time.Minute)
	_ = jobs1.Close()
	store1.Close() // afm dies: no run end is recorded

	fc.Advance(55 * time.Minute)
	store2, jobs2, o2 := open()
	t.Cleanup(func() { store2.Close(); _ = jobs2.Close() })
	job2 := o2.jobConfig(o2.stageJobMeta(stage, phaseImplementation, "agent"), "")
	publishStarted(t, job2, "job-c", jobsT0.Add(61*time.Minute))

	snap := jobs2.Snapshot()
	if !snap.Header.RecordingStartedAt.Equal(jobsT0) {
		t.Fatalf("recording start = %s, want the first process's %s", snap.Header.RecordingStartedAt, jobsT0)
	}
	facts := accounting.BuildFacts(accounting.AnalyticsInput{Jobs: snap, Meta: o2.AnalyticsMeta()})
	byID := map[string]accounting.JobItem{}
	for _, item := range facts.Select(accounting.Filter{}, fc.Now()).Jobs(accounting.SortStarted, false) {
		byID[item.ID] = item
	}
	if a := byID["job-a"]; a.Status != string(agentjobs.OutcomeInterrupted) || a.DurationMS != nil {
		t.Fatalf("open job of the dead process = %+v, want interrupted with unknown duration", a)
	}
	if b := byID["job-b"]; b.RunOffsetMS != nil || b.DurationMS == nil || *b.DurationMS != 360_000 {
		t.Fatalf("job ending after the last record = %+v, want its duration and no timeline place", b)
	}
	c := byID["job-c"]
	if c.Status != accounting.JobRunning || c.RunOffsetMS == nil || *c.RunOffsetMS != 360_000 {
		t.Fatalf("job of the resumed process = %+v, want running at offset 360000 (5m before the crash + 1m)", c)
	}
}

// The read model's stage and participant metadata: registry stages with their first start, every task by its
// participant ID and the Assistant. It is rebuilt only when the journal moves.
func TestAnalyticsMeta_DescribesStagesAndParticipants(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	registerTestSideAgent(t, o)
	stubPassingHelper(o)
	before := o.AnalyticsMeta()
	if again := o.AnalyticsMeta(); again.Revision != before.Revision {
		t.Fatalf("revision moved without a journal change: %d -> %d", before.Revision, again.Revision)
	}
	if len(before.Stages) != 1 || before.Stages[0].ID != assistantsTestStageID || !before.Stages[0].StartedAt.IsZero() {
		t.Fatalf("stages before start = %+v", before.Stages)
	}

	activateStage(t, o, assistantsTestStageID)
	taskID := attachTestTask(context.Background(), t, o, "op-meta")
	meta := o.AnalyticsMeta()
	if meta.Revision == before.Revision {
		t.Fatal("revision did not move after the journal changed")
	}
	history, _ := store.History()
	var firstStart time.Time
	for _, tr := range history {
		if tr.StageID == assistantsTestStageID && tr.To == state.StatusRunning {
			firstStart = tr.Time
			break
		}
	}
	if firstStart.IsZero() || !meta.Stages[0].StartedAt.Equal(firstStart) {
		t.Fatalf("stage start = %s, want %s", meta.Stages[0].StartedAt, firstStart)
	}
	agents := map[string]accounting.AgentMeta{}
	for _, a := range meta.Agents {
		agents[a.ID] = a
	}
	if a := agents["stage:"+assistantsTestStageID]; a.Kind != accounting.AgentKindStage {
		t.Fatalf("stage agent = %+v", a)
	}
	if a := agents[string(meshHelperParticipantID(taskID))]; a.Kind != accounting.AgentKindHelper || a.Title != "Reviewer" {
		t.Fatalf("helper agent = %+v", a)
	}
	if a := agents["side:run1"]; a.Kind != accounting.AgentKindAssistant || a.Title != "Assistant" {
		t.Fatalf("assistant agent = %+v", a)
	}
}

// A helper whose wait is answered before its process exits still yielded: the runtime resumes it for the
// answer, so its first job is yielded and the turn after it ok.
func TestAgentJobs_HelperTurnAnsweredBeforeItsExitIsYielded(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	jobs, err := agentjobs.Open(o.opts.RunDir, agentjobs.Header{RecordingStartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jobs.Close() })
	o.opts.Jobs = jobs
	activateStage(t, o, assistantsTestStageID)
	agent := newYieldingAgent(t)
	agent.useRealExecutor(o)

	taskID := deriveTaskID("op-yield-early")
	helperID := meshHelperParticipantID(taskID)
	attachTestTask(context.Background(), t, o, "op-yield-early")
	agent.waitStarted()
	registerHelperWait(t, o, helperID, deriveMeshThreadID("op-yield-open"))
	replyToHelper(t, o, helperID)
	agent.release()
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 5*time.Second)
	o.concurrency.WaitAgents()

	var outcomes []agentjobs.Outcome
	for _, rec := range recordsOf(jobs.Snapshot(), agentjobs.TypeFinished) {
		outcomes = append(outcomes, rec.Outcome)
	}
	if !slices.Equal(outcomes, []agentjobs.Outcome{agentjobs.OutcomeYielded, agentjobs.OutcomeOK}) {
		t.Fatalf("outcomes = %v, want [yielded ok]", outcomes)
	}
}

// A stage author's job ends the way its turn does: a wait it yielded on and that was answered before the
// process exited makes the job yielded; a wait that was ready at registration does not.
func TestAgentJobs_StageAuthorJobYieldsLikeItsTurn(t *testing.T) {
	for _, tc := range []struct {
		name      string
		readyWait bool
		want      bool
	}{
		{"wait answered before the exit", false, true},
		{"wait ready at registration", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExitFixture(t)
			jobs, err := agentjobs.Open(f.o.opts.RunDir, agentjobs.Header{RecordingStartedAt: time.Now()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = jobs.Close() })
			f.o.opts.Jobs = jobs
			job := f.o.jobConfig(f.o.stageJobMeta(flow.Stage{ID: assistantsTestStageID}, phaseImplementation, "agent"), f.stage)

			ctx := context.Background()
			sent, err := f.o.Send(ctx, SendRequest{
				OperationID: "op-ask", ThreadID: f.thread, Sender: f.stage, Recipients: []agentcomm.ParticipantID{meshUserParticipantID},
				Kind: agentcomm.MessageKindMessage, Body: "which format?", Actor: state.Actor{Kind: "assistant", ID: assistantsTestStageID},
			})
			if err != nil {
				t.Fatal(err)
			}
			mid := sent.MessageID
			answer := func() {
				if _, err := f.o.Send(ctx, SendRequest{
					OperationID: "op-answer", ThreadID: f.thread, Sender: meshUserParticipantID, Recipients: []agentcomm.ParticipantID{f.stage},
					Kind: agentcomm.MessageKindReply, InReplyTo: &mid, Body: "JSON",
				}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.readyWait {
				answer()
			}
			if _, err := f.o.Wait(ctx, WaitRequest{
				OperationID: "op-wait", Waiter: f.stage, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: mid},
			}); err != nil {
				t.Fatal(err)
			}
			if tc.readyWait {
				f.o.deliverRenderedInbox(f.stage)() // the agent read the answer in the same turn
			} else {
				answer() // arrives while the process is exiting
			}

			if job.Yielded == nil || job.Yielded("") != tc.want {
				t.Fatalf("job yielded = %v, want %v", job.Yielded != nil && job.Yielded(""), tc.want)
			}
		})
	}
}

// The Assistant outlives the flow: after the run ended, a job that crossed the end or started after it is
// running, its elapsed time grows and it is active work outside the run total until its finished record closes
// it. Only the job that started after the end has no place on the run timeline.
func TestAnalytics_AssistantJobsAfterTheFlowEndedAreRunning(t *testing.T) {
	rig := newJobsTestRig(t, flow.Stage{ID: "build", Command: "stub"})
	side, err := agentjobs.Open(t.TempDir(), agentjobs.Header{RecordingStartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = side.Close() })
	job := rig.o.AssistantJob(side)
	if err := rig.store.BeginRun(); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	publishStarted(t, job, "across", t0)
	if err := rig.store.EndRun(state.RunStatusFinished); err != nil {
		t.Fatal(err)
	}
	publishStarted(t, job, "after", t0.Add(time.Second))
	a := accounting.NewAnalytics(accounting.AnalyticsSources{Usage: rig.acct, Jobs: rig.jobs, Meta: rig.o.AnalyticsMeta,
		Assistant: &accounting.AssistantSources{Jobs: side}})
	at := func(d time.Duration) *accounting.Selection { return a.Facts().Select(accounting.Filter{}, t0.Add(d)) }
	jobByID := func(sel *accounting.Selection, id string) accounting.JobItem {
		t.Helper()
		for _, it := range sel.Jobs(accounting.SortStarted, false) {
			if it.ID == id {
				return it
			}
		}
		t.Fatalf("job %s is not listed", id)
		return accounting.JobItem{}
	}

	for _, poll := range []time.Duration{2 * time.Second, 5 * time.Second} {
		sel := at(poll)
		for id, start := range map[string]time.Duration{"across": 0, "after": time.Second} {
			it := jobByID(sel, id)
			want := (poll - start).Milliseconds()
			if it.Status != accounting.JobRunning || it.DurationMS == nil || *it.DurationMS != want || !it.Agent.OutsideRunTotal {
				t.Fatalf("at +%s job %s = status %q duration %v outside %v, want running for %dms outside the run total",
					poll, id, it.Status, deref(it.DurationMS), it.Agent.OutsideRunTotal, want)
			}
		}
		if out := sel.Outside(); out == nil || out.ActiveJobs != 2 || out.Tokens.Pending != 2 {
			t.Fatalf("at +%s outside = %+v, want two active jobs with pending usage", poll, out)
		}
	}
	if jobByID(at(2*time.Second), "across").RunOffsetMS == nil || jobByID(at(2*time.Second), "after").RunOffsetMS != nil {
		t.Fatal("only the job that started after the flow ended has no run offset")
	}

	publishFinished(t, job, "after", t0.Add(3*time.Second), 2*time.Second)
	sel := at(5 * time.Second)
	if it := jobByID(sel, "after"); it.Status != string(agentjobs.OutcomeOK) || it.DurationMS == nil || *it.DurationMS != 2000 {
		t.Fatalf("finished job = status %q duration %v, want ok for 2000ms", it.Status, deref(it.DurationMS))
	}
	if out := sel.Outside(); out == nil || out.ActiveJobs != 1 {
		t.Fatalf("outside = %+v, want only the job still running", out)
	}
}

// A stage migrated from state.json ran before anything recorded it, whatever status it was found in: the
// metadata says so, and the read model never takes its zero recorded jobs for a fact.
func TestAnalyticsMeta_StageMigratedFromStateJSONHasAnUnknownStart(t *testing.T) {
	o, acct, jobs := newLegacyMigratedOrchestrator(t)
	meta := o.AnalyticsMeta()
	for _, s := range meta.Stages {
		if !s.StartUnknown {
			t.Fatalf("stage %s = %+v, want an unknown start", s.ID, s)
		}
	}
	a := accounting.NewAnalytics(accounting.AnalyticsSources{Usage: acct, Jobs: jobs, Meta: o.AnalyticsMeta})
	snap := a.Facts().StageSnapshot("old", time.Now(), false)
	if snap.Jobs.Complete || !slices.Contains(snap.Jobs.Reasons, agentjobs.GapStartedBeforeRecording) {
		t.Fatalf("jobs = %+v, want unproven with started_before_recording", snap.Jobs)
	}
	c := a.Facts().Select(accounting.Filter{Scope: accounting.FilterStage, StageID: "old"}, time.Now()).Completeness()
	if c.Time.Status == accounting.CompletenessFull {
		t.Fatalf("time completeness = %+v, want unproven", c.Time)
	}
}

// newLegacyMigratedOrchestrator is a run found only as state.json (old done, gate paused) that this binary
// opened with fresh journals.
func newLegacyMigratedOrchestrator(t *testing.T) (*Orchestrator, *accounting.Store, *agentjobs.Store) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"stages":{"old":{"status":"done"},"gate":{"status":"paused"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(dir, []string{"old", "gate"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	jobs, err := agentjobs.Open(dir, agentjobs.Header{RecordingStartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jobs.Close() })
	acct, err := accounting.Open(dir, accounting.NewResolver(accounting.PricingConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = acct.Close() })
	o := New(Options{RunDir: dir, Store: store, Jobs: jobs, Accounting: acct, Config: config.Default(), RunID: jobsTestRunID,
		Stages: []flow.Stage{{ID: "old"}, {ID: "gate"}}})
	if !store.Snapshot().Stages["old"].Timing.Incomplete {
		t.Fatal("the fixture must have unknown legacy history")
	}
	return o, acct, jobs
}

// Every task names the participant it runs as, in either ID form, and the Assistant is always named: usage that
// carries only a task ID, or Assistant usage without its job, then finds the same agent as the jobs.
func TestAnalyticsMeta_TasksAndTheAssistantAreAlwaysNamed(t *testing.T) {
	legacy := agentcomm.ParticipantID("run:qa")
	newRun := meshRunParticipantID("t-new")
	rs := state.RunState{Assistants: state.AssistantsState{
		Tasks: map[assistants.TaskID]*state.TaskState{
			"t-1":   {Task: assistants.Task{ID: "t-1", Scope: assistants.ScopeStage, RootStageID: "build", ProfileID: "reviewer", Title: "Review"}},
			"t-old": {Task: assistants.Task{ID: "t-old", Scope: assistants.ScopeRun, ProfileID: "qa", Title: "QA"}},
			"t-new": {Task: assistants.Task{ID: "t-new", Scope: assistants.ScopeRun, ProfileID: "qa"}},
		},
		Mesh: state.MeshState{Participants: map[agentcomm.ParticipantID]*agentcomm.Participant{
			legacy: {ID: legacy, Kind: agentcomm.KindRunAgent, Scope: agentcomm.ScopeRun},
			newRun: {ID: newRun, Kind: agentcomm.KindRunAgent, Scope: agentcomm.ScopeRun},
		}},
	}}
	got := participantsMeta(&rs, "side:run1")
	want := []accounting.AgentMeta{
		{ID: "helper:t-1", Kind: accounting.AgentKindHelper, Title: "Review", TaskID: "t-1"},
		{ID: "run:t-new", Kind: accounting.AgentKindRunAgent, Title: "qa", TaskID: "t-new"},
		{ID: "run:qa", Kind: accounting.AgentKindRunAgent, Title: "QA", TaskID: "t-old"},
		{ID: "side:run1", Kind: accounting.AgentKindAssistant, Title: "Assistant"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("participants = %+v\nwant %+v", got, want)
	}
}
