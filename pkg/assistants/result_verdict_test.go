package assistants_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/jsonstrict"
)

const (
	neutralResultJSON = `{"schema_version":1,"kind":"result","summary":"logs collected"}`
	delegateJSON      = `{"schema_version":1,"kind":"delegate","children":[{"profile_id":"p","title":"t","instruction":"i"}]}`
)

// A result without verdict is a valid advisory answer: no assessment, nothing invented in its place.
func TestDecodeResult_VerdictAbsenceDiffersFromEmptyUnknownAndNull(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		advisoryErr bool
		requiredErr bool
		want        assistants.Verdict
	}{
		{"absent", neutralResultJSON, false, true, ""},
		{"pass", `{"schema_version":1,"kind":"result","verdict":"pass","summary":"s"}`, false, false, assistants.VerdictPass},
		{"needs_changes", `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"s"}`, false, false, assistants.VerdictNeedsChanges},
		{"inconclusive", `{"schema_version":1,"kind":"result","verdict":"inconclusive","summary":"s"}`, false, false, assistants.VerdictInconclusive},
		{"empty string", `{"schema_version":1,"kind":"result","verdict":"","summary":"s"}`, true, true, ""},
		{"null", `{"schema_version":1,"kind":"result","verdict":null,"summary":"s"}`, true, true, ""},
		{"unknown", `{"schema_version":1,"kind":"result","verdict":"fine","summary":"s"}`, true, true, ""},
		{"wrong type", `{"schema_version":1,"kind":"result","verdict":1,"summary":"s"}`, true, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := assistants.DecodeResultFor([]byte(tc.raw), false)
			if (err != nil) != tc.advisoryErr {
				t.Fatalf("advisory err = %v, want error=%v", err, tc.advisoryErr)
			}
			if err == nil && env.Result.Verdict != tc.want {
				t.Errorf("advisory verdict = %q, want %q", env.Result.Verdict, tc.want)
			}
			if _, err := assistants.DecodeResultFor([]byte(tc.raw), true); (err != nil) != tc.requiredErr {
				t.Errorf("required err = %v, want error=%v", err, tc.requiredErr)
			}
			// DecodeResult keeps the advisory rules for callers that do not know the mode.
			if _, err := assistants.DecodeResult([]byte(tc.raw)); (err != nil) != tc.advisoryErr {
				t.Errorf("DecodeResult err = %v, want error=%v", err, tc.advisoryErr)
			}
		})
	}
}

func TestDecodeResultFor_MissingVerdictIsADomainIssueInRequiredMode(t *testing.T) {
	_, err := assistants.DecodeResultFor([]byte(neutralResultJSON), true)
	var issue *jsonstrict.Issue
	if !errors.As(err, &issue) || issue.Kind != jsonstrict.DomainIssue {
		t.Fatalf("err = %#v, want a domain issue", err)
	}
}

// "verdict": null is a written value that is no verdict, not a missing one: refused outright, so no repair can
// turn it into an assessment.
func TestDecodeResultFor_NullVerdictIsADomainIssue(t *testing.T) {
	const nullVerdict = `{"schema_version":1,"kind":"result","verdict":null,"summary":"s"}`
	for _, require := range []bool{false, true} {
		_, err := assistants.DecodeResultFor([]byte(nullVerdict), require)
		var issue *jsonstrict.Issue
		if !errors.As(err, &issue) || issue.Kind != jsonstrict.DomainIssue {
			t.Errorf("requireVerdict=%v: err = %#v, want a domain issue", require, err)
		}
	}
}

func TestDecodeResultFor_DelegateIgnoresRequireVerdict(t *testing.T) {
	for _, require := range []bool{false, true} {
		env, err := assistants.DecodeResultFor([]byte(delegateJSON), require)
		if err != nil || env.Kind != assistants.KindDelegate {
			t.Errorf("requireVerdict=%v: env=%+v err=%v", require, env, err)
		}
	}
}

func TestDecodeResult_NeutralResultKeepsBlockerFindingsAndConsistencyRule(t *testing.T) {
	neutral := `{"schema_version":1,"kind":"result","summary":"s","findings":[{"id":"f","severity":"blocker","detail":"d"}]}`
	if _, err := assistants.DecodeResult([]byte(neutral)); err != nil {
		t.Fatalf("neutral result with a finding: %v", err)
	}
	pass := `{"schema_version":1,"kind":"result","verdict":"pass","summary":"s","findings":[{"id":"f","severity":"blocker","detail":"d"}]}` //nolint:gosec // test fixture, not a credential
	if _, err := assistants.DecodeResult([]byte(pass)); err == nil {
		t.Fatal("pass with a blocker was accepted")
	}
}

func TestResultJSON_OmitsAbsentVerdict(t *testing.T) {
	env, err := assistants.DecodeResult([]byte(neutralResultJSON))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(env.Result)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, found := fields["verdict"]; found {
		t.Fatalf(`result JSON carries "verdict": %s`, raw)
	}
	var old assistants.Result
	if err := json.Unmarshal([]byte(`{"verdict":"needs_changes","summary":"s"}`), &old); err != nil || old.Verdict != assistants.VerdictNeedsChanges {
		t.Fatalf("old journal payload decoded to %+v, %v", old, err)
	}
}

func TestEnvelopeClosesRequiredGate_NeutralResultDoesNotClose(t *testing.T) {
	env, err := assistants.DecodeResult([]byte(neutralResultJSON))
	if err != nil {
		t.Fatal(err)
	}
	if env.ClosesRequiredGate() {
		t.Fatal("a result without verdict closed a required gate")
	}
}
