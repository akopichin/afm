package flow_test

import (
	"encoding/json"
	"testing"

	"github.com/akopichin/afm/contracts/budget"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/internal/budgetparity"
)

// Journal events carry flows and stages verbatim: without a budget the JSON must stay byte-identical
// to the representation before the field existed (captured from the base revision).
func TestBudgetAbsentJSONIsByteIdentical(t *testing.T) {
	f := flow.Flow{
		Name: "n",
		Team: flow.Team{{ID: "a", Profile: "p", Title: "T"}},
		Stages: []flow.Stage{{
			ID: "s1", Name: "S", Agents: []flow.AgentType{"auto"},
			Assistants: flow.StageAssistants{{Profile: "qa"}},
		}},
	}
	want := `{"Name":"n","Description":"","Prompt":"","MaxParallel":0,"RootDir":"","Memory":{"Path":"","Mode":"","MemoryUse":false,"MaxRules":0,"Commit":false},"SkillsDir":"","Hooks":null,"SideAgent":null,"Worktrees":null,"Collaboration":null,"Team":[{"ID":"a","Profile":"p","Title":"T","Instruction":"","Skills":null,"SkillsDir":""}],"Repeat":{"Enabled":false,"MaxCycles":0,"KeepRuns":0},"Stages":[{"ID":"s1","Name":"S","Description":"","Agents":["auto"],"Skills":null,"SkillsDir":"","DependsOn":null,"EagerPlanning":false,"Plan":"","Command":"","MaxParallel":0,"Artifacts":null,"Inputs":null,"Interactive":false,"Verify":{"Steps":null,"MaxFailures":null,"ReviewPatience":null,"FreshCorrection":null},"Prompt":"","Script":"","ScriptTimeout":0,"ScriptBefore":"","ScriptBeforeTimeout":0,"ScriptAfter":"","ScriptAfterTimeout":0,"AutoApprove":false,"AutoRun":null,"Buttons":null,"Reflect":null,"MemoryUse":null,"Hooks":null,"Browser":null,"Assistants":[{"Profile":"qa","Title":"","Instruction":"","Mode":"","Trigger":"","Skills":null,"SkillsDir":"","Loop":null}]}]}`
	got, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("flow JSON changed:\n got %s\nwant %s", got, want)
	}
}

// Dynamic stages and replayed flows arrive as JSON: every container rejects a null or repeated Budget,
// alone and nested in a Flow.
func TestBudgetJSONBoundary(t *testing.T) {
	containers := map[string]func(body string) (*budget.Spec, error){
		"flow": func(body string) (*budget.Spec, error) {
			var f flow.Flow
			err := json.Unmarshal([]byte(body), &f)
			return f.Budget, err
		},
		"stage": func(body string) (*budget.Spec, error) {
			var s flow.Stage
			err := json.Unmarshal([]byte(body), &s)
			return s.Budget, err
		},
		"team member": func(body string) (*budget.Spec, error) {
			var m flow.TeamMember
			err := json.Unmarshal([]byte(body), &m)
			return m.Budget, err
		},
		"stage assistant": func(body string) (*budget.Spec, error) {
			var a flow.StageAssistant
			err := json.Unmarshal([]byte(body), &a)
			return a.Budget, err
		},
		"stage in a flow": func(body string) (*budget.Spec, error) {
			var f flow.Flow
			if err := json.Unmarshal([]byte(`{"Stages":[`+body+`]}`), &f); err != nil {
				return nil, err
			}
			return f.Stages[0].Budget, nil
		},
		"team member in a flow": func(body string) (*budget.Spec, error) {
			var f flow.Flow
			if err := json.Unmarshal([]byte(`{"Team":[`+body+`]}`), &f); err != nil {
				return nil, err
			}
			return f.Team[0].Budget, nil
		},
		"assistant in a flow": func(body string) (*budget.Spec, error) {
			var f flow.Flow
			if err := json.Unmarshal([]byte(`{"Stages":[{"Assistants":[`+body+`]}]}`), &f); err != nil {
				return nil, err
			}
			return f.Stages[0].Assistants[0].Budget, nil
		},
	}
	for name, decode := range containers {
		t.Run(name, func(t *testing.T) { budgetparity.CheckJSONContainer(t, decode) })
	}
}

func TestBudgetJSONRoundTrips(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, budgetFlow))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var back flow.Flow
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, data)
	}
	if *back.Budget != *f.Budget || *back.Team[0].Budget != *f.Team[0].Budget ||
		*back.Stages[0].Budget != *f.Stages[0].Budget ||
		*back.Stages[0].Assistants[0].Budget != *f.Stages[0].Assistants[0].Budget ||
		back.Stages[0].Assistants[1].Budget != nil {
		t.Fatalf("budgets changed on a JSON round trip:\n%s", data)
	}
}
