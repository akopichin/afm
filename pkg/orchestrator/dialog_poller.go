package orchestrator

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/state"
)

// violationCacheEntry хранит stat-данные для одного .jsonl файла.
// Используется в detectDialogViolation чтобы не перечитывать неизменившиеся файлы.
type violationCacheEntry struct {
	size  int64
	mtime time.Time
}

// startQuestionPoller launches a goroutine that scans active stage directories
// every second for new *.question.json files (file-based dialog protocol).
func (o *Orchestrator) startQuestionPoller(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		processed := map[string]bool{} // "stageID|phase|id" → true
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				o.pollQuestions(processed)
			}
		}
	}()
}

// pollQuestions scans each active stage directory for unanswered question files.
// For each new file: writes it to dialog.jsonl (for UI history) and publishes
// EventAskUser to transition the stage to awaiting_user_input.
func (o *Orchestrator) pollQuestions(processed map[string]bool) {
	snap := o.opts.Store.Snapshot()
	for stageID, st := range snap.Stages {
		switch st.Status {
		case state.StatusPlanning, state.StatusRunning, state.StatusRevising,
			state.StatusRetrying, state.StatusAwaitingUserInput:
		default:
			continue
		}
		stageDir := filepath.Join(o.opts.RunDir, stageID)
		questions, err := mcp.FindUnansweredQuestions(stageDir)
		if err != nil {
			continue
		}
		for _, q := range questions {
			key := stageID + "|" + q.Phase + "|" + q.ID
			if processed[key] {
				continue
			}
			processed[key] = true
			// Write to dialog.jsonl for history (idempotent via FindEntry check).
			dialogPath := filepath.Join(stageDir, q.Phase+".dialog.jsonl")
			if e, _ := mcp.FindEntry(dialogPath, q.ID); e == nil {
				_ = mcp.AppendQuestion(dialogPath, mcp.Question{
					ID:          q.ID,
					Question:    q.Question,
					Options:     q.Options,
					AllowCustom: q.AllowCustom,
				})
			}
			// Notify UI and transition stage status.
			o.ui.Publish(Event{
				Type:    EventAskUser,
				StageID: stageID,
				Data: map[string]any{
					keyID: q.ID, keyPhase: q.Phase, "question": q.Question,
					"options": q.Options, "allow_custom": q.AllowCustom,
				},
			})
			// Сохраняем реальную фазу ДО перехода в awaiting_user_input.
			// Фаза из имени файла (q.Phase) может быть неправильной (агент написал
			// "review" вместо "planning") — при EvUserAnswered используем сохранённую
			// фазу, а не ту что в файле вопроса.
			o.preAskPhase.Store(stageID, o.correctPhaseForState(o.currentStatus(stageID), q.Phase))
			o.Trigger(stageID, EvAskUser, GuardCtx{Phase: q.Phase}, "")
		}
		// No open question in stageDir: if this is an interactive stage, check
		// whether the agent wrote one elsewhere (GLM-4.7 hallucination bug: agent
		// constructs path from CWD instead of reading $AFM_STAGE_DIR).
		// Auto-relocate the misplaced file so the dialog becomes visible in the UI.
		if len(questions) == 0 {
			if stage := o.graph.Stage(stageID); stage != nil && stage.Interactive {
				o.relocateMisplacedQuestions(stageDir)
			}
		}
	}
}

// detectDialogViolation scans the agent's stream-json logs (<phase>.jsonl) for a
// Write of a *.question.json file OUTSIDE the stage directory. Such a write
// violates the file-based dialog contract: the poller and dashboard only look
// inside stageDir, so a misplaced question hangs the stage forever. Returns a
// human-readable reason when a violation is found.
//
// Результат каждого файла кешируется по (size, mtime): если файл не изменился
// с прошлого тика, он не перечитывается. Метод вызывается только из поллера —
// синхронизация не нужна.
func (o *Orchestrator) detectDialogViolation(stageDir string) (string, bool) {
	phases := dialogPhases(stageDir)
	for _, phase := range phases {
		jsonlPath := filepath.Join(stageDir, jsonlFileForPhase(phase))
		info, err := os.Stat(jsonlPath)
		if err != nil {
			continue // файл не существует — нарушений нет
		}
		cached, ok := o.violationCache[jsonlPath]
		if ok && cached.size == info.Size() && cached.mtime.Equal(info.ModTime()) {
			continue // не изменился с прошлого тика
		}
		for _, f := range executor.WrittenFiles(jsonlPath) {
			if !strings.HasSuffix(filepath.Base(f), ".question.json") {
				continue
			}
			if !pathInside(f, stageDir) {
				return fmt.Sprintf("dialog protocol violation: question written to %s, expected %s", f, stageDir), true
			}
		}
		o.violationCache[jsonlPath] = violationCacheEntry{size: info.Size(), mtime: info.ModTime()}
	}
	return "", false
}

// jsonlFileForPhase возвращает имя JSONL-лога для фазы (делегирует flow).
func jsonlFileForPhase(phase string) string {
	return flow.PhaseJSONL(flow.Phase(phase))
}

// dialogPhases возвращает фазы, чьи диалоговые артефакты (session/jsonl/вопросы)
// относятся к стадии: базовые planning/implementation/review плюс autonomous,
// если стадия исполняется в автономном треке (в stageDir есть autonomous.flag).
// Единый источник для сканов, ранее собиравшихся вручную в нескольких местах.
func dialogPhases(stageDir string) []string {
	phases := []string{phasePlanning, phaseImplementation, phaseReview}
	if isAutonomousStage(stageDir) {
		phases = append(phases, phaseAutonomous)
	}
	return phases
}

// relocateMisplacedQuestions чинит два способа, которыми агент может «спрятать»
// файл вопроса от поллера, и оба ведут к вечному зависанию стадии:
//
//  1. Неверная ДИРЕКТОРИЯ: question.json записан вне stageDir (GLM-4.7 bug —
//     модель конструирует путь из CWD вместо $AFM_STAGE_DIR).
//  2. Неверный ПРЕФИКС: файл лежит внутри stageDir, но назван по id стадии
//     (напр. "commit-changes.q1.question.json") вместо канонической фазы
//     ("planning.q1.question.json"). FindUnansweredQuestions матчит только
//     planning/implementation/review/autonomous_execution → такой файл невидим.
//
// В обоих случаях файл нормализуется к каноническому имени "<phase>.<id>.question.json".
// Правильная фаза берётся не из (возможно неверного) префикса, а из того, в чьём
// <phase>.jsonl нашёлся Write этого файла — это авторитетный признак. Для каждого
// нормализованного файла создаётся dangling-симлинк по ПУТИ, который опрашивает
// агент (его директория + его префикс), → канонический answer.json в stageDir,
// чтобы bash-polling-loop нашёл ответ, даже если агент ошибся и с папкой, и с префиксом.
func (o *Orchestrator) relocateMisplacedQuestions(stageDir string) {
	phases := dialogPhases(stageDir)
	for _, phase := range phases {
		jsonlPath := filepath.Join(stageDir, jsonlFileForPhase(phase))
		for _, f := range executor.WrittenFiles(jsonlPath) {
			base := filepath.Base(f)
			if !strings.HasSuffix(base, ".question.json") {
				continue
			}
			// Разбираем "<prefix>.<id>.question.json" → id.
			trimmed := strings.TrimSuffix(base, ".question.json")
			dot := strings.Index(trimmed, ".")
			if dot < 0 || trimmed[dot+1:] == "" {
				continue // не формат <prefix>.<id> — не наш файл
			}
			id := trimmed[dot+1:]
			dstBase := phase + "." + id + ".question.json"
			dst := filepath.Join(stageDir, dstBase)

			// Файл уже на своём каноническом месте — поллер подхватит штатно.
			if pathInside(f, stageDir) && base == dstBase {
				continue
			}
			if _, err := os.Stat(f); err != nil {
				continue // файл не существует — агент ещё не дошёл до записи
			}
			if _, err := os.Stat(dst); err == nil {
				continue // уже нормализован ранее
			}
			data, err := os.ReadFile(f)
			if err != nil {
				log.Printf("WARN: normalize question %s: read: %v", f, err)
				continue
			}
			if err := os.WriteFile(dst, data, 0644); err != nil {
				log.Printf("WARN: normalize question %s → %s: write: %v", f, dst, err)
				continue
			}
			wrongAnswer := filepath.Join(filepath.Dir(f), trimmed+".answer.json")
			rightAnswer := filepath.Join(stageDir, phase+"."+id+".answer.json")
			if _, err := os.Lstat(wrongAnswer); err != nil {
				_ = os.MkdirAll(filepath.Dir(wrongAnswer), 0755)
				_ = os.Symlink(rightAnswer, wrongAnswer)
			}
			log.Printf("INFO: normalized misplaced question %s → %s (symlink answer)", f, dst)
		}
	}
}

// pathInside reports whether file is located inside dir. Both are resolved to
// absolute paths the same way (filepath.Abs, no symlink resolution), so they
// stay in a consistent form — the agent's Write paths and stageDir both originate
// from the same source (AFM_STAGE_DIR), so a consistent resolution is
// sufficient and avoids EvalSymlinks' failure on not-yet-existing files.
func pathInside(file, dir string) bool {
	absFile, err := filepath.Abs(file)
	if err != nil {
		absFile = filepath.Clean(file)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = filepath.Clean(dir)
	}
	if absDir != string(filepath.Separator) {
		absDir += string(filepath.Separator)
	}
	return strings.HasPrefix(absFile+string(filepath.Separator), absDir)
}

// hasOpenQuestion reports whether stageDir contains a *.question.json file
// for the given phase that has no corresponding *.answer.json yet.
func (o *Orchestrator) hasOpenQuestion(stageID, phase string) bool {
	if phase == "" {
		return false
	}
	questions, err := mcp.FindUnansweredQuestions(filepath.Join(o.opts.RunDir, stageID))
	if err != nil {
		return false
	}
	for _, q := range questions {
		if q.Phase == phase {
			return true
		}
	}
	return false
}

// correctPhaseForState возвращает корректную фазу для возврата из awaiting_user_input,
// основываясь на текущем состоянии FSM, а не на фазе из имени файла вопроса.
// Агент может написать неправильное имя фазы (напр. "review" вместо "planning"),
// поэтому мы дублируем правило phaseDispatch на основе реального состояния:
// planning/revising → phasePlanning, всё остальное → фаза из файла (обычно корректна).
func (o *Orchestrator) correctPhaseForState(current state.StageStatus, filePhase string) string {
	if current == state.StatusPlanning || current == state.StatusRevising {
		return phasePlanning
	}
	return filePhase
}

// popPreAskPhase читает и удаляет сохранённую фазу для стейджа.
// Если запись отсутствует (напр. перезапуск afm без перехода через EvAskUser),
// возвращает fallback — фазу из имени файла вопроса.
func (o *Orchestrator) popPreAskPhase(stageID, fallback string) string {
	if v, ok := o.preAskPhase.LoadAndDelete(stageID); ok {
		return v.(string)
	}
	return fallback
}
