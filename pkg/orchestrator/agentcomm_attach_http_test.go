package orchestrator_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/server"
	"github.com/akopichin/afm/pkg/state"
)

// A correctable attach request is answered with a 4xx and its reason through the real server, not a bare 500.
func TestAttachAndSend_CorrectableErrorsAreNot500(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{ID: "impl", Name: "Work", Command: "claude"}, {ID: "lint", Name: "Lint", Script: "true"}}
	store, err := state.Open(runDir, []string{"impl", "lint"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-1"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatal(err)
	}
	reg, err := assistants.BuildRegistry(nil, []assistants.Profile{{ID: "qa", Title: "QA", Command: "helper-cmd"}}, nil, "claude",
		func(string) (bool, string, assistants.ReadonlyEnforcement) {
			return true, "", assistants.ReadonlyEnforcementOS
		})
	if err != nil {
		t.Fatal(err)
	}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, RootDir: t.TempDir(), Stages: stages, Store: store, Config: config.Default(), Prompts: orchestrator.DefaultPrompts(),
		AssistantsEnabled: true, AssistantsRegistry: reg,
	})
	srv := server.New(server.Config{RunDir: runDir, Store: store, UIBus: bus.NewUIBus(), Mesh: meshServerActionsForTest{orch: orch}})

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"invalid scope", map[string]any{"profile_id": "qa", "scope": "galaxy", "stage_id": "impl"}, http.StatusBadRequest},
		{"missing stage", map[string]any{"profile_id": "qa", "scope": "stage"}, http.StatusBadRequest},
		{"unknown stage", map[string]any{"profile_id": "qa", "scope": "stage", "stage_id": "nope"}, http.StatusNotFound},
		{"script stage", map[string]any{"profile_id": "qa", "scope": "stage", "stage_id": "lint"}, http.StatusBadRequest},
		{"stage id for a run agent", map[string]any{"profile_id": "qa", "scope": "run", "stage_id": "impl"}, http.StatusBadRequest},
		{"new run agent", map[string]any{"request_id": "req-reused", "profile_id": "qa", "scope": "run", "title": "QA"}, http.StatusOK},
		{"request id reused for another agent", map[string]any{"request_id": "req-reused", "profile_id": "qa", "scope": "run", "title": "Other"}, http.StatusConflict},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := tc.body["request_id"]; !ok {
				tc.body["request_id"] = "req-" + string(rune('a'+i))
			}
			raw, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/mesh/attach-and-send", bytes.NewReader(raw))
			req.Header.Set("Origin", "http://"+req.Host)
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("HTTP %d, want %d (body=%s)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
