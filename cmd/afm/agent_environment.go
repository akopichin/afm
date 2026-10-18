package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/secrets"
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

// minRedactedSecretLen is the shortest secret value redacted. A shorter value ("1", "on", "dev") cannot be a
// credential worth hiding, and replacing every occurrence of it would mangle ordinary text in every message.
const minRedactedSecretLen = 4

// loadRedactionSecrets returns every secret value this run can use, for redaction in mesh text, task context
// and logs: every value of the default global and project secrets.env (lifecycle hooks on the host read them)
// and of the configured secrets-file override (Docker and recipes read it), every value an `env:`/`file:`
// reference of a recipe or lifecycle hook resolves to from any of those layers or the environment, the resolved
// hook values, and the provider credentials in the environment. Best-effort: an unreadable file is reported,
// not fatal — the other sources are still redacted.
func loadRedactionSecrets(afmRoot string, cfg config.Config, hooks []lifecyclehooks.RegisteredHook) []string {
	sources := []string{""} // "": the default global and project files
	if cfg.Docker.SecretsFile != "" {
		sources = append(sources, cfg.Docker.SecretsFile)
	}
	layers := []map[string]string{nil} // nil: a reference resolved from the environment alone
	for _, override := range sources {
		loaded, err := docker.LoadSecretLayers(override, afmRoot)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: secrets.env not read for redaction: %v\n", err)
			continue
		}
		layers = append(layers, loaded)
	}

	seen := map[string]bool{}
	var values []string
	add := func(v string) {
		if len(v) >= minRedactedSecretLen && !seen[v] {
			seen[v] = true
			values = append(values, v)
		}
	}
	resolve := func(ref string) {
		for _, layer := range layers {
			if v, err := secrets.ResolveRef(ref, layer); err == nil {
				add(v)
			}
		}
	}
	for _, layer := range layers {
		for _, v := range layer {
			add(v)
		}
	}
	for _, recipe := range cfg.Docker.Agents {
		if recipe.Auth.From != "" {
			resolve(recipe.Auth.From)
		}
	}
	for _, h := range hooks {
		for _, ref := range h.Hook.Env {
			resolve(string(ref))
		}
		for _, v := range h.Hook.ResolvedEnv {
			add(v)
		}
	}
	for _, v := range sideAgentProtectedValues(cfg) {
		add(v)
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
