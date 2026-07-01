package docker_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/flow"
)

func TestScanCommands_SkipsClaude(t *testing.T) {
	f := &flow.Flow{
		Name: "test",
		Stages: []flow.Stage{
			{ID: "s1", Command: "claude"},
			{ID: "s2", Command: ""},
		},
	}
	mounts := docker.ScanCommands(f, "claude")
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
	mounts := docker.ScanCommands(f, "claude")
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
	mounts := docker.ScanCommands(f, "claude")
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
	mounts := docker.ScanCommands(f, "glm51")
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
	mounts := docker.ScanCommands(f, "claude")
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
		":/root/.claude", // хост-путь зависит от ОС, проверяем только контейнерную часть
		":/root/.afm",
		"-w /home/user/myproject",
		"-v /usr/local/bin/glm51:/usr/local/bin/glm51:ro",
		home + "/.ai-free:/root/.ai-free:ro", // extra_mount: хост home/.ai-free → контейнер /root/.ai-free ($HOME)
		"-e AFM_IN_DOCKER=1",
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
