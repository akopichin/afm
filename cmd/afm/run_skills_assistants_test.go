package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/skills"
	"github.com/akopichin/afm/pkg/state"
)

// A stage's declared helper settles its library like a team member: its own, then the flow's, then the
// config's — never the stage's own skills_dir.
func TestWithSkillLibraries_StageAssistants(t *testing.T) {
	stages := []flow.Stage{{
		ID:        "a",
		SkillsDir: "stage-lib",
		Assistants: flow.StageAssistants{
			{Profile: "own", Skills: skills.Names{"review"}, SkillsDir: "helper-lib"},
			{Profile: "inherits"},
		},
	}}

	got, _ := withSkillLibraries(stages, nil, &flow.Flow{SkillsDir: "flow-lib"}, config.Config{}, "/proj")

	if dir := got[0].Assistants[0].SkillsDir; dir != "/proj/helper-lib" {
		t.Errorf("own helper library = %q, want it resolved from the project root", dir)
	}
	if dir := got[0].Assistants[1].SkillsDir; dir != "/proj/flow-lib" {
		t.Errorf("helper without skills_dir = %q, want the flow's, not the stage's", dir)
	}
	if !slices.Equal(got[0].Assistants[0].Skills, []string{"review"}) || got[0].Assistants[1].Skills != nil {
		t.Errorf("required skills = %v / %v, want the helper's own only", got[0].Assistants[0].Skills, got[0].Assistants[1].Skills)
	}
	if stages[0].Assistants[0].SkillsDir != "helper-lib" || stages[0].Assistants[1].SkillsDir != "" {
		t.Errorf("the parsed flow was changed: %+v", stages[0].Assistants)
	}

	got, _ = withSkillLibraries(stages, nil, &flow.Flow{}, config.Config{SkillsDir: "/cfg-lib"}, "/proj")
	if dir := got[0].Assistants[1].SkillsDir; dir != "/cfg-lib" {
		t.Errorf("helper without flow library = %q, want the config's", dir)
	}
}

func TestCheckSkillLibraries_NamesTheHelper(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	stages := []flow.Stage{{ID: "a", Assistants: flow.StageAssistants{{Profile: "qa", SkillsDir: skills.Dir(missing)}}}}

	err := checkSkillLibraries(stages, nil)
	if err == nil || !strings.Contains(err.Error(), `stage "a" assistant "qa"`) || !strings.Contains(err.Error(), missing) {
		t.Fatalf("err = %v, want it to name the stage, the helper and the path", err)
	}
}

func TestPlannedSkillRun_StageAssistants(t *testing.T) {
	f := &flow.Flow{Stages: []flow.Stage{{ID: "a", Assistants: flow.StageAssistants{{Profile: "qa", SkillsDir: "helper-lib"}}}}}

	stages, team := plannedSkillRun(f, config.Config{}, "/proj", nil)
	if got := skillLibraryDirs(stages, team); !slices.Equal(got, []string{"/proj/helper-lib"}) {
		t.Fatalf("libraries = %v, want the helper's", got)
	}

	saved := &savedRun{
		state:  state.RunState{Stages: map[string]state.StageState{"a": {Status: state.StatusPending}}},
		stages: []flow.Stage{{ID: "a", Assistants: flow.StageAssistants{{Profile: "qa", SkillsDir: "/saved/helper-lib"}}}},
	}
	stages, team = plannedSkillRun(f, config.Config{}, "/proj", saved)
	if got := skillLibraryDirs(stages, team); !slices.Equal(got, []string{"/saved/helper-lib"}) {
		t.Fatalf("resumed libraries = %v, want the helper's from the saved registry", got)
	}
}

// With collaboration off no helper is attached, so its library must not stop the run or ask for a mount.
func TestPlannedSkillRun_NoHelperLibrariesWithoutCollaboration(t *testing.T) {
	t.Setenv("AFM_COLLABORATION", "0")
	missing := filepath.Join(t.TempDir(), "missing")
	f := &flow.Flow{Stages: []flow.Stage{{
		ID:         "a",
		SkillsDir:  "stage-lib",
		Assistants: flow.StageAssistants{{Profile: "qa", SkillsDir: skills.Dir(missing)}},
	}}}

	stages, team := plannedSkillRun(f, config.Config{}, "/proj", nil)

	if got := skillLibraryDirs(stages, team); !slices.Equal(got, []string{"/proj/stage-lib"}) {
		t.Fatalf("libraries = %v, want only the stage's", got)
	}
	if f.Stages[0].Assistants[0].SkillsDir == "" {
		t.Fatal("the parsed flow lost its helper declaration")
	}
}

// A helper's library goes through the same Docker trust rule and host check as a stage's.
func TestPrepareSkillMounts_HelperLibrary(t *testing.T) {
	root := t.TempDir()
	project := mkdirAll(t, filepath.Join(root, "proj"))
	home := mkdirAll(t, filepath.Join(root, "home"))
	shared := mkdirAll(t, filepath.Join(root, "shared"))
	writeCLISkill(t, shared, "review")
	f := &flow.Flow{Name: "f", Stages: []flow.Stage{{ID: "a", Assistants: flow.StageAssistants{{Profile: "qa", SkillsDir: skills.Dir(shared)}}}}}

	_, _, err := prepareSkillMounts(project, "", home, f, config.Config{}, nil)
	if err == nil || !strings.Contains(err.Error(), "not allowed to be mounted") {
		t.Fatalf("err = %v, want an external helper library refused without a config opt-in", err)
	}

	cfg := config.Config{Docker: config.DockerConfig{ExtraMounts: config.ExtraMounts{{Path: shared}}}}
	_, checked, err := prepareSkillMounts(project, "", home, f, cfg, nil)
	if err != nil || !slices.Equal(checked, []string{shared}) {
		t.Fatalf("checked = %v, %v; want the helper's library checked by the host", checked, err)
	}
}
