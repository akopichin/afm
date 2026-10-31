package assistants_test

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/jsonstrict"
)

// originalFinding is a finding as the model wrote it, with the extra field normalization folds into the detail.
type originalFinding struct {
	assistants.Finding
	SuggestedChange string `json:"suggested_change"`
}

func TestResultNormalizationRedesign(t *testing.T) {
	fixture, err := os.ReadFile("testdata/redesign-result.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{len(fixture), 17124} {
		raw := bytes.Replace(fixture, []byte(`"summary": "`), append([]byte(`"summary": "`), bytes.Repeat([]byte("x"), size-len(fixture))...), 1)
		before := bytes.Clone(raw)
		var original struct {
			Findings []originalFinding
			Evidence []string `json:"evidence"`
		}
		if err := json.Unmarshal(raw, &original); err != nil {
			t.Fatal(err)
		}
		env, repaired, err := assistants.DecodeResultWithRepair(raw)
		if err != nil {
			t.Fatalf("%d bytes: %v", size, err)
		}
		if !repaired || env.Result.Verdict != assistants.VerdictNeedsChanges || len(env.Result.Findings) != 7 {
			t.Fatalf("unexpected normalization: repaired=%v, %+v", repaired, env)
		}
		recommendations := 0
		for i, f := range original.Findings {
			want := f.Finding
			if f.SuggestedChange != "" {
				recommendations++
				want.Detail += "\n\nSuggested change:\n" + f.SuggestedChange
			}
			if !reflect.DeepEqual(env.Result.Findings[i], want) {
				t.Errorf("finding %d changed: %+v", i, env.Result.Findings[i])
			}
		}
		if recommendations != 6 || !reflect.DeepEqual(env.Result.Evidence, original.Evidence) || !bytes.Equal(raw, before) {
			t.Fatal("recommendations, evidence or immutable raw changed")
		}
	}
}

func TestResultNormalizationRefusesLoss(t *testing.T) {
	for _, extra := range []string{
		`"suggested_change":"keep","unknown":"also keep"`,
		`"suggested_change":"keep","suggested_change":"keep too"`,
		`"suggested_change":{"text":"keep"}`,
		`"suggested_change":null`,
	} {
		raw := []byte(`{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"review","findings":[{"id":"f","severity":"major","detail":"detail",` + extra + `}]}`)
		before := bytes.Clone(raw)
		if _, repaired, err := assistants.DecodeResultWithRepair(raw); err == nil || repaired {
			t.Fatalf("accepted %s", raw)
		}
		if !bytes.Equal(raw, before) {
			t.Fatal("raw changed")
		}
	}
}

func TestResultNormalizationPreservesMultilineRecommendation(t *testing.T) {
	raw := []byte(`{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"review","findings":[{"id":"f","severity":"major","detail":"detail","suggested_change":["  first  ","","last\nline"]}]}`)
	env, repaired, err := assistants.DecodeResultWithRepair(raw)
	if err != nil || !repaired {
		t.Fatalf("repaired=%v err=%v", repaired, err)
	}
	if got := env.Result.Findings[0].Detail; !strings.HasSuffix(got, "\n  first  \n\nlast\nline") {
		t.Fatalf("lost recommendation: %q", got)
	}
}

func TestResultRecoveryProvenanceAndIssues(t *testing.T) {
	valid := `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"review"}`
	for _, tc := range []struct {
		raw   string
		kind  jsonstrict.RepairKind
		issue jsonstrict.IssueKind
	}{
		{valid, "", ""},
		{"```json\n" + valid + "\n```", jsonstrict.MechanicalRepair, ""},
		{strings.Replace(valid, `"review"`, "\"line1\nline2\"", 1), jsonstrict.MechanicalRepair, ""},
		{strings.TrimSuffix(valid, "}") + `,"extra":"keep"}`, "", jsonstrict.StructureIssue},
		{strings.Replace(valid, `"schema_version":1`, `"schema_version":2`, 1), "", jsonstrict.DomainIssue},
		{strings.TrimSuffix(valid, "}") + `,"verdict":"pass"}`, "", jsonstrict.RefusalIssue},
		{strings.TrimSuffix(valid, "}"), "", jsonstrict.RefusalIssue},
		{valid + "{}", "", jsonstrict.RefusalIssue},
		{strings.Repeat(" ", assistants.MaxResultBytes) + valid, "", jsonstrict.RefusalIssue},
	} {
		raw := []byte(tc.raw)
		before := bytes.Clone(raw)
		env, candidate, kind, err := assistants.RecoverResult(raw)
		if kind != tc.kind || jsonstrict.Classify(err) != tc.issue {
			t.Errorf("%.100s: kind=%s issue=%s err=%v", raw, kind, jsonstrict.Classify(err), err)
		}
		if err == nil {
			if _, err := assistants.DecodeResult(candidate); err != nil {
				t.Fatal(err)
			}
			if env.Result.Verdict != assistants.VerdictNeedsChanges || env.Result.Repaired {
				t.Fatal("recovery changed verdict or asserted server-owned Repaired")
			}
		}
		if !bytes.Equal(raw, before) {
			t.Fatal("raw mutated")
		}
	}
}

// rawFinding keeps a finding's detail in the shape the model wrote it.
type rawFinding struct {
	ID     string          `json:"id"`
	Detail json.RawMessage `json:"detail"`
}

// A live consultation answered three of its findings' detail as a list of lines; the whole 29 KB review was
// rejected and a formatter had to rewrite it. The decoder takes it as written.
func TestDecodeResult_FindingDetailLinesFromLiveRun(t *testing.T) {
	raw, err := os.ReadFile("testdata/detail-lines-result.json")
	if err != nil {
		t.Fatal(err)
	}
	var original struct {
		Findings []rawFinding `json:"findings"`
	}
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	env, candidate, kind, err := assistants.RecoverResult(raw)
	if err != nil || kind != "" || !bytes.Equal(candidate, raw) {
		t.Fatalf("RecoverResult: kind=%q err=%v, want the raw answer accepted as is", kind, err)
	}
	lists := 0
	for i, f := range original.Findings {
		var lines []string
		if json.Unmarshal(f.Detail, &lines) != nil {
			continue
		}
		lists++
		if got, want := env.Result.Findings[i].Detail, strings.Join(lines, "\n"); got != want {
			t.Errorf("finding %s detail = %q, want %q", f.ID, got, want)
		}
	}
	if lists != 3 || len(env.Result.Findings) != 17 {
		t.Fatalf("fixture changed: %d list details, %d findings", lists, len(env.Result.Findings))
	}
}

func TestResultNormalizationFoldsRecommendationIntoDetailLines(t *testing.T) {
	raw := []byte(`{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"review","findings":[{"id":"f","severity":"major","detail":["one","two"],"suggested_change":"fix"}]}`)
	env, repaired, err := assistants.DecodeResultWithRepair(raw)
	if err != nil || !repaired {
		t.Fatalf("repaired=%v err=%v", repaired, err)
	}
	if got := env.Result.Findings[0].Detail; got != "one\ntwo\n\nSuggested change:\nfix" {
		t.Fatalf("detail = %q", got)
	}
}
