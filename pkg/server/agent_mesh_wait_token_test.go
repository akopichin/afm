package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/orchestrator"
)

// tokenRecordingActions notes the bearer token the Wait request of the real route carried in its context.
type tokenRecordingActions struct {
	*fakeAgentMeshActions
	waitToken string
}

func (a *tokenRecordingActions) Wait(ctx context.Context, requestID, actor string, req MeshWaitRequest) (MeshWaitReceipt, error) {
	a.waitToken = orchestrator.MeshInvocation(ctx)
	return a.fakeAgentMeshActions.Wait(ctx, requestID, actor, req)
}

// The orchestrator decides a wait against the invocation whose token authenticated it, so the route hands the token
// down in the context of the Wait action.
func TestAgentMesh_Wait_CarriesTheAuthenticatingTokenToTheAction(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-1"] = "stage:impl"
	actions := &tokenRecordingActions{fakeAgentMeshActions: fake}
	srv := New(Config{AgentMesh: actions})

	req := httptest.NewRequest(http.MethodPost, "/api/agent-mesh/wait", strings.NewReader(`{"request_id":"r1","condition":{"kind":"reply_to","message_id":"m1"}}`))
	req.Header.Set("Authorization", "Bearer tok-1")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if actions.waitToken != "tok-1" {
		t.Fatalf("token seen by the action = %q, want tok-1", actions.waitToken)
	}
}
