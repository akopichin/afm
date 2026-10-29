package flow_test

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/skills"
)

func TestSkills_StageAndTeamScalarAndListAreEquivalent(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, `
name: t
skills_dir: ./lib
team:
  - id: one
    profile: p
    title: One
    skills: tdd
  - id: many
    profile: p
    title: Many
    skills: [tdd, verification, tdd]
    skills_dir: ./team-lib
stages:
  - id: scalar
    agents: [auto]
    skills: tdd
  - id: list
    agents: [auto]
    skills: [superpowers:test-driven-development, tdd, superpowers:test-driven-development]
    skills_dir: ./stage-lib
  - id: legacy
    agents: [auto]
    skills: [a, b]
  - id: none
    agents: [auto]
    skills: []
  - id: absent
    agents: [auto]
`))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if f.SkillsDir != "./lib" {
		t.Errorf("flow skills_dir = %q", f.SkillsDir)
	}
	if !reflect.DeepEqual(f.Team[0].Skills, skills.Names{"tdd"}) || f.Team[0].SkillsDir != "" {
		t.Errorf("team one = %+v", f.Team[0])
	}
	if !reflect.DeepEqual(f.Team[1].Skills, skills.Names{"tdd", "verification"}) || f.Team[1].SkillsDir != "./team-lib" {
		t.Errorf("team many = %+v", f.Team[1])
	}
	wantStages := map[string]struct {
		names skills.Names
		dir   skills.Dir
	}{
		"scalar": {skills.Names{"tdd"}, ""},
		"list":   {skills.Names{"superpowers:test-driven-development", "tdd"}, "./stage-lib"},
		"legacy": {skills.Names{"a", "b"}, ""},
		"none":   {skills.Names{}, ""},
		"absent": {nil, ""},
	}
	for _, s := range f.Stages {
		want := wantStages[s.ID]
		if !reflect.DeepEqual(s.Skills, want.names) || s.SkillsDir != want.dir {
			t.Errorf("stage %s = %#v / %q, want %#v / %q", s.ID, s.Skills, s.SkillsDir, want.names, want.dir)
		}
	}
}

func TestSkills_Rejected(t *testing.T) {
	const tail = "stages:\n  - id: s\n    agents: [auto]\n"
	stage := func(line string) string {
		return "name: t\nstages:\n  - id: s\n    agents: [auto]\n    " + line + "\n"
	}
	team := func(line string) string {
		return "name: t\nteam:\n  - id: m\n    profile: p\n    title: M\n    " + line + "\n" + tail
	}
	tests := []struct {
		name, doc, wantErr string
	}{
		{"stage empty name", stage(`skills: ""`), "must not be empty"},
		{"stage null", stage("skills: null"), "must not be empty or null"},
		{"stage bare key", stage("skills:"), "must not be empty or null"},
		{"stage number", stage("skills: 5"), "must be a string"},
		{"stage number in list", stage("skills: [a, 5]"), "must be a string"},
		{"stage null in list", stage("skills: [a, null]"), "must be a string"},
		{"stage object", stage("skills: {a: b}"), "skill name or a list"},
		{"stage nested list", stage("skills: [[a]]"), "must be a string"},
		{"stage dir empty", stage(`skills_dir: ""`), "must not be empty"},
		{"stage dir null", stage("skills_dir: null"), "must not be empty or null"},
		{"stage dir bare key", stage("skills_dir:"), "must not be empty or null"},
		{"stage dir number", stage("skills_dir: 5"), "must be a string"},
		{"stage dir list", stage("skills_dir: [a]"), "must be a string"},
		{"stage dir tilde", stage("skills_dir: ~/lib"), "not expanded"},
		{"flow dir empty", "name: t\nskills_dir: \"\"\n" + tail, "must not be empty"},
		{"flow dir null", "name: t\nskills_dir: null\n" + tail, "must not be empty or null"},
		{"flow dir bare key", "name: t\nskills_dir:\n" + tail, "must not be empty or null"},
		{"flow dir number", "name: t\nskills_dir: 5\n" + tail, "must be a string"},
		{"flow dir tilde", "name: t\nskills_dir: ~/lib\n" + tail, "not expanded"},
		{"team empty name", team(`skills: ""`), "must not be empty"},
		{"team null", team("skills: null"), "must be a string"},
		{"team bare key", team("skills:"), "must be a string"},
		{"team number", team("skills: 5"), "must be a string"},
		{"team mixed list", team("skills: [a, 1]"), "must be a string"},
		{"team object", team("skills: {a: b}"), "skill name or a list"},
		{"team dir empty", team(`skills_dir: ""`), "must not be empty"},
		{"team dir null", team("skills_dir: null"), "must be a string"},
		{"team dir number", team("skills_dir: 5"), "must be a string"},
		{"team dir tilde", team("skills_dir: ~/lib"), "not expanded"},
		{"team skills twice", team("skills: tdd\n    skills: docs"), "duplicate"},
		{"team skills_dir twice", team("skills_dir: a\n    skills_dir: b"), "duplicate"},
		{"team title twice", team("title: N"), "duplicate"},
		{"stage skill name with a line break", stage(`skills: "a\nb"`), "control"},
		{"team skill name with a line break", team(`skills: "a\nb"`), "control"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := flow.ParseFile(writeTemp(t, tt.doc))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestSkills_StageMarshalRoundTrip(t *testing.T) {
	in := flow.Stage{ID: "s", Skills: skills.Names{"a", "b:c"}, SkillsDir: "./lib"}
	data, err := yaml.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "skills: [a, 'b:c']") || !strings.Contains(string(data), "skills_dir: ./lib") {
		t.Errorf("unexpected YAML:\n%s", data)
	}
	var out flow.Stage
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip: got %#v, want %#v", out, in)
	}
	empty, _ := yaml.Marshal(flow.Stage{ID: "s"})
	if strings.Contains(string(empty), "skills") {
		t.Errorf("unset fields must be omitted:\n%s", empty)
	}
}
