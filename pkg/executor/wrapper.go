package executor

// WrapperDirFor возвращает wrapper-dir для команды cmd: для generated-команд
// (autoShim) — wrapperDir, чтобы сгенерированный скрипт резолвился на PATH;
// для остальных (включая claude) — пусто (используется реальный бинарник).
func WrapperDirFor(cmd string, wrapperDir string, generated map[string]bool) string {
	if generated[cmd] {
		return wrapperDir
	}
	return ""
}
