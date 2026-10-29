package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

const (
	testOwnerToken = "owner-token-0123456789abcdef"
	testAgentToken = "agent-token-for-stage-s1"
)

// fakeOwnerWaits records every call: an unauthenticated request must leave both counters at zero.
type fakeOwnerWaits struct {
	mu          sync.Mutex
	listCalls   int
	cancelCalls int
	lastStage   string
	lastReqID   string
	lastCancel  OwnerCancelWaitsRequest
	waits       []orchestrator.StageWaitView
	err         error
}

func (f *fakeOwnerWaits) StageWaits(_ context.Context, stageID string) ([]orchestrator.StageWaitView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	f.lastStage = stageID
	return f.waits, f.err
}

func (f *fakeOwnerWaits) CancelStageWaits(_ context.Context, requestID, stageID string, req OwnerCancelWaitsRequest) ([]orchestrator.StageWaitView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls++
	f.lastReqID, f.lastStage, f.lastCancel = requestID, stageID, req
	return f.waits, f.err
}

func (f *fakeOwnerWaits) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listCalls + f.cancelCalls
}

type ownerFixture struct {
	srv   *Server
	owner *fakeOwnerWaits
	mesh  *fakeMeshActions
	agent *fakeAgentMeshActions
}

func newOwnerFixture(t *testing.T) *ownerFixture {
	t.Helper()
	f := &ownerFixture{owner: &fakeOwnerWaits{}, mesh: newFakeMeshActions(), agent: newFakeAgentMeshActions()}
	f.agent.tokens[testAgentToken] = "stage:s1"
	f.owner.waits = []orchestrator.StageWaitView{{
		WaitID: "mw-1", Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "mm-1"}, Status: agentcomm.WaitPending,
	}}
	f.srv = newTestServer(t, Config{Mesh: f.mesh, AgentMesh: f.agent, OwnerWaits: f.owner, OwnerToken: testOwnerToken})
	return f
}

// do sends a request; headers are applied after the defaults so a test can drop or replace one.
func (f *ownerFixture) do(method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(w, req)
	return w
}

func ownerHeaders(w ...string) map[string]string {
	h := map[string]string{"Authorization": "Bearer " + testOwnerToken}
	for _, kv := range w {
		k, v, _ := strings.Cut(kv, "=")
		h[k] = v
	}
	return h
}

const (
	listPath   = "/api/mesh/stages/s1/waits"
	cancelPath = "/api/mesh/stages/s1/waits/cancel"
)

func cancelBody() map[string]any {
	return map[string]any{"request_id": "req-1", "wait_ids": []string{"mw-1"}, "reason": "stale after a callback"}
}

// Missing, malformed, wrong and agent credentials get 401 before the Service is touched; no other signal — an
// Origin, a loopback caller, the dashboard's mesh trust, a token in the URL or the body — stands in for it.
func TestOwnerWaits_UnauthenticatedCallersGet401WithoutTouchingTheService(t *testing.T) {
	cases := map[string]map[string]string{
		"no credentials":     nil,
		"empty bearer":       {"Authorization": "Bearer "},
		"wrong scheme":       {"Authorization": "Basic " + testOwnerToken},
		"bare token":         {"Authorization": testOwnerToken},
		"wrong token":        {"Authorization": "Bearer " + testOwnerToken + "x"},
		"prefix of token":    {"Authorization": "Bearer " + testOwnerToken[:10]},
		"agent mesh token":   {"Authorization": "Bearer " + testAgentToken},
		"same-origin only":   {"Origin": "http://example.com"},
		"owner token in url": nil, // the URL case adds the query below
	}
	for name, headers := range cases {
		for _, route := range []struct{ method, path string }{{http.MethodGet, listPath}, {http.MethodPost, cancelPath}} {
			t.Run(name+" "+route.method, func(t *testing.T) {
				f := newOwnerFixture(t)
				path := route.path
				body := any(nil)
				if route.method == http.MethodPost {
					body = cancelBody()
					headers = mergeHeaders(headers, map[string]string{"Origin": "http://example.com"})
				}
				if name == "owner token in url" {
					path += "?token=" + testOwnerToken + "&owner_token=" + testOwnerToken
					if route.method == http.MethodPost {
						body = map[string]any{"request_id": "req-1", "wait_ids": []string{"mw-1"}, "token": testOwnerToken}
					}
				}
				w := f.do(route.method, path, body, headers)
				if w.Code != http.StatusUnauthorized {
					t.Fatalf("HTTP %d, want 401 (body %s)", w.Code, w.Body.String())
				}
				if got := f.owner.calls(); got != 0 {
					t.Fatalf("the Service was called %d times before authentication", got)
				}
				if strings.Contains(w.Body.String(), testOwnerToken) {
					t.Fatal("a 401 must not echo the token")
				}
			})
		}
	}
}

// Authentication is the first check: a caller without the token learns nothing about the Origin rule or the body
// rule, and no body is parsed for them.
func TestOwnerWaits_AuthenticationComesBeforeOriginAndBodyChecks(t *testing.T) {
	cases := map[string]struct {
		origin string
		body   []byte
	}{
		"foreign origin":      {"http://evil.test", mustJSON(t, cancelBody())},
		"no origin":           {"", mustJSON(t, cancelBody())},
		"malformed body":      {"http://evil.test", []byte("{not json")},
		"oversized body":      {"", mustJSON(t, map[string]any{"request_id": "r", "wait_ids": []string{"mw-1"}, "reason": strings.Repeat("x", 40<<10)})},
		"missing fields":      {"http://evil.test", []byte(`{}`)},
		"invalid stage in id": {"http://evil.test", mustJSON(t, cancelBody())},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newOwnerFixture(t)
			path := cancelPath
			if name == "invalid stage in id" {
				path = "/api/mesh/stages/bad%20id/waits/cancel"
			}
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(tc.body))
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			f.srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("HTTP %d, want 401 (body %s)", rec.Code, rec.Body.String())
			}
			if f.owner.calls() != 0 {
				t.Fatalf("the Service was called %d times", f.owner.calls())
			}
		})
	}
	// The same holds for reads of a malformed stage segment.
	f := newOwnerFixture(t)
	if w := f.do(http.MethodGet, "/api/mesh/stages/bad%20id/waits", nil, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("GET with an invalid stage id and no token: HTTP %d, want 401", w.Code)
	}
}

// RFC 7235: the authentication scheme name is case-insensitive.
func TestOwnerWaits_BearerSchemeIsCaseInsensitive(t *testing.T) {
	for _, scheme := range []string{"Bearer", "bearer", "BEARER", "bEaReR"} {
		f := newOwnerFixture(t)
		w := f.do(http.MethodGet, listPath, nil, map[string]string{"Authorization": scheme + " " + testOwnerToken})
		if w.Code != http.StatusOK {
			t.Errorf("scheme %q: HTTP %d, want 200", scheme, w.Code)
		}
	}
	f := newOwnerFixture(t)
	if w := f.do(http.MethodGet, listPath, nil, map[string]string{"Authorization": "Bearer  " + testOwnerToken}); w.Code != http.StatusUnauthorized {
		t.Errorf("a second space makes it a different token: HTTP %d, want 401", w.Code)
	}
}

func mergeHeaders(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// Without the credential configured the routes do not exist: no fallback to unauthenticated access.
func TestOwnerWaits_UnavailableWithoutTokenOrActions(t *testing.T) {
	owner := &fakeOwnerWaits{}
	for name, cfg := range map[string]Config{
		"no token":   {Mesh: newFakeMeshActions(), OwnerWaits: owner},
		"no actions": {Mesh: newFakeMeshActions(), OwnerToken: testOwnerToken},
		"neither":    {Mesh: newFakeMeshActions()},
		"no mesh":    {OwnerWaits: owner, OwnerToken: testOwnerToken},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newTestServer(t, cfg)
			for _, route := range []struct{ method, path string }{{http.MethodGet, listPath}, {http.MethodPost, cancelPath}} {
				req := httptest.NewRequest(route.method, route.path, bytes.NewReader([]byte(`{"request_id":"r","wait_ids":["mw-1"]}`)))
				req.Header.Set("Authorization", "Bearer "+testOwnerToken)
				req.Header.Set("Origin", "http://"+req.Host)
				w := httptest.NewRecorder()
				srv.Handler().ServeHTTP(w, req)
				if w.Code != http.StatusNotFound {
					t.Fatalf("%s %s: HTTP %d, want 404", route.method, route.path, w.Code)
				}
			}
		})
	}
	if owner.calls() != 0 {
		t.Fatalf("an unavailable route reached the Service %d times", owner.calls())
	}
}

func TestOwnerWaits_ValidOwnerReadsOneStagesWaitsWithoutMutation(t *testing.T) {
	f := newOwnerFixture(t)

	w := f.do(http.MethodGet, listPath, nil, ownerHeaders())

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var got struct {
		StageID string                       `json:"stage_id"`
		Waits   []orchestrator.StageWaitView `json:"waits"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.StageID != "s1" || len(got.Waits) != 1 || got.Waits[0].WaitID != "mw-1" || got.Waits[0].Condition.MessageID != "mm-1" {
		t.Fatalf("response = %+v", got)
	}
	if f.owner.listCalls != 1 || f.owner.cancelCalls != 0 || f.owner.lastStage != "s1" {
		t.Fatalf("calls list=%d cancel=%d stage=%q, want one read of s1", f.owner.listCalls, f.owner.cancelCalls, f.owner.lastStage)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", w.Header().Get("Cache-Control"))
	}
	if strings.Contains(w.Body.String(), testOwnerToken) {
		t.Error("the response must not carry the token")
	}
}

func TestOwnerWaits_EmptyListIsAnArrayNotNull(t *testing.T) {
	f := newOwnerFixture(t)
	f.owner.waits = nil
	w := f.do(http.MethodGet, listPath, nil, ownerHeaders())
	if !strings.Contains(w.Body.String(), `"waits":[]`) {
		t.Fatalf("body = %s, want an empty waits array", w.Body.String())
	}
}

func TestOwnerWaits_CancelPassesRequestIDStageIDsAndReasonToTheService(t *testing.T) {
	f := newOwnerFixture(t)

	w := f.do(http.MethodPost, cancelPath, cancelBody(), ownerHeaders("Origin=http://evil.test"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("a foreign Origin: HTTP %d, want 403", w.Code)
	}
	w = f.do(http.MethodPost, cancelPath, cancelBody(), ownerHeaders())
	if w.Code != http.StatusForbidden {
		t.Fatalf("no Origin: HTTP %d, want 403", w.Code)
	}
	if f.owner.calls() != 0 {
		t.Fatal("a refused origin must not reach the Service")
	}

	req := httptest.NewRequest(http.MethodPost, cancelPath, bytes.NewReader(mustJSON(t, cancelBody())))
	req.Header.Set("Authorization", "Bearer "+testOwnerToken)
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if f.owner.cancelCalls != 1 || f.owner.lastReqID != "req-1" || f.owner.lastStage != "s1" ||
		len(f.owner.lastCancel.WaitIDs) != 1 || f.owner.lastCancel.WaitIDs[0] != "mw-1" || f.owner.lastCancel.Reason != "stale after a callback" {
		t.Fatalf("service saw %+v (stage %q, request %q)", f.owner.lastCancel, f.owner.lastStage, f.owner.lastReqID)
	}
	if !strings.Contains(rec.Body.String(), `"mw-1"`) || strings.Contains(rec.Body.String(), testOwnerToken) {
		t.Fatalf("response = %s", rec.Body.String())
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The same request id repeated reaches the Service each time (it owns idempotency) and is answered the same.
func TestOwnerWaits_RepeatedCancelIsAnsweredFromTheService(t *testing.T) {
	f := newOwnerFixture(t)
	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, cancelPath, bytes.NewReader(mustJSON(t, cancelBody())))
		req.Header.Set("Authorization", "Bearer "+testOwnerToken)
		req.Header.Set("Origin", "http://"+req.Host)
		rec := httptest.NewRecorder()
		f.srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	first, second := post(), post()
	if first.Code != http.StatusOK || second.Code != http.StatusOK || first.Body.String() != second.Body.String() {
		t.Fatalf("first %d %s / second %d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	if f.owner.cancelCalls != 2 || f.owner.lastReqID != "req-1" {
		t.Fatalf("cancel calls = %d, last request %q", f.owner.cancelCalls, f.owner.lastReqID)
	}
}

func TestOwnerWaits_CancelRejectsBadShapesBeforeTheService(t *testing.T) {
	cases := map[string]struct {
		path string
		body any
		want int
	}{
		"no request id":     {cancelPath, map[string]any{"wait_ids": []string{"mw-1"}}, http.StatusBadRequest},
		"no wait ids":       {cancelPath, map[string]any{"request_id": "r"}, http.StatusBadRequest},
		"empty wait ids":    {cancelPath, map[string]any{"request_id": "r", "wait_ids": []string{}}, http.StatusBadRequest},
		"malformed json":    {cancelPath, "not an object", http.StatusBadRequest},
		"too large":         {cancelPath, map[string]any{"request_id": "r", "wait_ids": []string{"mw-1"}, "reason": strings.Repeat("x", 40<<10)}, http.StatusRequestEntityTooLarge},
		"bad stage segment": {"/api/mesh/stages/bad%20id/waits/cancel", cancelBody(), http.StatusBadRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newOwnerFixture(t)
			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(mustJSON(t, tc.body)))
			req.Header.Set("Authorization", "Bearer "+testOwnerToken)
			req.Header.Set("Origin", "http://"+req.Host)
			rec := httptest.NewRecorder()
			f.srv.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("HTTP %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			if f.owner.calls() != 0 {
				t.Fatal("the Service was called for a malformed request")
			}
		})
	}
}

func TestOwnerWaits_ServiceRejectionsKeepTheirStatus(t *testing.T) {
	f := newOwnerFixture(t)
	f.owner.err = &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: "unknown wait mw-9"}

	w := f.do(http.MethodGet, listPath, nil, ownerHeaders())
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET HTTP %d, want 404", w.Code)
	}
	req := httptest.NewRequest(http.MethodPost, cancelPath, bytes.NewReader(mustJSON(t, cancelBody())))
	req.Header.Set("Authorization", "Bearer "+testOwnerToken)
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST HTTP %d, want 404", rec.Code)
	}
}

// Other methods on the two paths are not routes.
func TestOwnerWaits_OtherMethodsAreNotRoutes(t *testing.T) {
	f := newOwnerFixture(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, listPath}, {http.MethodPut, listPath}, {http.MethodDelete, listPath},
		{http.MethodGet, cancelPath}, {http.MethodPut, cancelPath},
	} {
		if w := f.do(c.method, c.path, nil, ownerHeaders("Origin=http://example.com")); w.Code != http.StatusNotFound {
			t.Errorf("%s %s: HTTP %d, want 404", c.method, c.path, w.Code)
		}
	}
	if f.owner.calls() != 0 {
		t.Fatal("a non-route reached the Service")
	}
}
