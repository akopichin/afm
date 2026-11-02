package assistants

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// RuntimeContext — какое окружение получает процесс помощника: isolated (по умолчанию) отдаёт ему только
// свой секрет и system prompt, wrapper дополнительно оставляет ambient provider auth процесса afm.
type RuntimeContext string

const (
	RuntimeContextIsolated RuntimeContext = "isolated"
	RuntimeContextWrapper  RuntimeContext = "wrapper"
)

// yamlStringTag — тег YAML-скаляра-строки: только он может нести значение перечисления.
const yamlStringTag = "!!str"

// Valid сообщает, что значение — одно из двух известных. Пустая строка невалидна: значение по умолчанию
// подставляет только ResolveRuntimeContext.
func (r RuntimeContext) Valid() bool {
	return r == RuntimeContextIsolated || r == RuntimeContextWrapper
}

// ParseRuntimeContext проверяет значение поля field (полный путь ключа для текста ошибки).
func ParseRuntimeContext(field, s string) (RuntimeContext, error) {
	r := RuntimeContext(s)
	if !r.Valid() {
		return "", fmt.Errorf("%s: must be %q or %q, got %q", field, RuntimeContextIsolated, RuntimeContextWrapper, s)
	}
	return r, nil
}

// ParseRuntimeContextNode проверяет сырой YAML-узел до превращения в указатель: явный null, пустая строка,
// число, bool и коллекции отвергаются, а не стираются до «не задано». Алиас разворачивается до цели.
func ParseRuntimeContextNode(field string, n *yaml.Node) (RuntimeContext, error) {
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n.Kind != yaml.ScalarNode || n.Tag != yamlStringTag {
		return "", fmt.Errorf("%s: must be a string %q or %q", field, RuntimeContextIsolated, RuntimeContextWrapper)
	}
	return ParseRuntimeContext(field, n.Value)
}

// ResolveRuntimeContext применяет приоритет профиль → collaboration → isolated.
func ResolveRuntimeContext(profile, collaboration *RuntimeContext) RuntimeContext {
	switch {
	case profile != nil:
		return *profile
	case collaboration != nil:
		return *collaboration
	default:
		return RuntimeContextIsolated
	}
}
