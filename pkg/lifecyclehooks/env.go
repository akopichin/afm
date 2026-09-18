package lifecyclehooks

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/secrets"
)

// ResolveHookEnv резолвит env-ссылки хука в конкретные значения (targetVar→value)
// через pkg/secrets. Пустой env → nil. Ошибка (источник не найден/пуст) называет
// hook id и имя целевой переменной, но НЕ значение секрета.
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

// HookSecretTransportPrefix — префикс transient env-переменных, которыми
// хост докер-режима передаёт УЖЕ РЕЗОЛВНУТЫЕ секреты хуков в контейнер (см.
// TransportName). Единая точка для сравнения/фильтрации по префиксу
// (stripTransportVars в runner.go, UnsetTransportVars ниже).
const HookSecretTransportPrefix = "AFM_HOOK_SECRET_" //nolint:gosec // env-var name prefix, not a credential

// TransportName возвращает имя docker-transport env-переменной для одной
// env-переменной хука: hookIdx — позиция хука в итоговом []RegisteredHook
// (см. Combine), varIdx — позиция имени переменной в ОТСОРТИРОВАННОМ списке
// ключей Hook.Env (см. SortedEnvKeys). Индексное имя инъективно по
// построению: в отличие от sanitize(hookID) (коллизии на пунктуации/регистре,
// напр. "foo-bar" и "foo_bar" схлопнулись бы в один суффикс), два разных
// хука никогда не могут получить одно и то же транспортное имя. Хост и
// контейнер вычисляют его одинаково из ОДНОГО И ТОГО ЖЕ итогового combined —
// см. cmd/afm/run.go (Combine вынесен до docker-ветки).
func TransportName(hookIdx, varIdx int) string {
	return fmt.Sprintf("%s%d_%d", HookSecretTransportPrefix, hookIdx, varIdx)
}

// SortedEnvKeys возвращает имена переменных h.Env в детерминированном
// (отсортированном) порядке — источник varIdx для TransportName. Итерация по
// map в Go не детерминирована, поэтому и хост (резолв на docker run), и
// контейнер (чтение транспорта) ОБЯЗАНЫ использовать именно эту функцию,
// иначе varIdx разойдётся между сторонами транспорта.
func SortedEnvKeys(h Hook) []string {
	keys := make([]string, 0, len(h.Env))
	for k := range h.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// ResolveHookEnvFromTransport резолвит Hook.Env хука ИЗ docker-transport
// env-переменных (см. TransportName), а не из secrets.env-слоёв — используется
// in-container (AFM_IN_DOCKER=1), где секреты уже резолвнуты хостом до
// `docker run` и переданы как transient bare `-e` переменные. Пустая/
// отсутствующая транспортная переменная — fail-fast ошибка (тот же контракт,
// что у ResolveHookEnv: никогда не отдавать частично резолвнутый ResolvedEnv).
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

// UnsetTransportVars снимает КАЖДУЮ AFM_HOOK_SECRET_* переменную из окружения
// текущего процесса — единая точка изоляции (не перечисление spawn-сайтов):
// вызывается ОДИН раз in-container, сразу после того как ВСЕ хуки резолвили
// свой ResolvedEnv из транспорта, и ДО запуска любого агента/стадии. После
// этого вызова транспортных переменных в окружении afm-процесса нет вовсе —
// ни один дочерний процесс (агент, script-стадия, RunVerify, RunJSONQuery,
// git, другой хук) не может их унаследовать. Хуки продолжают получать
// секреты через Hook.ResolvedEnv (в памяти), поэтому снятие транспортных
// переменных безопасно и не влияет на доставку.
func UnsetTransportVars() {
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(name, HookSecretTransportPrefix) {
			_ = os.Unsetenv(name)
		}
	}
}
