package server

import (
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
