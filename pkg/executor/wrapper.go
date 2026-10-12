package executor

// WrapperDirFor возвращает wrapper-dir для команды cmd: для generated-команд — wrapperDir, чтобы
// сгенерированный скрипт резолвился на PATH; для остальных — пусто.
func WrapperDirFor(cmd string, wrapperDir string, generated map[string]bool) string {
	if generated[cmd] {
		return wrapperDir
	}
	return ""
}
