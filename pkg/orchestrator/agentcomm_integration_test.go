// Package orchestrator (this file, Agent Mesh Phase 8, T8.1): a deterministic, end-to-end acceptance scenario
// for two run-scoped mesh roles — "QA" and "DevOps" — cooperating entirely through the mesh primitives Phases
// 2-6 already built (AddRunAgent, OpenThread/Send, Wait/Publish/ReadBoard), with NO UI and NO stage involved
// at all: dev-plan.md's T8.1 explicitly asks for "no UI involved". Package `orchestrator` (white-box, same
// convention as agentcomm_peer_question_test.go/agentcomm_deadline_test.go, this task's own closest
// precedent) rather than the external `orchestrator_test` package run_barrier_test.go uses: this scenario is
// entirely about scope=run participants, whose resumption (tryResumePeer/resumeTaskViaCAS) is driven
// synchronously from Send/Publish's own externalWork — exactly like TestWait_SurvivesCounterQuestion already
// demonstrates — and never depends on Orchestrator.Run's own event loop (that loop only matters for
// STAGE-scoped awaiting_peer, which this scenario never touches). The one sub-scenario that genuinely needs
// white-box access — "a restart in the middle", which must engineer an EXACT crash point between a durable
// commit and its live resume — reuses assistants_crash_recovery_test.go's own established
// store.ApplyRunOperation(op, nil) idiom, only reachable from inside this package.
//
// max_parallel: 1 (dev-plan.md's own wording for this task): newMeshIntegrationOrchestrator's Manager is
// built with globalMaxParallel=1, and in any case QA/DevOps's shared "helper-cmd" profile command is never
// declared by any stage, so concurrency.Manager's own semForCmd would give it a conservative single-flight
// semaphore regardless (see concurrency.go's own doc comment) — either way, QA and DevOps can never actually
// run concurrently, proving the scenario's two colleagues cooperate correctly even serialized onto one slot
// (a waiting participant's own goroutine exits and releases its slot, T5.4's own invariant).
package orchestrator

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

const meshIntegrationStageID = "impl"

var (
	meshIntegrationQAProfile     = assistants.ProfileID("qa")
	meshIntegrationDevOpsProfile = assistants.ProfileID("devops")
)

// newMeshIntegrationOrchestrator builds a minimal, real (non-mocked) Store + graph + concurrency Manager with
// two run-scoped profiles ("qa"/"devops", sharing one command so they contend for the SAME single-flight
// slot — see this file's own header comment) — mirrors newAssistantsTestOrchestrator (assistants_test.go) but
// with the exact profile pair and MaxParallel this scenario needs, kept as its own fixture rather than adding
// parameters to that shared one (same reasoning run_barrier_test.go's newRunBarrierTestOrch gives for being
// its own fixture).
func newMeshIntegrationOrchestrator(t *testing.T) (*Orchestrator, *state.Store) {
	t.Helper()
	dir := t.TempDir()
	stages := []flow.Stage{{ID: meshIntegrationStageID, Command: "claude"}}
	store, err := state.Open(dir, []string{meshIntegrationStageID})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-1"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}

	reg, err := assistants.BuildRegistry(nil, []assistants.Profile{
		{ID: meshIntegrationQAProfile, Title: "QA", Command: "helper-cmd"},
		{ID: meshIntegrationDevOpsProfile, Title: "DevOps", Command: "helper-cmd"},
	}, nil, "claude",
		func(cmd string) (bool, string, assistants.ReadonlyEnforcement) {
			return true, "", assistants.ReadonlyEnforcementOS
		})
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}

	o := &Orchestrator{
		opts: Options{
			RunDir: dir, Stages: stages, Store: store, RunID: "run1",
			AssistantsEnabled: true, AssistantsRegistry: reg,
		},
		graph:         graph.NewStore(graph.NewRegistry(stages)),
		ui:            bus.NewUIBus(),
		critical:      bus.NewCriticalBus(16),
		fsm:           bus.NewFSM(store),
		concurrency:   concurrency.New(bus.NewCriticalBus(16), stages, "claude", 1, nil), // max_parallel: 1
		fingerprintFn: func(string) (string, bool) { return "", false },
	}
	return o, store
}

// qaDevOpsScenarioResult names everything TestAgentMesh_QADevOps_EndToEnd and
// TestAgentMesh_QADevOps_WithHTTPServerPresent both need to assert on — the same scenario driven twice (with
// and without a server present), so both tests can check they converge on an IDENTICAL outcome.
type qaDevOpsScenarioResult struct {
	QATaskID, DevOpsTaskID           assistants.TaskID
	QACalls, DevOpsCalls             *atomic.Int32
	FormatThreadID, QuestionThreadID agentcomm.ThreadID
}

// runQADevOpsScenario drives dev-plan.md's own T8.1 scenario end to end: two run-scoped roles (QA, DevOps)
// agree a report format in a thread, QA publishes board:qa.tests=ready with an artifact ref, DevOps's board
// wait wakes and it uses the agreed result — and, along the way, DevOps answers a genuine counter-question
// from a colleague WHILE its board wait is still pending (T5.5), without that counter-question ever being
// mistaken for the board becoming ready. Mirrors agentcomm_peer_question_test.go's TestWait_SurvivesCounterQuestion
// shape: a scripted fake runHelperAgent calling Service methods directly, driven via call-count switches.
func runQADevOpsScenario(t *testing.T, o *Orchestrator, store *state.Store) qaDevOpsScenarioResult {
	t.Helper()
	ctx := context.Background()
	qaParticipant := meshRunParticipantID(deriveTaskID("op-add-qa"))
	devopsParticipant := meshRunParticipantID(deriveTaskID("op-add-devops"))
	formatThreadID := deriveMeshThreadID("op-qa-devops-format-open")

	var qaCalls, devopsCalls atomic.Int32
	// goAheadThreadID gates QA's actual publish until the test has had a chance to prove DevOps's board wait
	// survives an unrelated counter-question WHILE still pending — without this gate, QA's publish could race
	// ahead and resolve the board before the counter-question is even sent, silently skipping the T5.5
	// assertion this scenario exists to cover. This MUST be a real mesh Wait, not a Go channel QA's own
	// goroutine blocks on in-process: under this fixture's single-flight "helper-cmd" semaphore (max_parallel:
	// 1), a blocked-but-still-running QA turn would hold the one concurrency slot forever, starving DevOps's
	// own bounded counter-question turn of ever being scheduled at all — a real deadlock this scenario would
	// otherwise hit. Yielding via Wait releases the slot exactly like every other park in this package.
	goAheadThreadID := deriveMeshThreadID("op-go-ahead-open")

	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		switch assistants.ProfileID(profileID) {
		case meshIntegrationQAProfile:
			switch qaCalls.Add(1) {
			case 1:
				// QA is attached BEFORE DevOps even exists as a mesh participant (see the attach ordering
				// below), so its own first turn cannot OpenThread naming DevOps yet — it only registers a Wait
				// on the format thread DevOps will open shortly (perfectly valid for a not-yet-existing
				// ThreadID, same "t-does-not-exist-yet" convention agentcomm_deadline_test.go's own fixtures
				// use) and yields.
				if _, err := o.Wait(context.Background(), WaitRequest{
					OperationID: "op-qa-wait-format", Waiter: qaParticipant,
					Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: formatThreadID},
				}); err != nil {
					t.Errorf("Wait (format): %v", err)
				}
				return executor.HelperOutcome{ProcessOK: true}, nil
			case 2:
				// DevOps proposed the format (its own first turn, below) — agree it, then yield on a SEPARATE
				// go-ahead wait (not yet opened by anyone — valid, see QA's first turn's own comment) until the
				// test confirms DevOps's counter-question turn ran without resolving its board wait.
				if _, err := o.Send(context.Background(), SendRequest{
					OperationID: "op-qa-devops-format-agree", ThreadID: formatThreadID, Sender: qaParticipant,
					Recipients: []agentcomm.ParticipantID{devopsParticipant}, Kind: agentcomm.MessageKindMessage,
					Body: "agreed, that format works for me",
				}); err != nil {
					t.Errorf("Send (agree): %v", err)
				}
				if _, err := o.Wait(context.Background(), WaitRequest{
					OperationID: "op-qa-wait-go-ahead", Waiter: qaParticipant,
					Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: goAheadThreadID},
				}); err != nil {
					t.Errorf("Wait (go-ahead): %v", err)
				}
				return executor.HelperOutcome{ProcessOK: true}, nil
			case 3:
				if _, err := o.Publish(context.Background(), PublishRequest{
					OperationID: "op-qa-publish", Author: qaParticipant, Key: "qa.tests", Status: "ready",
					ArtifactRefs: []string{"artifacts/qa-report.json"},
				}); err != nil {
					t.Errorf("Publish: %v", err)
				}
				return passResultOutcome(), nil
			default:
				t.Errorf("qa invoked unexpectedly a %d-th time", qaCalls.Load())
				return executor.HelperOutcome{}, nil
			}
		case meshIntegrationDevOpsProfile:
			n := devopsCalls.Add(1)
			if n == 1 {
				// DevOps is attached AFTER QA (see below), so "run:qa" is already a known participant by now —
				// open the format thread, propose, then register the board wait and yield. Opening this thread
				// resolves QA's own Wait above (WaitOnAddressedMessage), spawning QA's call #2 asynchronously.
				th, err := o.OpenThread(context.Background(), OpenThreadRequest{
					OperationID: "op-qa-devops-format-open", Title: "report format", Scope: agentcomm.ScopeRun,
					Members: []agentcomm.ParticipantID{devopsParticipant, qaParticipant}, Actor: state.Actor{Kind: "assistant", ID: taskID},
				})
				if err != nil {
					t.Errorf("OpenThread (format): %v", err)
					return executor.HelperOutcome{}, err
				}
				if th.ID != formatThreadID {
					t.Errorf("format thread id = %s, want %s (deriveMeshThreadID must be deterministic)", th.ID, formatThreadID)
				}
				if _, err := o.Send(context.Background(), SendRequest{
					OperationID: "op-qa-devops-format-propose", ThreadID: formatThreadID, Sender: devopsParticipant,
					Recipients: []agentcomm.ParticipantID{qaParticipant}, Kind: agentcomm.MessageKindMessage,
					Body: `proposing report format: {"status": "...", "failures": [...]}`,
				}); err != nil {
					t.Errorf("Send (propose): %v", err)
				}
				deadline := time.Now().Add(time.Hour) // never fires in this test; ErrWaitRequiresDeadline requires one for an unpublished key
				if _, err := o.Wait(context.Background(), WaitRequest{
					OperationID: "op-do-wait-board", Waiter: devopsParticipant, Deadline: &deadline,
					Condition:        agentcomm.WaitCondition{Kind: agentcomm.WaitOnBoardKey, Key: "qa.tests", Status: "ready"},
					ContinuationNote: "waiting for QA to publish the test report",
				}); err != nil {
					t.Errorf("Wait (board): %v", err)
				}
				return executor.HelperOutcome{ProcessOK: true}, nil
			}
			// n >= 2: DevOps was resumed for SOME addressed message while its board wait was still pending.
			// Which message that was is a genuine race, not a bug: QA's own "agree" reply (above) ALSO
			// addresses DevOps while it is parked, exactly like the test's deliberate counter-question does —
			// a real agent would see whichever happens to be in its inbox on a given bounded turn and should
			// only actually reply to the genuine question, never crash on an unrelated one. Check the board
			// first (the REAL wake, once QA has published); otherwise look for the counter-question's own text
			// and answer it if present, silently re-yielding for any other (e.g. QA's plain agreement, which
			// needs no reply from DevOps).
			if page, err := o.ReadBoard(context.Background(), ReadBoardRequest{Key: "qa.tests"}); err == nil && len(page.Entries) == 1 && page.Entries[0].Status == "ready" {
				return passResultOutcome(), nil
			}
			inbox, err := o.ReadInbox(context.Background(), ReadInboxRequest{Participant: devopsParticipant})
			if err != nil {
				t.Errorf("ReadInbox: %v", err)
			}
			for _, m := range inbox.Messages {
				if strings.Contains(m.Body, "quick one") {
					if _, err := o.Send(context.Background(), SendRequest{
						OperationID: "op-cq-answer", ThreadID: deriveMeshThreadID("op-cq-open"), Sender: devopsParticipant,
						Recipients: []agentcomm.ParticipantID{meshUserParticipantID}, Kind: agentcomm.MessageKindMessage,
						Body: "yes, count skipped tests as failures too",
					}); err != nil {
						t.Errorf("Send (cq answer): %v", err)
					}
					break
				}
			}
			return executor.HelperOutcome{ProcessOK: true}, nil
		default:
			return passResultOutcome(), nil
		}
	}

	qaMsg := "please agree the report format with DevOps, then publish your result"
	qaRes, err := o.AddRunAgent(ctx, AddRunAgentRequest{OperationID: "op-add-qa",
		ProfileID: meshIntegrationQAProfile, Title: "QA", Instruction: "watch for regressions",
		FirstMessage: &qaMsg, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AddRunAgent (qa): %v", err)
	}
	// QA is attached FIRST and DevOps SECOND, deliberately: "run:devops" cannot be a thread member until
	// DevOps has actually been attached (AttachAndSend's own participant registration), so QA's own first turn
	// (above) never names it — only DevOps's first turn, which runs once "run:qa" already exists, opens the
	// shared thread. Waiting for QA to park here is not required for that ordering (already satisfied by
	// attaching QA first), only to keep this scenario's own turns easy to reason about one at a time.
	waitForTaskStatus(t, store, qaRes.TaskID, assistants.StatusWaitingPeer, 5*time.Second)

	doMsg := "verify tests once QA reports ready"
	doRes, err := o.AddRunAgent(ctx, AddRunAgentRequest{OperationID: "op-add-devops",
		ProfileID: meshIntegrationDevOpsProfile, Title: "DevOps", Instruction: "verify the pipeline",
		FirstMessage: &doMsg, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AddRunAgent (devops): %v", err)
	}
	waitForTaskStatus(t, store, doRes.TaskID, assistants.StatusWaitingPeer, 5*time.Second)

	// DevOps's genuine counter-question (T5.5), asked by a colleague while its board wait is still pending —
	// mirrors TestWait_SurvivesCounterQuestion's own "B, always a known participant, no extra fixture needed"
	// convention (B here is the human "user"). Must be answered, and must NOT resolve the board wait, before
	// QA is allowed to publish (qaPublishRelease, closed below).
	cqThread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-cq-open", Title: "a quick question", Scope: agentcomm.ScopeRun,
		Members: []agentcomm.ParticipantID{devopsParticipant, meshUserParticipantID}, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("OpenThread (cq): %v", err)
	}
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-cq-question", ThreadID: cqThread.ID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{devopsParticipant}, Kind: agentcomm.MessageKindMessage,
		Body: "quick one while you wait: count skipped tests as failures?", Actor: state.Actor{Kind: "user"},
	}); err != nil {
		t.Fatalf("Send (cq question): %v", err)
	}

	// Poll the THREAD, not devopsCalls' raw count: whether DevOps answers in its very next bounded turn or an
	// earlier one already got spent on QA's own unrelated "agree" reply (both address DevOps while its board
	// wait is pending — a genuine race between two real colleagues, not a bug; see the content-driven handler
	// above) is immaterial to this scenario's own claim, which is only that the question gets answered at all,
	// promptly, without resolving the board wait.
	waitForThreadMessageCount(t, store, cqThread.ID, 2, 2*time.Second)
	waitForTaskStatus(t, store, doRes.TaskID, assistants.StatusWaitingPeer, 2*time.Second)
	// QA must also have settled back into waiting_peer on its OWN go-ahead wait by now (case 2, above) before
	// the test sends that go-ahead below — otherwise the go-ahead message could arrive before QA ever
	// registers the wait it is meant to resolve.
	waitForTaskStatus(t, store, qaRes.TaskID, assistants.StatusWaitingPeer, 2*time.Second)

	rs := store.Snapshot()
	var boardWaitID agentcomm.WaitID
	for id, w := range rs.Assistants.Mesh.Waits {
		if w.Waiter == devopsParticipant && w.Condition.Kind == agentcomm.WaitOnBoardKey {
			boardWaitID = id
		}
	}
	if boardWaitID == "" {
		t.Fatal("no board_key wait found for devops")
	}
	if got := rs.Assistants.Mesh.Waits[boardWaitID].Status; got != agentcomm.WaitPending {
		t.Fatalf("board wait status = %v, want still pending — the counter-question must not resolve it", got)
	}

	goAheadThread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-go-ahead-open", Title: "go ahead", Scope: agentcomm.ScopeRun,
		Members: []agentcomm.ParticipantID{qaParticipant, meshUserParticipantID}, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("OpenThread (go-ahead): %v", err)
	}
	if goAheadThread.ID != goAheadThreadID {
		t.Fatalf("go-ahead thread id = %s, want %s", goAheadThread.ID, goAheadThreadID)
	}
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-go-ahead-send", ThreadID: goAheadThreadID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{qaParticipant}, Kind: agentcomm.MessageKindMessage,
		Body: "go ahead, publish your result", Actor: state.Actor{Kind: "user"},
	}); err != nil {
		t.Fatalf("Send (go-ahead): %v", err)
	}

	waitForRunAgentTurn(t, store, qaRes.TaskID, 5*time.Second)
	waitForRunAgentTurn(t, store, doRes.TaskID, 5*time.Second)

	return qaDevOpsScenarioResult{
		QATaskID: qaRes.TaskID, DevOpsTaskID: doRes.TaskID,
		QACalls: &qaCalls, DevOpsCalls: &devopsCalls,
		FormatThreadID: formatThreadID, QuestionThreadID: cqThread.ID,
	}
}

// waitForThreadMessageCount polls store until threadID holds at least want messages, or fails the test after
// timeout — used instead of a call-count assertion where exactly WHICH turn produced the message is a benign
// race (see runQADevOpsScenario's own comment at its call site).
func waitForThreadMessageCount(t *testing.T, store *state.Store, threadID agentcomm.ThreadID, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(messagesInThread(store.Snapshot(), threadID)) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("thread %s did not reach %d messages within %s (got %d)", threadID, want, timeout, len(messagesInThread(store.Snapshot(), threadID)))
}

// messagesInThread returns every message in rs addressed to threadID, sorted by Seq (the same order ReadThread
// itself would page through) — used for simple, readable positional assertions ("propose, then agree") rather
// than hunting by content.
func messagesInThread(rs state.RunState, threadID agentcomm.ThreadID) []agentcomm.Message {
	var msgs []agentcomm.Message
	for _, m := range rs.Assistants.Mesh.Messages {
		if m.ThreadID == threadID {
			msgs = append(msgs, *m)
		}
	}
	slices.SortFunc(msgs, func(a, b agentcomm.Message) int {
		switch {
		case a.Seq < b.Seq:
			return -1
		case a.Seq > b.Seq:
			return 1
		default:
			return 0
		}
	})
	return msgs
}

// TestAgentMesh_QADevOps_EndToEnd is T8.1's central claim: run it with `-race -count=N` for the "passes
// deterministically" requirement.
func TestAgentMesh_QADevOps_EndToEnd(t *testing.T) {
	o, store := newMeshIntegrationOrchestrator(t)
	qaParticipant := meshRunParticipantID(deriveTaskID("op-add-qa"))
	devopsParticipant := meshRunParticipantID(deriveTaskID("op-add-devops"))

	result := runQADevOpsScenario(t, o, store)

	if got := result.QACalls.Load(); got != 3 {
		t.Fatalf("qa invoked %d times, want exactly 3 (wait for the format, agree+wait for go-ahead, then publish)", got)
	}
	// DevOps's own exact call count is NOT pinned to 3: QA's "agree" reply and the test's own counter-question
	// both address DevOps while its board wait is pending, so whether those land as one batched bounded turn
	// or two separate ones is a benign scheduling race (runQADevOpsScenario's own comment) — at minimum,
	// though, there must be the initial propose+wait turn, at least one bounded turn, and the final
	// use-the-result turn.
	if got := result.DevOpsCalls.Load(); got < 3 {
		t.Fatalf("devops invoked %d times, want at least 3 (propose+wait, >=1 bounded turn, then use the result)", got)
	}

	rs := store.Snapshot()
	entry, ok := rs.Assistants.Mesh.Board["qa.tests"]
	if !ok || entry.Status != "ready" || entry.AuthorID != qaParticipant {
		t.Fatalf("board entry = %+v, want ready and authored by qa", entry)
	}
	if len(entry.ArtifactRefs) != 1 || entry.ArtifactRefs[0] != "artifacts/qa-report.json" {
		t.Fatalf("board artifact refs = %v, want the published report", entry.ArtifactRefs)
	}

	formatMsgs := messagesInThread(rs, result.FormatThreadID)
	if len(formatMsgs) != 2 {
		t.Fatalf("format thread has %d messages, want exactly 2 (propose, agree)", len(formatMsgs))
	}
	if formatMsgs[0].Sender != devopsParticipant || formatMsgs[1].Sender != qaParticipant {
		t.Fatalf("format thread authors = %s/%s, want devops then qa", formatMsgs[0].Sender, formatMsgs[1].Sender)
	}
	if len(formatMsgs[0].Recipients) != 1 || formatMsgs[0].Recipients[0] != qaParticipant {
		t.Fatalf("format proposal recipients = %v, want exactly qa", formatMsgs[0].Recipients)
	}

	cqMsgs := messagesInThread(rs, result.QuestionThreadID)
	if len(cqMsgs) != 2 {
		t.Fatalf("counter-question thread has %d messages, want exactly 2 (question, answer)", len(cqMsgs))
	}
	if cqMsgs[0].Sender != meshUserParticipantID || cqMsgs[1].Sender != devopsParticipant {
		t.Fatalf("counter-question authors = %s/%s, want user then devops", cqMsgs[0].Sender, cqMsgs[1].Sender)
	}
	if cqMsgs[1].InReplyTo == nil && cqMsgs[1].Recipients[0] != meshUserParticipantID {
		t.Fatalf("devops's answer recipients = %v, want the user addressed back", cqMsgs[1].Recipients)
	}

	var formatWaitReady, boardWaitReady bool
	for _, w := range rs.Assistants.Mesh.Waits {
		switch {
		case w.Condition.Kind == agentcomm.WaitOnAddressedMessage && w.Condition.ThreadID == result.FormatThreadID:
			formatWaitReady = w.Status == agentcomm.WaitReady
		case w.Condition.Kind == agentcomm.WaitOnBoardKey && w.Condition.Key == "qa.tests":
			boardWaitReady = w.Status == agentcomm.WaitReady
		default:
			// другие mesh-waits сценарий не проверяет
		}
	}
	if !formatWaitReady {
		t.Fatal("QA's own format wait did not resolve to ready")
	}
	if !boardWaitReady {
		t.Fatal("DevOps's board wait did not resolve to ready")
	}

	o.concurrency.WaitAgents()
}

// TestAgentMesh_QADevOps_RestartMidScenario is T8.1's "a restart in the middle" bullet: QA proposes the
// report format and DevOps parks on its board wait exactly like the main scenario, but QA's eventual publish
// is committed DIRECTLY (bypassing Publish's own externalWork, which would otherwise resume DevOps in the
// SAME commit) — engineering the exact crash window assistants_crash_recovery_test.go's own
// TestCrash_HelperWaitResolvedBeforeRequeue_RecoveredOnRestart already establishes for a reply: durably
// recorded, but the live resume never ran because the process went down first. A freshly reopened Store +
// Orchestrator's own startup recovery (recoverAssistants) must still carry DevOps to completion.
func TestAgentMesh_QADevOps_RestartMidScenario(t *testing.T) {
	o, store := newMeshIntegrationOrchestrator(t)
	ctx := context.Background()
	runDir := o.opts.RunDir
	qaParticipant := meshRunParticipantID(deriveTaskID("op-add-qa"))
	devopsParticipant := meshRunParticipantID(deriveTaskID("op-add-devops"))
	formatThreadID := deriveMeshThreadID("op-restart-format-open")

	var devopsCalls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		switch assistants.ProfileID(profileID) {
		case meshIntegrationDevOpsProfile:
			// DevOps is attached FIRST (see below) — its own first turn names no one else, just registers the
			// board wait and yields. QA's own propose message (below) addresses DevOps while that wait is still
			// pending, which correctly triggers a SECOND, bounded T5.5 counter-question turn here — it must not
			// re-register the SAME wait operation id with a freshly computed deadline (that would be a
			// different payload for an already-used id, rejected by ApplyRunOperation's idempotency check), so
			// only the very first call registers it.
			if devopsCalls.Add(1) == 1 {
				deadline := time.Now().Add(time.Hour)
				if _, err := o.Wait(context.Background(), WaitRequest{
					OperationID: "op-restart-wait-board", Waiter: devopsParticipant, Deadline: &deadline,
					Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnBoardKey, Key: "qa.tests", Status: "ready"},
				}); err != nil {
					t.Errorf("Wait (board): %v", err)
				}
			}
			return executor.HelperOutcome{ProcessOK: true}, nil
		case meshIntegrationQAProfile:
			// QA is attached SECOND, once "run:devops" already exists, so it can safely open a thread naming it.
			if _, err := o.OpenThread(context.Background(), OpenThreadRequest{
				OperationID: "op-restart-format-open", Title: "report format", Scope: agentcomm.ScopeRun,
				Members: []agentcomm.ParticipantID{qaParticipant, devopsParticipant},
			}); err != nil {
				t.Errorf("OpenThread: %v", err)
			}
			if _, err := o.Send(context.Background(), SendRequest{
				OperationID: "op-restart-propose", ThreadID: formatThreadID, Sender: qaParticipant,
				Recipients: []agentcomm.ParticipantID{devopsParticipant}, Kind: agentcomm.MessageKindMessage,
				Body: "proposing JSON format",
			}); err != nil {
				t.Errorf("Send (propose): %v", err)
			}
			// QA's own part ends here — this test's whole point is DevOps's recovery, so the board publish
			// below is committed by the TEST directly (standing in for "QA's agent process already published,
			// durably, before the crash"), never by a second QA turn.
			return passResultOutcome(), nil
		}
		return passResultOutcome(), nil
	}

	doMsg := "verify once QA reports ready"
	doRes, err := o.AddRunAgent(ctx, AddRunAgentRequest{OperationID: "op-add-devops",
		ProfileID: meshIntegrationDevOpsProfile, Title: "DevOps", FirstMessage: &doMsg, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AddRunAgent (devops): %v", err)
	}
	waitForTaskStatus(t, store, doRes.TaskID, assistants.StatusWaitingPeer, 2*time.Second)

	qaMsg := "propose the format, then you're done"
	qaRes, err := o.AddRunAgent(ctx, AddRunAgentRequest{OperationID: "op-add-qa",
		ProfileID: meshIntegrationQAProfile, Title: "QA", FirstMessage: &qaMsg, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AddRunAgent (qa): %v", err)
	}
	waitForRunAgentTurn(t, store, qaRes.TaskID, 2*time.Second)
	// QA's own propose message just addressed DevOps a message while its board wait was still pending — T5.5's
	// own bounded counter-question resume (now correctly reachable for a scope=run task, see
	// meshParticipantForTask's own doc comment) fires for it asynchronously; let that settle back into
	// waiting_peer before engineering the crash window below, or the plain status read right after this could
	// transiently observe "running" instead.
	waitForTaskStatus(t, store, doRes.TaskID, assistants.StatusWaitingPeer, 2*time.Second)

	// Commit JUST the board publish — bypassing Publish's own externalWork (which would immediately call
	// notifyAndResumeNewlyReadyWaits/tryResumePeer in the SAME synchronous commit) — the exact state a crash
	// between "QA's result durably recorded" and "DevOps's continuation ran" would leave behind. Mirrors
	// assistants_crash_recovery_test.go's established "nil externalWork == crashed before the follow-up ran"
	// idiom, here for a board publish instead of a message send.
	publishEff, err := marshalEffect("mesh_board_published", state.Target{BoardKey: "qa.tests", ParticipantID: string(qaParticipant)},
		state.MeshBoardPublishedPayload{Status: "ready", ArtifactRefs: []string{"artifacts/qa-report.json"}})
	if err != nil {
		t.Fatalf("build publish effect: %v", err)
	}
	op := state.Operation{ID: "op-restart-publish", Kind: "mesh_publish", Effects: []state.Effect{publishEff}}
	if _, err := store.ApplyRunOperation(op, nil); err != nil { // nil externalWork == "crashed before the live resume ran"
		t.Fatalf("commit publish (pre-crash): %v", err)
	}
	if got := store.Snapshot().Assistants.Tasks[doRes.TaskID].Status; got != assistants.StatusWaitingPeer {
		t.Fatalf("test setup: devops must still be waiting_peer pre-restart, got %s", got)
	}

	store.Close() // release the flock, standing in for the OS reclaiming it on process death

	store2, err := state.Open(runDir, []string{meshIntegrationStageID})
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store2.Close()
	o2 := reopenAssistantsOrchestrator(o, store2) // assistants_crash_recovery_test.go's own fixture, same package

	var devopsResumed bool
	o2.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if taskID == string(doRes.TaskID) {
			devopsResumed = true
			page, err := o2.ReadBoard(context.Background(), ReadBoardRequest{Key: "qa.tests"})
			if err != nil || len(page.Entries) != 1 || page.Entries[0].Status != "ready" {
				t.Errorf("ReadBoard after restart = %+v, %v, want exactly one ready entry", page.Entries, err)
			}
		}
		return passResultOutcome(), nil
	}

	o2.recoverAssistants(context.Background())

	waitForRunAgentTurn(t, store2, doRes.TaskID, 2*time.Second)
	if !devopsResumed {
		t.Fatal("recovery must actually resume DevOps whose board wait resolved before the crash, not leave it stranded in waiting_peer")
	}

	o.concurrency.WaitAgents()
	o2.concurrency.WaitAgents()
}
