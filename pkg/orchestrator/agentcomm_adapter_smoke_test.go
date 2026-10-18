// Package orchestrator_test (this file, Agent Mesh Phase 9, T9.1): the one test in this codebase that drives
// a REAL, credentialed executor adapter (claude, codex, or a configured recipe wrapper) through the
// production pieces — a real *server.Server over the agent-facing /api/agent-mesh/* surface, a real
// per-invocation bearer token minted the same way setupMeshDescriptor mints one for a live stage
// (orchestrator.MeshClientForTest, export_test.go), and the real pkg/executor.Config path (WrapperDir, env
// filtering, process-group handling) — rather than a fake runHelperAgent/runVerifyAgent seam. Phases 0-8's
// whole suite proves the mesh's OWN logic against deterministic fakes; dev-plan.md's Phase 9 intro is
// explicit that "API-only демонстрации недостаточно" — this file is the one place an actual adapter binary
// is asked to use `afm agent list/send/inbox/wait` for itself.
//
// This MUST be the external orchestrator_test package, not orchestrator (internal): it constructs a real
// *server.Server, and pkg/server already imports pkg/orchestrator (action_drafts.go's own adapter types) —
// an internal test file importing pkg/server would be a genuine import cycle. See
// agentcomm_integration_server_test.go's own doc comment for the same constraint on its meshServerActionsForTest;
// smokeMeshActions below is this file's equivalent adapter, for server.AgentMeshActions instead of
// server.MeshActions, vendored from cmd/afm/run_agent_mesh.go's agentMeshActions (unreachable here: it lives
// in package main) rather than duplicated logic — every method still delegates to the SAME real
// orchestrator.Service methods a live `afm agent` CLI request would reach through routeAgentMesh.
//
// Env-gated and skipped by default — mirrors pkg/browser/smoke_mac_test.go's smokeChromePath (real Chrome
// found, or t.Skip naming the exact env var) and `make sdk-test-integration`'s own shape (a real afm binary
// resolved from an env var or a conventional build path, a plain *_test.go name rather than a build tag).
// AFM_MESH_SMOKE_COMMAND names the adapter binary to exec (e.g. "claude", "codex", or a configured recipe
// wrapper already on PATH); its own credentials (ANTHROPIC_*, OPENAI_*, …) are whatever this process's
// ambient environment already carries — this test does not know or care which kind they are, only that the
// adapter accepts them.
//
// Scenario (dev-plan.md's own four numbered steps): the test opens one thread between "user" (the always-
// present human/run-owner participant — no agent-facing CLI route can open a thread itself, see
// routeAgentMesh's own switch) and "stage:smoke" (the real adapter's mesh identity), then runs the real
// adapter TWICE: once to (1) list participants, (2) send a message that must land durably in events.jsonl,
// (3) read an inbox message delivered WHILE that first process is still running, and register a wait that
// yields; and a second time, after the test (standing in for the reply the peer would send, and for the
// orchestrator's own resumeAfterPeerReply re-invocation this smoke test deliberately bypasses — dev-plan.md's
// Execution rule forbids bootstrapping a nested `afm run` here) delivers (4) the reply inside the exact
// <inbox> block production code builds on resume, to confirm the adapter can read it and the whole round
// trip cost exactly one new invocation with no lease held while the first one was yielding.
package orchestrator_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/server"
	"github.com/akopichin/afm/pkg/state"
)

// smokeMeshCommandEnv names the adapter binary this test drives — unset by default, so the test skips
// cleanly in CI and on any machine that has not explicitly opted into exercising a real, credentialed agent.
const smokeMeshCommandEnv = "AFM_MESH_SMOKE_COMMAND"

// smokeAFMBinEnv optionally overrides the afm binary resolveSmokeAFMBinary hands the adapter via WrapperDir
// (mirrors AFM_SDK_TEST_BINARY's role in sdk/integration_test.go). Unset means "resolve the conventional
// `make build` output path, or skip if it is not there".
const smokeAFMBinEnv = "AFM_MESH_SMOKE_AFM_BIN"

// resolveSmokeAdapterCommand returns the adapter command to exec, or skips with the exact env var to set —
// the one clause dev-plan.md's T9.1 spells out literally ("otherwise it calls t.Skip with the exact env var
// to set"). A command that IS named but not resolvable on PATH is a misconfiguration, not an absent opt-in,
// so that case fails loudly instead of skipping silently.
func resolveSmokeAdapterCommand(t *testing.T) string {
	t.Helper()
	cmd := os.Getenv(smokeMeshCommandEnv)
	if cmd == "" {
		t.Skipf("set %s to a real adapter command (claude, codex, or a configured recipe wrapper) on PATH, "+
			"with its credentials already present in this process's environment, to run this smoke test", smokeMeshCommandEnv)
	}
	if _, err := exec.LookPath(cmd); err != nil {
		t.Fatalf("%s=%q is not resolvable on PATH: %v", smokeMeshCommandEnv, cmd, err)
	}
	return cmd
}

// resolveSmokeAFMBinary locates a pre-built afm binary for the adapter's own `afm agent` CLI calls — same
// idiom as sdk/integration_test.go's resolveAfmBinary, default path adjusted for this file's own depth
// (pkg/orchestrator is two directories below the repo root, where `make build` drops bin/afm).
func resolveSmokeAFMBinary(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv(smokeAFMBinEnv); bin != "" {
		if _, err := os.Stat(bin); err != nil {
			t.Fatalf("%s=%q: %v", smokeAFMBinEnv, bin, err)
		}
		return bin
	}
	bin := filepath.Join("..", "..", "bin", "afm")
	if abs, err := filepath.Abs(bin); err == nil {
		bin = abs
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("no afm binary at %s (run `make build` at the repo root, or set %s): %v", bin, smokeAFMBinEnv, err)
	}
	return bin
}

// smokeMeshActor mirrors cmd/afm/run_agent_mesh.go's meshActor exactly: every mesh call this adapter makes
// is attributed to the TOKEN-RESOLVED participant, never a value a request body could forge.
func smokeMeshActor(participant string) state.Actor {
	return state.Actor{Kind: "agent", ID: participant}
}

func smokeParticipantIDs(ids []string) []agentcomm.ParticipantID {
	out := make([]agentcomm.ParticipantID, len(ids))
	for i, id := range ids {
		out[i] = agentcomm.ParticipantID(id)
	}
	return out
}

func smokeMeshPage(p orchestrator.MessagePage) server.MeshMessagePage {
	return server.MeshMessagePage{Messages: p.Messages, NextCursor: p.NextCursor}
}

// smokeMeshActions adapts *orchestrator.Orchestrator to server.AgentMeshActions for this test's own real
// *server.Server — see this file's package doc comment for why it cannot simply import cmd/afm's own
// production adapter.
type smokeMeshActions struct{ orch *orchestrator.Orchestrator }

func (a smokeMeshActions) ResolveToken(token string) (string, bool) {
	p, ok := a.orch.ResolveMeshToken(token)
	return string(p), ok
}

func (a smokeMeshActions) ListParticipants(ctx context.Context, actor string) ([]agentcomm.Participant, error) {
	page, err := a.orch.ListParticipants(ctx, orchestrator.ListParticipantsRequest{Actor: smokeMeshActor(actor)})
	if err != nil {
		return nil, err
	}
	return page.Participants, nil
}

func (a smokeMeshActions) Send(ctx context.Context, requestID, actor string, req server.MeshSendRequest) (server.MeshSendReceipt, error) {
	var inReplyTo *agentcomm.MessageID
	if req.InReplyTo != "" {
		id := agentcomm.MessageID(req.InReplyTo)
		inReplyTo = &id
	}
	kind := agentcomm.MessageKind(req.Kind)
	if kind == "" {
		kind = agentcomm.MessageKindMessage
	}
	receipt, err := a.orch.Send(ctx, orchestrator.SendRequest{
		OperationID: requestID, ThreadID: agentcomm.ThreadID(req.ThreadID), Sender: agentcomm.ParticipantID(actor),
		Recipients: smokeParticipantIDs(req.Recipients), Kind: kind, InReplyTo: inReplyTo,
		Body: req.Body, ArtifactRefs: req.ArtifactRefs, Actor: smokeMeshActor(actor),
	})
	if err != nil {
		return server.MeshSendReceipt{}, err
	}
	return server.MeshSendReceipt{MessageID: string(receipt.MessageID), Seq: receipt.Seq}, nil
}

func (a smokeMeshActions) Inbox(ctx context.Context, actor string, cursor uint64, limit int) (server.MeshMessagePage, error) {
	page, err := a.orch.ReadInbox(ctx, orchestrator.ReadInboxRequest{Participant: agentcomm.ParticipantID(actor), Cursor: cursor, Limit: limit, Actor: smokeMeshActor(actor)})
	if err != nil {
		return server.MeshMessagePage{}, err
	}
	return smokeMeshPage(page), nil
}

func (a smokeMeshActions) Thread(ctx context.Context, actor, threadID string, cursor uint64, limit int) (server.MeshMessagePage, error) {
	page, err := a.orch.ReadThread(ctx, orchestrator.ReadThreadRequest{
		ThreadID: agentcomm.ThreadID(threadID), Reader: agentcomm.ParticipantID(actor), Cursor: cursor, Limit: limit, Actor: smokeMeshActor(actor),
	})
	if err != nil {
		return server.MeshMessagePage{}, err
	}
	return smokeMeshPage(page), nil
}

func (a smokeMeshActions) Ack(ctx context.Context, requestID, actor, messageID string) error {
	return a.orch.Acknowledge(ctx, orchestrator.AcknowledgeRequest{
		OperationID: requestID, MessageID: agentcomm.MessageID(messageID), Participant: agentcomm.ParticipantID(actor), Actor: smokeMeshActor(actor),
	})
}

func (a smokeMeshActions) Wait(ctx context.Context, requestID, actor string, req server.MeshWaitRequest) (server.MeshWaitReceipt, error) {
	var deadline *time.Time
	if req.DeadlineUnix != nil {
		d := time.Unix(*req.DeadlineUnix, 0).UTC()
		deadline = &d
	}
	receipt, err := a.orch.Wait(ctx, orchestrator.WaitRequest{
		OperationID: requestID, Waiter: agentcomm.ParticipantID(actor), Condition: req.Condition,
		Deadline: deadline, ContinuationNote: req.ContinuationNote, Actor: smokeMeshActor(actor),
	})
	if err != nil {
		return server.MeshWaitReceipt{}, err
	}
	return server.MeshWaitReceipt{WaitID: string(receipt.WaitID), Status: string(receipt.Status)}, nil
}

func (a smokeMeshActions) CancelWait(ctx context.Context, requestID, actor, waitID, reason string) error {
	return a.orch.CancelWait(ctx, orchestrator.CancelWaitRequest{
		OperationID: requestID, WaitID: agentcomm.WaitID(waitID), Reason: reason, Actor: smokeMeshActor(actor),
	})
}

func (a smokeMeshActions) PublishBoard(ctx context.Context, requestID, actor string, req server.MeshPublishRequest) (agentcomm.BoardEntry, error) {
	return a.orch.Publish(ctx, orchestrator.PublishRequest{
		OperationID: requestID, Author: agentcomm.ParticipantID(actor), Key: req.Key, Status: req.Status,
		ArtifactRefs: req.ArtifactRefs, ExpectedVersion: req.ExpectedVersion, AuthorizedBy: smokeParticipantIDs(req.AuthorizedBy),
		Actor: smokeMeshActor(actor),
	})
}

func (a smokeMeshActions) Board(ctx context.Context, key string) ([]agentcomm.BoardEntry, error) {
	page, err := a.orch.ReadBoard(ctx, orchestrator.ReadBoardRequest{Key: key})
	if err != nil {
		return nil, err
	}
	return page.Entries, nil
}

var _ server.AgentMeshActions = smokeMeshActions{}

// smokeReplyBody is the fixed, greppable marker the test's simulated peer reply carries — asserted against
// both the <inbox> block built for the resumed invocation and (structurally) against what a real adapter
// would be expected to notice.
const smokeReplyBody = "AFM_MESH_SMOKE_REPLY: the peer's answer has arrived."

// smokeFirstTurnPromptTemplate is handed to the FIRST real invocation. %s is substituted three times with
// the SAME thread id — a plain %s template (not positional %[1]s) to keep the quoting around the embedded
// shell commands simple to read.
const smokeFirstTurnPromptTemplate = `You are a participant in afm's Agent Mesh, reachable over a shared thread.
A CLI named "afm" is on your PATH; every subcommand below prints one line of JSON to stdout.

Thread id: %s

Do the following, in order, using plain shell commands only (do not write or edit any files):

1. Run ` + "`afm agent list`" + ` and confirm its JSON output includes a participant with id "user".
2. Run ` + "`afm agent send --thread %s --to user --kind message --body \"hello from the smoke adapter\"`" + `.
3. Poll for an incoming message: run ` + "`afm agent inbox`" + ` once per second, up to 10 times, until its
   JSON output contains a message whose body starts with "mid-turn ping". Once you see it, stop polling.
4. Run ` + "`afm agent wait --condition addressed_message --thread-id %s`" + `. Whatever its JSON "status"
   field says ("yield" or "ready"), stop immediately afterward: make no further tool calls and end your turn.`

// smokeResumePromptPreamble precedes the real <inbox> block (built by
// orchestrator.AgentCommInboxBlockForTest, the SAME production helper a live resumed stage gets) in the
// SECOND invocation's prompt.
const smokeResumePromptPreamble = `You are being resumed after your earlier "afm agent wait" yielded and a reply
arrived. Read the <inbox> block below — it is the same inbox injection a real resumed afm participant
receives, not a summary — confirm you can see the reply's body, and then end your turn. Make no tool calls.`

// TestAdapterSmoke_SendReadReplyYieldResume is Agent Mesh Phase 9, T9.1's gated real-adapter smoke test. See
// this file's package doc comment for the full scenario; skips immediately (AFM_MESH_SMOKE_COMMAND unset) or
// after resolving its two env-gated prerequisites below, in every environment that has not explicitly opted
// in — including CI.
func TestAdapterSmoke_SendReadReplyYieldResume(t *testing.T) {
	adapterCmd := resolveSmokeAdapterCommand(t)
	afmBin := resolveSmokeAFMBinary(t)

	runDir := t.TempDir()
	rootDir := t.TempDir()
	wrapperDir := t.TempDir()
	stageDir := filepath.Join(runDir, "stage-dir")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatalf("mkdir stage dir: %v", err)
	}
	// The adapter's own subprocess resolves "afm" via PATH (WrapperDir is prepended to it by the real
	// executor path, pkg/executor/executor.go) — a symlink is enough; this test never writes through it.
	if err := os.Symlink(afmBin, filepath.Join(wrapperDir, "afm")); err != nil {
		t.Fatalf("symlink afm into wrapper dir: %v", err)
	}

	const stageID = "smoke"
	const stageParticipant = agentcomm.ParticipantID("stage:" + stageID)
	const humanParticipant = agentcomm.ParticipantID("user")

	stages := []flow.Stage{{ID: stageID, Name: "Smoke Adapter", Script: "true"}}
	store, err := state.Open(runDir, []string{stageID})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, RootDir: rootDir, Stages: stages, Store: store,
		Config: config.Default(), Prompts: orchestrator.DefaultPrompts(),
	})

	srv := server.New(server.Config{RunDir: runDir, Store: store, UIBus: bus.NewUIBus(), AgentMesh: smokeMeshActions{orch: orch}})
	if srv == nil || srv.Handler() == nil {
		t.Fatal("server.New/Handler returned nil")
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	orch.SetDashboardURL(ts.URL)

	ctx := context.Background()

	// Only the human-equivalent "user" surface can open a thread (routeAgentMesh exposes no such route to an
	// agent) — this mirrors how a real human or the dashboard's own Feed/Team UI would start the
	// conversation the adapter under test then participates in. Opening it also registers stageParticipant
	// in the mesh roster as a side effect (OpenThread's own doc comment), which `afm agent list` needs to see.
	thread, err := orch.OpenThread(ctx, orchestrator.OpenThreadRequest{
		OperationID: "op-smoke-open", Title: "mesh smoke", Scope: agentcomm.ScopeStage,
		Members: []agentcomm.ParticipantID{humanParticipant, stageParticipant}, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	threadID := string(thread.ID)

	meshClient := orchestrator.MeshClientForTest(orch, stageParticipant)
	if meshClient == nil {
		t.Fatal("MeshClientForTest returned nil (DashboardURL not set?)")
	}

	cfg := executor.Config{
		Command: adapterCmd, WrapperDir: wrapperDir, Dir: rootDir, StageDir: stageDir,
		RunID: "mesh-smoke", StageID: stageID, MeshClient: meshClient, IdleTimeout: 2 * time.Minute,
	}
	e := executor.New(cfg)

	logFile1 := filepath.Join(runDir, "smoke-invocation-1.log")
	prompt1 := fmt.Sprintf(smokeFirstTurnPromptTemplate, threadID, threadID, threadID)

	runCtx1, cancel1 := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel1()

	done := make(chan error, 1)
	go func() { done <- e.RunAgent(runCtx1, adapterCmd, stageID, prompt1, logFile1) }()

	// Step 3's "mid-turn" message: delivered to the running adapter's inbox a few seconds after it starts —
	// while the FIRST invocation's process is still alive — exactly the scenario `afm agent inbox` exists
	// for, as opposed to a message that was already pending before the process even started.
	time.Sleep(3 * time.Second)
	if _, err := orch.Send(ctx, orchestrator.SendRequest{
		OperationID: "op-smoke-midturn", ThreadID: thread.ID, Sender: humanParticipant,
		Recipients: []agentcomm.ParticipantID{stageParticipant}, Kind: agentcomm.MessageKindMessage,
		Body: "mid-turn ping: please acknowledge via `afm agent inbox`.", Actor: state.Actor{Kind: "user"},
	}); err != nil {
		t.Fatalf("mid-turn Send: %v", err)
	}

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("first invocation of %q failed: %v (see %s)", adapterCmd, runErr, logFile1)
		}
	case <-time.After(5*time.Minute + 10*time.Second):
		t.Fatal("first invocation did not exit in time")
	}

	// Step 2's durability requirement: the message the adapter sent via `afm agent send` must be in
	// events.jsonl itself, not merely in an in-memory projection — a crash right after this point must not
	// lose it. Same raw-file assertion idiom as retry_test.go/verify_h2_planning_fail_test.go.
	rawEvents, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if !strings.Contains(string(rawEvents), "mesh_message_sent") {
		t.Fatalf("events.jsonl has no durable mesh_message_sent record from the adapter's `afm agent send`:\n%s", rawEvents)
	}

	// Step 4's reply is sent only now, AFTER the first invocation has already exited — proving whatever wait
	// it registered actually yielded (status="yield") rather than blocking the live process for it.
	if _, err := orch.Send(ctx, orchestrator.SendRequest{
		OperationID: "op-smoke-reply", ThreadID: thread.ID, Sender: humanParticipant,
		Recipients: []agentcomm.ParticipantID{stageParticipant}, Kind: agentcomm.MessageKindMessage,
		Body: smokeReplyBody, Actor: state.Actor{Kind: "user"},
	}); err != nil {
		t.Fatalf("reply Send: %v", err)
	}

	inboxBlock, onDelivered := orchestrator.AgentCommInboxBlockForTest(orch, stageParticipant)
	if !strings.Contains(inboxBlock, smokeReplyBody) {
		t.Fatalf("resume <inbox> block does not contain the reply body: %s", inboxBlock)
	}

	// "One new invocation, no held lease while waiting": exactly one more RunAgent call carries the reply —
	// mirroring the shape of the production resumeAfterPeerReply re-invocation (agentcomm_resume.go) this
	// smoke test deliberately does not bootstrap (dev-plan.md's Execution rule forbids a nested `afm run`
	// from an implementation stage) — never a second parallel process and never a retry loop.
	logFile2 := filepath.Join(runDir, "smoke-invocation-2.log")
	resumePrompt := smokeResumePromptPreamble + "\n\n" + inboxBlock
	cfg2 := cfg
	cfg2.MeshClient = orchestrator.MeshClientForTest(orch, stageParticipant)
	e2 := executor.New(cfg2)

	runCtx2, cancel2 := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel2()
	if err := e2.RunAgent(runCtx2, adapterCmd, stageID, resumePrompt, logFile2); err != nil {
		t.Fatalf("resumed (second) invocation of %q failed: %v (see %s)", adapterCmd, err, logFile2)
	}
	onDelivered()
}
