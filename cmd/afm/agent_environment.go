package main

import (
	"os"
	"path/filepath"

	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/flow"
)

// resolveAgentRoot возвращает корень проекта для агентов (их CWD) — поведение
// в точности повторяет прежнюю инлайн-логику `afm run`: относительный
// f.RootDir резолвится относительно afmRoot (--dir); пустой f.RootDir даёт
// пустую строку — агенты наследуют CWD процесса afm (прежнее поведение,
// не трогать без крайней необходимости, иначе `afm run` регрессирует).
func resolveAgentRoot(afmRoot string, f *flow.Flow) (string, error) {
	agentRoot := f.RootDir
	if agentRoot != "" && !filepath.IsAbs(agentRoot) {
		agentRoot = filepath.Join(afmRoot, agentRoot)
	}
	return agentRoot, nil
}

// absoluteAgentRoot — как resolveAgentRoot, но ВСЕГДА возвращает абсолютный
// путь: пустой f.RootDir отдаёт абсолютный текущий CWD вместо "" (прежнее
// поведение resolveAgentRoot из `afm run`, где пустая строка означает
// "агенты наследуют CWD процесса" — это устраивает Executor.Dir, но не
// подходит rebuild'у, которому нужен стабильный абсолютный путь для
// манифеста, лока и Executor.Dir агента "с нуля"). Используется ТОЛЬКО
// `afm memory rebuild` — `afm run` должен продолжать звать resolveAgentRoot.
func absoluteAgentRoot(afmRoot string, f *flow.Flow) (string, error) {
	if f.RootDir == "" {
		return os.Getwd()
	}
	agentRoot := f.RootDir
	if !filepath.IsAbs(agentRoot) {
		agentRoot = filepath.Join(afmRoot, agentRoot)
	}
	return filepath.Abs(agentRoot)
}

// lifecycleRootDir — абсолютный корень для метаданных lifecycle-хуков
// (AFM_ROOT_DIR, CWD hook-команды). Пустой agentRootDir означает
// «наследовать CWD» — для контракта хуков это фактический CWD процесса.
func lifecycleRootDir(agentRootDir string) string {
	if agentRootDir != "" {
		return agentRootDir
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

// loadHookSecretLayers собирает map секретов для env lifecycle-хуков: слои
// global ~/.afm/secrets.env и project <afmRoot>/.afm/secrets.env (project
// приоритетнее — та же семантика, что docker.LoadSecretLayers уже реализует
// для agent-recipe секретов; повторно используем её здесь, а не дублируем
// список путей). Дальше приоритет уходит к env процесса — это уже делает
// secrets.ResolveRef.
func loadHookSecretLayers(afmRoot string) (map[string]string, error) {
	return docker.LoadSecretLayers("", afmRoot)
}

// resolveMemoryDir возвращает директорию памяти флоу: если память выключена
// (!f.MemoryEnabled()) — пустая строка, ничего дальше её не использует.
// Иначе f.Memory.Path резолвится относительно agentRoot (корень проекта
// агентов), а если agentRoot пуст (агенты наследуют CWD процесса) — относительно
// afmRoot (--dir); уже абсолютный f.Memory.Path остаётся как есть.
func resolveMemoryDir(afmRoot, agentRoot string, f *flow.Flow) (string, error) {
	if !f.MemoryEnabled() {
		return "", nil
	}
	base := agentRoot
	if base == "" {
		base = afmRoot
	}
	memDir := f.Memory.Path
	if !filepath.IsAbs(memDir) {
		memDir = filepath.Join(base, memDir)
	}
	return memDir, nil
}
