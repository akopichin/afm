package control

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApproveExpectedRequiresAllIdentifiers(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"stage_id":"plan","expected_revision":"opaque"}`,
		`{"run_id":"run","expected_revision":"opaque"}`,
		`{"run_id":"run","stage_id":"plan"}`,
		`{"run_id":"run","stage_id":"plan","expected_revision":null}`,
		`{"run_id":"run","stage_id":"plan","expected_revision":" \t"}`,
	} {
		var req ApproveExpectedRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		if err := req.Validate(); err == nil {
			t.Errorf("accepted incomplete conditional approval: %s", body)
		}
	}
}

func TestApprovalSnapshotRoundTripPreservesReviewedContent(t *testing.T) {
	want := ApprovalSnapshot{
		RunID: "run-1", StageID: "plan", Revision: "opaque:9007199254740993:content",
		PlanMarkdown: "# Plan\n\nKeep `a < b` and Unicode: проверка.\n",
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got ApprovalSnapshot
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("snapshot changed: %#v", got)
	}
	req := ApproveExpectedRequest{RunID: got.RunID, StageID: got.StageID, ExpectedRevision: got.Revision}
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"expected_revision":"opaque:9007199254740993:content"`) {
		t.Fatalf("opaque revision was coerced: %s", raw)
	}
}
