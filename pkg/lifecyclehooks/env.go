package lifecyclehooks

import (
	"fmt"

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
