package flow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestStageMarshal_OmitsZeroValueFields(t *testing.T) {
	s := Stage{
		ID:     "implementation",
		Name:   "Implementation",
		Agents: []AgentType{AgentPlanning, AgentImplementation},
	}
	data, err := yaml.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(data)
	for _, unwanted := range []string{
		"command:", "verify:", "interactive:", "script:", "max_parallel:",
		"auto_approve:", "eager_planning:", "plan:",
		"description:",
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("output contains zero-value field %q:\n%s", unwanted, out)
		}
	}
	if !strings.Contains(out, "agents:") {
		t.Errorf("expected agents field present:\n%s", out)
	}
}

func TestStageMarshal_AgentsRenderInlineFlowStyle(t *testing.T) {
	s := Stage{ID: "x", Agents: []AgentType{AgentPlanning, AgentImplementation, AgentReview}}
	data, err := yaml.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), "agents: [planning, implementation, review]") {
		t.Errorf("expected inline flow-style agents, got:\n%s", data)
	}
}

func TestArtifactAndInputMarshal_OmitZeroValueFields(t *testing.T) {
	s := Stage{
		ID:        "check",
		Artifacts: []Artifact{{Name: "summary", Path: "summary.md"}},
		Inputs:    []Input{{Ref: "build.binary"}},
	}
	data, err := yaml.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(data)
	for _, unwanted := range []string{"inline:", "optional:", "description: \"\""} {
		if strings.Contains(out, unwanted) {
			t.Errorf("output contains zero-value field %q:\n%s", unwanted, out)
		}
	}
}

func TestFlowMarshal_RoundTripsThroughParseFile(t *testing.T) {
	f := Flow{
		Name:        "roundtrip-test",
		Description: "d",
		Stages: []Stage{
			{ID: "implementation", Name: "Implementation", Description: "do it",
				Agents: []AgentType{AgentPlanning, AgentImplementation}},
		},
	}
	data, err := yaml.Marshal(&f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "flow.yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	parsed, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v\n---\n%s", err, data)
	}
	if parsed.Name != f.Name || len(parsed.Stages) != 1 {
		t.Errorf("round-trip mismatch: %+v", parsed)
	}
}
