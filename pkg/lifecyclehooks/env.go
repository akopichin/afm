package lifecyclehooks

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/secrets"
)

// ResolveHookEnv резолвит env-ссылки хука в конкретные значения через pkg/secrets. Пустой env → nil.
func ResolveHookEnv(h Hook, loaded map[string]string) (map[string]string, error) {
	if len(h.Env) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(h.Env))
	for varName, ref := range h.Env {
		val, err := secrets.ResolveRef(string(ref), loaded)
		if err != nil {
			return nil, fmt.Errorf("hook %q env %q: %w", h.ID, varName, err)
		}
		out[varName] = val
	}
	return out, nil
}

// HookSecretTransportPrefix — префикс transient env-переменных, которыми хост докер-режима передаёт УЖЕ
// РЕЗОЛВНУТЫЕ секреты хуков в контейнер.
const HookSecretTransportPrefix = "AFM_HOOK_SECRET_" //nolint:gosec // env-var name prefix, not a credential

// TransportName использует индекс combined-хука и индекс отсортированного Env-ключа; хост и контейнер
// должны делить один combined.
func TransportName(hookIdx, varIdx int) string {
	return fmt.Sprintf("%s%d_%d", HookSecretTransportPrefix, hookIdx, varIdx)
}

// SortedEnvKeys возвращает имена переменных h.Env в детерминированном порядке — источник varIdx для
// TransportName.
func SortedEnvKeys(h Hook) []string {
	keys := make([]string, 0, len(h.Env))
	for k := range h.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// ResolveHookEnvFromTransport читает секреты, переданные хостом; пустая или отсутствующая переменная
// завершает резолв ошибкой.
func ResolveHookEnvFromTransport(hookIdx int, h Hook) (map[string]string, error) {
	if len(h.Env) == 0 {
		return nil, nil
	}
	keys := SortedEnvKeys(h)
	out := make(map[string]string, len(keys))
	for varIdx, varName := range keys {
		name := TransportName(hookIdx, varIdx)
		val, ok := os.LookupEnv(name)
		if !ok || val == "" {
			return nil, fmt.Errorf("hook %q env %q: docker transport var %s not set", h.ID, varName, name)
		}
		out[varName] = val
	}
	return out, nil
}

// UnsetTransportVars удаляет AFM_HOOK_SECRET_* после резолва всех хуков и до запуска дочерних процессов.
func UnsetTransportVars() {
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(name, HookSecretTransportPrefix) {
			_ = os.Unsetenv(name)
		}
	}
}
