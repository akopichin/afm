package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/jsonstrict"
	"github.com/akopichin/afm/schema"
)

const (
	nullVerdictNeutral  = `{"schema_version":1,"kind":"result","summary":"notes"}`
	nullVerdictFindings = `{"schema_version":1,"kind":"result","summary":"notes","findings":[{"id":"f1","severity":"minor","detail":"x"}]}`
)

// nullVerdictInputs are answers with an explicit null verdict, each with a defect that used to send them to the
// format agent: a syntax error, a structure error, or none besides the null itself.
var nullVerdictInputs = []struct{ name, raw, formatted string }{
	{"null only", `{"schema_version":1,"kind":"result","verdict":null,"summary":"notes"}`, nullVerdictNeutral},
	{"findings object", `{"schema_version":1,"kind":"result","verdict":null,"summary":"notes","findings":{"id":"f1","severity":"minor","detail":"x"}}`, nullVerdictFindings},
	{"trailing comma", `{"schema_version":1,"kind":"result","verdict":null,"summary":"notes",}`, nullVerdictNeutral},
	{"undefined", `{"schema_version":1,"kind":"result","verdict":undefined,"summary":"notes"}`, nullVerdictNeutral},
	{"None", `{"schema_version":1,"kind":"result","verdict":None,"summary":"notes"}`, nullVerdictNeutral},
	{"raw newline and findings object", "{\"schema_version\":1,\"kind\":\"result\",\"verdict\":null,\"summary\":\"a\nb\",\"findings\":{\"id\":\"f1\",\"severity\":\"minor\",\"detail\":\"x\"}}", `{"schema_version":1,"kind":"result","summary":"a\nb","findings":[{"id":"f1","severity":"minor","detail":"x"}]}`},
	{"null limitations", `{"schema_version":1,"kind":"result","verdict":null,"summary":"notes","limitations":null}`, nullVerdictNeutral},
	{"mixed case key", `{"schema_version":1,"kind":"result","Verdict":null,"summary":"notes","limitations":null}`, nullVerdictNeutral},
	{"upper case key and findings object", `{"schema_version":1,"kind":"result","VERDICT":null,"summary":"notes","findings":{"id":"f1","severity":"minor","detail":"x"}}`, nullVerdictFindings},
	{"upper case key and trailing comma", `{"schema_version":1,"kind":"result","VERDICT":null,"summary":"notes",}`, nullVerdictNeutral},
	{"case collision with pass", `{"schema_version":1,"kind":"result","verdict":"pass","VERDICT":null,"summary":"notes","limitations":null}`, nullVerdictNeutral},
	{"evidence number", `{"schema_version":1,"kind":"result","verdict":null,"summary":"notes","evidence":7}`, nullVerdictNeutral},
}

// An explicit null verdict is a contract error, never an absent verdict: the format agent must not even start,
// whatever else is wrong with the answer, because it could drop the null and the comparison would accept that.
func TestRecoverOutput_ExplicitNullVerdictIsNeverFormatRepaired(t *testing.T) {
	contracts := []schema.Contract{schema.AssistantResultOnly, schema.AssistantResult, schema.AssistantResultOnlyRequired, schema.AssistantResultRequired}
	for _, contract := range contracts {
		for _, input := range nullVerdictInputs {
			// The second answer shows a required helper's format agent supplying the verdict it cannot know.
			withVerdict := strings.Replace(input.formatted, `"kind":"result",`, `"kind":"result","verdict":"pass",`, 1)
			for _, formatted := range []string{input.formatted, withVerdict} {
				t.Run(string(contract)+"/"+input.name, func(t *testing.T) {
					calls := 0
					run := func(context.Context, schema.Contract, string, int) ([]byte, error) {
						calls++
						return []byte(formatted), nil
					}
					got, err := recoverOutput(context.Background(), contract, "t", []byte(input.raw), assistantOutputValidator, outputFormatAttempts, run)
					if err == nil || calls != 0 {
						t.Fatalf("err = %v, format agent runs = %d; want a refusal without the format agent", err, calls)
					}
					if got.Candidate != nil || got.Provenance.Validation != "rejected" || got.Provenance.Attempts != 0 {
						t.Fatalf("recovery = %+v; want no candidate, rejected, no attempts", got)
					}
				})
			}
		}
	}
}

func TestComparableOutput_ExplicitNullVerdict(t *testing.T) {
	for _, nullVerdict := range []string{
		`{"schema_version":1,"kind":"result","verdict":null,"summary":"notes","limitations":null}`,
		`{"schema_version":1,"kind":"result","VERDICT":null,"summary":"notes"}`,
	} {
		for _, repairSyntax := range []bool{true, false} {
			_, err := comparableOutput(schema.AssistantResultOnly, []byte(nullVerdict), repairSyntax)
			if jsonstrict.Classify(err) != jsonstrict.DomainIssue || err == nil || !strings.Contains(err.Error(), "verdict") {
				t.Fatalf("%s repairSyntax=%v: err = %v, want a domain issue naming the verdict", nullVerdict, repairSyntax, err)
			}
		}
	}
}

// Null stays "absent" for every other member and every other contract.
func TestComparableOutput_OtherNullsAreStillDropped(t *testing.T) {
	for _, tc := range []struct {
		name     string
		contract schema.Contract
		raw      string
		dropped  string
	}{
		{"assistant limitations", schema.AssistantResultOnly, `{"schema_version":1,"kind":"result","summary":"notes","limitations":null}`, "limitations"},
		{"assistant strict neutral", schema.AssistantResult, `{"schema_version":1,"kind":"result","summary":"notes","findings":null,"evidence":null,"context_version":null,"suggested_change":null}`, "findings"},
		{"verify finding path", schema.VerifyResult, `{"schema_version":1,"verdict":"pass","summary":"ok","context_version":null}`, "context_version"},
		{"question options", schema.Question, `{"schema_version":1,"question":"q","options":null}`, "options"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := comparableOutput(tc.contract, []byte(tc.raw), true)
			root, _ := got.(map[string]any)
			if _, present := root[tc.dropped]; err != nil || root == nil || present {
				t.Fatalf("comparison = %v, err = %v; want the %s member dropped", got, err, tc.dropped)
			}
		})
	}
}
