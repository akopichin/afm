package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/skills"
	"github.com/akopichin/afm/pkg/state"
	"github.com/akopichin/afm/pkg/worktree"
)

func mkdirAll(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSkillMountDirs(t *testing.T) {
	const project = "/work/proj"
	for _, tc := range []struct {
		name  string
		libs  []string
		extra config.ExtraMounts
		want  []string
	}{
		{"inside the project", []string{"/work/proj/skills"}, nil, nil},
		{"the project itself", []string{"/work/proj"}, nil, nil},
		{"outside the project", []string{"/shared/skills"}, nil, []string{"/shared/skills"}},
		{"sibling with the project name as prefix", []string{"/work/proj-other/skills"}, nil, []string{"/work/proj-other/skills"}},
		{"inside an absolute extra mount", []string{"/shared/skills/team"}, config.ExtraMounts{{Path: "/shared/skills"}}, nil},
		{"inside a project-relative extra mount", []string{"/work/shared/skills"}, config.ExtraMounts{{Path: "../shared"}}, nil},
		{"home extra mount lives elsewhere in the container", []string{"/home/u/skills"}, config.ExtraMounts{{Path: "~/skills"}}, []string{"/home/u/skills"}},
		{"the container spelling of a home extra mount is another host directory", []string{"/home/afm/skills"}, config.ExtraMounts{{Path: "~/skills"}}, []string{"/home/afm/skills"}},
		{"nested libraries share the outer mount", []string{"/shared/a/b", "/shared/a"}, nil, []string{"/shared/a"}},
		{"duplicates and order", []string{"/z/lib", "/a/lib", "/z/lib"}, nil, []string{"/a/lib", "/z/lib"}},
		{"nothing to mount", nil, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := skillMountDirs(tc.libs, sameSpellingDests(project, "/home/u", tc.extra))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("skillMountDirs = %v, want %v", got, tc.want)
			}
		})
	}
}

// A symlink decides by its target: a library reached through the project only by a link still needs its own mount.
func TestSkillMountDirs_Symlinks(t *testing.T) {
	root := t.TempDir()
	project := mkdirAll(t, filepath.Join(root, "proj"))
	external := mkdirAll(t, filepath.Join(root, "external", "lib"))
	inner := mkdirAll(t, filepath.Join(project, "real"))
	linkOut := filepath.Join(project, "skills")
	if err := os.Symlink(external, linkOut); err != nil {
		t.Fatal(err)
	}
	linkIn := filepath.Join(root, "alias")
	if err := os.Symlink(inner, linkIn); err != nil {
		t.Fatal(err)
	}

	t.Run("a link inside the project to an external library is mounted by its target", func(t *testing.T) {
		got := skillMountDirs([]string{linkOut}, sameSpellingDests(project, "/home/u", nil))
		if want := []string{external}; !slices.Equal(got, want) {
			t.Fatalf("skillMountDirs = %v, want %v", got, want)
		}
	})
	t.Run("a link outside the project keeps its own path, which the container lacks", func(t *testing.T) {
		got := skillMountDirs([]string{linkIn}, sameSpellingDests(project, "/home/u", nil))
		if want := []string{linkIn}; !slices.Equal(got, want) {
			t.Fatalf("skillMountDirs = %v, want %v", got, want)
		}
	})
	t.Run("a library whose target is covered by an extra mount needs none", func(t *testing.T) {
		got := skillMountDirs([]string{linkOut}, sameSpellingDests(project, "/home/u", config.ExtraMounts{{Path: filepath.Dir(external)}}))
		if len(got) != 0 {
			t.Fatalf("skillMountDirs = %v, want none", got)
		}
	})
}

// Only a library the user allowed in config may reach a container: flow.yaml alone cannot mount host paths.
func TestCheckSkillMounts(t *testing.T) {
	root := t.TempDir()
	project := mkdirAll(t, filepath.Join(root, "proj"))
	home := mkdirAll(t, filepath.Join(root, "home"))
	shared := mkdirAll(t, filepath.Join(root, "shared"))
	team := mkdirAll(t, filepath.Join(shared, "team"))
	other := mkdirAll(t, filepath.Join(root, "other"))
	outsideLink := filepath.Join(shared, "link-out")
	if err := os.Symlink(other, outsideLink); err != nil {
		t.Fatal(err)
	}
	insideLink := filepath.Join(other, "link-in")
	if err := os.Symlink(team, insideLink); err != nil {
		t.Fatal(err)
	}
	base := project

	cfgShared := config.Config{SkillsDir: skills.Dir(shared)}
	cfgExtra := config.Config{Docker: config.DockerConfig{ExtraMounts: config.ExtraMounts{{Path: shared}}}}
	cfgHomeExtra := config.Config{Docker: config.DockerConfig{ExtraMounts: config.ExtraMounts{{Path: "~/../shared"}}}}

	for _, tc := range []struct {
		name    string
		dirs    []string
		cfg     config.Config
		wantErr string
	}{
		{"no external library", nil, config.Config{}, ""},
		{"config skills_dir itself", []string{shared}, cfgShared, ""},
		{"inside config skills_dir", []string{team}, cfgShared, ""},
		{"inside an extra mount", []string{team}, cfgExtra, ""},
		{"inside a home extra mount", []string{team}, cfgHomeExtra, ""},
		{"only the flow asked for it", []string{other}, config.Config{}, "set skills_dir: " + other},
		{"outside the config library", []string{other}, cfgShared, "docker.extra_mounts"},
		{"link out of the allowed root is judged by its target", []string{outsideLink}, cfgShared, "not allowed"},
		{"link into the allowed root", []string{insideLink}, cfgShared, ""},
		{"filesystem root", []string{"/"}, cfgShared, "too broad"},
		{"home directory itself", []string{home}, config.Config{SkillsDir: skills.Dir(home)}, "too broad"},
		{"an ancestor of the project", []string{root}, config.Config{SkillsDir: skills.Dir(root)}, "too broad"},
		{"colon cannot be part of a -v argument", []string{filepath.Join(shared, "a:b")}, cfgShared, "':'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSkillMounts(tc.dirs, project, home, tc.cfg, base)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("checkSkillMounts: %v, want it accepted", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("checkSkillMounts = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// An allowed root that holds the home directory must not let flow.yaml mount a path inside home.
func TestCheckSkillMounts_BroadAllowedRoots(t *testing.T) {
	root := t.TempDir()
	project := mkdirAll(t, filepath.Join(root, "work", "proj"))
	people := mkdirAll(t, filepath.Join(root, "people"))
	home := mkdirAll(t, filepath.Join(people, "me"))
	secrets := mkdirAll(t, filepath.Join(home, ".ssh"))

	cfg := config.Config{SkillsDir: skills.Dir(people)}
	if err := checkSkillMounts([]string{people}, project, home, cfg, project); err == nil || !strings.Contains(err.Error(), "too broad") {
		t.Fatalf("an ancestor of home: err = %v, want too broad", err)
	}
	if err := checkSkillMounts([]string{secrets}, project, home, cfg, project); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("a path inside home under a broad allowed root: err = %v, want not allowed", err)
	}
	narrow := mkdirAll(t, filepath.Join(people, "shared"))
	if err := checkSkillMounts([]string{narrow}, project, home, config.Config{SkillsDir: skills.Dir(narrow)}, project); err != nil {
		t.Fatalf("a library beside home: %v", err)
	}
}

func TestEffectiveRoot(t *testing.T) {
	afmRoot := t.TempDir()
	for _, tc := range []struct {
		name     string
		flow     *flow.Flow
		worktree string
		want     string
	}{
		{"afm root", &flow.Flow{}, "", afmRoot},
		{"relative root_dir", &flow.Flow{RootDir: "src"}, "", filepath.Join(afmRoot, "src")},
		{"absolute root_dir", &flow.Flow{RootDir: "/abs/src"}, "", "/abs/src"},
		{"worktree wins", &flow.Flow{RootDir: "src"}, filepath.Join(afmRoot, ".worktrees", "wt"), filepath.Join(afmRoot, ".worktrees", "wt")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := effectiveRoot(afmRoot, tc.worktree, tc.flow)
			if err != nil || got != tc.want {
				t.Fatalf("effectiveRoot = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func savedTask(skillsDir string, status assistants.TaskStatus) *state.TaskState {
	return &state.TaskState{Task: assistants.Task{SkillsDir: skillsDir, Status: status}}
}

// A resumed run keeps what it was started with: unfinished saved stages and live saved agents decide the
// libraries, the current declaration is used only for a member whose agent does not exist yet.
func TestPlannedSkillRun_Resume(t *testing.T) {
	f := &flow.Flow{
		SkillsDir: "new-lib",
		Stages:    []flow.Stage{{ID: "a"}, {ID: "b"}},
		Team:      flow.Team{{ID: "old", SkillsDir: "edited-lib"}, {ID: "fresh", SkillsDir: "fresh-lib"}},
	}
	saved := &savedRun{
		state: state.RunState{
			Stages: map[string]state.StageState{"a": {Status: state.StatusDone}, "b": {Status: state.StatusPending}},
			Assistants: state.AssistantsState{
				OperationIndex: map[string]uint64{"team/old": 7},
				Tasks: map[assistants.TaskID]*state.TaskState{
					"t-old":  savedTask("/saved/old-lib", assistants.StatusWatching),
					"t-dead": savedTask("/saved/dead-lib", assistants.StatusCompleted),
					"t-bare": savedTask("", assistants.StatusWatching),
				},
			},
		},
		stages: []flow.Stage{{ID: "a", SkillsDir: "/saved/a-lib"}, {ID: "b", SkillsDir: "/saved/b-lib"}},
	}

	stages, team := plannedSkillRun(f, config.Config{}, "/proj", saved)

	if got := skillLibraryDirs(stages, team); !slices.Equal(got, []string{"/proj/fresh-lib", "/saved/b-lib", "/saved/old-lib"}) {
		t.Fatalf("libraries = %v, want the unfinished saved stage, the live saved agent and the declared new member", got)
	}
}

func TestPlannedSkillRun_NewRunUsesTheDeclaration(t *testing.T) {
	f := &flow.Flow{
		SkillsDir: "lib",
		Stages:    []flow.Stage{{ID: "a"}},
		Team:      flow.Team{{ID: "m", SkillsDir: "member-lib"}},
	}

	stages, team := plannedSkillRun(f, config.Config{}, "/proj", nil)

	if got := skillLibraryDirs(stages, team); !slices.Equal(got, []string{"/proj/lib", "/proj/member-lib"}) {
		t.Fatalf("libraries = %v, want the declared ones resolved from the project root", got)
	}
}

// With collaboration off no team agent is added, so a team library must not stop the run or ask for a mount.
func TestPlannedSkillRun_NoTeamWithoutCollaboration(t *testing.T) {
	t.Setenv("AFM_COLLABORATION", "0")
	f := &flow.Flow{Stages: []flow.Stage{{ID: "a", SkillsDir: "stage-lib"}}, Team: flow.Team{{ID: "m", SkillsDir: "member-lib"}}}
	saved := &savedRun{state: state.RunState{Assistants: state.AssistantsState{Tasks: map[assistants.TaskID]*state.TaskState{
		"t-m": savedTask("/saved/member-lib", assistants.StatusWatching),
	}}}}

	stages, team := plannedSkillRun(f, config.Config{}, "/proj", saved)

	if got := skillLibraryDirs(stages, team); !slices.Equal(got, []string{"/proj/stage-lib"}) {
		t.Fatalf("libraries = %v, want only the stage's", got)
	}
}

func TestLoadSavedRun(t *testing.T) {
	runs := t.TempDir()
	f := &flow.Flow{Name: "flow", Stages: []flow.Stage{{ID: "s"}}}
	if got, err := loadSavedRun(runs, "flow", f); got != nil || err != nil {
		t.Fatalf("no run yet: loadSavedRun = %v, %v; want nil, nil", got, err)
	}

	seedRun(t, runs, "flow-20260101-000000-abcd", state.StatusPending, "")
	runDir := filepath.Join(runs, "flow-20260101-000000-abcd")
	if _, err := graph.RestoreOrPersist(runDir, []flow.Stage{{ID: "s", SkillsDir: "/saved/lib"}}); err != nil {
		t.Fatal(err)
	}

	got, err := loadSavedRun(runs, "flow", f)
	if err != nil || got == nil {
		t.Fatalf("an unfinished run: loadSavedRun = %v, %v; want it", got, err)
	}
	if len(got.stages) != 1 || got.stages[0].SkillsDir != "/saved/lib" {
		t.Fatalf("saved stages = %+v, want the frozen registry", got.stages)
	}
	if got, err := loadSavedRun(runs, "other-flow", f); got != nil || err != nil {
		t.Fatalf("another flow's run must not be resumed: %v, %v", got, err)
	}
}

func TestLoadSavedRun_FinishedRunIsNotResumed(t *testing.T) {
	runs := t.TempDir()
	f := &flow.Flow{Name: "flow", Stages: []flow.Stage{{ID: "s"}}}
	seedRun(t, runs, "flow-20260101-000000-abcd", state.StatusDone, "")

	if got, err := loadSavedRun(runs, "flow", f); got != nil || err != nil {
		t.Fatalf("a finished run: loadSavedRun = %v, %v; want a new run", got, err)
	}
}

func TestPrepareSkillMounts(t *testing.T) {
	root := t.TempDir()
	project := mkdirAll(t, filepath.Join(root, "proj"))
	home := mkdirAll(t, filepath.Join(root, "home"))
	shared := mkdirAll(t, filepath.Join(root, "shared"))
	writeCLISkill(t, shared, "tdd")
	inside := mkdirAll(t, filepath.Join(project, "skills"))
	writeCLISkill(t, inside, "tdd")
	cfg := config.Config{SkillsDir: skills.Dir(shared)}

	t.Run("external library allowed by config is mounted once", func(t *testing.T) {
		f := &flow.Flow{Stages: []flow.Stage{{ID: "a"}, {ID: "b", SkillsDir: skills.Dir(shared)}}, Team: flow.Team{{ID: "m"}}}
		got, _, err := prepareSkillMounts(project, "", home, f, cfg, nil)
		if err != nil || len(got) != 1 || got[0].Path != shared || got[0].Browse {
			t.Fatalf("mounts = %+v, %v; want the library once, not browseable", got, err)
		}
	})
	t.Run("library inside the project adds no mount", func(t *testing.T) {
		f := &flow.Flow{SkillsDir: "skills", Stages: []flow.Stage{{ID: "a"}}}
		got, checked, err := prepareSkillMounts(project, "", home, f, config.Config{}, nil)
		if err != nil || len(got) != 0 {
			t.Fatalf("mounts = %+v, %v; want none", got, err)
		}
		if !slices.Equal(checked, []string{inside}) {
			t.Fatalf("checked = %v; want the project library, which the container may read without a restart", checked)
		}
	})
	t.Run("a project link to an allowed external library is mounted by its target", func(t *testing.T) {
		linkProject := mkdirAll(t, filepath.Join(root, "linkproj"))
		if err := os.Symlink(shared, filepath.Join(linkProject, "skills")); err != nil {
			t.Fatal(err)
		}
		f := &flow.Flow{SkillsDir: "skills", Stages: []flow.Stage{{ID: "a"}}}
		got, _, err := prepareSkillMounts(linkProject, "", home, f, cfg, nil)
		if err != nil || len(got) != 1 || got[0].Path != shared {
			t.Fatalf("mounts = %+v, %v; want %s", got, err, shared)
		}
	})
	t.Run("a colon in a library path is rejected, it would split the host-verified list", func(t *testing.T) {
		colon := mkdirAll(t, filepath.Join(project, "skills:v2"))
		writeCLISkill(t, colon, "tdd")
		f := &flow.Flow{SkillsDir: "skills:v2", Stages: []flow.Stage{{ID: "a"}}}
		_, _, err := prepareSkillMounts(project, "", home, f, config.Config{}, nil)
		if err == nil || !strings.Contains(err.Error(), "':'") {
			t.Fatalf("err = %v, want one rejecting ':' in the library path", err)
		}
	})
	t.Run("a missing library stops before docker creates it", func(t *testing.T) {
		missing := filepath.Join(shared, "missing")
		f := &flow.Flow{Stages: []flow.Stage{{ID: "a", SkillsDir: skills.Dir(missing)}}}
		_, _, err := prepareSkillMounts(project, "", home, f, cfg, nil)
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Fatalf("err = %v, want one naming %s", err, missing)
		}
	})
	t.Run("a file is not a library", func(t *testing.T) {
		file := filepath.Join(shared, "file.txt")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		f := &flow.Flow{Stages: []flow.Stage{{ID: "a", SkillsDir: skills.Dir(file)}}}
		if _, _, err := prepareSkillMounts(project, "", home, f, cfg, nil); err == nil {
			t.Fatal("a file as skills_dir must be rejected")
		}
	})
	t.Run("flow-only external path is rejected", func(t *testing.T) {
		other := mkdirAll(t, filepath.Join(root, "other"))
		writeCLISkill(t, other, "tdd")
		f := &flow.Flow{SkillsDir: skills.Dir(other), Stages: []flow.Stage{{ID: "a"}}}
		_, _, err := prepareSkillMounts(project, "", home, f, config.Config{}, nil)
		if err == nil || !strings.Contains(err.Error(), "config") {
			t.Fatalf("err = %v, want one naming the config to set", err)
		}
	})
	t.Run("a saved library that disappeared stops the resume", func(t *testing.T) {
		gone := filepath.Join(shared, "gone")
		saved := &savedRun{
			state:  state.RunState{Stages: map[string]state.StageState{"a": {Status: state.StatusPending}}},
			stages: []flow.Stage{{ID: "a", SkillsDir: skills.Dir(gone)}},
		}
		f := &flow.Flow{Stages: []flow.Stage{{ID: "a"}}}
		_, _, err := prepareSkillMounts(project, "", home, f, cfg, saved)
		if err == nil || !strings.Contains(err.Error(), gone) {
			t.Fatalf("err = %v, want one naming %s", err, gone)
		}
	})
	t.Run("resume also mounts the library the changed flow now declares", func(t *testing.T) {
		a := mkdirAll(t, filepath.Join(shared, "A"))
		writeCLISkill(t, a, "tdd")
		b := mkdirAll(t, filepath.Join(shared, "B"))
		writeCLISkill(t, b, "tdd")
		saved := &savedRun{
			state:  state.RunState{Stages: map[string]state.StageState{"a": {Status: state.StatusPending}}},
			stages: []flow.Stage{{ID: "a", SkillsDir: skills.Dir(a)}},
		}
		f := &flow.Flow{Stages: []flow.Stage{{ID: "a", SkillsDir: skills.Dir(b)}}}
		got, checked, err := prepareSkillMounts(project, "", home, f, cfg, saved)
		if err != nil || len(got) != 2 || got[0].Path != a || got[1].Path != b {
			t.Fatalf("mounts = %+v, %v; want both the saved %s and the declared %s", got, err, a, b)
		}
		if !slices.Equal(checked, []string{a, b}) {
			t.Fatalf("checked = %v; want the saved and the declared library, sorted", checked)
		}
	})
	t.Run("a declared library that is not there yet does not block a resume", func(t *testing.T) {
		a := mkdirAll(t, filepath.Join(shared, "A2"))
		writeCLISkill(t, a, "tdd")
		saved := &savedRun{
			state:  state.RunState{Stages: map[string]state.StageState{"a": {Status: state.StatusPending}}},
			stages: []flow.Stage{{ID: "a", SkillsDir: skills.Dir(a)}},
		}
		f := &flow.Flow{Stages: []flow.Stage{{ID: "a", SkillsDir: skills.Dir(filepath.Join(shared, "not-yet"))}}}
		got, _, err := prepareSkillMounts(project, "", home, f, cfg, saved)
		if err != nil || len(got) != 1 || got[0].Path != a {
			t.Fatalf("mounts = %+v, %v; want only the saved library", got, err)
		}
	})
	t.Run("a worktree anchors relative libraries", func(t *testing.T) {
		wt := mkdirAll(t, filepath.Join(project, ".worktrees", "wt"))
		writeCLISkill(t, filepath.Join(wt, "skills"), "tdd")
		f := &flow.Flow{SkillsDir: "skills", Stages: []flow.Stage{{ID: "a"}}}
		got, _, err := prepareSkillMounts(project, wt, home, f, config.Config{}, nil)
		if err != nil || len(got) != 0 {
			t.Fatalf("mounts = %+v, %v; want the worktree library without a mount", got, err)
		}
	})
}

func TestCheckReloadCompatible_ContainerNeedsRestartForANewSkillLibrary(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "1")
	chdirTemp(t)
	prev := &flow.Flow{Name: "f", Stages: []flow.Stage{{ID: "s"}}}
	same := &flow.Flow{Name: "f", Stages: []flow.Stage{{ID: "s", SkillsDir: "skills"}}}
	external := &flow.Flow{Name: "f", Stages: []flow.Stage{{ID: "s", SkillsDir: "/shared/skills"}}}

	for name, next := range map[string]*flow.Flow{"inside the project": same, "outside the project": external} {
		err := compatLoop(config.Config{}).checkReloadCompatible(prev, next)
		if err == nil || !strings.Contains(err.Error(), "restart `afm run`") {
			t.Fatalf("%s: err = %v, want a restart error: the host never checked this library", name, err)
		}
	}
}

func symlinkTo(t *testing.T, target, link string) string {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	return link
}

// The container sees only the Docker mount destinations, and a link there is followed by its literal target.
// So the decision must not change when everything outside the destinations is removed.
func TestSkillMountDirs_SameOnTheHostAndInTheContainer(t *testing.T) {
	type setup func(t *testing.T, root, project string) (libs []string, extra config.ExtraMounts, want, absentInContainer []string)
	for name, build := range map[string]setup{
		"inside the project": func(t *testing.T, root, p string) ([]string, config.ExtraMounts, []string, []string) {
			return []string{mkdirAll(t, filepath.Join(p, "skills"))}, nil, nil, nil
		},
		"a link to a directory inside the project": func(t *testing.T, root, p string) ([]string, config.ExtraMounts, []string, []string) {
			mkdirAll(t, filepath.Join(p, "skills"))
			return []string{symlinkTo(t, "skills", filepath.Join(p, "alias"))}, nil, nil, nil
		},
		"a project link out": func(t *testing.T, root, p string) ([]string, config.ExtraMounts, []string, []string) {
			ext := mkdirAll(t, filepath.Join(root, "ext", "lib"))
			return []string{symlinkTo(t, ext, filepath.Join(p, "skills"))}, nil, []string{ext}, []string{filepath.Join(root, "ext")}
		},
		"a project link out and an extra link to the same target": func(t *testing.T, root, p string) ([]string, config.ExtraMounts, []string, []string) {
			ext := mkdirAll(t, filepath.Join(root, "ext", "lib"))
			m := symlinkTo(t, ext, filepath.Join(root, "m"))
			return []string{symlinkTo(t, ext, filepath.Join(p, "skills"))}, config.ExtraMounts{{Path: m}}, []string{ext}, nil
		},
		"two links in a row": func(t *testing.T, root, p string) ([]string, config.ExtraMounts, []string, []string) {
			ext := mkdirAll(t, filepath.Join(root, "ext", "lib"))
			hop := symlinkTo(t, ext, filepath.Join(root, "hop"))
			return []string{symlinkTo(t, hop, filepath.Join(p, "skills"))}, nil, []string{hop}, []string{hop, filepath.Join(root, "ext")}
		},
		"a relative link out": func(t *testing.T, root, p string) ([]string, config.ExtraMounts, []string, []string) {
			ext := mkdirAll(t, filepath.Join(root, "ext", "lib"))
			return []string{symlinkTo(t, "../ext/lib", filepath.Join(p, "skills"))}, nil, []string{ext}, []string{filepath.Join(root, "ext")}
		},
		"the declared path is itself a link": func(t *testing.T, root, p string) ([]string, config.ExtraMounts, []string, []string) {
			ext := mkdirAll(t, filepath.Join(root, "ext", "lib"))
			hop := symlinkTo(t, ext, filepath.Join(root, "hop"))
			return []string{hop}, nil, []string{hop}, []string{hop, filepath.Join(root, "ext")}
		},
		"a target covered by a real extra mount": func(t *testing.T, root, p string) ([]string, config.ExtraMounts, []string, []string) {
			ext := mkdirAll(t, filepath.Join(root, "ext", "lib"))
			return []string{symlinkTo(t, ext, filepath.Join(p, "skills"))}, config.ExtraMounts{{Path: filepath.Join(root, "ext")}}, nil, nil
		},
		"declared through the path of an extra link": func(t *testing.T, root, p string) ([]string, config.ExtraMounts, []string, []string) {
			ext := mkdirAll(t, filepath.Join(root, "ext", "lib"))
			m := symlinkTo(t, filepath.Dir(ext), filepath.Join(root, "m"))
			return []string{filepath.Join(m, "lib")}, config.ExtraMounts{{Path: m}}, nil, nil
		},
		"two libraries with one target": func(t *testing.T, root, p string) ([]string, config.ExtraMounts, []string, []string) {
			ext := mkdirAll(t, filepath.Join(root, "ext", "lib"))
			a := symlinkTo(t, ext, filepath.Join(p, "a"))
			b := symlinkTo(t, "../ext/lib", filepath.Join(p, "b"))
			return []string{a, b, ext}, nil, []string{ext}, []string{filepath.Join(root, "ext")}
		},
		"a library of the project that does not exist": func(t *testing.T, root, p string) ([]string, config.ExtraMounts, []string, []string) {
			return []string{filepath.Join(p, "missing")}, nil, nil, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			project := mkdirAll(t, filepath.Join(root, "proj"))
			libs, extra, want, absentInContainer := build(t, root, project)

			if got := skillMountDirs(libs, sameSpellingDests(project, "/home/u", extra)); !slices.Equal(got, want) {
				t.Fatalf("host: skillMountDirs = %v, want %v", got, want)
			}
			for _, path := range absentInContainer {
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
			}
			if got := skillMountDirs(libs, sameSpellingDests(project, "/home/u", extra)); !slices.Equal(got, want) {
				t.Fatalf("container: skillMountDirs = %v, want the host's %v", got, want)
			}
		})
	}
}

// A mount source may be a link that leads back into the project (macOS /tmp), but nowhere else outside the
// allowed roots.
func TestCheckSkillMounts_LinkedSources(t *testing.T) {
	root := t.TempDir()
	project := mkdirAll(t, filepath.Join(root, "proj"))
	home := mkdirAll(t, filepath.Join(root, "home"))
	lib := mkdirAll(t, filepath.Join(project, "real", "lib"))
	hop := symlinkTo(t, lib, filepath.Join(root, "hop"))
	shared := mkdirAll(t, filepath.Join(root, "shared", "lib"))
	sharedHop := symlinkTo(t, shared, filepath.Join(root, "shared-hop"))
	outside := symlinkTo(t, shared, filepath.Join(root, "outside-hop"))

	if err := checkSkillMounts([]string{hop}, project, home, config.Config{}, project); err != nil {
		t.Fatalf("a link whose target is inside the project: %v", err)
	}
	cfg := config.Config{SkillsDir: skills.Dir(sharedHop)}
	if err := checkSkillMounts([]string{sharedHop}, project, home, cfg, project); err != nil {
		t.Fatalf("a link allowed in config by its own path: %v", err)
	}
	if err := checkSkillMounts([]string{outside}, project, home, config.Config{}, project); err == nil {
		t.Fatal("a link out of the project that config does not allow must be rejected")
	}
}

// With collaboration off no team agent is created, so the host preflight may not
// count a team library.
func TestPrepareSkillMounts_ResumeWithoutCollaborationIgnoresTeamLibraries(t *testing.T) {
	t.Setenv("AFM_COLLABORATION", "0")
	root := t.TempDir()
	project := mkdirAll(t, filepath.Join(root, "proj"))
	home := mkdirAll(t, filepath.Join(root, "home"))
	other := mkdirAll(t, filepath.Join(root, "other"))
	writeCLISkill(t, other, "tdd")
	f := &flow.Flow{Name: "f", Stages: []flow.Stage{{ID: "a"}}, Team: flow.Team{{ID: "m", SkillsDir: skills.Dir(other)}}}
	saved := &savedRun{
		state:  state.RunState{Stages: map[string]state.StageState{"a": {Status: state.StatusPending}}},
		stages: []flow.Stage{{ID: "a"}},
	}

	got, _, err := prepareSkillMounts(project, "", home, f, config.Config{}, saved)
	if err != nil || len(got) != 0 {
		t.Fatalf("mounts = %+v, %v; want none and no error for a team library nobody reads", got, err)
	}
}

// The container cannot see a link's target before it is mounted, so a new linked library must still ask for a restart.
func TestCheckReloadCompatible_ContainerRestartsForANewLinkedLibrary(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "1")
	dir := chdirTemp(t)
	external := mkdirAll(t, filepath.Join(t.TempDir(), "other"))
	symlinkTo(t, external, filepath.Join(dir, "skills2"))
	if err := os.Remove(external); err != nil { // not mounted in the container
		t.Fatal(err)
	}
	prev := &flow.Flow{Name: "f", Stages: []flow.Stage{{ID: "s"}}}
	next := &flow.Flow{Name: "f", Stages: []flow.Stage{{ID: "s", SkillsDir: "skills2"}}}

	err := compatLoop(config.Config{}).checkReloadCompatible(prev, next)
	if err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("err = %v, want a restart error", err)
	}
}

func TestSavedRunFor(t *testing.T) {
	runs := t.TempDir()
	f := &flow.Flow{Name: "flow", Stages: []flow.Stage{{ID: "s"}}}
	seedRun(t, runs, "flow-20260101-000000-abcd", state.StatusPending, "")

	if got, err := savedRunFor(worktree.ClearNone, runs, "flow", f); err != nil || got == nil {
		t.Fatalf("a plain start: savedRunFor = %v, %v; want the unfinished run", got, err)
	}
	if got, err := savedRunFor(worktree.ClearAll, runs, "flow", f); err != nil || got != nil {
		t.Fatalf("a clear flag starts a fresh run: savedRunFor = %v, %v; want nil", got, err)
	}
}

// A saved registry or journal is writable by an agent in the container, so a skills_dir read from it must be
// judged like one from YAML: "~" and relative paths would be checked against the CWD but mounted from the host
// home or the project.
func TestPrepareSkillMounts_SavedRelativeLibrariesAreRejected(t *testing.T) {
	root := t.TempDir()
	project := mkdirAll(t, filepath.Join(root, "proj"))
	home := mkdirAll(t, filepath.Join(root, "home"))
	t.Chdir(project)
	for _, dir := range []string{"~", "~/.ssh", "rel"} {
		mkdirAll(t, filepath.Join(project, dir))
	}
	f := &flow.Flow{Name: "f", Stages: []flow.Stage{{ID: "a"}}}
	pending := map[string]state.StageState{"a": {Status: state.StatusPending}}

	for _, dir := range []string{"~", "~/.ssh", "rel"} {
		t.Run("registry "+dir, func(t *testing.T) {
			saved := &savedRun{state: state.RunState{Stages: pending}, stages: []flow.Stage{{ID: "a", SkillsDir: skills.Dir(dir)}}}
			got, _, err := prepareSkillMounts(project, "", home, f, config.Config{}, saved)
			if err == nil || !strings.Contains(err.Error(), "absolute") {
				t.Fatalf("mounts = %+v, err = %v; want an error asking for an absolute path", got, err)
			}
		})
		t.Run("agent task "+dir, func(t *testing.T) {
			saved := &savedRun{state: state.RunState{
				Stages: pending,
				Assistants: state.AssistantsState{Tasks: map[assistants.TaskID]*state.TaskState{
					"t-m": savedTask(dir, assistants.StatusWatching),
				}},
			}}
			f := &flow.Flow{Name: "f", Stages: []flow.Stage{{ID: "a"}}}
			got, _, err := prepareSkillMounts(project, "", home, f, config.Config{}, saved)
			if err == nil || !strings.Contains(err.Error(), "absolute") {
				t.Fatalf("mounts = %+v, err = %v; want an error asking for an absolute path", got, err)
			}
		})
	}
	t.Run("a path that is not clean", func(t *testing.T) {
		saved := &savedRun{state: state.RunState{Stages: pending}, stages: []flow.Stage{{ID: "a", SkillsDir: skills.Dir(project + "/rel/../rel")}}}
		if _, _, err := prepareSkillMounts(project, "", home, f, config.Config{}, saved); err == nil {
			t.Fatal("a saved path with .. must be rejected: it is not what Resolve produces")
		}
	})
}

// The string that is checked is the string Docker mounts: a relative or "~" source is never accepted.
func TestCheckSkillMounts_RejectsNonAbsoluteSources(t *testing.T) {
	root := t.TempDir()
	project := mkdirAll(t, filepath.Join(root, "proj"))
	home := mkdirAll(t, filepath.Join(root, "home"))
	t.Chdir(project)
	mkdirAll(t, filepath.Join(project, "~"))
	for _, dir := range []string{"~", "~/.ssh", "rel"} {
		err := checkSkillMounts([]string{dir}, project, home, config.Config{}, project)
		if err == nil || !strings.Contains(err.Error(), "absolute") {
			t.Fatalf("checkSkillMounts(%q) = %v, want an error asking for an absolute path", dir, err)
		}
	}
}

// A computed source that is not on the host would be created by `docker -v` as an empty root-owned directory.
func TestCheckSkillMounts_SourceMustExist(t *testing.T) {
	root := t.TempDir()
	project := mkdirAll(t, filepath.Join(root, "proj"))
	home := mkdirAll(t, filepath.Join(root, "home"))
	missing := filepath.Join(root, "shared")

	err := checkSkillMounts([]string{missing}, project, home, config.Config{SkillsDir: skills.Dir(missing)}, project)

	if err == nil || !strings.Contains(err.Error(), "existing directory") {
		t.Fatalf("err = %v, want a missing-source error even though config allows the path", err)
	}
}

// The project is reached through a link, and a relative link of the library climbs above that link. The
// container resolves it from the mount destination, where the library is not, while the host reads it from
// the link's real parent: the computed source is absent on the host, so the run must stop before `docker -v`.
func TestPrepareSkillMounts_LinkClimbingAboveALinkedProjectAncestor(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realParent := mkdirAll(t, filepath.Join(root, "real", "dir"))
	realProject := mkdirAll(t, filepath.Join(realParent, "proj"))
	library := mkdirAll(t, filepath.Join(root, "real", "shared"))
	writeCLISkill(t, library, "tdd")
	symlinkTo(t, "../../shared", filepath.Join(realProject, "skills"))
	alias := symlinkTo(t, realParent, filepath.Join(root, "alias"))
	project := filepath.Join(alias, "proj")
	home := mkdirAll(t, filepath.Join(root, "home"))
	f := &flow.Flow{Name: "f", SkillsDir: "skills", Stages: []flow.Stage{{ID: "a"}}}

	for name, allowed := range map[string]string{
		"config allows the path the container would use": filepath.Join(root, "shared"),
		"config allows the real library":                 library,
	} {
		t.Run(name, func(t *testing.T) {
			got, _, err := prepareSkillMounts(project, "", home, f, config.Config{SkillsDir: skills.Dir(allowed)}, nil)
			if err == nil || !strings.Contains(err.Error(), "existing directory") {
				t.Fatalf("mounts = %+v, err = %v; want a missing-source error", got, err)
			}
		})
	}
}

// A link that starts in a subdirectory resolves from that subdirectory, not from the mount root.
func TestSkillMountDirs_NestedRelativeLink(t *testing.T) {
	root := t.TempDir()
	project := mkdirAll(t, filepath.Join(root, "proj"))
	ext := mkdirAll(t, filepath.Join(root, "ext", "lib"))
	mkdirAll(t, filepath.Join(project, "sub"))
	link := symlinkTo(t, "../../ext/lib", filepath.Join(project, "sub", "skills"))

	if got := skillMountDirs([]string{link}, sameSpellingDests(project, "/home/u", nil)); !slices.Equal(got, []string{ext}) {
		t.Fatalf("mounts = %v, want [%s]", got, ext)
	}
}

// Where the project is reached through a link (macOS /tmp → /private/tmp), a library whose canonical path is in
// the real project exposes nothing new and is accepted.
func TestCheckSkillMounts_ProjectReachedThroughALinkedAncestor(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realProject := mkdirAll(t, filepath.Join(root, "real", "proj"))
	library := mkdirAll(t, filepath.Join(realProject, "skills"))
	alias := symlinkTo(t, filepath.Join(root, "real"), filepath.Join(root, "alias"))
	home := mkdirAll(t, filepath.Join(root, "home"))

	if err := checkSkillMounts([]string{library}, filepath.Join(alias, "proj"), home, config.Config{}, root); err != nil {
		t.Fatalf("a library inside the real project: %v", err)
	}
}

// checkedAtReExec emulates the libraries the host verified and passed to the container at re-exec.
func checkedAtReExec(t *testing.T, dirs ...string) {
	t.Helper()
	t.Setenv(docker.SkillLibrariesEnvVar, strings.Join(dirs, string(os.PathListSeparator)))
}

// Only the host can tell whether a path leads to the same library on both sides, so a repeat cycle in the
// container takes exactly the libraries the host checked at re-exec; a directory that merely exists there
// (created by a stage, or from the image) is not one of them.
func TestCheckReloadCompatible_ContainerTakesOnlyHostCheckedLibraries(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "1")
	chdirTemp(t)
	shared := mkdirAll(t, filepath.Join(t.TempDir(), "shared"))
	sub := mkdirAll(t, filepath.Join(shared, "sub"))
	notMounted := filepath.Join(t.TempDir(), "later")
	createdInContainer := mkdirAll(t, filepath.Join(t.TempDir(), "newskills"))
	flowWith := func(dir string) *flow.Flow {
		return &flow.Flow{Name: "f", Stages: []flow.Stage{{ID: "s", SkillsDir: skills.Dir(dir)}}}
	}

	t.Run("a path below a checked library is not the checked library", func(t *testing.T) {
		checkedAtReExec(t, shared)
		if err := compatLoop(config.Config{}).checkReloadCompatible(flowWith(shared), flowWith(sub)); err == nil {
			t.Fatal("/shared → /shared/sub: want restart")
		}
	})
	t.Run("a library the host never checked stays a restart even when the flow did not change", func(t *testing.T) {
		checkedAtReExec(t, shared)
		err := compatLoop(config.Config{}).checkReloadCompatible(flowWith(notMounted), flowWith(notMounted))
		if err == nil || !strings.Contains(err.Error(), notMounted) || !strings.Contains(err.Error(), "restart `afm run`") {
			t.Fatalf("err = %v, want a restart error naming %s", err, notMounted)
		}
	})
	t.Run("a directory that exists in the container but was never checked is a restart", func(t *testing.T) {
		checkedAtReExec(t, shared)
		err := compatLoop(config.Config{}).checkReloadCompatible(flowWith(shared), flowWith(createdInContainer))
		if err == nil || !strings.Contains(err.Error(), createdInContainer) || !strings.Contains(err.Error(), "restart `afm run`") {
			t.Fatalf("err = %v, want a restart error naming %s", err, createdInContainer)
		}
	})
	t.Run("the same checked library is fine", func(t *testing.T) {
		checkedAtReExec(t, shared)
		if err := compatLoop(config.Config{}).checkReloadCompatible(flowWith(shared), flowWith(shared)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("an extra mount covering a path does not make it checked", func(t *testing.T) {
		checkedAtReExec(t, shared)
		cfg := config.Config{Docker: config.DockerConfig{ExtraMounts: config.ExtraMounts{{Path: shared}}}}
		if err := compatLoop(cfg).checkReloadCompatible(flowWith(shared), flowWith(sub)); err == nil {
			t.Fatal("want a restart: only the host's verdict counts")
		}
	})
	t.Run("without the host's list every library is a restart, none is fine", func(t *testing.T) {
		t.Setenv(docker.SkillLibrariesEnvVar, "")
		if err := compatLoop(config.Config{}).checkReloadCompatible(flowWith(shared), flowWith(shared)); err == nil {
			t.Fatal("a library with no host verdict must restart")
		}
		none := &flow.Flow{Name: "f", Stages: []flow.Stage{{ID: "s"}}}
		if err := compatLoop(config.Config{}).checkReloadCompatible(none, none); err != nil {
			t.Fatalf("no library, nothing to check: %v", err)
		}
	})
}

// Preflight reads a library through the host's links, the container resolves the same links from the mount
// destination. When these end in different directories, the run would use another library than the one checked.
func TestPrepareSkillMounts_RejectsALibraryTheContainerResolvesDifferently(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realParent := mkdirAll(t, filepath.Join(root, "real", "dir"))
	realProject := mkdirAll(t, filepath.Join(realParent, "proj"))
	checked := mkdirAll(t, filepath.Join(root, "real", "shared")) // A: what the host preflight reads
	writeCLISkill(t, checked, "tdd")
	other := mkdirAll(t, filepath.Join(root, "shared")) // B: where the container's link leads
	writeCLISkill(t, other, "other")
	symlinkTo(t, "../../shared", filepath.Join(realProject, "skills"))
	alias := symlinkTo(t, realParent, filepath.Join(root, "alias"))
	project := filepath.Join(alias, "proj")
	home := mkdirAll(t, filepath.Join(root, "home"))
	f := &flow.Flow{Name: "f", SkillsDir: "skills", Stages: []flow.Stage{{ID: "a"}}}

	for name, allowed := range map[string]string{
		"config allows the container's library": other,
		"config allows the checked library":     checked,
	} {
		t.Run(name, func(t *testing.T) {
			got, _, err := prepareSkillMounts(project, "", home, f, config.Config{SkillsDir: skills.Dir(allowed)}, nil)
			if err == nil {
				t.Fatalf("mounts = %+v, want an error: the mounted library is not the checked one", got)
			}
		})
	}
}

// Where nothing differs, the project reached through a link and a plain library inside it stay accepted.
func TestPrepareSkillMounts_LibraryInsideALinkedProjectIsFine(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realProject := mkdirAll(t, filepath.Join(root, "real", "proj"))
	writeCLISkill(t, mkdirAll(t, filepath.Join(realProject, "skills")), "tdd")
	alias := symlinkTo(t, filepath.Join(root, "real"), filepath.Join(root, "alias"))
	home := mkdirAll(t, filepath.Join(root, "home"))
	f := &flow.Flow{Name: "f", SkillsDir: "skills", Stages: []flow.Stage{{ID: "a"}}}

	got, _, err := prepareSkillMounts(filepath.Join(alias, "proj"), "", home, f, config.Config{}, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("mounts = %+v, err = %v; want none", got, err)
	}
}
