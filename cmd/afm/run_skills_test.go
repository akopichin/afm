package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/skills"
	"github.com/akopichin/afm/pkg/state"
)

// Relative skills_dir values are taken from the effective project root: the worktree, else root_dir from the
// afm root, else the afm root itself — without changing where the agent runs.
func TestPrepareRunEnvironment_SkillsBase(t *testing.T) {
	chdirTemp(t)
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		flow     *flow.Flow
		worktree string
		want     string
		wantRoot string
	}{
		{"afm root", &flow.Flow{Name: "f"}, "", dir, ""},
		{"root_dir", &flow.Flow{Name: "f", RootDir: "src"}, "", filepath.Join(dir, "src"), filepath.Join(dir, "src")},
		{"worktree", &flow.Flow{Name: "f", RootDir: "src"}, filepath.Join(".worktrees", "wt"), filepath.Join(dir, ".worktrees", "wt"), filepath.Join(dir, ".worktrees", "wt")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, err := prepareRunEnvironment(".", tc.flow, config.Default(), tc.worktree)
			if err != nil {
				t.Fatal(err)
			}
			if env.SkillsBase != tc.want {
				t.Errorf("SkillsBase = %q, want %q", env.SkillsBase, tc.want)
			}
			if env.RootDir != tc.wantRoot {
				t.Errorf("RootDir = %q, want %q: skills must not change the agent's CWD", env.RootDir, tc.wantRoot)
			}
		})
	}
}

func TestWithSkillLibraries_NearestExplicitValueWins(t *testing.T) {
	base := "/proj"
	f := &flow.Flow{SkillsDir: "flow-lib"}
	cfg := config.Config{SkillsDir: "/shared/cfg-lib"}
	stages := []flow.Stage{
		{ID: "own", SkillsDir: "./stage-lib"},
		{ID: "flow"},
		{ID: "abs", SkillsDir: "/abs/lib"},
	}
	team := flow.Team{
		{ID: "own", SkillsDir: "member-lib"},
		{ID: "flow"},
	}

	gotStages, gotTeam := withSkillLibraries(stages, team, f, cfg, base)

	wantStages := []skills.Dir{"/proj/stage-lib", "/proj/flow-lib", "/abs/lib"}
	for i, want := range wantStages {
		if gotStages[i].SkillsDir != want {
			t.Errorf("stage %s: skills_dir = %q, want %q", gotStages[i].ID, gotStages[i].SkillsDir, want)
		}
	}
	if gotTeam[0].SkillsDir != "/proj/member-lib" || gotTeam[1].SkillsDir != "/proj/flow-lib" {
		t.Errorf("team skills_dir = %q, %q; want the member's own, then the flow's", gotTeam[0].SkillsDir, gotTeam[1].SkillsDir)
	}
}

func TestWithSkillLibraries_ConfigIsTheLastResortAndNothingMeansNothing(t *testing.T) {
	stages := []flow.Stage{{ID: "a"}}
	team := flow.Team{{ID: "m"}}

	got, gotTeam := withSkillLibraries(stages, team, &flow.Flow{}, config.Config{SkillsDir: "cfg-lib"}, "/proj")
	if got[0].SkillsDir != "/proj/cfg-lib" || gotTeam[0].SkillsDir != "/proj/cfg-lib" {
		t.Errorf("config default = %q / %q, want it resolved from the project root", got[0].SkillsDir, gotTeam[0].SkillsDir)
	}

	got, gotTeam = withSkillLibraries(stages, team, &flow.Flow{}, config.Config{}, "/proj")
	if got[0].SkillsDir != "" || gotTeam[0].SkillsDir != "" {
		t.Errorf("no setting anywhere = %q / %q, want none", got[0].SkillsDir, gotTeam[0].SkillsDir)
	}
}

func TestWithSkillLibraries_DoesNotMutateTheParsedFlow(t *testing.T) {
	stages := []flow.Stage{{ID: "a", Skills: skills.Names{"tdd"}}}
	team := flow.Team{{ID: "m", Skills: skills.Names{"tdd"}}}

	got, gotTeam := withSkillLibraries(stages, team, &flow.Flow{SkillsDir: "lib"}, config.Config{}, "/proj")

	if stages[0].SkillsDir != "" || team[0].SkillsDir != "" {
		t.Fatalf("the parsed flow was changed: %q / %q", stages[0].SkillsDir, team[0].SkillsDir)
	}
	if !slices.Equal(got[0].Skills, []string{"tdd"}) || !slices.Equal(gotTeam[0].Skills, []string{"tdd"}) {
		t.Fatalf("required skills were lost: %v / %v", got[0].Skills, gotTeam[0].Skills)
	}
}

func writeCLISkill(t *testing.T, lib, name string) {
	t.Helper()
	dir := filepath.Join(lib, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: d\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckSkillLibraries(t *testing.T) {
	good := t.TempDir()
	writeCLISkill(t, good, "tdd")
	missing := filepath.Join(t.TempDir(), "missing")

	if err := checkSkillLibraries([]flow.Stage{{ID: "a", SkillsDir: skills.Dir(good)}, {ID: "b"}}, flow.Team{{ID: "m", SkillsDir: skills.Dir(good)}}); err != nil {
		t.Fatalf("a readable library and a stage without one: %v", err)
	}

	err := checkSkillLibraries([]flow.Stage{{ID: "a", SkillsDir: skills.Dir(missing)}}, nil)
	if err == nil || !strings.Contains(err.Error(), `stage "a"`) || !strings.Contains(err.Error(), missing) {
		t.Fatalf("a stage with a missing library: err = %v, want it to name the stage and the path", err)
	}

	err = checkSkillLibraries(nil, flow.Team{{ID: "m", SkillsDir: skills.Dir(missing)}})
	if err == nil || !strings.Contains(err.Error(), `team member "m"`) {
		t.Fatalf("a team member with a missing library: err = %v, want it to name the member", err)
	}
}

// settleSkillRun is the one place that freezes skills_dir into the stage registry and checks the libraries of
// the run about to start.
func TestSettleSkillRun(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "") // the helper itself may run in an afm container
	base := t.TempDir()
	good := mkdirAll(t, filepath.Join(base, "good"))
	writeCLISkill(t, good, "tdd")
	snap := state.RunState{Stages: map[string]state.StageState{}}
	declared := func(skillsDir string) *flow.Flow {
		return &flow.Flow{SkillsDir: skills.Dir(skillsDir), Stages: []flow.Stage{{ID: "a"}}, Team: flow.Team{{ID: "m"}}}
	}

	t.Run("a new run gets absolute libraries and a frozen registry", func(t *testing.T) {
		runDir := t.TempDir()
		f := declared("good")

		stages, team, err := settleSkillRun(runDir, f.Stages, f, config.Config{}, base, snap)

		if err != nil || stages[0].SkillsDir != skills.Dir(good) || team[0].SkillsDir != skills.Dir(good) {
			t.Fatalf("settled = %+v %+v, %v; want %s for both", stages, team, err, good)
		}
		if saved, found, _ := graph.LoadRegistry(runDir); !found || saved[0].SkillsDir != skills.Dir(good) {
			t.Fatalf("registry = %+v, found %v; want the settled library frozen", saved, found)
		}
	})
	t.Run("a stage that names its own relative library is resolved from the base", func(t *testing.T) {
		own := mkdirAll(t, filepath.Join(base, "own"))
		writeCLISkill(t, own, "tdd")
		f := &flow.Flow{Stages: []flow.Stage{{ID: "a", SkillsDir: "own"}}}

		stages, _, err := settleSkillRun(t.TempDir(), f.Stages, f, config.Config{}, base, snap)

		if err != nil || stages[0].SkillsDir != skills.Dir(own) {
			t.Fatalf("stage library = %q, %v; want %s", stages[0].SkillsDir, err, own)
		}
	})
	t.Run("a mistyped library of a new run is not frozen, so the fixed flow is used next time", func(t *testing.T) {
		runDir := t.TempDir()
		typo := declared("goood")
		if _, _, err := settleSkillRun(runDir, typo.Stages, typo, config.Config{}, base, snap); err == nil {
			t.Fatal("a missing library must stop the run")
		}
		if _, found, _ := graph.LoadRegistry(runDir); found {
			t.Fatal("the failed start froze the registry with the mistyped path")
		}

		fixed := declared("good")
		stages, _, err := settleSkillRun(runDir, fixed.Stages, fixed, config.Config{}, base, snap)
		if err != nil || stages[0].SkillsDir != skills.Dir(good) {
			t.Fatalf("after the fix: %+v, %v; want %s", stages, err, good)
		}
	})
	t.Run("a resume keeps the saved library and ignores the edited flow", func(t *testing.T) {
		runDir := t.TempDir()
		first := declared("good")
		if _, _, err := settleSkillRun(runDir, first.Stages, first, config.Config{}, base, snap); err != nil {
			t.Fatal(err)
		}
		elsewhere := mkdirAll(t, filepath.Join(base, "elsewhere"))
		writeCLISkill(t, elsewhere, "tdd")
		edited := declared("elsewhere")

		stages, _, err := settleSkillRun(runDir, edited.Stages, edited, config.Config{}, base, snap)

		if err != nil || stages[0].SkillsDir != skills.Dir(good) {
			t.Fatalf("resumed library = %+v, %v; want the saved %s", stages, err, good)
		}
	})
	t.Run("a saved library that disappeared stops the resume", func(t *testing.T) {
		runDir := t.TempDir()
		gone := mkdirAll(t, filepath.Join(base, "gone"))
		writeCLISkill(t, gone, "tdd")
		f := declared("gone")
		if _, _, err := settleSkillRun(runDir, f.Stages, f, config.Config{}, base, snap); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(gone); err != nil {
			t.Fatal(err)
		}

		if _, _, err := settleSkillRun(runDir, f.Stages, f, config.Config{}, base, snap); err == nil || !strings.Contains(err.Error(), gone) {
			t.Fatalf("err = %v, want one naming %s", err, gone)
		}
	})
	t.Run("a resume is checked against the saved library, not the edited flow", func(t *testing.T) {
		runDir := t.TempDir()
		gone := mkdirAll(t, filepath.Join(base, "gone-then-edited"))
		writeCLISkill(t, gone, "tdd")
		first := declared("gone-then-edited")
		if _, _, err := settleSkillRun(runDir, first.Stages, first, config.Config{}, base, snap); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(gone); err != nil {
			t.Fatal(err)
		}
		edited := declared("good") // exists, but the run is frozen on the deleted one

		if _, _, err := settleSkillRun(runDir, edited.Stages, edited, config.Config{}, base, snap); err == nil || !strings.Contains(err.Error(), gone) {
			t.Fatalf("err = %v, want one naming the saved %s", err, gone)
		}
	})
	t.Run("an edit to a library that does not exist yet does not stop a resume of a healthy library", func(t *testing.T) {
		runDir := t.TempDir()
		first := declared("good")
		if _, _, err := settleSkillRun(runDir, first.Stages, first, config.Config{}, base, snap); err != nil {
			t.Fatal(err)
		}
		edited := &flow.Flow{SkillsDir: "not-yet", Stages: []flow.Stage{{ID: "a"}}} // no team: its members would read the declaration

		if _, _, err := settleSkillRun(runDir, edited.Stages, edited, config.Config{}, base, snap); err != nil {
			t.Fatalf("the saved library is fine, the edit is not used: %v", err)
		}
	})
	t.Run("a saved library that is not an absolute path stops the resume", func(t *testing.T) {
		runDir := t.TempDir()
		t.Chdir(mkdirAll(t, filepath.Join(base, "cwd")))
		writeCLISkill(t, mkdirAll(t, filepath.Join(base, "cwd", "~")), "tdd")
		if _, err := graph.RestoreOrPersist(runDir, []flow.Stage{{ID: "a", SkillsDir: "~"}}); err != nil {
			t.Fatal(err)
		}
		f := declared("good")

		if _, _, err := settleSkillRun(runDir, f.Stages, f, config.Config{}, base, snap); err == nil || !strings.Contains(err.Error(), "absolute") {
			t.Fatalf("err = %v, want an error asking for an absolute path", err)
		}
	})
	t.Run("the library of a finished stage does not stop the resume", func(t *testing.T) {
		runDir := t.TempDir()
		gone := mkdirAll(t, filepath.Join(base, "finished"))
		writeCLISkill(t, gone, "tdd")
		f := &flow.Flow{SkillsDir: skills.Dir(gone), Stages: []flow.Stage{{ID: "a"}}}
		if _, _, err := settleSkillRun(runDir, f.Stages, f, config.Config{}, base, snap); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(gone); err != nil {
			t.Fatal(err)
		}
		done := state.RunState{Stages: map[string]state.StageState{"a": {Status: state.StatusDone}}}

		if _, _, err := settleSkillRun(runDir, f.Stages, f, config.Config{}, base, done); err != nil {
			t.Fatalf("a finished stage: %v", err)
		}
	})
	t.Run("a missing library of a team member whose agent does not exist yet stops the run", func(t *testing.T) {
		missing := filepath.Join(base, "missing-member-lib")
		f := &flow.Flow{Stages: []flow.Stage{{ID: "a"}}, Team: flow.Team{{ID: "m", SkillsDir: skills.Dir(missing)}}}

		_, _, err := settleSkillRun(t.TempDir(), f.Stages, f, config.Config{}, base, snap)

		if err == nil || !strings.Contains(err.Error(), `team member "m"`) {
			t.Fatalf("err = %v, want one naming the member", err)
		}
	})
}

func TestUnfinishedStagesSkipsDoneOnes(t *testing.T) {
	snap := state.RunState{Stages: map[string]state.StageState{"done": {Status: state.StatusDone}, "pending": {Status: state.StatusPending}}}

	got := unfinishedStages([]flow.Stage{{ID: "done"}, {ID: "pending"}, {ID: "unknown"}}, snap)

	if len(got) != 2 || got[0].ID != "pending" || got[1].ID != "unknown" {
		t.Fatalf("unfinishedStages = %+v, want pending and unknown", got)
	}
}

// executeFlow settles skills_dir before it starts any stage: the registry freezes the absolute library, and a
// missing one stops the run with no stage started.
func TestExecuteFlow_SettlesAndChecksSkillLibraries(t *testing.T) {
	t.Run("a library is frozen as an absolute path", func(t *testing.T) {
		fx := newCycleFixture(t)
		lib := mkdirAll(t, filepath.Join(fx.root, "lib"))
		writeCLISkill(t, lib, "tdd")
		fx.write("flow.yaml", "skills_dir: lib\n"+fx.flowYAML(flowSpec{}))

		if err := fx.runWithTimeout(testCycleConfig()); err != nil {
			t.Fatal(err)
		}

		saved, found, err := graph.LoadRegistry(filepath.Join(fx.root, ".afm", "runs", fx.runDirs()[0]))
		if err != nil || !found || saved[0].SkillsDir != skills.Dir(lib) {
			t.Fatalf("registry = %+v, found %v, err %v; want %s", saved, found, err, lib)
		}
	})
	t.Run("a missing library stops the run before any stage starts", func(t *testing.T) {
		fx := newCycleFixture(t)
		fx.write("flow.yaml", "skills_dir: nope\n"+fx.flowYAML(flowSpec{}))

		err := fx.runWithTimeout(testCycleConfig())

		if err == nil || !strings.Contains(err.Error(), "nope") || fx.ticks() != 0 {
			t.Fatalf("err = %v, ticks = %d; want an error naming nope and no stage started", err, fx.ticks())
		}
	})
}

// flow.yaml is read again inside the container, so an edit between the host preflight and the start could
// bring a library the host never checked; the container takes only the host's list.
func TestSettleSkillRun_ContainerTakesOnlyHostCheckedLibraries(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "1")
	base := t.TempDir()
	lib := mkdirAll(t, filepath.Join(base, "lib"))
	writeCLISkill(t, lib, "tdd")
	f := &flow.Flow{SkillsDir: "lib", Stages: []flow.Stage{{ID: "a"}}}
	snap := state.RunState{Stages: map[string]state.StageState{}}

	t.Run("a library the host did not list stops the run before the registry is frozen", func(t *testing.T) {
		t.Setenv(docker.SkillLibrariesEnvVar, filepath.Join(base, "other"))
		runDir := t.TempDir()
		_, _, err := settleSkillRun(runDir, f.Stages, f, config.Config{}, base, snap)
		if err == nil || !strings.Contains(err.Error(), lib) || !strings.Contains(err.Error(), "restart `afm run`") {
			t.Fatalf("err = %v, want a restart error naming %s", err, lib)
		}
		if _, found, _ := graph.LoadRegistry(runDir); found {
			t.Fatal("the registry was frozen with an unchecked library")
		}
	})
	t.Run("a library the host listed is accepted", func(t *testing.T) {
		t.Setenv(docker.SkillLibrariesEnvVar, lib)
		if _, _, err := settleSkillRun(t.TempDir(), f.Stages, f, config.Config{}, base, snap); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("outside a container nothing is compared", func(t *testing.T) {
		t.Setenv("AFM_IN_DOCKER", "")
		t.Setenv(docker.SkillLibrariesEnvVar, "")
		if _, _, err := settleSkillRun(t.TempDir(), f.Stages, f, config.Config{}, base, snap); err != nil {
			t.Fatal(err)
		}
	})
}
