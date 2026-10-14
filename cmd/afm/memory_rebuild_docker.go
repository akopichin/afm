package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
)

// rebuildDockerReExec перезапускает rebuild в Docker до захвата run/memory-локов; возвращает код выхода
// контейнера.
func rebuildDockerReExec(cfg config.Config, o rebuildOptions, absFlowPath, agentRoot, memDir, runDir string) error {
	// IsDockerEnabled() уже возвращает false внутри любого контейнера (config.InContainer, включая
	// AFM_IN_DOCKER=1) — отдельная проверка env здесь была бы избыточной.
	if !cfg.Docker.IsDockerEnabled() {
		return nil
	}

	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return fmt.Errorf("resolve project dir: %w", err)
	}
	if err := docker.CheckClaudeDockerAuth(cfg.Client.Command); err != nil {
		return err
	}
	if cfg.Docker.IsAutoShim() {
		if err := cfg.Docker.ValidateAgents(); err != nil {
			return err
		}
	}

	// rebuild не выполняет стадий флоу — единственный агент, который нужно смонтировать/сгенерировать, это
	// глобальный cfg.Client.Command.
	var recipes map[string]config.AgentRecipe
	var generatedForMount map[string]bool
	if cfg.Docker.IsAutoShim() {
		recipes = docker.UsedRecipes(nil, cfg.Client.Command, cfg.Docker.Agents)
		generatedForMount = make(map[string]bool, len(recipes))
		for cmd := range recipes {
			generatedForMount[cmd] = true
		}
	}
	cmds := docker.ScanCommands(nil, cfg.Client.Command, generatedForMount)
	mountCodexState := docker.UsesCodex(nil, cfg.Client.Command, recipes)

	if err := rebuildDockerPreflight(absRoot, cfg.Docker.ExtraMounts, map[string]string{
		"run dir":     runDir,
		"agent root":  agentRoot,
		"flow file":   absFlowPath,
		"prompts dir": cfg.PromptsDir,
	}); err != nil {
		return err
	}
	if memDir != "" {
		if err := rebuildDockerMemoryWritable(absRoot, memDir); err != nil {
			return err
		}
	}

	return docker.ReExec(docker.ReExecConfig{
		Image:           cfg.Docker.GetImage(),
		ProjectDir:      absRoot,
		Commands:        cmds,
		ExtraMounts:     cfg.Docker.ExtraMounts,
		ExtraArgs:       rebuildDockerArgs(o, absFlowPath, absRoot),
		ClientCommand:   cfg.Client.Command,
		Recipes:         recipes,
		SecretsFile:     cfg.Docker.SecretsFile,
		MountCodexState: mountCodexState,
		// DashboardPort/FileBrowserEnabled/FileRoots намеренно не заданы —
		// rebuild не поднимает dashboard, портов пробрасывать не нужно.
	})
}

// rebuildDockerArgs собирает аргументы из rebuildOptions с абсолютным путём флоу, независимым от CWD
// контейнера.
func rebuildDockerArgs(o rebuildOptions, absFlowPath, absRoot string) []string {
	args := []string{"memory", "rebuild"}
	if o.RunID != "" {
		args = append(args, "--run", o.RunID)
	}
	if o.DryRun {
		args = append(args, "--dry-run")
	}
	if o.ForceReflect {
		args = append(args, "--force-reflect")
	}
	if o.CommitSet {
		if o.Commit {
			args = append(args, "--commit")
		} else {
			args = append(args, "--no-commit")
		}
	}
	if o.Worktree != "" {
		args = append(args, "--worktree", o.Worktree)
	}
	args = append(args, absFlowPath)
	// --dir должен быть абсолютным: относительный путь внутри контейнера
	// резолвился бы относительно -w (absRoot) и мог бы задвоить вложенность.
	args = append(args, "--dir="+absRoot)
	return args
}

// rebuildDockerPreflight проверяет, что каждый именованный путь физически виден внутри контейнера, который
// поднимет docker.ReExec: под корнем проекта или под одним из docker.extra_mounts.
func rebuildDockerPreflight(projectDir string, mounts config.ExtraMounts, named map[string]string) error {
	reachable := append([]string{projectDir}, docker.ExtraMountContainerPaths(projectDir, mounts)...)
	labels := make([]string, 0, len(named))
	for label := range named {
		labels = append(labels, label)
	}
	slices.Sort(labels) // детерминированный порядок сообщения об ошибке
	for _, label := range labels {
		p := named[label]
		if p == "" {
			continue
		}
		// named-пути могут быть ОТНОСИТЕЛЬНЫМИ на обычном вызове, тогда как reachable — всегда абсолютные.
		absP, err := filepath.Abs(p)
		if err != nil {
			return fmt.Errorf("docker: resolve %s %q: %w", label, p, err)
		}
		if !isUnderAnyDir(absP, reachable) {
			return fmt.Errorf("docker: %s %q is not reachable inside the container; add its directory to docker.extra_mounts or move it under %s", label, p, projectDir)
		}
	}
	return nil
}

// rebuildDockerMemoryWritable разрешает запись только под корнем проекта; extra_mounts монтируются
// read-only.
func rebuildDockerMemoryWritable(projectDir, memDir string) error {
	// Defensive: normalize memDir to absolute too — in production memDir is already absolute, but a relative
	// memDir must not silently misclassify as "outside".
	absMemDir, err := filepath.Abs(memDir)
	if err != nil {
		return fmt.Errorf("docker: resolve memory dir %q: %w", memDir, err)
	}
	if isUnderDir(absMemDir, projectDir) {
		return nil
	}
	return fmt.Errorf(
		"docker: memory.path resolves to %q, outside the project directory %q; "+
			"docker.extra_mounts are read-only, so this location cannot be written to from inside the container — "+
			"move memory.path under the project directory, or run `afm memory rebuild` for this flow outside Docker",
		memDir, projectDir)
}

// isUnderDir сообщает, лежит ли path внутри root (включая сам root).
func isUnderDir(path, root string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// isUnderAnyDir сообщает, лежит ли path внутри хотя бы одного из roots.
func isUnderAnyDir(path string, roots []string) bool {
	for _, r := range roots {
		if isUnderDir(path, r) {
			return true
		}
	}
	return false
}
