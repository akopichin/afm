package assistants

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// CollaborationMode — режим представления collaboration в dashboard. Влияет только на UI: механизмы
// team/mesh/board и их исполнение от режима не зависят.
type CollaborationMode string

const (
	// CollaborationModeTeam — режим по умолчанию: Team, Board и People видны.
	CollaborationModeTeam CollaborationMode = "team"
	// CollaborationModeAssistants скрывает Team, Board и People; помощники под стадиями остаются.
	CollaborationModeAssistants CollaborationMode = "assistants"
)

// ParseCollaborationMode проверяет значение collaboration.mode. Пустая строка и неизвестные значения —
// ошибка: значение по умолчанию подставляет только разрешение режима, а не парсер.
func ParseCollaborationMode(s string) (CollaborationMode, error) {
	switch m := CollaborationMode(s); m {
	case CollaborationModeTeam, CollaborationModeAssistants:
		return m, nil
	default:
		return "", fmt.Errorf("collaboration.mode: must be %q or %q, got %q",
			CollaborationModeTeam, CollaborationModeAssistants, s)
	}
}

// ParseCollaborationModeNode проверяет сырой YAML-узел значения mode до того, как он превратится в
// *string: так явный null, число, bool, mapping и sequence отвергаются, а не стираются до «не задано»
// или приводятся к строке. Алиас (*anchor) сначала разворачивается до целевого узла. Общая проверка для
// config и flow.
func ParseCollaborationModeNode(n *yaml.Node) (CollaborationMode, error) {
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return "", fmt.Errorf("collaboration.mode: must be a string %q or %q",
			CollaborationModeTeam, CollaborationModeAssistants)
	}
	return ParseCollaborationMode(n.Value)
}
