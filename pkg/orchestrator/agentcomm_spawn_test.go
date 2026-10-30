package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// SpawnHelper: a stage agent starts a one-shot helper and gets back the id of the request addressed to it.

var (
	spawnStage      = meshStageParticipantID(assistantsTestStageID)
	spawnStageActor = state.Actor{Kind: "agent", ID: string(spawnStage)}
)

// gatedHelpers is a runHelperAgent seam whose helpers each wait for release(taskID) and then answer with
// outcome(taskID).
type gatedHelpers struct {
	mu      sync.Mutex
	gates   map[string]chan struct{}
	outcome func(taskID string) (executor.HelperOutcome, error)
}

func installGatedHelpers(o *Orchestrator, outcome func(taskID string) (executor.HelperOutcome, error)) *gatedHelpers {
	g := &gatedHelpers{gates: map[string]chan struct{}{}, outcome: outcome}
	o.runHelperAgent = func(ctx context.Context, taskID, _, _, _, _, _ string) (executor.HelperOutcome, error) {
		select {
		case <-g.gate(taskID):
			return g.outcome(taskID)
		case <-ctx.Done():
			return executor.HelperOutcome{}, ctx.Err()
		}
	}
	return g
}

func (g *gatedHelpers) gate(taskID string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gates[taskID] == nil {
		g.gates[taskID] = make(chan struct{})
	}
	return g.gates[taskID]
}

func (g *gatedHelpers) release(taskID assistants.TaskID) { close(g.gate(string(taskID))) }

func spawnReq(opID, instruction string) SpawnHelperRequest {
	return SpawnHelperRequest{OperationID: opID, ProfileID: "reviewer", Title: "Review " + opID, Instruction: instruction, Actor: spawnStageActor}
}

func mustSpawn(t *testing.T, o *Orchestrator, opID, instruction string) AttachAndSendResult {
	t.Helper()
	res, err := o.SpawnHelper(context.Background(), spawnReq(opID, instruction))
	if err != nil {
		t.Fatalf("SpawnHelper %s: %v", opID, err)
	}
	return res
}

func TestSpawnHelper_CreatesTaskAndRequestInOneCommit(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })

	res := mustSpawn(t, o, "op-spawn-1", "check the checkout flow")

	rs := store.Snapshot()
	task := rs.Assistants.Tasks[res.TaskID]
	if task == nil {
		t.Fatal("task not created")
	}
	if task.CreatedBy != assistants.OriginStageAgent || task.Mode != assistants.ModeAdvisory || task.Trigger != assistants.TriggerOneShot ||
		task.Scope != assistants.ScopeStage || task.RootStageID != assistantsTestStageID || task.ProfileID != "reviewer" {
		t.Fatalf("task = origin %q mode %q trigger %q scope %q stage %q profile %q, want a stage-agent advisory one-shot reviewer helper of %s",
			task.CreatedBy, task.Mode, task.Trigger, task.Scope, task.RootStageID, task.ProfileID, assistantsTestStageID)
	}
	if res.MessageID == nil {
		t.Fatal("no request message id")
	}
	msg := rs.Assistants.Mesh.Messages[*res.MessageID]
	if msg == nil || msg.Kind != agentcomm.MessageKindRequest || msg.Sender != spawnStage ||
		len(msg.Recipients) != 1 || msg.Recipients[0] != res.ParticipantID || msg.AutoResult {
		t.Fatalf("first message = %+v, want a request from the stage addressed only to the helper", msg)
	}
	if th := rs.Assistants.Mesh.Threads[res.ThreadID]; th == nil || th.OriginStage != assistantsTestStageID || len(rs.Assistants.Mesh.Messages) != 1 {
		t.Fatalf("thread = %+v, want it shown in the stage's feed with the one request", th)
	}

	// The task and its request were committed by ONE operation.
	op, committed, err := store.CommittedOperation("op-spawn-1", opKindAttachAndSend)
	if err != nil || !committed {
		t.Fatalf("CommittedOperation: committed=%v err=%v", committed, err)
	}
	if _, found, _ := op.Effect("task_created", &state.TaskCreatedPayload{}); !found {
		t.Fatal("the operation lacks task_created")
	}
	if _, found, _ := op.Effect("mesh_message_sent", &state.MeshMessageSentPayload{}); !found {
		t.Fatal("the operation lacks mesh_message_sent")
	}
}

func TestSpawnHelper_RetryOfTheSameOperationReturnsTheSameIDs(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })

	first := mustSpawn(t, o, "op-spawn-retry", "look at the cart")
	tasks, participants, threads, messages := meshSnapshotCounts(store.Snapshot())

	again := mustSpawn(t, o, "op-spawn-retry", "look at the cart")
	if again.TaskID != first.TaskID || again.ParticipantID != first.ParticipantID || again.ThreadID != first.ThreadID ||
		again.MessageID == nil || *again.MessageID != *first.MessageID {
		t.Fatalf("retry = %+v, want the first call's %+v", again, first)
	}
	if t2, p2, th2, m2 := meshSnapshotCounts(store.Snapshot()); t2 != tasks || p2 != participants || th2 != threads || m2 != messages {
		t.Fatal("a retry wrote something again")
	}

	_, err := o.SpawnHelper(context.Background(), spawnReq("op-spawn-retry", "a different instruction"))
	var conflict *state.OperationRejectedError
	if !errors.As(err, &conflict) {
		t.Fatalf("the same request id with another instruction: err = %v, want a conflict", err)
	}
}

// A spawn retry must not adopt the task of a HUMAN attach that committed the same operation id with the same
// task fields: it is another sender, kind and origin.
func TestSpawnHelper_RetryDoesNotAdoptAHumanAttachOfTheSameOperation(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })

	mustSpawn(t, o, "op-x", "i")
	first := "i"
	_, err := o.AttachAndSend(context.Background(), AttachAndSendRequest{
		OperationID: "op-x", ProfileID: "reviewer", Scope: assistants.ScopeStage, StageID: assistantsTestStageID,
		Mode: assistants.ModeAdvisory, Title: "Review op-x", Instruction: "i", FirstMessage: &first,
		Recipients: []agentcomm.ParticipantID{spawnStage}, Actor: state.Actor{Kind: "user"},
	})
	var conflict *state.OperationRejectedError
	if !errors.As(err, &conflict) {
		t.Fatalf("a human attach reusing a spawn's operation id: err = %v, want a conflict", err)
	}
}

func TestSpawnHelper_RejectionsWriteNothing(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, o *Orchestrator, store *state.Store)
		req   func(r SpawnHelperRequest) SpawnHelperRequest
		code  assistants.ErrorCode
	}{
		{name: "the human", req: func(r SpawnHelperRequest) SpawnHelperRequest {
			r.Actor = state.Actor{Kind: "user"}
			return r
		}, code: assistants.CodeActorNotAllowed},
		{name: "a helper", req: func(r SpawnHelperRequest) SpawnHelperRequest {
			r.Actor = state.Actor{Kind: "agent", ID: "helper:t-1"}
			return r
		}, code: assistants.CodeActorNotAllowed},
		{name: "a run agent", req: func(r SpawnHelperRequest) SpawnHelperRequest {
			r.Actor = state.Actor{Kind: "agent", ID: "run:t-1"}
			return r
		}, code: assistants.CodeActorNotAllowed},
		{name: "a non-agent actor naming a stage", req: func(r SpawnHelperRequest) SpawnHelperRequest {
			r.Actor = state.Actor{Kind: "assistant", ID: spawnStageActor.ID}
			return r
		}, code: assistants.CodeActorNotAllowed},
		{name: "unknown stage", req: func(r SpawnHelperRequest) SpawnHelperRequest {
			r.Actor = state.Actor{Kind: "agent", ID: "stage:nope"}
			return r
		}, code: assistants.CodeNotFound},
		{name: "terminal stage", setup: func(t *testing.T, o *Orchestrator, store *state.Store) {
			finishStage(t, o, assistantsTestStageID)
		}, req: func(r SpawnHelperRequest) SpawnHelperRequest { return r }, code: assistants.CodeInvalidStatus},
		{name: "unknown profile", req: func(r SpawnHelperRequest) SpawnHelperRequest {
			r.ProfileID = "does-not-exist"
			return r
		}, code: assistants.CodeCapabilityUnavailable},
		{name: "collaboration disabled", setup: func(t *testing.T, o *Orchestrator, store *state.Store) {
			o.opts.AssistantsEnabled = false
		}, req: func(r SpawnHelperRequest) SpawnHelperRequest { return r }, code: assistants.CodeFeatureDisabled},
		{name: "quota exhausted", setup: func(t *testing.T, o *Orchestrator, store *state.Store) {
			zero := 0
			o.opts.Config.Collaboration.Limits.MaxTasks = &zero
		}, req: func(r SpawnHelperRequest) SpawnHelperRequest { return r }, code: assistants.CodeBudgetExceeded},
		{name: "empty instruction", req: func(r SpawnHelperRequest) SpawnHelperRequest {
			r.Instruction = " "
			return r
		}, code: assistants.CodeInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			activateStage(t, o, assistantsTestStageID)
			installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
			if tc.setup != nil {
				tc.setup(t, o, store)
			}
			tasks, participants, threads, messages := meshSnapshotCounts(store.Snapshot())

			_, err := o.SpawnHelper(context.Background(), tc.req(spawnReq("op-spawn-bad", "review it")))
			requireRejected(t, err, tc.code)
			if t2, p2, th2, m2 := meshSnapshotCounts(store.Snapshot()); t2 != tasks || p2 != participants || th2 != threads || m2 != messages {
				t.Fatalf("a rejected spawn wrote something: tasks %d→%d participants %d→%d threads %d→%d messages %d→%d",
					tasks, t2, participants, p2, threads, th2, messages, m2)
			}
		})
	}
}

func TestSpawnHelper_UnavailableProfileWritesNothing(t *testing.T) {
	o, store := newBrokenProfileTestOrchestrator(t)
	activateStage(t, o, assistantsTestStageID)
	tasks, participants, threads, messages := meshSnapshotCounts(store.Snapshot())

	req := spawnReq("op-spawn-broken", "review it")
	req.ProfileID = "broken"
	_, err := o.SpawnHelper(context.Background(), req)
	requireRejected(t, err, assistants.CodeCapabilityUnavailable)
	if t2, p2, th2, m2 := meshSnapshotCounts(store.Snapshot()); t2 != tasks || p2 != participants || th2 != threads || m2 != messages {
		t.Fatal("a rejected spawn wrote something")
	}
}

// batchOfHelpers spawns n helpers of one profile with different instructions and waits for all their requests
// in one batch.
func batchOfHelpers(t *testing.T, o *Orchestrator, n int) (helpers []AttachAndSendResult, receipt WaitReceipt) {
	t.Helper()
	var ids []agentcomm.MessageID
	for i := range n {
		h := mustSpawn(t, o, fmt.Sprintf("op-spawn-%d", i), fmt.Sprintf("angle %d", i))
		helpers = append(helpers, h)
		ids = append(ids, *h.MessageID)
	}
	receipt, err := o.Wait(context.Background(), WaitRequest{
		OperationID: "op-batch", Waiter: spawnStage, Actor: spawnStageActor,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo}, MessageIDs: ids,
	})
	if err != nil {
		t.Fatalf("batch Wait: %v", err)
	}
	return helpers, receipt
}

func waitStatus(store *state.Store, id agentcomm.WaitID) *state.MeshWait {
	return store.Snapshot().Assistants.Mesh.Waits[id]
}

func TestSpawnHelper_ResultRepliesOnceAndItsBatchItemBecomesReady(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	gates := installGatedHelpers(o, func(string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, RawText: needsChangesWithTwoFindings}, nil
	})

	helpers, receipt := batchOfHelpers(t, o, 3)
	if receipt.Status != agentcomm.WaitPending || len(receipt.Items) != 3 {
		t.Fatalf("receipt = %+v, want three pending items", receipt)
	}

	gates.release(helpers[1].TaskID)
	waitUntil(t, "the second item is ready", func() bool { return waitStatus(store, receipt.Items[1].WaitID).Status == agentcomm.WaitReady })
	for _, i := range []int{0, 2} {
		if st := waitStatus(store, receipt.Items[i].WaitID).Status; st != agentcomm.WaitPending {
			t.Fatalf("item %d = %s while its helper is still running, want pending", i, st)
		}
	}
	replies := repliesTo(store, *helpers[1].MessageID)
	if len(replies) != 1 {
		t.Fatalf("replies to the second request = %d, want 1", len(replies))
	}
	r := replies[0]
	if r.Sender != helpers[1].ParticipantID || r.ThreadID != helpers[1].ThreadID || !r.AutoResult || r.Kind != agentcomm.MessageKindReply ||
		len(r.Recipients) != 1 || r.Recipients[0] != spawnStage {
		t.Fatalf("reply = %+v, want a passive reply from the helper to the stage in the request's thread", r)
	}
	for _, want := range []string{"checked the checkout flow", "needs_changes", "2 findings", string(helpers[1].TaskID)} {
		if !strings.Contains(r.Body, want) {
			t.Fatalf("reply body %q lacks %q", r.Body, want)
		}
	}

	gates.release(helpers[0].TaskID)
	gates.release(helpers[2].TaskID)
	for i, item := range receipt.Items {
		waitUntil(t, fmt.Sprintf("item %d is ready", i), func() bool { return waitStatus(store, item.WaitID).Status == agentcomm.WaitReady })
	}
	o.concurrency.WaitAgents()
	for i, h := range helpers {
		if n := len(repliesTo(store, *h.MessageID)); n != 1 {
			t.Fatalf("helper %d: replies = %d, want exactly 1", i, n)
		}
		if tk := store.Snapshot().Assistants.Tasks[h.TaskID]; tk.Status != assistants.StatusCompleted {
			t.Fatalf("helper %d: status = %s, want completed (one-shot helpers do not go idle)", i, tk.Status)
		}
	}
}

func TestSpawnHelper_FailedHelperCancelsOnlyItsItemWithTheReason(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	var failing assistants.TaskID
	gates := installGatedHelpers(o, func(taskID string) (executor.HelperOutcome, error) {
		if assistants.TaskID(taskID) == failing {
			return executor.HelperOutcome{}, errors.New("model unavailable")
		}
		return passResultOutcome(), nil
	})

	helpers, receipt := batchOfHelpers(t, o, 3)
	failing = helpers[1].TaskID
	for _, h := range helpers {
		gates.release(h.TaskID)
	}

	for _, i := range []int{0, 2} {
		waitUntil(t, "a surviving helper's item is ready", func() bool { return waitStatus(store, receipt.Items[i].WaitID).Status == agentcomm.WaitReady })
	}
	waitUntil(t, "the failed helper's item ends", func() bool { return waitStatus(store, receipt.Items[1].WaitID).Status == agentcomm.WaitCancelled })
	w := waitStatus(store, receipt.Items[1].WaitID)
	if !strings.Contains(w.Reason, "model unavailable") {
		t.Fatalf("cancelled reason = %q, want the helper's failure reason", w.Reason)
	}
	if n := len(repliesTo(store, *helpers[1].MessageID)); n != 0 {
		t.Fatalf("a failed helper replied %d times, want 0", n)
	}
	for _, i := range []int{0, 2} {
		if n := len(repliesTo(store, *helpers[i].MessageID)); n != 1 {
			t.Fatalf("surviving helper %d: replies = %d, want 1 (the failure must not hide the others' results)", i, n)
		}
	}
}

func TestSpawnHelper_CancelledHelperCancelsItsItem(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })

	helpers, receipt := batchOfHelpers(t, o, 2)
	waitForTaskStatus(t, store, helpers[0].TaskID, assistants.StatusRunning, 2*time.Second)
	if err := o.CancelTask(context.Background(), helpers[0].TaskID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	waitUntil(t, "the cancelled helper's item ends", func() bool { return waitStatus(store, receipt.Items[0].WaitID).Status == agentcomm.WaitCancelled })
	if st := waitStatus(store, receipt.Items[1].WaitID).Status; st != agentcomm.WaitPending {
		t.Fatalf("the other item = %s, want pending", st)
	}
}

// A human's attach keeps recording the actor's kind as the task's origin and sends its first message as a
// plain message from the human to the new helper.
func TestAttachAndSend_HumanAttachKeepsItsOriginAndMessageKind(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })

	msg := "please look"
	res, err := o.AttachAndSend(context.Background(), AttachAndSendRequest{
		OperationID: "op-human-attach", ProfileID: "reviewer", StageID: assistantsTestStageID, Title: "Reviewer",
		Instruction: "look", FirstMessage: &msg, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AttachAndSend: %v", err)
	}
	rs := store.Snapshot()
	m := rs.Assistants.Mesh.Messages[*res.MessageID]
	if got := rs.Assistants.Tasks[res.TaskID].CreatedBy; got != assistants.OriginUser {
		t.Fatalf("CreatedBy = %q, want user", got)
	}
	if m.Kind != agentcomm.MessageKindMessage || m.Sender != meshUserParticipantID || len(m.Recipients) != 1 || m.Recipients[0] != res.ParticipantID {
		t.Fatalf("first message = %+v, want a plain message from the human to the helper", m)
	}
}

// HelperResult: the stage reads the status and the last COMMITTED full result of a helper it spawned.

func TestHelperResult_RunningHelperHasStatusAndNoResult(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
	h := mustSpawn(t, o, "op-hr-running", "look")

	got, err := o.HelperResult(spawnStageActor, h.TaskID)
	if err != nil {
		t.Fatalf("HelperResult: %v", err)
	}
	if got.Result != nil || got.Status.Terminal() {
		t.Fatalf("view = %+v, want a non-terminal status and no result yet", got)
	}
}

func TestHelperResult_CompletedHelperReturnsTheFullCommittedResult(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	gates := installGatedHelpers(o, func(string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, RawText: needsChangesWithTwoFindings}, nil
	})
	h := mustSpawn(t, o, "op-hr-done", "look")
	gates.release(h.TaskID)
	waitForTaskStatus(t, store, h.TaskID, assistants.StatusCompleted, 2*time.Second)

	got, err := o.HelperResult(spawnStageActor, h.TaskID)
	if err != nil {
		t.Fatalf("HelperResult: %v", err)
	}
	if got.Status != assistants.StatusCompleted {
		t.Fatalf("status = %s, want completed", got.Status)
	}
	for _, want := range []string{`"verdict": "needs_changes"`, `"id": "f2"`, "checked the checkout flow"} {
		if !strings.Contains(string(got.Result), want) {
			t.Fatalf("result %s lacks %q", got.Result, want)
		}
	}
}

// HelperResult must never show a torn status/result pair: a non-terminal status together with an already-
// committed result, or a terminal "completed" status with no result at all. It reads the task's status and its
// full event history from Store.TaskAndEvents in ONE lock acquisition — before that fix it read Store.Snapshot()
// and Store.TaskEvents() as two separate locked calls, leaving a window where the finish commit (status change
// and task_result are ONE operation) could land between them.
func TestHelperResult_StatusAndResultNeverTornAcrossTheFinishCommit(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	gates := installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
	h := mustSpawn(t, o, "op-hr-torn", "look")

	stop := make(chan struct{})
	var torn atomic.Int32
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			got, err := o.HelperResult(spawnStageActor, h.TaskID)
			if err != nil {
				continue
			}
			if (got.Result != nil && !got.Status.Terminal()) || (got.Result == nil && got.Status == assistants.StatusCompleted) {
				torn.Add(1)
			}
		}
	})

	gates.release(h.TaskID)
	waitForTaskStatus(t, store, h.TaskID, assistants.StatusCompleted, 2*time.Second)
	close(stop)
	wg.Wait()

	if n := torn.Load(); n != 0 {
		t.Fatalf("HelperResult showed a torn status/result pair %d times", n)
	}
}

func TestHelperResult_FailedHelperHasTheReasonAndNoResult(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	gates := installGatedHelpers(o, func(string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{}, errors.New("model unavailable")
	})
	h := mustSpawn(t, o, "op-hr-failed", "look")
	gates.release(h.TaskID)
	waitUntil(t, "the helper is terminal", func() bool { return store.Snapshot().Assistants.Tasks[h.TaskID].Status.Terminal() })

	got, err := o.HelperResult(spawnStageActor, h.TaskID)
	if err != nil {
		t.Fatalf("HelperResult: %v", err)
	}
	if got.Result != nil || !strings.Contains(got.StatusReason, "model unavailable") {
		t.Fatalf("view = %+v, want the failure reason and no result", got)
	}
}

// A result.json the journal never accepted must not be shown.
func TestHelperResult_NeverReadsAnUncommittedResultFile(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
	h := mustSpawn(t, o, "op-hr-crash", "look")
	dir := o.taskDir(h.TaskID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte(`{"verdict":"pass","summary":"uncommitted"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := o.HelperResult(spawnStageActor, h.TaskID)
	if err != nil {
		t.Fatalf("HelperResult: %v", err)
	}
	if got.Result != nil {
		t.Fatalf("result = %s, want none: the finish commit never happened", got.Result)
	}
}

func TestHelperResult_RefusesWhatIsNotTheCallersOwnHelper(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
	h := mustSpawn(t, o, "op-hr-own", "look")
	// A task of the same stage that no stage agent spawned.
	human := attachTestTask(context.Background(), t, o, "op-hr-human")

	cases := []struct {
		name   string
		actor  state.Actor
		taskID assistants.TaskID
		code   assistants.ErrorCode
	}{
		{"unknown task", spawnStageActor, "t-nope", assistants.CodeNotFound},
		{"another stage", state.Actor{Kind: "agent", ID: string(meshStageParticipantID("other"))}, h.TaskID, assistants.CodeNotFound},
		{"not a stage-agent helper", spawnStageActor, human, assistants.CodeNotFound},
		{"the helper itself", state.Actor{Kind: "agent", ID: string(h.ParticipantID)}, h.TaskID, assistants.CodeActorNotAllowed},
		{"the human", state.Actor{Kind: "user", ID: "user"}, h.TaskID, assistants.CodeActorNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := o.HelperResult(tc.actor, tc.taskID)
			var rejected *state.OperationRejectedError
			if !errors.As(err, &rejected) || rejected.Code != tc.code {
				t.Fatalf("err = %v, want rejection %s", err, tc.code)
			}
		})
	}
	_ = store
}

// A new turn of the stage sees the helpers it already started, so it does not start them again.
func TestStageInbox_ListsTheHelpersTheStageSpawned(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
	stage := flow.Stage{ID: assistantsTestStageID}

	if block := o.agentCommInboxBlock(stage); strings.Contains(block, "<your_helpers>") {
		t.Fatalf("a stage without helpers must get no helpers block:\n%s", block)
	}
	a := mustSpawn(t, o, "op-list-a", "look at A")
	b := mustSpawn(t, o, "op-list-b", "look at B")

	block := o.agentCommInboxBlock(stage)
	section := block[strings.Index(block, "<your_helpers>"):]
	for _, want := range []string{"<your_helpers>", string(a.TaskID), "Review op-list-a", string(b.TaskID), "Review op-list-b", "running"} {
		if !strings.Contains(section, want) {
			t.Errorf("block lacks %q:\n%s", want, section)
		}
	}
	if strings.Contains(section, "look at A") {
		t.Fatalf("the block carries only id, title and status, not the instruction:\n%s", section)
	}
}

// A fresh attempt of the stage (Revise) invalidates its old helpers: they must leave <your_helpers>, or the
// agent would not re-run a review that now concerns an outdated state.
func TestStageInbox_DropsTheHelpersOfAnOldRootGeneration(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
	stage := flow.Stage{ID: assistantsTestStageID}
	// <your_requests> mentions the old helper too; only the <your_helpers> section is under test.
	listed := func() string {
		block := o.agentCommInboxBlock(stage)
		start := strings.Index(block, "<your_helpers>")
		if start < 0 {
			return ""
		}
		return block[start:]
	}

	old := mustSpawn(t, o, "op-gen-old", "look at the old state")
	if !strings.Contains(listed(), string(old.TaskID)) {
		t.Fatalf("a helper of the current generation must be listed:\n%s", listed())
	}
	if err := o.BumpRootGeneration(assistantsTestStageID); err != nil {
		t.Fatalf("BumpRootGeneration: %v", err)
	}
	if got := listed(); strings.Contains(got, string(old.TaskID)) {
		t.Fatalf("a helper of the old generation must not be listed:\n%s", got)
	}
	fresh := mustSpawn(t, o, "op-gen-new", "look at the new state")
	if got := listed(); !strings.Contains(got, string(fresh.TaskID)) || strings.Contains(got, string(old.TaskID)) {
		t.Fatalf("only the helper of the new generation must be listed:\n%s", got)
	}
}

// The prompt advertises `afm agent spawn` only while collaboration is on: SpawnHelper rejects it otherwise.
func TestAgentCommBlockFor_AdvertisesSpawnOnlyWhenCollaborationIsEnabled(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	o.opts.DashboardURL = "http://127.0.0.1:1"
	stage := flow.Stage{ID: assistantsTestStageID}

	if block := o.agentCommBlockFor(stage); !strings.Contains(block, "afm agent spawn") {
		t.Fatalf("collaboration on: the stage block must name spawn:\n%s", block)
	}
	o.opts.AssistantsEnabled = false
	if block := o.agentCommBlockFor(stage); strings.Contains(block, "afm agent spawn") {
		t.Fatalf("collaboration off: the stage block must not name spawn:\n%s", block)
	}
}

// unformattableHelperRaw is a finding's detail as an object: no local repair takes it, so it reaches the formatter.
const unformattableHelperRaw = `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"Eleven minutes of review",` +
	`"findings":[{"id":"f1","severity":"major","detail":{"text":"Keep the stage note"}}]}`

// A helper whose answer could not be accepted still failed, but the stage that waits for it gets a reply with the
// reason and the unvalidated answer: the work is not lost behind a cancelled wait.
func TestSpawnHelper_InvalidAnswerFailsButRepliesWithTheRawAnswer(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	gates := installGatedHelpers(o, func(string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, RawText: unformattableHelperRaw}, nil
	})
	o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	}
	h := mustSpawn(t, o, "op-invalid", "review the plan")
	gates.release(h.TaskID)
	tk := waitForTaskStatus(t, store, h.TaskID, assistants.StatusFailed, 2*time.Second)
	o.concurrency.WaitAgents()

	if !strings.HasPrefix(tk.StatusReason, protocolErrorPrefix) || !strings.Contains(tk.StatusReason, "detail") ||
		strings.Contains(tk.StatusReason, "helper execution failed") {
		t.Fatalf("reason = %q, want the rejected answer's protocol error, not an execution failure", tk.StatusReason)
	}
	replies := repliesTo(store, *h.MessageID)
	if len(replies) != 1 {
		t.Fatalf("replies = %d, want 1", len(replies))
	}
	r := replies[0]
	if !r.AutoResult || r.Sender != h.ParticipantID || len(r.Recipients) != 1 || r.Recipients[0] != spawnStage {
		t.Fatalf("reply = %+v, want a passive reply from the helper to the stage", r)
	}
	rawPath := filepath.Join(o.taskDir(h.TaskID), "turn-1-output.raw.json")
	for _, want := range []string{"could not produce a valid result", "Keep the stage note", "Eleven minutes of review", rawPath, "task failed"} {
		if !strings.Contains(r.Body, want) {
			t.Fatalf("reply body %q lacks %q", r.Body, want)
		}
	}
	if strings.Contains(r.Body, "write again") {
		t.Fatalf("reply body %q invites a retry of a one-shot helper", r.Body)
	}
}

// A usage limit the formatter hits parks the helper as before: waiting for the reset is no answer failure.
func TestSpawnHelper_FormatterUsageLimitStillParks(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	gates := installGatedHelpers(o, func(string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, RawText: unformattableHelperRaw}, nil
	})
	o.runOutputFormat = func(context.Context, executor.Config, string, string, string) ([]byte, error) {
		return nil, &executor.UsageLimitError{Provider: "anthropic", ResetAt: time.Now().Add(time.Hour)}
	}
	h := mustSpawn(t, o, "op-quota", "review the plan")
	gates.release(h.TaskID)
	waitUntil(t, "the helper parks for the limit", func() bool {
		tk := store.Snapshot().Assistants.Tasks[h.TaskID]
		return tk.Status == assistants.StatusQueued && tk.Quota != nil
	})
	if n := len(repliesTo(store, *h.MessageID)); n != 0 {
		t.Fatalf("replies = %d, want none while the helper waits for its limit", n)
	}
}
