package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
)

// rebuildDockerReExec — Docker self-re-exec для `afm memory rebuild`,
// зеркалит блок cmd/afm/run.go:74-146 МИНУС всё специфичное для dashboard
// (порт/browser opener/file-browser манифест) — rebuild не поднимает HTTP-
// сервер и не открывает браузер. Вызывается ПОСЛЕ того, как хост уже
// резолвил absFlowPath/agentRoot/memDir/runDir (нужны для preflight
// reachability ниже), но ДО захвата run-лока и memory-лока (review #14:
// re-exec BEFORE locks) — если Docker перезапускает процесс, лок должен
// быть взят уже ВНУТРИ контейнера тем же процессом, что его реально
// держит.
//
// Возвращает nil сразу, если Docker выключен или мы уже внутри контейнера
// (AFM_IN_DOCKER=1) — самый частый путь. Иначе либо возвращает ошибку
// (auth/preflight не прошли), либо делегирует docker.ReExec, которая либо
// не возвращает управление вовсе (успешный re-exec), либо возвращает
// *docker.SubprocessExitError (код завершения контейнера) — пробрасываем
// её вызывающей стороне как есть, main.go уже умеет транслировать такую
// ошибку в правильный os.Exit (см. main.go).
func rebuildDockerReExec(cfg config.Config, o rebuildOptions, absFlowPath, agentRoot, memDir, runDir string) error {
	if !cfg.Docker.IsDockerEnabled() || os.Getenv("AFM_IN_DOCKER") == "1" {
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

	// rebuild не выполняет стадий флоу — единственный агент, который нужно
	// смонтировать/сгенерировать, это глобальный cfg.Client.Command. Те же
	// UsedRecipes/ScanCommands/UsesCodex, что и `afm run`, теперь принимают
	// nil-флоу (pkg/docker/launcher.go, Task 13) — командный список сводится
	// к [cfg.Client.Command] без отдельной "командный список из одного
	// элемента" обёртки.
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

// rebuildDockerArgs строит ExtraArgs для docker.ReExec из УЖЕ распарсенных
// rebuildOptions, а не форвардит os.Args[1:] как это делает run.go (review
// #14): позиционный аргумент флоу мог быть задан относительным путём,
// резолвящимся от ХОСТОВОГО cwd — под container-ным `-w absRoot` тот же
// относительный путь резолвился бы иначе (или не резолвился бы вовсе).
// Пересборка из типизированных полей исключает этот класс ошибки и заодно
// не требует хрупкого позиционного парсинга os.Args. absFlowPath передаётся
// ВСЕГДА явно (даже если пользователь не указал флоу и он был найден
// автосканированием flowsDir() на хосте) — детерминированно, без риска, что
// контейнер с другим --dir найдёт другой файл при повторном автосканировании.
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
	args = append(args, absFlowPath)
	// --dir должен быть абсолютным: относительный путь внутри контейнера
	// резолвился бы относительно -w (absRoot) и мог бы задвоить вложенность.
	args = append(args, "--dir="+absRoot)
	return args
}

// rebuildDockerPreflight проверяет, что каждый именованный путь физически
// виден внутри контейнера, который поднимет docker.ReExec: под корнем
// проекта (смонтирован по тому же абсолютному пути) или под одним из
// docker.extra_mounts (смонтированы read-only, но для ЧТЕНИЯ этого
// достаточно). Пустые пути пропускаются (например, cfg.PromptsDir == "" —
// используются вкомпиленные дефолты, файла на диске вовсе нет). Ошибка
// здесь — явная и ДО дорогой работы конвейера, а не невнятный agent-side
// "no such file or directory" где-то внутри контейнера.
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
		if !isUnderAnyDir(p, reachable) {
			return fmt.Errorf("docker: %s %q is not reachable inside the container; add its directory to docker.extra_mounts or move it under %s", label, p, projectDir)
		}
	}
	return nil
}

// rebuildDockerMemoryWritable — review #14: единственный РЕАЛЬНО writable
// маунт в Docker-режиме — сам корень проекта (projectDir); все
// docker.extra_mounts монтируются read-only (pkg/docker/launcher.go, ReExec,
// "-v ...:ro"). Внешний (вне projectDir) абсолютный memory.path, даже если
// он "виден" через extra_mounts, физически не может быть перезаписан
// агентом update внутри контейнера — падаем здесь с понятной ошибкой,
// вместо непонятного сбоя где-то в середине Finalize после дорогого
// capture.
func rebuildDockerMemoryWritable(projectDir, memDir string) error {
	if isUnderDir(memDir, projectDir) {
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
