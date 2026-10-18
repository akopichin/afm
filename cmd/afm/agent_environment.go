package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/flow"
)

// resolveAgentRoot резолвит root_dir от afmRoot; пустой root_dir сохраняет CWD процесса.
func resolveAgentRoot(afmRoot string, f *flow.Flow) (string, error) {
	agentRoot := f.RootDir
	if agentRoot != "" && !filepath.IsAbs(agentRoot) {
		agentRoot = filepath.Join(afmRoot, agentRoot)
	}
	return agentRoot, nil
}

// absoluteAgentRoot — как resolveAgentRoot, но ВСЕГДА возвращает абсолютный путь: пустой f.RootDir отдаёт
// абсолютный текущий CWD вместо "".
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

// lifecycleRootDir — абсолютный корень для метаданных lifecycle-хуков. Пустой agentRootDir означает
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

// loadHookSecretLayers собирает map секретов для env lifecycle-хуков: слои global ~/.afm/secrets.env и
// project <afmRoot>/.afm/secrets.env.
func loadHookSecretLayers(afmRoot string) (map[string]string, error) {
	return docker.LoadSecretLayers("", afmRoot)
}

// minRedactedSecretLen skips secrets.env values too short to be credentials ("1", "true"): redacting them
// would mangle ordinary text everywhere.
const minRedactedSecretLen = 8

// loadRedactionSecrets returns the secret values from the global and project secrets.env (or the configured
// override) for redaction in mesh text, task context and logs. Best-effort: an unreadable file is reported,
// not fatal — environment secrets are still redacted.
func loadRedactionSecrets(afmRoot, override string) []string {
	loaded, err := docker.LoadSecretLayers(override, afmRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: secrets.env not read for redaction: %v\n", err)
		return nil
	}
	var values []string
	for _, v := range loaded {
		if len(v) >= minRedactedSecretLen {
			values = append(values, v)
		}
	}
	return values
}

// resolveMemoryDir возвращает директорию памяти флоу: если память выключена — пустая строка, ничего дальше
// её не использует.
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
