package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/akopichin/afm/pkg/executor"
)

// blockingThenFeedbackRunner: первый вызов RunAgent просто блокируется на
// ctx.Done() (реальный executor так же блокируется на подпроцессе, пока не
// придёт SIGINT/отмена) — используется вместе с настоящим InterruptCh через
// Trigger(EvRevise)+сигнал в канал реестра. Второй вызов (перезапуск с
// фидбеком) сразу читает feedback.md и завершает стадию.
//
// Используется в TestAgentSuggest_InterruptRestartsWithFeedback (Task 5,
// добавляется вместе с обработкой Revise для статуса running) — здесь только
// тип, чтобы Task 4 компилировался без готового e2e-теста.
type blockingThenFeedbackRunner struct {
	calls int
}

func (r *blockingThenFeedbackRunner) RunPlanning(_ context.Context, _, _, _, _ string) error {
	return errors.New("not used in this test")
}

func (r *blockingThenFeedbackRunner) RunAgent(ctx context.Context, _, _, _, logFile string) error {
	r.calls++
	stageDir := filepath.Dir(logFile)
	if r.calls == 1 {
		<-ctx.Done()
		return context.Canceled
	}
	feedback, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	if len(feedback) == 0 {
		return errors.New("expected feedback.md to be readable on the feedback restart")
	}
	return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644)
}

func (r *blockingThenFeedbackRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("not used in this test")
}

var _ executor.Runner = (*blockingThenFeedbackRunner)(nil)
