package lifecyclehooks

// secretValues извлекает значения резолвнутых секретов хука в плоский список для передачи в нейтральный
// редактор pkg/redact.
func secretValues(h Hook) []string {
	out := make([]string, 0, len(h.ResolvedEnv))
	for _, v := range h.ResolvedEnv {
		out = append(out, v)
	}
	return out
}
