package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// TestRecovery_AutoApproveAutoAnswerQAndASurviveRestart is the backend end-to-end
// guard for the auto-answer feed feature: a non-interactive planning stage whose
// agent asks a question is auto-answered by afm, planning completes, and the
// stage passes AUTO-APPROVAL to done — all without a human. RequireApproval:true
// is load-bearing: it proves the auto_approve branch drove the approval, not a
// no-approval fast path. After the run finishes, the store and server are torn
// down and a FRESH server is opened over the SAME run directory (simulating an
// afm restart). The recovered HTTP APIs must still show the whole story:
//
//   - /api/status: stage done, auto_approve:true, show_dialog:true.
//   - the stage NEVER entered awaiting_user_input (non-interactive auto-answer
//     leaves the FSM untouched — no ask_user transition in the durable log).
//   - /api/events?stage=<id>: the dialog_question AND the navigable auto-answer
//     (dialog:true) both replay from notices.jsonl.
//   - /api/stages/<id>/dialog: the full question with its auto-answered answer.
func TestRecovery_AutoApproveAutoAnswerQAndASurviveRestart(t *testing.T) {
	runDir := t.TempDir()
	const stageID = "plan"

	// A synthetic planning agent: writes a question, polls for afm's answer, then
	// emits the plan as assistant text (RunPlanning saves it to plan.md). Modeled
	// on integration_auto_answer_test.go / scenario_harness_test.go's planning
	// completion script.
	agentScript := filepath.Join(runDir, "planning-agent.sh")
	script := "#!/bin/bash\n" +
		"STAGE_DIR=\"$AFM_STAGE_DIR\"\n" +
		"if [ -z \"$STAGE_DIR\" ]; then echo 'no AFM_STAGE_DIR' >&2; exit 1; fi\n" +
		`printf '{"id":"q1","question":"Which approach?","options":["Option A","Option B (recommended)"],"allow_custom":true}' > "$STAGE_DIR/planning.q1.question.json"` + "\n" +
		"for i in $(seq 1 60); do\n" +
		"  if [ -f \"$STAGE_DIR/planning.q1.answer.json\" ]; then break; fi\n" +
		"  sleep 0.25\n" +
		"done\n" +
		"if [ ! -f \"$STAGE_DIR/planning.q1.answer.json\" ]; then echo 'timeout waiting for answer' >&2; exit 1; fi\n" +
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"## Tasks\n\n- [ ] step 1\n\n## Assumptions\n\n- none\n\n## Acceptance Criteria\n\n- [ ] done\n"}]}}'` + "\n" +
		`echo '{"type":"result","subtype":"success"}'` + "\n"
	if err := os.WriteFile(agentScript, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID:          stageID,
		Name:        "Plan",
		Description: "non-interactive planning stage whose agent asks a question",
		Agents:      []flow.AgentType{flow.AgentPlanning},
		Command:     agentScript,
		AutoApprove: true,
		// Interactive left unset (false) — the auto-answer scope.
	}}

	// --- Phase 1: drive the run to completion. ---
	store, err := state.Open(runDir, []string{stageID})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	orch := orchestrator.New(orchestrator.Options{
		RunDir:          runDir,
		Stages:          stages,
		Store:           store,
		Config:          config.Default(),
		Prompts:         orchestrator.DefaultPrompts(),
		RequireApproval: true, // proves the auto_approve branch, not a fast path
	})

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil {
		t.Fatalf("orch.Run: %v", err)
	}
	if got := store.Get(stageID); got != state.StatusDone {
		t.Fatalf("stage status after run = %s, want done", got)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// --- Phase 2: simulate an afm restart — reopen the SAME run dir fresh. ---
	store2, err := state.Open(runDir, []string{stageID})
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { store2.Close() })
	srv := New(Config{
		Port:    0,
		RunDir:  runDir,
		Store:   store2,
		UIBus:   bus.NewUIBus(),
		Actions: fakeStageActions{},
		Stages: map[string]StageConfig{
			stageID: {Interactive: false, AutoApprove: true},
		},
	})

	// --- /api/status: done + auto_approve + show_dialog, recovered from the log. ---
	statusReq := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	statusRR := httptest.NewRecorder()
	srv.handleStatus(statusRR, statusReq)
	if statusRR.Code != http.StatusOK {
		t.Fatalf("/api/status code = %d, want 200", statusRR.Code)
	}
	var status statusResponse
	mustDecode(t, statusRR, &status)
	var sv *StageView
	for i := range status.Stages {
		if status.Stages[i].ID == stageID {
			sv = &status.Stages[i]
			break
		}
	}
	if sv == nil {
		t.Fatalf("stage %q missing from /api/status", stageID)
	}
	if sv.Status != state.StatusDone {
		t.Errorf("recovered status = %s, want done", sv.Status)
	}
	if !sv.AutoApprove {
		t.Error("recovered auto_approve = false, want true")
	}
	if !sv.ShowDialog {
		t.Error("recovered show_dialog = false, want true (dialog history present)")
	}

	// --- /api/events?stage=<id>: question + navigable auto-answer, no awaiting_user_input. ---
	eventsReq := httptest.NewRequest(http.MethodGet, "/api/events?stage="+stageID, nil)
	eventsRR := httptest.NewRecorder()
	srv.handleEvents(eventsRR, eventsReq)
	var events []feedEvent
	mustDecode(t, eventsRR, &events)

	var sawQuestion, sawNavigableAnswer bool
	for _, e := range events {
		switch e.Type {
		case string(bus.EventDialogQuestion):
			sawQuestion = true
		case string(bus.EventAutoAnswered):
			m, _ := e.Data.(map[string]any)
			if dialog, _ := m["dialog"].(bool); dialog {
				sawNavigableAnswer = true
			}
		case eventAskUser:
			t.Error("found an ask_user event: the non-interactive stage must never surface the question to a human")
		case "stage_status_changed":
			if s, _ := e.Data.(string); s == string(state.StatusAwaitingUserInput) {
				t.Error("stage entered awaiting_user_input: the question must have been auto-answered")
			}
		default:
		}
	}
	if !sawQuestion {
		t.Error("recovered feed missing dialog_question")
	}
	if !sawNavigableAnswer {
		t.Error("recovered feed missing navigable auto_answered (dialog:true)")
	}

	// --- /api/stages/<id>/dialog: full question + its auto-answered answer. ---
	dialogReq := httptest.NewRequest(http.MethodGet, "/api/stages/"+stageID+"/dialog", nil)
	dialogRR := httptest.NewRecorder()
	srv.routeStages(dialogRR, dialogReq)
	if dialogRR.Code != http.StatusOK {
		t.Fatalf("/api/stages/%s/dialog code = %d, want 200; body=%s", stageID, dialogRR.Code, dialogRR.Body.String())
	}
	var dialog []dialogUIEntry
	if err := json.Unmarshal(dialogRR.Body.Bytes(), &dialog); err != nil {
		t.Fatalf("decode dialog: %v", err)
	}
	var qa *dialogUIEntry
	for i := range dialog {
		if dialog[i].ID == "q1" {
			qa = &dialog[i]
			break
		}
	}
	if qa == nil {
		t.Fatalf("dialog missing question q1; entries=%+v", dialog)
	}
	if qa.Question == "" {
		t.Error("dialog question text is empty")
	}
	if qa.Answer == nil || *qa.Answer != "Option B" {
		t.Errorf("dialog answer = %v, want %q", qa.Answer, "Option B")
	}
	if !qa.AutoAnswered {
		t.Error("dialog answer must carry the auto_answered flag")
	}
}
