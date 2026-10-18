package state

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
)

// ---- T1.1: participants and threads ----

func meshParticipantAddedControl(id, kind, scope string) *Control {
	payload, _ := json.Marshal(MeshParticipantAddedPayload{Kind: kind, Scope: scope})
	return &Control{
		Kind: kindMeshParticipantAdded, SchemaVersion: ControlSchemaVersion,
		Actor: Actor{Kind: "system"}, Target: Target{ParticipantID: id}, Payload: payload,
	}
}

func meshThreadOpenedControl(id string, members []string) *Control {
	payload, _ := json.Marshal(MeshThreadOpenedPayload{Scope: string(agentcomm.ScopeStage), Members: members})
	return &Control{
		Kind: kindMeshThreadOpened, SchemaVersion: ControlSchemaVersion,
		Actor: Actor{Kind: "system"}, Target: Target{ThreadID: id}, Payload: payload,
	}
}

func TestMesh_ParticipantAddedProjects(t *testing.T) {
	as := newAssistantsState()
	c := meshParticipantAddedControl("user", string(agentcomm.KindUser), string(agentcomm.ScopeRun))
	if err := dispatchControlKind(&as, c, false); err != nil {
		t.Fatalf("dispatchControlKind: %v", err)
	}
	p, ok := as.Mesh.Participants[agentcomm.ParticipantID("user")]
	if !ok {
		t.Fatal("participant not projected")
	}
	if p.Kind != agentcomm.KindUser || p.Scope != agentcomm.ScopeRun || !p.Reachable {
		t.Fatalf("participant = %+v, want kind=user scope=run reachable=true", p)
	}
	if len(as.Mesh.ParticipantOrder) != 1 || as.Mesh.ParticipantOrder[0] != "user" {
		t.Fatalf("ParticipantOrder = %v", as.Mesh.ParticipantOrder)
	}
}

func TestMesh_DuplicateParticipantRejected(t *testing.T) {
	as := newAssistantsState()
	c := meshParticipantAddedControl("user", string(agentcomm.KindUser), string(agentcomm.ScopeRun))
	if err := dispatchControlKind(&as, c, false); err != nil {
		t.Fatalf("first add: %v", err)
	}
	c2 := meshParticipantAddedControl("user", string(agentcomm.KindHelper), string(agentcomm.ScopeStage))
	if err := dispatchControlKind(&as, c2, false); err == nil {
		t.Fatal("duplicate participant id: want error, got nil")
	}
	// First participant's fields must be untouched by the rejected second attempt.
	if as.Mesh.Participants["user"].Kind != agentcomm.KindUser {
		t.Fatalf("original participant was mutated by rejected duplicate: %+v", as.Mesh.Participants["user"])
	}
}

func TestMesh_ThreadOpenedProjects(t *testing.T) {
	as := newAssistantsState()
	mustApply(t, &as, meshParticipantAddedControl("a", string(agentcomm.KindStageAgent), string(agentcomm.ScopeStage)))
	mustApply(t, &as, meshParticipantAddedControl("b", string(agentcomm.KindHelper), string(agentcomm.ScopeStage)))

	c := meshThreadOpenedControl("th-1", []string{"a", "b"})
	if err := dispatchControlKind(&as, c, false); err != nil {
		t.Fatalf("dispatchControlKind: %v", err)
	}
	th, ok := as.Mesh.Threads[agentcomm.ThreadID("th-1")]
	if !ok {
		t.Fatal("thread not projected")
	}
	if th.Status != agentcomm.ThreadOpen || len(th.Members) != 2 {
		t.Fatalf("thread = %+v, want open with 2 members", th)
	}
	if len(as.Mesh.ThreadOrder) != 1 || as.Mesh.ThreadOrder[0] != "th-1" {
		t.Fatalf("ThreadOrder = %v", as.Mesh.ThreadOrder)
	}
}

func TestMesh_ThreadOpenedUnknownMemberRejected(t *testing.T) {
	as := newAssistantsState()
	mustApply(t, &as, meshParticipantAddedControl("a", string(agentcomm.KindStageAgent), string(agentcomm.ScopeStage)))
	c := meshThreadOpenedControl("th-1", []string{"a", "ghost"})
	if err := dispatchControlKind(&as, c, false); err == nil {
		t.Fatal("thread with unknown member: want error, got nil")
	}
	if _, ok := as.Mesh.Threads["th-1"]; ok {
		t.Fatal("thread must not be projected when a member is unknown")
	}
}

func TestMesh_ThreadClosedTwiceRejected(t *testing.T) {
	as := newAssistantsState()
	mustApply(t, &as, meshParticipantAddedControl("a", string(agentcomm.KindStageAgent), string(agentcomm.ScopeStage)))
	mustApply(t, &as, meshThreadOpenedControl("th-1", []string{"a"}))

	closeCtl := &Control{Kind: kindMeshThreadClosed, SchemaVersion: ControlSchemaVersion, Target: Target{ThreadID: "th-1"}}
	if err := dispatchControlKind(&as, closeCtl, false); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if as.Mesh.Threads["th-1"].Status != agentcomm.ThreadClosed {
		t.Fatal("thread not closed")
	}
	if err := dispatchControlKind(&as, closeCtl, false); err == nil {
		t.Fatal("closing an already-closed thread: want error, got nil")
	}
}

func TestMesh_UnknownKindFailsClosed(t *testing.T) {
	rs := NewRunState([]string{"a"})
	log := `{"seq":1,"time":"2026-01-01T10:00:00Z","kind":"mesh_something_future","schema_version":1,"actor":{"kind":"system"}}
`
	res := parseEventLog([]byte(log), rs)
	if !errors.Is(res.unsupported, ErrUnsupportedRecord) {
		t.Fatalf("unsupported = %v, want ErrUnsupportedRecord", res.unsupported)
	}
	if res.corrupted {
		t.Fatal("unknown mesh kind must fail closed, not be treated as corrupted/quarantined")
	}
}

func TestMesh_DryRunMutatesNothing(t *testing.T) {
	as := newAssistantsState()
	c := meshParticipantAddedControl("user", string(agentcomm.KindUser), string(agentcomm.ScopeRun))
	if err := dispatchControlKind(&as, c, true); err != nil {
		t.Fatalf("dryRun apply: %v", err)
	}
	if len(as.Mesh.Participants) != 0 || len(as.Mesh.ParticipantOrder) != 0 {
		t.Fatalf("dryRun mutated state: %+v", as.Mesh)
	}

	mustApply(t, &as, meshParticipantAddedControl("a", string(agentcomm.KindStageAgent), string(agentcomm.ScopeStage)))
	mustApply(t, &as, meshParticipantAddedControl("b", string(agentcomm.KindHelper), string(agentcomm.ScopeStage)))
	threadCtl := meshThreadOpenedControl("th-1", []string{"a", "b"})
	if err := dispatchControlKind(&as, threadCtl, true); err != nil {
		t.Fatalf("dryRun thread: %v", err)
	}
	if len(as.Mesh.Threads) != 0 {
		t.Fatalf("dryRun mutated threads: %+v", as.Mesh.Threads)
	}
}

func TestMesh_CloneIsDeep(t *testing.T) {
	as := newAssistantsState()
	mustApply(t, &as, meshParticipantAddedControl("a", string(agentcomm.KindStageAgent), string(agentcomm.ScopeStage)))
	mustApply(t, &as, meshThreadOpenedControl("th-1", []string{"a"}))

	clone := as.Clone()
	// Mutating the clone's top-level maps/slices must never affect the original (Store.Snapshot's contract —
	// see AssistantsState.Clone's own doc comment: one level deep, same convention as Tasks/Holds).
	clone.Mesh.Participants["b"] = &agentcomm.Participant{ID: "b", Kind: agentcomm.KindHelper, Scope: agentcomm.ScopeStage, Reachable: true}
	clone.Mesh.ParticipantOrder = append(clone.Mesh.ParticipantOrder, "b")
	clone.Mesh.Threads["th-2"] = &agentcomm.Thread{ID: "th-2", Members: []agentcomm.ParticipantID{"a"}, Status: agentcomm.ThreadOpen}
	clone.Mesh.ThreadOrder = append(clone.Mesh.ThreadOrder, "th-2")

	if len(as.Mesh.Participants) != 1 {
		t.Fatalf("original Participants mutated via clone: %+v", as.Mesh.Participants)
	}
	if len(as.Mesh.ParticipantOrder) != 1 {
		t.Fatalf("original ParticipantOrder mutated via clone: %v", as.Mesh.ParticipantOrder)
	}
	if len(as.Mesh.Threads) != 1 {
		t.Fatalf("original Threads mutated via clone: %+v", as.Mesh.Threads)
	}
	if len(as.Mesh.ThreadOrder) != 1 {
		t.Fatalf("original ThreadOrder mutated via clone: %v", as.Mesh.ThreadOrder)
	}

	// deepClone additionally proves safe for IN-PLACE mutation (e.g. thread close) — a batched operation that
	// closes a thread in the scratch copy must not close it in the original if the batch is later discarded.
	scratch := as.deepClone()
	scratch.Mesh.Threads["th-1"].Status = agentcomm.ThreadClosed
	if as.Mesh.Threads["th-1"].Status != agentcomm.ThreadOpen {
		t.Fatalf("original thread status mutated via deepClone: %+v", as.Mesh.Threads["th-1"])
	}
}

// mustApply applies c to as, failing the test immediately on error.
func mustApply(t *testing.T, as *AssistantsState, c *Control) {
	t.Helper()
	if err := dispatchControlKind(as, c, false); err != nil {
		t.Fatalf("dispatchControlKind(%s): %v", c.Kind, err)
	}
}
