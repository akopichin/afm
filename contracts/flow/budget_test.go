package flow_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/budget"
	"github.com/akopichin/afm/contracts/flow"
	"gopkg.in/yaml.v3"
)

const budgetFlow = `
name: b
budget:
  tokens: 5m
  money: 20$
  stop_policy: medium
team:
  - id: arch
    profile: rev-arch
    title: Architecture
    budget: {tokens: 500k}
stages:
  - id: s1
    agents: [auto]
    budget: {money: 3.5}
    assistants:
      - profile: qa
        budget: {tokens: 1.5m, stop_policy: info}
      - docs
`

func TestBudget_ParsesInEveryFlowContainer(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, budgetFlow))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	wantFlow := budget.Spec{Tokens: 5_000_000, MoneyUSD: 20, StopPolicy: budget.PolicyMedium}
	if f.Budget == nil || *f.Budget != wantFlow {
		t.Errorf("flow budget = %+v, want %+v", f.Budget, wantFlow)
	}
	if got := f.Team[0].Budget; got == nil || *got != (budget.Spec{Tokens: 500_000, StopPolicy: budget.PolicyHard}) {
		t.Errorf("team budget = %+v", got)
	}
	stage := f.Stages[0]
	if got := stage.Budget; got == nil || *got != (budget.Spec{MoneyUSD: 3.5, StopPolicy: budget.PolicyHard}) {
		t.Errorf("stage budget = %+v", got)
	}
	if got := stage.Assistants[0].Budget; got == nil || *got != (budget.Spec{Tokens: 1_500_000, StopPolicy: budget.PolicyInfo}) {
		t.Errorf("stage assistant budget = %+v", got)
	}
	if stage.Assistants[1].Budget != nil {
		t.Errorf("assistant without budget must keep nil, got %+v", stage.Assistants[1].Budget)
	}
}

func TestBudget_RejectsInvalidDeclarations(t *testing.T) {
	containers := map[string]string{
		"flow":            "name: b\nbudget: %s\nstages:\n  - id: s1\n    agents: [auto]\n",
		"stage":           "name: b\nstages:\n  - id: s1\n    agents: [auto]\n    budget: %s\n",
		"team":            "name: b\nteam:\n  - id: a\n    profile: p\n    title: T\n    budget: %s\nstages:\n  - id: s1\n    agents: [auto]\n",
		"stage assistant": "name: b\nstages:\n  - id: s1\n    agents: [auto]\n    assistants:\n      - profile: qa\n        budget: %s\n",
	}
	bodies := []struct{ name, yaml string }{
		{"null", "null"},
		{"bare", ""},
		{"empty", "{}"},
		{"policy only", "{stop_policy: info}"},
		{"zero tokens", "{tokens: 0}"},
		{"negative", "{money: -1}"},
		{"unknown key", "{tokens: 1, cap: 2}"},
		{"unknown mode", "{tokens: 1, stop_policy: soft}"},
		{"duplicate", "{tokens: 1, tokens: 2}"},
		{"scalar", "100"},
		{"currency", "{money: 5eur}"},
	}
	for cname, tpl := range containers {
		for _, body := range bodies {
			doc := strings.Replace(tpl, "%s", body.yaml, 1)
			if _, err := flow.ParseFile(writeTemp(t, doc)); err == nil {
				t.Errorf("%s / %s: parse succeeded, want error\n%s", cname, body.name, doc)
			}
		}
	}
}

func TestBudget_NullErrorNamesTheKey(t *testing.T) {
	_, err := flow.ParseFile(writeTemp(t, "name: b\nstages:\n  - id: s1\n    agents: [auto]\n    budget:\n"))
	if err == nil || !strings.Contains(err.Error(), "budget must not be null") {
		t.Fatalf("err = %v, want budget null error", err)
	}
}

func TestBudget_FlowMarshalRoundTrips(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, budgetFlow))
	if err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	again, err := flow.ParseFile(writeTemp(t, string(data)))
	if err != nil {
		t.Fatalf("reparse: %v\n---\n%s", err, data)
	}
	if *again.Budget != *f.Budget || *again.Team[0].Budget != *f.Team[0].Budget ||
		*again.Stages[0].Budget != *f.Stages[0].Budget ||
		*again.Stages[0].Assistants[0].Budget != *f.Stages[0].Assistants[0].Budget {
		t.Fatalf("budgets lost on round trip\n---\n%s", data)
	}
}

// Файлы без budget сериализуются как раньше: ключа нет ни в одном контейнере.
func TestBudget_AbsentFromMarshalWhenUnset(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, "name: b\nteam:\n  - id: a\n    profile: p\n    title: T\nstages:\n  - id: s1\n    agents: [auto]\n    assistants: [qa]\n"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "budget") {
		t.Fatalf("marshal mentions budget:\n%s", data)
	}
}

func TestBudget_StageCopyKeepsField(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, budgetFlow))
	if err != nil {
		t.Fatal(err)
	}
	stages := append([]flow.Stage(nil), f.Stages...)
	copied := stages[0]
	if copied.Budget == nil || *copied.Budget != *f.Stages[0].Budget {
		t.Fatalf("copied stage lost budget: %+v", copied.Budget)
	}
}

// Null that arrives through a << merge or an alias is still null: yaml.v3 would decode it as an absent key.
func TestBudget_RejectsNullThroughMerge(t *testing.T) {
	docs := map[string]string{
		"flow merge":      "name: b\nx: &n {budget: null}\n<<: *n\nstages:\n  - id: s1\n    agents: [auto]\n",
		"stage merge":     "name: b\nx: &n {budget: null}\nstages:\n  - id: s1\n    <<: *n\n    agents: [auto]\n",
		"stage merge seq": "name: b\nx: &n {budget: ~}\ny: &o {name: Y}\nstages:\n  - id: s1\n    <<: [*o, *n]\n    agents: [auto]\n",
		"stage alias":     "name: b\nx: &n ~\nstages:\n  - id: s1\n    budget: *n\n    agents: [auto]\n",
	}
	for name, doc := range docs {
		if _, err := flow.ParseFile(writeTemp(t, doc)); err == nil || !strings.Contains(err.Error(), "budget must not be null") {
			t.Errorf("%s: err = %v, want budget null error", name, err)
		}
	}
}

func TestBudget_MergedValueAndOverrides(t *testing.T) {
	doc := "name: b\nx: &n {budget: null}\nz: &ok {budget: {tokens: 1k}}\nstages:\n" +
		"  - id: s1\n    <<: *n\n    budget: {money: 2}\n    agents: [auto]\n" +
		"  - id: s2\n    <<: *ok\n    agents: [auto]\n"
	f, err := flow.ParseFile(writeTemp(t, doc))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if got := f.Stages[0].Budget; got == nil || got.MoneyUSD != 2 {
		t.Errorf("explicit budget must win over the merged null: %+v", got)
	}
	if got := f.Stages[1].Budget; got == nil || got.Tokens != 1000 {
		t.Errorf("merged budget = %+v, want 1000 tokens", got)
	}
}

// Budget and stages keys count as yaml.v3 decodes them: through key aliases, tags and << merges.
func TestBudget_YAMLKeyIdentity(t *testing.T) {
	const stage = "stages: [{id: s1, agents: [auto]}]\n"
	rejected := map[string]string{
		"key alias":               "name: b\nx: &k budget\n*k : null\n" + stage,
		"stage key alias":         "name: b\nx: &k budget\nstages: [{id: s1, agents: [auto], *k : null}]\n",
		"stages key alias":        "name: b\nx: &k stages\n*k : [{id: s1, agents: [auto], budget: null}]\n",
		"binary stages key":       "name: b\n!!binary c3RhZ2Vz: [{id: s1, agents: [auto], budget: null}]\n",
		"merge":                   "name: b\n<<: {budget: null}\n" + stage,
		"merge with key alias":    "name: b\nx: &k budget\n<<: {*k : null}\n" + stage,
		"merged stages":           "name: b\n<<: {stages: [{id: s1, agents: [auto], budget: null}]}\n",
		"first merge source null": "name: b\n<<: [{budget: null}, {budget: {tokens: 1}}]\n" + stage,
		"merge tag on the key":    "name: b\n!!merge budget: null\n" + stage,
		"binary key":              "name: b\n!!binary YnVkZ2V0: null\n" + stage,
		"stage binary key":        "name: b\nstages: [{id: s1, agents: [auto], !!binary YnVkZ2V0: null}]\n",
	}
	for name, doc := range rejected {
		if _, err := flow.ParseFile(writeTemp(t, doc)); err == nil || !strings.Contains(err.Error(), "budget must not be null") {
			t.Errorf("%s: err = %v, want budget null error", name, err)
		}
	}
	if _, err := flow.ParseFile(writeTemp(t, "name: b\nx: &a {<<: *a}\n<<: *a\n"+stage)); err == nil {
		t.Error("a merge cycle must be an error")
	}

	type accepted struct {
		name, doc         string
		flowTokens, stage uint64 // 0 = no budget
	}
	for _, tc := range []accepted{
		{name: "direct over merged null", doc: "name: b\n<<: {budget: null}\nbudget: {tokens: 1}\n" + stage, flowTokens: 1},
		{name: "key alias over merged null", doc: "name: b\nx: &k budget\n<<: {budget: null}\n*k : {tokens: 2}\n" + stage, flowTokens: 2},
		{name: "tagged key over merged null", doc: "name: b\n<<: {budget: null}\n!!binary YnVkZ2V0: {tokens: 3}\n" + stage, flowTokens: 3},
		{name: "first merge source valid", doc: "name: b\n<<: [{budget: {tokens: 4}}, {budget: null}]\n" + stage, flowTokens: 4},
		{name: "stage key alias", doc: "name: b\nx: &k budget\nstages: [{id: s1, agents: [auto], *k : {tokens: 5}}]\n", stage: 5},
		{name: "quoted << is an ordinary key", doc: "name: b\n'<<': {budget: null}\n" + stage},
		{name: "alias to << is an ordinary key", doc: "name: b\nx: &m <<\n*m : {budget: null}\n" + stage},
	} {
		f, err := flow.ParseFile(writeTemp(t, tc.doc))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got := tokensOf(f.Budget); got != tc.flowTokens {
			t.Errorf("%s: flow budget tokens = %d, want %d", tc.name, got, tc.flowTokens)
		}
		if got := tokensOf(f.Stages[0].Budget); got != tc.stage {
			t.Errorf("%s: stage budget tokens = %d, want %d", tc.name, got, tc.stage)
		}
	}
}

func tokensOf(spec *budget.Spec) uint64 {
	if spec == nil {
		return 0
	}
	return spec.Tokens
}

// Team members and helpers match their keys by decoded name: an anchor called budget is not a budget key.
func TestBudget_TeamAndAssistantKeysAreDecoded(t *testing.T) {
	const stage = "stages: [{id: s1, agents: [auto]}]\n"
	docs := map[string]string{
		"team":      "name: b\nx: &budget %s\nteam: [{id: a, title: A, profile: p, *budget : {tokens: 1}}]\n" + stage,
		"assistant": "name: b\nx: &budget %s\nstages: [{id: s1, agents: [auto], assistants: [{profile: p, *budget : {tokens: 1}}]}]\n",
	}
	for container, tpl := range docs {
		if _, err := flow.ParseFile(writeTemp(t, fmt.Sprintf(tpl, "unknown"))); err == nil {
			t.Errorf("%s: an alias to an unknown key was accepted", container)
		}
		f, err := flow.ParseFile(writeTemp(t, fmt.Sprintf(tpl, "budget")))
		if err != nil {
			t.Errorf("%s: %v", container, err)
			continue
		}
		var spec *budget.Spec
		if container == "team" {
			spec = f.Team[0].Budget
		} else {
			spec = f.Stages[0].Assistants[0].Budget
		}
		if tokensOf(spec) != 1 {
			t.Errorf("%s: budget = %+v, want 1 token", container, spec)
		}
	}

	member := "name: b\nx: &k budget\ny: &n ~\nteam: [{id: a, title: A, profile: p, %s}]\n" + stage
	helper := "name: b\nx: &k budget\ny: &n ~\nstages: [{id: s1, agents: [auto], assistants: [{profile: p, %s}]}]\n"
	for _, fields := range []string{
		"budget: {tokens: 1}, *k : {tokens: 2}",
		"budget: {tokens: 1}, !!binary YnVkZ2V0: {tokens: 2}",
		"*k : null",
		"budget: *n",
		"<<: {title: B}",
	} {
		for container, tpl := range map[string]string{"team": member, "assistant": helper} {
			if _, err := flow.ParseFile(writeTemp(t, fmt.Sprintf(tpl, fields))); err == nil {
				t.Errorf("%s {%s}: parse succeeded, want error", container, fields)
			}
		}
	}
}
