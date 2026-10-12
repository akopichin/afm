package server

import (
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/web"
)

func TestServeIndex_SetsCSP(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	srv.serveIndex(w, req)

	got := w.Header().Get("Content-Security-Policy")
	if got == "" {
		t.Fatal("serveIndex did not set Content-Security-Policy")
	}
	for _, want := range []string{
		"default-src 'self'",
		"img-src 'self' data: blob:",
		"connect-src 'self' ws: wss:",
		"object-src 'none'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("CSP missing %q; got %q", want, got)
		}
	}
}

func TestDashboardCSP_InlineScriptHashMatches(t *testing.T) {
	indexBytes, err := fs.ReadFile(web.FS, "index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}
	m := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindSubmatch(indexBytes)
	if m == nil {
		t.Fatal("no inline <script> found in index.html")
	}
	sum := sha256.Sum256(m[1])
	want := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
	if !strings.Contains(dashboardCSP, want) {
		t.Errorf("dashboardCSP is missing the inline-script hash.\n  computed: %q\n  update the script-src hash in dashboardCSP (server.go)", want)
	}
}
