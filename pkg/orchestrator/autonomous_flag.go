package orchestrator

import (
	"os"
	"path/filepath"
)

// isAutonomousStage возвращает true, если stageDir содержит autonomous.flag —
// маркер того, что стадия уже переведена на автономный трек (agents:[auto]).
func isAutonomousStage(stageDir string) bool {
	_, err := os.Stat(filepath.Join(stageDir, "autonomous.flag"))
	return err == nil
}

// clearStaleAutonomousFlag удаляет autonomous.flag, оставшийся от неудавшейся автономной попытки, когда
// текущая попытка идёт по стандартному треку.
func clearStaleAutonomousFlag(stageDir string) {
	_ = os.Remove(filepath.Join(stageDir, "autonomous.flag"))
}
