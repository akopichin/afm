package docker_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
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

func TestScanCommands_NilFlow(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "glm51")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	mounts := docker.ScanCommands(nil, "glm51", nil)
	if len(mounts) != 1 {
		t.Fatalf("expected 1 mount for global cmd with nil flow, got %d: %v", len(mounts), mounts)
	}
	if mounts[0].ContainerName != "glm51" {
		t.Errorf("ContainerName: got %q, want %q", mounts[0].ContainerName, "glm51")
	}

	// claude как globalCmd с nil flow — по-прежнему не монтируется.
	if got := docker.ScanCommands(nil, "claude", nil); len(got) != 0 {
		t.Errorf("expected 0 mounts for claude global cmd with nil flow, got %d: %v", len(got), got)
	}
}

func TestScanCommands_VerifyOnlyCommandIsMounted(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "codex")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	f := &flow.Flow{
		Name: "test",
		Stages: []flow.Stage{
			{ID: "s1", Command: "claude", Verify: flow.VerifySpec{
				Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}},
			}},
		},
	}
	mounts := docker.ScanCommands(f, "claude", nil)
	if len(mounts) != 1 || mounts[0].ContainerName != "codex" {
		t.Fatalf("expected verify-only command codex to be mounted, got %v", mounts)
	}
}

// TestScanCommands_ShellVerifyNoExtraCommand — shell-шаг verify не ссылается
// на отдельного агента, поэтому не должен порождать дополнительный mount.
func TestScanCommands_ShellVerifyNoExtraCommand(t *testing.T) {
	f := &flow.Flow{
		Name: "test",
		Stages: []flow.Stage{
			{ID: "s1", Command: "claude", Verify: flow.NewShellVerify("echo ok")},
		},
	}
	mounts := docker.ScanCommands(f, "claude", nil)
	if len(mounts) != 0 {
		t.Errorf("shell-only verify must not add mounts, got %v", mounts)
	}
}

// TestScanCommands_VerifyStepsDedupeWithinStage — два verify-шага с одной и
// той же командой агента не должны давать два одинаковых mount.
func TestScanCommands_VerifyStepsDedupeWithinStage(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "codex")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	f := &flow.Flow{
		Name: "test",
		Stages: []flow.Stage{
			{ID: "s1", Command: "claude", Verify: flow.VerifySpec{
				Steps: []flow.VerifyStep{
					{Kind: flow.VerifyAgent, Command: "codex"},
					{Kind: flow.VerifyAgent, Command: "codex"},
				},
			}},
		},
	}
	mounts := docker.ScanCommands(f, "claude", nil)
	if len(mounts) != 1 {
		t.Errorf("expected 1 deduplicated mount, got %d: %v", len(mounts), mounts)
	}
}

func TestUsedRecipeCommands_VerifyOnlyCommand(t *testing.T) {
	recipes := map[string]config.AgentRecipe{
		"codex": {Type: config.RecipeTypeCodex},
	}
	f := &flow.Flow{
		Name: "test",
		Stages: []flow.Stage{
			{ID: "s1", Command: "claude", Verify: flow.VerifySpec{
				Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}},
			}},
		},
	}
	used := docker.UsedRecipeCommands(f, "claude", recipes)
	if !used["codex"] {
		t.Errorf("expected verify-only recipe command codex to be discovered, got %v", used)
	}
}

// TestUsesCodex_VerifyOnlyDirectCommand — verify-шаг напрямую вызывает
// codex-as-claude (без recipe) — тоже должно включать ~/.codex mount.
func TestUsesCodex_VerifyOnlyDirectCommand(t *testing.T) {
	f := &flow.Flow{
		Name: "test",
		Stages: []flow.Stage{
			{ID: "s1", Command: "claude", Verify: flow.VerifySpec{
				Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex-as-claude"}},
			}},
		},
	}
	if !docker.UsesCodex(f, "claude", nil) {
		t.Error("expected true for verify-only codex-as-claude command")
	}
}

func TestUsesCodex_VerifyOnlyRecipeType(t *testing.T) {
	all := map[string]config.AgentRecipe{
		"codex": {Type: config.RecipeTypeCodex},
	}
	f := &flow.Flow{
		Name: "test",
		Stages: []flow.Stage{
			{ID: "s1", Command: "claude", Verify: flow.VerifySpec{
				Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}},
			}},
		},
	}
	usedRecipes := docker.UsedRecipes(f, "claude", all)
	if !docker.UsesCodex(f, "claude", usedRecipes) {
		t.Error("expected true when verify-only recipe has type codex")
	}
}

// TestUsedRecipes_NilFlow — nil f + [globalCmd] должен спроецировать ТОЛЬКО
// рецепт глобальной команды, без паники на f.Stages.
func TestUsedRecipes_NilFlow(t *testing.T) {
	all := map[string]config.AgentRecipe{
		"glm51": {Model: "glm-5.1"},
		"glm52": {Model: "glm-5.2"},
	}
	got := docker.UsedRecipes(nil, "glm51", all)
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 recipe for nil flow, got %d: %v", len(got), got)
	}
	if _, ok := got["glm51"]; !ok {
		t.Errorf("expected glm51 recipe present, got %v", got)
	}
	if _, ok := got["glm52"]; ok {
		t.Errorf("glm52 must not be included (not the global command), got %v", got)
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
		ExtraMounts:   config.ExtraMounts{{Path: "~/.ai-free"}},
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
		"docker run --rm --init", // tini as PID 1 reaps orphaned agent descendants
		"-p 127.0.0.1:9876:9876", // dashboard всегда публикуется только на loopback
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

func TestReExec_ForwardsAccounting(t *testing.T) {
	run := func(t *testing.T, envVal string, setEnv bool) string {
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
		if setEnv {
			t.Setenv("AFM_ACCOUNTING", envVal)
		} else {
			_ = os.Unsetenv("AFM_ACCOUNTING")
		}

		if err := docker.ReExec(docker.ReExecConfig{
			Image:      "akopichin/afm:latest",
			ProjectDir: "/tmp/proj",
			ExtraArgs:  []string{"run", "flow.yaml"},
		}); err != nil {
			t.Fatal(err)
		}
		return strings.Join(capturedArgs, " ")
	}

	for _, v := range []string{"0", "1"} {
		t.Run("forwards "+v, func(t *testing.T) {
			if got := run(t, v, true); !strings.Contains(got, "-e AFM_ACCOUNTING="+v) {
				t.Errorf("AFM_ACCOUNTING=%s not forwarded by value: %s", v, got)
			}
		})
	}
	t.Run("unset forwards nothing", func(t *testing.T) {
		if got := run(t, "", false); strings.Contains(got, "AFM_ACCOUNTING") {
			t.Errorf("AFM_ACCOUNTING must not appear when unset: %s", got)
		}
	})
}

func TestReExec_ForwardsSideAgentEnv(t *testing.T) {
	run := func(t *testing.T, envVal string, setEnv bool) string {
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
		if setEnv {
			t.Setenv("AFM_SIDEAGENT", envVal)
		} else {
			_ = os.Unsetenv("AFM_SIDEAGENT")
		}

		if err := docker.ReExec(docker.ReExecConfig{
			Image:      "akopichin/afm:latest",
			ProjectDir: "/tmp/proj",
			ExtraArgs:  []string{"run", "flow.yaml"},
		}); err != nil {
			t.Fatal(err)
		}
		return strings.Join(capturedArgs, " ")
	}

	for _, v := range []string{"0", "1"} {
		t.Run("forwards "+v, func(t *testing.T) {
			if got := run(t, v, true); !strings.Contains(got, "-e AFM_SIDEAGENT="+v) {
				t.Errorf("AFM_SIDEAGENT=%s not forwarded by value: %s", v, got)
			}
		})
	}
	t.Run("unset forwards nothing", func(t *testing.T) {
		if got := run(t, "", false); strings.Contains(got, "AFM_SIDEAGENT") {
			t.Errorf("AFM_SIDEAGENT must not appear when unset: %s", got)
		}
	})
}

// TestDockerReExec_ForwardsExplicitCollaborationEnv — T1.4: an explicitly-set AFM_COLLABORATION value is
// forwarded by value (not bare -e: this is a switch, not a secret). An UNSET host env must not be invented
// inside the container — otherwise host and container would silently diverge in how they interpret the
// config/flow collaboration switch.
func TestDockerReExec_ForwardsExplicitCollaborationEnv(t *testing.T) {
	run := func(t *testing.T, envVal string, setEnv bool) string {
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
		if setEnv {
			t.Setenv("AFM_COLLABORATION", envVal)
		} else {
			_ = os.Unsetenv("AFM_COLLABORATION")
		}

		if err := docker.ReExec(docker.ReExecConfig{
			Image:      "akopichin/afm:latest",
			ProjectDir: "/tmp/proj",
			ExtraArgs:  []string{"run", "flow.yaml"},
		}); err != nil {
			t.Fatal(err)
		}
		return strings.Join(capturedArgs, " ")
	}

	for _, v := range []string{"0", "1"} {
		t.Run("forwards "+v, func(t *testing.T) {
			if got := run(t, v, true); !strings.Contains(got, "-e AFM_COLLABORATION="+v) {
				t.Errorf("AFM_COLLABORATION=%s not forwarded by value: %s", v, got)
			}
		})
	}
	t.Run("unset forwards nothing", func(t *testing.T) {
		if got := run(t, "", false); strings.Contains(got, "AFM_COLLABORATION") {
			t.Errorf("AFM_COLLABORATION must not appear when unset (must not be invented): %s", got)
		}
	})
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

func hasEnvKey(args []string, key string) bool {
	for i, a := range args {
		if a != "-e" || i+1 >= len(args) {
			continue
		}
		v := args[i+1]
		if v == key || strings.HasPrefix(v, key+"=") {
			return true
		}
	}
	return false
}

func TestReExec_FileBrowserWiring(t *testing.T) {
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

	cfg := docker.ReExecConfig{
		Image: "img", ProjectDir: "/work/afm", DashboardPort: 8080,
		ExtraArgs:          []string{"run", "flow.yaml"},
		FileBrowserEnabled: true,
		FileRoots: docker.FileRootManifest{Version: 1, Roots: []docker.FileRootManifestEntry{
			{ID: "project", ContainerPath: "/work/afm", Kind: "project"},
		}},
	}
	if err := docker.ReExec(cfg); err != nil {
		t.Fatalf("ReExec: %v", err)
	}
	joined := strings.Join(capturedArgs, " ")
	if !strings.Contains(joined, "-p 127.0.0.1:8080:8080") {
		t.Errorf("expected loopback publish, got: %s", joined)
	}
	if !hasEnvKey(capturedArgs, docker.FileRootsEnvVar) {
		t.Errorf("expected -e %s, got: %s", docker.FileRootsEnvVar, joined)
	}
}

func TestReExec_DashboardAlwaysLoopback(t *testing.T) {
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

	cfg := docker.ReExecConfig{
		Image: "img", ProjectDir: "/work/afm", DashboardPort: 8080,
		ExtraArgs:          []string{"run", "flow.yaml"},
		FileBrowserEnabled: false,
	}
	if err := docker.ReExec(cfg); err != nil {
		t.Fatalf("ReExec: %v", err)
	}
	joined := strings.Join(capturedArgs, " ")
	if !strings.Contains(joined, "-p 127.0.0.1:8080:8080") {
		t.Errorf("expected loopback publish, got: %s", joined)
	}
	if hasEnvKey(capturedArgs, docker.FileRootsEnvVar) {
		t.Errorf("did not expect file-roots env, got: %s", joined)
	}
}

func TestReExec_ExtraMountPathResolution(t *testing.T) {
	homeDir := t.TempDir()
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

	mounts := config.ExtraMounts{
		{Path: "../shared", Browse: true},   // относительный — резолвится от ProjectDir
		{Path: "/abs/tokens", Browse: true}, // абсолютный — не меняется
		{Path: "~/.ai-free", Browse: true},  // ~ — expandHome
	}
	err := docker.ReExec(docker.ReExecConfig{
		Image:       "img",
		ProjectDir:  "/work/afm",
		ExtraMounts: mounts,
		ExtraArgs:   []string{"run", "flow.yaml"},
	})
	if err != nil {
		t.Fatalf("ReExec: %v", err)
	}
	argsStr := strings.Join(capturedArgs, " ")

	checks := []string{
		"-v /work/shared:/work/shared:ro",                   // относительный: без расхождения хост/контейнер
		"-v /abs/tokens:/abs/tokens:ro",                     // абсолютный: не изменился
		"-v " + homeDir + "/.ai-free:/home/afm/.ai-free:ro", // ~: хост-home vs контейнер-home
	}
	for _, c := range checks {
		if !strings.Contains(argsStr, c) {
			t.Errorf("missing %q in args: %s", c, argsStr)
		}
	}
	if strings.Contains(argsStr, "../shared") {
		t.Errorf("relative path must not leak raw into docker args: %s", argsStr)
	}

	// Манифест (используемый file browser'ом) должен резолвить тот же относительный путь в тот же абсолютный
	// /work/shared — без расхождения с тем, что реально смонтировано через -v.
	manifest, err := docker.BuildFileRootManifest("/work/afm", mounts)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"extra-1": "/work/shared",
		"extra-2": "/abs/tokens",
		"extra-3": "/home/afm/.ai-free",
	}
	found := 0
	for _, r := range manifest.Roots {
		if r.Kind != "extra" {
			continue
		}
		found++
		if want[r.ID] != r.ContainerPath {
			t.Errorf("manifest %s container path: got %q, want %q", r.ID, r.ContainerPath, want[r.ID])
		}
	}
	if found != 3 {
		t.Fatalf("expected 3 extra roots in manifest, got %d", found)
	}
}

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

func TestReExec_HookSecretTransport(t *testing.T) {
	tokFile := filepath.Join(t.TempDir(), "hook-token")
	if err := os.WriteFile(tokFile, []byte("hook-secret-value\n"), 0600); err != nil {
		t.Fatal(err)
	}

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
	t.Setenv("TG_TOKEN", "env-secret-value")

	// "tg-notify" / "TG_NOTIFY" — под sanitize(hookID) (envName: [A-Z0-9_],
	// остальное → '_', + ToUpper) оба схлопнулись бы в "TG_NOTIFY".
	hooks := []lifecyclehooks.RegisteredHook{
		{Hook: lifecyclehooks.Hook{ID: "tg-notify", Command: "true", Env: map[string]lifecyclehooks.SecretRef{ //nolint:gosec // test fixture, not a real secret
			"TOKEN": "env:TG_TOKEN",
		}}},
		{Hook: lifecyclehooks.Hook{ID: "TG_NOTIFY", Command: "true", Env: map[string]lifecyclehooks.SecretRef{
			"TOKEN": lifecyclehooks.SecretRef("file:" + tokFile),
		}}},
	}

	err := docker.ReExec(docker.ReExecConfig{
		Image:      "akopichin/afm:latest",
		ProjectDir: "/tmp/proj",
		ExtraArgs:  []string{"run", "flow.yaml"},
		Hooks:      hooks,
	})
	if err != nil {
		t.Fatalf("ReExec: %v", err)
	}
	defer func() {
		_ = os.Unsetenv(lifecyclehooks.TransportName(0, 0))
		_ = os.Unsetenv(lifecyclehooks.TransportName(1, 0))
	}()

	name0 := lifecyclehooks.TransportName(0, 0)
	name1 := lifecyclehooks.TransportName(1, 0)
	if name0 == name1 {
		t.Fatalf("transport names must differ for distinct hooks: %q == %q", name0, name1)
	}

	argsStr := strings.Join(capturedArgs, " ")
	if !strings.Contains(argsStr, "-e "+name0) {
		t.Errorf("missing transient -e %s (bare): %s", name0, argsStr)
	}
	if !strings.Contains(argsStr, "-e "+name1) {
		t.Errorf("missing transient -e %s (bare): %s", name1, argsStr)
	}
	// значения НЕ должны попасть в argv (bare -e без "=value")
	if strings.Contains(argsStr, "env-secret-value") || strings.Contains(argsStr, "hook-secret-value") {
		t.Errorf("secret value leaked into argv: %s", argsStr)
	}
	// секрет-файл не монтируется
	if strings.Contains(argsStr, "-v "+tokFile) {
		t.Errorf("secret file must not be mounted: %s", argsStr)
	}
	// значения — в env хоста (то же env, что уйдёт docker'у как envv)
	if v := os.Getenv(name0); v != "env-secret-value" {
		t.Errorf("os.Getenv(%s) = %q, want env-secret-value", name0, v)
	}
	if v := os.Getenv(name1); v != "hook-secret-value" {
		t.Errorf("os.Getenv(%s) = %q, want hook-secret-value", name1, v)
	}
}

// TestReExec_HookNoEnv_NoTransport доказывает гейт UsesLifecycleSecrets: хук
// без Env не должен породить ни одной AFM_HOOK_SECRET_* переменной.
func TestReExec_HookNoEnv_NoTransport(t *testing.T) {
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

	hooks := []lifecyclehooks.RegisteredHook{
		{Hook: lifecyclehooks.Hook{ID: "plain", Command: "true"}}, // без Env
	}
	err := docker.ReExec(docker.ReExecConfig{
		Image: "akopichin/afm:latest", ProjectDir: "/tmp/proj",
		ExtraArgs: []string{"run", "flow.yaml"}, Hooks: hooks,
	})
	if err != nil {
		t.Fatalf("ReExec: %v", err)
	}
	if strings.Contains(strings.Join(capturedArgs, " "), lifecyclehooks.HookSecretTransportPrefix) {
		t.Error("a hook without Env must not emit any AFM_HOOK_SECRET_ transport var")
	}
}

func TestReExec_HookSecretMissingFailFast(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	hooks := []lifecyclehooks.RegisteredHook{
		{Hook: lifecyclehooks.Hook{ID: "tg", Command: "true", Env: map[string]lifecyclehooks.SecretRef{ //nolint:gosec // test fixture, not a real secret
			"TOKEN": "env:NOPE_MISSING_HOOK_SECRET",
		}}},
	}
	err := docker.ReExec(docker.ReExecConfig{
		Image: "akopichin/afm:latest", ProjectDir: "/tmp/proj", Hooks: hooks,
	})
	if err == nil {
		t.Fatal("expected fail-fast on missing hook secret")
	}
	if !strings.Contains(err.Error(), "tg") {
		t.Errorf("error should name the hook: %v", err)
	}
}

func TestUsesLifecycleSecrets(t *testing.T) {
	if docker.UsesLifecycleSecrets(nil) {
		t.Error("nil hooks → false")
	}
	noEnv := []lifecyclehooks.RegisteredHook{{Hook: lifecyclehooks.Hook{ID: "a"}}}
	if docker.UsesLifecycleSecrets(noEnv) {
		t.Error("hooks without Env → false")
	}
	withEnv := []lifecyclehooks.RegisteredHook{
		{Hook: lifecyclehooks.Hook{ID: "a"}},
		{Hook: lifecyclehooks.Hook{ID: "b", Env: map[string]lifecyclehooks.SecretRef{"X": "env:X"}}},
	}
	if !docker.UsesLifecycleSecrets(withEnv) {
		t.Error("a hook with Env among others → true")
	}
}

func TestReExec_BrowserTokenNotInArgv(t *testing.T) {
	var gotArgv []string
	docker.SetExecFunc(func(argv0 string, argv []string, envv []string) error {
		gotArgv = argv
		return nil
	})
	defer docker.ResetExecFunc()

	// fake docker on PATH
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	err := docker.ReExec(docker.ReExecConfig{
		Image: "img", ProjectDir: "/p",
		BrowserEnabled: true, BrowserToken: "tok-SECRET",
		BrowserEndpoint: "host.docker.internal:5001", BrowserEpoch: "ep1",
		BrowserAppHostPort: 54321, BrowserAppContainerPort: 18080,
		BrowserAppURL: "http://127.0.0.1:54321",
	})
	if err != nil {
		t.Fatalf("ReExec: %v", err)
	}
	defer func() { _ = os.Unsetenv("AFM_BROWSER_TRANSPORT_TOKEN") }()

	argsStr := strings.Join(gotArgv, " ")
	for _, a := range gotArgv {
		if strings.Contains(a, "tok-SECRET") {
			t.Fatalf("token leaked into argv: %q", a)
		}
	}
	// bare -e (no =value) for the token — value inherited from the process env.
	if !containsBareEnv(gotArgv, "AFM_BROWSER_TRANSPORT_TOKEN") {
		t.Fatalf("expected bare -e AFM_BROWSER_TRANSPORT_TOKEN: %s", argsStr)
	}
	if !strings.Contains(argsStr, "AFM_BROWSER_APP_URL=http://127.0.0.1:54321") {
		t.Fatalf("expected app URL baked into env before start: %s", argsStr)
	}
	if !strings.Contains(argsStr, "AFM_BROWSER_ENDPOINT=host.docker.internal:5001") {
		t.Fatalf("expected endpoint env: %s", argsStr)
	}
	if !strings.Contains(argsStr, "AFM_BROWSER_EPOCH=ep1") {
		t.Fatalf("expected epoch env: %s", argsStr)
	}
	if !strings.Contains(argsStr, "-p 127.0.0.1:54321:18080") {
		t.Fatalf("expected fixed loopback app publish 127.0.0.1:54321:18080: %s", argsStr)
	}
}

// containsBareEnv reports whether argv has `-e NAME` with NAME as its own token
// (no "=value"), i.e. the value is inherited from the process env.
func containsBareEnv(argv []string, name string) bool {
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "-e" && argv[i+1] == name {
			return true
		}
	}
	return false
}

func TestReExec_ForwardsWorktreeEnv(t *testing.T) {
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
		Image:       "img",
		ProjectDir:  "/p",
		ExtraArgs:   []string{"run", "flow.yaml"},
		WorktreeDir: "/p/.worktrees/f",
	})
	if err != nil {
		t.Fatalf("ReExec: %v", err)
	}
	if !hasEnvKey(capturedArgs, docker.WorktreeEnvVar) {
		t.Fatalf("expected -e %s, got: %s", docker.WorktreeEnvVar, strings.Join(capturedArgs, " "))
	}
	if !strings.Contains(strings.Join(capturedArgs, " "), docker.WorktreeEnvVar+"=/p/.worktrees/f") {
		t.Fatalf("expected %s=/p/.worktrees/f, got: %s", docker.WorktreeEnvVar, strings.Join(capturedArgs, " "))
	}
}

func TestReExec_NoWorktreeEnvWhenEmpty(t *testing.T) {
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
		Image:      "img",
		ProjectDir: "/p",
		ExtraArgs:  []string{"run", "flow.yaml"},
	})
	if err != nil {
		t.Fatalf("ReExec: %v", err)
	}
	if strings.Contains(strings.Join(capturedArgs, " "), docker.WorktreeEnvVar) {
		t.Fatalf("%s must not appear when WorktreeDir is empty, got: %s", docker.WorktreeEnvVar, strings.Join(capturedArgs, " "))
	}
}

// TestReExec_WorktreeNeedsNoExtraMount pairs with TestReExec_ForwardsWorktreeEnv and is sound only together
// with resolveWorktree's host-side containment guard (cmd/afm/run_worktree.go), which refuses any
// worktree path outside ProjectDir before this code ever runs. Given that guarantee, a worktree under
// ProjectDir needs no bind-mount of its own: ProjectDir is already mounted at the identical container path,
// so the container sees the worktree for free. This test only proves "no new -v flag"; it does not and
// cannot prove containment itself — that's resolveWorktree's job.
func TestReExec_WorktreeNeedsNoExtraMount(t *testing.T) {
	countFlag := func(argv []string, flag string) int {
		n := 0
		for _, a := range argv {
			if a == flag {
				n++
			}
		}
		return n
	}

	run := func(t *testing.T, worktreeDir string) []string {
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
			Image:       "img",
			ProjectDir:  "/p",
			ExtraArgs:   []string{"run", "flow.yaml"},
			WorktreeDir: worktreeDir,
		})
		if err != nil {
			t.Fatalf("ReExec: %v", err)
		}
		return capturedArgs
	}

	without := run(t, "")
	with := run(t, "/p/.worktrees/f")

	mountsWithout := countFlag(without, "-v")
	mountsWith := countFlag(with, "-v")
	if mountsWith != mountsWithout {
		t.Fatalf("expected no extra -v mount for an in-ProjectDir worktree: without=%d with=%d\nwithout=%s\nwith=%s",
			mountsWithout, mountsWith, strings.Join(without, " "), strings.Join(with, " "))
	}

	// The only difference between the two argv's should be the worktree -e pair.
	if hasEnvKey(without, docker.WorktreeEnvVar) {
		t.Fatalf("baseline run must not carry %s: %s", docker.WorktreeEnvVar, strings.Join(without, " "))
	}
	if !hasEnvKey(with, docker.WorktreeEnvVar) {
		t.Fatalf("worktree run must carry %s: %s", docker.WorktreeEnvVar, strings.Join(with, " "))
	}
}

// A helper-profile command that is a recipe (reviewer → codex) is "used" too: it needs its in-image wrapper
// and its secrets, otherwise the host's binary (a macOS codex) gets bind-mounted into the Linux container and
// fails with "exec format error". A non-recipe extra command stays out (it is mounted, not generated).
func TestUsedRecipeCommands_IncludesHelperProfileCommands(t *testing.T) {
	recipes := map[string]config.AgentRecipe{
		"codex":     {Type: config.RecipeTypeCodex},
		"architect": {Model: "opus"},
	}
	f := &flow.Flow{Name: "test", Stages: []flow.Stage{{ID: "s1", Command: "claude"}}}
	used := docker.UsedRecipeCommands(f, "claude", recipes, "codex", "architect", "some-host-tool")
	if !used["codex"] || !used["architect"] || used["some-host-tool"] {
		t.Fatalf("used = %v, want codex and architect (profile recipes), not some-host-tool", used)
	}
	got := docker.UsedRecipes(f, "claude", recipes, "codex")
	if _, ok := got["codex"]; !ok {
		t.Fatalf("UsedRecipes = %v, want the helper's codex recipe", got)
	}
	mounts := docker.ScanCommandsWithExtra(f, "claude", used, []string{"codex"})
	for _, m := range mounts {
		if m.ContainerName == "codex" {
			t.Fatalf("codex must not be mounted from the host when its recipe wrapper is generated: %+v", m)
		}
	}
}
