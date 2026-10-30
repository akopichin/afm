package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// The restart table runs the real startup path (Run, and Continue for a paused stage) over a journal seeded with
// what the retry loop writes around a summary correction, and checks what every restart does with the stage.

const (
	restartStage = "s"
	restartAfter = "d"
)

// restartAuthor records every turn the orchestrator starts and, like a working author, writes the summary.
type restartAuthor struct {
	store *state.Store
	// turn, when set, plays the n-th turn of restartStage: whether the author writes the summary, and its error.
	turn func(n int) (writes bool, err error)

	mu       sync.Mutex
	phases   []string // the phase of each author turn of restartStage
	prompts  []string
	planned  int  // planning turns
	afterRun bool // restartAfter started before restartStage was done
}

func (a *restartAuthor) RunPlanning(context.Context, string, string, string, string) error {
	a.mu.Lock()
	a.planned++
	a.mu.Unlock()
	return errors.New("unexpected planning turn")
}

func (a *restartAuthor) RunAgent(_ context.Context, phase, _, prompt, logFile string) error {
	stageDir := filepath.Dir(logFile)
	a.mu.Lock()
	turn := 0
	if stage := filepath.Base(stageDir); stage == restartStage {
		a.phases = append(a.phases, phase)
		a.prompts = append(a.prompts, prompt)
		turn = len(a.prompts)
	} else if a.store.Get(restartStage) != state.StatusDone {
		a.afterRun = true
	}
	a.mu.Unlock()
	if a.turn != nil && turn > 0 {
		if writes, err := a.turn(turn); err != nil || !writes {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(stageDir, stagefiles.SummaryFile), []byte("## Summary\nDone.\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done"), 0o644)
}

func (a *restartAuthor) RunJSONQuery(context.Context, string) ([]byte, error) { return nil, nil }

// seedStep is one journal transition of restartStage.
type seedStep struct {
	to      state.StageStatus
	event   bus.FSMEvent
	summary *state.SummaryStep
	quota   *state.QuotaWait
}

var (
	seedStart     = []seedStep{{to: state.StatusReady, event: bus.EvReady}, {to: state.StatusRunning, event: bus.EvStartRun}}
	seedReserve   = seedStep{to: state.StatusRetrying, event: bus.EvScheduleRetry, summary: &state.SummaryStep{Source: state.SummarySourcePostExit, Reason: "missing"}}
	seedResume    = seedStep{to: state.StatusRunning, event: bus.EvResumeAfterRetry}
	seedIssue     = seedStep{to: state.StatusRunning, event: bus.EvReserveSummaryHook, summary: &state.SummaryStep{Source: state.SummarySourcePostExitIssued}}
	seedTransport = seedStep{to: state.StatusRetrying, event: bus.EvScheduleRetry}
	seedReady     = seedStep{to: state.StatusReady, event: bus.EvReady}
	seedAsk       = seedStep{to: state.StatusAwaitingUserInput, event: bus.EvAskUser}
	seedPause     = seedStep{to: state.StatusPaused, event: bus.EvPause}
	seedQuota     = seedStep{to: state.StatusAwaitingQuota, event: bus.EvAwaitQuota, quota: &state.QuotaWait{
		Provider: "claude", Until: time.Now().Add(-time.Hour), Target: phaseAutonomous, Waits: 1,
	}}
)

func seedSteps(parts ...any) []seedStep {
	var out []seedStep
	for _, p := range parts {
		switch v := p.(type) {
		case seedStep:
			out = append(out, v)
		case []seedStep:
			out = append(out, v...)
		default:
			panic(fmt.Sprintf("unsupported seed part %T", p))
		}
	}
	return out
}

// How the stage is declared and which files its directory holds when AFM restarts.
type restartStageKind int

const (
	stageAuto        restartStageKind = iota // agents: [auto]
	stageAutoDialog                          // agents: [auto], interactive, with its session file
	stageLegacyPlans                         // planning stage that turned autonomous (autonomous.flag)
)

type restartRow struct {
	name    string
	kind    restartStageKind
	journal []seedStep
	summary bool // execution_summary.md is on disk
	stale   bool // a .done left by an earlier attempt is on disk
	paused  bool // the stage is continued by the user after the start
	// wantTurn is the correction the author's turn carries ("(k of 2)"); 0 = a turn without a correction,
	// -1 = no turn: the stage completes from the file alone.
	wantTurn int
	// wantPostExit is the correction count in the journal when the stage is done.
	wantPostExit int
}

const (
	noTurn        = -1
	plainTurn     = 0
	firstCorrect  = 1
	secondCorrect = 2
)

func restartRows() []restartRow {
	reserved := seedSteps(seedStart, seedReserve, seedResume)
	issued := seedSteps(reserved, seedIssue)
	return []restartRow{
		// running: the author's process died with the stage running
		{name: "running, nothing reserved", journal: seedSteps(seedStart), wantTurn: plainTurn, wantPostExit: 0},
		{name: "running, nothing reserved, summary", journal: seedSteps(seedStart), summary: true, wantTurn: noTurn, wantPostExit: 0},
		{name: "running, correction reserved", journal: reserved, wantTurn: firstCorrect, wantPostExit: 1},
		{name: "running, correction reserved, summary", journal: reserved, summary: true, wantTurn: noTurn, wantPostExit: 1},
		{name: "running, correction issued", journal: issued, wantTurn: secondCorrect, wantPostExit: 2},
		{name: "running, correction issued, summary", journal: issued, summary: true, wantTurn: noTurn, wantPostExit: 1},
		{name: "running, correction issued, stale .done", journal: issued, stale: true, wantTurn: secondCorrect, wantPostExit: 2},
		// retrying: a crash during the backoff of a transport retry or of a correction
		{name: "retrying, correction reserved", journal: seedSteps(seedStart, seedReserve), wantTurn: firstCorrect, wantPostExit: 1},
		{name: "retrying, correction reserved, summary", journal: seedSteps(seedStart, seedReserve), summary: true, wantTurn: noTurn, wantPostExit: 1},
		{name: "retrying, correction reserved, stale .done", journal: seedSteps(seedStart, seedReserve), stale: true, wantTurn: firstCorrect, wantPostExit: 1},
		{name: "retrying, correction reserved, summary and .done", journal: seedSteps(seedStart, seedReserve), summary: true, stale: true, wantTurn: noTurn, wantPostExit: 1},
		{name: "retrying after an issued correction", journal: seedSteps(issued, seedTransport), wantTurn: firstCorrect, wantPostExit: 1},
		{name: "retrying after an issued correction, summary", journal: seedSteps(issued, seedTransport), summary: true, wantTurn: noTurn, wantPostExit: 1},
		{name: "retrying after a transport failure, nothing reserved", journal: seedSteps(seedStart, seedTransport), wantTurn: plainTurn, wantPostExit: 0},
		// ready: a crash between the readiness and the start
		{name: "ready, correction reserved", journal: seedSteps(seedStart, seedReserve, seedReady), wantTurn: firstCorrect, wantPostExit: 1},
		// usage limit whose wake is due
		{name: "usage limit after an issued correction", journal: seedSteps(issued, seedQuota), wantTurn: firstCorrect, wantPostExit: 1},
		// a question the user was asked inside the correction
		{name: "question after an issued correction", journal: seedSteps(issued, seedAsk), wantTurn: firstCorrect, wantPostExit: 1},
		{name: "question after an issued correction, interactive", kind: stageAutoDialog, journal: seedSteps(issued, seedAsk), wantTurn: firstCorrect, wantPostExit: 1},
		// paused, then continued by the user
		{name: "paused from running after an issued correction", journal: seedSteps(issued, seedPause), paused: true, wantTurn: firstCorrect, wantPostExit: 1},
		{name: "paused from running, summary", journal: seedSteps(issued, seedPause), paused: true, summary: true, wantTurn: noTurn, wantPostExit: 1},
		{name: "paused from retrying", journal: seedSteps(seedStart, seedReserve, seedPause), paused: true, wantTurn: firstCorrect, wantPostExit: 1},
		{name: "paused from retrying, summary", journal: seedSteps(seedStart, seedReserve, seedPause), paused: true, summary: true, wantTurn: noTurn, wantPostExit: 1},
		// a planning stage that turned autonomous follows the same track
		{name: "legacy, running, correction issued", kind: stageLegacyPlans, journal: issued, wantTurn: secondCorrect, wantPostExit: 2},
		{name: "legacy, running, correction reserved, summary", kind: stageLegacyPlans, journal: reserved, summary: true, wantTurn: noTurn, wantPostExit: 1},
		{name: "legacy, retrying, correction reserved", kind: stageLegacyPlans, journal: seedSteps(seedStart, seedReserve), wantTurn: firstCorrect, wantPostExit: 1},
		{name: "legacy, retrying, correction reserved, summary", kind: stageLegacyPlans, journal: seedSteps(seedStart, seedReserve), summary: true, wantTurn: noTurn, wantPostExit: 1},
		{name: "legacy, paused from retrying", kind: stageLegacyPlans, journal: seedSteps(seedStart, seedReserve, seedPause), paused: true, wantTurn: firstCorrect, wantPostExit: 1},
	}
}

func TestSummaryRestartTable(t *testing.T) {
	for _, row := range restartRows() {
		t.Run(row.name, func(t *testing.T) { runRestartRow(t, row) })
	}
}

func runRestartRow(t *testing.T, row restartRow) {
	t.Helper()
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{restartStage, restartAfter})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stages := seedRestartRun(t, store, runDir, row)

	author := &restartAuthor{store: store}
	o := startRestartRun(t, store, runDir, stages, author)

	if row.paused {
		waitUntilFor(t, 10*time.Second, "Run to start", func() bool {
			o.runMu.Lock()
			defer o.runMu.Unlock()
			return o.runCtx != nil
		})
		if err := o.Continue(context.Background(), restartStage); err != nil {
			t.Fatal(err)
		}
	}
	waitRestartDone(t, store)
	requireRestartOutcome(t, row, store, author, runDir)
}

// startRestartRun runs the orchestrator over the seeded store until the test ends; the cleanup waits for Run, so
// the store is closed after it. tune adjusts the orchestrator before Run starts.
func startRestartRun(t *testing.T, store *state.Store, runDir string, stages []flow.Stage, author *restartAuthor, tune ...func(*Orchestrator)) *Orchestrator {
	t.Helper()
	o := New(Options{RunDir: runDir, Store: store, Config: config.Default(), Runner: author, Stages: stages})
	for _, set := range tune {
		set(o)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	runDone := make(chan struct{})
	go func() { _ = o.Run(ctx); close(runDone) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return within 10s of cancellation")
		}
	})
	return o
}

func waitRestartDone(t *testing.T, store *state.Store) {
	t.Helper()
	waitUntilFor(t, 15*time.Second, "both stages to be done", func() bool {
		return store.Get(restartStage) == state.StatusDone && store.Get(restartAfter) == state.StatusDone
	})
}

// seedRestartRun lays out the stage directory and the journal, and returns the flow.
func seedRestartRun(t *testing.T, store *state.Store, runDir string, row restartRow) []flow.Stage {
	t.Helper()
	stageDir := filepath.Join(runDir, restartStage)
	if err := os.MkdirAll(filepath.Join(runDir, restartAfter), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile := func(name string) {
		if err := os.WriteFile(filepath.Join(stageDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if row.summary {
		if err := os.WriteFile(filepath.Join(stageDir, stagefiles.SummaryFile), []byte("## Summary\nDone.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if row.stale {
		writeFile(".done")
	}
	stage := flow.Stage{ID: restartStage, Name: "S", Agents: []flow.AgentType{flow.AgentAuto}}
	switch row.kind {
	case stageAutoDialog:
		stage.Interactive = true
		writeFile(filepath.Base(stagefiles.SessionFile("", phaseAutonomous)))
	case stageLegacyPlans:
		stage.Agents = []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}
		writeFile("autonomous.flag")
	default:
		// stageAuto needs no extra files.
	}

	for _, step := range row.journal {
		tr := state.Transition{
			StageID: restartStage, From: store.Get(restartStage), To: step.to, Event: string(step.event),
			SummaryStep: step.summary, Quota: step.quota,
		}
		if err := store.Apply(&tr); err != nil {
			t.Fatalf("seed %s: %v", step.event, err)
		}
	}
	return []flow.Stage{stage, {ID: restartAfter, Name: "D", Agents: []flow.AgentType{flow.AgentAuto}, DependsOn: []string{restartStage}}}
}

func requireRestartOutcome(t *testing.T, row restartRow, store *state.Store, author *restartAuthor, runDir string) {
	t.Helper()
	author.mu.Lock()
	defer author.mu.Unlock()

	if author.planned != 0 {
		t.Errorf("planning turns = %d, want none for an autonomous stage", author.planned)
	}
	for _, phase := range author.phases {
		if phase != phaseAutonomous {
			t.Errorf("author turns ran as %v, want only the autonomous phase", author.phases)
			break
		}
	}
	if author.afterRun {
		t.Error("the dependent stage started before the summary stage was done")
	}

	wantTurns := 1
	if row.wantTurn == noTurn {
		wantTurns = 0
	}
	if len(author.prompts) != wantTurns {
		t.Fatalf("author turns = %d (%v), want %d", len(author.prompts), author.phases, wantTurns)
	}
	if wantTurns == 1 {
		requireCorrectionPrompt(t, author.prompts[0], row.wantTurn)
	}

	if got := store.Snapshot().Stages[restartStage].Summary; (got == nil && row.wantPostExit != 0) || (got != nil && got.PostExit != row.wantPostExit) {
		t.Errorf("summary = %+v, want %d post-exit corrections charged", got, row.wantPostExit)
	}

	started := summaryNoticesIn(t, runDir, eventSummaryCorrectionStarted)
	wantStarted := 0
	if row.wantTurn > 0 {
		wantStarted = 1
	}
	if len(started) != wantStarted {
		t.Errorf("correction start notices = %+v, want %d", started, wantStarted)
	}
	reservations := map[string]bool{}
	for _, n := range started {
		if reservations[n.Data["reservation"]] {
			t.Errorf("reservation %s announced twice: %+v", n.Data["reservation"], started)
		}
		reservations[n.Data["reservation"]] = true
	}

	// A correction that got the file through the probe is announced once, whoever wrote the file.
	wantRecovered := 0
	if row.wantPostExit > 0 {
		wantRecovered = 1
	}
	if got := summaryNoticesIn(t, runDir, eventSummaryRecovered); len(got) != wantRecovered {
		t.Errorf("summary_recovered notices = %+v, want %d", got, wantRecovered)
	}
}

// requireCorrectionPrompt checks the "(k of 2)" the turn's prompt carries; k = 0: the turn has none.
func requireCorrectionPrompt(t *testing.T, prompt string, k int) {
	t.Helper()
	for n := 1; n <= maxSummaryPostExit; n++ {
		has := strings.Contains(prompt, fmt.Sprintf("(%d of %d)", n, maxSummaryPostExit))
		if has != (n == k) {
			t.Errorf("prompt has the (%d of 2) correction = %v, want the turn to carry correction %d:\n%s", n, has, k, prompt)
		}
	}
}
