package stagefiles

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/prompts"
)

// adoptWrittenPlan ищет валидный план среди файлов, которые планирующий агент записал через Write tool, и
// копирует найденный план в outFile.
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
