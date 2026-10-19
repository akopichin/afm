package assistants_test

import (
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
			name: "empty arrays and null",
			raw:  `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","evidence":[],"limitations":null}`,
			want: assistants.Result{Verdict: assistants.VerdictPass, Summary: "ok", Evidence: []string{}},
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
