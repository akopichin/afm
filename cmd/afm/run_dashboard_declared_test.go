package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/assistants"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/pkg/server"
)

func titledRegistry(t *testing.T) *assistants.Registry {
	t.Helper()
	reg, err := assistants.BuildRegistry(nil, nil, []assistants.Profile{{ID: "reviewer", Title: "Code Reviewer", Command: "claude"}, {ID: "plain", Command: "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestDeclaredAssistants(t *testing.T) {
	reg := titledRegistry(t)
	in := flow.StageAssistants{
		{Profile: "reviewer"},
		{Profile: "reviewer", Title: "  Own title  ", Mode: assistants.ModeRequired, Trigger: assistants.TriggerCheckpoints},
		{Profile: "plain"},
		{Profile: "ghost", Trigger: assistants.TriggerOnMessage, Loop: &flow.StageAssistantLoop{Every: "5m", Prompt: "secret prompt"}},
	}
	want := []server.DeclaredAssistant{
		{Profile: "reviewer", Name: "Code Reviewer", Trigger: assistants.TriggerOneShot, Mode: assistants.ModeAdvisory},
		{Profile: "reviewer", Name: "Own title", Trigger: assistants.TriggerCheckpoints, Mode: assistants.ModeRequired},
		{Profile: "plain", Name: "plain", Trigger: assistants.TriggerOneShot, Mode: assistants.ModeAdvisory},
		{Profile: "ghost", Name: "ghost", Trigger: assistants.TriggerOnMessage, Mode: assistants.ModeAdvisory, LoopEvery: "5m"},
	}
	if got := declaredAssistants(in, profileTitles(reg)); !reflect.DeepEqual(got, want) {
		t.Fatalf("declaredAssistants =\n%+v\nwant\n%+v", got, want)
	}
	// A nil registry falls back to the profile ID.
	got := declaredAssistants(flow.StageAssistants{{Profile: "reviewer"}}, profileTitles(nil))
	if len(got) != 1 || got[0].Name != "reviewer" {
		t.Fatalf("nil registry name = %+v", got)
	}
	if declaredAssistants(nil, profileTitles(reg)) != nil {
		t.Fatal("no declarations must give nil")
	}
}

func TestDashboardStageConfig_DoesNotLeakHelperInternals(t *testing.T) {
	stages := []flow.Stage{{ID: "a", Assistants: flow.StageAssistants{{
		Profile: "reviewer", Instruction: "SECRET-INSTRUCTION", Skills: []string{"SECRET-SKILL"},
		Loop: &flow.StageAssistantLoop{Every: "1m", Prompt: "SECRET-LOOP-PROMPT"},
	}}}}
	data, err := json.Marshal(dashboardStageConfig(stages, profileTitles(nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"SECRET-INSTRUCTION", "SECRET-SKILL", "SECRET-LOOP-PROMPT"} {
		if strings.Contains(string(data), marker) {
			t.Fatalf("stage config JSON leaks %s: %s", marker, data)
		}
	}
}

type stageReader []flow.Stage

func (r stageReader) StageConfigs() []flow.Stage { return r }

// A stage added at runtime exists only in the registry, so its declarations must come from the provider's reader.
func TestRegistryStageProvider_IncludesRuntimeAddedStageDeclarations(t *testing.T) {
	provider := registryStageProvider{
		reader:   stageReader{{ID: "added", Assistants: flow.StageAssistants{{Profile: "reviewer"}}}},
		profiles: titledRegistry(t),
	}
	got := provider.StageConfigs()["added"].Assistants
	if len(got) != 1 || got[0].Name != "Code Reviewer" {
		t.Fatalf("runtime stage declarations = %+v", got)
	}
}
