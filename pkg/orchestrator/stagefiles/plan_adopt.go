package stagefiles

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/prompts"
)

// adoptWrittenPlan ищет валидный план среди файлов, которые планирующий агент
// записал через Write tool, и копирует найденный план в outFile. Пути берутся
// из stream-json лога (logFile с расширением .jsonl).
//
// Нужен когда описание стадии просит агента сохранить план в файл с
// произвольным именем: текстовый вывод агента тогда — лишь резюме, и валидация
// outFile проваливается, хотя полноценный план лежит рядом в файле.
// Кандидаты проверяются от последнего записанного к первому.
func AdoptWrittenPlan(logFile, outFile string) bool {
	jsonlFile := strings.TrimSuffix(logFile, ".log") + ".jsonl"
	absOut, err := filepath.Abs(outFile)
	if err != nil {
		return false
	}

	files := executor.WrittenFiles(jsonlFile)
	for i := len(files) - 1; i >= 0; i-- {
		abs, absErr := filepath.Abs(files[i])
		if absErr != nil || abs == absOut {
			// outFile уже проверен валидацией — не кандидат.
			continue
		}
		data, readErr := os.ReadFile(abs)
		if readErr != nil {
			continue
		}
		if !prompts.ValidatePlan(string(data), RequiredPlanSections).IsClean() {
			continue
		}
		return os.WriteFile(outFile, data, 0644) == nil
	}
	return false
}
