package afmsdk

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStatus_ParsesStages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"flow_name":"demo","stages":[{"id":"a","status":"done"},{"id":"b","status":"running"}]}`)
	}))
	defer srv.Close()

	run := &Run{baseURL: srv.URL, httpClient: srv.Client()}
	status, err := run.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.FlowName != "demo" {
		t.Errorf("FlowName: got %q, want %q", status.FlowName, "demo")
	}
	if status.Stages["a"] != StageDone {
		t.Errorf("stage a: got %q, want %q", status.Stages["a"], StageDone)
	}
	if status.Stages["b"] != StageRunning {
		t.Errorf("stage b: got %q, want %q", status.Stages["b"], StageRunning)
	}
	if status.Done {
		t.Error("Done: got true, want false (stage b is running)")
	}
	if status.Failed {
		t.Error("Failed: got true, want false")
	}
}

func TestStatus_AllDoneAndFailedFlags(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantDone   bool
		wantFailed bool
	}{
		{"all done", `{"flow_name":"x","stages":[{"id":"a","status":"done"},{"id":"b","status":"done"}]}`, true, false},
		{"one failed", `{"flow_name":"x","stages":[{"id":"a","status":"done"},{"id":"b","status":"failed"}]}`, false, true},
		{"one hook_failed", `{"flow_name":"x","stages":[{"id":"a","status":"hook_failed"}]}`, false, true},
		{"no stages", `{"flow_name":"x","stages":[]}`, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()

			run := &Run{baseURL: srv.URL, httpClient: srv.Client()}
			status, err := run.Status(context.Background())
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if status.Done != tc.wantDone {
				t.Errorf("Done: got %t, want %t", status.Done, tc.wantDone)
			}
			if status.Failed != tc.wantFailed {
				t.Errorf("Failed: got %t, want %t", status.Failed, tc.wantFailed)
			}
		})
	}
}

func TestStatus_NonOKResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	run := &Run{baseURL: srv.URL, httpClient: srv.Client()}
	if _, err := run.Status(context.Background()); err == nil {
		t.Fatal("expected error on 500 response")
	}
}
