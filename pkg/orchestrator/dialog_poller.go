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
	"github.com/akopichin/afm/pkg/orchestrator/bus"
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

	// activeInteractiveCount считает interactive-стадии, активные прямо сейчас
	// (см. relocateMisplacedQuestions: файл в root_dir не несёт stageID, поэтому
	// скан root_dir безопасен, только когда однозначно ясно, кому он принадлежит).
	activeInteractiveCount := 0
	for id, st := range snap.Stages {
		switch st.Status {
		case state.StatusPlanning, state.StatusRunning, state.StatusRevising,
			state.StatusRetrying, state.StatusAwaitingUserInput:
		default:
			continue
		}
		if stage := o.graph.Stage(id); stage != nil && stage.Interactive {
			activeInteractiveCount++
		}
	}

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

			// Non-interactive stage (default, or agents:[auto]): afm answers the
			// question itself instead of surfacing it to a human — the stage's
			// FSM status is left untouched (no EvAskUser transition).
			if stage := o.graph.Stage(stageID); stage != nil && !stage.Interactive {
				answer, fromOptions := mcp.PickAutoAnswer(q)
				if err := mcp.WriteAnswer(stageDir, q.Phase, q.ID, answer, fromOptions, true); err != nil {
					log.Printf("WARN: auto-answer %s/%s.%s: %v", stageID, q.Phase, q.ID, err)
					continue
				}
				o.ui.Publish(bus.Event{
					Type:    bus.EventAutoAnswered,
					StageID: stageID,
					Data: map[string]any{
						keyID: q.ID, keyPhase: q.Phase, "answer": answer, "from_options": fromOptions,
					},
				})
				continue
			}

			// Сохраняем реальную фазу ДО перехода в awaiting_user_input.
			// Фаза из имени файла (q.Phase) может быть неправильной (агент написал
			// "review" вместо "planning") — при EvUserAnswered используем сохранённую
			// фазу, а не ту что в файле вопроса.
			o.preAskPhase.Store(stageID, o.correctPhaseForState(o.currentStatus(stageID), q.Phase))
			// Триггерим ПЕРЕД публикацией ask_user, чтобы приложить к событию
			// реальный seq этой transition — фронт дедуплицирует по нему live-
			// событие с историческим двойником из /api/events.
			_, seq, _ := o.triggerWithSeq(stageID, bus.EvAskUser, bus.GuardCtx{Phase: q.Phase}, "")
			o.ui.Publish(bus.Event{
				Type:    bus.EventAskUser,
				StageID: stageID,
				Data: map[string]any{
					keyID: q.ID, keyPhase: q.Phase, "question": q.Question,
					"options": q.Options, "allow_custom": q.AllowCustom,
				},
				Seq: seq,
			})
		}
		// No open question in stageDir: if this is an interactive stage, check
		// whether the agent wrote one elsewhere (GLM-4.7 hallucination bug: agent
		// constructs path from CWD instead of reading $AFM_STAGE_DIR).
		// Auto-relocate the misplaced file so the dialog becomes visible in the UI.
		if len(questions) == 0 {
			if stage := o.graph.Stage(stageID); stage != nil && stage.Interactive {
				o.relocateMisplacedQuestions(stageID, stageDir, activeInteractiveCount <= 1)
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
	var phases []string
	for _, p := range flow.Phases() {
		if p == flow.PhaseAutonomous && !isAutonomousStage(stageDir) {
			continue
		}
		phases = append(phases, string(p))
	}
	return phases
}

// relocateScanInterval — throttle для скана root_dir внутри
// relocateMisplacedQuestions: это запасная сеть, а не основной путь, лишняя
// частота (раз в секунду, как основной poll-тик) не нужна.
const relocateScanInterval = 5 * time.Second

// activeDialogPhase возвращает фазу, чей <phase>.jsonl изменялся последним
// среди dialogPhases(stageDir) — это и есть фаза, в которой сейчас реально
// работает агент (одновременно активна только одна). Пустая строка, если
// агент ещё не начал писать ни в одну фазу (ни один <phase>.jsonl не создан).
func activeDialogPhase(stageDir string) string {
	var latest string
	var latestMod time.Time
	for _, phase := range dialogPhases(stageDir) {
		info, err := os.Stat(filepath.Join(stageDir, jsonlFileForPhase(phase)))
		if err != nil {
			continue
		}
		if latest == "" || info.ModTime().After(latestMod) {
			latest = phase
			latestMod = info.ModTime()
		}
	}
	return latest
}

// collectQuestionFiles добавляет в into абсолютные пути всех *.question.json
// в верхнем уровне dir (без рекурсии в поддиректории).
func collectQuestionFiles(dir string, into map[string]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".question.json") {
			continue
		}
		into[filepath.Join(dir, e.Name())] = true
	}
}

// relocateMisplacedQuestions чинит два способа, которыми агент может «спрятать»
// файл вопроса от поллера, и оба ведут к вечному зависанию стадии:
//
//  1. Неверная ДИРЕКТОРИЯ: question.json записан вне stageDir (агент строит
//     путь из CWD вместо $AFM_STAGE_DIR).
//  2. Неверный ПРЕФИКС: файл лежит внутри stageDir, но назван не по
//     канонической фазе (напр. "commit-changes.q1.question.json" или вообще
//     без префикса "q1.question.json").
//
// В отличие от прежней реализации, НЕ парсит stream-json лог агента в поисках
// вызова инструмента Write — сканирует файловую систему напрямую, поэтому не
// зависит от того, каким инструментом (Write, Bash echo/heredoc, кастомный
// скилл-скрипт) агент создал файл.
//
// Скан stageDir (случай 2) выполняется всегда — дёшево, там всего несколько
// файлов. Скан root_dir (случай 1, allowRootScan) — throttled раз в
// relocateScanInterval на стадию и ТОЛЬКО когда allowRootScan=true: если
// параллельно активно ≥2 interactive-стадий без открытого вопроса, у файла в
// root_dir нет однозначного адресата (в имени только phase+id, без stageID) —
// безопаснее оставить стадию висеть ещё один тик поллера, чем угадать неверно
// (см. дизайн-документ, "Безопасность при нескольких параллельных
// interactive-стадиях").
func (o *Orchestrator) relocateMisplacedQuestions(stageID, stageDir string, allowRootScan bool) {
	phase := activeDialogPhase(stageDir)
	if phase == "" {
		return // агент ещё не начал писать ни в одну фазу — сканировать нечего
	}

	candidates := map[string]bool{}
	collectQuestionFiles(stageDir, candidates)

	if allowRootScan && o.opts.RootDir != "" && !pathInside(o.opts.RootDir, stageDir) {
		last, seen := o.lastRootScan[stageID]
		if !seen || time.Since(last) >= relocateScanInterval {
			o.lastRootScan[stageID] = time.Now()
			collectQuestionFiles(o.opts.RootDir, candidates)
		}
	}

	for f := range candidates {
		o.normalizeMisplacedQuestion(f, stageDir, phase)
	}
}

// normalizeMisplacedQuestion нормализует один найденный файл в канонический
// путь "<phase>.<id>.question.json" внутри stageDir и создаёт dangling-симлинк
// на будущий answer.json по (неверному) пути, который опрашивает агент.
//
// Разбор имени: "<prefix>.<id>.question.json" → id (префикс отбрасывается,
// неважно, стадии он или фазы). Если в имени нет точки-разделителя вообще
// (агент написал голый "q1.question.json") — id это всё имя целиком.
func (o *Orchestrator) normalizeMisplacedQuestion(f, stageDir, phase string) {
	base := filepath.Base(f)
	trimmed := strings.TrimSuffix(base, ".question.json")
	id := trimmed
	if dot := strings.Index(trimmed, "."); dot >= 0 {
		id = trimmed[dot+1:]
	}
	if id == "" {
		return
	}
	// Файл уже разрешён под СВОИМ (возможно другим) именем — не трогаем.
	// Отсекает случай, когда предыдущая фаза интерактивной стадии уже
	// задала вопрос и получила ответ: activeDialogPhase() к этому моменту
	// может указывать на следующую фазу, но старый
	// <старая-фаза>.<id>.question.json остаётся лежать в stageDir и не
	// должен копироваться под именем новой фазы, будто это новый вопрос.
	ownAnswer := strings.TrimSuffix(f, ".question.json") + ".answer.json"
	if _, err := os.Stat(ownAnswer); err == nil {
		return
	}
	dstBase := phase + "." + id + ".question.json"
	dst := filepath.Join(stageDir, dstBase)

	if _, err := os.Stat(dst); err == nil {
		return // уже на каноническом месте (либо f==dst, либо нормализовано ранее)
	}
	data, err := os.ReadFile(f)
	if err != nil {
		log.Printf("WARN: normalize question %s: read: %v", f, err)
		return
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		log.Printf("WARN: normalize question %s → %s: write: %v", f, dst, err)
		return
	}
	wrongAnswer := filepath.Join(filepath.Dir(f), trimmed+".answer.json")
	rightAnswer := filepath.Join(stageDir, phase+"."+id+".answer.json")
	if _, err := os.Lstat(wrongAnswer); err != nil {
		_ = os.MkdirAll(filepath.Dir(wrongAnswer), 0755)
		_ = os.Symlink(rightAnswer, wrongAnswer)
	}
	log.Printf("INFO: normalized misplaced question %s → %s (symlink answer)", f, dst)
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
