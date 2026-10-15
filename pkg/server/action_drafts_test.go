package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/akopichin/afm/pkg/sideagent"
)

// turnCompletedEvent builds a durable turn_completed event carrying the given action drafts — the only way
// handleConfirmActionDraft is allowed to learn a draft's content (never the request body).
func turnCompletedEvent(t *testing.T, turnID string, seq uint64, drafts []sideagent.ActionDraft) sideagent.Event {
	t.Helper()
	e := sideagent.Event{Type: sideagent.EventTurnCompleted, TurnID: turnID, Seq: seq}
	if err := e.SetData(sideagent.TurnCompletedData{ActionDrafts: drafts}); err != nil {
		t.Fatalf("SetData: %v", err)
	}
	return e
}

func attachDraft(stageID, profileID, title, instruction, expectedRevision string) sideagent.ActionDraft {
	target, _ := json.Marshal(map[string]string{"stage_id": stageID, "profile_id": profileID})
	payload, _ := json.Marshal(map[string]string{"title": title, "instruction": instruction})
	return sideagent.ActionDraft{
		Type: "attach_assistant", Target: target, Payload: payload, ExpectedRevision: expectedRevision,
	}
}

// aa is the AssistantActions INTERFACE, not *fakeAssistantActions — passing a nil *fakeAssistantActions
// through a concrete-typed parameter would produce a non-nil interface wrapping a nil pointer (the classic Go
// typed-nil trap), defeating TestDraft_CapabilityAndPolicyChecked's nil-capability check entirely.
func newDraftConfirmServer(t *testing.T, sa *fakeSideAgent, aa AssistantActions) *Server {
	t.Helper()
	return newTestServer(t, Config{SideAgent: sa, AssistantActions: aa})
}

func postDraftConfirm(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/side-agent/drafts/confirm", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// TestDraft_ValidatedAgainstAllowedFields — a draft whose Target or Payload carries a field outside the
// attach_assistant schema must be rejected, not silently ignored (the extra field could be an attempt to
// smuggle something the schema never sanctioned).
func TestDraft_ValidatedAgainstAllowedFields(t *testing.T) {
	target, _ := json.Marshal(map[string]string{"stage_id": "impl", "profile_id": "qaEngineer", "unexpected": "x"})
	payload, _ := json.Marshal(map[string]string{"title": "t", "instruction": "i"})
	draft := sideagent.ActionDraft{Type: "attach_assistant", Target: target, Payload: payload, ExpectedRevision: "rev-1"}

	sa := &fakeSideAgent{history: []sideagent.Event{turnCompletedEvent(t, "turn-1", 1, []sideagent.ActionDraft{draft})}}
	aa := &fakeAssistantActions{policyRevision: "rev-1"}
	s := newDraftConfirmServer(t, sa, aa)

	rec := postDraftConfirm(t, s, `{"turn_id":"turn-1","index":0}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
	}
	if aa.attachCalls != 0 {
		t.Fatalf("Attach called %d times, want 0 — validation must reject before executing", aa.attachCalls)
	}
}

// TestDraft_CapabilityAndPolicyChecked — a disabled capability (AssistantActions == nil, same convention as
// every other assistants write endpoint) must 404 even on a direct, well-formed, same-origin POST — bypassing
// a hidden confirm button client-side must not be able to execute anything.
func TestDraft_CapabilityAndPolicyChecked(t *testing.T) {
	draft := attachDraft("impl", "qaEngineer", "t", "i", "rev-1")
	sa := &fakeSideAgent{history: []sideagent.Event{turnCompletedEvent(t, "turn-1", 1, []sideagent.ActionDraft{draft})}}
	s := newDraftConfirmServer(t, sa, nil)

	rec := postDraftConfirm(t, s, `{"turn_id":"turn-1","index":0}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (capability off); body=%s", rec.Code, rec.Body.String())
	}
}

// TestDraft_ExpectedRevisionMismatch — the draft's ExpectedRevision no longer matches the CURRENT policy
// revision: reject with stale_revision and a fresh preview, never silently apply against whatever the policy
// happens to be now.
func TestDraft_ExpectedRevisionMismatch(t *testing.T) {
	draft := attachDraft("impl", "qaEngineer", "t", "i", "rev-1")
	sa := &fakeSideAgent{history: []sideagent.Event{turnCompletedEvent(t, "turn-1", 1, []sideagent.ActionDraft{draft})}}
	aa := &fakeAssistantActions{policyRevision: "rev-2", maxDepth: 3}
	s := newDraftConfirmServer(t, sa, aa)

	rec := postDraftConfirm(t, s, `{"turn_id":"turn-1","index":0}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp["error"] != "stale_revision" {
		t.Fatalf("error = %v, want stale_revision", resp["error"])
	}
	if resp["current_revision"] != "rev-2" {
		t.Fatalf("current_revision = %v, want rev-2 (a fresh preview)", resp["current_revision"])
	}
	if aa.attachCalls != 0 {
		t.Fatalf("Attach called %d times, want 0 — a stale draft must never be applied", aa.attachCalls)
	}
}

// TestDraft_ExecutesThroughSingleOperationService — a valid, non-stale draft executes through the EXACT same
// AssistantActions.Attach path manual confirmation already uses — there is no second mutation path.
func TestDraft_ExecutesThroughSingleOperationService(t *testing.T) {
	draft := attachDraft("impl", "qaEngineer", "Look at this", "please review", "rev-1")
	sa := &fakeSideAgent{history: []sideagent.Event{turnCompletedEvent(t, "turn-1", 1, []sideagent.ActionDraft{draft})}}
	aa := &fakeAssistantActions{policyRevision: "rev-1"}
	s := newDraftConfirmServer(t, sa, aa)

	rec := postDraftConfirm(t, s, `{"turn_id":"turn-1","index":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if aa.attachCalls != 1 {
		t.Fatalf("Attach called %d times, want exactly 1", aa.attachCalls)
	}
	if aa.lastStage != "impl" || aa.lastAttach.ProfileID != "qaEngineer" ||
		aa.lastAttach.Title != "Look at this" || aa.lastAttach.Instruction != "please review" {
		t.Fatalf("Attach called with %+v / stage=%q, want the draft's own fields", aa.lastAttach, aa.lastStage)
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp["task_id"] == "" {
		t.Fatal("response missing task_id")
	}

	// A second confirm of the SAME draft must not create a second task (idempotent operation id derived from
	// turn_id+index, mirroring Attach's own request_id-based idempotency).
	rec2 := postDraftConfirm(t, s, `{"turn_id":"turn-1","index":0}`)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second confirm status = %d, want 200; body=%s", rec2.Code, rec2.Body.String())
	}
	if aa.attachCalls != 2 {
		t.Fatalf("Attach called %d times after a repeat confirm, want 2 (re-entering Attach, but idempotently)", aa.attachCalls)
	}
	var resp2 map[string]string
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	if resp2["task_id"] != resp["task_id"] {
		t.Fatalf("repeat confirm task_id = %q, want the SAME task_id %q (idempotent)", resp2["task_id"], resp["task_id"])
	}
}

// TestDraft_ActorScopeEnforced — same-origin alone is not authorization: the confirm request body has no
// field for the client to assert its own actor/scope, and any attempt to smuggle one in is rejected by strict
// decoding rather than silently ignored. Real authorization (capability/policy/depth) is Attach's own job,
// exercised separately above.
func TestDraft_ActorScopeEnforced(t *testing.T) {
	draft := attachDraft("impl", "qaEngineer", "t", "i", "rev-1")
	sa := &fakeSideAgent{history: []sideagent.Event{turnCompletedEvent(t, "turn-1", 1, []sideagent.ActionDraft{draft})}}
	aa := &fakeAssistantActions{policyRevision: "rev-1"}
	s := newDraftConfirmServer(t, sa, aa)

	rec := postDraftConfirm(t, s, `{"turn_id":"turn-1","index":0,"actor":{"kind":"admin"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (unknown field 'actor' must be rejected); body=%s", rec.Code, rec.Body.String())
	}
	if aa.attachCalls != 0 {
		t.Fatalf("Attach called %d times, want 0", aa.attachCalls)
	}
}

// TestDraft_UnknownOrMissingDraft404s — a confirm for a turn/index that does not exist in history must 404,
// not panic or silently no-op with a 200.
func TestDraft_UnknownOrMissingDraft404s(t *testing.T) {
	sa := &fakeSideAgent{history: []sideagent.Event{turnCompletedEvent(t, "turn-1", 1, nil)}}
	aa := &fakeAssistantActions{policyRevision: "rev-1"}
	s := newDraftConfirmServer(t, sa, aa)

	for _, body := range []string{`{"turn_id":"turn-1","index":0}`, `{"turn_id":"unknown-turn","index":0}`} {
		rec := postDraftConfirm(t, s, body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("body=%s: status = %d, want 404", body, rec.Code)
		}
	}
}
