package orchestrator

import (
	"context"
	"errors"
	"reflect"
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

// Agent Mesh Phase 6, T6.2: AttachAndSend.

// stubPassingHelper installs a runHelperAgent seam that returns a passing result immediately — used by every
// happy-path test below so maybeScheduleTask's externalWork spawn actually runs to completion instead of
// trying (and failing) to exec a real "helper-cmd" subprocess.
func stubPassingHelper(o *Orchestrator) {
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}
}

// newBrokenProfileTestOrchestrator is newAssistantsTestOrchestrator's fixture PLUS one additional profile,
// "broken", whose command the registry's availability check reports as unavailable — needed to exercise
// AttachAndSend's "unavailable profile" gate (CodeCapabilityUnavailable), which newAssistantsTestOrchestrator's
// own registry (every profile available) cannot reach.
func newBrokenProfileTestOrchestrator(t *testing.T) (*Orchestrator, *state.Store) {
	t.Helper()
	o, store := newAssistantsTestOrchestrator(t, true)
	reg, err := assistants.BuildRegistry(nil, []assistants.Profile{
		{ID: "reviewer", Title: "Reviewer", Command: "helper-cmd", Capabilities: assistants.Capabilities{Delegate: true}},
		{ID: "qa", Title: "QA", Command: "helper-cmd", Capabilities: assistants.Capabilities{Delegate: true}},
		{ID: "broken", Title: "Broken", Command: "broken-cmd"},
	}, nil, "claude", func(cmd string) (bool, string, assistants.ReadonlyEnforcement) {
		if cmd == "broken-cmd" {
			return false, "no such tool on PATH", assistants.ReadonlyEnforcementNone
		}
		return true, "", assistants.ReadonlyEnforcementOS
	})
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	o.opts.AssistantsRegistry = reg
	return o, store
}

// meshSnapshotCounts captures the four counters TestAttachAndSend_AtomicOrNothing diffs across an induced
// failure: a partial attach would move at least one of them.
func meshSnapshotCounts(rs state.RunState) (tasks, participants, threads, messages int) {
	return len(rs.Assistants.Tasks), len(rs.Assistants.Mesh.Participants), len(rs.Assistants.Mesh.Threads), len(rs.Assistants.Mesh.Messages)
}

func TestAttachAndSend_ScopeStageCreatesTaskParticipantThreadMessage(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	stubPassingHelper(o)

	msg := "please take a look"
	res, err := o.AttachAndSend(ctx, AttachAndSendRequest{
		OperationID: "op-as-1", ProfileID: "reviewer", Scope: assistants.ScopeStage, StageID: assistantsTestStageID,
		Title: "Reviewer", Instruction: "look at this", FirstMessage: &msg, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AttachAndSend: %v", err)
	}
	waitForTaskStatus(t, store, res.TaskID, assistants.StatusCompleted, 2*time.Second)

	rs := store.Snapshot()
	task, ok := rs.Assistants.Tasks[res.TaskID]
	if !ok {
		t.Fatal("task not found in projection")
	}
	if task.Scope != assistants.ScopeStage {
		t.Fatalf("task.Scope = %q, want stage", task.Scope)
	}
	if task.RootStageID != assistantsTestStageID {
		t.Fatalf("task.RootStageID = %q, want %q", task.RootStageID, assistantsTestStageID)
	}

	p, ok := rs.Assistants.Mesh.Participants[res.ParticipantID]
	if !ok {
		t.Fatal("participant not found in projection")
	}
	if p.Kind != agentcomm.KindHelper || p.Scope != agentcomm.ScopeStage {
		t.Fatalf("participant kind/scope = %s/%s, want helper/stage", p.Kind, p.Scope)
	}
	if res.ParticipantID != meshHelperParticipantID(res.TaskID) {
		t.Fatalf("participant id = %s, want the standard helper:<taskID> form", res.ParticipantID)
	}

	thread, ok := rs.Assistants.Mesh.Threads[res.ThreadID]
	if !ok {
		t.Fatal("thread not found in projection")
	}
	if len(thread.Members) != 2 {
		t.Fatalf("thread members = %v, want exactly [user, %s]", thread.Members, res.ParticipantID)
	}

	if res.MessageID == nil {
		t.Fatal("MessageID is nil, want the first message's id")
	}
	m, ok := rs.Assistants.Mesh.Messages[*res.MessageID]
	if !ok {
		t.Fatal("message not found in projection")
	}
	if m.Body != msg {
		t.Fatalf("message body = %q, want %q", m.Body, msg)
	}
	if len(m.Recipients) != 1 || m.Recipients[0] != res.ParticipantID {
		t.Fatalf("message recipients = %v, want exactly [%s]", m.Recipients, res.ParticipantID)
	}
}

func TestAttachAndSend_ScopeRunCreatesStandingParticipant(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	stubPassingHelper(o)

	msg := "standing by"
	res, err := o.AttachAndSend(ctx, AttachAndSendRequest{
		OperationID: "op-as-run-1", ProfileID: "qa", Scope: assistants.ScopeRun,
		Title: "My QA", Instruction: "watch for regressions", FirstMessage: &msg, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AttachAndSend: %v", err)
	}
	waitForRunAgentTurn(t, store, res.TaskID, 2*time.Second)

	rs := store.Snapshot()
	task, ok := rs.Assistants.Tasks[res.TaskID]
	if !ok {
		t.Fatal("task not found in projection")
	}
	if task.Scope != assistants.ScopeRun {
		t.Fatalf("task.Scope = %q, want run", task.Scope)
	}
	if task.RootStageID != "" {
		t.Fatalf("task.RootStageID = %q, want empty for a scope=run task", task.RootStageID)
	}

	p, ok := rs.Assistants.Mesh.Participants[res.ParticipantID]
	if !ok {
		t.Fatal("run participant not found in projection")
	}
	if p.Kind != agentcomm.KindRunAgent || p.Scope != agentcomm.ScopeRun {
		t.Fatalf("participant kind/scope = %s/%s, want run_agent/run", p.Kind, p.Scope)
	}
	if res.ParticipantID != meshRunParticipantID(res.TaskID) {
		t.Fatalf("participant id = %s, want the run:<taskID> form", res.ParticipantID)
	}
	if p.StageID != "" {
		t.Fatalf("a scope=run participant must not be bound to a stage, got %q", p.StageID)
	}
}

// TestAttachAndSend_NoFirstMessageStartsNoMessageEffects is T6.2's "a role attached with no first message is
// addressable and starts no work" guarantee: the committed effects (task, participant, thread) are identical
// to the first-message case, but the executor must never be spawned — codex's verify finding on this task
// caught that an earlier version scheduled (and the old test waited for completion of) a task with no work to
// do at all.
func TestAttachAndSend_NoFirstMessageStartsNoMessageEffects(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	var calls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		calls.Add(1)
		return passResultOutcome(), nil
	}

	res, err := o.AttachAndSend(ctx, AttachAndSendRequest{
		OperationID: "op-as-nomsg", ProfileID: "reviewer", Scope: assistants.ScopeStage, StageID: assistantsTestStageID,
		Title: "Reviewer", Instruction: "look at this", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AttachAndSend: %v", err)
	}
	if res.MessageID != nil {
		t.Fatalf("MessageID = %v, want nil when FirstMessage is nil", res.MessageID)
	}

	rs := store.Snapshot()
	task, ok := rs.Assistants.Tasks[res.TaskID]
	if !ok {
		t.Fatal("task not found in projection")
	}
	if task.Status != assistants.StatusQueued {
		t.Fatalf("task.Status = %q, want queued — no first message must start no work", task.Status)
	}
	if len(rs.Assistants.Mesh.Messages) != 0 {
		t.Fatalf("Mesh.Messages = %d, want 0 — no message effects without a FirstMessage", len(rs.Assistants.Mesh.Messages))
	}
	// Still addressable: the participant and its (empty) thread exist.
	if _, ok := rs.Assistants.Mesh.Participants[res.ParticipantID]; !ok {
		t.Fatal("participant must still be registered even with no first message")
	}
	if _, ok := rs.Assistants.Mesh.Threads[res.ThreadID]; !ok {
		t.Fatal("thread must still be opened even with no first message")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("runHelperAgent invoked %d times, want 0 — attach with no first message must not spawn the executor", got)
	}
}

// TestAttachAndSend_AtomicOrNothing: every induced failure writes NOTHING — no new task, participant, thread
// or message. Four of the five scenarios dev-plan.md's T6.2 lists correspond to gates that exist in this
// codebase today; "exceeded MaxTasks quota" is included for real because AttachAndSend (unlike Attach) DOES
// reserve 1 unit of AssistantsState.QuotaReserved against Collaboration.Limits.MaxTasks — see
// agentcomm_attach.go's doc comment for why that is a deliberate addition, not scope creep.
func TestAttachAndSend_AtomicOrNothing(t *testing.T) {
	msg := "hi"
	cases := []struct {
		name   string
		broken bool // use newBrokenProfileTestOrchestrator instead of newAssistantsTestOrchestrator
		setup  func(t *testing.T, o *Orchestrator, store *state.Store)
		req    AttachAndSendRequest
	}{
		{
			name: "unknown profile",
			req: AttachAndSendRequest{
				OperationID: "op-fail-1", ProfileID: "does-not-exist", Scope: assistants.ScopeStage, StageID: assistantsTestStageID,
				Title: "X", Instruction: "x", FirstMessage: &msg, Actor: state.Actor{Kind: "user"},
			},
		},
		{
			name:   "unavailable profile",
			broken: true,
			req: AttachAndSendRequest{
				OperationID: "op-fail-2", ProfileID: "broken", Scope: assistants.ScopeStage, StageID: assistantsTestStageID,
				Title: "X", Instruction: "x", FirstMessage: &msg, Actor: state.Actor{Kind: "user"},
			},
		},
		{
			name: "closed run (finalizing)",
			setup: func(t *testing.T, o *Orchestrator, store *state.Store) {
				store.SetRunFinalizing(true)
			},
			req: AttachAndSendRequest{
				OperationID: "op-fail-3", ProfileID: "reviewer", Scope: assistants.ScopeStage, StageID: assistantsTestStageID,
				Title: "X", Instruction: "x", FirstMessage: &msg, Actor: state.Actor{Kind: "user"},
			},
		},
		{
			name: "terminal stage",
			setup: func(t *testing.T, o *Orchestrator, store *state.Store) {
				o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
				waitStageStatus(t, store, assistantsTestStageID, state.StatusDone, 2*time.Second)
			},
			req: AttachAndSendRequest{
				OperationID: "op-fail-4", ProfileID: "reviewer", Scope: assistants.ScopeStage, StageID: assistantsTestStageID,
				Title: "X", Instruction: "x", FirstMessage: &msg, Actor: state.Actor{Kind: "user"},
			},
		},
		{
			name: "exceeded MaxTasks quota",
			setup: func(t *testing.T, o *Orchestrator, store *state.Store) {
				zero := 0
				o.opts.Config.Collaboration.Limits.MaxTasks = &zero
			},
			req: AttachAndSendRequest{
				OperationID: "op-fail-5", ProfileID: "reviewer", Scope: assistants.ScopeStage, StageID: assistantsTestStageID,
				Title: "X", Instruction: "x", FirstMessage: &msg, Actor: state.Actor{Kind: "user"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var o *Orchestrator
			var store *state.Store
			if tc.broken {
				o, store = newBrokenProfileTestOrchestrator(t)
			} else {
				o, store = newAssistantsTestOrchestrator(t, true)
			}
			activateStage(t, o, assistantsTestStageID)
			stubPassingHelper(o)
			if tc.setup != nil {
				tc.setup(t, o, store)
			}

			before := store.Snapshot()
			bt, bp, bth, bm := meshSnapshotCounts(before)

			if _, err := o.AttachAndSend(context.Background(), tc.req); err == nil {
				t.Fatalf("%s: AttachAndSend: want error, got nil", tc.name)
			}

			after := store.Snapshot()
			at, ap, ath, am := meshSnapshotCounts(after)
			if at != bt || ap != bp || ath != bth || am != bm {
				t.Fatalf("%s: partial attach detected: before=(tasks=%d participants=%d threads=%d messages=%d) after=(tasks=%d participants=%d threads=%d messages=%d)",
					tc.name, bt, bp, bth, bm, at, ap, ath, am)
			}
		})
	}
}

// waitStageStatus polls store for stageID to reach want — same shape as waitForTaskStatus but for a stage.
func waitStageStatus(t *testing.T, store *state.Store, stageID string, want state.StageStatus, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if store.Snapshot().Stages[stageID].Status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("stage %s status = %s, want %s", stageID, store.Snapshot().Stages[stageID].Status, want)
}

// TestAttachAndSend_IdempotentPerOperation: a replay of the SAME operation id and SAME request returns the
// SAME ids and does not re-send the first message; a DIFFERENT body under the SAME operation id is rejected
// with ErrOperationIDConflict.
func TestAttachAndSend_IdempotentPerOperation(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	stubPassingHelper(o)

	msg := "hi"
	req := AttachAndSendRequest{
		OperationID: "op-as-idem", ProfileID: "reviewer", Scope: assistants.ScopeStage, StageID: assistantsTestStageID,
		Title: "Reviewer", Instruction: "look at this", FirstMessage: &msg, Actor: state.Actor{Kind: "user"},
	}

	r1, err := o.AttachAndSend(ctx, req)
	if err != nil {
		t.Fatalf("AttachAndSend (first): %v", err)
	}
	r2, err := o.AttachAndSend(ctx, req)
	if err != nil {
		t.Fatalf("AttachAndSend (retry of the same operation id): %v", err)
	}
	if r1.TaskID != r2.TaskID || r1.ParticipantID != r2.ParticipantID || r1.ThreadID != r2.ThreadID {
		t.Fatalf("retry returned different ids: first=%+v retry=%+v", r1, r2)
	}
	if r1.MessageID == nil || r2.MessageID == nil || *r1.MessageID != *r2.MessageID {
		t.Fatalf("retry returned a different message id: first=%v retry=%v", r1.MessageID, r2.MessageID)
	}

	rs := store.Snapshot()
	if len(rs.Assistants.Tasks) != 1 {
		t.Fatalf("Tasks = %d, want exactly 1 (no duplicate task on replay)", len(rs.Assistants.Tasks))
	}
	if len(rs.Assistants.Mesh.MessagesByThread[r1.ThreadID]) != 1 {
		t.Fatalf("thread has %d messages, want exactly 1 (no re-send on replay)", len(rs.Assistants.Mesh.MessagesByThread[r1.ThreadID]))
	}

	// A DIFFERENT body under the SAME operation id must be rejected, not silently accepted.
	other := "a completely different message"
	conflicting := req
	conflicting.FirstMessage = &other
	if _, err := o.AttachAndSend(ctx, conflicting); !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("conflicting replay: err = %v, want ErrOperationIDConflict", err)
	}

	// Первое сообщение запустило задачу: дожидаемся её, иначе она допишет файлы после удаления TempDir.
	waitForTaskStatus(t, store, r1.TaskID, assistants.StatusCompleted, 2*time.Second)
}

// TestAttachAndSend_RejectsClientSuppliedCommand documents and verifies, structurally, that
// AttachAndSendRequest has no command/model/executor field at all — the executor is resolved server-side from
// ProfileID alone, mirroring AttachRequest's own guarantee (TestAttach_RejectsClientSuppliedCommand,
// pkg/server/assistants_test.go). There is nowhere for a client-supplied value to land; this test asserts that
// invariant by reflecting over the type rather than merely asserting it in a comment.
func TestAttachAndSend_RejectsClientSuppliedCommand(t *testing.T) {
	typ := reflect.TypeOf(AttachAndSendRequest{})
	forbidden := []string{"Command", "Args", "Env", "Cwd", "Model"}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		for _, f := range forbidden {
			if name == f {
				t.Fatalf("AttachAndSendRequest has a %q field — the executor must be resolved server-side from ProfileID alone", f)
			}
		}
	}
}

// A role attached with no first message stays queued — until somebody actually writes to it: its first
// addressed message starts it (before, it stayed queued forever and never answered).
func TestAttachAndSend_NoFirstMessage_FirstAddressedMessageStartsTheAgent(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	stubPassingHelper(o)

	res, err := o.AddRunAgent(ctx, AddRunAgentRequest{
		OperationID: "op-idle-qa", ProfileID: "qa", Title: "Idle QA", Instruction: "answer questions", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AddRunAgent: %v", err)
	}
	if st := store.Snapshot().Assistants.Tasks[res.TaskID].Status; st != assistants.StatusQueued {
		t.Fatalf("status = %s, want queued before any message", st)
	}
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-idle-hello", ThreadID: res.ThreadID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{res.ParticipantID}, Kind: agentcomm.MessageKindRequest, Body: "are you there?",
		Actor: state.Actor{Kind: "user"},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitForRunAgentTurn(t, store, res.TaskID, 2*time.Second)
}

// A helper that fails to start reports WHY (e.g. a host binary of the wrong architecture in the container)
// instead of a bare "helper execution failed".
func TestHelperStartFailure_ReasonCarriesTheProcessError(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessErr: errors.New("agent start failed: codex: fork/exec /usr/local/bin/codex: exec format error")}, nil
	}
	msg := "check the api"
	res, err := o.AddRunAgent(ctx, AddRunAgentRequest{
		OperationID: "op-broken", ProfileID: "reviewer", Title: "Reviewer", Instruction: "review", FirstMessage: &msg,
		Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AddRunAgent: %v", err)
	}
	task := waitForTaskStatus(t, store, res.TaskID, assistants.StatusFailed, 2*time.Second)
	if !strings.Contains(task.StatusReason, "exec format error") {
		t.Fatalf("reason = %q, want the process error", task.StatusReason)
	}
}

// Work a message starts must not die with the HTTP request that carried it: the dashboard handler returns (its
// request context is cancelled) while the agent's turn is still running.
func TestSend_StartedWorkSurvivesTheRequestContext(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		time.Sleep(50 * time.Millisecond)
		if ctx.Err() != nil {
			return executor.HelperOutcome{Interrupted: true}, nil
		}
		return passResultOutcome(), nil
	}
	res, err := o.AddRunAgent(context.Background(), AddRunAgentRequest{
		OperationID: "op-ctx-qa", ProfileID: "qa", Title: "QA", Instruction: "answer", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AddRunAgent: %v", err)
	}
	reqCtx, cancel := context.WithCancel(context.Background())
	if _, err := o.Send(reqCtx, SendRequest{
		OperationID: "op-ctx-hello", ThreadID: res.ThreadID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{res.ParticipantID}, Kind: agentcomm.MessageKindRequest, Body: "hi",
		Actor: state.Actor{Kind: "user"},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	cancel() // the HTTP handler returned
	waitForRunAgentTurn(t, store, res.TaskID, 2*time.Second)
}

// TestAddRunAgent_AliasIsUniqueInTheRun: two run agents with the same title get distinct @names, so an @name
// always names exactly one participant.
func TestAddRunAgent_AliasIsUniqueInTheRun(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	first := addTestRunAgent(t, o, "op-qa-1", nil)
	second := addTestRunAgent(t, o, "op-qa-2", nil)

	rs := store.Snapshot()
	a, b := rs.Assistants.Mesh.Participants[first.ParticipantID].Alias, rs.Assistants.Mesh.Participants[second.ParticipantID].Alias
	if a != "qa" || b != "qa-2" {
		t.Fatalf("aliases = %q, %q, want qa and qa-2", a, b)
	}
	roster := o.liveRoster(rs)
	for alias, want := range map[string]agentcomm.ParticipantID{"@qa": first.ParticipantID, "@qa-2": second.ParticipantID} {
		if got, err := agentcomm.ResolveAlias(alias, roster); err != nil || got != want {
			t.Fatalf("ResolveAlias(%s) = %s, %v, want %s", alias, got, err, want)
		}
	}
}

// TestTopLevelAttach_SharesOneQuota: Attach and AttachAndSend admit top-level tasks against the same
// max_tasks — one of each cannot exceed a limit of one.
func TestTopLevelAttach_SharesOneQuota(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	one := 1
	o.opts.Config.Collaboration.Limits.MaxTasks = &one

	if _, err := o.Attach(context.Background(), AttachRequest{OperationID: "op-helper", StageID: assistantsTestStageID, ProfileID: "reviewer", Instruction: "look", Actor: state.Actor{Kind: "user"}}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	_, err := o.AddRunAgent(context.Background(), AddRunAgentRequest{OperationID: "op-run", ProfileID: "qa", Title: "QA", Actor: state.Actor{Kind: "user"}})
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeBudgetExceeded {
		t.Fatalf("a second top-level task over max_tasks=1: err = %v, want budget_exceeded", err)
	}
	if got := store.Snapshot().Assistants.QuotaReserved; got != 1 {
		t.Fatalf("QuotaReserved = %d, want 1", got)
	}
}

// TestCommittedRequestReplaysAfterStateChange: a retry of a request whose response was lost replays the
// committed outcome even when the state it was validated against has changed since — here the stage
// finished, which a fresh attach, discussion post or thread open would be refused or built differently for.
func TestCommittedRequestReplaysAfterStateChange(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	stage := meshStageParticipantID(assistantsTestStageID)

	attach := AttachRequest{OperationID: "op-attach", StageID: assistantsTestStageID, ProfileID: "reviewer", Instruction: "look", Actor: state.Actor{Kind: "user"}}
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}
	taskID, err := o.Attach(ctx, attach)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	post := PostMessageRequest{OperationID: "op-post", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorHuman, Round: assistants.RoundFreeform, Body: "hello", Actor: state.Actor{Kind: "user"}}
	postID, err := o.PostDiscussionMessage(ctx, post)
	if err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}
	open := OpenThreadRequest{OperationID: "op-open", Scope: agentcomm.ScopeStage, Members: []agentcomm.ParticipantID{meshUserParticipantID, stage}}
	thread, err := o.OpenThread(ctx, open)
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	if _, ok := o.Trigger(assistantsTestStageID, bus.EvComplete, bus.GuardCtx{}, ""); !ok {
		t.Fatal("EvComplete failed")
	}

	if again, err := o.Attach(ctx, attach); err != nil || again != taskID {
		t.Fatalf("Attach retry after the stage finished = %s, %v, want %s", again, err, taskID)
	}
	if again, err := o.PostDiscussionMessage(ctx, post); err != nil || again != postID {
		t.Fatalf("discussion post retry after the stage finished = %s, %v, want %s", again, err, postID)
	}
	if again, err := o.OpenThread(ctx, open); err != nil || again.ID != thread.ID {
		t.Fatalf("thread open retry after the stage finished = %+v, %v, want %s", again, err, thread.ID)
	}

	changed := attach
	changed.Instruction = "something else"
	if _, err := o.Attach(ctx, changed); !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("the same request id for a different attach: err = %v, want a conflict", err)
	}
}
