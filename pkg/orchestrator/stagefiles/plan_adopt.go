package stagefiles

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/prompts"
)

// AdoptWrittenPlan ищет валидный план среди файлов, которые планирующий агент записал через Write tool, и
// копирует найденный план в outFile. agentDir — рабочий каталог агента: от него считаются относительные пути.
func AdoptWrittenPlan(logFile, outFile, agentDir string) bool {
	jsonlFile := strings.TrimSuffix(logFile, ".log") + ".jsonl"
	absOut, err := filepath.Abs(outFile)
	if err != nil {
		return false
	}

	files := executor.WrittenFiles(jsonlFile)
	for i := len(files) - 1; i >= 0; i-- {
		abs, absErr := executor.AgentPath(agentDir, files[i])
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
