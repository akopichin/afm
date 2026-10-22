package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 6, T6.7: the bound Side agent becomes a participant of the SAME Service.

func registerTestSideAgent(t *testing.T, o *Orchestrator) agentcomm.Participant {
	t.Helper()
	p, err := o.RegisterSideAgent(context.Background(), RegisterSideAgentRequest{
		DisplayName: "claude", Actor: state.Actor{Kind: "system"},
	})
	if err != nil {
		t.Fatalf("RegisterSideAgent: %v", err)
	}
	o.SetSideAgentAvailable(true)
	return p
}

func TestSideAgentMesh_AvailabilityGatesRosterAndSends(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	if _, err := o.RegisterSideAgent(context.Background(), RegisterSideAgentRequest{DisplayName: "codex", Actor: state.Actor{Kind: "system"}}); err != nil {
		t.Fatal(err)
	}
	checkReachable := func(want bool) {
		t.Helper()
		page, err := o.ListParticipants(context.Background(), ListParticipantsRequest{})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page.Participants {
			if p.ID == o.SideAgentParticipantID() {
				if p.Reachable != want {
					t.Fatalf("side reachable = %v, want %v", p.Reachable, want)
				}
				return
			}
		}
		t.Fatal("historical side participant disappeared")
	}
	send := func(op string) error {
		_, err := o.SendFromAgent(context.Background(), SendRequest{OperationID: op, Sender: "user", Recipients: []agentcomm.ParticipantID{o.SideAgentParticipantID()}, Kind: agentcomm.MessageKindMessage, Body: "hello", Actor: state.Actor{Kind: "user"}})
		return err
	}
	checkReachable(false)
	if err := send("side-off-1"); err == nil {
		t.Fatal("side accepted send before bridge startup")
	}
	o.SetSideAgentAvailable(true)
	checkReachable(true)
	if err := send("side-on"); err != nil {
		t.Fatal(err)
	}
	o.SetSideAgentAvailable(false)
	checkReachable(false)
	if err := send("side-off-2"); err == nil {
		t.Fatal("side accepted send after shutdown")
	}
	resumed := New(o.opts) // same durable participant, capability not restarted
	page, err := resumed.ListParticipants(context.Background(), ListParticipantsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range page.Participants {
		if p.ID == resumed.SideAgentParticipantID() {
			if p.Reachable {
				t.Fatal("resumed side without bridge is reachable")
			}
			return
		}
	}
	t.Fatal("resumed run lost historical side participant")
}

func TestSideAgentMesh_FailureMarkerReplaysNoticeAndExplicitReplyWins(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	side := registerTestSideAgent(t, o).ID
	ctx := context.Background()
	first, err := o.SendFromAgent(ctx, SendRequest{OperationID: "side-failure-one", Sender: "user", Recipients: []agentcomm.ParticipantID{side}, Kind: agentcomm.MessageKindMessage, Body: "one", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.SideAgentMeshResponseFailed(first.MessageID, "conversation unavailable"); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot().Assistants.Mesh.Receipts[first.MessageID][side]; got != agentcomm.DeliveryUndeliverable {
		t.Fatalf("receipt = %s", got)
	}
	path := filepath.Join(o.opts.RunDir, "notices.jsonl")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	} // crash after marker, before notice append
	o.RepublishSideAgentMeshFailure(first.MessageID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"type":"side_agent_mesh_failure"`) || !strings.Contains(string(data), string(first.MessageID)) {
		t.Fatalf("missing keyed replay notice: %s", data)
	}

	second, err := o.SendFromAgent(ctx, SendRequest{OperationID: "side-failure-two", Sender: "user", Recipients: []agentcomm.ParticipantID{side}, Kind: agentcomm.MessageKindMessage, Body: "two", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.SendFromAgent(ctx, SendRequest{OperationID: "side-explicit-before-failure", Sender: side, Kind: agentcomm.MessageKindReply, InReplyTo: &second.MessageID, Body: "answered", Actor: state.Actor{Kind: "agent", ID: string(side)}}); err != nil {
		t.Fatal(err)
	}
	if err := o.SideAgentMeshResponseFailed(second.MessageID, "could not answer"); !errors.Is(err, ErrSideAgentExplicitReply) {
		t.Fatalf("failure after explicit reply = %v", err)
	}
	if got := store.Snapshot().Assistants.Mesh.Receipts[second.MessageID][side]; got == agentcomm.DeliveryUndeliverable {
		t.Fatal("explicit answer was marked undeliverable")
	}
}

// TestSideAgent_HasParticipantIdentity — the bound side agent appears in ListParticipants as
// "side:<runID>", kind side_agent, with its configured executor as DisplayName and a display alias; a
// message it originates is attributed to that id, never to "user" and never to a stage agent id.
func TestSideAgent_HasParticipantIdentity(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()

	p := registerTestSideAgent(t, o)
	wantID := agentcomm.ParticipantID("side:run1") // newAssistantsTestOrchestrator fixes RunID to "run1"
	if p.ID != wantID {
		t.Fatalf("participant id = %q, want %q", p.ID, wantID)
	}
	if p.Kind != agentcomm.KindSideAgent {
		t.Fatalf("kind = %q, want %q", p.Kind, agentcomm.KindSideAgent)
	}
	if p.Alias == "" {
		t.Fatal("expected a non-empty display alias (so @-mentions can address it)")
	}
	if p.DisplayName != "claude" {
		t.Fatalf("display name = %q, want the configured executor %q", p.DisplayName, "claude")
	}
	if !p.Reachable {
		t.Fatal("a freshly registered side agent must be reachable")
	}

	page, err := o.ListParticipants(ctx, ListParticipantsRequest{Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatalf("ListParticipants: %v", err)
	}
	found := false
	for _, got := range page.Participants {
		if got.ID != wantID {
			continue
		}
		found = true
		if got.Kind != agentcomm.KindSideAgent {
			t.Fatalf("roster entry kind = %q, want %q", got.Kind, agentcomm.KindSideAgent)
		}
	}
	if !found {
		t.Fatalf("ListParticipants did not include %q", wantID)
	}

	// A message the side agent originates is attributed to ITS id — never "user", never a stage agent.
	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-side-thread", Title: "side chat", Scope: agentcomm.ScopeRun,
		Members: []agentcomm.ParticipantID{meshUserParticipantID, wantID}, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	receipt, err := o.Send(ctx, SendRequest{
		OperationID: "op-side-send", ThreadID: thread.ID, Sender: wantID,
		Recipients: []agentcomm.ParticipantID{meshUserParticipantID}, Kind: agentcomm.MessageKindMessage,
		Body: "hello from the side agent", Actor: state.Actor{Kind: "system"},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	rs := store.Snapshot()
	msg, ok := rs.Assistants.Mesh.Messages[receipt.MessageID]
	if !ok {
		t.Fatalf("message %s not found after Send", receipt.MessageID)
	}
	if msg.Sender != wantID {
		t.Fatalf("message sender = %q, want %q", msg.Sender, wantID)
	}
	if msg.Sender == meshUserParticipantID {
		t.Fatal("a side-agent-originated message must never be attributed to \"user\"")
	}
	if msg.Sender == meshStageParticipantID(assistantsTestStageID) {
		t.Fatal("a side-agent-originated message must never be attributed to a stage agent")
	}
}

// TestSideAgent_CanAddressAndBeAddressed — the side agent can Send to a stage agent, a helper and a run
// agent, and can receive a reply; delivery obeys the same receipts and gates as any other participant (no
// privileged inbox).
func TestSideAgent_CanAddressAndBeAddressed(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	stubPassingHelper(o)

	sideID := registerTestSideAgent(t, o).ID
	stageID := meshStageParticipantID(assistantsTestStageID)
	// Run agent ниже стартует с тем же блокирующим stub-ом, что и helper. Cleanup выполняется в обратном
	// порядке, поэтому этот (зарегистрированный раньше) дожидается run agent уже после того, как cleanup
	// newRunningHelperForMesh отпустит stub, и до удаления TempDir.
	var runAgentTask assistants.TaskID
	t.Cleanup(func() {
		if runAgentTask != "" {
			waitForRunAgentTurn(t, store, runAgentTask, 2*time.Second)
		}
	})
	taskID := newRunningHelperForMesh(t, o, store, "op-side-helper")
	helperID := meshHelperParticipantID(taskID)

	msg := "hi run agent"
	runRes, err := o.AttachAndSend(ctx, AttachAndSendRequest{
		OperationID: "op-side-run-agent", ProfileID: "qa", Scope: assistants.ScopeRun,
		Title: "My QA", Instruction: "watch for regressions", FirstMessage: &msg, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AttachAndSend (run agent): %v", err)
	}
	runAgentID := runRes.ParticipantID
	runAgentTask = runRes.TaskID

	for _, to := range []agentcomm.ParticipantID{stageID, helperID, runAgentID} {
		thread, err := o.OpenThread(ctx, OpenThreadRequest{
			OperationID: "op-side-open-" + string(to), Title: "side <-> " + string(to), Scope: agentcomm.ScopeRun,
			Members: []agentcomm.ParticipantID{sideID, to}, Actor: state.Actor{Kind: "system"},
		})
		if err != nil {
			t.Fatalf("OpenThread(side,%s): %v", to, err)
		}

		sent, err := o.Send(ctx, SendRequest{
			OperationID: "op-side-send-" + string(to), ThreadID: thread.ID, Sender: sideID,
			Recipients: []agentcomm.ParticipantID{to}, Kind: agentcomm.MessageKindMessage, Body: "ping",
			Actor: state.Actor{Kind: "system"},
		})
		if err != nil {
			t.Fatalf("Send(side -> %s): %v", to, err)
		}

		replyID := sent.MessageID
		if _, err := o.Send(ctx, SendRequest{
			OperationID: "op-side-reply-" + string(to), ThreadID: thread.ID, Sender: to,
			Recipients: []agentcomm.ParticipantID{sideID}, Kind: agentcomm.MessageKindReply, InReplyTo: &replyID,
			Body: "pong", Actor: state.Actor{Kind: "system"},
		}); err != nil {
			t.Fatalf("Send(%s -> side, reply): %v", to, err)
		}

		// Delivered into the side agent's inbox exactly like any other participant — no privileged bypass.
		inbox, err := o.ReadInbox(ctx, ReadInboxRequest{Participant: sideID, Actor: state.Actor{Kind: "system"}})
		if err != nil {
			t.Fatalf("ReadInbox(side): %v", err)
		}
		foundReply := false
		for _, m := range inbox.Messages {
			if m.ThreadID == thread.ID && m.Sender == to {
				foundReply = true
			}
		}
		if !foundReply {
			t.Fatalf("side agent inbox missing the reply from %s; inbox=%+v thread=%s sent=%s", to, inbox, thread.ID, sent.MessageID)
		}
	}
}

// TestSideAgent_DisabledCapabilityLeavesNoParticipant — with the side agent off (RegisterSideAgent never
// called — the real gate lives in cmd/afm's sideAgentEnabled check around the one production call site), no
// "side:" participant exists and nothing in the mesh references it.
func TestSideAgent_DisabledCapabilityLeavesNoParticipant(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	page, err := o.ListParticipants(ctx, ListParticipantsRequest{Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatalf("ListParticipants: %v", err)
	}
	for _, p := range page.Participants {
		if p.Kind == agentcomm.KindSideAgent {
			t.Fatalf("unexpected side_agent participant %+v with the side agent never registered", p)
		}
	}

	rs := store.Snapshot()
	wantID := o.SideAgentParticipantID()
	if _, ok := rs.Assistants.Mesh.Participants[wantID]; ok {
		t.Fatalf("mesh durably references %q although RegisterSideAgent was never called", wantID)
	}
}

func TestSideAgent_MeshClientLazyAndScoped(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	if o.SideAgentMeshClient() != nil {
		t.Fatal("client minted before dashboard URL")
	}
	o.opts.DashboardURL = "http://127.0.0.1:9876"
	client := o.SideAgentMeshClient()
	if client == nil || client.ParticipantID != string(o.SideAgentParticipantID()) {
		t.Fatalf("wrong side client: %+v", client)
	}
	_, token, revoke := client.Mint()
	if got, ok := o.ResolveMeshToken(token); !ok || got != o.SideAgentParticipantID() {
		t.Fatal("side token not scoped to side participant")
	}
	revoke()
	if _, ok := o.ResolveMeshToken(token); ok {
		t.Fatal("side token was not revoked")
	}
}

// The bound Assistant is the one participant that always answers to @assistant (the user-chosen system name).
// These tests pin the stored alias of a fresh registration, the reservation against every other participant
// holding the same @name, replay of runs journaled with the older @side alias, and the one-line CLI warning.

// liveAliases maps every roster participant to the @name it answers to now.
func liveAliases(o *Orchestrator) map[agentcomm.ParticipantID]string {
	aliases := map[agentcomm.ParticipantID]string{}
	for _, p := range o.liveRoster(o.opts.Store.Snapshot()) {
		aliases[p.ID] = p.Alias
	}
	return aliases
}

// mustResolveHandle resolves an @handle against the live roster, the way `afm agent send --to` does.
func mustResolveHandle(t *testing.T, o *Orchestrator, handle string) agentcomm.ParticipantID {
	t.Helper()
	id, err := agentcomm.ResolveAlias(handle, o.liveRoster(o.opts.Store.Snapshot()))
	if err != nil {
		t.Fatalf("ResolveAlias(%q): %v", handle, err)
	}
	return id
}

// addNamedRunAgent adds a run agent without a first message and returns its participant id.
func addNamedRunAgent(t *testing.T, o *Orchestrator, opID, title string) agentcomm.ParticipantID {
	t.Helper()
	res, err := o.AddRunAgent(context.Background(), AddRunAgentRequest{
		OperationID: opID, ProfileID: "qa", Title: title, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AddRunAgent(%q): %v", title, err)
	}
	return res.ParticipantID
}

// seedLegacySideAgent journals the bound agent exactly as a run created before the rename stored it.
func seedLegacySideAgent(t *testing.T, o *Orchestrator, store *state.Store) {
	t.Helper()
	eff, err := meshParticipantAddedEffect(agentcomm.Participant{
		ID: o.SideAgentParticipantID(), Kind: agentcomm.KindSideAgent, Scope: agentcomm.ScopeRun,
		Alias: "side", DisplayName: "claude", Reachable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	op := state.Operation{
		ID: sideAgentRegisterOperationID("run1"), Kind: "mesh_register_side_agent",
		Actor: state.Actor{Kind: "system"}, Effects: []state.Effect{eff},
	}
	if _, err := store.ApplyRunOperation(op, func(state.OperationResult) {}); err != nil {
		t.Fatal(err)
	}
}

func TestSideAgent_FreshRegistrationAnswersToAssistant(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	p := registerTestSideAgent(t, o)
	if p.ID != "side:run1" || p.Alias != "assistant" {
		t.Fatalf("registered %q @%s, want side:run1 @assistant", p.ID, p.Alias)
	}
	if stored := store.Snapshot().Assistants.Mesh.Participants[p.ID]; stored == nil || stored.Alias != "assistant" {
		t.Fatalf("stored descriptor = %+v, want alias assistant", stored)
	}
	if got := mustResolveHandle(t, o, "@assistant"); got != p.ID {
		t.Fatalf("@assistant = %s, want %s", got, p.ID)
	}
	if _, err := agentcomm.ResolveAlias("@side", o.liveRoster(store.Snapshot())); !errors.Is(err, agentcomm.ErrAliasNotFound) {
		t.Fatalf("@side err = %v, want ErrAliasNotFound: a new run has no @side", err)
	}
}

// Whoever else holds @assistant — before or after the bound agent registers — answers to the first free
// @assistant-N instead, and the effective names survive a replay of the journal.
func TestSideAgent_RunAgentNamedAssistantMakesRoom(t *testing.T) {
	for _, order := range []string{"run agent after", "run agent before"} {
		t.Run(order, func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			var runAgent agentcomm.ParticipantID
			if order == "run agent before" {
				runAgent = addNamedRunAgent(t, o, "op-assistant", "Assistant")
			}
			side := registerTestSideAgent(t, o).ID
			if order == "run agent after" {
				runAgent = addNamedRunAgent(t, o, "op-assistant", "Assistant")
			}

			check := func(o *Orchestrator) {
				t.Helper()
				aliases := liveAliases(o)
				if aliases[side] != "assistant" || aliases[runAgent] != "assistant-2" {
					t.Fatalf("aliases = %q / %q, want the bound agent on assistant and the run agent on assistant-2", aliases[side], aliases[runAgent])
				}
				if got := mustResolveHandle(t, o, "@assistant"); got != side {
					t.Fatalf("@assistant = %s, want %s", got, side)
				}
				if got := mustResolveHandle(t, o, "@assistant-2"); got != runAgent {
					t.Fatalf("@assistant-2 = %s, want %s", got, runAgent)
				}
			}
			check(o)

			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, _ := reopenAssistantsTestOrchestrator(t, o)
			check(reopened)
		})
	}
}

// A stage's @name is its id: "assistant" and "Assistant" both lose it to the bound agent (names are
// case-insensitive), and a displaced stage skips @assistant-N names somebody already holds.
func TestSideAgent_StageNamedAssistantMakesRoom(t *testing.T) {
	for _, stageID := range []string{"assistant", "Assistant"} {
		t.Run(stageID, func(t *testing.T) {
			o, _ := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: stageID, Command: "claude"})
			side := registerTestSideAgent(t, o).ID
			stage := meshStageParticipantID(stageID)
			aliases := liveAliases(o)
			if aliases[side] != "assistant" || aliases[stage] != "assistant-2" {
				t.Fatalf("aliases = %q / %q, want assistant / assistant-2", aliases[side], aliases[stage])
			}
			if got := mustResolveHandle(t, o, "@assistant"); got != side {
				t.Fatalf("@assistant = %s, want %s", got, side)
			}
			if got := mustResolveHandle(t, o, "@assistant-2"); got != stage {
				t.Fatalf("@assistant-2 = %s, want %s", got, stage)
			}
		})
	}

	t.Run("assistant-2 is taken", func(t *testing.T) {
		o, _ := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "assistant", Command: "claude"})
		taken := addNamedRunAgent(t, o, "op-taken", "Assistant") // the stage holds @assistant: stored as assistant-2
		side := registerTestSideAgent(t, o).ID
		aliases := liveAliases(o)
		if aliases[side] != "assistant" || aliases[taken] != "assistant-2" || aliases["stage:assistant"] != "assistant-3" {
			t.Fatalf("aliases = %v, want the stage on assistant-3 behind the run agent on assistant-2", aliases)
		}
	})

	t.Run("two displaced participants get distinct names", func(t *testing.T) {
		o, store := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "assistant", Command: "claude"})
		// A run agent journaled with the plain name: possible when it was added before the stage existed.
		const early = agentcomm.ParticipantID("run:early")
		eff, err := meshParticipantAddedEffect(agentcomm.Participant{
			ID: early, Kind: agentcomm.KindRunAgent, Scope: agentcomm.ScopeRun, Alias: "assistant", DisplayName: "Early", Reachable: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.ApplyRunOperation(state.Operation{ID: "op-early", Kind: "seed", Actor: state.Actor{Kind: "system"}, Effects: []state.Effect{eff}}, func(state.OperationResult) {}); err != nil {
			t.Fatal(err)
		}
		side := registerTestSideAgent(t, o).ID
		aliases := liveAliases(o)
		if aliases[side] != "assistant" || aliases["stage:assistant"] == aliases[early] ||
			!strings.HasPrefix(aliases["stage:assistant"], "assistant-") || !strings.HasPrefix(aliases[early], "assistant-") {
			t.Fatalf("aliases = %v, want two distinct assistant-N names behind the bound agent", aliases)
		}
		_ = mustResolveHandle(t, o, "@"+aliases["stage:assistant"])
		_ = mustResolveHandle(t, o, "@"+aliases[early])
	})
}

// A run journaled with the @side alias reopens untouched: the saved descriptor is returned as it is, even when
// the configured command differs now, and @assistant does not name it.
func TestSideAgent_LegacySideAliasReplaysWithoutConflict(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	seedLegacySideAgent(t, o, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, rstore := reopenAssistantsTestOrchestrator(t, o)

	for _, display := range []string{"claude", "codex"} {
		p, err := reopened.RegisterSideAgent(context.Background(), RegisterSideAgentRequest{DisplayName: display, Actor: state.Actor{Kind: "system"}})
		if err != nil {
			t.Fatalf("RegisterSideAgent(%q) over a legacy descriptor: %v", display, err)
		}
		if p.Alias != "side" || p.DisplayName != "claude" {
			t.Fatalf("RegisterSideAgent(%q) = @%s / %q, want the saved @side / claude", display, p.Alias, p.DisplayName)
		}
	}
	sides := 0
	for _, p := range rstore.Snapshot().Assistants.Mesh.Participants {
		if p.Kind == agentcomm.KindSideAgent {
			sides++
		}
	}
	if sides != 1 {
		t.Fatalf("bound participants = %d, want 1", sides)
	}
	if got := mustResolveHandle(t, reopened, "@side"); got != reopened.SideAgentParticipantID() {
		t.Fatalf("@side = %s", got)
	}
	if _, err := agentcomm.ResolveAlias("@assistant", reopened.liveRoster(rstore.Snapshot())); !errors.Is(err, agentcomm.ErrAliasNotFound) {
		t.Fatalf("@assistant err = %v, want ErrAliasNotFound for a run with the saved @side", err)
	}
}

// Without a bound agent nobody reserves @assistant: a stage of that name keeps it, silently.
func TestSideAgent_StageKeepsAssistantWithoutBoundAgent(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "assistant", Command: "claude"})
	var warnings bytes.Buffer
	o.opts.AliasWarning = &warnings
	if got := mustResolveHandle(t, o, "@assistant"); got != "stage:assistant" {
		t.Fatalf("@assistant = %s, want stage:assistant", got)
	}
	if warnings.Len() != 0 {
		t.Fatalf("unexpected warning: %q", warnings.String())
	}
}

// A retry of a send that committed to the stage called @assistant is the same request even after the bound
// Assistant registered and moved that stage to @assistant-2: the receipt is replayed, not a conflict.
func TestSideAgent_RetryAfterAliasDisplacementReplaysReceipt(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "assistant", Command: "claude"})
	req := SendRequest{
		OperationID: "op-retry-displaced", Sender: "user", Recipients: []agentcomm.ParticipantID{"assistant"},
		Kind: agentcomm.MessageKindMessage, Body: "hello", Actor: state.Actor{Kind: "user"},
	}
	first, err := o.SendFromAgent(context.Background(), req)
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	registerTestSideAgent(t, o) // takes @assistant; the stage moves to @assistant-2

	retry, err := o.SendFromAgent(context.Background(), req)
	if err != nil {
		t.Fatalf("retry after the stage lost @assistant: %v", err)
	}
	if retry != first {
		t.Fatalf("retry receipt = %+v, want the first send's %+v", retry, first)
	}
}

// A retry is judged by the names the caller wrote, not by who they resolve to now: the same request under
// the same id is a replay even when its alias is the live one (@assistant-2 — the stage moved there).
func TestSideAgent_RetryWithDisplacedAliasNameReplaysReceipt(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "assistant", Command: "claude"})
	registerTestSideAgent(t, o) // takes @assistant; the stage answers to @assistant-2
	req := SendRequest{
		OperationID: "op-live-alias", Sender: "user", Recipients: []agentcomm.ParticipantID{"@assistant-2"},
		Kind: agentcomm.MessageKindMessage, Body: "hello", Actor: state.Actor{Kind: "user"},
	}
	first, err := o.SendFromAgent(context.Background(), req)
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	retry, err := o.SendFromAgent(context.Background(), req)
	if err != nil {
		t.Fatalf("retry of the same request: %v", err)
	}
	if retry != first {
		t.Fatalf("retry receipt = %+v, want the first send's %+v", retry, first)
	}
}

// Another name under the same id is another request, even when it now names the same participant's old
// neighbour: @assistant is the bound Assistant, the committed send went to the stage written as @assistant-2.
func TestSideAgent_RetryWithOtherNameConflicts(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "assistant", Command: "claude"})
	registerTestSideAgent(t, o)
	req := SendRequest{
		OperationID: "op-other-name", Sender: "user", Recipients: []agentcomm.ParticipantID{"@assistant-2"},
		Kind: agentcomm.MessageKindMessage, Body: "hello", Actor: state.Actor{Kind: "user"},
	}
	if _, err := o.SendFromAgent(context.Background(), req); err != nil {
		t.Fatalf("first send: %v", err)
	}
	req.Recipients = []agentcomm.ParticipantID{"@assistant"}
	if _, err := o.SendFromAgent(context.Background(), req); !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("retry with another recipient name: err = %v, want ErrOperationIDConflict", err)
	}
}

// A send written with a participant id is retried by an alias only while the alias still names that
// participant: once the Assistant took @assistant, it names someone else, so it is another request.
func TestSideAgent_RetryByAliasThatChangedHandsConflicts(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "assistant", Command: "claude"})
	req := SendRequest{
		OperationID: "op-by-id", Sender: "user", Recipients: []agentcomm.ParticipantID{"stage:assistant"},
		Kind: agentcomm.MessageKindMessage, Body: "hello", Actor: state.Actor{Kind: "user"},
	}
	first, err := o.SendFromAgent(context.Background(), req)
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	req.Recipients = []agentcomm.ParticipantID{"@assistant"}
	if retry, err := o.SendFromAgent(context.Background(), req); err != nil || retry != first {
		t.Fatalf("retry by the alias that still names the stage = %+v, %v; want %+v", retry, err, first)
	}
	registerTestSideAgent(t, o) // @assistant is now the bound Assistant
	if _, err := o.SendFromAgent(context.Background(), req); !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("retry by the alias that changed hands: err = %v, want ErrOperationIDConflict", err)
	}
}

// A reply without recipients goes back to the sender it answers; its retry — also without recipients — is a
// replay, and one that now names recipients is another request.
func TestSideAgent_RetryOfReplyWithoutRecipients(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "assistant", Command: "claude"})
	question, err := o.SendFromAgent(context.Background(), SendRequest{
		OperationID: "op-question", Sender: "user", Recipients: []agentcomm.ParticipantID{"assistant"},
		Kind: agentcomm.MessageKindMessage, Body: "question", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("question: %v", err)
	}
	reply := SendRequest{
		OperationID: "op-answer", Sender: "stage:assistant", InReplyTo: &question.MessageID,
		Kind: agentcomm.MessageKindReply, Body: "answer", Actor: state.Actor{Kind: "agent", ID: "stage:assistant"},
	}
	first, err := o.SendFromAgent(context.Background(), reply)
	if err != nil {
		t.Fatalf("reply: %v", err)
	}
	retry, err := o.SendFromAgent(context.Background(), reply)
	if err != nil || retry != first {
		t.Fatalf("retry = %+v, %v; want the first reply's receipt %+v", retry, err, first)
	}
	reply.Recipients = []agentcomm.ParticipantID{"stage:assistant"}
	if _, err := o.SendFromAgent(context.Background(), reply); !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("retry with other recipients: err = %v, want ErrOperationIDConflict", err)
	}
}

// The registration that takes @assistant away from another participant says so, once, on AliasWarning.
func TestSideAgent_RegistrationWarnsAboutReassignedName(t *testing.T) {
	t.Run("stage", func(t *testing.T) {
		o, _ := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "assistant", Command: "claude"})
		var warnings bytes.Buffer
		o.opts.AliasWarning = &warnings
		registerTestSideAgent(t, o)
		want := `warning: @assistant is reserved for the dashboard Assistant; stage "assistant" (stage:assistant) now answers to @assistant-2` + "\n"
		if warnings.String() != want {
			t.Fatalf("warning = %q, want %q", warnings.String(), want)
		}
		// Registering again (every resume) changes nothing and says nothing.
		warnings.Reset()
		registerTestSideAgent(t, o)
		if warnings.Len() != 0 {
			t.Fatalf("repeated registration warned: %q", warnings.String())
		}
	})

	t.Run("run agent", func(t *testing.T) {
		o, _ := newAssistantsTestOrchestrator(t, true)
		id := addNamedRunAgent(t, o, "op-assistant", "Assistant")
		var warnings bytes.Buffer
		o.opts.AliasWarning = &warnings
		registerTestSideAgent(t, o)
		want := fmt.Sprintf(`warning: @assistant is reserved for the dashboard Assistant; run agent "Assistant" (%s) now answers to @assistant-2`+"\n", id)
		if warnings.String() != want {
			t.Fatalf("warning = %q, want %q", warnings.String(), want)
		}
	})

	t.Run("no conflict", func(t *testing.T) {
		o, _ := newAssistantsTestOrchestrator(t, true)
		addNamedRunAgent(t, o, "op-qa", "QA")
		var warnings bytes.Buffer
		o.opts.AliasWarning = &warnings
		registerTestSideAgent(t, o)
		if warnings.Len() != 0 {
			t.Fatalf("unexpected warning: %q", warnings.String())
		}
	})

	t.Run("reopening a registered run", func(t *testing.T) {
		o, store := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "assistant", Command: "claude"})
		registerTestSideAgent(t, o)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, _ := reopenAssistantsTestOrchestrator(t, o)
		var warnings bytes.Buffer
		reopened.opts.AliasWarning = &warnings
		registerTestSideAgent(t, reopened)
		if warnings.Len() != 0 {
			t.Fatalf("a resume warned: %q", warnings.String())
		}
	})
}

// A run resumed with the bound agent saved as @assistant but its runtime off: the saved participant still owns
// the name, so a send to it is refused as unavailable and never reaches the stage that was moved aside.
func TestSideAgent_ReservationHoldsWhileUnavailable(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "assistant", Command: "claude"})
	side := registerTestSideAgent(t, o).ID
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, rstore := reopenAssistantsTestOrchestrator(t, o) // SetSideAgentAvailable is not called

	page, err := reopened.ListParticipants(context.Background(), ListParticipantsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range page.Participants {
		switch p.ID {
		case side:
			if p.Alias != "assistant" || p.Reachable {
				t.Fatalf("bound participant = @%s reachable=%v, want an unreachable @assistant", p.Alias, p.Reachable)
			}
		case "stage:assistant":
			if p.Alias != "assistant-2" {
				t.Fatalf("stage alias = %q, want assistant-2", p.Alias)
			}
		default:
		}
	}
	if got := mustResolveHandle(t, reopened, "@assistant"); got != side {
		t.Fatalf("@assistant = %s, want %s", got, side)
	}

	_, err = reopened.SendFromAgent(context.Background(), SendRequest{
		OperationID: "op-to-assistant", Sender: "user", Recipients: []agentcomm.ParticipantID{"assistant"},
		Kind: agentcomm.MessageKindMessage, Body: "hello", Actor: state.Actor{Kind: "user"},
	})
	requireRejected(t, err, assistants.CodeInvalidStatus)
	for _, msg := range rstore.Snapshot().Assistants.Mesh.Messages {
		if slices.Contains(msg.Recipients, "stage:assistant") {
			t.Fatalf("message %s reached the displaced stage", msg.ID)
		}
	}
}

// Agent-facing texts name a displaced participant by the @name it answers to now.
func TestSideAgent_DisplacedNameInAgentFacingTexts(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "assistant", Command: "claude"})
	registerTestSideAgent(t, o)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	activateStage(t, o, "assistant")

	// <your_requests>: impl asked the displaced stage a question.
	if _, err := o.SendFromAgent(ctx, SendRequest{
		OperationID: "op-ask", Sender: "stage:impl", Recipients: []agentcomm.ParticipantID{"stage:assistant"},
		Kind: agentcomm.MessageKindRequest, Body: "ready?", Actor: state.Actor{Kind: "agent", ID: "stage:impl"},
	}); err != nil {
		t.Fatalf("SendFromAgent: %v", err)
	}
	block := o.agentCommInboxBlock(flow.Stage{ID: assistantsTestStageID})
	if !strings.Contains(block, " to assistant-2: ") || strings.Contains(block, " to assistant: ") {
		t.Fatalf("<your_requests> must name the displaced stage @assistant-2:\n%s", block)
	}

	// A post that mentions the displaced stage after it finished is refused, naming it by its current @name.
	finishStage(t, o, "assistant")
	_, err := o.PostDiscussionMessage(ctx, PostMessageRequest{
		OperationID: "op-post", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorHuman,
		Round: assistants.RoundFreeform, Body: "hello", Mentions: []agentcomm.ParticipantID{"stage:assistant"},
		Actor: state.Actor{Kind: "user"},
	})
	requireRejected(t, err, assistants.CodeInvalidStatus)
	if !strings.Contains(err.Error(), "@assistant-2 cannot read this post") {
		t.Fatalf("err = %v, want the displaced stage named @assistant-2", err)
	}
}

// effectiveAlias (state only, used inside commit checks) must agree with the full roster on every @name.
func TestSideAgent_EffectiveAliasAgreesWithLiveRoster(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "assistant", Command: "claude"})
	addNamedRunAgent(t, o, "op-qa", "QA")
	registerTestSideAgent(t, o)
	addNamedRunAgent(t, o, "op-assistant", "Assistant")
	rs := store.Snapshot()
	for id, want := range liveAliases(o) {
		if got := effectiveAlias(rs, id); got != want {
			t.Errorf("effectiveAlias(%s) = %q, want %q", id, got, want)
		}
	}
}

// Every text a person (or an agent acting for one) can read about the bound agent being unavailable calls it
// the Assistant. The unavailability reason must not contain "mesh": the dashboard classifies that word apart.
func TestSideAgent_AvailabilityTextsNameAssistant(t *testing.T) {
	requireAssistantText := func(t *testing.T, what, text string) {
		t.Helper()
		lower := strings.ToLower(text)
		if !strings.Contains(text, "Assistant") || strings.Contains(lower, "side agent") || strings.Contains(lower, "mesh") {
			t.Fatalf("%s = %q, want a text that says Assistant, not side agent or mesh", what, text)
		}
	}

	t.Run("roster reason and rejected send", func(t *testing.T) {
		o, _ := newAssistantsTestOrchestrator(t, true)
		side := registerTestSideAgent(t, o).ID
		o.SetSideAgentAvailable(false)
		page, err := o.ListParticipants(context.Background(), ListParticipantsRequest{})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page.Participants {
			if p.ID == side {
				requireAssistantText(t, "UnreachableReason", p.UnreachableReason)
			}
		}
		_, err = o.SendFromAgent(context.Background(), SendRequest{
			OperationID: "op-unavailable", Sender: "user", Recipients: []agentcomm.ParticipantID{side},
			Kind: agentcomm.MessageKindMessage, Body: "hello", Actor: state.Actor{Kind: "user"},
		})
		requireRejected(t, err, assistants.CodeInvalidStatus)
		requireAssistantText(t, "send rejection", err.Error())
	})

	t.Run("explicit reply reason", func(t *testing.T) {
		requireAssistantText(t, "ErrSideAgentExplicitReply", ErrSideAgentExplicitReply.Error())
	})

	t.Run("registration error", func(t *testing.T) {
		o, _ := newAssistantsTestOrchestrator(t, true)
		o.opts.RunID = ""
		_, err := o.RegisterSideAgent(context.Background(), RegisterSideAgentRequest{DisplayName: "claude", Actor: state.Actor{Kind: "system"}})
		if err == nil {
			t.Fatal("registration without a run id succeeded")
		}
		requireAssistantText(t, "registration error", err.Error())
	})

	t.Run("receipt and ended wait of a message the Assistant never started", func(t *testing.T) {
		o, store := newAssistantsTestOrchestrator(t, true)
		side := registerTestSideAgent(t, o).ID
		ctx := context.Background()
		thread, err := o.OpenThread(ctx, OpenThreadRequest{
			OperationID: "op-thread", Scope: agentcomm.ScopeRun,
			Members: []agentcomm.ParticipantID{"stage:impl", side}, Actor: state.Actor{Kind: "system"},
		})
		if err != nil {
			t.Fatal(err)
		}
		source, err := o.Send(ctx, SendRequest{
			OperationID: "op-source", ThreadID: thread.ID, Sender: "stage:impl", Recipients: []agentcomm.ParticipantID{side},
			Kind: agentcomm.MessageKindRequest, Body: "question", Actor: state.Actor{Kind: "agent", ID: "stage:impl"},
		})
		if err != nil {
			t.Fatal(err)
		}
		wait, err := o.Wait(ctx, WaitRequest{
			OperationID: "op-wait", Waiter: "stage:impl",
			Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: source.MessageID},
			Actor:     state.Actor{Kind: "agent", ID: "stage:impl"},
		})
		if err != nil || wait.Status != agentcomm.WaitPending {
			t.Fatalf("pending reply wait = %+v, %v", wait, err)
		}
		if err := o.SideAgentMeshMarkUnprocessed(source.MessageID); err != nil {
			t.Fatal(err)
		}
		requireAssistantText(t, "ended wait reason", store.Snapshot().Assistants.Mesh.Waits[wait.WaitID].Reason)
	})
}
