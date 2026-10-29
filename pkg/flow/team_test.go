package flow_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
)

func TestTeam_ParsesInDeclarationOrder(t *testing.T) {
	yamlDoc := `
name: t
team:
  - id: arch
    profile: rev-arch
    title: Architecture
    instruction: Review changes for simplicity and duplication when asked.
  - id: qa
    profile: qa
    title: QA
stages:
  - id: s1
    agents: [auto]
`
	f, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	want := []flow.TeamMember{
		{ID: "arch", Profile: "rev-arch", Title: "Architecture", Instruction: "Review changes for simplicity and duplication when asked."},
		{ID: "qa", Profile: "qa", Title: "QA"},
	}
	if len(f.Team) != len(want) {
		t.Fatalf("Team = %+v, want %+v", f.Team, want)
	}
	for i := range want {
		if !reflect.DeepEqual(f.Team[i], want[i]) {
			t.Fatalf("Team[%d] = %+v, want %+v", i, f.Team[i], want[i])
		}
	}
}

func TestTeam_Rejected(t *testing.T) {
	tests := []struct {
		name    string
		team    string
		wantErr string
	}{
		{"missing id", "  - profile: qa\n    title: QA\n", "team[0]: id is required"},
		{"unsafe id", "  - id: a/b\n    profile: qa\n    title: QA\n", `team member "a/b": id must match`},
		{"missing profile", "  - id: qa\n    title: QA\n", `team member "qa": profile is required`},
		{"missing title", "  - id: qa\n    profile: qa\n", `team member "qa": title is required`},
		{"empty title", "  - id: qa\n    profile: qa\n    title: \"\"\n", `team member "qa": title is required`},
		{"duplicate id", "  - id: qa\n    profile: qa\n    title: QA\n  - id: qa\n    profile: qa\n    title: QA 2\n", `duplicate team member id "qa"`},
		{"unknown key", "  - id: qa\n    profile: qa\n    title: QA\n    alias: q\n", `team: unknown field "alias"`},
		{"not a list", "  id: qa\n", "team must be a list"},
		{"null", "  null\n", "team must be a list"},
		{"empty value", "", "team must be a list"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yamlDoc := "name: t\nteam:\n" + tt.team + "stages:\n  - id: s1\n    agents: [auto]\n"
			_, err := flow.ParseFile(writeTemp(t, yamlDoc))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ParseFile error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
