package assistantstest

import (
	"encoding/json"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
)

func TestFixtures_ValidResultDecodes(t *testing.T) {
	cases := map[string]string{
		"valid":        FixtureValidResult,
		"withFindings": FixtureResultWithFindings,
		"needsChanges": FixtureNeedsChangesResult,
		"withSecret":   FixtureResultWithSecret,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			var r assistants.Result
			if err := json.Unmarshal([]byte(raw), &r); err != nil {
				t.Fatalf("decode %s: %v", name, err)
			}
			if r.Summary == "" {
				t.Fatalf("decode %s: summary is empty, fixture is not representative", name)
			}
			if r.Verdict == "" {
				t.Fatalf("decode %s: verdict is empty, fixture is not representative", name)
			}
		})
	}
}

func TestFixtures_MalformedResultRejected(t *testing.T) {
	cases := map[string]string{
		"invalidJSON":    FixtureInvalidJSON,
		"markdownFenced": FixtureMarkdownFenced,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			var r assistants.Result
			if err := json.Unmarshal([]byte(raw), &r); err == nil {
				t.Fatalf("decode %s: want error, got none (r=%+v)", name, r)
			}
		})
	}
}
