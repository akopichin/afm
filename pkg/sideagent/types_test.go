package sideagent

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// Каждый тип события должен пережить сериализацию/десериализацию конверта вместе
// со своей типизированной data-нагрузкой без потерь.
func TestEvent_RoundTripAllTypes(t *testing.T) {
	ts := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		typ  EventType
		data any
	}{
		{EventTurnAccepted, TurnAcceptedData{Message: "привет", ClientMessageID: "cm-1"}},
		{EventTurnStarted, TurnStartedData{Command: "claude", ContextFormatVersion: "v1"}},
		{EventAssistantText, AssistantTextData{Text: "ответ агента"}},
		{EventToolAction, ToolActionData{Tool: "Bash", Detail: "ls -la", LogRef: "turns/t1/agent.log"}},
		{EventTurnCompleted, TurnCompletedData{}},
		{EventTurnFailed, TurnFailedData{Code: ErrCodeContextLimit, Message: "too big"}},
		{EventTurnInterrupted, TurnInterruptedData{Reason: "shutdown"}},
	}

	for _, c := range cases {
		t.Run(string(c.typ), func(t *testing.T) {
			e := Event{
				SchemaVersion:  SchemaVersion,
				Seq:            7,
				ConversationID: "run-1",
				TurnID:         "t1",
				Type:           c.typ,
				Timestamp:      ts,
			}
			if err := e.SetData(c.data); err != nil {
				t.Fatalf("SetData: %v", err)
			}
			raw, err := e.Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			got, err := DecodeEvent(raw)
			if err != nil {
				t.Fatalf("DecodeEvent: %v", err)
			}
			if got.Type != c.typ || got.Seq != 7 || got.ConversationID != "run-1" ||
				got.TurnID != "t1" || !got.Timestamp.Equal(ts) || got.SchemaVersion != SchemaVersion {
				t.Fatalf("envelope mismatch: %+v", got)
			}

			// data восстанавливается в тот же типизированный вид
			switch want := c.data.(type) {
			case TurnAcceptedData:
				var d TurnAcceptedData
				mustDecode(t, got, &d)
				if d != want {
					t.Fatalf("data mismatch: got %+v want %+v", d, want)
				}
			case TurnStartedData:
				var d TurnStartedData
				mustDecode(t, got, &d)
				if d != want {
					t.Fatalf("data mismatch: got %+v want %+v", d, want)
				}
			case AssistantTextData:
				var d AssistantTextData
				mustDecode(t, got, &d)
				if d != want {
					t.Fatalf("data mismatch: got %+v want %+v", d, want)
				}
			case ToolActionData:
				var d ToolActionData
				mustDecode(t, got, &d)
				if d != want {
					t.Fatalf("data mismatch: got %+v want %+v", d, want)
				}
			case TurnFailedData:
				var d TurnFailedData
				mustDecode(t, got, &d)
				if d != want {
					t.Fatalf("data mismatch: got %+v want %+v", d, want)
				}
			case TurnInterruptedData:
				var d TurnInterruptedData
				mustDecode(t, got, &d)
				if d != want {
					t.Fatalf("data mismatch: got %+v want %+v", d, want)
				}
			default:
				// TurnCompletedData — пустая нагрузка, сверять нечего.
			}
		})
	}
}

func mustDecode(t *testing.T, e Event, v any) {
	t.Helper()
	if err := e.DecodeData(v); err != nil {
		t.Fatalf("DecodeData: %v", err)
	}
}

func TestDecodeEvent_UnknownTypeRejected(t *testing.T) {
	raw := []byte(`{"schema_version":1,"seq":1,"conversation_id":"r","turn_id":"t","type":"wat","timestamp":"2026-10-01T00:00:00Z"}`)
	_, err := DecodeEvent(raw)
	if !errors.Is(err, ErrUnknownEventType) {
		t.Fatalf("want ErrUnknownEventType, got %v", err)
	}
}

func TestDecodeEvent_UnknownSchemaVersionRejected(t *testing.T) {
	raw := []byte(`{"schema_version":999,"seq":1,"conversation_id":"r","turn_id":"t","type":"turn_accepted","timestamp":"2026-10-01T00:00:00Z"}`)
	_, err := DecodeEvent(raw)
	if !errors.Is(err, ErrUnknownSchemaVersion) {
		t.Fatalf("want ErrUnknownSchemaVersion, got %v", err)
	}
}

// schema_version==0 (отсутствует/нулевой) тоже явная ошибка, а не тихий zero-value.
func TestDecodeEvent_ZeroSchemaVersionRejected(t *testing.T) {
	raw := []byte(`{"seq":1,"conversation_id":"r","turn_id":"t","type":"turn_accepted","timestamp":"2026-10-01T00:00:00Z"}`)
	_, err := DecodeEvent(raw)
	if !errors.Is(err, ErrUnknownSchemaVersion) {
		t.Fatalf("want ErrUnknownSchemaVersion, got %v", err)
	}
}

func TestEventType_Valid(t *testing.T) {
	for _, ty := range []EventType{
		EventTurnAccepted, EventTurnStarted, EventAssistantText, EventToolAction,
		EventTurnCompleted, EventTurnFailed, EventTurnInterrupted,
	} {
		if !ty.Valid() {
			t.Fatalf("%q should be valid", ty)
		}
	}
	if EventType("nope").Valid() {
		t.Fatal(`"nope" should be invalid`)
	}
}

func TestTurnState_Transitions(t *testing.T) {
	valid := [][2]TurnState{
		{TurnStateAccepted, TurnStateStarted},
		{TurnStateAccepted, TurnStateFailed},
		{TurnStateAccepted, TurnStateInterrupted},
		{TurnStateStarted, TurnStateCompleted},
		{TurnStateStarted, TurnStateFailed},
		{TurnStateStarted, TurnStateInterrupted},
	}
	for _, p := range valid {
		if !p[0].CanTransitionTo(p[1]) {
			t.Fatalf("transition %s->%s should be allowed", p[0], p[1])
		}
	}
	invalid := [][2]TurnState{
		{TurnStateAccepted, TurnStateCompleted}, // нельзя завершить, не стартовав
		{TurnStateCompleted, TurnStateStarted},  // из терминального никуда
		{TurnStateFailed, TurnStateCompleted},
		{TurnStateStarted, TurnStateAccepted},
		{TurnStateInterrupted, TurnStateStarted},
	}
	for _, p := range invalid {
		if p[0].CanTransitionTo(p[1]) {
			t.Fatalf("transition %s->%s should be rejected", p[0], p[1])
		}
	}
}

func TestTurnState_IsTerminal(t *testing.T) {
	for _, s := range []TurnState{TurnStateCompleted, TurnStateFailed, TurnStateInterrupted} {
		if !s.IsTerminal() {
			t.Fatalf("%s should be terminal", s)
		}
	}
	for _, s := range []TurnState{TurnStateAccepted, TurnStateStarted} {
		if s.IsTerminal() {
			t.Fatalf("%s should not be terminal", s)
		}
	}
}

func TestEventType_TurnState(t *testing.T) {
	cases := map[EventType]TurnState{
		EventTurnAccepted:    TurnStateAccepted,
		EventTurnStarted:     TurnStateStarted,
		EventTurnCompleted:   TurnStateCompleted,
		EventTurnFailed:      TurnStateFailed,
		EventTurnInterrupted: TurnStateInterrupted,
	}
	for ty, want := range cases {
		got, ok := ty.TurnState()
		if !ok || got != want {
			t.Fatalf("%s.TurnState() = %q,%v want %q", ty, got, ok, want)
		}
	}
	// content-события не меняют состояние хода
	for _, ty := range []EventType{EventAssistantText, EventToolAction} {
		if _, ok := ty.TurnState(); ok {
			t.Fatalf("%s should not map to a turn state", ty)
		}
	}
}

// DecodeData на событии без data не должен падать и не должен портить цель.
func TestDecodeData_EmptyIsNoop(t *testing.T) {
	e := Event{SchemaVersion: SchemaVersion, Type: EventTurnCompleted}
	var d TurnCompletedData
	if err := e.DecodeData(&d); err != nil {
		t.Fatalf("DecodeData empty: %v", err)
	}
}

// Error-коды — стабильные строковые константы (часть контракта API).
func TestErrorCodes_Values(t *testing.T) {
	got := map[ErrorCode]string{
		ErrCodeBusy:               "busy",
		ErrCodeInvalidMessage:     "invalid_message",
		ErrCodeMessageTooLarge:    "message_too_large",
		ErrCodeContextLimit:       "context_limit",
		ErrCodeShuttingDown:       "shutting_down",
		ErrCodeStorageUnavailable: "storage_unavailable",
		ErrCodeStaleTurn:          "stale_turn",
	}
	for code, want := range got {
		if string(code) != want {
			t.Fatalf("error code %q != %q", code, want)
		}
	}
	// коды сериализуются как обычные строки внутри data
	b, _ := json.Marshal(TurnFailedData{Code: ErrCodeBusy})
	if string(b) != `{"code":"busy"}` {
		t.Fatalf("unexpected TurnFailedData json: %s", b)
	}
}
