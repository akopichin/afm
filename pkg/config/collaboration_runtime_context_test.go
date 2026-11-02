package config_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
)

func TestRuntimeContext_LayerMergeAndProfileReplacement(t *testing.T) {
	global := "collaboration:\n  runtime_context: wrapper\n  profiles:\n    ext:\n      command: /abs/x.sh\n      runtime_context: wrapper\n"
	project := "collaboration:\n  profiles:\n    ext:\n      command: /abs/y.sh\n"
	cfg, err := loadModeLayers(t, global, project)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collaboration.RuntimeContext == nil || *cfg.Collaboration.RuntimeContext != assistants.RuntimeContextWrapper {
		t.Fatalf("collaboration.runtime_context = %v, want wrapper inherited from global", cfg.Collaboration.RuntimeContext)
	}
	ext := cfg.Collaboration.Profiles["ext"]
	if ext.Command != "/abs/y.sh" || ext.RuntimeContext != nil {
		t.Fatalf("project profile must replace the global one as a whole, got %+v", ext)
	}
}

func TestRuntimeContext_ProjectOverridesGlobalAndAbsentIsNil(t *testing.T) {
	cfg, err := loadModeLayers(t, "collaboration:\n  runtime_context: wrapper\n", "collaboration:\n  runtime_context: isolated\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := *cfg.Collaboration.RuntimeContext; got != assistants.RuntimeContextIsolated {
		t.Fatalf("got %q, want isolated", got)
	}
	cfg, err = loadModeLayers(t, "", "collaboration:\n  enabled: true\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collaboration.RuntimeContext != nil {
		t.Fatal("unset runtime_context must stay nil: the default lives only in ResolveRuntimeContext")
	}
}

func TestRuntimeContext_ProfileWithoutCommandLoads(t *testing.T) {
	cfg, err := loadModeLayers(t, "", "collaboration:\n  profiles:\n    reviewer:\n      instructions: Review it.\n")
	if err != nil {
		t.Fatal(err)
	}
	if p := cfg.Collaboration.Profiles["reviewer"]; p.Command != "" || p.RuntimeContext != nil {
		t.Fatalf("role-only profile must stay empty of command/runtime, got %+v", p)
	}
}

func TestRuntimeContext_Priority(t *testing.T) {
	cfg, err := loadModeLayers(t, "", "collaboration:\n  runtime_context: wrapper\n  profiles:\n    a:\n      instructions: x\n    b:\n      runtime_context: isolated\n")
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.Collaboration
	if got := assistants.ResolveRuntimeContext(c.Profiles["a"].RuntimeContext, c.RuntimeContext); got != assistants.RuntimeContextWrapper {
		t.Errorf("profile a = %q, want collaboration wrapper", got)
	}
	if got := assistants.ResolveRuntimeContext(c.Profiles["b"].RuntimeContext, c.RuntimeContext); got != assistants.RuntimeContextIsolated {
		t.Errorf("profile b = %q, want its own isolated", got)
	}
}

var invalidRuntimeContextValues = []struct{ name, line string }{
	{"empty string", `runtime_context: ""`},
	{"explicit null", "runtime_context: null"},
	{"tilde null", "runtime_context: ~"},
	{"bare key", "runtime_context:"},
	{"number", "runtime_context: 1"},
	{"bool", "runtime_context: true"},
	{"mapping", "runtime_context: {a: b}"},
	{"unknown string", "runtime_context: host"},
}

func TestRuntimeContext_InvalidRejectedAtLoad(t *testing.T) {
	for _, v := range invalidRuntimeContextValues {
		forms := map[string]string{
			"collaboration": "collaboration:\n  " + v.line + "\n",
			"profile":       "collaboration:\n  profiles:\n    p:\n      " + v.line + "\n",
			"merge key":     "d: &d\n  " + v.line + "\ncollaboration:\n  <<: *d\n",
		}
		for form, body := range forms {
			for _, layer := range []string{"global", "project"} {
				t.Run(layer+"/"+form+"/"+v.name, func(t *testing.T) {
					var err error
					if layer == "global" {
						_, err = loadModeLayers(t, body, "")
					} else {
						_, err = loadModeLayers(t, "", body)
					}
					if err == nil {
						t.Fatal("expected LoadFrom to reject the value")
					}
					if !strings.Contains(err.Error(), "runtime_context") {
						t.Errorf("error = %q, want it to name runtime_context", err)
					}
					if !strings.Contains(err.Error(), "parse "+layer+" config") {
						t.Errorf("error = %q, want the layer named", err)
					}
				})
			}
		}
	}
}

func TestRuntimeContext_InvalidLowerLayerRejectedEvenWhenOverridden(t *testing.T) {
	_, err := loadModeLayers(t, "collaboration:\n  runtime_context: host\n", "collaboration:\n  runtime_context: wrapper\n")
	if err == nil || !strings.Contains(err.Error(), "parse global config") {
		t.Fatalf("err = %v, want the global layer rejected", err)
	}
}

// The value yaml.v3 itself decodes into runtime_context is the one checked: tagged key spellings, aliases and
// merge-key precedence (direct keys over `<<`, earlier merge sources over later ones) all follow the decoder.
func TestRuntimeContext_ValidationFollowsYAMLDecoding(t *testing.T) {
	keys := map[string]string{
		"plain":      "runtime_context",
		"merge tag":  "!!merge runtime_context",
		"binary key": "!!binary cnVudGltZV9jb250ZXh0",
	}
	invalid := map[string]string{
		"null":                     "KEY: null",
		"tilde null":               "KEY: ~",
		"anchored null":            "KEY: *n",
		"host":                     "KEY: host",
		"null through merge":       "<<: { KEY: null }",
		"host through merge":       "<<: { KEY: host }",
		"earlier host in sequence": "<<: [ { KEY: host }, { runtime_context: isolated } ]",
	}
	valid := map[string]string{
		"isolated":                     "KEY: isolated",
		"overrides merged host":        "<<: { runtime_context: host }, KEY: isolated",
		"shadows merged null":          "<<: { runtime_context: null }, KEY: isolated",
		"earlier isolated in sequence": "<<: [ { KEY: isolated }, { runtime_context: host } ]",
	}
	levels := map[string]string{
		"collaboration": "collaboration: { %s }\n",
		"profile":       "collaboration:\n  profiles:\n    p: { %s }\n",
	}
	const anchor = "n: &n null\n"
	const replaceProfile = "collaboration:\n  profiles:\n    p: { command: /abs/y.sh }\n"
	decoded := func(cfg config.Config, level string) *assistants.RuntimeContext {
		if level == "profile" {
			return cfg.Collaboration.Profiles["p"].RuntimeContext
		}
		return cfg.Collaboration.RuntimeContext
	}

	for keyName, key := range keys {
		for level, tmpl := range levels {
			for name, body := range invalid {
				doc := anchor + fmt.Sprintf(tmpl, strings.ReplaceAll(body, "KEY", key))
				layers := map[string][2]string{"global": {doc, ""}, "project": {"", doc}}
				if level == "profile" {
					layers["global, profile replaced by project"] = [2]string{doc, replaceProfile}
				}
				for layerName, l := range layers {
					t.Run("invalid/"+keyName+"/"+level+"/"+name+"/"+layerName, func(t *testing.T) {
						_, err := loadModeLayers(t, l[0], l[1])
						if err == nil {
							t.Fatalf("expected LoadFrom to reject:\n%s", doc)
						}
						layer := "project"
						if l[0] != "" {
							layer = "global"
						}
						if !strings.Contains(err.Error(), "runtime_context") || !strings.Contains(err.Error(), "parse "+layer+" config") {
							t.Errorf("error = %q, want runtime_context and the %s layer named", err, layer)
						}
					})
				}
			}
			for name, body := range valid {
				doc := fmt.Sprintf(tmpl, strings.ReplaceAll(body, "KEY", key))
				for layerName, l := range map[string][2]string{"global": {doc, ""}, "project": {"", doc}} {
					t.Run("valid/"+keyName+"/"+level+"/"+name+"/"+layerName, func(t *testing.T) {
						cfg, err := loadModeLayers(t, l[0], l[1])
						if err != nil {
							t.Fatalf("LoadFrom: %v\n%s", err, doc)
						}
						if got := decoded(cfg, level); got == nil || *got != assistants.RuntimeContextIsolated {
							t.Errorf("runtime_context = %v, want isolated", got)
						}
					})
				}
				if level == "profile" {
					t.Run("valid/"+keyName+"/profile/"+name+"/global, profile replaced by project", func(t *testing.T) {
						cfg, err := loadModeLayers(t, doc, replaceProfile)
						if err != nil {
							t.Fatalf("LoadFrom: %v\n%s", err, doc)
						}
						if p := cfg.Collaboration.Profiles["p"]; p.RuntimeContext != nil || p.Command != "/abs/y.sh" {
							t.Errorf("profile p = %+v, want the project's definition in full", p)
						}
					})
				}
			}
		}
	}
}
