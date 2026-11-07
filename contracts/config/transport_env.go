package config

import "strings"

// Префиксы транспортных переменных, через которые pkg/docker передаёт секрет и system prompt recipe-команды
// в контейнер, а executor — в процесс агента.
const (
	SecretEnvPrefix    = "AFM_SECRET_"    //nolint:gosec // это имя переменной окружения, а не сам секрет
	SyspromptEnvPrefix = "AFM_SYSPROMPT_" //nolint:gosec // аналогично
)

// CommandEnvName превращает имя команды в суффикс имени переменной: только [A-Z0-9_], остальное → '_'.
func CommandEnvName(cmd string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(cmd) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// SecretEnvVar — имя переменной с секретом команды (AFM_SECRET_<NAME>).
func SecretEnvVar(cmd string) string { return SecretEnvPrefix + CommandEnvName(cmd) }

// SyspromptEnvVar — имя переменной с system prompt команды (AFM_SYSPROMPT_<NAME>).
func SyspromptEnvVar(cmd string) string { return SyspromptEnvPrefix + CommandEnvName(cmd) }
