package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/akopichin/afm/pkg/mcp"
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
// that the global response never exceeds maxReplayEvents (raised from 1000 to
// 10000 — see TestHandleEvents_GlobalCapRaisedTo10000 for the actual boundary
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

// TestReconstructNotices_AutoAnsweredDedupOnlyWhenDialog is the regression guard
// for the auto-answer feed feature: an `auto_answered` notice is dialog-navigable
// (and therefore dialog-dedupable) IFF its data carries `dialog: true` — the
// navigable auto-answer the frontend links to the full dialog history. The
// non-navigable repair-progress notices (data.answer == "⚙️ …", NO `dialog`
// field) must NEVER be collapsed: they legitimately repeat, one per repair
// attempt, and dropping them would hide the progress trail. It also proves Task 1
// (answer is part of the dedup key): two navigable auto-answers that share
// phase/id but carry DIFFERENT answers must both survive.
func TestReconstructNotices_AutoAnsweredDedupOnlyWhenDialog(t *testing.T) {
	runDir := t.TempDir()
	lines := []string{
		// Two identical navigable auto-answers (dialog:true) — collapse to ONE.
		`{"time":"2026-09-29T10:00:00Z","type":"auto_answered","stage_id":"s1","data":{"phase":"planning","id":"q1","answer":"Option B","from_options":true,"question_title":"Which approach?","dialog":true}}`,
		`{"time":"2026-09-29T10:00:05Z","type":"auto_answered","stage_id":"s1","data":{"phase":"planning","id":"q1","answer":"Option B","from_options":true,"question_title":"Which approach?","dialog":true}}`,
		// Two DIFFERENT repair-progress notices (no dialog marker) — both survive.
		`{"time":"2026-09-29T10:00:01Z","type":"auto_answered","stage_id":"s1","data":{"phase":"planning","id":"q2","answer":"⚙️ repairing (attempt 1)","from_options":false}}`,
		`{"time":"2026-09-29T10:00:02Z","type":"auto_answered","stage_id":"s1","data":{"phase":"planning","id":"q2","answer":"⚙️ repairing (attempt 2)","from_options":false}}`,
		// A navigable auto-answer with a DIFFERENT answer, same phase/id — must NOT
		// collapse into the first two (Task 1: answer is part of the dedup key).
		`{"time":"2026-09-29T10:00:06Z","type":"auto_answered","stage_id":"s1","data":{"phase":"planning","id":"q1","answer":"Option C","from_options":true,"question_title":"Which approach?","dialog":true}}`,
		// Two navigable auto-answers reusing id q3 with the SAME answer but for
		// DIFFERENT questions (an agent may legitimately reuse an answered id) —
		// must NOT collapse: question_title is part of the dedup key (review HIGH #2).
		`{"time":"2026-09-29T10:00:07Z","type":"auto_answered","stage_id":"s1","data":{"phase":"planning","id":"q3","answer":"Yes","from_options":true,"question_title":"Use Postgres?","dialog":true}}`,
		`{"time":"2026-09-29T10:00:08Z","type":"auto_answered","stage_id":"s1","data":{"phase":"planning","id":"q3","answer":"Yes","from_options":true,"question_title":"Enable cache?","dialog":true}}`,
	}
	data := ""
	for _, l := range lines {
		data += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(runDir, "notices.jsonl"), []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	out := reconstructNotices(runDir, "")

	var navigable, repair int
	for _, e := range out {
		if e.Type != string(bus.EventAutoAnswered) {
			continue
		}
		m, _ := e.Data.(map[string]any)
		if dialog, _ := m["dialog"].(bool); dialog {
			navigable++
		} else {
			repair++
		}
	}
	// Option B (deduped from 2 → 1) + Option C (distinct answer) + q3/"Use
	// Postgres?" + q3/"Enable cache?" (distinct question_title) = 4 navigable.
	if navigable != 4 {
		t.Errorf("navigable auto_answered count = %d, want 4 (identical collapse; distinct answer AND distinct question_title survive)", navigable)
	}
	// Both repair-progress notices survive — never dialog-deduped.
	if repair != 2 {
		t.Errorf("repair-progress auto_answered count = %d, want 2 (non-dialog notices must never be deduped)", repair)
	}
}

// TestHandleEvents_EarlyStageQAndASurviveGlobalLimit is the stage-filter-before-
// limit guard for the auto-answer feed feature. An early stage's dialog_question
// and its navigable auto_answered notice, written FIRST, must both survive a
// per-stage query even when the shared notices.jsonl is buried under >10000 later
// notices from dozens of OTHER stages. reconstructNotices rings PER SCOPE while
// scanning, so a ?stage=early query only ever holds early's own notices — it must
// NOT global-ring-then-filter (the original bug), which would evict the two
// early notices (oldest of >10000) before the stage filter ever ran.
func TestHandleEvents_EarlyStageQAndASurviveGlobalLimit(t *testing.T) {
	runDir := t.TempDir()

	// early: the question and its navigable auto-answer, written FIRST (oldest).
	stagefiles.AppendNotice(runDir, "early", string(bus.EventDialogQuestion),
		mcp.DialogFeedNotice("planning", "q1", "Which approach?"))
	stagefiles.AppendNotice(runDir, "early", string(bus.EventAutoAnswered),
		mcp.AutoAnsweredNotice("planning", "q1", "Option B", true, "Which approach?"))

	// >10000 later notices spread across 35 OTHER stages (35*300 = 10500).
	stageIDs := []string{"early"}
	for i := 0; i < 35; i++ {
		id := "noise" + strconv.Itoa(i)
		stageIDs = append(stageIDs, id)
		writeNotices(t, runDir, id, 300)
	}

	srv := newTestServerForRunDir(t, runDir, stageIDs)
	req := httptest.NewRequest("GET", "/api/events?stage=early", nil)
	rr := httptest.NewRecorder()
	srv.handleEvents(rr, req)

	var got []feedEvent
	mustDecode(t, rr, &got)

	questionIdx, answerIdx := -1, -1
	var questions, answers int
	for i, e := range got {
		if e.StageID != "early" {
			t.Fatalf("stage filter leaked stage %q", e.StageID)
		}
		switch e.Type {
		case string(bus.EventDialogQuestion):
			questions++
			questionIdx = i
		case string(bus.EventAutoAnswered):
			answers++
			answerIdx = i
		default:
		}
	}
	if questions != 1 {
		t.Errorf("dialog_question count = %d, want exactly 1 (survives the global limit, no duplicate)", questions)
	}
	if answers != 1 {
		t.Errorf("navigable auto_answered count = %d, want exactly 1 (survives the global limit, no duplicate)", answers)
	}
	if questionIdx >= 0 && answerIdx >= 0 && questionIdx > answerIdx {
		t.Errorf("question must sort before its answer: questionIdx=%d, answerIdx=%d", questionIdx, answerIdx)
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

func TestHandleEvents_StageFilterCapsAt2000(t *testing.T) {
	// 'noise' has > 2000 reconstructable events; ?stage=noise must cap at 2000.
	srv := newTestServerWithStages(t, map[string]int{"noise": 2100})
	req := httptest.NewRequest("GET", "/api/events?stage=noise", nil)
	rr := httptest.NewRecorder()
	srv.handleEvents(rr, req)
	var got []feedEvent
	mustDecode(t, rr, &got)
	if len(got) != 2000 {
		t.Fatalf("per-stage cap: want 2000, got %d", len(got))
	}
}

func TestHandleEvents_GlobalCapRaisedTo10000(t *testing.T) {
	// > 10000 events flow-wide; global response caps at 10000 (was 1000).
	srv := newTestServerWithStages(t, map[string]int{"noise": 11000})
	req := httptest.NewRequest("GET", "/api/events", nil)
	rr := httptest.NewRecorder()
	srv.handleEvents(rr, req)
	var got []feedEvent
	mustDecode(t, rr, &got)
	if len(got) != 10000 {
		t.Fatalf("global cap: want 10000, got %d", len(got))
	}
}

// TestReconstructEventHistory_PerStageReaches2000NotBottleneckedByMaxLines
// proves the per-stage feed can actually reach its 2000 cap: a single stage
// whose phase log holds MORE than 2000 parseable agent-action lines must yield
// exactly maxStageReplayEvents=2000 reconstructed events. This can only hold if
// maxLinesPerLog (2400) leaves headroom above the per-stage cap — with the old
// 1200 window readLines would keep only 1200 lines, so the per-stage feed would
// top out at 1200 and this test would fail.
func TestReconstructEventHistory_PerStageReaches2000NotBottleneckedByMaxLines(t *testing.T) {
	runDir := t.TempDir()
	stageID := "solo"
	if err := os.MkdirAll(filepath.Join(runDir, stageID), 0755); err != nil {
		t.Fatal(err)
	}

	// 2500 parseable assistant tool_use lines in one phase log — well above the
	// per-stage cap (2000) AND above maxLinesPerLog (2400), so the read window
	// is exercised too. Each line is a valid agent action (executor.ParseToolAction).
	var buf []byte
	for i := 0; i < 2500; i++ {
		buf = append(buf, []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"echo `+strconv.Itoa(i)+`"}}]}}`+"\n")...)
	}
	if err := os.WriteFile(filepath.Join(runDir, stageID, "planning.jsonl"), buf, 0644); err != nil {
		t.Fatal(err)
	}

	srv := newTestServerForRunDir(t, runDir, []string{stageID})
	got := srv.reconstructEventHistory(stageID)
	if len(got) != 2000 {
		t.Fatalf("per-stage reconstruction: want exactly 2000 (cap), got %d — maxLinesPerLog must not bottleneck below the per-stage cap", len(got))
	}
}

// TestReconstructEventHistory_QAOnCompletedStageVisibleWithinCap proves a
// completed stage's dialog_question/dialog_answer notices remain visible in the
// per-stage feed when followed by many (but < 2000) later same-stage events.
// With the old 200 cap the two dialog notices, written FIRST, would be evicted
// from the per-stage ring by the trailing filler; the raised 2000 cap keeps them.
func TestReconstructEventHistory_QAOnCompletedStageVisibleWithinCap(t *testing.T) {
	runDir := t.TempDir()
	stageID := "chat"
	if err := os.MkdirAll(filepath.Join(runDir, stageID), 0755); err != nil {
		t.Fatal(err)
	}

	// Q&A written FIRST, then 1500 later same-stage filler notices (< 2000).
	stagefiles.AppendNotice(runDir, stageID, string(bus.EventDialogQuestion), map[string]any{
		"phase": "planning", "id": "q1", "title": "Which approach?",
	})
	stagefiles.AppendNotice(runDir, stageID, string(bus.EventDialogAnswer), map[string]any{
		"phase": "planning", "id": "q1", "title": "Option B",
	})
	writeNotices(t, runDir, stageID, 1500)

	srv := newTestServerForRunDir(t, runDir, []string{stageID})
	// Mark the stage done — the scenario is Q&A on an already-completed stage.
	if err := srv.store.Apply(&state.Transition{
		StageID: stageID, From: state.StatusPending, To: state.StatusDone, Event: "done",
	}); err != nil {
		t.Fatal(err)
	}

	got := srv.reconstructEventHistory(stageID)
	var sawQuestion, sawAnswer bool
	for _, e := range got {
		switch e.Type {
		case string(bus.EventDialogQuestion):
			sawQuestion = true
		case string(bus.EventDialogAnswer):
			sawAnswer = true
		default:
		}
	}
	if !sawQuestion {
		t.Error("dialog_question evicted: Q&A on a completed stage must stay visible within the 2000 cap")
	}
	if !sawAnswer {
		t.Error("dialog_answer evicted: Q&A on a completed stage must stay visible within the 2000 cap")
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
