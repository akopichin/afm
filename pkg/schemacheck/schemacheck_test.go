package schemacheck_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const (
	flowSchemaPath   = "../../schema/flow.schema.json"
	configSchemaPath = "../../schema/config.schema.json"
)

type schemaDocument map[string]any

func loadSchemaDocument(t *testing.T, path string) schemaDocument {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read schema %s: %v", path, err)
	}
	var doc schemaDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse schema %s: %v", path, err)
	}
	return doc
}

func compileSchema(t *testing.T, path string) *jsonschema.Schema {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("absolute schema path %s: %v", path, err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft7)
	sch, err := compiler.Compile(abs)
	if err != nil {
		t.Fatalf("compile schema %s: %v", path, err)
	}
	return sch
}

func jsonInstance(t *testing.T, yamlData []byte) any {
	t.Helper()
	var value any
	if err := yaml.Unmarshal(yamlData, &value); err != nil {
		t.Fatalf("parse YAML instance: %v", err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("normalize YAML instance as JSON: %v", err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode JSON instance: %v", err)
	}
	return instance
}

func schemaAcceptsYAML(t *testing.T, sch *jsonschema.Schema, body string) bool {
	t.Helper()
	return sch.Validate(jsonInstance(t, []byte(body))) == nil
}

// resolveLocalRef follows the local references used by these schemas.
func resolveLocalRef(t *testing.T, doc schemaDocument, node map[string]any, at string) map[string]any {
	t.Helper()
	ref, _ := node["$ref"].(string)
	if ref == "" {
		return node
	}
	if !strings.HasPrefix(ref, "#/") {
		t.Fatalf("%s: unsupported non-local schema reference %q", at, ref)
	}
	var current any = map[string]any(doc)
	for _, token := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		obj, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("%s: %q does not resolve to an object", at, ref)
		}
		current, ok = obj[token]
		if !ok {
			t.Fatalf("%s: unresolved schema reference %q", at, ref)
		}
	}
	resolved, ok := current.(map[string]any)
	if !ok {
		t.Fatalf("%s: %q does not resolve to a schema object", at, ref)
	}
	return resolveLocalRef(t, doc, resolved, at)
}

func schemaForType(t *testing.T, doc schemaDocument, node map[string]any, want, at string) map[string]any {
	t.Helper()
	node = resolveLocalRef(t, doc, node, at)
	if got, _ := node["type"].(string); got == want {
		return node
	}
	for _, keyword := range []string{"anyOf", "oneOf", "allOf"} {
		alternatives, _ := node[keyword].([]any)
		for _, alternative := range alternatives {
			candidate, ok := alternative.(map[string]any)
			if !ok {
				continue
			}
			candidate = resolveLocalRef(t, doc, candidate, at)
			if got, _ := candidate["type"].(string); got == want {
				return candidate
			}
		}
	}
	t.Fatalf("%s: schema does not contain a %s shape", at, want)
	return nil
}

var (
	buttonsType       = reflect.TypeOf(flow.Buttons{})
	eventSelectorType = reflect.TypeOf(lifecyclehooks.EventSelector{})
	verifySpecType    = reflect.TypeOf(flow.VerifySpec{})
	repeatConfigType  = reflect.TypeOf(flow.RepeatConfig{})
)

func assertTypeCovered(t *testing.T, doc schemaDocument, node map[string]any, typ reflect.Type, at string) {
	t.Helper()
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}

	// Buttons has a deliberately different YAML shape from its Go slice: YAML is an ordered label:prompt
	// mapping. Its field-level shape is covered by the behavioral tests below.
	if typ == buttonsType {
		schemaForType(t, doc, node, "object", at)
		return
	}

	// EventSelector accepts scalar 'all' or an event list; ensure its schema includes the array alternative.
	if typ == eventSelectorType {
		schemaForType(t, doc, node, "array", at)
		return
	}

	// VerifySpec is a deliberate union, not a plain object: the schema is oneOf[string, run-step object,
	// command-step object, array of step objects].
	if typ == verifySpecType {
		schemaForType(t, doc, node, "object", at)
		return
	}

	// RepeatConfig is a boolean-or-object union with custom YAML parsing and no yaml tags on its fields,
	// so the generic struct walk cannot see its keys: check them explicitly.
	if typ == repeatConfigType {
		schemaForType(t, doc, node, "boolean", at)
		obj := schemaForType(t, doc, node, "object", at)
		props, _ := obj["properties"].(map[string]any)
		for _, name := range []string{"max_cycles", "keep_runs"} {
			if _, ok := props[name]; !ok {
				t.Errorf("%s: missing repeat field %q", at, name)
			}
		}
		return
	}

	switch typ.Kind() {
	case reflect.Struct:
		obj := schemaForType(t, doc, node, "object", at)
		props, ok := obj["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s: object schema has no properties", at)
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.PkgPath != "" {
				continue
			}
			name := strings.Split(field.Tag.Get("yaml"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			child, ok := props[name].(map[string]any)
			if !ok {
				t.Errorf("%s: missing yaml field %q from %s", at, name, typ)
				continue
			}
			assertTypeCovered(t, doc, child, field.Type, at+"."+name)
		}
	case reflect.Slice, reflect.Array:
		array := schemaForType(t, doc, node, "array", at)
		items, ok := array["items"].(map[string]any)
		if !ok {
			t.Fatalf("%s: array schema has no object-valued items", at)
		}
		assertTypeCovered(t, doc, items, typ.Elem(), at+"[]")
	case reflect.Map:
		obj := schemaForType(t, doc, node, "object", at)
		additional, ok := obj["additionalProperties"].(map[string]any)
		if !ok {
			t.Fatalf("%s: map schema has no object-valued additionalProperties", at)
		}
		assertTypeCovered(t, doc, additional, typ.Elem(), at+".*")
	default:
		// Scalar fields need no deeper structural walk: their presence in the
		// containing object's properties map was already checked by the caller.
	}
}

func assertCovered(t *testing.T, schemaPath string, structType reflect.Type) {
	t.Helper()
	doc := loadSchemaDocument(t, schemaPath)
	assertTypeCovered(t, doc, map[string]any(doc), structType, structType.Name())
}

func TestFlowSchemaCoversStructAtCorrectPaths(t *testing.T) {
	assertCovered(t, flowSchemaPath, reflect.TypeOf(flow.Flow{}))
}

func TestConfigSchemaCoversStructAtCorrectPaths(t *testing.T) {
	assertCovered(t, configSchemaPath, reflect.TypeOf(config.Config{}))
}

func TestSchemasCompileAsDraft7(t *testing.T) {
	compileSchema(t, flowSchemaPath)
	compileSchema(t, configSchemaPath)
}

func TestFlowSchemaMatchesParserEdgeCases(t *testing.T) {
	sch := compileSchema(t, flowSchemaPath)
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"planning stage", "name: f\nstages:\n  - id: s\n    agents: [planning]\n", true},
		{"autonomous stage", "name: f\nstages:\n  - id: s\n    agents: [auto]\n", true},
		{"script stage", "name: f\nstages:\n  - id: s\n    script: echo ok\n", true},
		{"interactive stage", "name: f\nstages:\n  - id: s\n    interactive: true\n", true},
		{"ready plan stage", "name: f\nstages:\n  - id: s\n    plan: docs/plan.md\n", true},
		{"zero max rules uses default", "name: f\nmemory:\n  path: memory\n  max_rules: 0\nstages:\n  - id: s\n    agents: [planning]\n", true},
		{"buttons", "name: f\nstages:\n  - id: s\n    agents: [planning]\n    buttons:\n      Lint: Run the linter\n", true},
		{"dot id", "name: f\nstages:\n  - id: .\n    agents: [planning]\n", false},
		{"dot-dot id", "name: f\nstages:\n  - id: ..\n    agents: [planning]\n", false},
		{"no runnable mode", "name: f\nstages:\n  - id: s\n", false},
		{"auto mixed with review", "name: f\nstages:\n  - id: s\n    agents: [auto, review]\n", false},
		{"script mixed with agents", "name: f\nstages:\n  - id: s\n    script: echo ok\n    agents: [planning]\n", false},
		{"script mixed with command", "name: f\nstages:\n  - id: s\n    script: echo ok\n    command: claude\n", false},
		{"script mixed with interactive", "name: f\nstages:\n  - id: s\n    script: echo ok\n    interactive: true\n", false},
		{"script mixed with plan", "name: f\nstages:\n  - id: s\n    script: echo ok\n    plan: plan.md\n", false},
		{"script mixed with verify", "name: f\nstages:\n  - id: s\n    script: echo ok\n    verify: go test ./...\n", false},
		{"script mixed with object verify", "name: f\nstages:\n  - id: s\n    script: echo ok\n    verify:\n      run: go test ./...\n", false},
		{"script mixed with list verify", "name: f\nstages:\n  - id: s\n    script: echo ok\n    verify:\n      - run: go test ./...\n", false},
		{"verify run object", "name: f\nstages:\n  - id: s\n    agents: [planning, implementation]\n    verify:\n      run: go test ./...\n", true},
		{"verify command object", "name: f\nstages:\n  - id: s\n    agents: [planning, implementation]\n    verify:\n      command: codex\n      prompt: check it\n", true},
		{"verify list of steps", "name: f\nstages:\n  - id: s\n    agents: [planning, implementation]\n    verify:\n      - run: go test ./...\n      - command: codex\n", true},
		{"verify run and command together", "name: f\nstages:\n  - id: s\n    agents: [implementation]\n    verify:\n      run: go test ./...\n      command: codex\n", false},
		{"verify unknown agent field", "name: f\nstages:\n  - id: s\n    agents: [implementation]\n    verify:\n      agent: codex\n", false},
		{"verify empty object", "name: f\nstages:\n  - id: s\n    agents: [implementation]\n    verify: {}\n", false},
		{"verify empty list", "name: f\nstages:\n  - id: s\n    agents: [implementation]\n    verify: []\n", false},
		{"verify container form", "name: f\nstages:\n  - id: s\n    agents: [planning, implementation]\n    verify:\n      steps:\n        - run: go test ./...\n        - command: codex\n      max_failures: 3\n", true},
		{"verify container zero max_failures", "name: f\nstages:\n  - id: s\n    agents: [planning, implementation]\n    verify:\n      steps:\n        - run: go test ./...\n      max_failures: 0\n", true},
		{"verify container without max_failures", "name: f\nstages:\n  - id: s\n    agents: [planning, implementation]\n    verify:\n      steps:\n        - run: go test ./...\n", true},
		{"verify container negative max_failures", "name: f\nstages:\n  - id: s\n    agents: [planning, implementation]\n    verify:\n      steps:\n        - run: go test ./...\n      max_failures: -1\n", false},
		{"verify container empty steps", "name: f\nstages:\n  - id: s\n    agents: [planning, implementation]\n    verify:\n      steps: []\n      max_failures: 2\n", false},
		{"verify container unknown key", "name: f\nstages:\n  - id: s\n    agents: [planning, implementation]\n    verify:\n      steps:\n        - run: go test ./...\n      bogus: x\n", false},
		{"verify container review_patience", "name: f\nstages:\n  - id: s\n    agents: [planning, implementation]\n    verify:\n      steps:\n        - command: codex\n      review_patience: 2\n", true},
		{"verify container max_failures and review_patience", "name: f\nstages:\n  - id: s\n    agents: [planning, implementation]\n    verify:\n      steps:\n        - run: go test ./...\n        - command: codex\n      max_failures: 6\n      review_patience: 2\n", true},
		{"verify container zero review_patience", "name: f\nstages:\n  - id: s\n    agents: [planning, implementation]\n    verify:\n      steps:\n        - command: codex\n      review_patience: 0\n", true},
		{"verify container negative review_patience", "name: f\nstages:\n  - id: s\n    agents: [planning, implementation]\n    verify:\n      steps:\n        - command: codex\n      review_patience: -1\n", false},
		{"verify container fresh_correction false", "name: f\nstages:\n  - id: s\n    agents: [planning, implementation]\n    verify:\n      steps:\n        - run: go test ./...\n      fresh_correction: false\n", true},
		{"script mixed with buttons", "name: f\nstages:\n  - id: s\n    script: echo ok\n    buttons:\n      Retry: Try again\n", false},
		{"empty button label", "name: f\nstages:\n  - id: s\n    agents: [planning]\n    buttons:\n      \"\": Try again\n", false},
		{"empty button prompt", "name: f\nstages:\n  - id: s\n    agents: [planning]\n    buttons:\n      Retry: \"\"\n", false},
		{"reflect without memory path", "name: f\nstages:\n  - id: s\n    agents: [planning]\n    reflect:\n      file: stage.md\n", false},
		{"parent reflect path", "name: f\nmemory:\n  path: memory\nstages:\n  - id: s\n    agents: [planning]\n    reflect:\n      file: ../escape.md\n", false},
		{"absolute reflect path", "name: f\nmemory:\n  path: memory\nstages:\n  - id: s\n    agents: [planning]\n    reflect:\n      file: /tmp/escape.md\n", false},
		{"reserved reflect path", "name: f\nmemory:\n  path: memory\nstages:\n  - id: s\n    agents: [planning]\n    reflect:\n      file: ./memory.md\n", false},
		{"collaboration max_depth", "name: f\ncollaboration:\n  max_depth: 2\nstages:\n  - id: s\n    agents: [planning]\n", true},
		{"collaboration max_depth zero", "name: f\ncollaboration:\n  max_depth: 0\nstages:\n  - id: s\n    agents: [planning]\n", true},
		{"collaboration negative max_depth", "name: f\ncollaboration:\n  max_depth: -1\nstages:\n  - id: s\n    agents: [planning]\n", false},
		{"collaboration mode team", "name: f\ncollaboration:\n  mode: team\nstages:\n  - id: s\n    agents: [planning]\n", true},
		{"collaboration mode assistants", "name: f\ncollaboration:\n  mode: assistants\nstages:\n  - id: s\n    agents: [planning]\n", true},
		{"collaboration mode with max_depth", "name: f\ncollaboration:\n  max_depth: 2\n  mode: assistants\nstages:\n  - id: s\n    agents: [planning]\n", true},
		{"collaboration mode unknown", "name: f\ncollaboration:\n  mode: solo\nstages:\n  - id: s\n    agents: [planning]\n", false},
		{"collaboration mode wrong case", "name: f\ncollaboration:\n  mode: Team\nstages:\n  - id: s\n    agents: [planning]\n", false},
		{"collaboration mode empty string", "name: f\ncollaboration:\n  mode: \"\"\nstages:\n  - id: s\n    agents: [planning]\n", false},
		{"collaboration mode null", "name: f\ncollaboration:\n  mode: null\nstages:\n  - id: s\n    agents: [planning]\n", false},
		{"collaboration mode bare key", "name: f\ncollaboration:\n  mode:\nstages:\n  - id: s\n    agents: [planning]\n", false},
		{"collaboration mode number", "name: f\ncollaboration:\n  mode: 1\nstages:\n  - id: s\n    agents: [planning]\n", false},
		{"collaboration mode list", "name: f\ncollaboration:\n  mode: [team]\nstages:\n  - id: s\n    agents: [planning]\n", false},
		{"collaboration enabled is rejected (no per-flow enable)", "name: f\ncollaboration:\n  enabled: true\nstages:\n  - id: s\n    agents: [planning]\n", false},
		{"assistants scalar", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    assistants: reviewer\n", true},
		{"assistants list of names", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    assistants: [reviewer, qa]\n", true},
		{"assistants object form", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    assistants:\n      - profile: qa\n        title: QA gate\n        instruction: check coverage\n        mode: required\n        trigger: on_completion\n", true},
		{"assistants mixed list", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    assistants:\n      - reviewer\n      - profile: qa\n        mode: required\n", true},
		{"assistants mixed with script", "name: f\nstages:\n  - id: s\n    script: echo ok\n    assistants: reviewer\n", false},
		{"assistants empty profile name", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    assistants: [\"\"]\n", false},
		{"assistants duplicate profile", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    assistants: [reviewer, reviewer]\n", false},
		{"assistants invalid mode", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    assistants:\n      - profile: reviewer\n        mode: advisry\n", false},
		{"assistants invalid trigger", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    assistants:\n      - profile: reviewer\n        trigger: on_complete\n", false},
		{"assistants explicit empty mode and trigger", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    assistants:\n      - profile: reviewer\n        mode: \"\"\n        trigger: \"\"\n", true},
		{"team member", "name: f\nteam:\n  - id: arch\n    profile: rev-arch\n    title: Architecture\n    instruction: Review when asked.\nstages:\n  - id: s\n    agents: [auto]\n", true},
		{"team member without instruction", "name: f\nteam:\n  - id: qa\n    profile: qa\n    title: QA\nstages:\n  - id: s\n    agents: [auto]\n", true},
		{"team member without id", "name: f\nteam:\n  - profile: qa\n    title: QA\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"team member unsafe id", "name: f\nteam:\n  - id: a/b\n    profile: qa\n    title: QA\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"team member without profile", "name: f\nteam:\n  - id: qa\n    title: QA\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"team member empty title", "name: f\nteam:\n  - id: qa\n    profile: qa\n    title: \"\"\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"team member unknown key", "name: f\nteam:\n  - id: qa\n    profile: qa\n    title: QA\n    alias: q\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"team null", "name: f\nteam: null\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"team empty value", "name: f\nteam:\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"team empty list", "name: f\nteam: []\nstages:\n  - id: s\n    agents: [auto]\n", true},
		{"team as object", "name: f\nteam:\n  id: qa\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"repeat missing key", "name: f\nstages:\n  - id: s\n    agents: [auto]\n", true},
		{"repeat false", "name: f\nrepeat: false\nstages:\n  - id: s\n    agents: [auto]\n", true},
		{"repeat true", "name: f\nrepeat: true\nstages:\n  - id: s\n    agents: [auto]\n", true},
		{"repeat mapping defaults", "name: f\nrepeat:\n  max_cycles: 3\nstages:\n  - id: s\n    agents: [auto]\n", true},
		{"repeat empty mapping", "name: f\nrepeat: {}\nstages:\n  - id: s\n    agents: [auto]\n", true},
		{"repeat explicit keep_runs zero", "name: f\nrepeat:\n  keep_runs: 0\nstages:\n  - id: s\n    agents: [auto]\n", true},
		{"repeat explicit max_cycles zero", "name: f\nrepeat:\n  max_cycles: 0\n  keep_runs: 5\nstages:\n  - id: s\n    agents: [auto]\n", true},
		{"repeat full mapping", "name: f\nrepeat:\n  max_cycles: 4\n  keep_runs: 1\nstages:\n  - id: s\n    agents: [auto]\n", true},
		{"repeat negative max_cycles", "name: f\nrepeat:\n  max_cycles: -1\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"repeat negative keep_runs", "name: f\nrepeat:\n  keep_runs: -1\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"repeat unknown key", "name: f\nrepeat:\n  max_cycle: 3\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"repeat string instead of bool", "name: f\nrepeat: \"true\"\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"repeat number", "name: f\nrepeat: 3\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"repeat list", "name: f\nrepeat: [1]\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"repeat max_cycles not a number", "name: f\nrepeat:\n  max_cycles: many\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"repeat fractional keep_runs", "name: f\nrepeat:\n  keep_runs: 1.5\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"repeat yaml 1.1 bool yes", "name: f\nrepeat: yes\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"repeat yaml 1.1 bool on", "name: f\nrepeat: on\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"repeat quoted yaml 1.1 bool", "name: f\nrepeat: \"yes\"\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"repeat integral float max_cycles", "name: f\nrepeat:\n  max_cycles: 1.0\nstages:\n  - id: s\n    agents: [auto]\n", true},
		{"repeat exponent float max_cycles", "name: f\nrepeat:\n  max_cycles: 1e0\nstages:\n  - id: s\n    agents: [auto]\n", true},
		{"repeat fractional max_cycles", "name: f\nrepeat:\n  max_cycles: 1.5\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"repeat null", "name: f\nrepeat: null\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"repeat bare key", "name: f\nrepeat:\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"stage skills scalar", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills: tdd\n", true},
		{"stage skills list", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills: [tdd, verification, tdd]\n", true},
		{"stage skills namespace", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills: superpowers:test-driven-development\n", true},
		{"stage skills empty list", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills: []\n", true},
		{"stage skills empty name", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills: \"\"\n", false},
		{"stage skills control character", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills: \"a\\tb\"\n", false},
		{"stage skills control character in list", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills: [ok, \"a\\u0085b\"]\n", false},
		{"stage skills blank name in list", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills: [a, \"  \"]\n", false},
		{"stage skills null", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills: null\n", false},
		{"stage skills bare key", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills:\n", false},
		{"stage skills number", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills: 5\n", false},
		{"stage skills mixed list", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills: [a, 1]\n", false},
		{"stage skills object", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills: {a: b}\n", false},
		{"stage skills nested list", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills: [[a]]\n", false},
		{"stage skills_dir", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills_dir: ./skills\n", true},
		{"stage skills_dir empty", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills_dir: \"\"\n", false},
		{"stage skills_dir blank", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills_dir: \"  \"\n", false},
		{"stage skills_dir null", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills_dir: null\n", false},
		{"stage skills_dir bare key", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills_dir:\n", false},
		{"stage skills_dir number", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills_dir: 5\n", false},
		{"stage skills_dir tilde", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills_dir: ~/skills\n", false},
		{"flow skills_dir", "name: f\nskills_dir: /opt/skills\nstages:\n  - id: s\n    agents: [auto]\n", true},
		{"flow skills_dir empty", "name: f\nskills_dir: \"\"\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"flow skills_dir null", "name: f\nskills_dir: null\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"flow skills_dir bare key", "name: f\nskills_dir:\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"flow skills_dir newline", "name: f\nskills_dir: \"a\\nb\"\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"stage skills_dir newline", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    skills_dir: \"a\\nb\"\n", false},
		{"team skills_dir newline", "name: f\nteam:\n  - id: qa\n    profile: qa\n    title: QA\n    skills_dir: \"a\\nb\"\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"flow skills_dir tilde", "name: f\nskills_dir: ~/skills\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"flow skills_dir list", "name: f\nskills_dir: [a]\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"team skills scalar", "name: f\nteam:\n  - id: qa\n    profile: qa\n    title: QA\n    skills: tdd\nstages:\n  - id: s\n    agents: [auto]\n", true},
		{"team skills list and dir", "name: f\nteam:\n  - id: qa\n    profile: qa\n    title: QA\n    skills: [tdd, verification]\n    skills_dir: ./skills\nstages:\n  - id: s\n    agents: [auto]\n", true},
		{"team skills empty list", "name: f\nteam:\n  - id: qa\n    profile: qa\n    title: QA\n    skills: []\nstages:\n  - id: s\n    agents: [auto]\n", true},
		{"team skills control character", "name: f\nteam:\n  - id: qa\n    profile: qa\n    title: QA\n    skills: \"a\\nb\"\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"team skills empty name", "name: f\nteam:\n  - id: qa\n    profile: qa\n    title: QA\n    skills: \"\"\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"team skills null", "name: f\nteam:\n  - id: qa\n    profile: qa\n    title: QA\n    skills: null\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"team skills bare key", "name: f\nteam:\n  - id: qa\n    profile: qa\n    title: QA\n    skills:\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"team skills number", "name: f\nteam:\n  - id: qa\n    profile: qa\n    title: QA\n    skills: 5\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"team skills mixed list", "name: f\nteam:\n  - id: qa\n    profile: qa\n    title: QA\n    skills: [a, 1]\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"team skills object", "name: f\nteam:\n  - id: qa\n    profile: qa\n    title: QA\n    skills: {a: b}\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"team skills_dir empty", "name: f\nteam:\n  - id: qa\n    profile: qa\n    title: QA\n    skills_dir: \"\"\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"team skills_dir null", "name: f\nteam:\n  - id: qa\n    profile: qa\n    title: QA\n    skills_dir: null\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"team skills_dir number", "name: f\nteam:\n  - id: qa\n    profile: qa\n    title: QA\n    skills_dir: 5\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"team skills_dir tilde", "name: f\nteam:\n  - id: qa\n    profile: qa\n    title: QA\n    skills_dir: ~/skills\nstages:\n  - id: s\n    agents: [auto]\n", false},
		{"assistants unknown object key", "name: f\nstages:\n  - id: s\n    agents: [auto]\n    assistants:\n      - profile: reviewer\n        triger: on_completion\n", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := schemaAcceptsYAML(t, sch, tt.body)
			if got != tt.want {
				t.Errorf("schema accepts = %v, want %v", got, tt.want)
			}

			path := filepath.Join(t.TempDir(), "flow.yaml")
			if err := os.WriteFile(path, []byte(tt.body), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := flow.ParseFile(path)
			runtimeAccepts := err == nil
			if runtimeAccepts != tt.want {
				t.Errorf("flow.ParseFile accepts = %v, want %v (error: %v)", runtimeAccepts, tt.want, err)
			}
		})
	}
}

func TestRepositoryYAMLExamplesMatchSchemas(t *testing.T) {
	flowSchema := compileSchema(t, flowSchemaPath)
	flowExamples, err := filepath.Glob("../../examples/*/flow.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(flowExamples) == 0 {
		t.Fatal("no flow examples found")
	}
	for _, path := range flowExamples {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := flowSchema.Validate(jsonInstance(t, data)); err != nil {
				t.Errorf("%s does not match flow schema: %v", path, err)
			}
		})
	}

	configSchema := compileSchema(t, configSchemaPath)
	exampleConfigs, err := filepath.Glob("../../examples/*/.afm/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range append([]string{"../../config.example.yaml"}, exampleConfigs...) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := configSchema.Validate(jsonInstance(t, data)); err != nil {
			t.Errorf("%s does not match config schema: %v", path, err)
		}
	}
}

func TestConfigSchemaMatchesSkillsDir(t *testing.T) {
	sch := compileSchema(t, configSchemaPath)
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"absolute path", "skills_dir: /opt/skills\n", true},
		{"relative path", "skills_dir: ./skills\n", true},
		{"empty string", "skills_dir: \"\"\n", false},
		{"blank string", "skills_dir: \"  \"\n", false},
		{"null", "skills_dir: null\n", false},
		{"bare key", "skills_dir:\n", false},
		{"number", "skills_dir: 5\n", false},
		{"list", "skills_dir: [a]\n", false},
		{"tilde", "skills_dir: ~/skills\n", false},
		{"newline", "skills_dir: \"a\\nb\"\n", false},
		{"c1 control character", "skills_dir: \"a\\u0085b\"\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := schemaAcceptsYAML(t, sch, tt.body); got != tt.want {
				t.Errorf("schema accepts = %v, want %v", got, tt.want)
			}
			projectDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte(tt.body), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := config.LoadFrom(t.TempDir(), projectDir)
			if runtimeAccepts := err == nil; runtimeAccepts != tt.want {
				t.Errorf("config.LoadFrom accepts = %v, want %v (error: %v)", runtimeAccepts, tt.want, err)
			}
		})
	}
}

func TestConfigSchemaMatchesAddStageSwitch(t *testing.T) {
	sch := compileSchema(t, configSchemaPath)
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"enabled false", "add_stage:\n  enabled: false\n", true},
		{"enabled true", "add_stage:\n  enabled: true\n", true},
		{"empty mapping", "add_stage: {}\n", true},
		{"string instead of bool", "add_stage:\n  enabled: \"false\"\n", false},
		{"number instead of bool", "add_stage:\n  enabled: 0\n", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := schemaAcceptsYAML(t, sch, tt.body); got != tt.want {
				t.Errorf("schema accepts = %v, want %v", got, tt.want)
			}

			projectDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte(tt.body), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := config.LoadFrom(t.TempDir(), projectDir)
			if runtimeAccepts := err == nil; runtimeAccepts != tt.want {
				t.Errorf("config.LoadFrom accepts = %v, want %v (error: %v)", runtimeAccepts, tt.want, err)
			}
		})
	}
}
