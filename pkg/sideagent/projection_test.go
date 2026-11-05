package sideagent

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// buildHistory кодирует события с монотонным seq: тесты проекции не нуждаются в Store.
func buildHistory(t *testing.T, items ...histItem) []Event {
	t.Helper()
	var out []Event
	for i, it := range items {
		e := Event{SchemaVersion: SchemaVersion, Seq: uint64(i + 1), ConversationID: "run", TurnID: it.turn, Type: it.typ}
		if it.data != nil {
			if err := e.SetData(it.data); err != nil {
				t.Fatal(err)
			}
		}
		out = append(out, e)
	}
	return out
}

type histItem struct {
	typ  EventType
	turn string
	data any
}

func queued(id, text, stage string) histItem {
	return histItem{EventMessageQueued, "", MessageQueuedData{MessageID: id, ClientMessageID: "c-" + id, Message: text, StageID: stage}}
}

func claim(turn, stage string, ids ...string) histItem {
	return histItem{EventTurnAccepted, turn, TurnAcceptedData{StageID: stage, MessageIDs: ids}}
}

func TestProjection_QueueAndClaim(t *testing.T) {
	c := ProjectConversation(buildHistory(t,
		queued("m1", "A", ""), queued("m2", "B", ""), queued("m3", "C", ""),
		histItem{EventMessageCancelled, "", MessageCancelledData{MessageID: "m2"}},
	))
	var ids []string
	for _, q := range c.Queued() {
		ids = append(ids, q.MessageID)
	}
	if !reflect.DeepEqual(ids, []string{"m1", "m3"}) {
		t.Fatalf("queue = %v, want [m1 m3]", ids)
	}
	if c.State() != StateIdle || c.ActiveTurnID() != "" {
		t.Fatalf("state=%q active=%q, want idle with queued messages", c.State(), c.ActiveTurnID())
	}
	if m, _ := c.Message("m2"); m.State != MessageStateCancelled {
		t.Fatalf("m2 state = %q", m.State)
	}
}

func TestProjection_MessageStatesFollowTurn(t *testing.T) {
	h := buildHistory(t,
		queued("m1", "A", ""), queued("m2", "B", ""), queued("m3", "C", ""),
		claim("t1", "", "m1", "m2"),
	)
	c := ProjectConversation(h)
	if m, _ := c.Message("m1"); m.State != MessageStateRunning || m.TurnID != "t1" {
		t.Fatalf("m1 = %+v, want running in t1", m)
	}
	if c.State() != StateRunning || c.ActiveTurnID() != "t1" {
		t.Fatalf("state=%q active=%q", c.State(), c.ActiveTurnID())
	}
	if q := c.Queued(); len(q) != 1 || q[0].MessageID != "m3" {
		t.Fatalf("queue = %+v, want [m3]", q)
	}

	cases := map[EventType]MessageState{
		EventTurnCompleted:   MessageStateCompleted,
		EventTurnFailed:      MessageStateFailed,
		EventTurnInterrupted: MessageStateInterrupted,
	}
	for typ, want := range cases {
		full := append(append([]Event{}, h...), buildHistory(t, histItem{EventTurnStarted, "t1", nil})...)
		full[len(full)-1].Seq = uint64(len(full))
		term := Event{SchemaVersion: SchemaVersion, Seq: uint64(len(full) + 1), TurnID: "t1", Type: typ}
		c := ProjectConversation(append(full, term))
		for _, id := range []string{"m1", "m2"} {
			if m, _ := c.Message(id); m.State != want {
				t.Fatalf("%s: %s state = %q, want %q", typ, id, m.State, want)
			}
		}
		if m, _ := c.Message("m3"); m.State != MessageStateQueued {
			t.Fatalf("%s: queued m3 must stay queued, got %q", typ, m.State)
		}
	}
}

func TestProjection_FirstTerminalWins(t *testing.T) {
	c := ProjectConversation(buildHistory(t,
		queued("m1", "A", ""),
		histItem{EventMessageCancelled, "", MessageCancelledData{MessageID: "m1"}},
		histItem{EventMessageFailed, "", MessageFailedData{MessageID: "m1", Code: ErrCodeContextLimit}},
		claim("t1", "", "m1"),
	))
	if m, _ := c.Message("m1"); m.State != MessageStateCancelled {
		t.Fatalf("state = %q, want first terminal (cancelled)", m.State)
	}
}

func TestProjection_FailedMessageKeepsCode(t *testing.T) {
	c := ProjectConversation(buildHistory(t,
		queued("m1", "A", ""),
		histItem{EventMessageFailed, "", MessageFailedData{MessageID: "m1", Code: ErrCodeContextLimit, Message: "big"}},
	))
	m, _ := c.Message("m1")
	if m.State != MessageStateFailed || m.ErrorCode != ErrCodeContextLimit {
		t.Fatalf("m1 = %+v", m)
	}
	if len(c.Queued()) != 0 {
		t.Fatal("failed message must leave the queue")
	}
}

func TestProjection_QueuedGroupStopsAtContextChange(t *testing.T) {
	c := ProjectConversation(buildHistory(t,
		queued("m1", "A", "s1"), queued("m2", "B", "s2"), queued("m3", "C", "s1"),
	))
	group := c.QueuedGroup()
	if len(group) != 1 || group[0].MessageID != "m1" {
		t.Fatalf("group = %+v, want only m1", group)
	}
}

func TestProjection_ClientLookupCoversQueuedAndLegacy(t *testing.T) {
	c := ProjectConversation(buildHistory(t,
		histItem{EventTurnAccepted, "t0", TurnAcceptedData{Message: "old", ClientMessageID: "legacy"}},
		queued("m1", "A", "s1"),
	))
	rec, ok := c.Client("legacy")
	if !ok || rec.TurnID != "t0" || rec.MessageID != "" || rec.Text != "old" {
		t.Fatalf("legacy = %+v ok=%v", rec, ok)
	}
	rec, ok = c.Client("c-m1")
	if !ok || rec.MessageID != "m1" || rec.Text != "A" || rec.StageID != "s1" {
		t.Fatalf("queued = %+v ok=%v", rec, ok)
	}
}

func TestProjection_ActiveSource(t *testing.T) {
	c := ProjectConversation(buildHistory(t,
		histItem{EventTurnAccepted, "t1", TurnAcceptedData{Message: "hi", ClientMessageID: "mesh:x", SourceMessageID: "x", SourceSenderID: "stage:a", SourceSenderName: "Alice"}},
	))
	src := c.ActiveSource()
	if src == nil || src.Kind != SourceMesh || src.SenderID != "stage:a" || src.SenderName != "Alice" {
		t.Fatalf("source = %+v", src)
	}
	c = ProjectConversation(buildHistory(t, claim("t1", "", "m1")))
	if src := c.ActiveSource(); src == nil || src.Kind != SourceModal {
		t.Fatalf("source = %+v, want modal", src)
	}
	if ProjectConversation(nil).ActiveSource() != nil {
		t.Fatal("empty conversation has no active source")
	}
}

// Журнал без новых событий читается как раньше: состояние по последнему ходу.
func TestProjection_LegacyJournalState(t *testing.T) {
	c := ProjectConversation(buildHistory(t,
		histItem{EventTurnAccepted, "t1", TurnAcceptedData{Message: "hi", ClientMessageID: "c1"}},
		histItem{EventTurnStarted, "t1", TurnStartedData{}},
		histItem{EventTurnCompleted, "t1", TurnCompletedData{}},
		histItem{EventTurnAccepted, "t2", TurnAcceptedData{Message: "hi2", ClientMessageID: "c2"}},
	))
	if c.State() != StateRunning || c.ActiveTurnID() != "t2" {
		t.Fatalf("state=%q active=%q", c.State(), c.ActiveTurnID())
	}
	if s, ok := c.TurnState("t1"); !ok || s != TurnStateCompleted {
		t.Fatalf("t1 = %q", s)
	}
	if len(c.Queued()) != 0 {
		t.Fatal("legacy journal has no queue")
	}
}

func TestProjection_ValidateClaimAndCancel(t *testing.T) {
	base := buildHistory(t,
		queued("m1", "A", "s1"), queued("m2", "B", "s1"), queued("m3", "C", "s2"),
		histItem{EventMessageCancelled, "", MessageCancelledData{MessageID: "m2"}},
	)
	enc := func(typ EventType, turn string, data any) Event {
		e := Event{SchemaVersion: SchemaVersion, Type: typ, TurnID: turn}
		if err := e.SetData(data); err != nil {
			t.Fatal(err)
		}
		return e
	}
	cases := []struct {
		name string
		ev   Event
		want error
	}{
		{"claim skips queue head", enc(EventTurnAccepted, "t", TurnAcceptedData{StageID: "s2", MessageIDs: []string{"m3"}}), ErrInvalidTransition},
		{"claim cancelled message", enc(EventTurnAccepted, "t", TurnAcceptedData{StageID: "s1", MessageIDs: []string{"m1", "m2"}}), ErrMessageNotQueued},
		{"claim across contexts", enc(EventTurnAccepted, "t", TurnAcceptedData{StageID: "s1", MessageIDs: []string{"m1", "m3"}}), ErrInvalidTransition},
		{"claim wrong turn context", enc(EventTurnAccepted, "t", TurnAcceptedData{StageID: "other", MessageIDs: []string{"m1"}}), ErrInvalidTransition},
		{"claim unknown", enc(EventTurnAccepted, "t", TurnAcceptedData{MessageIDs: []string{"nope"}}), ErrMessageNotFound},
		{"cancel unknown", enc(EventMessageCancelled, "", MessageCancelledData{MessageID: "nope"}), ErrMessageNotFound},
		{"cancel twice", enc(EventMessageCancelled, "", MessageCancelledData{MessageID: "m2"}), ErrMessageNotQueued},
		{"fail cancelled", enc(EventMessageFailed, "", MessageFailedData{MessageID: "m2"}), ErrMessageNotQueued},
		{"message event with turn id", enc(EventMessageCancelled, "t1", MessageCancelledData{MessageID: "m1"}), ErrInvalidTransition},
		{"duplicate message id", enc(EventMessageQueued, "", MessageQueuedData{MessageID: "m1", Message: "x"}), ErrInvalidTransition},
		{"queued without id", enc(EventMessageQueued, "", MessageQueuedData{Message: "x"}), ErrInvalidTransition},
		{"valid claim", enc(EventTurnAccepted, "t", TurnAcceptedData{StageID: "s1", MessageIDs: []string{"m1"}}), nil},
		{"valid cancel", enc(EventMessageCancelled, "", MessageCancelledData{MessageID: "m3"}), nil},
	}
	c := ProjectConversation(base)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.validate(tc.ev)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("validate: %v", err)
				}
				return
			}
			if !isErr(err, tc.want) {
				t.Fatalf("validate = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestProjection_ClaimAfterClaimRejected(t *testing.T) {
	c := ProjectConversation(buildHistory(t, queued("m1", "A", ""), claim("t1", "", "m1")))
	e := Event{SchemaVersion: SchemaVersion, Type: EventTurnAccepted, TurnID: "t2"}
	_ = e.SetData(TurnAcceptedData{MessageIDs: []string{"m1"}})
	if err := c.validate(e); !isErr(err, ErrMessageNotQueued) {
		t.Fatalf("validate = %v, want not queued", err)
	}
}

func isErr(err, target error) bool { return err != nil && errors.Is(err, target) }

var _ = json.Marshal
