package orchestrator

import (
	"context"
	"encoding/json"
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
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// maxMalformedRetries — сколько раз afm просит агента переписать
// question.json, который не парсится даже после jsonrepair, прежде чем
// сдаться и показать сырой текст пользователю без вариантов ответа.
//
// Было 3 — рассчитано на быстрый локальный Claude CLI. Живой прогон с
// remote-агентом через type: openai-agent (GLM, HTTP round-trip + reasoning
// на каждый ход) показал: агент сам корректно диагностировал и исправил
// невалидный JSON (не экранированную кавычку внутри markdown-контента
// вопроса), но это заняло заметно больше 3×MalformedNudgeTimeout — afm уже
// сдался и показал сырой текст человеку раньше, чем агент успел
// самостоятельно восстановиться.
const maxMalformedRetries = 5

// MalformedNudgeTimeout — сколько ждать реакции агента на один "перепиши
// JSON"-нудж, прежде чем считать этот раунд ретрая исчерпанным и пробовать
// следующий (или сдаться, если ретраи кончились). Читается один раз в New()
// и фиксируется в Orchestrator.malformedNudgeTimeout — тот же паттерн, что
// у RetryBackoff/MaxRetries (retry.go): тесты могут переопределять значение,
// но обязаны делать это ДО New().
//
// Без этого таймаута агент, который вообще не реагирует на нудж (падает,
// игнорирует, зависает) навсегда останавливал бы retry-автомат:
// unblockRewrittenMalformedQuestions раньше снимала блокировку ТОЛЬКО когда
// видела, что агент реально переписал файл — не отвечающий агент никогда не
// меняет содержимое, значит ключ никогда не разблокировался бы и
// maxMalformedRetries никогда бы не достигался.
//
// Было 10s — верно для локального Claude CLI, но слишком мало для
// remote-агентов (type: openai-agent/openai, напр. GLM) — один ход там это
// полный HTTP round-trip плюс reasoning, легко занимающий десятки секунд,
// особенно на большом (десятки КБ) payload вопроса. Поднято до 30s.
var MalformedNudgeTimeout = 30 * time.Second

// malformedQuestionState отслеживает прогресс ретраев для одного
// (stageID,phase,id), чей question.json не парсится. Живёт всё время работы
// поллинг-горутины, параллельно с `processed` (см. startQuestionPoller).
type malformedQuestionState struct {
	lastRaw  []byte    // сырые байты, увиденные на предыдущем тике для этого ключа
	retries  int       // сколько "перепиши JSON" уже отправлено агенту (0..maxMalformedRetries)
	nudgedAt time.Time // когда отправлен последний нудж (нулевое значение — нуджа ещё не было)
}

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
		processed := map[string]bool{}                    // "stageID|phase|id" → true
		malformed := map[string]*malformedQuestionState{} // "stageID|phase|id" → retry state
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				o.pollQuestions(processed, malformed)
			}
		}
	}()
}

// pollQuestions scans each active stage directory for unanswered question files.
// For each new file: writes it to dialog.jsonl (for UI history) and publishes
// EventAskUser to transition the stage to awaiting_user_input.
func (o *Orchestrator) pollQuestions(processed map[string]bool, malformed map[string]*malformedQuestionState) {
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
		stage := o.graph.Stage(stageID)
		if stage != nil && stage.Interactive {
			// A rewrite in response to an earlier nudge (see
			// handleMalformedQuestion) makes the file's content change but
			// leaves our synthetic answer.json sitting on disk — without
			// removing it here, FindUnansweredQuestions below would keep
			// treating this id as "already answered" forever and never see
			// the rewritten content.
			o.unblockRewrittenMalformedQuestions(malformed, stageID, stageDir)
		}
		questions, err := mcp.FindUnansweredQuestions(stageDir)
		if err != nil {
			continue
		}
		// Forget any previously-processed key for this stage that is no longer
		// in the current unanswered set — it must have been answered (dropped
		// out of FindUnansweredQuestions once its answer.json appeared).
		// Without this, `processed` never releases a key: the prompt tells
		// agents to never reuse a question id within a phase, but a real
		// agent (goga-brainstorm's revision loop) did reuse the same id for a
		// second, distinct question after the first was answered — and the
		// stale `processed[key] == true` silently swallowed it forever, with
		// no EvAskUser, no dialog entry, and no visible symptom beyond the
		// stage going idle. Restarting the browser can't fix this (the map is
		// server-side, in-process); only a full afm restart happened to clear
		// it. See TestPollQuestions_ReusedIDAfterAnswerAsksAgain.
		unanswered := make(map[string]bool, len(questions))
		for _, q := range questions {
			unanswered[stageID+"|"+q.Phase+"|"+q.ID] = true
		}
		prefix := stageID + "|"
		for key := range processed {
			if strings.HasPrefix(key, prefix) && !unanswered[key] {
				delete(processed, key)
			}
		}
		for _, q := range questions {
			key := stageID + "|" + q.Phase + "|" + q.ID

			// Malformed question.json (unparseable even after jsonrepair):
			// never surface it directly. A single failed parse is frequently
			// a torn read racing the agent's still-in-flight Write call, not
			// a genuine mistake — see handleMalformedQuestion for the full
			// retry state machine. Only applies to interactive stages: a
			// non-interactive stage's existing auto-answer path already
			// tolerates missing structure (PickAutoAnswer falls back to a
			// fixed text when there are no options), so it isn't worth the
			// extra retry machinery.
			if q.Malformed {
				if stage != nil && stage.Interactive {
					o.handleMalformedQuestion(malformed, stageID, stageDir, q, key)
				}
				continue
			}

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
			if stage != nil && !stage.Interactive {
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
				stagefiles.AppendNotice(o.opts.RunDir, stageID, string(bus.EventAutoAnswered), map[string]any{
					keyID: q.ID, keyPhase: q.Phase, "answer": answer, "from_options": fromOptions,
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
			if stage != nil && stage.Interactive {
				o.relocateMisplacedQuestions(stageID, stageDir, activeInteractiveCount <= 1)
			}
		}
	}
}

// unblockRewrittenMalformedQuestions checks every malformed-retry key
// belonging to stageID that is currently mid-nudge (retries > 0 — afm already
// wrote a synthetic "please rewrite" answer and is waiting for the agent to
// act). It removes the stale synthetic answer.json — freeing the id for
// FindUnansweredQuestions to see again this same tick — once EITHER:
//   - the agent has rewritten question.json since our last observation (a
//     correction under the SAME id, per the "never reuse an id" prompt rule —
//     without this, it would stay invisible forever, exactly like the
//     id-reuse bug this poller already had to learn to forget, see the
//     `processed` pruning above); or
//   - MalformedNudgeTimeout has elapsed with no rewrite at all — an agent
//     that never responds to a nudge (crashed, ignored it, hung) would
//     otherwise leave this key stuck waiting for a content change that will
//     never come, and maxMalformedRetries would never be reached.
func (o *Orchestrator) unblockRewrittenMalformedQuestions(malformed map[string]*malformedQuestionState, stageID, stageDir string) {
	prefix := stageID + "|"
	for key, st := range malformed {
		if st.retries == 0 || !strings.HasPrefix(key, prefix) {
			continue
		}
		phase, id, ok := strings.Cut(strings.TrimPrefix(key, prefix), "|")
		if !ok {
			continue
		}
		qPath := filepath.Join(stageDir, phase+"."+id+".question.json")
		raw, err := os.ReadFile(qPath)
		if err != nil {
			continue
		}
		changed := string(raw) != string(st.lastRaw)
		if !changed && time.Since(st.nudgedAt) < o.malformedNudgeTimeout {
			continue // still within the response window, nothing changed yet
		}
		_ = os.Remove(filepath.Join(stageDir, phase+"."+id+".answer.json"))
		if changed && mcp.CanParseQuestion(raw) {
			// Fixed. Stop tracking this key entirely — from here on
			// FindUnansweredQuestions handles it as a completely normal
			// question. Leaving the entry behind would make this same
			// "content changed since lastRaw" check fire on every future
			// tick (raw is now permanently different from the stale broken
			// lastRaw) and delete any LATER, unrelated real answer.json the
			// moment a human actually answers the now-valid question — found
			// live in a real browser run, not by inspection: the recovery
			// path worked, but the very next legitimate answer.json the
			// human submitted was silently deleted before the agent's bash
			// loop could read it, hanging the stage.
			delete(malformed, key)
		} else {
			// Still broken (either the rewrite didn't fix it, or there was no
			// rewrite at all and we're only here because of the timeout).
			// Refresh the baseline — a no-op if unchanged — so
			// handleMalformedQuestion immediately sees "stable, still broken"
			// and starts the next retry round instead of granting a
			// redundant grace tick.
			st.lastRaw = raw
		}
	}
}

// handleMalformedQuestion advances the retry state machine for a
// question.json that failed to parse even after jsonrepair. A single failed
// parse is NOT evidence of a genuine agent mistake — found via a real
// production log: afm's poller can read a file while the agent's Write tool
// call is still landing on disk (a torn read), producing bytes that fail to
// parse and never change again on their own — except that in the real
// incident, the SAME bytes parsed perfectly on the very next tick, once the
// write actually finished. So:
//   - First time this content is seen as broken: remember it, do nothing —
//     if it was a torn read, the next tick's re-read is already complete and
//     parses fine with zero agent involvement.
//   - Same broken content again on a later tick (the write is genuinely
//     done): nudge the agent, up to maxMalformedRetries times, via the exact
//     file-based channel its own bash polling loop already reads
//     (<phase>.<id>.answer.json) — no new protocol needed.
//   - Still broken after exhausting retries: stop asking for valid JSON,
//     persist a real parseable stub, and only now run the normal
//     ask-the-user flow (see giveUpOnMalformedQuestion).
func (o *Orchestrator) handleMalformedQuestion(malformed map[string]*malformedQuestionState, stageID, stageDir string, q mcp.QuestionFile, key string) {
	qPath := filepath.Join(stageDir, q.Phase+"."+q.ID+".question.json")
	raw, err := os.ReadFile(qPath)
	if err != nil {
		return
	}

	st, seen := malformed[key]
	if !seen {
		malformed[key] = &malformedQuestionState{lastRaw: raw}
		return // grace tick: give a possibly-still-writing file one more pass
	}
	if string(raw) != string(st.lastRaw) {
		// Still changing — slow write, or the agent just rewrote it in
		// response to a nudge. Wait one more tick before judging again.
		st.lastRaw = raw
		return
	}

	// Same broken bytes as last tick: the write is done, this is genuinely
	// broken, not a race.
	if st.retries >= maxMalformedRetries {
		o.giveUpOnMalformedQuestion(stageID, stageDir, q, qPath, raw)
		delete(malformed, key)
		return
	}

	st.retries++
	st.nudgedAt = time.Now()
	msg := fmt.Sprintf(
		"⚠️ afm: файл вопроса %s.%s.question.json содержит невалидный JSON и не может быть обработан (попытка %d из %d). "+
			"Это не ответ пользователя — прочитайте и перепишите этот же файл корректным JSON (тот же id %q), "+
			"затем снова дождитесь %s.%s.answer.json.",
		q.Phase, q.ID, st.retries, maxMalformedRetries, q.ID, q.Phase, q.ID,
	)
	if err := mcp.WriteInternalAnswer(stageDir, q.Phase, q.ID, msg); err != nil {
		log.Printf("WARN: malformed-question nudge %s/%s.%s: %v", stageID, q.Phase, q.ID, err)
		return
	}
	o.ui.Publish(bus.Event{
		Type:    bus.EventAutoAnswered,
		StageID: stageID,
		Data: map[string]any{
			keyID: q.ID, keyPhase: q.Phase, "answer": msg, "from_options": false,
		},
	})
	stagefiles.AppendNotice(o.opts.RunDir, stageID, string(bus.EventAutoAnswered), map[string]any{
		keyID: q.ID, keyPhase: q.Phase, "answer": msg, "from_options": false,
	})
}

// giveUpOnMalformedQuestion runs once retries are exhausted: persists a
// real, parseable stub to disk — handleDialogAnswer (pkg/server/handlers.go)
// re-parses question.json strictly on every answer submission, so leaving
// broken JSON in place would 500 forever — and surfaces it to the user with
// no options, free text only; whatever they type becomes the literal answer.
func (o *Orchestrator) giveUpOnMalformedQuestion(stageID, stageDir string, q mcp.QuestionFile, qPath string, raw []byte) {
	explanation := fmt.Sprintf(
		"⚠️ Агент %d раз(а) подряд не смог записать корректный JSON для этого вопроса. Показан необработанный текст файла — ответьте свободным текстом.\n\nСодержимое файла:\n%s",
		maxMalformedRetries, string(raw),
	)
	stub := struct {
		ID          string `json:"id"`
		Question    string `json:"question"`
		AllowCustom bool   `json:"allow_custom"`
	}{ID: q.ID, Question: explanation, AllowCustom: true}
	data, err := json.Marshal(stub)
	if err != nil {
		log.Printf("WARN: marshal malformed-question stub %s/%s.%s: %v", stageID, q.Phase, q.ID, err)
		return
	}
	if err := os.WriteFile(qPath, data, 0644); err != nil {
		log.Printf("WARN: persist malformed-question stub %s/%s.%s: %v", stageID, q.Phase, q.ID, err)
		return
	}

	dialogPath := filepath.Join(stageDir, q.Phase+".dialog.jsonl")
	if e, _ := mcp.FindEntry(dialogPath, q.ID); e == nil {
		_ = mcp.AppendQuestion(dialogPath, mcp.Question{ID: q.ID, Question: explanation, AllowCustom: true})
	}
	o.preAskPhase.Store(stageID, o.correctPhaseForState(o.currentStatus(stageID), q.Phase))
	_, seq, _ := o.triggerWithSeq(stageID, bus.EvAskUser, bus.GuardCtx{Phase: q.Phase}, "")
	o.ui.Publish(bus.Event{
		Type:    bus.EventAskUser,
		StageID: stageID,
		Data: map[string]any{
			keyID: q.ID, keyPhase: q.Phase, "question": explanation,
			"options": []string(nil), "allow_custom": true,
		},
		Seq: seq,
	})
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
