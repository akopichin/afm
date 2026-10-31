package assistants_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
)

// Models answer free-text fields both as one string and as a list of lines. The decoder accepts either shape
// and stores the field's own type, so a well-formed review never fails on the shape of its prose.
func TestDecodeResult_FreeTextFieldsAcceptStringOrList(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want assistants.Result
	}{
		{
			name: "limitations as an array (the live-run answer)",
			raw: `{"schema_version":1,"kind":"result","verdict":"pass","summary":"No findings.",` +
				`"evidence":["read slug.go"],"limitations":["The base diff was empty.","Tests were not run."]}`,
			want: assistants.Result{
				Verdict: assistants.VerdictPass, Summary: "No findings.", Evidence: []string{"read slug.go"},
				Limitations: "The base diff was empty.\nTests were not run.",
			},
		},
		{
			name: "summary and suggested_change as arrays",
			raw: `{"schema_version":1,"kind":"result","verdict":"inconclusive","summary":["Line one.","Line two."],` +
				`"suggested_change":["Rename x.","Add a test."]}`,
			want: assistants.Result{
				Verdict: assistants.VerdictInconclusive, Summary: "Line one.\nLine two.", SuggestedChange: "Rename x.\nAdd a test.",
			},
		},
		{
			name: "evidence as a single string",
			raw:  `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","evidence":"ran go test ./..."}`,
			want: assistants.Result{Verdict: assistants.VerdictPass, Summary: "ok", Evidence: []string{"ran go test ./..."}},
		},
		{
			name: "empty array",
			raw:  `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","evidence":[]}`,
			want: assistants.Result{Verdict: assistants.VerdictPass, Summary: "ok", Evidence: []string{}},
		},
		{
			name: "finding detail as an array (the live-run answer)",
			raw: `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"ok",` +
				`"findings":[{"id":"f1","severity":"major","detail":["First line.","Second line."]}]}`,
			want: assistants.Result{
				Verdict: assistants.VerdictNeedsChanges, Summary: "ok",
				Findings: []assistants.Finding{{ID: "f1", Severity: "major", Detail: "First line.\nSecond line."}},
			},
		},
		{
			name: "canonical shapes unchanged",
			raw:  `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","evidence":["a","b"],"limitations":"none"}`,
			want: assistants.Result{Verdict: assistants.VerdictPass, Summary: "ok", Evidence: []string{"a", "b"}, Limitations: "none"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := assistants.DecodeResult([]byte(tc.raw))
			if err != nil {
				t.Fatalf("DecodeResult: %v", err)
			}
			if !reflect.DeepEqual(*env.Result, tc.want) {
				t.Fatalf("result = %+v, want %+v", *env.Result, tc.want)
			}
		})
	}
}

func TestDecodeResult_DelegateSummaryAcceptsList(t *testing.T) {
	raw := `{"schema_version":1,"kind":"delegate","summary":["split","the work"],` +
		`"children":[{"profile_id":"p","title":"x","instruction":"look at x","mode":"advisory","trigger":"one_shot"}]}`
	env, err := assistants.DecodeResult([]byte(raw))
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	if env.Delegate.Summary != "split\nthe work" {
		t.Fatalf("delegate summary = %q", env.Delegate.Summary)
	}
}

// The tolerance is for prose only: anything that is neither a string nor a list of strings is still a
// protocol error, and required fields and the verdict enum stay strict.
func TestDecodeResult_FreeTextFieldsStayStrictOtherwise(t *testing.T) {
	cases := map[string]string{
		"number in a list":   `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","limitations":["a",1]}`,
		"object":             `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","limitations":{"a":"b"}}`,
		"nested list":        `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","evidence":[["a"]]}`,
		"number as summary":  `{"schema_version":1,"kind":"result","verdict":"pass","summary":42}`,
		"verdict as a list":  `{"schema_version":1,"kind":"result","verdict":["pass"],"summary":"ok"}`,
		"unknown verdict":    `{"schema_version":1,"kind":"result","verdict":"approved","summary":"ok"}`,
		"unknown field kept": `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","notes":["x"]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := assistants.DecodeResult([]byte(raw)); err == nil {
				t.Fatal("DecodeResult accepted it, want a protocol error")
			}
		})
	}
}

// The published schema and the decoder accept and reject the same free-text shapes: a model checked against
// the schema is never told an answer is wrong that afm would take, and the other way round.
func TestFreeTextShapes_SchemaMatchesDecoder(t *testing.T) {
	sch := compileAssistantResultSchema(t)
	const result = `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok",%s}`
	const delegate = `{"schema_version":1,"kind":"delegate",%s,` +
		`"children":[{"profile_id":"p","title":"x","instruction":"look at x","mode":"advisory","trigger":"one_shot"}]}`
	cases := []struct {
		name   string
		raw    string
		accept bool
	}{
		{"summary as an array", `{"schema_version":1,"kind":"result","verdict":"pass","summary":["a","b"]}`, true},
		{"evidence as a string", fmt.Sprintf(result, `"evidence":"ran the tests"`), true},
		{"evidence as an array", fmt.Sprintf(result, `"evidence":["a"]`), true},
		{"suggested_change as an array", fmt.Sprintf(result, `"suggested_change":["a","b"]`), true},
		{"limitations as an array", fmt.Sprintf(result, `"limitations":["a","b"]`), true},
		{"limitations as a string", fmt.Sprintf(result, `"limitations":"a"`), true},
		{"delegate summary as an array", fmt.Sprintf(delegate, `"summary":["a","b"]`), true},
		{"delegate summary as a string", fmt.Sprintf(delegate, `"summary":"a"`), true},
		{"number in a list", fmt.Sprintf(result, `"limitations":["a",1]`), false},
		{"object", fmt.Sprintf(result, `"limitations":{"a":"b"}`), false},
		{"nested list", fmt.Sprintf(result, `"evidence":[["a"]]`), false},
		{"number as summary", `{"schema_version":1,"kind":"result","verdict":"pass","summary":42}`, false},
		{"delegate summary as a number", fmt.Sprintf(delegate, `"summary":1`), false},
		{"null limitations", fmt.Sprintf(result, `"limitations":null`), false},
		{"null summary", `{"schema_version":1,"kind":"result","verdict":"pass","summary":null}`, false},
		{"null in an evidence list", fmt.Sprintf(result, `"evidence":["a",null]`), false},
		{"null in a limitations list", fmt.Sprintf(result, `"limitations":[null]`), false},
		{"null delegate summary", fmt.Sprintf(delegate, `"summary":null`), false},
		{"finding detail as an array", fmt.Sprintf(result, `"findings":[{"id":"f","severity":"info","detail":["a","b"]}]`), true},
		{"finding detail as a number", fmt.Sprintf(result, `"findings":[{"id":"f","severity":"info","detail":1}]`), false},
		{"null finding detail", fmt.Sprintf(result, `"findings":[{"id":"f","severity":"info","detail":null}]`), false},
		{"unknown finding field", fmt.Sprintf(result, `"findings":[{"id":"f","severity":"info","detail":"a","note":"x"}]`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, decErr := assistants.DecodeResult([]byte(tc.raw))
			schemaErr := sch.Validate(mustUnmarshalJSON(t, tc.raw))
			if (decErr == nil) != tc.accept || (schemaErr == nil) != tc.accept {
				t.Fatalf("decoder err = %v, schema err = %v; want both to accept = %v", decErr, schemaErr, tc.accept)
			}
		})
	}
}
