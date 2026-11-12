package flow_test

import (
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/internal/budgetparity"
)

const flowSchema = "../schema/flow.schema.json"

// Every container that takes a budget gets the same verdict from the parser and the flow schema.
func TestBudgetSchemaParity(t *testing.T) {
	containers := map[string]string{
		"flow":            "name: b\nbudget: %s\nstages:\n  - id: s1\n    agents: [auto]\n",
		"stage":           "name: b\nstages:\n  - id: s1\n    agents: [auto]\n    budget: %s\n",
		"team member":     "name: b\nteam:\n  - {id: a, profile: p, title: T, budget: %s}\nstages:\n  - id: s1\n    agents: [auto]\n",
		"stage assistant": "name: b\nstages:\n  - id: s1\n    agents: [auto]\n    assistants:\n      - {profile: qa, budget: %s}\n",
	}
	for container, tmpl := range containers {
		for _, tc := range budgetparity.Cases {
			t.Run(container+"/"+tc.Name, func(t *testing.T) {
				doc := strings.Replace(tmpl, "%s", tc.Budget, 1)
				_, err := flow.ParseFile(writeTemp(t, doc))
				if got := err == nil; got != tc.ParserOK {
					t.Errorf("parser accepts = %v (err %v), want %v", got, err, tc.ParserOK)
				}
				if got := budgetparity.SchemaAccepts(t, flowSchema, doc); got != tc.SchemaOK() {
					t.Errorf("schema accepts = %v, want %v (gap %q)", got, tc.SchemaOK(), tc.SchemaGap)
				}
			})
		}
	}
}

// Whole documents whose budget or stages key arrives through an alias or a << merge get the same verdict
// too. Anchors sit on fields the schema allows, so the schema judges only the budget.
func TestBudgetSchemaParity_EffectiveKeys(t *testing.T) {
	const stage = "stages:\n  - id: s1\n    agents: [auto]\n"
	cases := []struct {
		name, doc string
		parserOK  bool
		schemaGap string
	}{
		{name: "key alias", doc: "name: &k budget\n*k : {tokens: 1k}\n" + stage, parserOK: true},
		{name: "key alias null", doc: "name: &k budget\n*k : null\n" + stage},
		{name: "stages key alias", doc: "name: b\ndescription: &s stages\n*s :\n  - {id: s1, agents: [auto], budget: {money: 1}}\n", parserOK: true},
		{name: "stages key alias with a null budget", doc: "name: b\ndescription: &s stages\n*s :\n  - {id: s1, agents: [auto], budget: null}\n"},
		{name: "merged null", doc: "name: b\n<<: {budget: null}\n" + stage},
		{name: "direct over merged null", doc: "name: b\n<<: {budget: null}\nbudget: {tokens: 1k}\n" + stage, parserOK: true},
		{name: "first merge source valid", doc: "name: b\n<<: [{budget: {tokens: 1k}}, {budget: null}]\n" + stage, parserOK: true},
		{name: "first merge source null", doc: "name: b\n<<: [{budget: null}, {budget: {tokens: 1k}}]\n" + stage},
		{name: "merge tag on the key", doc: "name: b\n!!merge budget: null\n" + stage},
		{name: "merged stage budget null", doc: "name: b\nstages:\n  - {<<: {budget: null}, id: s1, agents: [auto]}\n"},
		{
			name: "key repeated through an alias", doc: "name: &k budget\nbudget: {tokens: 1k}\n*k : {tokens: 2k}\n" + stage,
			schemaGap: "decoding YAML for the schema keeps the last of the repeated keys",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := flow.ParseFile(writeTemp(t, tc.doc))
			if got := err == nil; got != tc.parserOK {
				t.Errorf("parser accepts = %v (err %v), want %v", got, err, tc.parserOK)
			}
			if got, want := budgetparity.SchemaAccepts(t, flowSchema, tc.doc), tc.parserOK || tc.schemaGap != ""; got != want {
				t.Errorf("schema accepts = %v, want %v (gap %q)", got, want, tc.schemaGap)
			}
		})
	}
}
