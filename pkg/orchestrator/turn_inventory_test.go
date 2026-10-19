package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// A helper turn's inventory — its number and the messages handed to it — is in the journal before the process
// starts (TaskState.Turn/TurnInbox, from task_status_changed); turn-inbox.json is only a cache of it.

func setRunContext(ctx context.Context, o *Orchestrator) {
	o.runMu.Lock()
	o.runCtx = ctx
	o.runMu.Unlock()
}

func turnOf(store *state.Store, taskID assistants.TaskID) (uint64, []agentcomm.MessageID) {
	t := store.Snapshot().Assistants.Tasks[taskID]
	return t.Turn, t.TurnInbox
}

// TestTurnInventory_RecordedBeforeTheProcessStarts: while a turn runs, the journal already names it and the
// messages it was handed; once its result is committed the input is consumed and the cache file is gone.
func TestTurnInventory_RecordedBeforeTheProcessStarts(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	var duringTurn atomic.Value
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		turn, inbox := turnOf(store, assistants.TaskID(taskID))
		duringTurn.Store(struct {
			turn  uint64
			inbox []agentcomm.MessageID
		}{turn, inbox})
		return passResultOutcome(), nil
	}
	msg := "first question"
	res := addTestRunAgent(t, o, "op-turn-rec", &msg)
	waitForRunAgentTurn(t, store, res.TaskID, 2*time.Second)
	o.concurrency.WaitAgents()

	got := duringTurn.Load().(struct {
		turn  uint64
		inbox []agentcomm.MessageID
	})
	if got.turn != 1 || !slices.Equal(got.inbox, []agentcomm.MessageID{*res.MessageID}) {
		t.Fatalf("during the turn: turn %d, inbox %v; want turn 1 handed %s", got.turn, got.inbox, *res.MessageID)
	}
	if turn, inbox := turnOf(store, res.TaskID); turn != 1 || inbox != nil {
		t.Fatalf("after the result: turn %d, inbox %v; want turn 1 with its input consumed", turn, inbox)
	}
	if _, err := os.Stat(filepath.Join(o.taskDir(res.TaskID), turnInboxFileName)); !os.IsNotExist(err) {
		t.Fatalf("the turn-inbox cache outlived its finished turn: %v", err)
	}
}

// TestTurnInventory_CrashBeforeResultCommit_RetryGetsTheSameInput: the process was handed the message (it is
// delivered) and afm went down before the turn's outcome was committed. After the restart — with the cache file
// gone, so only the journal can tell — the retried turn is handed the same message.
func TestTurnInventory_CrashBeforeResultCommit_RetryGetsTheSameInput(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	runCtx, cancelRun := context.WithCancel(context.Background())
	setRunContext(runCtx, o)
	var calls atomic.Int32
	started := make(chan struct{})
	var retried atomic.Value
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			o.helperExecutorConfig(assistants.TaskID(taskID), cmd).OnDelivered() // what the executor does at process start
			close(started)
			<-ctx.Done() // afm goes down before this turn's outcome is committed
			return executor.HelperOutcome{ProcessErr: ctx.Err()}, nil
		}
		retried.Store(prompt)
		return passResultOutcome(), nil
	}
	msg := "please review the checkout flow"
	res := addTestRunAgent(t, o, "op-turn-crash-before", &msg)
	<-started
	cancelRun()
	o.concurrency.WaitAgents()
	if turn, inbox := turnOf(store, res.TaskID); turn != 1 || !slices.Equal(inbox, []agentcomm.MessageID{*res.MessageID}) {
		t.Fatalf("journal after the crash: turn %d, inbox %v; want turn 1 still holding its input", turn, inbox)
	}

	store = reopenStore(t, o, store)
	if err := os.Remove(filepath.Join(o.taskDir(res.TaskID), turnInboxFileName)); err != nil {
		t.Fatalf("remove the cache: %v", err)
	}
	resumed, cancelResumed := context.WithCancel(context.Background())
	defer cancelResumed()
	setRunContext(resumed, o)
	o.recoverAssistants(resumed)

	waitForRunAgentTurn(t, store, res.TaskID, 2*time.Second)
	o.concurrency.WaitAgents()
	if prompt, _ := retried.Load().(string); !strings.Contains(prompt, msg) {
		t.Fatalf("the retried turn was not handed the message of the crashed one:\n%s", prompt)
	}
	if turn, inbox := turnOf(store, res.TaskID); turn != 2 || inbox != nil {
		t.Fatalf("after the retried turn: turn %d, inbox %v; want turn 2 with its input consumed", turn, inbox)
	}
}

// TestTurnInventory_CrashAfterResultCommit_SecondTurnStartsClean: the turn's outcome was committed and afm went
// down before the cache file was cleaned up. After the restart, the agent's second turn is handed only the new
// message — the stale cache does not bring the first one back.
func TestTurnInventory_CrashAfterResultCommit_SecondTurnStartsClean(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	var prompts []string
	var calls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		calls.Add(1)
		prompts = append(prompts, prompt)
		return passResultOutcome(), nil
	}
	first := "first question"
	res := addTestRunAgent(t, o, "op-turn-crash-after", &first)
	waitForRunAgentTurn(t, store, res.TaskID, 2*time.Second)
	o.concurrency.WaitAgents()
	if err := o.saveTurnInbox(res.TaskID, []agentcomm.MessageID{*res.MessageID}); err != nil { // the stale cache
		t.Fatal(err)
	}

	store = reopenStore(t, o, store)
	resumed, cancelResumed := context.WithCancel(context.Background())
	defer cancelResumed()
	setRunContext(resumed, o)
	o.recoverAssistants(resumed) // an idle run agent stays idle
	second := "second question"
	if _, err := o.Send(context.Background(), SendRequest{
		OperationID: "op-turn-second", ThreadID: res.ThreadID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{res.ParticipantID}, Kind: agentcomm.MessageKindMessage, Body: second,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	waitForRunAgentTurn(t, store, res.TaskID, 2*time.Second)
	o.concurrency.WaitAgents()

	if len(prompts) != 2 {
		t.Fatalf("turns = %d, want 2", len(prompts))
	}
	if !strings.Contains(prompts[1], second) || strings.Contains(prompts[1], first) {
		t.Fatalf("the second turn must see only the new message:\n%s", prompts[1])
	}
	if turn, inbox := turnOf(store, res.TaskID); turn != 2 || inbox != nil {
		t.Fatalf("after the second turn: turn %d, inbox %v; want turn 2 with its input consumed", turn, inbox)
	}
}

// TestTurnInventory_PreInventoryJournalResumes: a journal written before turns were recorded (a run agent cut
// short mid-turn, its input only in turn-inbox.json) still loads, and its retried turn is handed that input.
func TestTurnInventory_PreInventoryJournalResumes(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join("testdata", "turn-inbox-pre-journal")
	if err := os.CopyFS(o.opts.RunDir, os.DirFS(fixture)); err != nil && !os.IsExist(err) {
		t.Fatalf("copy the fixture: %v", err)
	}
	// os.CopyFS refuses to overwrite: events.jsonl from state.Open above is replaced explicitly.
	raw, err := os.ReadFile(filepath.Join(fixture, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.opts.RunDir, "events.jsonl"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	reopened, err := state.Open(o.opts.RunDir, []string{assistantsTestStageID})
	if err != nil {
		t.Fatalf("open the pre-inventory journal: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })
	o.opts.Store = reopened
	o.fsm = bus.NewFSM(reopened)

	const taskID = assistants.TaskID("t-c81bb0462afcbdc62a60")
	if turn, inbox := turnOf(reopened, taskID); turn != 0 || inbox != nil {
		t.Fatalf("an old journal has no turn records, got turn %d, inbox %v", turn, inbox)
	}
	var retried atomic.Value
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		retried.Store(prompt)
		return passResultOutcome(), nil
	}
	resumed, cancelResumed := context.WithCancel(context.Background())
	defer cancelResumed()
	setRunContext(resumed, o)
	o.recoverAssistants(resumed)

	waitForRunAgentTurn(t, reopened, taskID, 2*time.Second)
	o.concurrency.WaitAgents()
	if prompt, _ := retried.Load().(string); !strings.Contains(prompt, "please review the checkout flow") {
		t.Fatalf("the retried turn of an old journal lost its input:\n%s", prompt)
	}
	if turn, inbox := turnOf(reopened, taskID); turn != 1 || inbox != nil {
		t.Fatalf("after the retried turn: turn %d, inbox %v; want turn 1 with its input consumed", turn, inbox)
	}
}

// TestTurnInventory_DelegationClearsTheCache: a turn that ends by delegating consumed its input; its
// turn-inbox.json cache goes too, so an older binary reading the cache cannot hand the messages over again.
func TestTurnInventory_DelegationClearsTheCache(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	var calls atomic.Int32
	release := make(chan struct{})
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		switch calls.Add(1) {
		case 1:
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
		case 2:
			awaitRelease(t, release) // the child holds the parent in waiting_children
		default:
		}
		return passResultOutcome(), nil
	}
	msg := "split this up"
	res := addTestRunAgent(t, o, "op-delegate-cache", &msg)
	waitForTaskStatus(t, store, res.TaskID, assistants.StatusWaitingChildren, 2*time.Second)
	// The cache is dropped right after the delegation commits, on the turn's own goroutine.
	waitUntil(t, "the delegating turn's inbox cache is gone", func() bool {
		_, err := os.Stat(filepath.Join(o.taskDir(res.TaskID), turnInboxFileName))
		return os.IsNotExist(err)
	})
	close(release)
	waitForRunAgentTurn(t, store, res.TaskID, 3*time.Second)
	o.concurrency.WaitAgents()
}
