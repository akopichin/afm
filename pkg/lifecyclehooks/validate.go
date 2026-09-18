package lifecyclehooks

import (
	"fmt"
	"regexp"
	"strings"
)

// parentDir — сегмент «..»: запрещён в id хука (path traversal).
const parentDir = ".."

// envVarNameRe — допустимое имя целевой env-переменной хука.
var envVarNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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
		if h.ID == "." || h.ID == parentDir || strings.ContainsAny(h.ID, "/\\\x00") {
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
		// env: имена целевых переменных валидны, не начинаются (регистронезависимо)
		// с зарезервированного префикса AFM_ и не коллизируют друг с другом после
		// ASCII upper-case (Windows env — регистронезависимое). Источник —
		// env:NAME или file:PATH с непустым хвостом; его существование не
		// проверяется здесь (Task 3, резолв в рантайме).
		seenUpper := make(map[string]bool, len(h.Env))
		for varName, ref := range h.Env {
			if !envVarNameRe.MatchString(varName) {
				return fmt.Errorf("hooks[%d] (%s): env var name %q must match [A-Za-z_][A-Za-z0-9_]*", i, h.ID, varName)
			}
			up := strings.ToUpper(varName)
			if strings.HasPrefix(up, "AFM_") {
				return fmt.Errorf("hooks[%d] (%s): env var name %q: prefix AFM_ is reserved (case-insensitive)", i, h.ID, varName)
			}
			if seenUpper[up] {
				return fmt.Errorf("hooks[%d] (%s): env var name %q collides case-insensitively with another", i, h.ID, varName)
			}
			seenUpper[up] = true
			s := string(ref)
			okEnv := strings.HasPrefix(s, "env:") && len(s) > len("env:")
			okFile := strings.HasPrefix(s, "file:") && len(s) > len("file:")
			if !okEnv && !okFile {
				return fmt.Errorf("hooks[%d] (%s): env %q source must be env:NAME or file:PATH, got %q", i, h.ID, varName, s)
			}
		}
	}
	return nil
}
