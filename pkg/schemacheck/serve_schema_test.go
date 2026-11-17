package schemacheck_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/akopichin/afm/pkg/serve"
)

const serveSchemaPath = "../../schema/serve.schema.json"

func TestServeSchemaCoversConfigStruct(t *testing.T) {
	assertCovered(t, serveSchemaPath, reflect.TypeFor[serve.Config]())
}

// TestServeSchemaHasNoPropertiesMissingFromGo is the reverse walk: a schema property the strict parser would
// reject must not exist.
func TestServeSchemaHasNoPropertiesMissingFromGo(t *testing.T) {
	doc := loadSchemaDocument(t, serveSchemaPath)
	definitions, _ := doc["definitions"].(map[string]any)
	checks := []struct {
		at   string
		node map[string]any
		typ  reflect.Type
	}{
		{"config", doc, reflect.TypeFor[serve.Config]()},
		{"project", asObject(definitions["project"]), reflect.TypeFor[serve.Project]()},
		{"pipeline", asObject(definitions["pipeline"]), reflect.TypeFor[serve.Pipeline]()},
		{"limits", asObject(asObject(doc["properties"])["limits"]), reflect.TypeFor[serve.Limits]()},
		{"retention", asObject(asObject(doc["properties"])["retention"]), reflect.TypeFor[serve.Retention]()},
	}
	for _, c := range checks {
		props := asObject(c.node["properties"])
		fields := 0
		for f := range c.typ.Fields() {
			if f.Tag.Get("yaml") == "" {
				continue
			}
			fields++
		}
		if len(props) != fields {
			t.Errorf("%s: schema has %d properties, Go type has %d yaml fields", c.at, len(props), fields)
		}
	}
}

func asObject(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func TestServeSchemaCompilesAsDraft7(t *testing.T) {
	compileSchema(t, serveSchemaPath)
}

// TestServeSchemaMatchesParser keeps the editor schema and serve.ParseConfig in agreement on what a config
// author hits.
func TestServeSchemaMatchesParser(t *testing.T) {
	sch := compileSchema(t, serveSchemaPath)
	dir := t.TempDir()
	opts := filepath.Join(dir, "options.yaml")
	if err := os.WriteFile(opts, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, body string
		want       bool
	}{
		{"empty object", "{}\n", true},
		{"full", "projects: [{id: app-1, path: " + dir + "}]\npipelines: [{id: dev, source: s, options: " + opts + "}]\nlimits: {max_running: 8, max_starting: 3}\nretention: {release_after: 24h}\n", true},
		{"unknown top key", "extra: 1\n", false},
		{"unknown project key", "projects: [{id: a, path: " + dir + ", x: 1}]\n", false},
		{"bad id", "projects: [{id: A_b, path: " + dir + "}]\n", false},
		{"missing path", "projects: [{id: a}]\n", false},
		{"relative path", "projects: [{id: a, path: rel}]\n", false},
		{"pipeline without source", "pipelines: [{id: p}]\n", false},
		{"relative options", "pipelines: [{id: p, source: s, options: o.yaml}]\n", false},
		{"zero max_running", "limits: {max_running: 0}\n", false},
		{"zero max_starting", "limits: {max_starting: 0}\n", false},
		{"unknown limit", "limits: {max_total: 3}\n", false},
		{"bad duration", "retention: {release_after: soon}\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := serve.ParseConfig([]byte(tc.body))
			if got := err == nil; got != tc.want {
				t.Errorf("parser accepts=%v want=%v (%v)", got, tc.want, err)
			}
			if got := schemaAcceptsYAML(t, sch, tc.body); got != tc.want {
				t.Errorf("schema accepts=%v want=%v", got, tc.want)
			}
		})
	}
}
