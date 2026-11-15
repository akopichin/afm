package schemacheck_test

import (
	"encoding/json"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/pipeline"
)

const pipelineSchemaPath = "../../schema/pipeline.schema.json"

func TestPipelineSchemaCoversManifestStruct(t *testing.T) {
	assertCovered(t, pipelineSchemaPath, reflect.TypeOf(pipeline.Manifest{}))
}

// TestPipelineSchemaHasNoPropertiesMissingFromGo is the reverse of the coverage walk: a schema property the parser
// would reject must not exist.
func TestPipelineSchemaHasNoPropertiesMissingFromGo(t *testing.T) {
	data, err := os.ReadFile(pipelineSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Properties  map[string]json.RawMessage `json:"properties"`
		Definitions map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		at         string
		properties map[string]json.RawMessage
		typ        reflect.Type
	}{
		{"manifest", doc.Properties, reflect.TypeFor[pipeline.Manifest]()},
		{"definitions.role", doc.Definitions["role"].Properties, reflect.TypeFor[pipeline.Role]()},
		{"definitions.requirements", doc.Definitions["requirements"].Properties, reflect.TypeFor[pipeline.Requirements]()},
	}
	for _, c := range checks {
		fields := map[string]bool{}
		for f := range c.typ.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			fields[name] = true
		}
		if got, want := slices.Sorted(maps.Keys(c.properties)), slices.Sorted(maps.Keys(fields)); !slices.Equal(got, want) {
			t.Errorf("%s: schema properties %v, Go yaml fields %v", c.at, got, want)
		}
	}
}

func TestPipelineSchemaCompilesAsDraft7(t *testing.T) {
	compileSchema(t, pipelineSchemaPath)
}

// TestPipelineSchemaMatchesParser keeps the editor schema and pipeline.ParseManifest in agreement on the cases a
// package author hits.
func TestPipelineSchemaMatchesParser(t *testing.T) {
	sch := compileSchema(t, pipelineSchemaPath)
	const head = "schema_version: 2\nname: demo-pipe\nversion: 1.0.0\nentrypoint: flow.yaml\n"
	cases := []struct {
		name, body string
		want       bool
	}{
		{"minimal", head, true},
		{"full", head + "description: d\nagents: agents.yaml\nresources: [skills, scripts/run.sh]\nroles:\n  writer: {command: demo-pipe-writer, profile: demo-pipe-writer, description: w}\nrequirements:\n  docker_auto_shim: true\n  collaboration: true\n  browser: false\n  tools: [git]\n", true},
		{"catalog v1", "schema_version: 1\nname: demo-pipe\nversion: 1\nentrypoint: flow.yaml\n", false},
		{"unknown version", "schema_version: 9\nname: demo-pipe\nversion: 1\nentrypoint: flow.yaml\n", false},
		{"unknown key", head + "setup: {}\n", false},
		{"bad name", "schema_version: 2\nname: Demo\nversion: 1\nentrypoint: flow.yaml\n", false},
		{"missing entrypoint", "schema_version: 2\nname: demo-pipe\nversion: 1\n", false},
		{"parent entrypoint", "schema_version: 2\nname: demo-pipe\nversion: 1\nentrypoint: ../flow.yaml\n", false},
		{"absolute resource", head + "resources: [/etc]\n", false},
		{"dot resource", head + "resources: [.]\n", false},
		{"nested parent resource", head + "resources: [a/../../b]\n", false},
		{"role without command", head + "roles: {w: {description: x}}\n", false},
		{"bad tool", head + "requirements: {tools: [/bin/sh]}\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pipeline.ParseManifest([]byte(tc.body))
			if got := err == nil; got != tc.want {
				t.Errorf("parser accepts=%v want=%v (%v)", got, tc.want, err)
			}
			if got := schemaAcceptsYAML(t, sch, tc.body); got != tc.want {
				t.Errorf("schema accepts=%v want=%v", got, tc.want)
			}
		})
	}
}
