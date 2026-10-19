package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
)

// runRootDirInline — снимок прежней инлайн-логики cmd/afm/run.go для agentRootDir, буквально скопированный
// сюда как эталон.
func runRootDirInline(afmRoot string, f *flow.Flow) string {
	agentRootDir := f.RootDir
	if agentRootDir != "" && !filepath.IsAbs(agentRootDir) {
		agentRootDir = filepath.Join(afmRoot, agentRootDir)
	}
	return agentRootDir
}

// runMemDirInline — снимок прежней инлайн-логики cmd/afm/run.go для memDir.
func runMemDirInline(afmRoot, agentRoot string, f *flow.Flow) string {
	if !f.MemoryEnabled() {
		return ""
	}
	base := agentRoot
	if base == "" {
		base = afmRoot
	}
	memDir := f.Memory.Path
	if !filepath.IsAbs(memDir) {
		memDir = filepath.Join(base, memDir)
	}
	return memDir
}

func TestResolveAgentRoot_MatchesRunInlineLogic(t *testing.T) {
	cases := []struct {
		name    string
		afmRoot string
		rootDir string
	}{
		{"relative root_dir", "/afm/root", "sub/project"},
		{"absolute root_dir", "/afm/root", "/abs/project"},
		{"empty root_dir", "/afm/root", ""},
		{"relative root_dir with relative afmRoot", ".", "sub/project"},
		{"dot root_dir", "/afm/root", "."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &flow.Flow{RootDir: tc.rootDir}
			want := runRootDirInline(tc.afmRoot, f)
			got, err := resolveAgentRoot(tc.afmRoot, f)
			if err != nil {
				t.Fatalf("resolveAgentRoot: %v", err)
			}
			if got != want {
				t.Errorf("resolveAgentRoot(%q, root_dir=%q) = %q, want %q (inline)", tc.afmRoot, tc.rootDir, got, want)
			}
		})
	}
}

func TestResolveMemoryDir_MatchesRunInlineLogic(t *testing.T) {
	cases := []struct {
		name       string
		afmRoot    string
		agentRoot  string
		memoryPath string
	}{
		{"memory disabled", "/afm/root", "/agent/root", ""},
		{"relative memory.path, agentRoot set", "/afm/root", "/agent/root", "memory"},
		{"relative memory.path, agentRoot empty", "/afm/root", "", "memory"},
		{"absolute memory.path", "/afm/root", "/agent/root", "/external/memory"},
		{"absolute memory.path, agentRoot empty", "/afm/root", "", "/external/memory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &flow.Flow{Memory: flow.MemoryConfig{Path: tc.memoryPath}}
			want := runMemDirInline(tc.afmRoot, tc.agentRoot, f)
			got, err := resolveMemoryDir(tc.afmRoot, tc.agentRoot, f)
			if err != nil {
				t.Fatalf("resolveMemoryDir: %v", err)
			}
			if got != want {
				t.Errorf("resolveMemoryDir(%q, %q, memory.path=%q) = %q, want %q (inline)", tc.afmRoot, tc.agentRoot, tc.memoryPath, got, want)
			}
		})
	}
}

func TestAbsoluteAgentRoot_NonEmptyRootDirMatchesResolveAgentRoot(t *testing.T) {
	cases := []struct {
		name    string
		afmRoot string
		rootDir string
	}{
		{"relative root_dir", "/afm/root", "sub/project"},
		{"absolute root_dir", "/afm/root", "/abs/project"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &flow.Flow{RootDir: tc.rootDir}
			relWant, err := resolveAgentRoot(tc.afmRoot, f)
			if err != nil {
				t.Fatalf("resolveAgentRoot: %v", err)
			}
			got, err := absoluteAgentRoot(tc.afmRoot, f)
			if err != nil {
				t.Fatalf("absoluteAgentRoot: %v", err)
			}
			wantAbs, err := filepath.Abs(relWant)
			if err != nil {
				t.Fatal(err)
			}
			if got != wantAbs {
				t.Errorf("absoluteAgentRoot(%q, root_dir=%q) = %q, want %q", tc.afmRoot, tc.rootDir, got, wantAbs)
			}
			if !filepath.IsAbs(got) {
				t.Errorf("absoluteAgentRoot returned non-absolute path %q", got)
			}
		})
	}
}

func TestAbsoluteAgentRoot_EmptyRootDirReturnsCWD(t *testing.T) {
	tmp := t.TempDir()
	// На macOS t.TempDir может лежать под символической ссылкой; резолвим её, иначе сравнение с os.Getwd
	// может ложно разойтись.
	resolved, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatal(err)
	}

	prevWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(resolved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevWd) })

	f := &flow.Flow{RootDir: ""}
	got, err := absoluteAgentRoot("/afm/root", f)
	if err != nil {
		t.Fatalf("absoluteAgentRoot: %v", err)
	}
	if got != resolved {
		t.Errorf("absoluteAgentRoot with empty root_dir = %q, want CWD %q", got, resolved)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("absoluteAgentRoot returned non-absolute path %q", got)
	}
}

func TestAbsoluteAgentRoot_DiffersFromResolveAgentRoot_OnEmptyRootDir(t *testing.T) {
	f := &flow.Flow{RootDir: ""}
	relGot, err := resolveAgentRoot("/afm/root", f)
	if err != nil {
		t.Fatal(err)
	}
	if relGot != "" {
		t.Fatalf("resolveAgentRoot with empty root_dir = %q, want \"\"", relGot)
	}
	absGot, err := absoluteAgentRoot("/afm/root", f)
	if err != nil {
		t.Fatal(err)
	}
	if absGot == "" || !filepath.IsAbs(absGot) {
		t.Errorf("absoluteAgentRoot with empty root_dir = %q, want a non-empty absolute path", absGot)
	}
}

func TestLifecycleRootDir_NonEmptyPassesThrough(t *testing.T) {
	got := lifecycleRootDir("/agent/root")
	if got != "/agent/root" {
		t.Errorf("lifecycleRootDir(%q) = %q, want passthrough", "/agent/root", got)
	}
}

func TestLoadHookSecretLayers_ProjectOverridesGlobal(t *testing.T) {
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".afm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".afm", "secrets.env"), []byte("K=project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := loadHookSecretLayers(proj)
	if err != nil {
		t.Fatal(err)
	}
	if m["K"] != "project" {
		t.Errorf("loadHookSecretLayers(%q) = %#v, want K=project", proj, m)
	}
}

func TestLifecycleRootDir_EmptyReturnsAbsoluteCWD(t *testing.T) {
	wantWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got := lifecycleRootDir("")
	if got == "" || !filepath.IsAbs(got) {
		t.Errorf("lifecycleRootDir(\"\") = %q, want a non-empty absolute path", got)
	}
	if got != wantWd {
		t.Errorf("lifecycleRootDir(\"\") = %q, want CWD %q", got, wantWd)
	}
}

// loadRedactionSecrets collects every effective secret: both default secrets.env layers AND the override, the
// values recipe and hook references resolve to (environment included), skipping values under 4 characters.
func TestLoadRedactionSecrets_IncludesEveryActiveSource(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HOOK_TOKEN_FROM_ENV", "hook-env-secret")
	t.Setenv("RECIPE_TOKEN_FROM_ENV", "recipe-env-secret")
	for dir, content := range map[string]string{
		home:    "GLOBAL_TOKEN=global-secret-value\n",
		project: "PROJECT_TOKEN=project-secret-value\nPIN=4242\nDEBUG=1\n",
	} {
		if err := os.MkdirAll(filepath.Join(dir, ".afm"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".afm", "secrets.env"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	override := filepath.Join(t.TempDir(), "override.env")
	if err := os.WriteFile(override, []byte("OVERRIDE_TOKEN=override-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	cfg.Docker.SecretsFile = override
	cfg.Docker.Agents = map[string]config.AgentRecipe{"glm": {Auth: config.RecipeAuth{From: "env:RECIPE_TOKEN_FROM_ENV"}}}
	hooks := []lifecyclehooks.RegisteredHook{{Hook: lifecyclehooks.Hook{
		ID: "notify", Env: map[string]lifecyclehooks.SecretRef{"TOKEN": "env:HOOK_TOKEN_FROM_ENV"}, //nolint:gosec // a reference to a test env var, not a credential
		ResolvedEnv: map[string]string{"CHAT": "resolved-hook-value"},
	}}}

	got := loadRedactionSecrets(project, cfg, hooks)
	for _, want := range []string{"global-secret-value", "project-secret-value", "4242", "override-secret", "hook-env-secret", "recipe-env-secret", "resolved-hook-value"} {
		if !slices.Contains(got, want) {
			t.Errorf("redaction set %v is missing %q", got, want)
		}
	}
	if slices.Contains(got, "1") {
		t.Errorf("redaction set %v holds a value shorter than %d characters", got, minRedactedSecretLen)
	}
}
