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

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// A stage runner owns the attempt it was started for, not the one it happens to find when it gets a command slot:
// a Pause, Continue or Revise committed while it was queued belongs to whoever took the stage over.

const (
	grantBlocker = "blocker"
	grantImpl    = "impl"
)

// grantRunner plays every stage turn: blocker holds the only command slot until released; impl records which run
// started (plain or feedback) and may yield on a wait through a token minted for its invocation, as the executor does.
type grantRunner struct {
	o        *Orchestrator
	release  chan struct{}
	entered  chan struct{}
	implWait bool
	// beforeMint runs in impl's plain turn after the runner checked its ownership and before the executor mints.
	beforeMint func()

	mu    sync.Mutex
	turns []string
	waitE error
}

func (r *grantRunner) RunPlanning(context.Context, string, string, string, string) error {
	return errors.New("unexpected planning turn")
}

func (r *grantRunner) RunAgent(_ context.Context, _, _, _, logFile string) error {
	stageDir := filepath.Dir(logFile)
	stage := filepath.Base(stageDir)
	if stage == grantBlocker {
		close(r.entered)
		<-r.release
	} else {
		kind := "plain"
		if strings.Contains(filepath.Base(logFile), "feedback") {
			kind = "feedback"
		}
		r.mu.Lock()
		r.turns = append(r.turns, kind)
		r.mu.Unlock()
		if r.implWait && kind == "plain" {
			if r.beforeMint != nil {
				r.beforeMint()
			}
			// The executor mints the invocation's token as the process starts, then the agent waits on a colleague.
			_, token, revoke := r.o.meshClientFor(meshStageParticipantID(grantImpl)).Mint()
			defer revoke()
			_, err := r.o.Wait(WithMeshInvocation(context.Background(), token), WaitRequest{
				OperationID: "op-late-turn-wait", Waiter: meshStageParticipantID(grantImpl), Actor: state.Actor{Kind: "agent", ID: string(meshStageParticipantID(grantImpl))},
				Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-nobody"}, Deadline: testFarDeadline(),
			})
			r.mu.Lock()
			r.waitE = err
			r.mu.Unlock()
			return nil
		}
	}
	if err := os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("summary"), 0o644)
}

func (r *grantRunner) RunJSONQuery(context.Context, string) ([]byte, error) { return nil, nil }

func (r *grantRunner) implTurns() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.turns...)
}

type grantRig struct {
	t      *testing.T
	o      *Orchestrator
	store  *state.Store
	runner *grantRunner
}

// newGrantRig: blocker and impl share one command slot; impl is held by auto_run: false until the test continues
// it, so its runner is queued behind blocker deterministically.
func newGrantRig(t *testing.T) *grantRig {
	t.Helper()
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{grantBlocker, grantImpl})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-1"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{grantBlocker, grantImpl} {
		if err := os.MkdirAll(filepath.Join(runDir, id), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runner := &grantRunner{release: make(chan struct{}), entered: make(chan struct{})}
	no := false
	o := New(Options{
		RunDir: runDir, Store: store, Config: config.Default(), Runner: runner, DashboardURL: "http://127.0.0.1:0",
		Stages: []flow.Stage{
			{ID: grantBlocker, Name: "Blocker", Agents: []flow.AgentType{flow.AgentAuto}, MaxParallel: 1},
			{ID: grantImpl, Name: "Impl", Agents: []flow.AgentType{flow.AgentAuto}, MaxParallel: 1, AutoRun: &no},
		},
		AssistantsEnabled: true,
	})
	runner.o = o
	return &grantRig{t: t, o: o, store: store, runner: runner}
}

func (g *grantRig) start() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	done := make(chan struct{})
	go func() { _ = g.o.Run(ctx); close(done) }()
	g.t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			g.t.Error("Run did not return")
		}
	})
	<-g.runner.entered // blocker holds the slot
	if err := g.o.Continue(context.Background(), grantImpl); err != nil {
		g.t.Fatal(err)
	}
	waitUntilFor(g.t, 5*time.Second, "impl running (its runner queued for the slot)", func() bool { return g.store.Get(grantImpl) == state.StatusRunning })
}

// A Revise committed while impl's runner waits for the slot: the runner must not run the old attempt; the Revise's
// feedback run is the stage's only turn, and no wait of the old attempt parks it.
func TestTurnGrant_ReviseOfAQueuedRunnerRunsOnlyTheFeedbackTurn(t *testing.T) {
	g := newGrantRig(t)
	g.runner.implWait = true
	g.start()

	if applied, _, err := g.o.Revise(context.Background(), grantImpl, "use YAML"); err != nil || !applied {
		t.Fatalf("Revise = %v, %v", applied, err)
	}
	close(g.runner.release)

	deadline := time.Now().Add(5 * time.Second)
	for g.store.Get(grantImpl) != state.StatusDone && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	g.runner.mu.Lock()
	waitErr := g.runner.waitE
	g.runner.mu.Unlock()
	if st := g.store.Get(grantImpl); st != state.StatusDone {
		t.Fatalf("impl = %s (turns %v, old-turn wait err %v, pending waits %d), want done after only the feedback turn",
			st, g.runner.implTurns(), waitErr, len(pendingWaitsOf(g.store.Snapshot().Assistants.Mesh, meshStageParticipantID(grantImpl))))
	}
	if got := fmt.Sprint(g.runner.implTurns()); got != "[feedback]" {
		t.Fatalf("impl turns = %s, want [feedback]: the queued runner of the old attempt ran", got)
	}
}

// Pause → Continue while impl's runner waits for the slot: Continue starts its own runner; the queued one must not
// run a second turn.
func TestTurnGrant_PauseContinueOfAQueuedRunnerRunsOneTurn(t *testing.T) {
	g := newGrantRig(t)
	g.start()

	if err := g.o.Pause(context.Background(), grantImpl); err != nil {
		t.Fatal(err)
	}
	if err := g.o.Continue(context.Background(), grantImpl); err != nil {
		t.Fatal(err)
	}
	close(g.runner.release)

	waitUntilFor(t, 10*time.Second, "impl done", func() bool { return g.store.Get(grantImpl) == state.StatusDone })
	g.o.concurrency.WaitAgents()
	if got := fmt.Sprint(g.runner.implTurns()); got != "[plain]" {
		t.Fatalf("impl turns = %s, want one [plain]: two runners each ran a turn", got)
	}
}

// A Revise between the runner's ownership check and the executor's mint: the token still carries the epoch of the
// turn that started the process, so the old agent's wait registers nothing under the new attempt.
func TestTurnGrant_TokenCarriesTheEpochOfItsTurn(t *testing.T) {
	g := newGrantRig(t)
	g.runner.implWait = true
	g.runner.beforeMint = func() {
		if applied, _, err := g.o.Revise(context.Background(), grantImpl, "use YAML"); err != nil || !applied {
			t.Errorf("Revise = %v, %v", applied, err)
		}
	}
	g.start()
	close(g.runner.release)

	waitUntilFor(t, 10*time.Second, "the old turn's wait to be decided", func() bool {
		g.runner.mu.Lock()
		defer g.runner.mu.Unlock()
		return g.runner.waitE != nil || len(pendingWaitsOf(g.store.Snapshot().Assistants.Mesh, meshStageParticipantID(grantImpl))) > 0
	})
	if n := len(pendingWaitsOf(g.store.Snapshot().Assistants.Mesh, meshStageParticipantID(grantImpl))); n != 0 {
		t.Fatalf("pending waits of impl = %d: the interrupted turn's agent registered a wait under the Revise's attempt", n)
	}
}

// The grant is fenced to the epoch read before it: a takeover that falls between the read and the commit, even one that
// leaves the stage in a status the event is legal from, refuses it.
func TestTurnGrant_AGrantReadBeforeATakeoverIsRefused(t *testing.T) {
	g := newGrantRig(t)
	g.start()
	defer close(g.runner.release)
	staleEpoch := g.o.stageEpoch(grantImpl)
	if err := g.o.Pause(context.Background(), grantImpl); err != nil {
		t.Fatal(err)
	}
	if err := g.o.Continue(context.Background(), grantImpl); err != nil {
		t.Fatal(err)
	}

	if _, _, ok := g.o.grantTurnAt(grantImpl, staleEpoch, bus.EvPause, bus.GuardCtx{}, ""); ok {
		t.Fatal("a grant decided before Pause and Continue was committed")
	}
	if got := g.store.Get(grantImpl); got != state.StatusRunning {
		t.Fatalf("impl = %s, want running: the refused grant must change nothing", got)
	}
}

// A stage that ended while its runner waited for the slot (failed by the user, say) starts no agent.
func TestTurnGrant_AQueuedRunnerOfAStageThatEndedRunsNothing(t *testing.T) {
	g := newGrantRig(t)
	g.start()

	g.o.FailStage(grantImpl, "failed while queued")
	close(g.runner.release)
	waitUntilFor(t, 10*time.Second, "impl's queued runner to get its slot and return", func() bool {
		return g.store.Get(grantBlocker) == state.StatusDone && !g.o.concurrency.IsActive(grantImpl)
	})

	if got := g.runner.implTurns(); len(got) != 0 {
		t.Fatalf("impl turns = %v, want none: the runner started an agent on a failed stage", got)
	}
}

// A spawn that is not a grant (recovery, resume) still fixes the epoch it was decided in, not the one found when the
// slot frees.
func TestTurnGrant_ASpawnWithoutAGrantOwnsTheEpochItWasDecidedIn(t *testing.T) {
	g := newGrantRig(t)
	g.start()
	decidedIn := g.o.stageEpoch(grantImpl)
	owned := make(chan uint64, 1)
	g.o.spawnAgentLeased(context.Background(), *g.o.reg().Stage(grantImpl), func(ctx context.Context, _ flow.Stage) {
		epoch, _ := turnEpochFrom(ctx)
		owned <- epoch
	})
	if err := g.o.Pause(context.Background(), grantImpl); err != nil {
		t.Fatal(err)
	}
	if err := g.o.Continue(context.Background(), grantImpl); err != nil {
		t.Fatal(err)
	}
	close(g.runner.release)

	select {
	case got := <-owned:
		if got != decidedIn {
			t.Fatalf("the spawn owns epoch %d, want %d (the one it was decided in)", got, decidedIn)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the spawned run never started")
	}
}
