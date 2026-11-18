package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// pcStore is a run with stage "s" parked in a pre_completion round of one required helper "h", which answered the
// round's request with verdict.
func pcStore(t *testing.T, verdict assistants.Verdict) *state.Store {
	t.Helper()
	store, err := state.Open(t.TempDir(), []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-1"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: testStageID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	op := func(id string, effects ...state.Effect) {
		t.Helper()
		if _, err := store.ApplyRunOperation(state.Operation{ID: id, Kind: "test", Effects: effects}, nil); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	effect := func(kind string, payload any) state.Effect {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return state.Effect{Kind: kind, Target: state.Target{TaskID: "h", StageID: testStageID}, Payload: raw}
	}
	op("attach",
		effect("task_created", state.TaskCreatedPayload{ProfileID: "p", Mode: "required", Trigger: "pre_completion", Scope: "stage"}),
		effect("task_status_changed", state.TaskStatusChangedPayload{Status: "watching"}))
	if err := store.Apply(&state.Transition{
		StageID: testStageID, From: state.StatusRunning, To: state.StatusAwaitingPreCompletion, Event: "await_pre_completion",
		PreCompletion: &state.PreCompletionStep{Phase: "implementation"},
	}); err != nil {
		t.Fatal(err)
	}
	if verdict == "" {
		return store
	}
	req := string(state.PreCompletionRequestID(store.Snapshot().Stages[testStageID].PreCompletion.RoundSeq))
	op("start", effect("task_status_changed", state.TaskStatusChangedPayload{Status: "running", Turn: 1, RequestID: req}))
	op("answer",
		effect("task_result", state.TaskResultPayload{Result: assistants.Result{Verdict: verdict, Summary: "s"}, RequestID: req, Turn: 1}),
		effect("task_status_changed", state.TaskStatusChangedPayload{Status: "watching", Turn: 1, RequestID: req, RequestOutcome: state.RequestOutcomeResult}))
	return store
}

func TestStageView_ShowsThePreCompletionRoundAndWhenItNeedsAHuman(t *testing.T) {
	for _, tc := range []struct {
		name        string
		verdict     assistants.Verdict
		wantBlocked bool
	}{
		{"helper still working", "", false},
		{"helper passed", assistants.VerdictPass, false},
		{"helper inconclusive", assistants.VerdictInconclusive, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := pcStore(t, tc.verdict)
			views := buildStageViews(store.Snapshot(), t.TempDir(), map[string]StageConfig{testStageID: {PreCompletionMaxFailures: 2}}, nil)
			view := views[0]
			if view.Status != state.StatusAwaitingPreCompletion || view.PreCompletionBlocked != tc.wantBlocked {
				t.Fatalf("status %s, blocked %v; want awaiting_pre_completion, blocked %v", view.Status, view.PreCompletionBlocked, tc.wantBlocked)
			}
			if want := (PreCompletionView{Round: 1, Corrections: 0, MaxFailures: 2, Decision: ""}); view.PreCompletion == nil || *view.PreCompletion != want {
				t.Fatalf("pre_completion = %+v, want %+v", view.PreCompletion, want)
			}
		})
	}
}

func TestStageView_PreCompletionWireNames(t *testing.T) {
	store := pcStore(t, assistants.VerdictInconclusive)
	view := buildStageViews(store.Snapshot(), t.TempDir(), map[string]StageConfig{testStageID: {PreCompletionMaxFailures: 1}}, nil)[0]
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"pre_completion":{"round":1,"corrections":0,"max_failures":1,"decision":""}`, `"pre_completion_blocked":true`, `"status":"awaiting_pre_completion"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("view JSON lacks %s:\n%s", want, raw)
		}
	}
	idle := buildStageViews(*state.NewRunState([]string{testStageID}), t.TempDir(), map[string]StageConfig{testStageID: {}}, nil)[0]
	raw, _ = json.Marshal(idle)
	if strings.Contains(string(raw), "pre_completion") {
		t.Fatalf("a stage without a round must not carry the fields:\n%s", raw)
	}
}

func TestStageActions_AcceptAwaitingPreCompletion(t *testing.T) {
	var revised, paused bool
	store := pcStore(t, "")
	srv := New(Config{
		Port: 0, RunDir: t.TempDir(), Store: store, UIBus: bus.NewUIBus(),
		Actions: fakeStageActions{
			revise: func(context.Context, string, string) (bool, uint64, error) { revised = true; return true, 1, nil },
			pause:  func(context.Context, string) error { paused = true; return nil },
		},
	})
	body, _ := json.Marshal(map[string]string{"feedback": "also cover the empty case"})
	for _, tc := range []struct {
		path string
		body []byte
	}{{"/revise", body}, {"/pause", nil}} {
		req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+tc.path, bytes.NewReader(tc.body))
		req.Header.Set("Origin", "http://"+req.Host)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: HTTP %d, want 200 (body=%s)", tc.path, w.Code, w.Body.String())
		}
	}
	if !revised || !paused {
		t.Fatalf("revised=%v paused=%v, want both reached the orchestrator", revised, paused)
	}
}

func TestAttach_PreCompletionRefusalIs409WithItsReason(t *testing.T) {
	const reason = "stage \"s\" is in pre-completion review round 1 — attach the helper after the round ends"
	fake := &fakeAssistantActions{attachErr: &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: reason}}
	srv := newTestServer(t, Config{AssistantActions: fake})
	body, _ := json.Marshal(map[string]any{"request_id": "req-1", "profile_id": "reviewer", "trigger": "pre_completion"})
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/assistants", bytes.NewReader(body))
	req.Header.Set("Origin", "http://"+req.Host)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("HTTP %d, want 409 (body=%s)", w.Code, w.Body.String())
	}
	if fake.lastAttach.Trigger != "pre_completion" {
		t.Fatalf("Trigger = %q, want pre_completion passed to the service", fake.lastAttach.Trigger)
	}
	if !strings.Contains(w.Body.String(), "invalid_status") || !strings.Contains(w.Body.String(), "pre-completion review round 1") {
		t.Fatalf("body = %s, want the code and the round named", w.Body.String())
	}
}
