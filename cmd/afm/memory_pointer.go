package main

import "fmt"

// buildMemoryPointer возвращает текст, дописываемый к GlobalPrompt, который
// СООБЩАЕТ агенту путь к файлам памяти (содержимое агент читает сам своим
// Read — так промпт не растёт вместе с памятью). Пусто, если память выключена.
func buildMemoryPointer(projectPath, sessionPath string) string {
	if projectPath == "" {
		return ""
	}
	return fmt.Sprintf(`Project memory — accumulated findings from earlier stages and runs — lives at:
  %s
Session memory — this run's short-term context — lives at:
  %s
Before you start, read both files and take their Best Practices (🟩) and Anti-Patterns (🟥) into account. They may not exist yet on the first stage; that is fine.`, projectPath, sessionPath)
}
