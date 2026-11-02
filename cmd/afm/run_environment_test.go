package main

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// A relative afm-root (the default ".") must not leak into agent-facing paths: the agent's CWD is the project
// root or a worktree, where a relative root_dir or memory path would point somewhere else.
func TestPrepareRunEnvironment_RelativeAfmRootGivesAbsolutePaths(t *testing.T) {
	chdirTemp(t)
	dir, err := os.Getwd() // the resolved CWD (macOS reports /var temp dirs as /private/var)
	if err != nil {
		t.Fatal(err)
	}
	f := &flow.Flow{Name: "flow", RootDir: "src"}
	f.Memory.Path = "memory"

	env := prepareTestRunEnvironment(t, f, "")
	if want := filepath.Join(dir, "src"); env.RootDir != want {
		t.Errorf("RootDir = %q, want %q", env.RootDir, want)
	}
	if want := filepath.Join(dir, "src", "memory"); env.MemoryDir != want {
		t.Errorf("MemoryDir = %q, want %q", env.MemoryDir, want)
	}

	wt := prepareTestRunEnvironment(t, &flow.Flow{Name: "flow"}, filepath.Join(".worktrees", "feature"))
	if want := filepath.Join(dir, ".worktrees", "feature"); wt.RootDir != want {
		t.Errorf("worktree RootDir = %q, want %q", wt.RootDir, want)
	}
}

// prepareTestRunEnvironment is prepareRunEnvironment for a test: when it ends, PATH is restored and the afm-bin
// shim removed, so no later test resolves a bare `afm` to this test binary.
func prepareTestRunEnvironment(t *testing.T, f *flow.Flow, worktree string) runEnvironment {
	t.Helper()
	t.Setenv("PATH", os.Getenv("PATH"))
	env, err := prepareRunEnvironment(".", f, config.Default(), worktree, nil)
	if err != nil {
		t.Fatalf("prepareRunEnvironment: %v", err)
	}
	t.Cleanup(env.Close)
	return env
}

func skipWithoutSymlinks(t *testing.T) {
	t.Helper()
	if err := os.Symlink(t.TempDir(), filepath.Join(t.TempDir(), "probe")); err != nil {
		t.Skipf("PATH shim needs symlinks: %v", err)
	}
}

// Agents run the afm of this process, not whichever afm the user's PATH finds first (a stale brew copy
// lacks the commands the prompt tells the agent to use).
func TestPrepareRunEnvironment_PutsThisAfmFirstOnPath(t *testing.T) {
	skipWithoutSymlinks(t)
	chdirTemp(t)
	oldDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(oldDir, "afm"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	originalPath := oldDir + string(os.PathListSeparator) + os.Getenv("PATH")
	t.Setenv("PATH", originalPath)

	env, err := prepareRunEnvironment(".", &flow.Flow{Name: "flow"}, config.Default(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	dir, _, _ := strings.Cut(os.Getenv("PATH"), string(os.PathListSeparator))
	if !strings.HasPrefix(filepath.Base(dir), "afm-bin-") || env.AfmBinDir != dir {
		t.Fatalf("PATH starts with %q (AfmBinDir %q), want the afm-bin-* directory", dir, env.AfmBinDir)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wantTarget, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatal(err)
	}
	gotTarget, err := os.Readlink(filepath.Join(dir, "afm"))
	if err != nil || gotTarget != wantTarget {
		t.Fatalf("afm link = %q (%v), want %q", gotTarget, err, wantTarget)
	}

	env.Close()
	if got := os.Getenv("PATH"); got != originalPath {
		t.Errorf("PATH after Close = %q, want %q", got, originalPath)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("afm-bin directory survived Close: %v", err)
	}
}

// The same afm already first on PATH (Docker image, or a second prepare in one process) needs no shim.
func TestPrepareRunEnvironment_SkipsShimWhenPathAlreadyHasThisAfm(t *testing.T) {
	skipWithoutSymlinks(t)
	chdirTemp(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(binDir, "afm")); err != nil {
		t.Fatal(err)
	}
	originalPath := binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	t.Setenv("PATH", originalPath)

	env, err := prepareRunEnvironment(".", &flow.Flow{Name: "flow"}, config.Default(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	if env.AfmBinDir != "" || os.Getenv("PATH") != originalPath {
		t.Fatalf("shim installed although PATH already resolves afm to this binary: dir=%q PATH=%q", env.AfmBinDir, os.Getenv("PATH"))
	}
}

func recipeTestConfig(agents map[string]config.AgentRecipe) config.Config {
	autoShim := true
	cfg := config.Default()
	cfg.Docker.AutoShim = &autoShim
	cfg.Docker.Agents = agents
	return cfg
}

// stubClaudeOnPATH puts a fake claude first in PATH: generating a claude-type wrapper looks the real one up.
func stubClaudeOnPATH(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\nexit 97\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A task records the identity of the recipe its command's wrapper is built from and compares it before every turn.
// In the container every docker.agents alias has one, generated here or not: a saved task left without its wrapper
// because its recipe changed must still see the change. Without a recipe, and for any command on the host, there is none.
func TestPrepareRunEnvironment_RecipeIdentity(t *testing.T) {
	chdirTemp(t)
	stubClaudeOnPATH(t)
	recipe := config.AgentRecipe{Type: config.ClaudeCommand, Model: "fake-model-1", Auth: config.RecipeAuth{To: "env:ANTHROPIC_API_KEY"}}
	unused := recipe
	unused.Model = "fake-model-unused"
	f := &flow.Flow{Name: "flow", Stages: []flow.Stage{stageWithCommand("b", "fake-recipe")}}
	identity := func(model string) func(string) string {
		t.Helper()
		used := recipe
		used.Model = model
		agents := map[string]config.AgentRecipe{"fake-recipe": used, "unused-recipe": unused, config.ClaudeCommand: recipe}
		env, err := prepareRunEnvironment(".", f, recipeTestConfig(agents), "", nil)
		if err != nil {
			t.Fatalf("prepareRunEnvironment: %v", err)
		}
		t.Cleanup(env.Close)
		return env.RecipeIdentity
	}

	t.Setenv("AFM_IN_DOCKER", "1")
	inContainer := identity("fake-model-1")
	if got := inContainer("fake-recipe"); got == "" || got != recipe.Identity() {
		t.Fatalf("identity of the generated wrapper = %q, want its recipe's %q", got, recipe.Identity())
	}
	if got := inContainer("unused-recipe"); got != unused.Identity() {
		t.Errorf("identity of a recipe nothing uses = %q, want its own %q", got, unused.Identity())
	}
	for _, cmd := range []string{config.ClaudeCommand, "/opt/ext-as-claude.sh"} {
		if got := inContainer(cmd); got != "" {
			t.Errorf("identity of %q = %q, want none: no wrapper is ever built for it", cmd, got)
		}
	}
	if identity("fake-model-2")("fake-recipe") == inContainer("fake-recipe") {
		t.Error("another model of the alias must give another identity")
	}

	t.Setenv("AFM_IN_DOCKER", "")
	host := identity("fake-model-1")
	for _, cmd := range []string{"fake-recipe", "unused-recipe"} {
		if got := host(cmd); got != "" {
			t.Errorf("identity of %q on the host = %q, want none: the host generates no wrapper", cmd, got)
		}
	}
}

// In the container the saved run's unfinished tasks get their wrappers too, even when no profile names the recipe.
func TestPrepareRunEnvironment_WrapperForSavedTask(t *testing.T) {
	chdirTemp(t)
	stubClaudeOnPATH(t)
	t.Setenv("AFM_IN_DOCKER", "1")
	cfg := recipeTestConfig(map[string]config.AgentRecipe{
		"old-recipe": {Type: config.ClaudeCommand, Model: "m", Auth: config.RecipeAuth{To: "env:ANTHROPIC_API_KEY"}},
	})
	saved := &savedRun{state: state.RunState{Assistants: state.AssistantsState{Tasks: map[assistants.TaskID]*state.TaskState{
		"t-old": savedHelperTask("old-recipe", assistants.StatusQueued, ""),
	}}}}
	for _, tc := range []struct {
		name  string
		saved *savedRun
		want  bool
	}{{"new run", nil, false}, {"resumed run", saved, true}} {
		env, err := prepareRunEnvironment(".", &flow.Flow{Name: "flow"}, cfg, "", tc.saved)
		if err != nil {
			t.Fatalf("%s: prepareRunEnvironment: %v", tc.name, err)
		}
		t.Cleanup(env.Close)
		if got := env.GeneratedAgents["old-recipe"]; got != tc.want {
			t.Errorf("%s: wrapper for the saved task's recipe generated = %t, want %t", tc.name, got, tc.want)
		}
	}
}

// boundHelperTask is a saved unfinished task admitted on command with the given recipe identity.
func boundHelperTask(command, identity string) *state.TaskState {
	t := savedHelperTask(command, assistants.StatusQueued, "")
	t.Execution = &assistants.ExecutionBinding{Source: assistants.CommandSourceProfile, RecipeIdentity: identity}
	return t
}

// The restart of a run whose helper's profile is gone and whose recipe now names another model and a secret nobody
// set: the task fails executor_changed before its first turn, so neither the host nor the container prepares its
// command, and the restart does not stop on that secret. The container still reports the new identity to compare.
func TestRestart_TaskOfAChangedRecipeNeedsNoWrapperOrSecret(t *testing.T) {
	chdirTemp(t)
	stubClaudeOnPATH(t)
	t.Setenv("AFM_COLLABORATION", "1")
	admitted := config.AgentRecipe{Type: config.ClaudeCommand, Model: "m1", Auth: config.RecipeAuth{From: "env:AFM_TEST_OLD_SECRET", To: "env:ANTHROPIC_API_KEY"}}
	changed := admitted
	changed.Model = "m2"
	changed.Auth.From = "env:AFM_TEST_MISSING_SECRET"
	kept := config.AgentRecipe{Type: config.ClaudeCommand, Model: "k", Auth: config.RecipeAuth{From: "env:AFM_TEST_KEPT_SECRET", To: "env:ANTHROPIC_API_KEY"}}
	saved := &savedRun{state: state.RunState{Assistants: state.AssistantsState{Tasks: map[assistants.TaskID]*state.TaskState{
		"t-stale": boundHelperTask("glm", admitted.Identity()),
		"t-kept":  boundHelperTask("kept-recipe", kept.Identity()),
		// Admitted when its command had no recipe: it now has one, which the task was not admitted with.
		"t-became-recipe": boundHelperTask("new-recipe", ""),
	}}}}
	f := &flow.Flow{Name: "flow", Stages: []flow.Stage{{ID: "s"}}}
	cfg := recipeTestConfig(map[string]config.AgentRecipe{"glm": changed, "kept-recipe": kept, "new-recipe": kept})

	t.Setenv("AFM_IN_DOCKER", "")
	host, err := projectDocker(f, cfg, resumeTaskCommands(saved, f, cfg)) // what reexecFlow hands docker.ReExec
	if err != nil {
		t.Fatalf("host projection: %v", err)
	}
	for _, cmd := range []string{"glm", "new-recipe"} {
		_, recipe := host.Recipes[cmd]
		if recipe || host.Generated[cmd] || slices.Contains(host.Extra, cmd) || slices.Contains(host.Commands, cmd) {
			t.Errorf("host prepares %s for a task that fails executor_changed: %+v", cmd, host)
		}
	}
	if _, ok := host.Recipes["kept-recipe"]; !ok || !slices.Equal(host.Extra, []string{"kept-recipe"}) {
		t.Errorf("host projection = %+v, want the matching task's recipe and its secret prepared", host)
	}

	t.Setenv("AFM_IN_DOCKER", "1")
	env, err := prepareRunEnvironment(".", f, cfg, "", saved)
	if err != nil {
		t.Fatalf("container: prepareRunEnvironment: %v", err)
	}
	t.Cleanup(env.Close)
	if !maps.Equal(env.GeneratedAgents, map[string]bool{"kept-recipe": true}) {
		t.Errorf("container wrappers = %v, want only the matching task's", env.GeneratedAgents)
	}
	// What executorChangedReason compares each binding with at the turn start.
	for cmd, want := range map[string]string{"glm": changed.Identity(), "new-recipe": kept.Identity(), "kept-recipe": kept.Identity()} {
		if got := env.RecipeIdentity(cmd); got != want {
			t.Errorf("container identity of %s = %q, want %q", cmd, got, want)
		}
	}
	if env.RecipeIdentity("glm") == admitted.Identity() || env.RecipeIdentity("new-recipe") == "" {
		t.Error("a changed recipe must not match the identity the task was admitted with")
	}
}

// A helper task is the only user of the codex adapter: its codex state is mounted only while its recipe is unchanged.
func TestRestart_CodexStateOnlyForAnUnchangedRecipe(t *testing.T) {
	t.Setenv("AFM_COLLABORATION", "1")
	admitted := config.AgentRecipe{Type: config.RecipeTypeCodex, Model: "gpt-1"}
	changed := admitted
	changed.Model = "gpt-2"
	f := &flow.Flow{Name: "flow", Stages: []flow.Stage{{ID: "s"}}}
	cfg := recipeTestConfig(map[string]config.AgentRecipe{"reviewer": changed})
	for identity, want := range map[string]bool{admitted.Identity(): false, changed.Identity(): true} {
		saved := &savedRun{state: state.RunState{Assistants: state.AssistantsState{Tasks: map[assistants.TaskID]*state.TaskState{
			"t": boundHelperTask("reviewer", identity),
		}}}}
		p, err := projectDocker(f, cfg, resumeTaskCommands(saved, f, cfg))
		if err != nil {
			t.Fatal(err)
		}
		if p.MountCodexState != want {
			t.Errorf("task admitted with the current recipe %t: MountCodexState = %t", want, p.MountCodexState)
		}
	}
}
