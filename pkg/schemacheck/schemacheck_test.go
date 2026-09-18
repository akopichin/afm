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

// resolveLocalRef follows the local references used by these schemas. Keeping
// the walk structural is important: a stage.path field must be found under the
// stage definition, not accidentally satisfied by memory.path elsewhere.
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
)

func assertTypeCovered(t *testing.T, doc schemaDocument, node map[string]any, typ reflect.Type, at string) {
	t.Helper()
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}

	// Buttons has a deliberately different YAML shape from its Go slice:
	// YAML is an ordered label:prompt mapping. Its field-level shape is covered
	// by the behavioral tests below.
	if typ == buttonsType {
		schemaForType(t, doc, node, "object", at)
		return
	}

	// EventSelector has a deliberately different YAML shape from its Go struct:
	// the scalar "all" or a list of event names — there is no object form to
	// walk, and the property's presence was already checked by the caller.
	if typ == eventSelectorType {
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
		{"script mixed with buttons", "name: f\nstages:\n  - id: s\n    script: echo ok\n    buttons:\n      Retry: Try again\n", false},
		{"empty button label", "name: f\nstages:\n  - id: s\n    agents: [planning]\n    buttons:\n      \"\": Try again\n", false},
		{"empty button prompt", "name: f\nstages:\n  - id: s\n    agents: [planning]\n    buttons:\n      Retry: \"\"\n", false},
		{"reflect without memory path", "name: f\nstages:\n  - id: s\n    agents: [planning]\n    reflect:\n      file: stage.md\n", false},
		{"parent reflect path", "name: f\nmemory:\n  path: memory\nstages:\n  - id: s\n    agents: [planning]\n    reflect:\n      file: ../escape.md\n", false},
		{"absolute reflect path", "name: f\nmemory:\n  path: memory\nstages:\n  - id: s\n    agents: [planning]\n    reflect:\n      file: /tmp/escape.md\n", false},
		{"reserved reflect path", "name: f\nmemory:\n  path: memory\nstages:\n  - id: s\n    agents: [planning]\n    reflect:\n      file: ./memory.md\n", false},
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
	configExample := "../../config.example.yaml"
	data, err := os.ReadFile(configExample)
	if err != nil {
		t.Fatal(err)
	}
	if err := configSchema.Validate(jsonInstance(t, data)); err != nil {
		t.Errorf("%s does not match config schema: %v", configExample, err)
	}
}
