package schema

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

var nativeContracts = []Contract{VerifyResult, AssistantResultOnly, Question}

var nativeDialects = []Dialect{DialectClaude, DialectStrict}

// validPayloads is the round-trip corpus: answers each original contract accepts.
var validPayloads = map[Contract][]string{
	VerifyResult: {
		`{"schema_version":1,"verdict":"pass","summary":"ok"}`,
		`{"schema_version":1,"verdict":"pass","summary":"ok","findings":[{"blocking":false,"title":"nit"}]}`,
		`{"schema_version":1,"verdict":"inconclusive","summary":"x","findings":null}`,
		`{"schema_version":1,"verdict":"needs_changes","summary":["line 1","line 2"],"findings":[{"blocking":true,"title":"t","path":"a.go","line_start":3,"line_end":4,"requirement":["r"],"evidence":"e","minimal_fix":"m"}]}`,
	},
	AssistantResultOnly: {
		`{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok"}`,
		`{"schema_version":1,"kind":"result","verdict":"inconclusive","summary":["a","b"],"findings":null,"evidence":"checked","context_version":null}`,
		`{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"s","findings":[{"id":"f1","severity":"major","path":"a.go","line_start":1,"line_end":2,"detail":["d"]}],"evidence":["e"],"suggested_change":"fix","limitations":"none","context_version":"v1"}`,
	},
	Question: {
		`{"question":"Pick?","options":["a","b"],"allow_custom":false}`,
		`{"question":"Free?"}`,
		`{"id":"q1","question":"x","options":null,"allow_custom":null}`,
	},
}

func compileSchema(t *testing.T, raw string) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("schema is not JSON: %v\n%s", err, raw)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("https://afm.invalid/native.json", doc); err != nil {
		t.Fatal(err)
	}
	sch, err := compiler.Compile("https://afm.invalid/native.json")
	if err != nil {
		t.Fatalf("compile: %v\n%s", err, raw)
	}
	return sch
}

func validateAgainst(sch *jsonschema.Schema, raw string) error {
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
	if err != nil {
		return err
	}
	return sch.Validate(doc)
}

func mustNative(t *testing.T, contract Contract, dialect Dialect) string {
	t.Helper()
	raw, ok := NativeJSON(contract, dialect)
	if !ok {
		t.Fatalf("NativeJSON(%s, %s) has no variant", contract, dialect)
	}
	return raw
}

func decodeObject(t *testing.T, raw string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, raw)
	}
	return doc
}

// walkSchemaNodes calls visit for every schema node reachable through subschema positions.
func walkSchemaNodes(node map[string]any, visit func(map[string]any)) {
	visit(node)
	if items, ok := node["items"].(map[string]any); ok {
		walkSchemaNodes(items, visit)
	}
	properties, _ := node["properties"].(map[string]any)
	for _, child := range properties {
		walkSchemaNodes(child.(map[string]any), visit)
	}
	anyOf, _ := node["anyOf"].([]any)
	for _, child := range anyOf {
		walkSchemaNodes(child.(map[string]any), visit)
	}
}

func TestNativeJSON_ClaudeDialectHasAPlainObjectRoot(t *testing.T) {
	for _, contract := range nativeContracts {
		raw := mustNative(t, contract, DialectClaude)
		root := decodeObject(t, raw)
		if root["type"] != "object" {
			t.Errorf("%s: root type = %v, want object", contract, root["type"])
		}
		for _, key := range []string{"oneOf", "anyOf", "allOf", "not", "$schema", "$id", "$ref", "definitions"} {
			if _, found := root[key]; found {
				t.Errorf("%s: root keeps %q", contract, key)
			}
		}
		if strings.Contains(raw, `"$ref"`) {
			t.Errorf("%s: a $ref was not inlined: %s", contract, raw)
		}
	}
}

func TestNativeJSON_ClaudeDialectOfResultOnlyIsTheResultDefinition(t *testing.T) {
	root := decodeObject(t, mustNative(t, AssistantResultOnly, DialectClaude))
	required, _ := root["required"].([]any)
	if !slices.Contains(required, any("kind")) {
		t.Fatalf("root required = %v, want the result definition's fields", required)
	}
	properties := root["properties"].(map[string]any)
	findings := properties["findings"].(map[string]any)
	if _, inlined := findings["items"].(map[string]any)["properties"]; !inlined {
		t.Fatalf("findings items = %v, want the finding definition inlined", findings["items"])
	}
}

func TestNativeJSON_StrictDialectKeepsOnlyAllowedKeywordsAndClosesObjects(t *testing.T) {
	for _, contract := range nativeContracts {
		raw := mustNative(t, contract, DialectStrict)
		walkSchemaNodes(decodeObject(t, raw), func(node map[string]any) {
			for key := range node {
				if !IsStrictKeyword(key) {
					t.Errorf("%s: keyword %q is not allowed in strict mode: %v", contract, key, node)
				}
			}
			properties, isObject := node["properties"].(map[string]any)
			if !isObject {
				return
			}
			if closed, isBool := node["additionalProperties"].(bool); !isBool || closed {
				t.Errorf("%s: object not closed: %v", contract, node)
			}
			required, _ := node["required"].([]any)
			if len(required) != len(properties) {
				t.Errorf("%s: required %v does not list every property of %v", contract, required, node)
			}
			for _, name := range required {
				if _, found := properties[name.(string)]; !found {
					t.Errorf("%s: required %q is not a property", contract, name)
				}
			}
		})
	}
}

// A strict-mode model must send every property, so an optional one that cannot be null gets null as its "absent".
func TestNativeJSON_StrictDialectMakesOptionalPropertiesNullable(t *testing.T) {
	root := decodeObject(t, mustNative(t, AssistantResultOnly, DialectStrict))
	properties := root["properties"].(map[string]any)
	for _, name := range []string{"evidence", "suggested_change", "limitations"} {
		if !acceptsNull(properties[name].(map[string]any)) {
			t.Errorf("optional %q does not accept null: %v", name, properties[name])
		}
	}
	for _, name := range []string{"summary", "verdict", "kind", "schema_version"} {
		if acceptsNull(properties[name].(map[string]any)) {
			t.Errorf("required %q accepts null: %v", name, properties[name])
		}
	}
	if properties["kind"].(map[string]any)["enum"] == nil {
		t.Errorf("kind = %v, want const turned into an enum", properties["kind"])
	}
}

// A model that sees a const or enum without a type may write the const 1 as the string "1"; the native mode then
// rejects every try.
func TestNativeJSON_TypesEveryConstAndEnum(t *testing.T) {
	wantTypes := map[Contract]map[string]string{
		VerifyResult:        {"schema_version": "integer", "verdict": "string"},
		AssistantResultOnly: {"schema_version": "integer", "kind": "string", "verdict": "string"},
		Question:            {},
	}
	for _, contract := range nativeContracts {
		for _, dialect := range nativeDialects {
			root := decodeObject(t, mustNative(t, contract, dialect))
			walkSchemaNodes(root, func(node map[string]any) {
				_, hasConst := node["const"]
				_, hasEnum := node["enum"]
				if (hasConst || hasEnum) && node["type"] == nil {
					t.Errorf("%s/%s: %v pins values without a type", contract, dialect, node)
				}
			})
			properties := root["properties"].(map[string]any)
			for name, want := range wantTypes[contract] {
				if got := properties[name].(map[string]any)["type"]; got != want {
					t.Errorf("%s/%s: %s type = %v, want %s", contract, dialect, name, got, want)
				}
			}
		}
	}
}

func TestTypeConstAndEnum_TakesTheTypeTheValuesShare(t *testing.T) {
	tests := []struct{ name, node, want string }{
		{"integer", `{"const":1}`, `{"const":1,"type":"integer"}`},
		{"non-integral number", `{"enum":[0.5,1.5]}`, `{"enum":[0.5,1.5],"type":"number"}`},
		{"string", `{"enum":["a","b"]}`, `{"enum":["a","b"],"type":"string"}`},
		{"boolean", `{"const":true}`, `{"const":true,"type":"boolean"}`},
		{"own type stays", `{"type":["string","null"],"enum":["a",null]}`, `{"type":["string","null"],"enum":["a",null]}`},
		{"mixed types stay open", `{"enum":["a",1]}`, `{"enum":["a",1]}`},
		{"null stays open", `{"enum":["a",null]}`, `{"enum":["a",null]}`},
		{"object const stays open", `{"const":{"a":1}}`, `{"const":{"a":1}}`},
		{
			name: "nested subschemas, not values",
			node: `{"type":"object","properties":{"const":{"const":"x"},"list":{"type":"array","items":{"anyOf":[{"const":2},{"type":"null"}]}}},"default":{"const":3}}`,
			want: `{"type":"object","properties":{"const":{"const":"x","type":"string"},"list":{"type":"array","items":{"anyOf":[{"const":2,"type":"integer"},{"type":"null"}]}}},"default":{"const":3}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := decodeObject(t, tt.node)
			typeConstAndEnum(node)
			got, _ := json.Marshal(node)
			want, _ := json.Marshal(decodeObject(t, tt.want))
			if !bytes.Equal(got, want) {
				t.Fatalf("typed = %s, want %s", got, want)
			}
		})
	}
}

// The types only guide the model: the original contract, the acceptance gate, stays as written.
func TestNativeJSON_LeavesTheContractAsWritten(t *testing.T) {
	for _, contract := range append(slices.Clone(nativeContracts), AssistantResult) {
		before := JSON(contract)
		for _, dialect := range nativeDialects {
			NativeJSON(contract, dialect)
		}
		NormalizeNative(contract, []byte(`{"schema_version":1,"evidence":null}`))
		if JSON(contract) != before {
			t.Errorf("%s: the contract changed after deriving its native variants", contract)
		}
	}
	properties := decodeObject(t, JSON(VerifyResult))["properties"].(map[string]any)
	if got, _ := json.Marshal(properties["schema_version"]); string(got) != `{"const":1}` {
		t.Errorf("verify-result schema_version = %s, want the contract's own {\"const\":1}", got)
	}
}

// Delegation's root alternatives have no native form, so a delegating profile keeps the schema in the prompt.
func TestNativeJSON_NoVariantForTheDelegatingContract(t *testing.T) {
	for _, dialect := range nativeDialects {
		if raw, ok := NativeJSON(AssistantResult, dialect); ok || raw != "" {
			t.Errorf("NativeJSON(assistant-result, %s) = %q, %v; want no variant", dialect, raw, ok)
		}
	}
}

func TestNativeJSON_IsDeterministic(t *testing.T) {
	for _, contract := range nativeContracts {
		for _, dialect := range nativeDialects {
			first, second := mustNative(t, contract, dialect), mustNative(t, contract, dialect)
			if first != second {
				t.Errorf("NativeJSON(%s, %s) differs between calls", contract, dialect)
			}
		}
	}
}

// fillNulls gives every property the strict schema lists but value lacks a null, as a strict-mode model does.
func fillNulls(value any, node map[string]any) any {
	switch v := value.(type) {
	case map[string]any:
		properties, _ := node["properties"].(map[string]any)
		for name, child := range properties {
			if item, present := v[name]; present {
				v[name] = fillNulls(item, child.(map[string]any))
			} else {
				v[name] = nil
			}
		}
	case []any:
		if items, ok := node["items"].(map[string]any); ok {
			for i, item := range v {
				v[i] = fillNulls(item, items)
			}
		}
	default:
		// A scalar has no properties to fill.
	}
	return value
}

// withoutNulls drops null-valued properties at any depth: for the decoders a null optional field is an absent one.
func withoutNulls(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for name, item := range v {
			if item == nil {
				delete(v, name)
			} else {
				v[name] = withoutNulls(item)
			}
		}
	case []any:
		for i, item := range v {
			v[i] = withoutNulls(item)
		}
	default:
		// A scalar has no properties to drop.
	}
	return value
}

func decodeValue(t *testing.T, raw string) any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
	}
	return v
}

// Every accepted answer fits the Claude variant; filled with nulls it fits the strict variant, and NormalizeNative
// turns that back into an answer the original contract accepts, with the same content.
func TestNativeJSON_RoundTripCorpus(t *testing.T) {
	for _, contract := range nativeContracts {
		claude := compileSchema(t, mustNative(t, contract, DialectClaude))
		strictRaw := mustNative(t, contract, DialectStrict)
		strict := compileSchema(t, strictRaw)
		for _, payload := range validPayloads[contract] {
			if err := Validate(contract, []byte(payload)); err != nil {
				t.Fatalf("%s: corpus payload is invalid: %v\n%s", contract, err, payload)
			}
			if err := validateAgainst(claude, payload); err != nil {
				t.Errorf("%s: claude variant rejects %s: %v", contract, payload, err)
			}
			filled, _ := json.Marshal(fillNulls(decodeValue(t, payload), decodeObject(t, strictRaw)))
			if err := validateAgainst(strict, string(filled)); err != nil {
				t.Errorf("%s: strict variant rejects %s: %v", contract, filled, err)
			}
			normalized := NormalizeNative(contract, filled)
			if err := Validate(contract, normalized); err != nil {
				t.Errorf("%s: normalized %s fails the contract: %v", contract, normalized, err)
			}
			if got, want := withoutNulls(decodeValue(t, string(normalized))), withoutNulls(decodeValue(t, payload)); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: normalized content = %v, want %v", contract, got, want)
			}
		}
	}
}

// Rules that live only in dropped keywords (root combinators, if/then, contains, patterns) still reach the
// original contract: a native answer breaking one is rejected there.
func TestNativeJSON_DroppedRulesStayWithTheContract(t *testing.T) {
	tests := []struct {
		name     string
		contract Contract
		payload  string
		dialects []Dialect
	}{
		{"verify pass with a blocking finding", VerifyResult, `{"schema_version":1,"verdict":"pass","summary":"ok","findings":[{"blocking":true,"title":"bug"}]}`, nativeDialects},
		{"verify needs_changes without findings", VerifyResult, `{"schema_version":1,"verdict":"needs_changes","summary":"x"}`, nativeDialects},
		{"verify blank summary", VerifyResult, `{"schema_version":1,"verdict":"inconclusive","summary":"   "}`, []Dialect{DialectStrict}},
		{"assistant pass with a blocker", AssistantResultOnly, `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","findings":[{"id":"f","severity":"blocker","detail":"d"}]}`, nativeDialects},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, dialect := range tt.dialects {
				variant := mustNative(t, tt.contract, dialect)
				answer := []byte(tt.payload)
				if dialect == DialectStrict {
					answer, _ = json.Marshal(fillNulls(decodeValue(t, tt.payload), decodeObject(t, variant)))
				}
				if err := validateAgainst(compileSchema(t, variant), string(answer)); err != nil {
					t.Fatalf("%s variant rejects it, so the test proves nothing: %v", dialect, err)
				}
				if err := Validate(tt.contract, NormalizeNative(tt.contract, answer)); err == nil {
					t.Fatalf("contract accepts %s from the %s variant", answer, dialect)
				}
			}
		})
	}
}

func TestNormalizeNative_DropsOnlyNullsTheContractRejects(t *testing.T) {
	tests := []struct {
		name     string
		contract Contract
		raw      string
		want     string
	}{
		{
			name:     "optional free text",
			contract: AssistantResultOnly,
			raw:      `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","evidence":null,"suggested_change":null,"limitations":null,"findings":null,"context_version":null}`,
			want:     `{"context_version":null,"findings":null,"kind":"result","schema_version":1,"summary":"ok","verdict":"pass"}`,
		},
		{
			name:     "required field stays for the validator",
			contract: AssistantResultOnly,
			raw:      `{"schema_version":1,"kind":"result","verdict":"pass","summary":null}`,
			want:     `{"schema_version":1,"kind":"result","verdict":"pass","summary":null}`,
		},
		{
			name:     "verify answer is unchanged",
			contract: VerifyResult,
			raw:      `{"schema_version":1,"verdict":"pass","summary":"ok","findings":[{"blocking":null,"title":null,"path":null,"line_start":null,"line_end":null,"requirement":null,"evidence":null,"minimal_fix":null}]}`,
			want:     `{"schema_version":1,"verdict":"pass","summary":"ok","findings":[{"blocking":null,"title":null,"path":null,"line_start":null,"line_end":null,"requirement":null,"evidence":null,"minimal_fix":null}]}`,
		},
		{
			name:     "duplicate keys are left for the validator",
			contract: AssistantResultOnly,
			raw:      `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","summary":"again","evidence":null}`,
			want:     `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","summary":"again","evidence":null}`,
		},
		{"not JSON", AssistantResultOnly, `{"evidence":null`, `{"evidence":null`},
		{
			name:     "a second document is left for the validator",
			contract: AssistantResultOnly,
			raw:      `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","evidence":null} {"verdict":"needs_changes"}`,
			want:     `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","evidence":null} {"verdict":"needs_changes"}`,
		},
		{
			name:     "trailing text is left for the validator",
			contract: AssistantResultOnly,
			raw:      `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","evidence":null} DO NOT ACCEPT`,
			want:     `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","evidence":null} DO NOT ACCEPT`,
		},
		{"no variant", AssistantResult, `{"evidence":null}`, `{"evidence":null}`},
		{
			name:     "text keeps its characters",
			contract: AssistantResultOnly,
			raw:      `{"schema_version":1,"kind":"result","verdict":"pass","summary":"a < b && c","evidence":null}`,
			want:     `{"kind":"result","schema_version":1,"summary":"a < b && c","verdict":"pass"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeNative(tt.contract, []byte(tt.raw)); !bytes.Equal(got, []byte(tt.want)) {
				t.Fatalf("NormalizeNative = %s, want %s", got, tt.want)
			}
		})
	}
}
