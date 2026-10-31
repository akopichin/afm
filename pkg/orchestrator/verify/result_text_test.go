package verify

import (
	"reflect"
	"testing"
)

// A verifier, like a helper, writes prose either as one string or as a list of lines. The shape of the prose
// must not throw away a correct verdict: the decoder joins the lines.
func TestDecodeModelResult_TextFieldsAcceptLines(t *testing.T) {
	raw := `{"schema_version":1,"verdict":"needs_changes","summary":["Two problems.","See findings."],"findings":[` +
		`{"blocking":true,"title":["No","test"],"path":null,"line_start":null,"line_end":null,` +
		`"requirement":["cover it"],"evidence":["grep found nothing","in pkg/x"],"minimal_fix":["add","a test"]}]}`
	got, err := DecodeModelResult([]byte(raw))
	if err != nil {
		t.Fatalf("DecodeModelResult: %v", err)
	}
	want := ModelResult{SchemaVersion: 1, Verdict: VerdictNeedsChanges, Summary: "Two problems.\nSee findings.", Findings: []Finding{{
		Blocking: true, Title: "No\ntest", Requirement: "cover it", Evidence: "grep found nothing\nin pkg/x", MinimalFix: "add\na test",
	}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("result = %+v, want %+v", got, want)
	}
}

func TestDecodeModelResult_TextFieldsStayStrictOtherwise(t *testing.T) {
	for name, raw := range map[string]string{
		"number in a summary list": `{"schema_version":1,"verdict":"pass","summary":["ok",1]}`,
		"null in a summary list":   `{"schema_version":1,"verdict":"pass","summary":["ok",null]}`,
		"blank summary lines":      `{"schema_version":1,"verdict":"pass","summary":[" ",""]}`,
		"empty summary list":       `{"schema_version":1,"verdict":"pass","summary":[]}`,
		"object as a title":        `{"schema_version":1,"verdict":"needs_changes","summary":"s","findings":[{"blocking":true,"title":{"a":"b"}}]}`,
		"nested list as a title":   `{"schema_version":1,"verdict":"needs_changes","summary":"s","findings":[{"blocking":true,"title":[["a"]]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeModelResult([]byte(raw)); err == nil {
				t.Fatal("accepted it, want a protocol error")
			}
		})
	}
}

// Null stays what it was for the optional finding fields: no text.
func TestDecodeModelResult_NullFindingTextStillEmpty(t *testing.T) {
	raw := `{"schema_version":1,"verdict":"needs_changes","summary":"s","findings":[{"blocking":true,"title":null,"evidence":null}]}`
	got, err := DecodeModelResult([]byte(raw))
	if err != nil || got.Findings[0].Title != "" || got.Findings[0].Evidence != "" {
		t.Fatalf("result = %+v, err = %v", got, err)
	}
}
