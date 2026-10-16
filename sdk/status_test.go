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

func TestStatus_IgnoresUnknownVerifyRelatedFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"flow_name": "demo",
			"run_cost": {"display_cost": "$0.01"},
			"stages": [
				{
					"id": "build",
					"status": "done",
					"verify_step": 2,
					"verify_verdict": "pass",
					"verify_report_path": "/runs/x/build/verify/v-1/report.md"
				}
			]
		}`)
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
	if status.Stages["build"] != StageDone {
		t.Errorf("stage build: got %q, want %q", status.Stages["build"], StageDone)
	}
	if !status.Done {
		t.Error("Done: got false, want true")
	}
}

// TestSdk_StatusContractIncludesNewFields — T11.5: afm's helper-agents feature (dev-plan.md stages 0-10)
// added a real stage status, awaiting_assistants, that did not exist when this SDK module was first written.
// Before this test/fix, sdk/status.go had no StageAwaitingAssistants constant at all — an SDK consumer would
// see a stage's Status as the raw unrecognized string "awaiting_assistants" with no named value to compare
// against, and worse, that stage would silently count as neither Done nor Failed forever with no way for a
// caller to name what it actually was. This locks the contract: the wire status string round-trips to the
// typed constant, and it participates correctly (neither Done nor Failed) in the aggregate flags.
func TestSdk_StatusContractIncludesNewFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"flow_name":"demo","stages":[{"id":"a","status":"done"},{"id":"impl","status":"awaiting_assistants"}]}`)
	}))
	defer srv.Close()

	run := &Run{baseURL: srv.URL, httpClient: srv.Client()}
	status, err := run.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Stages["impl"] != StageAwaitingAssistants {
		t.Fatalf("stage impl: got %q, want the named constant %q", status.Stages["impl"], StageAwaitingAssistants)
	}
	if status.Done {
		t.Error("Done: got true, want false — a stage still awaiting_assistants is not done")
	}
	if status.Failed {
		t.Error("Failed: got true, want false — awaiting_assistants is not a failure, it needs a human decision")
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
