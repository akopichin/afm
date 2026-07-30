package docker_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/flow"
)

func TestScanCommands_SkipsGenerated(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "glm51")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	f := &flow.Flow{
		Name: "test",
		Stages: []flow.Stage{
			{ID: "s1", Command: "glm51"}, // generated → не монтировать
		},
	}
	mounts := docker.ScanCommands(f, "claude", map[string]bool{"glm51": true})
	if len(mounts) != 0 {
		t.Errorf("generated command must not be mounted, got %d: %v", len(mounts), mounts)
	}

	// тот же flow, но glm51 не generated → монтируется
	mounts2 := docker.ScanCommands(f, "claude", nil)
	if len(mounts2) != 1 {
		t.Errorf("non-generated command must be mounted, got %d", len(mounts2))
	}
}

func TestScanCommands_SkipsClaude(t *testing.T) {
	f := &flow.Flow{
		Name: "test",
		Stages: []flow.Stage{
			{ID: "s1", Command: "claude"},
			{ID: "s2", Command: ""},
		},
	}
	mounts := docker.ScanCommands(f, "claude", nil)
	if len(mounts) != 0 {
		t.Errorf("expected 0 mounts, got %d: %v", len(mounts), mounts)
	}
}

func TestScanCommands_FindsBinary(t *testing.T) {
	// Создаём фиктивный бинарник во временной директории.
	dir := t.TempDir()
	binPath := filepath.Join(dir, "myagent")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	f := &flow.Flow{
		Name: "test",
		Stages: []flow.Stage{
			{ID: "s1", Command: "myagent"},
			{ID: "s2", Command: "claude"},
		},
	}
	mounts := docker.ScanCommands(f, "claude", nil)
	if len(mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(mounts))
	}
	if mounts[0].ContainerName != "myagent" {
		t.Errorf("ContainerName: got %q, want myagent", mounts[0].ContainerName)
	}
	if mounts[0].HostPath != binPath {
		t.Errorf("HostPath: got %q, want %q", mounts[0].HostPath, binPath)
	}
}

func TestScanCommands_DeduplicatesCommands(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "glm51")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	f := &flow.Flow{
		Name: "test",
		Stages: []flow.Stage{
			{ID: "s1", Command: "glm51"},
			{ID: "s2", Command: "glm51"},
			{ID: "s3", Command: "glm51"},
		},
	}
	mounts := docker.ScanCommands(f, "claude", nil)
	if len(mounts) != 1 {
		t.Errorf("expected 1 unique mount, got %d", len(mounts))
	}
}

func TestScanCommands_GlobalCmdMounted(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "glm51")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	f := &flow.Flow{
		Name:   "test",
		Stages: []flow.Stage{{ID: "s1", Command: ""}},
	}
	// Если globalCmd не claude — тоже монтируем.
	mounts := docker.ScanCommands(f, "glm51", nil)
	if len(mounts) != 1 {
		t.Fatalf("expected 1 mount for global cmd, got %d", len(mounts))
	}
	if mounts[0].HostPath != binPath {
		t.Errorf("HostPath: got %q, want %q", mounts[0].HostPath, binPath)
	}
}

func TestScanCommands_SkipsMissingBinary(t *testing.T) {
	f := &flow.Flow{
		Name: "test",
		Stages: []flow.Stage{
			{ID: "s1", Command: "nonexistent-binary-xyz-42"},
		},
	}
	mounts := docker.ScanCommands(f, "claude", nil)
	if len(mounts) != 0 {
		t.Errorf("expected 0 mounts for missing binary, got %d", len(mounts))
	}
}

func TestReExec_BuildsDockerArgs(t *testing.T) {
	// Перехватываем execFunc чтобы не запускать реальный docker.
	var capturedArgv0 string
	var capturedArgs []string
	docker.SetExecFunc(func(argv0 string, argv []string, envv []string) error {
		capturedArgv0 = argv0
		capturedArgs = argv
		return nil
	})
	defer docker.ResetExecFunc()

	// Создаём фейковый docker бинарник.
	dir := t.TempDir()
	dockerBin := filepath.Join(dir, "docker")
	if err := os.WriteFile(dockerBin, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	err := docker.ReExec(docker.ReExecConfig{
		Image:      "akopichin/afm:latest",
		ProjectDir: "/home/user/myproject",
		Commands: []docker.CommandMount{
			{HostPath: "/usr/local/bin/glm51", ContainerName: "glm51"},
		},
		DashboardPort: 9876,
		ExtraMounts:   []string{"~/.ai-free"},
		ExtraArgs:     []string{"run", "flow.yaml"},
	})
	if err != nil {
		t.Fatalf("ReExec: %v", err)
	}

	if capturedArgv0 != dockerBin {
		t.Errorf("argv0: got %q, want %q", capturedArgv0, dockerBin)
	}

	// Проверяем ключевые флаги в построенных аргументах.
	argsStr := strings.Join(capturedArgs, " ")
	home, _ := os.UserHomeDir()

	checks := []string{
		"docker run --rm",
		"-p 9876:9876",
		"-v /home/user/myproject:/home/user/myproject",
		":/home/afm/.claude", // хост-путь зависит от ОС, проверяем контейнерную часть (= HOME контейнера)
		":/home/afm/.afm",
		"-w /home/user/myproject",
		"-v /usr/local/bin/glm51:/usr/local/bin/glm51:ro",
		home + "/.ai-free:/home/afm/.ai-free:ro", // extra_mount: хост home/.ai-free → контейнер /home/afm/.ai-free ($HOME)
		"-e AFM_IN_DOCKER=1",
		"-e AFM_HOST_UID=", // привилегии дропаются до хостового uid (entrypoint gosu)
		"-e AFM_HOST_GID=",
		"akopichin/afm:latest",
		"run flow.yaml",
	}
	for _, check := range checks {
		if !strings.Contains(argsStr, check) {
			t.Errorf("args missing %q\nfull args: %s", check, argsStr)
		}
	}
}

func TestReExec_DockerNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // пустая PATH — docker не найдётся
	err := docker.ReExec(docker.ReExecConfig{
		Image:      "akopichin/afm:latest",
		ProjectDir: "/tmp/proj",
		ExtraArgs:  []string{"run", "flow.yaml"},
	})
	if err == nil {
		t.Fatal("expected error when docker not in PATH")
	}
}

func TestReExec_PassthroughEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")
	t.Setenv("ANTHROPIC_BASE_URL", "https://custom.api")

	var capturedArgs []string
	docker.SetExecFunc(func(argv0 string, argv []string, envv []string) error {
		capturedArgs = argv
		return nil
	})
	defer docker.ResetExecFunc()

	dir := t.TempDir()
	dockerBin := filepath.Join(dir, "docker")
	if err := os.WriteFile(dockerBin, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	if err := docker.ReExec(docker.ReExecConfig{
		Image:      "akopichin/afm:latest",
		ProjectDir: "/tmp/proj",
		ExtraArgs:  []string{"run", "flow.yaml"},
	}); err != nil {
		t.Fatal(err)
	}

	argsStr := strings.Join(capturedArgs, " ")
	// Секреты передаём в bare-форме `-e KEY` (без значения), чтобы они не
	// попадали в argv `docker run` и не светились в `ps aux`/history/audit.
	if !strings.Contains(argsStr, "-e ANTHROPIC_API_KEY") {
		t.Errorf("ANTHROPIC_API_KEY bare flag not passed: %s", argsStr)
	}
	if !strings.Contains(argsStr, "-e ANTHROPIC_BASE_URL") {
		t.Errorf("ANTHROPIC_BASE_URL bare flag not passed: %s", argsStr)
	}
	// Старая форма c явным значением НЕ должна присутствовать — это утечка секрета.
	if strings.Contains(argsStr, "-e ANTHROPIC_API_KEY=sk-test-key") {
		t.Errorf("ANTHROPIC_API_KEY leaked in argv with value: %s", argsStr)
	}
	if strings.Contains(argsStr, "-e ANTHROPIC_BASE_URL=https://custom.api") {
		t.Errorf("ANTHROPIC_BASE_URL leaked in argv with value: %s", argsStr)
	}
}

func TestReExec_RecipeTransientEnv_NoMount(t *testing.T) {
	// секрет в host-only файле
	tokFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokFile, []byte("secret-value\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var capturedArgs []string
	docker.SetExecFunc(func(argv0 string, argv []string, envv []string) error {
		capturedArgs = argv
		return nil
	})
	defer docker.ResetExecFunc()

	// fake docker
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	recipes := map[string]config.AgentRecipe{
		"glm51": {Model: "glm-5.1", Auth: config.RecipeAuth{From: "file:" + tokFile, To: "env:ANTHROPIC_AUTH_TOKEN"}},
	}
	err := docker.ReExec(docker.ReExecConfig{
		Image:       "akopichin/afm:latest",
		ProjectDir:  "/tmp/proj",
		ExtraArgs:   []string{"run", "flow.yaml"},
		Recipes:     recipes,
		SecretsFile: "",
	})
	if err != nil {
		t.Fatalf("ReExec: %v", err)
	}

	argsStr := strings.Join(capturedArgs, " ")
	// transient bare-form -e AFM_SECRET_GLM51 (без значения в argv)
	if !strings.Contains(argsStr, "-e AFM_SECRET_GLM51") {
		t.Errorf("missing transient -e AFM_SECRET_GLM51: %s", argsStr)
	}
	// значение секрета НЕ в argv
	if strings.Contains(argsStr, "secret-value") {
		t.Errorf("secret value leaked into argv: %s", argsStr)
	}
	// AFM_URL нет (url читается из cfg в контейнере)
	if strings.Contains(argsStr, "AFM_URL_") {
		t.Errorf("AFM_URL_ must not be passed: %s", argsStr)
	}
	// cleanup transient env
	if err := os.Unsetenv("AFM_SECRET_GLM51"); err != nil {
		t.Fatal(err)
	}
}

func TestReExec_RecipeMissingSecretFailFast(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	recipes := map[string]config.AgentRecipe{
		"glm51": {Model: "glm-5.1", Auth: config.RecipeAuth{From: "file:" + filepath.Join(t.TempDir(), "nope"), To: "env:ANTHROPIC_AUTH_TOKEN"}},
	}
	err := docker.ReExec(docker.ReExecConfig{
		Image: "akopichin/afm:latest", ProjectDir: "/tmp/proj", Recipes: recipes,
	})
	if err == nil {
		t.Fatal("expected fail-fast on missing secret")
	}
	if !strings.Contains(err.Error(), "glm51") {
		t.Errorf("error should name the agent: %v", err)
	}
}

// TestUsedRecipes_FiltersUnusedAgents доказывает контракт: неиспользуемый агент
// с отсутствующим секретом не может заблокировать запуск — UsedRecipes исключает
// его из maps, которую ReExec резолвит. glm52 определён в конфиге, но флоу его не
// использует → его recipe (с секретом на несуществующий файл) отфильтровывается.
func TestUsedRecipes_FiltersUnusedAgents(t *testing.T) {
	// Секрет для glm51 — валидный файл; для glm52 — несуществующий файл.
	tokFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokFile, []byte("glm51-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}

	all := map[string]config.AgentRecipe{
		"glm51": {Model: "glm-5.1", Auth: config.RecipeAuth{From: "file:" + tokFile, To: "env:ANTHROPIC_AUTH_TOKEN"}},
		"glm52": {Model: "glm-5.2", Auth: config.RecipeAuth{From: "file:" + filepath.Join(t.TempDir(), "missing"), To: "env:ANTHROPIC_AUTH_TOKEN"}},
	}

	// Флоу использует ТОЛЬКО glm51; glm52 определён в конфиге, но не используется.
	f := &flow.Flow{
		Name:   "test",
		Stages: []flow.Stage{{ID: "s1", Command: "glm51"}},
	}

	got := docker.UsedRecipes(f, "claude", all)

	if len(got) != 1 {
		t.Fatalf("expected exactly 1 used recipe (glm51), got %d: %v", len(got), got)
	}
	recipe, ok := got["glm51"]
	if !ok {
		t.Fatal("glm51 (used agent) must be present in result")
	}
	if recipe.Model != "glm-5.1" {
		t.Errorf("glm51 model: got %q, want glm-5.1", recipe.Model)
	}
	if _, present := got["glm52"]; present {
		t.Error("glm52 (defined but unused agent) must be filtered out; " +
			"its missing secret must not be able to block the run")
	}
}

// TestUsedRecipes_GlobalCommand приводит globalCmd, который есть в recipes, но
// ни один stage его не использует — он всё равно должен попасть в результат.
func TestUsedRecipes_GlobalCommand(t *testing.T) {
	all := map[string]config.AgentRecipe{
		"glm51": {Model: "glm-5.1"},
	}
	// Флоу без stage-команд, globalCmd=glm51.
	f := &flow.Flow{Name: "test"}
	got := docker.UsedRecipes(f, "glm51", all)
	if len(got) != 1 || got["glm51"].Model != "glm-5.1" {
		t.Fatalf("globalCmd glm51 must be in result, got %v", got)
	}
}

// TestUsedRecipes_EmptyAndClaude покрывает тривиальные случаи: nil recipes и
// claude-команда (claude никогда не recipe).
func TestUsedRecipes_EmptyAndClaude(t *testing.T) {
	all := map[string]config.AgentRecipe{
		"glm51": {Model: "glm-5.1"},
	}
	// globalCmd=claude + stages с claude → пустой результат.
	f := &flow.Flow{
		Name:   "test",
		Stages: []flow.Stage{{ID: "s1", Command: "claude"}},
	}
	got := docker.UsedRecipes(f, "claude", all)
	if len(got) != 0 {
		t.Errorf("claude should not be a recipe, got %v", got)
	}
}

func TestUsesCodex_DirectCommand(t *testing.T) {
	f := &flow.Flow{Stages: []flow.Stage{{ID: "s1", Command: "codex-as-claude"}}}
	if !docker.UsesCodex(f, "", nil) {
		t.Error("expected true for direct codex-as-claude stage command")
	}
}

func TestUsesCodex_GlobalCommand(t *testing.T) {
	if !docker.UsesCodex(nil, "codex-as-claude", nil) {
		t.Error("expected true for global codex-as-claude client command")
	}
}

func TestUsesCodex_RecipeType(t *testing.T) {
	recipes := map[string]config.AgentRecipe{"codex": {Type: config.RecipeTypeCodex}}
	if !docker.UsesCodex(nil, "", recipes) {
		t.Error("expected true when a used recipe has type codex")
	}
}

func TestUsesCodex_False(t *testing.T) {
	f := &flow.Flow{Stages: []flow.Stage{{ID: "s1", Command: "glm51"}}}
	recipes := map[string]config.AgentRecipe{"glm51": {Type: ""}}
	if docker.UsesCodex(f, "claude", recipes) {
		t.Error("expected false when codex is not used")
	}
}

func TestReExec_CodexStateMount_WhenPresentAndFlagged(t *testing.T) {
	homeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, ".codex"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeDir)

	var capturedArgs []string
	docker.SetExecFunc(func(argv0 string, argv []string, envv []string) error {
		capturedArgs = argv
		return nil
	})
	defer docker.ResetExecFunc()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	err := docker.ReExec(docker.ReExecConfig{
		Image: "akopichin/afm:latest", ProjectDir: "/tmp/proj",
		ExtraArgs: []string{"run", "flow.yaml"}, MountCodexState: true,
	})
	if err != nil {
		t.Fatalf("ReExec: %v", err)
	}
	argsStr := strings.Join(capturedArgs, " ")
	want := homeDir + "/.codex:/tmp/host-codex:ro"
	if !strings.Contains(argsStr, want) {
		t.Errorf("missing codex state mount %q: %s", want, argsStr)
	}
}

func TestReExec_CodexStateMount_SkippedWhenFlagFalse(t *testing.T) {
	homeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, ".codex"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeDir)

	var capturedArgs []string
	docker.SetExecFunc(func(argv0 string, argv []string, envv []string) error {
		capturedArgs = argv
		return nil
	})
	defer docker.ResetExecFunc()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	err := docker.ReExec(docker.ReExecConfig{
		Image: "akopichin/afm:latest", ProjectDir: "/tmp/proj",
		ExtraArgs: []string{"run", "flow.yaml"}, MountCodexState: false,
	})
	if err != nil {
		t.Fatalf("ReExec: %v", err)
	}
	if strings.Contains(strings.Join(capturedArgs, " "), "host-codex") {
		t.Error("codex state must not be mounted when MountCodexState=false")
	}
}

func TestReExec_CodexStateMount_SkippedWhenDirMissing(t *testing.T) {
	homeDir := t.TempDir() // нет .codex внутри
	t.Setenv("HOME", homeDir)

	var capturedArgs []string
	docker.SetExecFunc(func(argv0 string, argv []string, envv []string) error {
		capturedArgs = argv
		return nil
	})
	defer docker.ResetExecFunc()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	err := docker.ReExec(docker.ReExecConfig{
		Image: "akopichin/afm:latest", ProjectDir: "/tmp/proj",
		ExtraArgs: []string{"run", "flow.yaml"}, MountCodexState: true,
	})
	if err != nil {
		t.Fatalf("ReExec: %v", err)
	}
	if strings.Contains(strings.Join(capturedArgs, " "), "host-codex") {
		t.Error("codex state must not be mounted when ~/.codex does not exist")
	}
}

// TestReExec_CodexRecipeNoAuth_DoesNotFail проверяет фикс бага, найденного в
// ревью Task 2: recipe типа codex может законно иметь пустой Auth (Validate()
// это разрешает — авторизация идёт через смонтированную ~/.codex, а не через
// секрет). Раньше ResolveAuthValue("", secrets) фейлил на пустом auth.from и
// валил ВЕСЬ ReExec — это ломало главный (безсекретный) сценарий codex.
func TestReExec_CodexRecipeNoAuth_DoesNotFail(t *testing.T) {
	var capturedArgs []string
	docker.SetExecFunc(func(argv0 string, argv []string, envv []string) error {
		capturedArgs = argv
		return nil
	})
	defer docker.ResetExecFunc()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	recipes := map[string]config.AgentRecipe{
		"codex": {Type: config.RecipeTypeCodex}, // Auth умышленно не задан
	}
	err := docker.ReExec(docker.ReExecConfig{
		Image: "akopichin/afm:latest", ProjectDir: "/tmp/proj",
		ExtraArgs: []string{"run", "flow.yaml"}, Recipes: recipes,
	})
	if err != nil {
		t.Fatalf("ReExec must not fail for a no-auth codex recipe: %v", err)
	}
	if strings.Contains(strings.Join(capturedArgs, " "), "AFM_SECRET_CODEX") {
		t.Error("no-auth codex recipe must not emit an AFM_SECRET_ env var")
	}
}
