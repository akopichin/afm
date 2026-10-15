package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
)

// fakeAssistantProfiles is a test double for AssistantProfilesService.
type fakeAssistantProfiles struct {
	profiles       []assistants.ProfileView
	maxDepth       int
	maxDepthSource string
}

func (f fakeAssistantProfiles) Profiles() []assistants.ProfileView { return f.profiles }
func (f fakeAssistantProfiles) MaxDepth() (int, string)            { return f.maxDepth, f.maxDepthSource }

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
