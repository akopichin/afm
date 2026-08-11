package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHandleUploadAttachment_Success(t *testing.T) {
	srv, runDir := setupTestServer(t)

	body := bytes.Repeat([]byte{0xFF}, 128)
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/attachments", bytes.NewReader(body))
	req.Header.Set("Content-Type", "image/png")
	w := httptest.NewRecorder()

	srv.handleUploadAttachment(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Path == "" {
		t.Fatal("response path is empty")
	}
	wantDir := filepath.Join(runDir, testStageID, "attachments")
	if filepath.Dir(resp.Path) != wantDir {
		t.Errorf("path dir: got %q, want %q", filepath.Dir(resp.Path), wantDir)
	}

	written, err := os.ReadFile(resp.Path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if !bytes.Equal(written, body) {
		t.Error("written file content does not match uploaded body")
	}
}

func TestHandleUploadAttachment_RejectsUnsupportedType(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/attachments", bytes.NewReader([]byte("not an image")))
	req.Header.Set("Content-Type", "application/pdf")
	w := httptest.NewRecorder()

	srv.handleUploadAttachment(w, req)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status: got %d, want 415", w.Code)
	}
}

func TestHandleUploadAttachment_RejectsOversizedBody(t *testing.T) {
	srv, _ := setupTestServer(t)

	body := bytes.Repeat([]byte{0x00}, maxAttachmentBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/attachments", bytes.NewReader(body))
	req.Header.Set("Content-Type", "image/png")
	w := httptest.NewRecorder()

	srv.handleUploadAttachment(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status: got %d, want 413", w.Code)
	}
}

func TestHandleUploadAttachment_RejectsInvalidStageID(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/stages/../escape/attachments", bytes.NewReader([]byte{0x01}))
	req.Header.Set("Content-Type", "image/png")
	w := httptest.NewRecorder()

	srv.handleUploadAttachment(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestHandleUploadAttachment_TwoUploadsGetDistinctFilenames(t *testing.T) {
	srv, _ := setupTestServer(t)

	upload := func() string {
		req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/attachments", bytes.NewReader([]byte{0x01, 0x02}))
		req.Header.Set("Content-Type", "image/png")
		w := httptest.NewRecorder()
		srv.handleUploadAttachment(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200", w.Code)
		}
		var resp struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp.Path
	}

	p1 := upload()
	p2 := upload()
	if p1 == p2 {
		t.Errorf("two uploads got the same path: %q", p1)
	}
}

func TestHandleUploadAttachment_RoutedThroughMux(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/attachments", bytes.NewReader([]byte{0x01}))
	req.Header.Set("Content-Type", "image/png")
	w := httptest.NewRecorder()

	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200, body=%s", w.Code, w.Body.String())
	}
}
