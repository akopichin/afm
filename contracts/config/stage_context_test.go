package config_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/config"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

func TestProfileStageContextRoundTrip(t *testing.T) {
	for _, raw := range []string{"instructions: test\n", "stage_context: {inputs: []}\n", "stage_context: {inputs: [prep.test-rules, prep.results.json]}\n"} {
		var want config.ProfileConfig
		if err := yaml.Unmarshal([]byte(raw), &want); err != nil {
			t.Fatal(err)
		}
		for _, format := range []string{"yaml", "json"} {
			var data []byte
			var err error
			var got config.ProfileConfig
			if format == "yaml" {
				data, err = yaml.Marshal(want)
			} else {
				data, err = json.Marshal(want)
			}
			if err != nil {
				t.Fatal(err)
			}
			if format == "yaml" {
				err = yaml.Unmarshal(data, &got)
			} else {
				err = json.Unmarshal(data, &got)
			}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("%s round trip %s: %+v, %v; want %+v", format, data, got, err, want)
			}
			if want.StageContext == nil && strings.Contains(string(data), "stage_context") {
				t.Fatalf("absent context emitted: %s", data)
			}
			if want.StageContext != nil && len(want.StageContext.Inputs) == 0 && !strings.Contains(string(data), "[]") {
				t.Fatalf("empty list lost: %s", data)
			}
		}
	}
}

func TestProfileStageContextParserSchemaParity(t *testing.T) {
	schemaBytes, err := os.ReadFile("../schema/config.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schemaDoc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(schemaBytes)))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	const url = "https://afm.invalid/config.json"
	if err := c.AddResource(url, schemaDoc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile(url)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{`{"inputs":[]}`, true}, {`{"inputs":["prep.test-rules","prep.results.json"]}`, true},
		{`null`, false}, {`{}`, false}, {`[]`, false}, {`true`, false}, {`"inputs"`, false},
		{`{"inputs":null}`, false}, {`{"inputs":"prep.rules"}`, false}, {`{"inputs":{}}`, false},
		{`{"inputs":[1]}`, false}, {`{"inputs":[true]}`, false}, {`{"inputs":[null]}`, false},
		{`{"inputs":[],"extra":true}`, false}, {`{"Inputs":[]}`, false},
		{`{"inputs":[""]}`, false}, {`{"inputs":["prep"]}`, false},
		{`{"inputs":[".rules"]}`, false}, {`{"inputs":["prep."]}`, false},
		{`{"inputs":["prep.../secret"]}`, false}, {`{"inputs":["prep.a b"]}`, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			profile := `{"stage_context":` + tc.raw + `}`
			var p config.ProfileConfig
			if err := json.Unmarshal([]byte(profile), &p); (err == nil) != tc.valid {
				t.Errorf("JSON error = %v; valid = %v", err, tc.valid)
			}
			if err := yaml.Unmarshal([]byte(profile), &p); (err == nil) != tc.valid {
				t.Errorf("YAML error = %v; valid = %v", err, tc.valid)
			}
			doc, err := jsonschema.UnmarshalJSON(strings.NewReader(`{"collaboration":{"profiles":{"writer":` + profile + `}}}`))
			if err != nil {
				t.Fatal(err)
			}
			if err := sch.Validate(doc); (err == nil) != tc.valid {
				t.Errorf("schema error = %v; valid = %v", err, tc.valid)
			}
		})
	}
}

func TestProfileStageContextYAMLBoundaries(t *testing.T) {
	for _, body := range []string{
		"stage_context:\n", "stage_context: {inputs: [123]}\n",
		"<<: {stage_context: null}\n", "stage_context: &n null\n",
		"stage_context: {inputs: [], other: x}\n", "stage_context: {inputs: [], inputs: []}\n",
	} {
		if _, err := loadModeLayers(t, "", "collaboration:\n  profiles:\n    writer:\n      "+strings.ReplaceAll(strings.TrimSuffix(body, "\n"), "\n", "\n      ")+"\n"); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
	for _, body := range []string{
		"shared: &n null\nstage_context: *n\n",
		"shared: &k stage_context\n*k : null\n",
		"stage_context: {<<: {inputs: null}}\n",
	} {
		var p config.ProfileConfig
		if err := yaml.Unmarshal([]byte(body), &p); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

func TestProfileStageContextConfigLayerReplacement(t *testing.T) {
	const prefix = "collaboration:\n  profiles:\n    writer:\n"
	cfg, err := loadModeLayers(t, prefix+"      stage_context: {inputs: [prep.rules]}\n", prefix+"      stage_context: {inputs: []}\n")
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Collaboration.Profiles["writer"].StageContext
	if got == nil || got.Inputs == nil || len(got.Inputs) != 0 {
		t.Fatalf("context = %+v", got)
	}
}
