package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
)

// runRootDirInline — снимок прежней (до Task 13) инлайн-логики
// cmd/afm/run.go для agentRootDir, буквально скопированный сюда как
// эталон. resolveAgentRoot обязан давать байт-в-байт тот же результат для
// любого представительного флоу — регрессия здесь означает регрессию
// `afm run`.
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

// TestResolveAgentRoot_MatchesRunInlineLogic — регрессионный тест (review
// task-13, шаг 1): resolveAgentRoot должен совпадать с прежней инлайн-
// логикой run.go для представительных флоу (относительный/абсолютный/
// пустой root_dir). Любое расхождение здесь ломает `afm run` за пределами
// видимости остальных тестов cmd/afm.
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

// TestResolveMemoryDir_MatchesRunInlineLogic — та же регрессионная проверка
// для memDir, включая случай "память выключена" и все комбинации
// agentRoot пуст/непуст × memory.path относительный/абсолютный.
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

// TestAbsoluteAgentRoot_NonEmptyRootDirMatchesResolveAgentRoot — когда
// f.RootDir непустой, absoluteAgentRoot обязан согласовываться с
// resolveAgentRoot (тот же путь, только гарантированно абсолютный/чистый).
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

// TestAbsoluteAgentRoot_EmptyRootDirReturnsCWD — review task-13 (#14): для
// rebuild пустой root_dir не может означать "унаследовать CWD" (как в `afm
// run`, где это устраивает Executor.Dir) — rebuild'у нужен стабильный
// абсолютный путь для манифеста/лока/Executor.Dir "с нуля", поэтому
// absoluteAgentRoot возвращает АБСОЛЮТНЫЙ текущий CWD процесса.
func TestAbsoluteAgentRoot_EmptyRootDirReturnsCWD(t *testing.T) {
	tmp := t.TempDir()
	// На macOS t.TempDir() может лежать под символической ссылкой
	// (/tmp -> /private/tmp); резолвим её, иначе сравнение с os.Getwd()
	// (который тоже возвращает уже разрешённый путь) может ложно разойтись.
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

// TestAbsoluteAgentRoot_DiffersFromResolveAgentRoot_OnEmptyRootDir —
// документирует единственный контракт-разрыв между двумя хелперами (review
// #14: "один хелпер не может удовлетворить оба контракта"): при пустом
// root_dir resolveAgentRoot отдаёт "" (агенты `afm run` наследуют CWD
// процесса — Executor.Dir), а absoluteAgentRoot отдаёт непустой абсолютный
// CWD.
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

// TestLifecycleRootDir_NonEmptyPassesThrough — task-13: непустой
// agentRootDir (уже разрешённый resolveAgentRoot) отдаётся хуками как есть,
// без повторного резолва.
func TestLifecycleRootDir_NonEmptyPassesThrough(t *testing.T) {
	got := lifecycleRootDir("/agent/root")
	if got != "/agent/root" {
		t.Errorf("lifecycleRootDir(%q) = %q, want passthrough", "/agent/root", got)
	}
}

// TestLoadHookSecretLayers_ProjectOverridesGlobal — Task 3 (lifecycle hooks
// phase 3): loadHookSecretLayers должен вести себя как docker.LoadSecretLayers
// с project-слоем последним (project приоритетнее global) — она лишь
// делегирует, но сам факт делегирования (пути afmRoot/.afm/secrets.env)
// стоит закрепить регрессионным тестом здесь.
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

// TestLifecycleRootDir_EmptyReturnsAbsoluteCWD — task-13: пустой
// agentRootDir означает «агенты наследуют CWD процесса» (resolveAgentRoot),
// но контракт lifecycle-хуков (AFM_ROOT_DIR) требует непустой абсолютный
// путь — lifecycleRootDir должен подставить os.Getwd().
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
