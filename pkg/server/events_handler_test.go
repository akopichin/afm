package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
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

// TestHandleEvents_TransitionsNeverExceedGlobalCap is a basic sanity check
// that the global response never exceeds maxReplayEvents (raised from 200 to
// 1000 — see TestHandleEvents_GlobalCapRaisedTo1000 for the actual boundary
// test, which exercises the cap via notices rather than transitions).
func TestHandleEvents_TransitionsNeverExceedGlobalCap(t *testing.T) {
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
	if len(events) > maxReplayEvents {
		t.Errorf("got %d events, want <= %d", len(events), maxReplayEvents)
	}
}

// TestReconstructNotices_DedupsDialogQuestionAndAnswerByContent is the
// regression guard for F2/F3 (task-fixB): `processed` in dialog_poller.go is
// in-memory, so an afm restart with a still-unanswered interactive question
// re-emits the SAME dialog_question notice again (recovery re-scans, the
// in-memory map starts empty). reconstructNotices must collapse repeated
// dialog_question/dialog_answer notices that share (type, stage_id, phase,
// id, title) down to one — the read-side half of "idempotent by content" —
// while leaving every OTHER notice type untouched, since script_output/
// auto_answered/context_warning legitimately repeat.
func TestReconstructNotices_DedupsDialogQuestionAndAnswerByContent(t *testing.T) {
	runDir := t.TempDir()
	lines := []string{
		`{"time":"2026-09-16T10:00:00Z","type":"dialog_question","stage_id":"s1","data":{"phase":"planning","id":"q1","title":"Which approach?"}}`,
		// A restart-duplicate: identical content, later timestamp.
		`{"time":"2026-09-16T10:00:05Z","type":"dialog_question","stage_id":"s1","data":{"phase":"planning","id":"q1","title":"Which approach?"}}`,
		// Two DIFFERENT script_output lines — must both survive (allowlist-gated dedup).
		`{"time":"2026-09-16T10:00:01Z","type":"script_output","stage_id":"s1","data":"line one"}`,
		`{"time":"2026-09-16T10:00:02Z","type":"script_output","stage_id":"s1","data":"line one"}`,
		// Same phase/id as the question above but a DIFFERENT type+title — must survive as its own row.
		`{"time":"2026-09-16T10:00:06Z","type":"dialog_answer","stage_id":"s1","data":{"phase":"planning","id":"q1","title":"Option B"}}`,
	}
	data := ""
	for _, l := range lines {
		data += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(runDir, "notices.jsonl"), []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	out := reconstructNotices(runDir, "")

	var dialogQuestions, dialogAnswers, scriptOutputs int
	for _, e := range out {
		switch e.Type {
		case "dialog_question":
			dialogQuestions++
		case "dialog_answer":
			dialogAnswers++
		case "script_output":
			scriptOutputs++
		default:
		}
	}
	if dialogQuestions != 1 {
		t.Errorf("dialog_question count = %d, want 1 (duplicate must be deduped)", dialogQuestions)
	}
	if dialogAnswers != 1 {
		t.Errorf("dialog_answer count = %d, want 1 (different type+title, must survive)", dialogAnswers)
	}
	if scriptOutputs != 2 {
		t.Errorf("script_output count = %d, want 2 (non-allowlisted type must never be deduped)", scriptOutputs)
	}
}

// TestHandleEvents_ScriptFailedAndStderrScriptOutputReplay is the regression
// guard for the script-stderr-visibility feature (Tasks 3-5): script_failed
// (data {error, stderr_tail}, agents.go's runScriptStage) and script_output
// enriched with a stream field (data {hook, line, stream}, hooks.go's
// execScript) must replay through GET /api/events unchanged — reconstructNotices
// is generic over notice type and doesn't allowlist/filter either shape, so
// this proves that with no code change needed (only dialog_question/
// dialog_answer are content-deduped, see dialogDedupTypes above).
func TestHandleEvents_ScriptFailedAndStderrScriptOutputReplay(t *testing.T) {
	runDir := t.TempDir()
	stageID := "build"

	stagefiles.AppendNotice(runDir, stageID, string(bus.EventScriptFailed), map[string]string{
		"error":       "exit status 1",
		"stderr_tail": "line1\nline2\n",
	})
	stagefiles.AppendNotice(runDir, stageID, string(bus.EventScriptOutput), map[string]string{
		"hook":   "",
		"line":   "boom",
		"stream": "stderr",
	})

	srv := newTestServerForRunDir(t, runDir, []string{stageID})
	req := httptest.NewRequest("GET", "/api/events?stage="+stageID, nil)
	rr := httptest.NewRecorder()
	srv.handleEvents(rr, req)

	var events []feedEvent
	mustDecode(t, rr, &events)

	var sawScriptFailed, sawStderrScriptOutput bool
	for _, e := range events {
		if e.StageID != stageID {
			t.Fatalf("stage filter leaked stage %q", e.StageID)
		}
		data, ok := e.Data.(map[string]any)
		if !ok {
			continue
		}
		switch e.Type {
		case "script_failed":
			sawScriptFailed = true
			if data["error"] != "exit status 1" {
				t.Errorf("script_failed error = %v, want %q", data["error"], "exit status 1")
			}
			if data["stderr_tail"] != "line1\nline2\n" {
				t.Errorf("script_failed stderr_tail = %v, want %q", data["stderr_tail"], "line1\nline2\n")
			}
		case "script_output":
			if data["stream"] == "stderr" {
				sawStderrScriptOutput = true
				if data["line"] != "boom" {
					t.Errorf("script_output line = %v, want %q", data["line"], "boom")
				}
			}
		default:
		}
	}
	if !sawScriptFailed {
		t.Error("expected a script_failed event with error/stderr_tail intact")
	}
	if !sawStderrScriptOutput {
		t.Error("expected a script_output event with stream=stderr intact")
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

// mustDecode decodes rr's JSON body into v, failing the test on error.
func mustDecode(t *testing.T, rr *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rr.Body.Bytes(), v); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
}

// writeNotices appends n script_output notices for stageID into runDir's
// notices.jsonl via stagefiles.AppendNotice — the exact call execScript makes
// for real script-stage output (pkg/orchestrator/hooks.go's execScript).
func writeNotices(t *testing.T, runDir, stageID string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		data := map[string]string{"hook": "", "line": "line " + strconv.Itoa(i)}
		stagefiles.AppendNotice(runDir, stageID, string(bus.EventScriptOutput), data)
	}
}

// newTestServerForRunDir opens a Server around an already-prepared run dir
// (e.g. one whose notices.jsonl was written directly by the test) for the
// given stage IDs. Unlike setupTestServer it doesn't seed plan.md/an initial
// transition — callers that need those write them themselves.
func newTestServerForRunDir(t *testing.T, runDir string, stageIDs []string) *Server {
	t.Helper()
	store, err := state.Open(runDir, stageIDs)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return New(Config{
		Port:    0,
		RunDir:  runDir,
		Store:   store,
		UIBus:   bus.NewUIBus(),
		Actions: fakeStageActions{},
	})
}

// newTestServerWithStages builds a Server backed by a fresh temp run dir,
// seeding each stage in counts with that many script_output notices (via
// writeNotices) — the shared fixture for the stage-filter/cap tests.
func newTestServerWithStages(t *testing.T, counts map[string]int) *Server {
	t.Helper()
	runDir := t.TempDir()
	stageIDs := make([]string, 0, len(counts))
	for id := range counts {
		stageIDs = append(stageIDs, id)
	}
	slices.Sort(stageIDs) // deterministic write order across stages
	for _, id := range stageIDs {
		writeNotices(t, runDir, id, counts[id])
	}
	return newTestServerForRunDir(t, runDir, stageIDs)
}

func TestHandleEvents_StageFilterReturnsOnlyThatStage(t *testing.T) {
	// Build a run with two stages; assert ?stage=early returns only 'early' events.
	srv := newTestServerWithStages(t, map[string]int{"early": 3, "noise": 50})
	req := httptest.NewRequest("GET", "/api/events?stage=early", nil)
	rr := httptest.NewRecorder()
	srv.handleEvents(rr, req)
	var got []feedEvent
	mustDecode(t, rr, &got)
	for _, e := range got {
		if e.StageID != "early" {
			t.Fatalf("stage filter leaked stage %q", e.StageID)
		}
	}
	if len(got) == 0 {
		t.Fatal("expected 'early' events, got none")
	}
}

func TestHandleEvents_StageFilterCapsAt200(t *testing.T) {
	// 'noise' has > 200 reconstructable events; ?stage=noise must cap at 200.
	srv := newTestServerWithStages(t, map[string]int{"noise": 300})
	req := httptest.NewRequest("GET", "/api/events?stage=noise", nil)
	rr := httptest.NewRecorder()
	srv.handleEvents(rr, req)
	var got []feedEvent
	mustDecode(t, rr, &got)
	if len(got) != 200 {
		t.Fatalf("per-stage cap: want 200, got %d", len(got))
	}
}

func TestHandleEvents_GlobalCapRaisedTo1000(t *testing.T) {
	// > 1000 events flow-wide; global response caps at 1000 (was 200).
	srv := newTestServerWithStages(t, map[string]int{"noise": 1100})
	req := httptest.NewRequest("GET", "/api/events", nil)
	rr := httptest.NewRecorder()
	srv.handleEvents(rr, req)
	var got []feedEvent
	mustDecode(t, rr, &got)
	if len(got) != 1000 {
		t.Fatalf("global cap: want 1000, got %d", len(got))
	}
}

// TestHandleEvents_StageFilterSurvivesFlowWideNoticeNoise is the blocker
// regression (codex review): an early stage's notices must survive even when
// buried under thousands of NEWER other-stage notice lines in the shared
// notices.jsonl. This is the real-world reproduction of the original bug.
func TestHandleEvents_StageFilterSurvivesFlowWideNoticeNoise(t *testing.T) {
	runDir := t.TempDir()
	// early: 5 notices written FIRST.
	writeNotices(t, runDir, "early", 5)
	// noise: 2000 notices written AFTER (dominate any flow-wide tail window).
	writeNotices(t, runDir, "noise", 2000)
	srv := newTestServerForRunDir(t, runDir, []string{"early", "noise"})
	req := httptest.NewRequest("GET", "/api/events?stage=early", nil)
	rr := httptest.NewRecorder()
	srv.handleEvents(rr, req)
	var got []feedEvent
	mustDecode(t, rr, &got)
	if len(got) < 5 {
		t.Fatalf("early notices truncated by flow-wide noise: got %d, want >= 5", len(got))
	}
}
