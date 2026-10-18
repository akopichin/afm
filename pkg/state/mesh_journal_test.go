package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
)

// ---- T1.5: journal-level sweep ----
//
// Note on scope: the plan's "idempotency after opResultCache eviction" bullet is deliberately SKIPPED here.
// That bullet is about the bundled-Effect/ApplyRunOperation path (operation.go's opResultCache) — as of this
// phase nothing outside pkg/state/pkg/agentcomm references any mesh kind (verified by grepping
// pkg/orchestrator, pkg/server, pkg/web, cmd for agentcomm./kindMesh/MeshState/applyMesh — zero hits), so
// there is no caller yet that bundles a mesh effect into an operation_committed record. A test against that
// path would be exercising code that does not exist.

func mustUnmarshal(t *testing.T, raw json.RawMessage, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
}

// ---- round-trip decode of every new mesh kind ----

type meshKindFixture struct {
	kind    string
	target  Target
	payload json.RawMessage
	check   func(t *testing.T, payload json.RawMessage)
}

func TestMesh_RoundTripDecodeEveryKind(t *testing.T) {
	inReplyTo := "m1"
	fixtures := []meshKindFixture{
		{
			kind:    kindMeshParticipantAdded,
			target:  Target{ParticipantID: "a"},
			payload: mustMarshal(t, MeshParticipantAddedPayload{Kind: string(agentcomm.KindUser), Scope: string(agentcomm.ScopeRun), Alias: "qa"}),
			check: func(t *testing.T, raw json.RawMessage) {
				var p MeshParticipantAddedPayload
				mustUnmarshal(t, raw, &p)
				if p.Kind != string(agentcomm.KindUser) || p.Scope != string(agentcomm.ScopeRun) || p.Alias != "qa" {
					t.Fatalf("payload = %+v", p)
				}
			},
		},
		{
			kind:    kindMeshThreadOpened,
			target:  Target{ThreadID: "th-1"},
			payload: mustMarshal(t, MeshThreadOpenedPayload{Scope: string(agentcomm.ScopeStage), Members: []string{"a", "b"}}),
			check: func(t *testing.T, raw json.RawMessage) {
				var p MeshThreadOpenedPayload
				mustUnmarshal(t, raw, &p)
				if len(p.Members) != 2 || p.Members[0] != "a" || p.Members[1] != "b" {
					t.Fatalf("payload = %+v", p)
				}
			},
		},
		{
			kind:   kindMeshThreadClosed,
			target: Target{ThreadID: "th-1"},
		},
		{
			kind:    kindMeshMessageSent,
			target:  Target{MessageID: "m2", ThreadID: "th-1"},
			payload: mustMarshal(t, MeshMessageSentPayload{Sender: "a", Recipients: []string{"b"}, Kind: string(agentcomm.MessageKindReply), InReplyTo: &inReplyTo, Body: "re"}),
			check: func(t *testing.T, raw json.RawMessage) {
				var p MeshMessageSentPayload
				mustUnmarshal(t, raw, &p)
				if p.InReplyTo == nil || *p.InReplyTo != "m1" || p.Body != "re" || p.Sender != "a" {
					t.Fatalf("payload = %+v", p)
				}
			},
		},
		{
			kind:    kindMeshMessageDelivered,
			target:  Target{MessageID: "m1", ParticipantID: "b"},
			payload: mustMarshal(t, MeshMessageReceiptPayload{Reason: "net blip"}),
			check: func(t *testing.T, raw json.RawMessage) {
				var p MeshMessageReceiptPayload
				mustUnmarshal(t, raw, &p)
				if p.Reason != "net blip" {
					t.Fatalf("payload = %+v", p)
				}
			},
		},
		{
			kind:    kindMeshMessageAcked,
			target:  Target{MessageID: "m1", ParticipantID: "b"},
			payload: mustMarshal(t, MeshMessageReceiptPayload{Reason: "seen"}),
			check: func(t *testing.T, raw json.RawMessage) {
				var p MeshMessageReceiptPayload
				mustUnmarshal(t, raw, &p)
				if p.Reason != "seen" {
					t.Fatalf("payload = %+v", p)
				}
			},
		},
		{
			kind:    kindMeshMessageUndeliverable,
			target:  Target{MessageID: "m1", ParticipantID: "b"},
			payload: mustMarshal(t, MeshMessageReceiptPayload{Reason: "gone"}),
			check: func(t *testing.T, raw json.RawMessage) {
				var p MeshMessageReceiptPayload
				mustUnmarshal(t, raw, &p)
				if p.Reason != "gone" {
					t.Fatalf("payload = %+v", p)
				}
			},
		},
		{
			kind:   kindMeshWaitRegistered,
			target: Target{WaitID: "w1", TaskID: "t1"},
			payload: mustMarshal(t, MeshWaitRegisteredPayload{
				Waiter: "b", Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnTaskResult, TaskID: "t1"}, Generation: 3,
			}),
			check: func(t *testing.T, raw json.RawMessage) {
				var p MeshWaitRegisteredPayload
				mustUnmarshal(t, raw, &p)
				if p.Generation != 3 || p.Condition.TaskID != "t1" || p.Waiter != "b" {
					t.Fatalf("payload = %+v", p)
				}
			},
		},
		{
			kind:    kindMeshWaitResolved,
			target:  Target{WaitID: "w1"},
			payload: mustMarshal(t, MeshWaitResolvedPayload{Status: string(agentcomm.WaitReady), Reason: "done"}),
			check: func(t *testing.T, raw json.RawMessage) {
				var p MeshWaitResolvedPayload
				mustUnmarshal(t, raw, &p)
				if p.Status != string(agentcomm.WaitReady) || p.Reason != "done" {
					t.Fatalf("payload = %+v", p)
				}
			},
		},
		{
			kind:   kindMeshBoardPublished,
			target: Target{BoardKey: "k", ParticipantID: "a"},
			payload: mustMarshal(t, MeshBoardPublishedPayload{
				Status: "final", ExpectedVersion: 2, ArtifactRefs: []string{"artifacts/x"}, AuthorizedBy: []string{"b"},
			}),
			check: func(t *testing.T, raw json.RawMessage) {
				var p MeshBoardPublishedPayload
				mustUnmarshal(t, raw, &p)
				if p.ExpectedVersion != 2 || len(p.ArtifactRefs) != 1 || p.ArtifactRefs[0] != "artifacts/x" || len(p.AuthorizedBy) != 1 || p.AuthorizedBy[0] != "b" {
					t.Fatalf("payload = %+v", p)
				}
			},
		},
	}

	fixedTime := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	for _, f := range fixtures {
		t.Run(f.kind, func(t *testing.T) {
			c := &Control{
				Seq: 1, Time: fixedTime, Kind: f.kind, SchemaVersion: ControlSchemaVersion,
				Actor: Actor{Kind: "system"}, Target: f.target, Payload: f.payload,
			}
			line, err := json.Marshal(c)
			if err != nil {
				t.Fatalf("marshal control: %v", err)
			}
			dl, unsupported, err := decodeLine(line)
			if err != nil || unsupported {
				t.Fatalf("decodeLine(%s): err=%v unsupported=%v", f.kind, err, unsupported)
			}
			if dl.control == nil || dl.transition != nil {
				t.Fatalf("decodeLine(%s) = %+v, want a control record only", f.kind, dl)
			}
			if dl.control.Kind != f.kind {
				t.Fatalf("decoded kind = %q, want %q", dl.control.Kind, f.kind)
			}
			if dl.control.Target != f.target {
				t.Fatalf("decoded target = %+v, want %+v", dl.control.Target, f.target)
			}
			if f.check != nil {
				f.check(t, dl.control.Payload)
			}
		})
	}
}

// ---- unknown kind fails closed ----

func TestMesh_BogusKindFailsClosed(t *testing.T) {
	rs := NewRunState([]string{"a"})
	log := `{"seq":1,"time":"2026-01-01T10:00:00Z","kind":"mesh_bogus_kind","schema_version":1,"actor":{"kind":"system"}}
`
	res := parseEventLog([]byte(log), rs)
	if !errors.Is(res.unsupported, ErrUnsupportedRecord) {
		t.Fatalf("unsupported = %v, want ErrUnsupportedRecord", res.unsupported)
	}
	if res.corrupted {
		t.Fatal("an unknown mesh_* kind must fail closed, not be treated as corrupted/quarantined")
	}
}

// ---- seq shared and monotonic across a mixed mesh + non-mesh journal ----

// meshJournalFixture builds a small journal mixing mesh and task-tree control records, each explicitly
// stamped with a distinct Seq/Time, as raw JSON lines — the fixture both TestMesh_JournalSeqMonotonic... and
// TestMesh_ReplayEquivalence replay.
func meshJournalFixture(t *testing.T) []byte {
	t.Helper()
	controls := []*Control{
		meshParticipantAddedControl("a", string(agentcomm.KindStageAgent), string(agentcomm.ScopeStage)),
		meshParticipantAddedControl("b", string(agentcomm.KindHelper), string(agentcomm.ScopeStage)),
		meshThreadOpenedControl("th-1", []string{"a", "b"}),
		meshMessageSentControl(0, "m1", "th-1", "a", []string{"b"}, string(agentcomm.MessageKindMessage), nil, "hi"),
		meshMessageReceiptControl(kindMeshMessageDelivered, "m1", "b"),
		meshWaitRegisteredControl("w1", "b", agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "th-1"}, "", "", 0),
		meshBoardPublishedControl("k", "a", "draft", 0, nil, nil),
		taskCreatedControl("op-1", "t1", "stage-a", nil),
	}

	var buf bytes.Buffer
	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	for i, c := range controls {
		c.Seq = uint64(i + 1)
		c.Time = base.Add(time.Duration(i) * time.Second)
		data, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal control %d (%s): %v", i, c.Kind, err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func TestMesh_JournalSeqMonotonicAcrossMixedKinds(t *testing.T) {
	data := meshJournalFixture(t)
	rs := NewRunState([]string{"stage-a"})
	res := parseEventLog(data, rs)
	if res.corrupted || res.unsupported != nil {
		t.Fatalf("mixed mesh+task journal must parse cleanly: corrupted=%v unsupported=%v", res.corrupted, res.unsupported)
	}
	if res.lastSeq != 8 {
		t.Fatalf("lastSeq = %d, want 8 (one shared counter across mesh and non-mesh control records)", res.lastSeq)
	}
	for i := 1; i < len(res.controls); i++ {
		if res.controls[i].Seq <= res.controls[i-1].Seq {
			t.Fatalf("controls[%d].Seq=%d is not strictly greater than controls[%d].Seq=%d", i, res.controls[i].Seq, i-1, res.controls[i-1].Seq)
		}
	}
}

// ---- restart/replay equivalence ----

func TestMesh_ReplayEquivalence(t *testing.T) {
	data := meshJournalFixture(t)

	rs1 := NewRunState([]string{"stage-a"})
	if res := parseEventLog(data, rs1); res.corrupted || res.unsupported != nil {
		t.Fatalf("first replay failed: corrupted=%v unsupported=%v", res.corrupted, res.unsupported)
	}
	rs2 := NewRunState([]string{"stage-a"})
	if res := parseEventLog(data, rs2); res.corrupted || res.unsupported != nil {
		t.Fatalf("second replay failed: corrupted=%v unsupported=%v", res.corrupted, res.unsupported)
	}

	if !reflect.DeepEqual(rs1.Assistants.Mesh, rs2.Assistants.Mesh) {
		t.Fatalf("Mesh state differs across two independent replays of the identical journal:\nfirst:  %+v\nsecond: %+v", rs1.Assistants.Mesh, rs2.Assistants.Mesh)
	}
}

// ---- registry sweep: every kindMesh* constant is registered AND dispatchable ----

// extractMeshKindConstants derives the list of mesh control kinds directly from record.go's source text
// (rather than hand-copying the kind strings into this test), so a newly added kindMeshXxx constant is
// automatically picked up and the table below is forced to account for it.
func extractMeshKindConstants(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("record.go")
	if err != nil {
		t.Fatalf("read record.go: %v", err)
	}
	re := regexp.MustCompile(`kindMesh\w*\s*=\s*"(mesh_\w+)"`)
	matches := re.FindAllStringSubmatch(string(src), -1)
	if len(matches) == 0 {
		t.Fatal("no kindMesh* constants found in record.go -- this test's extraction regexp has drifted from the source")
	}
	kinds := make([]string, 0, len(matches))
	for _, m := range matches {
		kinds = append(kinds, m[1])
	}
	return kinds
}

// seedMeshMessage is the common fixture TestMesh_AllKindsRegisteredAndDispatchable's three receipt-kind
// cases (delivered/acked/undeliverable) all need: a thread with exactly one sender and one recipient, and
// one message already sent from the former to the latter.
func seedMeshMessage(t *testing.T, as *AssistantsState, msgID, threadID, sender, recipient string) {
	t.Helper()
	mustApply(t, as, meshParticipantAddedControl(sender, string(agentcomm.KindHelper), string(agentcomm.ScopeStage)))
	mustApply(t, as, meshParticipantAddedControl(recipient, string(agentcomm.KindHelper), string(agentcomm.ScopeStage)))
	mustApply(t, as, meshThreadOpenedControl(threadID, []string{sender, recipient}))
	mustApply(t, as, meshMessageSentControl(1, msgID, threadID, sender, []string{recipient}, string(agentcomm.MessageKindMessage), nil, "hi"))
}

// TestMesh_AllKindsRegisteredAndDispatchable proves, for every kindMesh* constant found in record.go: (1) it
// is present in controlKinds (decodeLine would accept it), and (2) dispatchControlKind actually routes it to
// a working handler that accepts a minimal valid record for that kind — not just "does not say unknown
// kind", but a genuine successful apply. If a new mesh kind is added to record.go without a builder entry
// below, this test fails loudly naming the missing kind, instead of silently skipping it.
func TestMesh_AllKindsRegisteredAndDispatchable(t *testing.T) {
	builders := map[string]func(t *testing.T, as *AssistantsState) *Control{
		kindMeshParticipantAdded: func(t *testing.T, as *AssistantsState) *Control {
			return meshParticipantAddedControl("reg-participant", string(agentcomm.KindHelper), string(agentcomm.ScopeStage))
		},
		kindMeshThreadOpened: func(t *testing.T, as *AssistantsState) *Control {
			mustApply(t, as, meshParticipantAddedControl("reg-member", string(agentcomm.KindHelper), string(agentcomm.ScopeStage)))
			return meshThreadOpenedControl("reg-thread", []string{"reg-member"})
		},
		kindMeshThreadClosed: func(t *testing.T, as *AssistantsState) *Control {
			mustApply(t, as, meshParticipantAddedControl("reg-member2", string(agentcomm.KindHelper), string(agentcomm.ScopeStage)))
			mustApply(t, as, meshThreadOpenedControl("reg-thread2", []string{"reg-member2"}))
			return &Control{Kind: kindMeshThreadClosed, SchemaVersion: ControlSchemaVersion, Target: Target{ThreadID: "reg-thread2"}}
		},
		kindMeshMessageSent: func(t *testing.T, as *AssistantsState) *Control {
			mustApply(t, as, meshParticipantAddedControl("reg-sender", string(agentcomm.KindHelper), string(agentcomm.ScopeStage)))
			mustApply(t, as, meshParticipantAddedControl("reg-recipient", string(agentcomm.KindHelper), string(agentcomm.ScopeStage)))
			mustApply(t, as, meshThreadOpenedControl("reg-thread3", []string{"reg-sender", "reg-recipient"}))
			return meshMessageSentControl(1, "reg-msg", "reg-thread3", "reg-sender", []string{"reg-recipient"}, string(agentcomm.MessageKindMessage), nil, "hi")
		},
		kindMeshMessageDelivered: func(t *testing.T, as *AssistantsState) *Control {
			seedMeshMessage(t, as, "reg-msg-d", "reg-thread-d", "reg-sender-d", "reg-recipient-d")
			return meshMessageReceiptControl(kindMeshMessageDelivered, "reg-msg-d", "reg-recipient-d")
		},
		kindMeshMessageAcked: func(t *testing.T, as *AssistantsState) *Control {
			seedMeshMessage(t, as, "reg-msg-a", "reg-thread-a", "reg-sender-a", "reg-recipient-a")
			return meshMessageReceiptControl(kindMeshMessageAcked, "reg-msg-a", "reg-recipient-a")
		},
		kindMeshMessageUndeliverable: func(t *testing.T, as *AssistantsState) *Control {
			seedMeshMessage(t, as, "reg-msg-u", "reg-thread-u", "reg-sender-u", "reg-recipient-u")
			return meshMessageReceiptControl(kindMeshMessageUndeliverable, "reg-msg-u", "reg-recipient-u")
		},
		kindMeshWaitRegistered: func(t *testing.T, as *AssistantsState) *Control {
			cond := agentcomm.WaitCondition{Kind: agentcomm.WaitOnTaskResult, TaskID: "none"}
			return meshWaitRegisteredControl("reg-wait", "reg-waiter", cond, "", "", 0)
		},
		kindMeshWaitResolved: func(t *testing.T, as *AssistantsState) *Control {
			cond := agentcomm.WaitCondition{Kind: agentcomm.WaitOnTaskResult, TaskID: "none"}
			mustApply(t, as, meshWaitRegisteredControl("reg-wait2", "reg-waiter2", cond, "", "", 0))
			return meshWaitResolvedControl("reg-wait2", string(agentcomm.WaitReady), "ok")
		},
		kindMeshBoardPublished: func(t *testing.T, as *AssistantsState) *Control {
			return meshBoardPublishedControl("reg-key", "reg-author", "draft", 0, nil, nil)
		},
	}

	for _, kind := range extractMeshKindConstants(t) {
		t.Run(kind, func(t *testing.T) {
			if !controlKinds[kind] {
				t.Fatalf("%s is declared in record.go but missing from controlKinds", kind)
			}
			build, ok := builders[kind]
			if !ok {
				t.Fatalf("%s has no builder in this test's table -- add one so the registry sweep actually exercises it", kind)
			}
			as := newAssistantsState()
			c := build(t, &as)
			if err := dispatchControlKind(&as, c, false); err != nil {
				t.Fatalf("dispatchControlKind(%s): %v", kind, err)
			}
		})
	}
}
