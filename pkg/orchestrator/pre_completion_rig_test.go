package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/state"
)

const pcStageID = "work"

// pcLog is the order in which the fake author, the helpers and verify did their work.
type pcLog struct {
	mu    sync.Mutex
	steps []string
}

func (l *pcLog) add(step string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.steps = append(l.steps, step)
}

func (l *pcLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.steps)
}

// pcAuthor is the stage's agent: each turn logs itself, keeps its prompt and writes the summary that completes it.
type pcAuthor struct {
	log   *pcLog
	turns atomic.Int32
	// hold, when set, blocks every turn until the test closes it.
	hold            chan struct{}
	constantSummary bool
	// onTurn, when set, runs at the start of each turn with its number and log file.
	onTurn func(n int, logFile string)
	// blockTurn, when not 0, makes that numbered turn wait for the run to stop, as a turn cut short by Ctrl+C.
	blockTurn atomic.Int32
	// summaryFor, when set, decides what a turn writes as its summary: the text, and false to write nothing.
	summaryFor func(n int) (string, bool)
	// holdAfterSummary, when set, keeps a turn alive after its summary is written until the test closes it.
	holdAfterSummary chan struct{}
	// summaryWritten gets a signal each time a turn has written its summary.
	summaryWritten chan struct{}
	// blockAfterWriting, when not 0, makes that numbered turn write its summary and then wait for the run to stop: a crash
	// after the author's marker is on disk and before the process exits.
	blockAfterWriting atomic.Int32

	mu      sync.Mutex
	prompts []string
}

func (a *pcAuthor) RunPlanning(context.Context, string, string, string, string) error {
	return errors.New("unexpected planning turn")
}

func (a *pcAuthor) RunJSONQuery(context.Context, string) ([]byte, error) { return nil, nil }

func (a *pcAuthor) RunAgent(ctx context.Context, _, _, prompt, logFile string) error {
	n := a.turns.Add(1)
	a.mu.Lock()
	a.prompts = append(a.prompts, prompt)
	a.mu.Unlock()
	a.log.add(fmt.Sprintf("author:%d", n))
	if a.onTurn != nil {
		a.onTurn(int(n), logFile)
	}
	if a.blockTurn.Load() == n {
		<-ctx.Done()
		return ctx.Err()
	}
	if a.hold != nil {
		select {
		case <-a.hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	summary := fmt.Sprintf("## Summary\nturn %d\n", n)
	if a.constantSummary {
		summary = "## Summary\nthe same\n"
	}
	if a.summaryFor != nil {
		var write bool
		if summary, write = a.summaryFor(int(n)); !write {
			return nil
		}
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(logFile), "execution_summary.md"), []byte(summary), 0o644); err != nil {
		return err
	}
	select {
	case a.summaryWritten <- struct{}{}:
	default:
	}
	if a.holdAfterSummary != nil {
		select {
		case <-a.holdAfterSummary:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if a.blockAfterWriting.Load() == n {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (a *pcAuthor) prompt(i int) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if i >= len(a.prompts) {
		return ""
	}
	return a.prompts[i]
}

// pcAnswer is what a helper says in one round.
type pcAnswer struct {
	// raw, when set, is the helper's whole answer text.
	raw       string
	verdict   assistants.Verdict
	summary   string
	suggested string
	err       error
}

func (a pcAnswer) outcome() (executor.HelperOutcome, error) {
	if a.err != nil {
		return executor.HelperOutcome{}, a.err
	}
	if a.raw != "" {
		return executor.HelperOutcome{ProcessOK: true, RawText: a.raw}, nil
	}
	suggested := ""
	if a.suggested != "" {
		suggested = fmt.Sprintf(`,"suggested_change":%q`, a.suggested)
	}
	raw := fmt.Sprintf(`{"schema_version":1,"kind":"result","verdict":%q,"summary":%q%s}`, a.verdict, a.summary, suggested)
	return executor.HelperOutcome{ProcessOK: true, RawText: raw}, nil
}

var (
	pcPass  = pcAnswer{verdict: assistants.VerdictPass, summary: "fine"}
	pcFixIt = pcAnswer{verdict: assistants.VerdictNeedsChanges, summary: "the retry loop leaks", suggested: "close the connection in defer"}
)

// pcWakeClock is a fake clock that tells when a usage-limit wake timer (a wait of a minute or more) is registered, so
// a test moves the clock only once the wake is armed.
type pcWakeClock struct {
	*clock.FakeClock
	armed chan time.Duration
}

func (c *pcWakeClock) After(d time.Duration) <-chan time.Time {
	ch := c.FakeClock.After(d)
	if d >= time.Minute {
		select {
		case c.armed <- d:
		default:
		}
	}
	return ch
}

// awaitWakeTimer blocks until a wake timer has been registered since the last call.
func (c *pcWakeClock) awaitWakeTimer(t *testing.T) {
	t.Helper()
	select {
	case <-c.armed:
	case <-time.After(10 * time.Second):
		t.Fatal("no usage-limit wake timer was registered")
	}
}

// pcHelperSpec is one declared pre_completion helper and its answers by call index; the last repeats.
type pcHelperSpec struct {
	title    string
	advisory bool
	// delegate lets the helper hand work to children.
	delegate bool
	answers  []pcAnswer
}

// pcRigOptions varies the stage and the helpers' answers of a rig.
type pcRigOptions struct {
	maxFailures *int
	noVerify    bool
	noHelper    bool
	advisory    bool
	hold        bool
	// constantSummary makes every author turn write the same summary.
	constantSummary bool
	// freshCorrection is the stage's verify.fresh_correction (nil: the default).
	freshCorrection *bool
	// sharedSlot gives the author and the helpers one command with one parallel slot.
	sharedSlot bool
	// noAssistants runs without the collaboration capability.
	noAssistants bool
	exitOnFail   bool
	// interactive makes the stage interactive. The fake author is no interactive runner: set it only for a restart
	// whose stage does not call its author again, or with authorCommand.
	interactive bool
	// authorCommand, when set, is the stage's command: the script an interactive stage runs as its author.
	authorCommand string
	// keepAlive adds an unrelated long script stage, so the run outlives a failed stage (as a dashboard run does).
	keepAlive bool
	// clock, when set, replaces the real clock (usage-limit waits are timed on it).
	clock *pcWakeClock
	// verifyAgent, when set, makes verify one agent step answered by it (call numbers from 1) instead of the shell step.
	verifyAgent func(call int) (verify.RunOutcome, error)
	// blockVerify makes the first verify wait for the run to stop.
	blockVerify bool
	// holdHelper blocks every helper turn until the test releases it (or the run stops); holdHelperFrom, when not 0,
	// holds only each helper's turns from that number on.
	holdHelper     bool
	holdHelperFrom int
	// dir, when set, is an existing run directory (a journal captured earlier) the process starts over.
	dir string
	// patience and verifyFailures are the stage's verify.review_patience and verify.max_failures.
	patience       *int
	verifyFailures *int
	// answers are the answers of the one default helper; helpers, when set, replaces it.
	answers []pcAnswer
	helpers []pcHelperSpec
	// beforeRun runs on the orchestrator before Run starts (seams of a test).
	beforeRun func(*pcRig)
}

// pcRig is a real Run over one autonomous stage: a fake author, one pre_completion helper answered from a script
// and verify as a shell step the fake logs.
type pcRig struct {
	t      *testing.T
	dir    string
	o      *Orchestrator
	store  *state.Store
	author *pcAuthor
	log    *pcLog
	calls  atomic.Int32
	stop   func() // cancels the run, waits until it and its agents are gone, checks what Run returned, closes the journal
	opts   pcRigOptions
	// helperCmd is the command the helpers run under.
	helperCmd string

	blockVerify atomic.Bool
	verifyCalls atomic.Int32
	restarted   bool
	releaseOnce sync.Once
	helperGate  chan struct{}
	helperOnce  sync.Once
	runDone     chan struct{}
	mu          sync.Mutex
	perHelper   map[string]int
}

func newPCWakeClock(start time.Time) *pcWakeClock {
	return &pcWakeClock{FakeClock: clock.NewFake(start), armed: make(chan time.Duration, 16)}
}

func newPCRig(t *testing.T, opts pcRigOptions) *pcRig {
	t.Helper()
	dir := opts.dir
	if dir == "" {
		dir = t.TempDir()
	}
	r := &pcRig{t: t, dir: dir, log: &pcLog{}, opts: opts}
	r.author = &pcAuthor{log: r.log, constantSummary: opts.constantSummary, summaryWritten: make(chan struct{}, 16)}
	if opts.hold {
		r.author.hold = make(chan struct{})
	}
	if opts.holdHelper {
		r.helperGate = make(chan struct{})
	}
	r.blockVerify.Store(opts.blockVerify)
	r.start(opts.dir != "")
	return r
}

// releaseHelpers lets held helper turns answer.
func (r *pcRig) releaseHelpers() { r.helperOnce.Do(func() { close(r.helperGate) }) }

// start opens the run's journal and starts one afm process over it; resumed marks a restart.
func (r *pcRig) start(resumed bool) {
	t := r.t
	stage := flow.Stage{ID: pcStageID, Name: "Work", Agents: []flow.AgentType{flow.AgentAuto}, Interactive: r.opts.interactive, Command: r.opts.authorCommand}
	helperCmd, cfg := "helper-cmd", config.Default()
	if r.opts.sharedSlot { // the helpers run the client's own command, so they queue for the author's slot
		helperCmd, cfg.Executor.MaxParallel = cfg.Client.Command, 1
	}
	if !r.opts.noVerify && r.opts.verifyAgent != nil {
		stage.Verify = flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex-as-claude"}}}
	} else if !r.opts.noVerify {
		stage.Verify = flow.NewShellVerify("verify-cmd")
	}
	if !r.opts.noVerify {
		stage.Verify.ReviewPatience, stage.Verify.MaxFailures = r.opts.patience, r.opts.verifyFailures
		stage.Verify.FreshCorrection = r.opts.freshCorrection
	}
	if r.opts.maxFailures != nil {
		stage.PreCompletion = &flow.PreCompletionSpec{MaxFailures: r.opts.maxFailures}
	}
	for _, h := range r.helperSpecs() {
		mode := assistants.ModeRequired
		if h.advisory {
			mode = assistants.ModeAdvisory
		}
		stage.Assistants = append(stage.Assistants, flow.StageAssistant{Profile: strings.ToLower(h.title), Title: h.title, Instruction: "review the work", Mode: mode, Trigger: assistants.TriggerPreCompletion})
	}
	stages, ids := []flow.Stage{stage}, []string{pcStageID}
	if r.opts.keepAlive {
		stages, ids = append(stages, flow.Stage{ID: "keeper", Name: "Keeper", Script: "sleep 20"}), append(ids, "keeper")
	}
	store, err := state.Open(r.dir, ids)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-1"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}
	var profiles []assistants.Profile
	for _, h := range r.helperSpecs() {
		profiles = append(profiles, assistants.Profile{ID: assistants.ProfileID(strings.ToLower(h.title)), Title: h.title, Command: helperCmd, Capabilities: assistants.Capabilities{Delegate: h.delegate}})
	}
	profiles = append(profiles, assistants.Profile{ID: "late", Title: "Late", Command: "helper-cmd"})
	reg, err := assistants.BuildRegistry(nil, profiles, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.store, r.helperCmd = store, helperCmd
	oo := Options{
		RunDir: r.dir, RootDir: t.TempDir(), Store: store, Config: cfg, Runner: r.author, Prompts: DefaultPrompts(), Resumed: resumed,
		Stages: stages, AssistantsEnabled: !r.opts.noAssistants, AssistantsRegistry: reg, ExitOnFail: r.opts.exitOnFail,
	}
	if r.opts.clock != nil {
		oo.Clock = r.opts.clock
	}
	r.o = New(oo)
	r.o.helperAccess = allowHelpersOS
	if r.opts.verifyAgent != nil {
		r.o.runVerifyAgent = func(_ context.Context, _ flow.Stage, _, _, _, _ string) (verify.RunOutcome, error) {
			r.log.add("verify")
			return r.opts.verifyAgent(int(r.verifyCalls.Add(1)))
		}
	}
	r.o.runVerifyShell = func(ctx context.Context, _, _, _ string) (string, error) {
		r.log.add("verify")
		if r.blockVerify.CompareAndSwap(true, false) {
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "", nil
	}
	r.o.runHelperAgent = func(ctx context.Context, taskID, _, _, prompt, _, _ string) (executor.HelperOutcome, error) {
		title := r.store.Snapshot().Assistants.Tasks[assistants.TaskID(taskID)].Title
		r.calls.Add(1)
		own := r.helperCalls(title)
		r.log.add(fmt.Sprintf("helper:%s:%d", title, own))
		if r.helperGate != nil && own >= r.opts.holdHelperFrom {
			select {
			case <-r.helperGate:
			case <-ctx.Done():
				return executor.HelperOutcome{}, ctx.Err()
			}
		}
		for _, h := range r.helperSpecs() {
			if h.title != title {
				continue
			}
			answers := h.answers
			if len(answers) == 0 {
				answers = []pcAnswer{pcPass}
			}
			return answers[min(own-1, len(answers)-1)].outcome()
		}
		return pcPass.outcome()
	}
	if r.opts.beforeRun != nil {
		r.opts.beforeRun(r)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	done := make(chan struct{})
	r.runDone = done
	var runErr error
	go func() { runErr = r.o.Run(ctx); close(done) }()
	exitOnFail := r.opts.exitOnFail
	r.stop = sync.OnceFunc(func() {
		cancel()
		<-done
		for _, err := range unexpectedRunErrs(runErr, exitOnFail) {
			t.Errorf("Run returned %v", err)
		}
		if err := store.Close(); err != nil {
			t.Errorf("close the journal: %v", err)
		}
	})
	t.Cleanup(r.stop)
}

// unexpectedRunErrs is what in Run's error is not the stop the test made: the cancellation, and under exit_on_fail the
// stage failure that ended the run. Every part of a joined error is checked, so nothing hides beside a cancellation.
func unexpectedRunErrs(err error, exitOnFail bool) []error {
	switch e := err.(type) {
	case nil:
		return nil
	case interface{ Unwrap() []error }:
		var out []error
		for _, part := range e.Unwrap() {
			out = append(out, unexpectedRunErrs(part, exitOnFail)...)
		}
		return out
	case interface{ Unwrap() error }:
		if len(unexpectedRunErrs(e.Unwrap(), exitOnFail)) > 0 {
			return []error{err}
		}
		return nil
	}
	var failed *StageFailedError
	if errors.Is(err, context.Canceled) || (exitOnFail && errors.As(err, &failed)) {
		return nil
	}
	return []error{err}
}

// helperSpecs is the helpers the rig declares: the default one unless the options list them.
func (r *pcRig) helperSpecs() []pcHelperSpec {
	switch {
	case len(r.opts.helpers) > 0:
		return r.opts.helpers
	case r.opts.noHelper:
		return nil
	default:
		return []pcHelperSpec{{title: "Reviewer", advisory: r.opts.advisory, answers: r.opts.answers}}
	}
}

// helperCalls counts a helper's turn and returns its number.
func (r *pcRig) helperCalls(title string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.perHelper == nil {
		r.perHelper = map[string]int{}
	}
	r.perHelper[title]++
	return r.perHelper[title]
}

func (r *pcRig) stage() state.StageState { return r.store.Snapshot().Stages[pcStageID] }

func (r *pcRig) waitStatus(want state.StageStatus) {
	r.t.Helper()
	r.waitFor(string(want), func(st state.StageState) bool { return st.Status == want })
}

func (r *pcRig) waitFor(what string, ok func(state.StageState) bool) {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok(r.stage()) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	st := r.stage()
	r.t.Fatalf("stage did not reach %s: status %s, round %+v, log %v", what, st.Status, st.PreCompletion, r.log.list())
}

func (r *pcRig) order() string { return strings.Join(r.log.list(), ",") }

// awaitSummary blocks until an author turn has written its summary.
func (r *pcRig) awaitSummary() {
	r.t.Helper()
	select {
	case <-r.author.summaryWritten:
	case <-time.After(10 * time.Second):
		r.t.Fatal("the author wrote no summary")
	}
}

// releaseAuthor lets a held author turn finish.
func (r *pcRig) releaseAuthor() { r.releaseOnce.Do(func() { close(r.author.hold) }) }

// restart is a crash at this point: the run directory as it is now goes to a copy, the running process is stopped
// (cancelled, drained, its error checked, its journal closed) and another process starts over the copy, so nothing
// the old one did after the crash point reaches the new one.
func (r *pcRig) restart() {
	r.t.Helper()
	captured := r.t.TempDir()
	if err := os.CopyFS(captured, os.DirFS(r.dir)); err != nil {
		r.t.Fatalf("capture the run directory: %v", err)
	}
	r.stop()
	r.dir = captured
	r.start(true)
}

// awaitTaskIdle blocks until the turn that holds the task's slot has ended, its commit included: a probe takes the
// slot only after that turn gave it up.
func (r *pcRig) awaitTaskIdle(id assistants.TaskID) {
	r.t.Helper()
	probed := make(chan struct{})
	r.o.concurrency.SpawnOwned(context.Background(), concurrency.TaskOwnerKey(string(id)), r.helperCmd, nil, func(context.Context, *concurrency.Lease) {
		close(probed)
	})
	select {
	case <-probed:
	case <-time.After(10 * time.Second):
		r.t.Fatal("the helper's turn did not end")
	}
}

// awaitStageIdle blocks until the turn that holds the stage has returned, its deferred work included: a probe gets the
// stage only after that turn gave it up. The stage must not be paused, or the probe is never run.
func (r *pcRig) awaitStageIdle() {
	r.t.Helper()
	probed := make(chan struct{})
	r.o.concurrency.SpawnAgent(context.Background(), *r.o.reg().Stage(pcStageID), func(context.Context, flow.Stage) {
		close(probed)
	})
	select {
	case <-probed:
	case <-time.After(10 * time.Second):
		r.t.Fatal("the stage's turn did not end")
	}
}

// The rig's check of what Run returned looks at every part of a joined error: a storage failure beside the cancellation
// that stopped the run fails the test, a closed journal included.
func TestPCRig_RunErrorCheckSeesEveryJoinedPart(t *testing.T) {
	storage := fmt.Errorf("persist run completion: %w", state.ErrStoreClosed)
	failed := &StageFailedError{StageID: pcStageID, Reason: "helper died"}
	for _, tc := range []struct {
		name       string
		err        error
		exitOnFail bool
		unexpected int
	}{
		{"nothing", nil, false, 0},
		{"cancellation", context.Canceled, false, 0},
		{"wrapped cancellation", fmt.Errorf("run: %w", context.Canceled), false, 0},
		{"cancellation and a closed journal", errors.Join(context.Canceled, storage), false, 1},
		{"cancellation and a write error", errors.Join(context.Canceled, errors.New("write events.jsonl: invalid argument")), false, 1},
		{"a join wrapped once more", fmt.Errorf("run: %w", errors.Join(context.Canceled, storage)), false, 1},
		{"the closed journal alone", state.ErrStoreClosed, false, 1},
		{"the stage failure under exit_on_fail", errors.Join(failed, context.Canceled), true, 0},
		{"the stage failure without exit_on_fail", failed, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unexpectedRunErrs(tc.err, tc.exitOnFail); len(got) != tc.unexpected {
				t.Fatalf("unexpected parts of %v = %v, want %d", tc.err, got, tc.unexpected)
			}
		})
	}
}
