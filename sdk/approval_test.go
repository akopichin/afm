package afmsdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
)

func TestApprovalSnapshot_ReadsRevision(t *testing.T) {
	run := startFake(t, fakeCfg{ApprovalRev: "rev-a"}, ModeCore)
	got, err := run.ApprovalSnapshot(context.Background(), "build")
	if err != nil {
		t.Fatalf("ApprovalSnapshot: %v", err)
	}
	want := ApprovalSnapshot{RunID: "run-1", StageID: "build", Revision: "rev-a", PlanMarkdown: "# plan"}
	if got != want {
		t.Errorf("snapshot = %+v, want %+v", got, want)
	}
}

func TestApproveExpected_AppliedAndStale(t *testing.T) {
	run := startFake(t, fakeCfg{ApprovalRev: "rev-b"}, ModeCore)
	ctx := context.Background()

	_, err := run.ApproveExpected(ctx, "build", ApproveExpectedRequest{RunID: "run-1", ExpectedRevision: "rev-a"})
	var approvalErr *ApprovalError
	if !errors.As(err, &approvalErr) || approvalErr.Code != ApprovalCodeStale {
		t.Fatalf("stale approve error = %v, want ApprovalError %q", err, ApprovalCodeStale)
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusConflict {
		t.Errorf("stale approve error = %v, want an unwrappable HTTP 409", err)
	}

	result, err := run.ApproveExpected(ctx, "build", ApproveExpectedRequest{RunID: "run-1", ExpectedRevision: "rev-b"})
	if err != nil {
		t.Fatalf("ApproveExpected: %v", err)
	}
	want := ApproveExpectedResult{RunID: "run-1", StageID: "build", ApprovedRevision: "rev-b", TransitionSeq: 7}
	if result != want {
		t.Errorf("result = %+v, want %+v", result, want)
	}
}

func TestApproveExpected_RunChangedCode(t *testing.T) {
	run := startFake(t, fakeCfg{ApprovalRev: "rev-a"}, ModeCore)
	_, err := run.ApproveExpected(context.Background(), "build", ApproveExpectedRequest{RunID: "other", ExpectedRevision: "rev-a"})
	var approvalErr *ApprovalError
	if !errors.As(err, &approvalErr) || approvalErr.Code != ApprovalCodeRunChanged {
		t.Fatalf("error = %v, want ApprovalError %q", err, ApprovalCodeRunChanged)
	}
}

// The body carries exactly the fields the server accepts (it rejects unknown ones) and the stage is escaped in the path.
func TestApproveExpected_RequestShape(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.EscapedPath(), r.Method
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = fmt.Fprint(w, `{"run_id":"r","stage_id":"a/b","approved_revision":"v","transition_seq":3}`)
	}))
	defer srv.Close()

	run := &Run{baseURL: srv.URL, httpClient: srv.Client()}
	if _, err := run.ApproveExpected(context.Background(), "a/b", ApproveExpectedRequest{RunID: "r", ExpectedRevision: "v"}); err != nil {
		t.Fatalf("ApproveExpected: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/stages/a%2Fb/approval" {
		t.Errorf("request = %s %s, want POST /api/stages/a%%2Fb/approval", gotMethod, gotPath)
	}
	if len(gotBody) != 2 || gotBody["run_id"] != "r" || gotBody["expected_revision"] != "v" {
		t.Errorf("body = %v, want exactly run_id and expected_revision", gotBody)
	}
}

func TestApprovalError_NonCodeFailuresStayHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "plain failure", http.StatusBadRequest)
	}))
	defer srv.Close()

	run := &Run{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := run.ApprovalSnapshot(context.Background(), "build")
	var approvalErr *ApprovalError
	var httpErr *HTTPError
	if errors.As(err, &approvalErr) || !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("error = %v, want a plain *HTTPError", err)
	}
}

// The golden file lists the JSON shapes of the engine's approval contract; the SDK mirror must neither
// drop nor invent a field, so a contract change fails here instead of silently losing data.
func TestApprovalTypes_MatchContractGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/approval-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]map[string]any
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("golden is not an object of objects: %v", err)
	}
	cases := []struct {
		name   string
		value  any
		ignore string // contract field the SDK carries in the URL instead of the body
	}{
		{"snapshot", ApprovalSnapshot{}, ""},
		{"approve_request", ApproveExpectedRequest{}, "stage_id"},
		{"approve_result", ApproveExpectedResult{}, ""},
	}
	for _, tc := range cases {
		want, ok := golden[tc.name]
		if !ok {
			t.Fatalf("golden has no %q object", tc.name)
		}
		delete(want, tc.ignore)
		encoded, err := json.Marshal(tc.value)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(encoded, &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Errorf("%s: SDK fields %v, golden fields %v", tc.name, keys(got), keys(want))
		}
		for field := range want {
			if _, ok := got[field]; !ok {
				t.Errorf("%s: SDK type lacks golden field %q", tc.name, field)
			}
		}
	}
	goldenCodes, ok := golden["error_codes"]
	if !ok {
		t.Fatal(`golden has no "error_codes" object`)
	}
	var got []string
	for _, code := range goldenCodes {
		got = append(got, fmt.Sprint(code))
	}
	slices.Sort(got)
	want := []string{ApprovalCodeRunChanged, ApprovalCodeStale, ApprovalCodeUnavailable}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("golden error codes %v, SDK constants %v", got, want)
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
