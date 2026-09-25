package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// agentNoteNotice — поле "data" одной строки notices.jsonl для agent_note.
type agentNoteNotice struct {
	ID   uint64 `json:"id"`
	Text string `json:"text"`
}

// readAgentNoteNotices читает notices.jsonl и возвращает данные строк agent_note.
func readAgentNoteNotices(t *testing.T, runDir string) []agentNoteNotice {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []agentNoteNotice
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var envelope struct {
			Type string          `json:"type"`
			Data agentNoteNotice `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("invalid notices.jsonl line: %v (%s)", err, line)
		}
		if envelope.Type == string(bus.EventAgentNote) {
			out = append(out, envelope.Data)
		}
	}
	return out
}

func drainAgentNoteEvents(events <-chan bus.Event) []bus.Event {
	var got []bus.Event
	for {
		select {
		case ev := <-events:
			if ev.Type == bus.EventAgentNote {
				got = append(got, ev)
			}
		default:
			return got
		}
	}
}

// runningNoteServer — сервер с одной running-стадией (revise-able), опционально
// помеченной как скрипт, и заданным поведением Revise.
func runningNoteServer(t *testing.T, isScript bool, revise func(context.Context, string, string) (bool, uint64, error)) (*Server, string, <-chan bus.Event) {
	t.Helper()
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: testStageID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	uiBus := bus.NewUIBus()
	cfg := Config{
		Port: 0, RunDir: runDir, Store: store, UIBus: uiBus,
		Actions: fakeStageActions{revise: revise},
	}
	if isScript {
		cfg.Stages = map[string]StageConfig{testStageID: {IsScript: true}}
	}
	srv := New(cfg)
	subID, events := uiBus.Subscribe(64)
	t.Cleanup(func() { uiBus.Unsubscribe(subID) })
	return srv, runDir, events
}

// TestHandleRevise_EmitsAgentNoteOnApplied: применённый Revise публикует РОВНО
// одно событие agent_note живьём И дублирует ту же нагрузку {id,text} в
// notices.jsonl, где text == mcp.DialogSnippet(feedback), а id == seq перехода.
func TestHandleRevise_EmitsAgentNoteOnApplied(t *testing.T) {
	srv, runDir, events := runningNoteServer(t, false, func(context.Context, string, string) (bool, uint64, error) {
		return true, 42, nil
	})

	feedback := "Учти что API возвращает 500\nи второй строкой деталь"
	body, _ := json.Marshal(map[string]string{"feedback": feedback})
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/revise", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleRevise(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	wantText := mcp.DialogSnippet(feedback)

	noteEvents := drainAgentNoteEvents(events)
	if len(noteEvents) != 1 {
		t.Fatalf("want exactly 1 EventAgentNote, got %d: %+v", len(noteEvents), noteEvents)
	}
	if noteEvents[0].StageID != testStageID {
		t.Errorf("event StageID = %q, want %q", noteEvents[0].StageID, testStageID)
	}
	data, ok := noteEvents[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("event Data has unexpected type: %T", noteEvents[0].Data)
	}
	if data["text"] != wantText {
		t.Errorf("event text = %v, want %q", data["text"], wantText)
	}
	if id, _ := data["id"].(uint64); id != 42 {
		t.Errorf("event id = %v, want 42", data["id"])
	}

	notices := readAgentNoteNotices(t, runDir)
	if len(notices) != 1 {
		t.Fatalf("want exactly 1 agent_note line in notices.jsonl, got %d: %+v", len(notices), notices)
	}
	if notices[0].Text != wantText || notices[0].ID != 42 {
		t.Errorf("notice = %+v, want id=42 text=%q", notices[0], wantText)
	}
}

// TestHandleRevise_NoOpDoesNotEmitAgentNote: если Revise не применился
// (applied=false — стадия ушла из окна / проиграла CAS), хэндлер отвечает 409 и
// НЕ публикует agent_note (ни живьём, ни в notices.jsonl).
func TestHandleRevise_NoOpDoesNotEmitAgentNote(t *testing.T) {
	srv, runDir, events := runningNoteServer(t, false, func(context.Context, string, string) (bool, uint64, error) {
		return false, 0, nil
	})

	body, _ := json.Marshal(map[string]string{"feedback": "note"})
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/revise", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleRevise(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	if got := drainAgentNoteEvents(events); len(got) != 0 {
		t.Fatalf("no-op revise must not publish EventAgentNote, got %+v", got)
	}
	if notices := readAgentNoteNotices(t, runDir); len(notices) != 0 {
		t.Fatalf("no-op revise must not write an agent_note notice, got %+v", notices)
	}
}

// TestHandleRevise_ScriptStageRejected: running-СКРИПТ отклоняется 400, Revise не
// зовётся, agent_note не пишется (у скрипта нет живого агента для правки).
func TestHandleRevise_ScriptStageRejected(t *testing.T) {
	reviseCalled := false
	srv, runDir, events := runningNoteServer(t, true, func(context.Context, string, string) (bool, uint64, error) {
		reviseCalled = true
		return true, 1, nil
	})

	body, _ := json.Marshal(map[string]string{"feedback": "note"})
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/revise", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleRevise(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if reviseCalled {
		t.Error("Revise must not be called for a script stage")
	}
	if got := drainAgentNoteEvents(events); len(got) != 0 {
		t.Fatalf("script revise must not publish EventAgentNote, got %+v", got)
	}
	if notices := readAgentNoteNotices(t, runDir); len(notices) != 0 {
		t.Fatalf("script revise must not write an agent_note notice, got %+v", notices)
	}
}
