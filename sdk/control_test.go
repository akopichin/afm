package afmsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestApprove_Success(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"approved","stage_id":"plan-me"}`)
	}))
	defer srv.Close()

	run := &Run{baseURL: srv.URL, httpClient: srv.Client()}
	if err := run.Approve(context.Background(), "plan-me"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method: got %q, want POST", gotMethod)
	}
	if gotPath != "/api/stages/plan-me/approve" {
		t.Errorf("path: got %q, want %q", gotPath, "/api/stages/plan-me/approve")
	}
}

func TestApprove_ErrorIncludesResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "stage is done, not awaiting_approval", http.StatusBadRequest)
	}))
	defer srv.Close()

	run := &Run{baseURL: srv.URL, httpClient: srv.Client()}
	err := run.Approve(context.Background(), "plan-me")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "stage is done, not awaiting_approval") {
		t.Errorf("error %q does not include server message", err.Error())
	}
}

func TestRetry_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"retried","stage_id":"notify"}`)
	}))
	defer srv.Close()

	run := &Run{baseURL: srv.URL, httpClient: srv.Client()}
	if err := run.Retry(context.Background(), "notify"); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if gotPath != "/api/stages/notify/retry" {
		t.Errorf("path: got %q, want %q", gotPath, "/api/stages/notify/retry")
	}
}

func TestRevise_SendsFeedbackBody(t *testing.T) {
	var gotBody []byte
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"revised","stage_id":"plan-me"}`)
	}))
	defer srv.Close()

	run := &Run{baseURL: srv.URL, httpClient: srv.Client()}
	if err := run.Revise(context.Background(), "plan-me", "add more tests"); err != nil {
		t.Fatalf("Revise: %v", err)
	}
	if gotPath != "/api/stages/plan-me/revise" {
		t.Errorf("path: got %q, want %q", gotPath, "/api/stages/plan-me/revise")
	}
	var decoded map[string]string
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if decoded["feedback"] != "add more tests" {
		t.Errorf("feedback: got %q, want %q", decoded["feedback"], "add more tests")
	}
}

func TestStageIDIsPathEscaped(t *testing.T) {
	var gotEscapedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// r.URL.Path is always the *decoded* path (a real "/" and an escaped
		// "%2F" both decode to the same "/", so .Path can't distinguish
		// them) — only .EscapedPath() (or .RawPath) preserves the encoding
		// that was actually on the wire, which is what this test needs to
		// verify.
		gotEscapedPath = r.URL.EscapedPath()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	run := &Run{baseURL: srv.URL, httpClient: srv.Client()}
	_ = run.Retry(context.Background(), "weird/id")
	if gotEscapedPath != "/api/stages/weird%2Fid/retry" {
		t.Errorf("escaped path: got %q, want escaped slash", gotEscapedPath)
	}
}
