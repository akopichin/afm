package server

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// artifactsDir creates <runDir>/<testStageID>/artifacts and returns its path.
func artifactsDir(t *testing.T, runDir string) string {
	t.Helper()
	dir := filepath.Join(runDir, testStageID, "artifacts")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	return dir
}

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatalf("write png: %v", err)
	}
}

func writeJPEG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatalf("write jpeg: %v", err)
	}
}

func writeGIF(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, w, h), []color.Color{color.Black, color.White})
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatalf("write gif: %v", err)
	}
}

func getArtifact(t *testing.T, srv *Server, name string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/stages/"+testStageID+"/artifacts/"+name, nil)
	w := httptest.NewRecorder()
	srv.handleArtifact(w, req)
	return w
}

func TestHandleArtifact_PNG(t *testing.T) {
	srv, runDir := setupTestServer(t)
	writePNG(t, filepath.Join(artifactsDir(t, runDir), "chart.png"), 4, 4)

	w := getArtifact(t, srv, "chart.png")
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("content-type: got %q, want image/png", ct)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff: got %q", got)
	}
	if got := w.Header().Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
		t.Errorf("corp: got %q", got)
	}
	if got := w.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("referrer-policy: got %q", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "private, max-age=31536000, immutable" {
		t.Errorf("cache-control: got %q", got)
	}
	if et := w.Header().Get("ETag"); len(et) < 2 || et[0] != '"' || et[len(et)-1] != '"' {
		t.Errorf("etag not a quoted string: got %q", et)
	}
	if w.Body.Len() == 0 {
		t.Error("empty body")
	}
}

func TestHandleArtifact_JPEG(t *testing.T) {
	srv, runDir := setupTestServer(t)
	writeJPEG(t, filepath.Join(artifactsDir(t, runDir), "photo.jpg"), 4, 4)

	w := getArtifact(t, srv, "photo.jpg")
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("content-type: got %q, want image/jpeg", ct)
	}
}

func TestHandleArtifact_GIF(t *testing.T) {
	srv, runDir := setupTestServer(t)
	writeGIF(t, filepath.Join(artifactsDir(t, runDir), "anim.gif"), 4, 4)

	w := getArtifact(t, srv, "anim.gif")
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/gif" {
		t.Errorf("content-type: got %q, want image/gif", ct)
	}
}

func TestHandleArtifact_RejectsNonGET(t *testing.T) {
	srv, runDir := setupTestServer(t)
	writePNG(t, filepath.Join(artifactsDir(t, runDir), "chart.png"), 4, 4)

	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/artifacts/chart.png", nil)
	w := httptest.NewRecorder()
	srv.handleArtifact(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 405", w.Code)
	}
}

func TestHandleArtifact_RejectsInvalidStageID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/stages/..%2Fescape/artifacts/chart.png", nil)
	// Force a raw, decoded path that contains ".." in the stage id segment.
	req.URL.Path = "/api/stages/../escape/artifacts/chart.png"
	w := httptest.NewRecorder()
	srv.handleArtifact(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestHandleArtifact_RejectsBadName(t *testing.T) {
	srv, runDir := setupTestServer(t)
	artifactsDir(t, runDir)

	cases := []string{
		"..",
		"a..b",        // contains ".."
		"foo%2Fbar",   // encoded slash → charset reject after path is set
		"foo bar.png", // space out of charset
		"foo&bar.png", // & out of charset
		"foo#.png",    // # out of charset
		"",            // empty
	}
	for _, name := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/stages/"+testStageID+"/artifacts/x", nil)
		req.URL.Path = "/api/stages/" + testStageID + "/artifacts/" + name
		w := httptest.NewRecorder()
		srv.handleArtifact(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("name %q: status got %d, want 404", name, w.Code)
		}
	}
}

func TestHandleArtifact_RejectsNameWithSlash(t *testing.T) {
	srv, runDir := setupTestServer(t)
	dir := artifactsDir(t, runDir)
	// Create a nested file that would be reachable only via a slash in name.
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	writePNG(t, filepath.Join(sub, "inner.png"), 4, 4)

	req := httptest.NewRequest(http.MethodGet, "/api/stages/"+testStageID+"/artifacts/x", nil)
	req.URL.Path = "/api/stages/" + testStageID + "/artifacts/sub/inner.png"
	w := httptest.NewRecorder()
	srv.handleArtifact(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

func TestHandleArtifact_RejectsSymlinkEscape(t *testing.T) {
	srv, runDir := setupTestServer(t)
	dir := artifactsDir(t, runDir)

	// A secret file outside the artifacts sandbox.
	secret := filepath.Join(runDir, "secret.png")
	writePNG(t, secret, 4, 4)

	link := filepath.Join(dir, "escape.png")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	w := getArtifact(t, srv, "escape.png")
	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404 (symlink escape must be rejected)", w.Code)
	}
}

func TestHandleArtifact_MissingDir(t *testing.T) {
	srv, _ := setupTestServer(t)
	// No artifacts dir created.
	w := getArtifact(t, srv, "chart.png")
	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

func TestHandleArtifact_MissingFile(t *testing.T) {
	srv, runDir := setupTestServer(t)
	artifactsDir(t, runDir)
	w := getArtifact(t, srv, "nope.png")
	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

func TestHandleArtifact_NonImageText(t *testing.T) {
	srv, runDir := setupTestServer(t)
	dir := artifactsDir(t, runDir)
	if err := os.WriteFile(filepath.Join(dir, "fake.png"), []byte("this is not an image at all"), 0644); err != nil {
		t.Fatal(err)
	}
	w := getArtifact(t, srv, "fake.png")
	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status: got %d, want 415", w.Code)
	}
}

func TestHandleArtifact_Oversize(t *testing.T) {
	srv, runDir := setupTestServer(t)
	dir := artifactsDir(t, runDir)
	// A valid PNG header followed by junk to exceed the size cap without
	// needing to actually encode 10 MiB of image pixels.
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	data := append(buf.Bytes(), bytes.Repeat([]byte{0x00}, maxArtifactBytes+1)...)
	if err := os.WriteFile(filepath.Join(dir, "big.png"), data, 0644); err != nil {
		t.Fatal(err)
	}
	w := getArtifact(t, srv, "big.png")
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status: got %d, want 413", w.Code)
	}
}

func TestHandleArtifact_OversizeDims(t *testing.T) {
	srv, runDir := setupTestServer(t)
	dir := artifactsDir(t, runDir)
	// 9000x1 is a tiny file but its header reports width > maxArtifactDim.
	writePNG(t, filepath.Join(dir, "wide.png"), 9000, 1)
	w := getArtifact(t, srv, "wide.png")
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status: got %d, want 413", w.Code)
	}
}

func TestHandleArtifact_NotModified304(t *testing.T) {
	srv, runDir := setupTestServer(t)
	writePNG(t, filepath.Join(artifactsDir(t, runDir), "chart.png"), 4, 4)

	first := getArtifact(t, srv, "chart.png")
	if first.Code != http.StatusOK {
		t.Fatalf("first status: got %d, want 200", first.Code)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no etag on first response")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/stages/"+testStageID+"/artifacts/chart.png", nil)
	req.Header.Set("If-None-Match", etag)
	w := httptest.NewRecorder()
	srv.handleArtifact(w, req)
	if w.Code != http.StatusNotModified {
		t.Errorf("status: got %d, want 304", w.Code)
	}
}

func TestHandleArtifact_RoutedThroughMux(t *testing.T) {
	srv, runDir := setupTestServer(t)
	writePNG(t, filepath.Join(artifactsDir(t, runDir), "chart.png"), 4, 4)

	req := httptest.NewRequest(http.MethodGet, "/api/stages/"+testStageID+"/artifacts/chart.png", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200, body=%s", w.Code, w.Body.String())
	}
}
