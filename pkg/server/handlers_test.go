package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

const testStageID = "s1"
const testQuestionID = "q1"

func setupTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	return setupTestServerWithWS(t, 0, 0)
}

// setupTestServerWithWS — как setupTestServer, но с явными keepalive-таймаутами
// вебсокета (нужны websocket-тестам; 0 → дефолты из websocket.go).
func setupTestServerWithWS(t *testing.T, pongWait, pingPeriod time.Duration) (*Server, string) {
	t.Helper()
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# Test Plan"), 0644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "planning.log"), []byte("test log line\n"), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: testStageID, From: state.StatusPending, To: state.StatusAwaitingApproval, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	uiBus := bus.NewUIBus()
	srv := New(Config{
		Port:         0,
		RunDir:       runDir,
		Store:        store,
		UIBus:        uiBus,
		Actions:      fakeStageActions{},
		WSPongWait:   pongWait,
		WSPingPeriod: pingPeriod,
	})
	return srv, runDir
}

// fakeStageActions is the StageActions test double — every method is backed by an overridable func field;
// the zero value succeeds and does nothing, matching what most handler tests need.
type fakeStageActions struct {
	approve   func(ctx context.Context, stageID string) error
	revise    func(ctx context.Context, stageID, feedback string) (applied bool, seq uint64, err error)
	retry     func(ctx context.Context, stageID string) error
	pause     func(ctx context.Context, stageID string) error
	continue_ func(ctx context.Context, stageID string) error
	button    func(ctx context.Context, stageID, name string) error
}

func (f fakeStageActions) Approve(ctx context.Context, stageID string) error {
	if f.approve == nil {
		return nil
	}
	return f.approve(ctx, stageID)
}

func (f fakeStageActions) Revise(ctx context.Context, stageID, feedback string) (bool, uint64, error) {
	if f.revise == nil {
		return true, 1, nil
	}
	return f.revise(ctx, stageID, feedback)
}

func (f fakeStageActions) Retry(ctx context.Context, stageID string) error {
	if f.retry == nil {
		return nil
	}
	return f.retry(ctx, stageID)
}

func (f fakeStageActions) Pause(ctx context.Context, stageID string) error {
	if f.pause == nil {
		return nil
	}
	return f.pause(ctx, stageID)
}

func (f fakeStageActions) Continue(ctx context.Context, stageID string) error {
	if f.continue_ == nil {
		return nil
	}
	return f.continue_(ctx, stageID)
}

func (f fakeStageActions) Button(ctx context.Context, stageID, name string) error {
	if f.button == nil {
		return nil
	}
	return f.button(ctx, stageID, name)
}

// fakeSecondaryActions is the SecondaryActions test double.
type fakeSecondaryActions struct {
	retryHook    func(stageID string) error
	skipHook     func(stageID string) error
	notifyAnswer func(stageID, phase, qID, answer string, fromOptions bool) error
	cancelDialog func(stageID string) error
}

func (f fakeSecondaryActions) RetryHook(stageID string) error {
	if f.retryHook == nil {
		return nil
	}
	return f.retryHook(stageID)
}

func (f fakeSecondaryActions) SkipHook(stageID string) error {
	if f.skipHook == nil {
		return nil
	}
	return f.skipHook(stageID)
}

func (f fakeSecondaryActions) NotifyAnswer(stageID, phase, qID, answer string, fromOptions bool) error {
	if f.notifyAnswer == nil {
		return nil
	}
	return f.notifyAnswer(stageID, phase, qID, answer, fromOptions)
}

func (f fakeSecondaryActions) CancelDialog(stageID string) error {
	if f.cancelDialog == nil {
		return nil
	}
	return f.cancelDialog(stageID)
}

func TestHandleStatus(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	var resp statusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, sv := range resp.Stages {
		if sv.ID == testStageID {
			found = true
		}
	}
	if !found {
		t.Error("stage s1 missing from status")
	}
}

func TestHandleStatus_IncludesStageNames(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.store.SetStageNames(map[string]string{testStageID: "Backend Stage"})

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var resp statusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var got string
	for _, sv := range resp.Stages {
		if sv.ID == testStageID {
			got = sv.Name
		}
	}
	if got != "Backend Stage" {
		t.Errorf("stage name = %q, want %q", got, "Backend Stage")
	}
}

func TestHandleStatus_IncludesFlowName(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.store.SetFlowName("demo-flow")

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var resp statusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.FlowName != "demo-flow" {
		t.Errorf("flow_name = %q, want %q", resp.FlowName, "demo-flow")
	}
}

// hostTimeZoneResponse декодирует только wire-поля зоны, фиксируя их имена в JSON.
type hostTimeZoneResponse struct {
	TimeZone         string `json:"time_zone"`
	UTCOffsetMinutes int    `json:"utc_offset_minutes"`
}

func TestHandleStatus_HostTimeZoneNativeWinsOverTransport(t *testing.T) {
	// Нативный хост: transport-env мог остаться в окружении, но без re-exec
	// в контейнер он игнорируется — побеждает собственная зона хоста.
	t.Setenv("AFM_IN_DOCKER", "")
	t.Setenv("TZ", "Asia/Tokyo")
	t.Setenv("AFM_HOST_TIME_ZONE", "Europe/Berlin")
	t.Setenv("AFM_HOST_UTC_OFFSET_MINUTES", "120")
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var resp hostTimeZoneResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TimeZone != "Asia/Tokyo" {
		t.Errorf("time_zone = %q, want %q", resp.TimeZone, "Asia/Tokyo")
	}
	if resp.UTCOffsetMinutes != 540 {
		t.Errorf("utc_offset_minutes = %d, want 540", resp.UTCOffsetMinutes)
	}
}

func TestHandleStatus_HostTimeZoneFromTransportInContainer(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "1")
	t.Setenv("TZ", "Asia/Tokyo") // зона контейнера не должна подменять зону хоста
	t.Setenv("AFM_HOST_TIME_ZONE", "Europe/Berlin")
	t.Setenv("AFM_HOST_UTC_OFFSET_MINUTES", "120")
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var resp hostTimeZoneResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TimeZone != "Europe/Berlin" {
		t.Errorf("time_zone = %q, want %q", resp.TimeZone, "Europe/Berlin")
	}
	if resp.UTCOffsetMinutes != 120 {
		t.Errorf("utc_offset_minutes = %d, want 120", resp.UTCOffsetMinutes)
	}
}

func TestHandleStatus_MissingHostTimeZoneTransportUsesLocalHost(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "1")
	t.Setenv("TZ", "Asia/Tokyo")
	for _, key := range []string{"AFM_HOST_TIME_ZONE", "AFM_HOST_UTC_OFFSET_MINUTES"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	srv, _ := setupTestServer(t)

	w := httptest.NewRecorder()
	srv.handleStatus(w, httptest.NewRequest("GET", "/api/status", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var resp hostTimeZoneResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.TimeZone != "Asia/Tokyo" || resp.UTCOffsetMinutes != 540 {
		t.Errorf("host time zone = %+v, want Asia/Tokyo +540", resp)
	}
}

func TestHandleStatus_IncludesFlowPauseState(t *testing.T) {
	srv := newTestServer(t, Config{
		ReviewState: func() (string, []string) { return "paused", []string{"s1"} },
	})

	resp := decodeStatus(t, srv)
	if resp.FlowPauseState != "paused" {
		t.Errorf("flow_pause_state = %q, want %q", resp.FlowPauseState, "paused")
	}
	if len(resp.FlowPausedStages) != 1 || resp.FlowPausedStages[0] != "s1" {
		t.Errorf("flow_paused_stages = %v, want [s1]", resp.FlowPausedStages)
	}
}

func TestHandleStatus_FlowPauseStateDefaultsToNone(t *testing.T) {
	srv := newTestServer(t, Config{})

	resp := decodeStatus(t, srv)
	if resp.FlowPauseState != "none" {
		t.Errorf("flow_pause_state = %q, want %q", resp.FlowPauseState, "none")
	}
	if len(resp.FlowPausedStages) != 0 {
		t.Errorf("flow_paused_stages = %v, want empty", resp.FlowPausedStages)
	}
}

func TestHandleStatus_IncludesInteractiveAndAutonomous(t *testing.T) {
	srv, runDir := setupTestServer(t)
	srv.stages = StaticStageConfigs{testStageID: {Interactive: true}}
	// пометить стадию автономной
	if err := os.WriteFile(filepath.Join(runDir, testStageID, "autonomous.flag"), nil, 0644); err != nil {
		t.Fatalf("write flag: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var resp statusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found *StageView
	for i := range resp.Stages {
		if resp.Stages[i].ID == testStageID {
			found = &resp.Stages[i]
		}
	}
	if found == nil {
		t.Fatal("stage missing from status")
	}
	if !found.Interactive {
		t.Error("Interactive = false, want true")
	}
	if !found.Autonomous {
		t.Error("Autonomous = false, want true")
	}
}

func TestHandleStatus_IncludesHasDialog(t *testing.T) {
	srv, runDir := setupTestServer(t)
	if err := os.WriteFile(filepath.Join(runDir, testStageID, "implementation.dialog.jsonl"), []byte(`{"id":"q1","question":"x?"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	var resp statusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found *StageView
	for i := range resp.Stages {
		if resp.Stages[i].ID == testStageID {
			found = &resp.Stages[i]
		}
	}
	if found == nil {
		t.Fatal("stage missing from status")
	}
	if !found.HasDialog {
		t.Error("HasDialog = false, want true")
	}
}

func TestHandleStatus_HasDialogFalseWhenNoDialogFile(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	var resp statusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found *StageView
	for i := range resp.Stages {
		if resp.Stages[i].ID == testStageID {
			found = &resp.Stages[i]
		}
	}
	if found == nil {
		t.Fatal("stage missing from status")
	}
	if found.HasDialog {
		t.Error("HasDialog = true, want false (no dialog.jsonl written)")
	}
}

func TestHandleStatus_IncludesAutoApprove(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.stages = StaticStageConfigs{testStageID: {AutoApprove: true}}

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var resp statusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found *StageView
	for i := range resp.Stages {
		if resp.Stages[i].ID == testStageID {
			found = &resp.Stages[i]
		}
	}
	if found == nil {
		t.Fatal("stage missing from status")
	}
	if !found.AutoApprove {
		t.Error("AutoApprove = false, want true")
	}
}

func TestHandleStatus_IncludesDescription(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.Description = "Мой флоу для тестов"

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var resp statusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Description != "Мой флоу для тестов" {
		t.Errorf("description = %q, want %q", resp.Description, "Мой флоу для тестов")
	}
}

func TestHandlePlan(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/stages/"+testStageID+"/plan", nil)
	w := httptest.NewRecorder()
	srv.handlePlan(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "# Test Plan") {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

func TestHandleApprove(t *testing.T) {
	approved := ""
	srv, _ := setupTestServer(t)
	srv.actions = fakeStageActions{approve: func(ctx context.Context, id string) error { approved = id; return nil }}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/approve", nil)
	w := httptest.NewRecorder()
	srv.handleApprove(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if approved != testStageID {
		t.Errorf("approve not called with s1, got %q", approved)
	}
}

func TestHandleRevise(t *testing.T) {
	var revisedID, revisedFB string
	srv, _ := setupTestServer(t)
	srv.actions = fakeStageActions{revise: func(ctx context.Context, id, fb string) (bool, uint64, error) {
		revisedID = id
		revisedFB = fb
		return true, 1, nil
	}}

	body := `{"feedback":"Добавь Redis"}`
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/revise", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleRevise(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if revisedID != testStageID || revisedFB != "Добавь Redis" {
		t.Errorf("revise: id=%q fb=%q", revisedID, revisedFB)
	}
}

// An author waiting for its helpers (awaiting_assistants) has finished its turn: a note reaches it only as a
// Revise, which the orchestrator accepts in that status — so the HTTP layer must not refuse it first.
func TestHandleRevise_AwaitingAssistants(t *testing.T) {
	srv, _ := setupPendingServer(t, false)
	if err := srv.store.Apply(&state.Transition{StageID: testStageID, From: state.StatusPending, To: state.StatusAwaitingAssistants, Event: "main_done"}); err != nil {
		t.Fatal(err)
	}
	revised := ""
	srv.actions = fakeStageActions{revise: func(ctx context.Context, id, fb string) (bool, uint64, error) {
		revised = id
		return true, 1, nil
	}}
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/revise", strings.NewReader(`{"feedback":"also cover the edge case"}`))
	w := httptest.NewRecorder()
	srv.handleRevise(w, req)
	if w.Code != http.StatusOK || revised != testStageID {
		t.Fatalf("status %d (body %q), revised %q: want the Revise delivered", w.Code, w.Body.String(), revised)
	}
}

// setupPendingServer builds a server whose single stage stays pending (no
// transition applied), for pre-note handler tests. isScript configures the stage type.
func setupPendingServer(t *testing.T, isScript bool) (*Server, string) {
	t.Helper()
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	srv := New(Config{
		Port:    0,
		RunDir:  runDir,
		Store:   store,
		UIBus:   bus.NewUIBus(),
		Actions: fakeStageActions{},
		Stages:  StaticStageConfigs{testStageID: {IsScript: isScript}},
	})
	return srv, runDir
}

func TestHandleStageNote_SaveAndClear(t *testing.T) {
	srv, runDir := setupPendingServer(t, false)
	notePath := filepath.Join(runDir, testStageID, "prenote.md")

	// Прикрепляем заметку.
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/note", strings.NewReader(`{"note":"Учти лимиты API"}`))
	w := httptest.NewRecorder()
	srv.handleStageNote(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("save: got %d, want 200 (%s)", w.Code, w.Body.String())
	}
	if data, _ := os.ReadFile(notePath); string(data) != "Учти лимиты API" {
		t.Errorf("prenote.md = %q", string(data))
	}

	// /api/status отдаёт pre_note.
	sreq := httptest.NewRequest("GET", "/api/status", nil)
	sw := httptest.NewRecorder()
	srv.handleStatus(sw, sreq)
	if !strings.Contains(sw.Body.String(), "Учти лимиты API") {
		t.Errorf("pre_note missing from /api/status: %s", sw.Body.String())
	}

	// Пустой текст = удалить.
	req = httptest.NewRequest("POST", "/api/stages/"+testStageID+"/note", strings.NewReader(`{"note":""}`))
	w = httptest.NewRecorder()
	srv.handleStageNote(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("clear: got %d, want 200", w.Code)
	}
	if _, err := os.Stat(notePath); !os.IsNotExist(err) {
		t.Errorf("prenote.md should be deleted, stat err=%v", err)
	}
}

func TestHandleStageNote_RejectsNonPending(t *testing.T) {
	srv, _ := setupTestServer(t) // стадия в awaiting_approval
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/note", strings.NewReader(`{"note":"x"}`))
	w := httptest.NewRecorder()
	srv.handleStageNote(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("non-pending: got %d, want 400", w.Code)
	}
}

func TestHandleStageNote_RejectsScriptStage(t *testing.T) {
	srv, _ := setupPendingServer(t, true)
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/note", strings.NewReader(`{"note":"x"}`))
	w := httptest.NewRecorder()
	srv.handleStageNote(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("script stage: got %d, want 400", w.Code)
	}
}

func TestHandleStageButton_Success(t *testing.T) {
	var gotStage, gotName string
	srv, _ := setupTestServer(t) // seeded at awaiting_approval — allowed
	srv.stages = StaticStageConfigs{testStageID: {Buttons: []string{"Run linter"}}}
	srv.actions = fakeStageActions{button: func(_ context.Context, id, name string) error {
		gotStage, gotName = id, name
		return nil
	}}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/button", strings.NewReader(`{"name":"Run linter"}`))
	w := httptest.NewRecorder()
	srv.handleStageButton(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (%s)", w.Code, w.Body.String())
	}
	if gotStage != testStageID || gotName != "Run linter" {
		t.Errorf("Button called with (%q, %q), want (%q, %q)", gotStage, gotName, testStageID, "Run linter")
	}
}

// A button whose Revise changed nothing (a Pause won the race) is a conflict, not a silent success.
func TestHandleStageButton_NotAppliedIsConflict(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.stages = StaticStageConfigs{testStageID: {Buttons: []string{"Run linter"}}}
	srv.actions = fakeStageActions{button: func(context.Context, string, string) error { return orchestrator.ErrNotApplied }}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/button", strings.NewReader(`{"name":"Run linter"}`))
	w := httptest.NewRecorder()
	srv.handleStageButton(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409 (%s)", w.Code, w.Body.String())
	}
}

func TestHandleStageButton_UnknownName(t *testing.T) {
	called := false
	srv, _ := setupTestServer(t)
	srv.stages = StaticStageConfigs{testStageID: {Buttons: []string{"Run linter"}}}
	srv.actions = fakeStageActions{button: func(_ context.Context, _, _ string) error {
		called = true
		return nil
	}}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/button", strings.NewReader(`{"name":"Nope"}`))
	w := httptest.NewRecorder()
	srv.handleStageButton(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
	if called {
		t.Error("Button must not be called for an unknown button name")
	}
}

func TestHandleStageButton_WrongStatus(t *testing.T) {
	srv, _ := setupTestServer(t)
	if err := srv.store.Apply(&state.Transition{StageID: testStageID, From: state.StatusAwaitingApproval, To: state.StatusFailed, Event: "test"}); err != nil {
		t.Fatal(err)
	}
	srv.stages = StaticStageConfigs{testStageID: {Buttons: []string{"Run linter"}}}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/button", strings.NewReader(`{"name":"Run linter"}`))
	w := httptest.NewRecorder()
	srv.handleStageButton(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 (failed stage has no live agent)", w.Code)
	}
}

func TestHandleStageButton_ScriptStage(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.stages = StaticStageConfigs{testStageID: {IsScript: true, Buttons: []string{"Run linter"}}}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/button", strings.NewReader(`{"name":"Run linter"}`))
	w := httptest.NewRecorder()
	srv.handleStageButton(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 (script stage has no agent)", w.Code)
	}
}

func TestHandleStageButton_MissingName(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.stages = StaticStageConfigs{testStageID: {Buttons: []string{"Run linter"}}}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/button", strings.NewReader(`{"name":""}`))
	w := httptest.NewRecorder()
	srv.handleStageButton(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 (empty name)", w.Code)
	}
}

func TestHandleStageButton_NonexistentStage(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest("POST", "/api/stages/ghost/button", strings.NewReader(`{"name":"Run linter"}`))
	w := httptest.NewRecorder()
	srv.handleStageButton(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404 (stage not found)", w.Code)
	}
}

func TestHandleRetry(t *testing.T) {
	var retriedID string
	srv, _ := setupTestServer(t)
	if err := srv.store.Apply(&state.Transition{StageID: testStageID, From: state.StatusAwaitingApproval, To: state.StatusFailed, Event: "test_fail"}); err != nil {
		t.Fatal(err)
	}
	srv.actions = fakeStageActions{retry: func(ctx context.Context, id string) error { retriedID = id; return nil }}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/retry", nil)
	w := httptest.NewRecorder()
	srv.handleRetry(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if retriedID != testStageID {
		t.Errorf("retry not called with s1, got %q", retriedID)
	}
}

func TestHandleRetryNotFailed(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.actions = fakeStageActions{retry: func(ctx context.Context, id string) error { return nil }}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/retry", nil)
	w := httptest.NewRecorder()
	srv.handleRetry(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-failed stage, got %d", w.Code)
	}
}

func TestHandlePause(t *testing.T) {
	var pausedID string
	srv, _ := setupTestServer(t)
	if err := srv.store.Apply(&state.Transition{StageID: testStageID, From: state.StatusAwaitingApproval, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	srv.actions = fakeStageActions{pause: func(ctx context.Context, id string) error { pausedID = id; return nil }}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/pause", nil)
	w := httptest.NewRecorder()
	srv.handlePause(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if pausedID != testStageID {
		t.Errorf("pause not called with %s, got %q", testStageID, pausedID)
	}
}

// A stage parked waiting for a colleague (awaiting_peer) can be paused: team reviews park there routinely.
func TestHandlePause_AwaitingPeer(t *testing.T) {
	var pausedID string
	srv, _ := setupTestServer(t)
	for _, tr := range []state.Transition{
		{StageID: testStageID, From: state.StatusAwaitingApproval, To: state.StatusRunning, Event: "test_setup"},
		{StageID: testStageID, From: state.StatusRunning, To: state.StatusAwaitingPeer, Event: "await_peer"},
	} {
		if err := srv.store.Apply(&tr); err != nil {
			t.Fatal(err)
		}
	}
	srv.actions = fakeStageActions{pause: func(ctx context.Context, id string) error { pausedID = id; return nil }}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/pause", nil)
	w := httptest.NewRecorder()
	srv.handlePause(w, req)

	if w.Code != http.StatusOK || pausedID != testStageID {
		t.Fatalf("pause of awaiting_peer: status %d, paused %q; want 200 and the stage paused", w.Code, pausedID)
	}
}

// A stage waiting for a provider usage limit (awaiting_quota) can be paused; Continue is only for paused.
func TestHandlePause_AwaitingQuota(t *testing.T) {
	var pausedID string
	srv, _ := setupTestServer(t)
	for _, tr := range []state.Transition{
		{StageID: testStageID, From: state.StatusAwaitingApproval, To: state.StatusRunning, Event: "test_setup"},
		{StageID: testStageID, From: state.StatusRunning, To: state.StatusAwaitingQuota, Event: "await_quota", Quota: &state.QuotaWait{Provider: "claude", Target: "implementation"}},
	} {
		if err := srv.store.Apply(&tr); err != nil {
			t.Fatal(err)
		}
	}
	srv.actions = fakeStageActions{pause: func(ctx context.Context, id string) error { pausedID = id; return nil }}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/pause", nil)
	w := httptest.NewRecorder()
	srv.handlePause(w, req)

	if w.Code != http.StatusOK || pausedID != testStageID {
		t.Fatalf("pause of awaiting_quota: status %d, paused %q; want 200 and the stage paused", w.Code, pausedID)
	}
}

func TestHandleContinue_AwaitingQuotaNeedsPauseFirst(t *testing.T) {
	continued := false
	srv, _ := setupTestServer(t)
	for _, tr := range []state.Transition{
		{StageID: testStageID, From: state.StatusAwaitingApproval, To: state.StatusRunning, Event: "test_setup"},
		{StageID: testStageID, From: state.StatusRunning, To: state.StatusAwaitingQuota, Event: "await_quota", Quota: &state.QuotaWait{Provider: "claude", Target: "implementation"}},
	} {
		if err := srv.store.Apply(&tr); err != nil {
			t.Fatal(err)
		}
	}
	srv.actions = fakeStageActions{continue_: func(context.Context, string) error { continued = true; return nil }}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/continue", nil)
	w := httptest.NewRecorder()
	srv.handleContinue(w, req)

	if w.Code == http.StatusOK || continued {
		t.Fatalf("direct Continue of awaiting_quota: status %d, continued=%v; want it refused (Pause first)", w.Code, continued)
	}

	// После Pause стадия paused (PausedFrom=awaiting_quota): Continue принимается — это ручная проба.
	if err := srv.store.Apply(&state.Transition{StageID: testStageID, From: state.StatusAwaitingQuota, To: state.StatusPaused, Event: "pause"}); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	srv.handleContinue(w, httptest.NewRequest("POST", "/api/stages/"+testStageID+"/continue", nil))
	if w.Code != http.StatusOK || !continued {
		t.Fatalf("Continue of a quota pause: status %d, continued=%v; want 200", w.Code, continued)
	}
}

func TestHandlePause_WrongStatus(t *testing.T) {
	srv, _ := setupTestServer(t) // seeded at awaiting_approval by setupTestServer itself
	srv.actions = fakeStageActions{}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/pause", nil)
	w := httptest.NewRecorder()
	srv.handlePause(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestHandlePause_ScriptStageRunning_Returns409(t *testing.T) {
	srv, _ := setupTestServer(t)
	if err := srv.store.Apply(&state.Transition{StageID: testStageID, From: state.StatusAwaitingApproval, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	srv.stages = StaticStageConfigs{testStageID: {IsScript: true}}
	srv.actions = fakeStageActions{}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/pause", nil)
	w := httptest.NewRecorder()
	srv.handlePause(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status: got %d, want 409", w.Code)
	}
}

func TestHandleContinue(t *testing.T) {
	var continuedID string
	srv, _ := setupTestServer(t)
	if err := srv.store.Apply(&state.Transition{StageID: testStageID, From: state.StatusAwaitingApproval, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	srv.actions = fakeStageActions{continue_: func(ctx context.Context, id string) error { continuedID = id; return nil }}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/continue", nil)
	w := httptest.NewRecorder()
	srv.handleContinue(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if continuedID != testStageID {
		t.Errorf("continue not called with %s, got %q", testStageID, continuedID)
	}
}

func TestHandleContinue_NotPaused(t *testing.T) {
	srv, _ := setupTestServer(t) // seeded at awaiting_approval, not paused
	srv.actions = fakeStageActions{}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/continue", nil)
	w := httptest.NewRecorder()
	srv.handleContinue(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestHandleRetryHook_Success(t *testing.T) {
	srv, _ := setupTestServer(t)
	called := ""
	srv.secondary = fakeSecondaryActions{retryHook: func(stageID string) error {
		called = stageID
		return nil
	}}

	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/retry-hook", nil)
	w := httptest.NewRecorder()
	srv.routeStages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if called != testStageID {
		t.Errorf("RetryHook called with %q, want %q", called, testStageID)
	}
}

func TestHandleSkipHook_Success(t *testing.T) {
	srv, _ := setupTestServer(t)
	called := ""
	srv.secondary = fakeSecondaryActions{skipHook: func(stageID string) error {
		called = stageID
		return nil
	}}

	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/skip-hook", nil)
	w := httptest.NewRecorder()
	srv.routeStages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if called != testStageID {
		t.Errorf("SkipHook called with %q, want %q", called, testStageID)
	}
}

func TestHandleRetryHook_FnReturnsError(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.secondary = fakeSecondaryActions{retryHook: func(stageID string) error {
		return fmt.Errorf("stage %q has no hook awaiting a decision", stageID)
	}}

	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/retry-hook", nil)
	w := httptest.NewRecorder()
	srv.routeStages(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", w.Code, w.Body.String())
	}
}

func TestHandleSkipHook_FnReturnsError(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.secondary = fakeSecondaryActions{skipHook: func(stageID string) error {
		return fmt.Errorf("stage %q has no hook awaiting a decision", stageID)
	}}

	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/skip-hook", nil)
	w := httptest.NewRecorder()
	srv.routeStages(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", w.Code, w.Body.String())
	}
}

func TestHandleRetryHook_NotConfigured(t *testing.T) {
	srv, _ := setupTestServer(t)
	// secondary intentionally left nil.

	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/retry-hook", nil)
	w := httptest.NewRecorder()
	srv.routeStages(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501, body = %s", w.Code, w.Body.String())
	}
}

func TestDialogGet(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	os.MkdirAll(stageDir, 0755)
	dialogPath := filepath.Join(stageDir, "implementation.dialog.jsonl")
	if err := mcp.AppendQuestion(dialogPath, mcp.Question{ID: testQuestionID, Question: "x?"}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{ID: testQuestionID, Answer: "yes"}); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	srv := New(Config{
		RunDir: runDir,
		Store:  store,
		UIBus:  bus.NewUIBus(),
	})

	req := httptest.NewRequest("GET", "/api/stages/"+testStageID+"/dialog", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status %d, body %s", w.Code, w.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 || got[0]["id"] != testQuestionID || got[0]["phase"] != "implementation" {
		t.Errorf("dialog entries wrong: %+v", got)
	}
}

func TestDialogGet_SkipsMalformedPendingQuestion(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	os.MkdirAll(stageDir, 0755)
	qPath := filepath.Join(stageDir, "implementation.q1.question.json")
	if err := os.WriteFile(qPath, []byte(`not json at all {{{`), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	srv := New(Config{
		RunDir: runDir,
		Store:  store,
		UIBus:  bus.NewUIBus(),
	})

	req := httptest.NewRequest("GET", "/api/stages/"+testStageID+"/dialog", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status %d, body %s", w.Code, w.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("malformed pending question must not be surfaced yet, got %+v", got)
	}
}

// TestDialogGetWithTranscript проверяет, что тексты агента из stream-json
// лога перемежают вопросы диалога в порядке появления.
func TestDialogGetWithTranscript(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	os.MkdirAll(stageDir, 0755)

	dialogPath := filepath.Join(stageDir, "planning.dialog.jsonl")
	if err := mcp.AppendQuestion(dialogPath, mcp.Question{ID: "q1", Question: "Дизайн ок?"}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{ID: "q1", Answer: "да"}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendQuestion(dialogPath, mcp.Question{ID: "q2", Question: "Утверждаем?"}); err != nil {
		t.Fatal(err)
	}

	jsonl := `{"type":"assistant","message":{"content":[{"type":"text","text":"## Дизайн расширения"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__afm__ask_user","input":{"id":"q1","question":"Дизайн ок?"}}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Финальный штрих."}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__afm__ask_user","input":{"id":"q2","question":"Утверждаем?"}}]}}
`
	if err := os.WriteFile(filepath.Join(stageDir, "planning.jsonl"), []byte(jsonl), 0644); err != nil {
		t.Fatal(err)
	}

	got := buildDialogEntries(stageDir, false)
	if len(got) != 4 {
		t.Fatalf("got %d entries, want 4: %+v", len(got), got)
	}
	if got[0].Type != typeAgentText || got[0].Text != "## Дизайн расширения" {
		t.Errorf("entry 0: %+v", got[0])
	}
	if got[1].ID != "q1" || got[1].Answer == nil || *got[1].Answer != "да" {
		t.Errorf("entry 1: %+v", got[1])
	}
	if got[2].Type != typeAgentText || got[2].Text != "Финальный штрих." {
		t.Errorf("entry 2: %+v", got[2])
	}
	if got[3].ID != "q2" || got[3].Answer != nil {
		t.Errorf("entry 3: %+v", got[3])
	}
}

// TestDialogGetNoDialogFile проверяет, что без диалога тексты агента
// не показываются (неинтерактивные стейджи не раздувают панель).
func TestDialogGetNoDialogFile(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	os.MkdirAll(stageDir, 0755)
	jsonl := `{"type":"assistant","message":{"content":[{"type":"text","text":"просто текст"}]}}
`
	if err := os.WriteFile(filepath.Join(stageDir, "planning.jsonl"), []byte(jsonl), 0644); err != nil {
		t.Fatal(err)
	}
	if got := buildDialogEntries(stageDir, false); len(got) != 0 {
		t.Errorf("expected no entries, got %+v", got)
	}
}

func TestBuildDialogEntries_SerializeShowsOnlyCurrent(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"q1", "q2", "q3"} {
		body := `{"id":"` + id + `","question":"Q ` + id + `","options":["A"],"allow_custom":true}`
		if err := os.WriteFile(filepath.Join(dir, "autonomous_execution."+id+".question.json"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	out := buildDialogEntries(dir, true)
	var unanswered []string
	for _, e := range out {
		if e.Type == typeAgentText || e.ID == "" || e.Answer != nil {
			continue
		}
		unanswered = append(unanswered, e.ID)
	}
	if len(unanswered) != 1 || unanswered[0] != "q1" {
		t.Fatalf("serialize=true: expected only q1 unanswered, got %v", unanswered)
	}
}

func TestBuildDialogEntries_NoSerializeShowsAll(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"q1", "q2"} {
		body := `{"id":"` + id + `","question":"Q ` + id + `","options":["A"],"allow_custom":true}`
		if err := os.WriteFile(filepath.Join(dir, "autonomous_execution."+id+".question.json"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	out := buildDialogEntries(dir, false)
	count := 0
	for _, e := range out {
		if e.Type != typeAgentText && e.ID != "" && e.Answer == nil {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("serialize=false: expected both questions, got %d", count)
	}
}

func TestDialogAnswer(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	os.MkdirAll(stageDir, 0755)

	dialogPath := filepath.Join(stageDir, "implementation.dialog.jsonl")
	if err := mcp.AppendQuestion(dialogPath, mcp.Question{ID: testQuestionID, Question: "test?"}); err != nil {
		t.Fatal(err)
	}
	// question.json written by the agent (file-based dialog protocol).
	qPath := filepath.Join(stageDir, "implementation."+testQuestionID+".question.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"test?"}`), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	called := struct {
		stage, phase, id, answer string
		fromOptions              bool
	}{}
	srv := New(Config{
		RunDir: runDir,
		Store:  store,
		UIBus:  bus.NewUIBus(),
		Secondary: fakeSecondaryActions{notifyAnswer: func(s, p, q, a string, fo bool) error {
			called.stage, called.phase, called.id, called.answer, called.fromOptions = s, p, q, a, fo
			return nil
		}},
	})

	body := `{"id":"` + testQuestionID + `","phase":"implementation","answer":"hello","from_options":false}`
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/dialog/answer", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d, body %s", w.Code, w.Body.String())
	}
	if called.stage != testStageID || called.id != testQuestionID || called.answer != "hello" {
		t.Errorf("answerFn called with wrong args: %+v", called)
	}

	// The answer file must be written so the agent's bash loop can pick it up.
	// Without this assertion the write could be silently dropped (regression).
	answerPath := filepath.Join(stageDir, "implementation."+testQuestionID+".answer.json")
	data, err := os.ReadFile(answerPath)
	if err != nil {
		t.Fatalf("answer.json not written: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("invalid answer.json: %v", err)
	}
	if got["answer"] != "hello" {
		t.Errorf("answer.json content mismatch: %v", got)
	}
}

func TestHandleDialogAnswer_InvalidID(t *testing.T) {
	srv, runDir := setupTestServer(t)
	stageDir := filepath.Join(runDir, testStageID)
	// A benign question file exists, but the attacker POSTs a traversal id.
	if err := os.WriteFile(filepath.Join(stageDir, "planning.q1.question.json"),
		[]byte(`{"id":"q1","question":"x?"}`), 0644); err != nil {
		t.Fatal(err)
	}

	for _, badID := range []string{"../../foo", "a/b", "..", ""} {
		body := `{"id":"` + badID + `","phase":"planning","answer":"yes"}`
		req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/dialog/answer",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("id=%q: want 400, got %d: %s", badID, w.Code, w.Body.String())
		}
	}
}

// TestDialogCancelAwaiting verifies the success path: a stage in
// awaiting_user_input is cancelled and CancelDialog is invoked.
func TestDialogCancelAwaiting(t *testing.T) {
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: testStageID, From: state.StatusPending, To: state.StatusAwaitingUserInput, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	cancelled := ""
	srv := New(Config{
		RunDir:    runDir,
		Store:     store,
		UIBus:     bus.NewUIBus(),
		Secondary: fakeSecondaryActions{cancelDialog: func(id string) error { cancelled = id; return nil }},
	})

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/dialog/cancel", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if cancelled != testStageID {
		t.Errorf("CancelDialog not called with %q, got %q", testStageID, cancelled)
	}
}

func TestDialogCancelRejectsNonAwaiting(t *testing.T) {
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: testStageID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{
		RunDir: runDir,
		Store:  store,
		UIBus:  bus.NewUIBus(),
	})

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/dialog/cancel", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for non-awaiting stage, got %d", w.Code)
	}
}

func TestHandleDialogAnswer_WritesAnswerFile(t *testing.T) {
	srv, runDir := setupTestServer(t)
	stageDir := filepath.Join(runDir, testStageID)

	// Write a question file (agent-side).
	qPath := filepath.Join(stageDir, "planning."+testQuestionID+".question.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"proceed?","options":["yes"],"allow_custom":false}`), 0644); err != nil {
		t.Fatal(err)
	}
	// Pre-populate dialog.jsonl so AppendAnswer has a matching question.
	dialogPath := filepath.Join(stageDir, "planning.dialog.jsonl")
	if err := mcp.AppendQuestion(dialogPath, mcp.Question{ID: testQuestionID, Question: "proceed?"}); err != nil {
		t.Fatal(err)
	}

	body := `{"id":"q1","phase":"planning","answer":"yes","from_options":true}`
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/dialog/answer",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	// answer.json must exist with correct content.
	answerPath := filepath.Join(stageDir, "planning."+testQuestionID+".answer.json")
	data, err := os.ReadFile(answerPath)
	if err != nil {
		t.Fatalf("answer.json not created: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("invalid JSON in answer.json: %v", err)
	}
	if got["answer"] != "yes" {
		t.Errorf("answer.json answer mismatch: %v", got)
	}
	if v, _ := got["from_options"].(bool); !v {
		t.Errorf("answer.json from_options mismatch: %v", got)
	}
}

func TestHandleDialogAnswer_AppendAnswerFailureStillNotifies(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, testStageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Agent-side question file exists.
	qPath := filepath.Join(stageDir, "planning."+testQuestionID+".question.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"proceed?"}`), 0644); err != nil {
		t.Fatal(err)
	}
	// Make AppendAnswer fail: planning.dialog.jsonl is a directory, so opening
	// it for writing returns an error.
	if err := os.MkdirAll(filepath.Join(stageDir, "planning.dialog.jsonl"), 0755); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	notifyCalled := false
	srv := New(Config{
		RunDir: runDir,
		Store:  store,
		UIBus:  bus.NewUIBus(),
		Secondary: fakeSecondaryActions{notifyAnswer: func(s, p, q, a string, fo bool) error {
			notifyCalled = true
			return nil
		}},
	})

	body := `{"id":"q1","phase":"planning","answer":"yes","from_options":false}`
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/dialog/answer",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("AppendAnswer failure must not fail the request: want 200, got %d: %s", w.Code, w.Body.String())
	}
	if !notifyCalled {
		t.Error("NotifyAnswer was not called after AppendAnswer failure")
	}
	answerPath := filepath.Join(stageDir, "planning."+testQuestionID+".answer.json")
	if _, err := os.Stat(answerPath); err != nil {
		t.Errorf("answer.json not written despite notify: %v", err)
	}
}

func TestHandleDialogGet_SurfacesAutoAnsweredFlag(t *testing.T) {
	srv, runDir := setupTestServer(t)
	stageDir := filepath.Join(runDir, testStageID)

	dialogPath := filepath.Join(stageDir, "implementation.dialog.jsonl")
	if err := mcp.AppendQuestion(dialogPath, mcp.Question{ID: "q1", Question: "which?", Options: []string{"A", "B"}}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{ID: "q1", Answer: "B", FromOptions: true, AutoAnswered: true}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/stages/"+testStageID+"/dialog", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	var got []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want auto_answered:true surfaced in GET /dialog, got %v", got)
	}
	if autoAnswered, _ := got[0]["auto_answered"].(bool); !autoAnswered {
		t.Fatalf("want auto_answered:true surfaced in GET /dialog, got %v", got)
	}
}

func TestHandleDialogAnswer_QuestionNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	body := `{"id":"nonexistent","phase":"planning","answer":"yes"}`
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/dialog/answer",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", w.Code)
	}
}

func TestHandleDialogAnswer_DuplicateAnswer(t *testing.T) {
	srv, runDir := setupTestServer(t)
	stageDir := filepath.Join(runDir, testStageID)

	// Create question.json + answer.json (already answered).
	qPath := filepath.Join(stageDir, "planning.q1.question.json")
	aPath := filepath.Join(stageDir, "planning.q1.answer.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"x?"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aPath, []byte(`{"id":"q1","answer":"yes"}`), 0644); err != nil {
		t.Fatal(err)
	}

	body := `{"id":"q1","phase":"planning","answer":"no"}`
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/dialog/answer",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("want 409, got %d", w.Code)
	}
}

func TestHandleRevise_RunningAllowed(t *testing.T) {
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

	var reviseCalled bool
	srv := New(Config{
		RunDir: runDir,
		Store:  store,
		UIBus:  bus.NewUIBus(),
		Actions: fakeStageActions{revise: func(_ context.Context, _, _ string) (bool, uint64, error) {
			reviseCalled = true
			return true, 1, nil
		}},
	})

	body, _ := json.Marshal(map[string]string{"feedback": "note"})
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/revise", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleRevise(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !reviseCalled {
		t.Error("Revise should be called for a running stage")
	}
}

func TestHandleStatus_IncludesIdleSinceWhenIdle(t *testing.T) {
	srv, _ := setupTestServer(t)
	// setupTestServer already moved s1 to awaiting_approval — that's a
	// QUESTION_STATUSES-equivalent, so the flow is idle right now.

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	var resp struct {
		IdleSince        *time.Time  `json:"idle_since"`
		BackoffOpenSince []time.Time `json:"backoff_open_since"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.IdleSince == nil {
		t.Error("idle_since = nil, want a timestamp (stage is awaiting_approval)")
	}
	if len(resp.BackoffOpenSince) != 0 {
		t.Errorf("backoff_open_since = %v, want empty (no stage retrying)", resp.BackoffOpenSince)
	}
}

func TestHandleStatus_ClosesCountersWhenRunEnds(t *testing.T) {
	srv, _ := setupTestServer(t)
	if err := srv.store.BeginRun(); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.EndRun(state.RunStatusFailed); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	srv.handleStatus(w, httptest.NewRequest("GET", "/api/status", nil))
	var resp struct {
		RunStatus            string     `json:"run_status"`
		EndedAt              *time.Time `json:"ended_at"`
		ElapsedAccumulatedMs int64      `json:"elapsed_accumulated_ms"`
		ElapsedSince         *time.Time `json:"elapsed_since"`
		IdleSince            *time.Time `json:"idle_since"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.RunStatus != string(state.RunStatusFailed) || resp.EndedAt == nil || resp.ElapsedSince != nil || resp.IdleSince != nil {
		t.Fatalf("terminal status = %+v", resp)
	}
}

func TestHandleStatus_IncludesBackoffOpenSinceWhenRetrying(t *testing.T) {
	srv, _ := setupTestServer(t)
	if err := srv.store.Apply(&state.Transition{StageID: testStageID, From: state.StatusAwaitingApproval, To: state.StatusRetrying, Event: "test"}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	var resp struct {
		BackoffOpenSince []time.Time `json:"backoff_open_since"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.BackoffOpenSince) != 1 {
		t.Fatalf("backoff_open_since len = %d, want 1", len(resp.BackoffOpenSince))
	}
}

// TestHandleStatus_CollaborationMode: collaboration_mode — независимое top-level поле статуса. Оно всегда есть
// в JSON (без omitempty), нулевой Config даёт "team", а значение не зависит от capabilities.collaboration.
func TestHandleStatus_CollaborationMode(t *testing.T) {
	cases := []struct {
		name       string
		cfg        Config
		wantMode   string
		wantCollab bool
	}{
		{"zero config defaults to team", Config{}, "team", false},
		{"explicit team", Config{CollaborationMode: assistants.CollaborationModeTeam}, "team", false},
		{"assistants without collaboration capability", Config{CollaborationMode: assistants.CollaborationModeAssistants}, "assistants", false},
		{"assistants with collaboration capability", Config{
			CollaborationMode: assistants.CollaborationModeAssistants,
			AssistantProfiles: fakeAssistantProfiles{maxDepthSource: "default"},
		}, "assistants", true},
		{"team with collaboration capability", Config{
			AssistantProfiles: fakeAssistantProfiles{maxDepthSource: "default"},
		}, "team", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t, tc.cfg)
			w := httptest.NewRecorder()
			srv.handleStatus(w, httptest.NewRequest("GET", "/api/status", nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status: got %d, want 200", w.Code)
			}

			var raw map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
				t.Fatalf("decode: %v", err)
			}
			rawMode, ok := raw["collaboration_mode"]
			if !ok {
				t.Fatalf("collaboration_mode key missing from JSON: %s", w.Body.String())
			}
			if string(rawMode) != `"`+tc.wantMode+`"` {
				t.Errorf("collaboration_mode = %s, want %q", rawMode, tc.wantMode)
			}

			var resp statusResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Capabilities.Collaboration != tc.wantCollab {
				t.Errorf("capabilities.collaboration = %v, want %v", resp.Capabilities.Collaboration, tc.wantCollab)
			}
		})
	}
}
