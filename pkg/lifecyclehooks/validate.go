package lifecyclehooks

import (
	"fmt"
	"strings"
)

// ValidateLayer проверяет слой хуков (config-слой, flow-слой или хуки одной
// стадии). stageScoped=true — хуки конкретной стадии: flow-события в них
// запрещены (stage hook физически не может получить событие всего флоу).
func ValidateLayer(defs []Hook, stageScoped bool) error {
	known := make(map[EventType]bool, 32)
	for _, ev := range AllEvents() {
		known[ev] = true
	}
	ids := make(map[string]bool, len(defs))
	for i, h := range defs {
		if h.ID == "" {
			return fmt.Errorf("hooks[%d]: id is required", i)
		}
		// id становится именем файла лога (<LogDir>/<id>.log) — обязан быть
		// безопасным одиночным компонентом пути, иначе `id: ../events.jsonl`
		// допишет hook-лог в авторитетный events.jsonl (codex CRIT#1).
		if h.ID == "." || h.ID == ".." || strings.ContainsAny(h.ID, "/\\\x00") {
			return fmt.Errorf("hooks[%d]: id %q must be a single safe path component (no /, \\, NUL, dot segments)", i, h.ID)
		}
		if ids[h.ID] {
			return fmt.Errorf("hooks[%d]: duplicate id %q in the same layer", i, h.ID)
		}
		ids[h.ID] = true
		if h.Command == "" {
			return fmt.Errorf("hooks[%d] (%s): command is required", i, h.ID)
		}
		if !h.Events.All && len(h.Events.Events) == 0 {
			return fmt.Errorf("hooks[%d] (%s): events must be \"all\" or a non-empty list", i, h.ID)
		}
		for _, ev := range h.Events.Events {
			if !known[ev] {
				return fmt.Errorf("hooks[%d] (%s): unknown event %q", i, h.ID, ev)
			}
			if stageScoped && IsFlowEvent(ev) {
				return fmt.Errorf("hooks[%d] (%s): flow-level event %q is not allowed in a stage hook", i, h.ID, ev)
			}
		}
		for _, ev := range h.SkipEvents {
			if !known[ev] {
				return fmt.Errorf("hooks[%d] (%s): unknown skip_events entry %q", i, h.ID, ev)
			}
		}
		if h.Retries < 0 {
			return fmt.Errorf("hooks[%d] (%s): retries must not be negative", i, h.ID)
		}
		if h.Timeout < 0 {
			return fmt.Errorf("hooks[%d] (%s): timeout must not be negative", i, h.ID)
		}
	}
	return nil
}
