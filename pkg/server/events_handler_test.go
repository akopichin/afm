package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/state"
)

func TestHandleEvents_ReplaysTransitionsAndNotices(t *testing.T) {
	srv, runDir := setupTestServer(t)

	// events.jsonl уже содержит одну transition из setupTestServerWithWS
	// (StatusPending → StatusAwaitingApproval, Event: "test_setup") — плюс
	// добавим ask_user, чтобы проверить, что она реплеится как отдельный тип.
	if err := srv.store.Apply(&state.Transition{
		StageID: testStageID, From: state.StatusAwaitingApproval, To: state.StatusAwaitingUserInput,
		Event: "ask_user",
	}); err != nil {
		t.Fatal(err)
	}

	// notices.jsonl (Task 3 sidecar) — одна строка agent_completed.
	noticesLine := `{"time":"2026-07-27T10:00:00Z","type":"agent_completed","stage_id":"s1","data":"planning"}` + "\n"
	if err := os.WriteFile(filepath.Join(runDir, "notices.jsonl"), []byte(noticesLine), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/events", nil)
	w := httptest.NewRecorder()
	srv.handleEvents(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var events []feedEvent
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	var sawStatusChanged, sawAskUser, sawAgentCompleted bool
	for _, e := range events {
		switch e.Type {
		case "stage_status_changed":
			sawStatusChanged = true
		case "ask_user":
			sawAskUser = true
		case "agent_completed":
			sawAgentCompleted = true
			if e.Data != "planning" {
				t.Errorf("agent_completed Data = %v, want %q", e.Data, "planning")
			}
		default:
		}
	}
	if !sawStatusChanged {
		t.Error("expected at least one stage_status_changed event")
	}
	if !sawAskUser {
		t.Error("expected an ask_user event derived from the ask_user transition")
	}
	if !sawAgentCompleted {
		t.Error("expected an agent_completed event from notices.jsonl")
	}
}

func TestHandleEvents_CapsAt200(t *testing.T) {
	srv, _ := setupTestServer(t)
	for i := 0; i < 250; i++ {
		from := state.StatusAwaitingApproval
		if i > 0 {
			from = state.StatusRunning
		}
		to := state.StatusRunning
		if i == 249 {
			to = state.StatusDone
		}
		if err := srv.store.Apply(&state.Transition{StageID: testStageID, From: from, To: to, Event: "noop"}); err != nil {
			// CAS может не совпасть при таком синтетическом чередовании —
			// тесту важно только итоговое количество записей в логе, не
			// валидность каждого перехода, поэтому игнорируем ошибку CAS.
			continue
		}
	}

	req := httptest.NewRequest("GET", "/api/events", nil)
	w := httptest.NewRecorder()
	srv.handleEvents(w, req)

	var events []feedEvent
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(events) > 200 {
		t.Errorf("got %d events, want <= 200", len(events))
	}
}

func TestReconstructAgentActions_CoversAllPhasesIncludingAutonomous(t *testing.T) {
	stageDir := t.TempDir()

	planningLine := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"echo hi"}}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(stageDir, "planning.jsonl"), []byte(planningLine), 0644); err != nil {
		t.Fatal(err)
	}
	// autonomous — граничный случай: файл называется autonomous.jsonl,
	// НЕ autonomous_execution.jsonl (flow.PhaseJSONL спецкейс).
	autonomousLine := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"plan.md"}}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(stageDir, "autonomous.jsonl"), []byte(autonomousLine), 0644); err != nil {
		t.Fatal(err)
	}

	out := reconstructAgentActions(filepath.Dir(stageDir), filepath.Base(stageDir))

	if len(out) != 2 {
		t.Fatalf("want 2 agent_action events (one per phase file), got %d: %+v", len(out), out)
	}
	tools := map[string]bool{}
	for _, e := range out {
		data, ok := e.Data.(map[string]string)
		if !ok {
			t.Fatalf("unexpected Data type: %#v", e.Data)
		}
		tools[data["tool"]] = true
	}
	if !tools["Bash"] || !tools["Write"] {
		t.Errorf("expected actions from both planning.jsonl (Bash) and autonomous.jsonl (Write), got tools: %v", tools)
	}
}
