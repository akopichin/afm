package schemacheck_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

type schemaCase struct {
	raw  string
	want bool
}

func TestOutputSchemaParity(t *testing.T) {
	contracts := []struct {
		name   string
		accept func([]byte) bool
		cases  []schemaCase
	}{
		{"assistant-result", func(raw []byte) bool { _, err := assistants.DecodeResult(raw); return err == nil }, []schemaCase{
			{`{"schema_version":1,"kind":"result","verdict":"pass","summary":["ok"],"evidence":"checked","context_version":null,"findings":null}`, true},
			{`{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"review","findings":[{"id":"f","severity":"major","detail":"d","path":null,"line_start":null,"line_end":null}]}`, true},
			{`{"schema_version":1,"kind":"delegate","children":[{"profile_id":"p","title":"t","instruction":"i","mode":"advisory","trigger":"one_shot"}],"continuation":null}`, true},
			{`{"schema_version":1,"kind":"result","verdict":"pass"}`, false},
			{`{"schema_version":1,"kind":"result","verdict":"pass","summary":null}`, false},
			{`{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","findings":[{}]}`, false},
			{`{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","findings":[{"id":"f","severity":"typo","detail":"d"}]}`, false},
			{`{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","findings":[{"id":"f","severity":"blocker","detail":"d"}]}`, false},
			{`{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","findings":[{"id":"f","severity":"minor","detail":"d","line_start":0}]}`, false},
			{`{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","repaired":true}`, false},
			{`{"schema_version":1,"kind":"delegate","children":[{}]}`, false},
		}},
		{"verify-result", func(raw []byte) bool { _, err := verify.DecodeModelResult(raw); return err == nil }, []schemaCase{
			{`{"schema_version":1,"verdict":"pass","summary":"ok"}`, true},
			{`{"schema_version":1,"verdict":"pass","summary":"ok","findings":null}`, true},
			{`{"schema_version":1,"verdict":"needs_changes","summary":"fix","findings":[{"blocking":true,"path":null,"line_start":null,"line_end":null}]}`, true},
			{`{"schema_version":1,"verdict":"pass","summary":"ok","findings":[{"blocking":null,"title":null,"line_start":0}]}`, true},
			{`{"schema_version":1,"verdict":"needs_changes","summary":"fix","findings":[]}`, false},
			{`{"schema_version":1,"verdict":"pass","summary":"ok","findings":[{"blocking":true}]}`, false},
			{`{"schema_version":1,"verdict":"inconclusive","summary":" \t\n"}`, false},
			{`{"schema_version":1,"verdict":"accepted_stalemate","summary":"ok"}`, false},
			{`{"schema_version":2,"verdict":"pass","summary":"ok"}`, false},
			{`{"schema_version":1,"verdict":"pass","summary":"ok","extra":"keep"}`, false},
		}},
		{"question", mcp.CanParseQuestion, []schemaCase{
			{`{"question":"Proceed?"}`, true},
			{`{"id":null,"question":"Proceed?","options":null,"allow_custom":null}`, true},
			{`{"id":"ignored","question":"Proceed?","options":["Yes","No"],"allow_custom":false}`, true},
			{`{}`, true},
			{`{"question":42}`, false},
			{`{"question":"Proceed?","options":[{}]}`, false},
			{`{"question":"Proceed?","allow_custom":"yes"}`, false},
			{`{"question":"Proceed?","unknown":"keep"}`, false},
		}},
	}
	for _, contract := range contracts {
		t.Run(contract.name, func(t *testing.T) {
			sch := compileSchema(t, "../../schema/"+contract.name+".schema.json")
			for _, tc := range contract.cases {
				t.Run(tc.raw, func(t *testing.T) {
					if got := contract.accept([]byte(tc.raw)); got != tc.want {
						t.Errorf("decoder accepts=%v want=%v", got, tc.want)
					}
					if got := sch.Validate(jsonInstance(t, []byte(tc.raw))) == nil; got != tc.want {
						t.Errorf("schema accepts=%v want=%v", got, tc.want)
					}
				})
			}
		})
	}
}

func TestDelegateDefaultsMatchSchema(t *testing.T) {
	sch := compileSchema(t, "../../schema/assistant-result.schema.json")
	for _, fields := range []string{"", `,"mode":"","trigger":""`, `,"mode":null,"trigger":null`} {
		raw := `{"schema_version":1,"kind":"delegate","children":[{"profile_id":"p","title":"t","instruction":"i"` + fields + `}]}`
		env, err := assistants.DecodeResult([]byte(raw))
		if err != nil {
			t.Errorf("decoder rejected admission defaults: %v", err)
		} else if env.Delegate.Children[0].Mode != "" || env.Delegate.Children[0].Trigger != "" {
			t.Fatal("decoder replaced admission-owned defaults")
		}
		if err := sch.Validate(jsonInstance(t, []byte(raw))); err != nil {
			t.Errorf("schema rejected admission defaults: %v", err)
		}
	}
}

func TestOutputSchemaNumericVersionParity(t *testing.T) {
	for _, contract := range []struct {
		name, raw string
		accept    func([]byte) bool
	}{
		{"assistant-result", `{"schema_version":VERSION,"kind":"result","verdict":"pass","summary":"ok"}`, func(b []byte) bool { _, err := assistants.DecodeResult(b); return err == nil }},
		{"assistant-result", `{"schema_version":VERSION,"kind":"delegate","children":[{"profile_id":"p","title":"t","instruction":"i"}]}`, func(b []byte) bool { _, err := assistants.DecodeResult(b); return err == nil }},
		{"verify-result", `{"schema_version":VERSION,"verdict":"pass","summary":"ok"}`, func(b []byte) bool { _, err := verify.DecodeModelResult(b); return err == nil }},
	} {
		for _, number := range []string{"1", "1.0", "1e0", "1.01", "1.00000000000000000000001", "2", `"1.0"`} {
			t.Run(contract.name+"/"+number+"/"+contract.raw, func(t *testing.T) {
				raw := []byte(strings.Replace(contract.raw, "VERSION", number, 1))
				want := number == "1" || number == "1.0" || number == "1e0"
				sch := compileSchema(t, "../../schema/"+contract.name+".schema.json")
				var instance any
				dec := json.NewDecoder(bytes.NewReader(raw))
				dec.UseNumber()
				if err := dec.Decode(&instance); err != nil {
					t.Fatal(err)
				}
				if got := sch.Validate(instance) == nil; got != want {
					t.Fatalf("schema accepted=%v want=%v", got, want)
				}
				if got := contract.accept(raw); got != want {
					t.Fatalf("decoder accepted=%v want=%v", got, want)
				}
			})
		}
	}
}
