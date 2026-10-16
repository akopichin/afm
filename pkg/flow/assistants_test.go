package flow_test

import (
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
)

func TestStageAssistants_Scalar(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants: reviewer
`
	f, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	want := flow.StageAssistants{{Profile: "reviewer"}}
	if got := f.Stages[0].Assistants; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("Assistants = %+v, want %+v", got, want)
	}
}

func TestStageAssistants_SingleElementList(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants: [reviewer]
`
	f, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	want := flow.StageAssistants{{Profile: "reviewer"}}
	if got := f.Stages[0].Assistants; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("Assistants = %+v, want %+v", got, want)
	}
}

func TestStageAssistants_MultiElementListPreservesOrder(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants: [reviewer, qa]
`
	f, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	got := f.Stages[0].Assistants
	if len(got) != 2 || got[0].Profile != "reviewer" || got[1].Profile != "qa" {
		t.Fatalf("Assistants = %+v, want [reviewer, qa] in order", got)
	}
}

func TestStageAssistants_ObjectAllFields(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants:
      - profile: qa
        title: "QA gate"
        instruction: "Check test coverage"
        mode: required
        trigger: on_completion
`
	f, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	got := f.Stages[0].Assistants
	want := flow.StageAssistant{
		Profile: "qa", Title: "QA gate", Instruction: "Check test coverage",
		Mode: assistants.ModeRequired, Trigger: assistants.TriggerOnCompletion,
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("Assistants = %+v, want [%+v]", got, want)
	}
}

func TestStageAssistants_MixedListOfNameAndObject(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants:
      - reviewer
      - profile: qa
        mode: required
`
	f, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	got := f.Stages[0].Assistants
	if len(got) != 2 {
		t.Fatalf("Assistants = %+v, want 2 elements", got)
	}
	if got[0] != (flow.StageAssistant{Profile: "reviewer"}) {
		t.Fatalf("Assistants[0] = %+v, want {Profile: reviewer}", got[0])
	}
	if got[1] != (flow.StageAssistant{Profile: "qa", Mode: assistants.ModeRequired}) {
		t.Fatalf("Assistants[1] = %+v, want {Profile: qa, Mode: required}", got[1])
	}
}

func TestStageAssistants_SingleObjectWithoutList(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants:
      profile: qa
`
	f, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	want := flow.StageAssistants{{Profile: "qa"}}
	if got := f.Stages[0].Assistants; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("Assistants = %+v, want %+v", got, want)
	}
}

func TestStageAssistants_ParseErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"number", "assistants: 42"},
		{"bool", "assistants: true"},
		{"nested list", "assistants: [[reviewer]]"},
		{"unknown key", "assistants: [{triger: on_completion, profile: qa}]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    ` + tc.body + `
`
			if _, err := flow.ParseFile(writeTemp(t, yamlDoc)); err == nil {
				t.Fatalf("ParseFile: want error for %s, got nil", tc.name)
			}
		})
	}
}

// ---- T2: Flow.validate() rules ----

func TestStageAssistants_ScriptStageRejected(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    script: "echo hi"
    assistants: reviewer
`
	_, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err == nil {
		t.Fatal("ParseFile: want error, got nil")
	}
	want := `stage "s1": "assistants" cannot be combined with script`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestStageAssistants_PlanningOnlyStageRejected(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [planning]
    assistants: reviewer
`
	_, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err == nil {
		t.Fatal("ParseFile: want error, got nil")
	}
	want := `stage "s1": assistants requires an execution stage (planning-only stage would never attach them)`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestStageAssistants_EmptyProfileNameFromScalar(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants: [""]
`
	_, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err == nil {
		t.Fatal("ParseFile: want error, got nil")
	}
	want := `stage "s1": assistants[0]: profile is required`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestStageAssistants_EmptyProfileNameFromObject(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants:
      - profile: ""
`
	_, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err == nil {
		t.Fatal("ParseFile: want error, got nil")
	}
	want := `stage "s1": assistants[0]: profile is required`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestStageAssistants_DuplicateProfileWithinStage(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants: [reviewer, reviewer]
`
	_, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err == nil {
		t.Fatal("ParseFile: want error, got nil")
	}
	want := `stage "s1": duplicate assistant profile "reviewer"`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestStageAssistants_InvalidMode(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants:
      - profile: reviewer
        mode: advisry
`
	_, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err == nil {
		t.Fatal("ParseFile: want error, got nil")
	}
	if !strings.Contains(err.Error(), "mode") {
		t.Fatalf("error = %q, want it to mention %q", err.Error(), "mode")
	}
}

func TestStageAssistants_InvalidTrigger(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants:
      - profile: reviewer
        trigger: on_complete
`
	_, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err == nil {
		t.Fatal("ParseFile: want error, got nil")
	}
	if !strings.Contains(err.Error(), "trigger") {
		t.Fatalf("error = %q, want it to mention %q", err.Error(), "trigger")
	}
}

func TestStageAssistants_SameProfileAcrossDifferentStagesIsValid(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants: reviewer
  - id: s2
    agents: [auto]
    assistants: reviewer
`
	if _, err := flow.ParseFile(writeTemp(t, yamlDoc)); err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
}

func TestStageAssistants_UnknownKeySuggestsProfile(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants: [{role: qa}]
`
	_, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err == nil {
		t.Fatal("ParseFile: want error, got nil")
	}
	if !strings.Contains(err.Error(), "profile") {
		t.Fatalf("error = %q, want it to mention %q", err.Error(), "profile")
	}
}
