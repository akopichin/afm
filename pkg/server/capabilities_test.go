package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/server/workspace"
	"github.com/akopichin/afm/pkg/state"
)

// fakeFS is a workspace.FS test double: Roots() returns a configurable
// number of entries, every other method is unused by this test and returns
// workspace.ErrNotFound / its zero value.
type fakeFS struct {
	roots int
}

func (f fakeFS) Roots() []workspace.RootView {
	views := make([]workspace.RootView, f.roots)
	for i := range views {
		views[i] = workspace.RootView{ID: "root", Label: "root"}
	}
	return views
}

func (f fakeFS) List(context.Context, string, string, string) (workspace.Page, error) {
	return workspace.Page{}, workspace.ErrNotFound
}

func (f fakeFS) Reference(context.Context, string, string) (workspace.Reference, error) {
	return workspace.Reference{}, workspace.ErrNotFound
}

func (f fakeFS) Read(context.Context, string, string) (workspace.File, error) {
	return workspace.File{}, workspace.ErrNotFound
}

func (f fakeFS) Diff(context.Context, string, string) (workspace.Diff, error) {
	return workspace.Diff{}, workspace.ErrNotFound
}

func (f fakeFS) Close() error {
	return nil
}

// newTestServer builds a minimal Server for capability tests, reusing the
// same store/bus setup as setupTestServer but allowing the caller to
// override Workspace (and other Config fields as needed later).
func newTestServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	cfg.RunDir = runDir
	cfg.Store = store
	if cfg.UIBus == nil {
		cfg.UIBus = bus.NewUIBus()
	}
	if cfg.Actions == nil {
		cfg.Actions = fakeStageActions{}
	}
	return New(cfg)
}

func decodeStatus(t *testing.T, srv *Server) statusResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/status: expected 200, got %d", w.Code)
	}
	var resp statusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode /api/status response: %v", err)
	}
	return resp
}

func TestStatus_CapabilityReflectsWorkspace(t *testing.T) {
	// with a fake FS exposing one root -> true
	srvOn := newTestServer(t, Config{Workspace: fakeFS{roots: 1}})
	if !decodeStatus(t, srvOn).Capabilities.FileBrowser {
		t.Error("expected file_browser=true")
	}
	// nil workspace (host mode) -> false
	srvOff := newTestServer(t, Config{})
	if decodeStatus(t, srvOff).Capabilities.FileBrowser {
		t.Error("expected file_browser=false")
	}
}
