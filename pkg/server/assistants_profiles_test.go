package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/assistants"
	"github.com/akopichin/afm/contracts/state"
)

// fakeAssistantProfiles is a test double for AssistantProfilesService: profiles are the run-level views, byStage
// those of a stage, and the stage "script" is a script stage.
type fakeAssistantProfiles struct {
	profiles       []assistants.ProfileView
	byStage        map[string][]assistants.ProfileView
	maxDepth       int
	maxDepthSource string
}

func (f fakeAssistantProfiles) Profiles(stageID string) ([]assistants.ProfileView, error) {
	switch views, ok := f.byStage[stageID]; {
	case stageID == "":
		return f.profiles, nil
	case stageID == "script":
		return nil, &state.OperationRejectedError{Code: assistants.CodeInvalidRequest, Reason: "stage \"script\" is a script stage"}
	case !ok:
		return nil, &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: fmt.Sprintf("unknown stage %q", stageID)}
	default:
		return views, nil
	}
}
func (f fakeAssistantProfiles) MaxDepth() (int, string) { return f.maxDepth, f.maxDepthSource }

func getProfiles(t *testing.T, svc AssistantProfilesService, query string) *httptest.ResponseRecorder {
	t.Helper()
	srv := newTestServer(t, Config{AssistantProfiles: svc})
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/assistant-profiles"+query, nil))
	return w
}

// The dashboard asks for the profiles of the stage it attaches to; without stage_id it gets the run-level ones.
func TestProfiles_StageIDSelectsTheStageViews(t *testing.T) {
	svc := fakeAssistantProfiles{
		profiles: []assistants.ProfileView{{ID: "reviewer", Command: "claude", CommandSource: assistants.CommandSourceClient, Available: true}},
		byStage: map[string][]assistants.ProfileView{
			"b": {{ID: "reviewer", Command: "fake-recipe", CommandSource: assistants.CommandSourceStage, Available: true}},
		},
	}
	for query, want := range map[string]string{"": "client", "?stage_id=b": "stage"} {
		w := getProfiles(t, svc, query)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %q: status %d, want 200: %s", query, w.Code, w.Body.String())
		}
		var resp assistantProfilesResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatal(err)
		}
		if len(resp.Profiles) != 1 || resp.Profiles[0].CommandSource != want {
			t.Fatalf("GET %q: profiles = %+v, want reviewer with command_source %q", query, resp.Profiles, want)
		}
	}
}

func TestProfiles_UnknownStageIs404AndScriptStage400(t *testing.T) {
	for query, want := range map[string]int{"?stage_id=nope": http.StatusNotFound, "?stage_id=script": http.StatusBadRequest} {
		if w := getProfiles(t, fakeAssistantProfiles{}, query); w.Code != want {
			t.Errorf("GET %q: status %d, want %d: %s", query, w.Code, want, w.Body.String())
		}
	}
}

// The wire names are the contract of the dashboard client: a rename breaks it silently.
func TestProfiles_WireFields(t *testing.T) {
	svc := fakeAssistantProfiles{profiles: []assistants.ProfileView{
		{ID: "external", Title: "External", Command: "/opt/ext-as-claude.sh", CommandSource: assistants.CommandSourceProfile,
			RuntimeContext: assistants.RuntimeContextWrapper, Available: true},
		{ID: "ghost", Title: "Ghost", Command: "claude", CommandSource: assistants.CommandSourceClient,
			RuntimeContext: assistants.RuntimeContextIsolated, UnavailableReason: "not on the host"},
	}}
	w := getProfiles(t, svc, "")
	var resp struct {
		Profiles []map[string]any `json:"profiles"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"id": "external", "title": "External", "command": "/opt/ext-as-claude.sh", "command_name": "ext-as-claude.sh", "command_source": "profile", "runtime_context": "wrapper", "available": true},
		{"id": "ghost", "title": "Ghost", "command": "claude", "command_name": "Claude Code", "command_source": "client", "runtime_context": "isolated", "available": false, "unavailable_reason": "not on the host"},
	}
	if !reflect.DeepEqual(resp.Profiles, want) {
		t.Fatalf("profiles on the wire = %v, want %v", resp.Profiles, want)
	}
}

func TestProfiles_404WhenServiceNil(t *testing.T) {
	srv := newTestServer(t, Config{}) // no AssistantProfiles wired
	req := httptest.NewRequest(http.MethodGet, "/api/assistant-profiles", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 when collaboration capability off, got %d", w.Code)
	}
}

func TestStatus_CollaborationCapabilityFalseWhenDisabled(t *testing.T) {
	srv := newTestServer(t, Config{}) // no AssistantProfiles wired
	resp := decodeStatus(t, srv)
	if resp.Capabilities.Collaboration {
		t.Error("expected capabilities.collaboration=false when service nil")
	}
}

func TestStatus_CollaborationCapabilityTrueWhenEnabled(t *testing.T) {
	srv := newTestServer(t, Config{AssistantProfiles: fakeAssistantProfiles{maxDepthSource: "default"}})
	resp := decodeStatus(t, srv)
	if !resp.Capabilities.Collaboration {
		t.Error("expected capabilities.collaboration=true when service is wired")
	}
}

func TestProfiles_MethodNotAllowed(t *testing.T) {
	srv := newTestServer(t, Config{AssistantProfiles: fakeAssistantProfiles{maxDepthSource: "default"}})
	req := httptest.NewRequest(http.MethodPost, "/api/assistant-profiles", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/assistant-profiles: expected 405, got %d", w.Code)
	}
}

func TestProfiles_ReturnsConfiguredProfiles(t *testing.T) {
	svc := fakeAssistantProfiles{
		profiles: []assistants.ProfileView{
			{ID: "frontendDeveloper", Title: "Frontend reviewer", Command: "frontend-reviewer", Available: true},
			{ID: "ghost", Title: "Ghost", Command: "claude", Available: false, UnavailableReason: "no verifiable read-only policy for command \"claude\" on the host"},
		},
		maxDepth:       3,
		maxDepthSource: "project",
	}
	srv := newTestServer(t, Config{AssistantProfiles: svc})
	req := httptest.NewRequest(http.MethodGet, "/api/assistant-profiles", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp assistantProfilesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.MaxDepth != 3 || resp.MaxDepthSource != "project" {
		t.Errorf("max_depth/_source = %d/%q, want 3/project", resp.MaxDepth, resp.MaxDepthSource)
	}
	if len(resp.Profiles) != 2 {
		t.Fatalf("Profiles = %d entries, want 2", len(resp.Profiles))
	}
	if resp.Profiles[0].ID != "frontendDeveloper" || !resp.Profiles[0].Available {
		t.Errorf("Profiles[0] = %+v, want available frontendDeveloper", resp.Profiles[0])
	}
	if resp.Profiles[1].Available || resp.Profiles[1].UnavailableReason == "" {
		t.Errorf("Profiles[1] = %+v, want unavailable with a reason", resp.Profiles[1])
	}
}

// TestProfiles_UnavailableShownWithReason — T4.1 acceptance: a role without a working provider is still
// visible in GET /api/assistant-profiles (not dropped from the list), carrying available:false and a
// non-empty reason — so the `@mention` menu (T4.2) can show it as unavailable instead of silently hiding it.
func TestProfiles_UnavailableShownWithReason(t *testing.T) {
	svc := fakeAssistantProfiles{
		profiles: []assistants.ProfileView{
			{ID: "ghost", Title: "Ghost", Command: "claude", Available: false, UnavailableReason: "no verifiable read-only policy for command \"claude\" on the host"},
		},
		maxDepth:       3,
		maxDepthSource: "default",
	}
	srv := newTestServer(t, Config{AssistantProfiles: svc})
	req := httptest.NewRequest(http.MethodGet, "/api/assistant-profiles", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	var resp assistantProfilesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Profiles) != 1 {
		t.Fatalf("Profiles = %d entries, want 1 (unavailable role must still be listed)", len(resp.Profiles))
	}
	if resp.Profiles[0].Available {
		t.Error("Available = true, want false")
	}
	if resp.Profiles[0].UnavailableReason == "" {
		t.Error("UnavailableReason is empty, want a non-empty reason")
	}
}

func TestProfiles_DoesNotLeakCommandPathOrSecrets(t *testing.T) {
	svc := fakeAssistantProfiles{
		profiles: []assistants.ProfileView{
			{ID: "x", Title: "X", Command: "claude", Available: true},
		},
		maxDepth:       3,
		maxDepthSource: "default",
	}
	srv := newTestServer(t, Config{AssistantProfiles: svc})
	req := httptest.NewRequest(http.MethodGet, "/api/assistant-profiles", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	body := w.Body.String()
	for _, forbidden := range []string{"/Users/", "/home/", "sk-ant-", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "/usr/bin/", "Bearer "} {
		if strings.Contains(body, forbidden) {
			t.Errorf("response body contains forbidden substring %q: %s", forbidden, body)
		}
	}
}
