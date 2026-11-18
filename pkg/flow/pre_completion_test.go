package flow_test

import (
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"gopkg.in/yaml.v3"
)

func TestPreCompletion_ParseAndBudget(t *testing.T) {
	tests := []struct {
		name  string
		stage string
		want  int
	}{
		{"default without block", "    agents: [auto]\n", 1},
		{"empty block keeps default", "    agents: [auto]\n    pre_completion: {}\n", 1},
		{"explicit budget", "    agents: [auto]\n    pre_completion:\n      max_failures: 3\n", 3},
		{"integral float", "    agents: [auto]\n    pre_completion:\n      max_failures: 2.0\n", 2},
		{"zero means no corrections", "    agents: [planning, implementation]\n    pre_completion:\n      max_failures: 0\n", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := flow.ParseFile(writeTemp(t, "name: t\nstages:\n  - id: s\n"+tc.stage))
			if err != nil {
				t.Fatalf("ParseFile: %v", err)
			}
			if got := f.Stages[0].PreCompletionMaxFailures(); got != tc.want {
				t.Fatalf("PreCompletionMaxFailures() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestPreCompletion_ValidationErrors(t *testing.T) {
	tests := []struct {
		name  string
		stage string
		want  string
	}{
		{"fractional budget", "    agents: [auto]\n    pre_completion:\n      max_failures: 1.9\n", "expected an integer"},
		{"negative fraction", "    agents: [auto]\n    pre_completion:\n      max_failures: -0.5\n", "expected an integer"},
		{"null budget", "    agents: [auto]\n    pre_completion:\n      max_failures: null\n", "expected an integer"},
		{"negative budget", "    agents: [auto]\n    pre_completion:\n      max_failures: -1\n", "must not be negative"},
		{"unknown key", "    agents: [auto]\n    pre_completion:\n      max_fails: 1\n", "unknown field"},
		{"not an object", "    agents: [auto]\n    pre_completion: 2\n", "must be an object"},
		{"planning only", "    agents: [planning]\n    pre_completion:\n      max_failures: 1\n", "requires an execution stage"},
		{"script stage", "    script: echo ok\n    pre_completion:\n      max_failures: 1\n", "requires an execution stage"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := flow.ParseFile(writeTemp(t, "name: t\nstages:\n  - id: s\n"+tc.stage))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ParseFile error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestPreCompletion_RoundTripKeepsBudget(t *testing.T) {
	zero := 0
	data, err := yaml.Marshal(flow.Stage{ID: "s", PreCompletion: &flow.PreCompletionSpec{MaxFailures: &zero}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back flow.Stage
	if err := yaml.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal %q: %v", data, err)
	}
	if back.PreCompletion == nil || back.PreCompletion.MaxFailures == nil || *back.PreCompletion.MaxFailures != 0 {
		t.Fatalf("round trip lost max_failures: %s", data)
	}
	empty, _ := yaml.Marshal(flow.Stage{ID: "s"})
	if strings.Contains(string(empty), "pre_completion") {
		t.Fatalf("a stage without the block must not emit it: %s", empty)
	}
}

func TestPreCompletion_HelperTriggerParses(t *testing.T) {
	doc := "name: t\nstages:\n  - id: s\n    agents: [auto]\n    assistants:\n      - profile: qa\n        trigger: pre_completion\n"
	f, err := flow.ParseFile(writeTemp(t, doc))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if got := f.Stages[0].Assistants[0].Trigger; got != assistants.TriggerPreCompletion {
		t.Fatalf("Trigger = %q, want %q", got, assistants.TriggerPreCompletion)
	}
}
