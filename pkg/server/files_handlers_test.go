package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/server/workspace"
)

// doGET performs a GET request against the server's handler and returns the
// recorded response.
func doGET(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

func TestFiles_DisabledReturns404(t *testing.T) {
	srv := newTestServer(t, Config{}) // no workspace
	for _, p := range []string{"/api/files/roots", "/api/files/tree?root=project&path=."} {
		rr := doGET(t, srv, p)
		if rr.Code != http.StatusNotFound {
			t.Errorf("%s: got %d want 404", p, rr.Code)
		}
	}
}

func TestFiles_ContentAndErrorShape(t *testing.T) {
	srv := newTestServer(t, Config{Workspace: fakeFS{
		files: map[string]workspace.File{"a.go": {Path: "a.go", Language: "go", Content: "package a\n", Reference: `[AFM file: "/x/a.go"]`}},
		roots: 1,
	}})
	rr := doGET(t, srv, "/api/files/content?root=project&path=a.go")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"language":"go"`) {
		t.Fatalf("content: %d %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("X-Content-Type-Options"); ct != "nosniff" {
		t.Errorf("missing nosniff, got %q", ct)
	}
	// binary → 415 JSON error, no absolute path leak
	rr = doGET(t, srv, "/api/files/content?root=project&path=bin")
	if rr.Code != 415 || !strings.Contains(rr.Body.String(), "binary_file") {
		t.Errorf("binary: %d %s", rr.Code, rr.Body.String())
	}
	// The error body must be exactly the scoped {"error": code} shape — no
	// underlying error text (which could embed an absolute filesystem path).
	if body := strings.TrimSpace(rr.Body.String()); body != `{"error":"binary_file"}` {
		t.Errorf("binary body leaks details, got %q", body)
	}
}

func TestFiles_Search(t *testing.T) {
	srv := newTestServer(t, Config{Workspace: fakeFS{
		roots: 1,
		search: workspace.SearchResult{
			Entries:   []workspace.Entry{{Name: "a.go", Path: "pkg/a.go", Kind: "file", Language: "go", Selectable: true}},
			Truncated: true,
		},
	}})

	rr := doGET(t, srv, "/api/files/search?root=project&q=a")
	if rr.Code != 200 {
		t.Fatalf("search: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"path":"pkg/a.go"`) || !strings.Contains(body, `"truncated":true`) {
		t.Errorf("unexpected body: %s", body)
	}

	// Empty query → fakeFS returns ErrInvalidRootOrPath → 400 with scoped shape.
	rr = doGET(t, srv, "/api/files/search?root=project&q=")
	if rr.Code != 400 || strings.TrimSpace(rr.Body.String()) != `{"error":"invalid_root_or_path"}` {
		t.Errorf("empty query: %d %s", rr.Code, rr.Body.String())
	}
}

func TestFilesChanged_Success(t *testing.T) {
	srv := newTestServer(t, Config{Workspace: fakeFS{roots: 1, changes: workspace.ChangeList{
		Entries: []workspace.Change{{Name: "a.go", Path: "a.go", Status: workspace.ChangeModified, Selectable: true}},
	}}})
	rr := doGET(t, srv, "/api/files/changed?root=r&mode=head")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	var body workspace.ChangeList
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Entries) != 1 || body.Entries[0].Path != "a.go" {
		t.Fatalf("body = %+v", body)
	}
}

func TestFilesChanged_InvalidMode(t *testing.T) {
	srv := newTestServer(t, Config{Workspace: fakeFS{roots: 1, changesErr: workspace.ErrInvalidRootOrPath}})
	rr := doGET(t, srv, "/api/files/changed?root=r&mode=bogus")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestFilesChanged_NotRepo(t *testing.T) {
	srv := newTestServer(t, Config{Workspace: fakeFS{roots: 1, changesErr: workspace.ErrDiffUnavailable}})
	rr := doGET(t, srv, "/api/files/changed?root=r&mode=index")
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
}

func TestFilesChanged_NonGET(t *testing.T) {
	srv := newTestServer(t, Config{Workspace: fakeFS{roots: 1}})
	req := httptest.NewRequest(http.MethodPost, "/api/files/changed?root=r&mode=index", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestFilesChanged_WorkspaceDisabled(t *testing.T) {
	srv := newTestServer(t, Config{Workspace: fakeFS{roots: 0}})
	rr := doGET(t, srv, "/api/files/changed?root=r&mode=index")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}
