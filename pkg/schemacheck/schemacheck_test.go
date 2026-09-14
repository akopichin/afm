package schemacheck_test

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
)

// collectYAMLFields walks a struct type and records every yaml field name
// (the token before the first comma). It recurses through pointers, slices and
// maps into nested structs. Fields without a yaml tag are decoded via a custom
// UnmarshalYAML and are intentionally skipped.
func collectYAMLFields(t reflect.Type, seen map[reflect.Type]bool, out map[string]bool) {
	for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || seen[t] {
		return
	}
	seen[t] = true
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		name := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		out[name] = true
		collectYAMLFields(f.Type, seen, out)
	}
}

// collectSchemaProps gathers every property name defined anywhere in a parsed
// JSON Schema (under any "properties" object, including $defs).
func collectSchemaProps(v any, out map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		if props, ok := x["properties"].(map[string]any); ok {
			for k := range props {
				out[k] = true
			}
		}
		for _, val := range x {
			collectSchemaProps(val, out)
		}
	case []any:
		for _, val := range x {
			collectSchemaProps(val, out)
		}
	default:
		// scalars (strings, numbers, bools) carry no properties
	}
}

func schemaProps(t *testing.T, path string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read schema %s: %v", path, err)
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse schema %s: %v", path, err)
	}
	out := map[string]bool{}
	collectSchemaProps(doc, out)
	return out
}

func assertCovered(t *testing.T, schemaPath string, structType reflect.Type) {
	t.Helper()
	props := schemaProps(t, schemaPath)
	fields := map[string]bool{}
	collectYAMLFields(structType, map[reflect.Type]bool{}, fields)

	var missing []string
	for name := range fields {
		if !props[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		t.Fatalf("%s is missing these yaml fields present in %s: %v\n"+
			"add them to the schema (this guards against schema drift)",
			schemaPath, structType.String(), missing)
	}
}

func TestFlowSchemaCoversStruct(t *testing.T) {
	assertCovered(t, "../../schema/flow.schema.json", reflect.TypeOf(flow.Flow{}))
}

func TestConfigSchemaCoversStruct(t *testing.T) {
	assertCovered(t, "../../schema/config.schema.json", reflect.TypeOf(config.Config{}))
}
