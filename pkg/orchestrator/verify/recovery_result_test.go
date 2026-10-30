package verify_test

import (
	"bytes"
	"testing"

	"github.com/akopichin/afm/pkg/jsonstrict"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

func TestModelResultRecoveryKeepsNarrowPolicy(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		kind  jsonstrict.RepairKind
		issue jsonstrict.IssueKind
	}{
		{"{\"schema_version\":1,\"verdict\":\"needs_changes\",\"summary\":\"fix\",\"findings\":[{\"blocking\":true,\"evidence\":\"line1\nline2\"}]}", jsonstrict.MechanicalRepair, ""},
		{"```json\n{\"schema_version\":1,\"verdict\":\"pass\",\"summary\":\"ok\"}\n```", "", jsonstrict.RefusalIssue},
		{`{"schema_version":1,"verdict":"pass","summary":"ok","findings":[{"blocking":true}]}`, "", jsonstrict.DomainIssue},
		{`{"schema_version":1,"verdict":"pass","summary":"ok","unknown":"keep"}`, "", jsonstrict.StructureIssue},
		{`{"schema_version":1,"verdict":"pass","summary":"ok","summary":"different"}`, "", jsonstrict.RefusalIssue},
		{`{"schema_version":1,"verdict":"pass","summary":"unfinished`, "", jsonstrict.RefusalIssue},
	} {
		raw := []byte(tc.raw)
		before := bytes.Clone(raw)
		r, candidate, kind, err := verify.RecoverModelResult(raw)
		if kind != tc.kind || jsonstrict.Classify(err) != tc.issue {
			t.Errorf("kind=%s issue=%s err=%v", kind, jsonstrict.Classify(err), err)
		}
		if err == nil {
			if _, err := verify.DecodeModelResult(candidate); err != nil {
				t.Fatal(err)
			}
			if r.Verdict != verify.VerdictNeedsChanges || r.Findings[0].Evidence != "line1\nline2" {
				t.Fatal("verdict or evidence changed")
			}
		}
		if !bytes.Equal(raw, before) {
			t.Fatal("raw mutated")
		}
	}
}
