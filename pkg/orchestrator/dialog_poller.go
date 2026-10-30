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
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// maxJSONFixAttempts — сколько раз afm запускает свежий изолированный агент для починки question.json,
// который не парсится даже после jsonrepair, прежде чем сдаться.
const maxJSONFixAttempts = 3

// malformedQuestionState отслеживает прогресс починки одного, чей question.json не парсится. Живёт всё
// время работы поллинг-горутины, параллельно с `processed` (см. startQuestionPoller).
type malformedQuestionState struct {
	lastRaw  []byte          // сырые байты, увиденные на предыдущем тике (torn-read grace + детект перезаписи)
	attempts int             // сколько свежих fix-агентов уже запущено (0..maxJSONFixAttempts)
	done     <-chan struct{} // != nil, пока fix-агент в полёте; закрывается по его завершении
}

// violationCacheEntry хранит stat-данные для одного .jsonl файла.
// Используется в detectDialogViolation чтобы не перечитывать неизменившиеся файлы.
type violationCacheEntry struct {
	size  int64
	mtime time.Time
}

// startQuestionPoller launches a goroutine that scans active stage directories every second for new
// *.question.json files.
func (o *Orchestrator) startQuestionPoller(ctx context.Context) {
	o.pollerDone = make(chan struct{})
	go func() {
		defer close(o.pollerDone)
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
func (o *Orchestrator) pollQuestions(processed map[string]bool, malformed map[string]*malformedQuestionState) {
	snap := o.opts.Store.Snapshot()

	// activeInteractiveCount считает interactive-стадии, активные прямо сейчас.
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
		// Once a fix-agent has made a tracked question.json valid again, stop tracking it — from here it flows
		// through the normal question path.
		o.reconcileMalformedFixes(malformed, stageID, stageDir)
		questions, err := mcp.FindUnansweredQuestions(stageDir)
		if err != nil {
			continue
		}
		// Serialize interactive dialogs: an agent's polling loop blocks on the FIRST question it wrote, so afm
		// must process only the oldest unanswered question and hold the rest.
		var interactiveCurrent *mcp.QuestionFile
		if stage != nil && stage.Interactive {
			if cur, ok := mcp.SelectCurrentQuestion(questions); ok {
				c := cur
				interactiveCurrent = &c
			}
		}
		// Forget answered keys so the agent can reuse a question ID.
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
			// Hold back every interactive question that is not the current one. Not marked processed, so it is
			// re-evaluated each tick and surfaced once it becomes current.
			if interactiveCurrent != nil &&
				(q.Phase != interactiveCurrent.Phase || q.ID != interactiveCurrent.ID) {
				continue
			}

			key := stageID + "|" + q.Phase + "|" + q.ID

			// Malformed question.json: never surface it directly.
			if q.Malformed {
				if stage != nil {
					o.handleMalformedQuestion(malformed, stageID, stageDir, q, key)
				}
				continue
			}

			if processed[key] {
				continue
			}
			// Write to dialog.jsonl for history. FindEntry reflects that re-opening, so the next tick sees an
			// unanswered entry and does not append a duplicate.
			dialogPath := mcp.DialogPath(stageDir, q.Phase)
			if e, _ := mcp.FindEntry(dialogPath, q.ID); e == nil || e.Answer != nil {
				_ = mcp.AppendQuestion(dialogPath, mcp.Question{
					ID:          q.ID,
					Question:    q.Question,
					Options:     q.Options,
					AllowCustom: q.AllowCustom,
				})
			}

			// Non-interactive stage: afm answers the question itself instead of surfacing it to a human — the
			// stage's FSM status is left untouched in the normal case where the agent is still polling.
			if stage != nil && !stage.Interactive {
				if o.reviewTxnActive() {
					// Leave questions unanswered during review-pause; retry after the reviewer releases the flow.
					continue
				}
				// Гейт снимаем ДО WriteAnswer: паркинг-стадию покрывает FSM-путь — не-FSM пара ниже эмитится ТОЛЬКО
				// когда стадия НЕ была запаркована, иначе stage_question_asked/answered задваивались бы.
				parked := o.currentStatus(stageID) == state.StatusAwaitingUserInput
				answer, fromOptions := mcp.PickAutoAnswer(q)
				if err := mcp.WriteAnswer(stageDir, q.Phase, q.ID, answer, fromOptions, true); err != nil {
					log.Printf("WARN: auto-answer %s/%s.%s: %v", stageID, q.Phase, q.ID, err)
					// НЕ помечаем processed: неудачная запись (диск / O_EXCL-гонка) должна ретраиться на следующем тике,
					// иначе стадия зависнет в ожидании answer.json до idle-таймаута исполнителя (~30 мин).
					continue
				}
				processed[key] = true
				// Публикуем dialog_question ПЕРЕД auto_answered — лента должна показать и вопрос, и авто-ответ, оба
				// навигируемыми в полный диалог, в правильном порядке.
				o.publishDialogQuestion(stageID, q.Phase, q.ID, q.Question)
				o.publishNotice(bus.Event{
					Type:    bus.EventAutoAnswered,
					StageID: stageID,
					Data:    mcp.AutoAnsweredNotice(q.Phase, q.ID, answer, fromOptions, mcp.DialogSnippet(q.Question)),
				})
				if !parked {
					o.emitLifecycle(lifecyclehooks.Event{
						Type:      lifecyclehooks.EventStageQuestionAsked,
						StageID:   stageID,
						StageName: o.stageName(stageID),
						Phase:     q.Phase,
						Reason:    q.Question,
						Key:       q.Phase + "/" + q.ID,
					})
					o.emitLifecycle(lifecyclehooks.Event{
						Type:      lifecyclehooks.EventStageQuestionAnswered,
						StageID:   stageID,
						StageName: o.stageName(stageID),
						Phase:     q.Phase,
						Reason:    answer,
						Key:       q.Phase + "/" + q.ID,
					})
				}
				// Если агент уже вышел, оставив стадию запаркованной в awaiting_user_input, написать answer.json
				// недостаточно — стадию надо вывести из этого статуса и перезапустить агента.
				_ = o.resumeAfterAnswer(stageID, q.Phase, q.ID, answer, false)
				continue
			}
			// Триггерим ПЕРЕД публикацией ask_user, чтобы приложить к событию реальный seq этой transition — фронт
			// дедуплицирует по нему live- событие с историческим двойником из /api/events.
			seq, move := o.surfaceQuestion(stageID, q.Phase, q.ID)
			if move == questionDeferred {
				continue // не processed: следующий скан спросит, когда стадия сможет уйти к пользователю
			}
			processed[key] = true
			// Первое всплытие ЭТОГО вопроса пользователю — ровно тот же гейт, что уже гарантирует единственность
			// EventAskUser.
			o.publishDialogQuestion(stageID, q.Phase, q.ID, q.Question)
			if move == questionAnswered {
				continue
			}
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
		// No open question in stageDir: if this is an interactive stage, check whether the agent wrote one
		// elsewhere. Auto-relocate the misplaced file so the dialog becomes visible in the UI.
		if len(questions) == 0 {
			if stage != nil && stage.Interactive {
				o.relocateMisplacedQuestions(stageID, stageDir, activeInteractiveCount <= 1)
			}
		}
	}
}

// questionMove — что surfaceQuestion сделал с вопросом, найденным сканом открытым.
type questionMove int

const (
	questionAsked    questionMove = iota // стадия отдана пользователю по этому вопросу
	questionAnswered                     // на вопрос уже ответили после скана: никого не спрашиваем
	questionDeferred                     // стадия сейчас не может уйти к пользователю (её держит feedback-запуск Revise)
)

// surfaceQuestion отдаёт стадию пользователю по вопросу id, если на него не ответили после скана: решение
// принимается под answerMu, поэтому ответ либо применяется после этого перехода, либо виден ему.
func (o *Orchestrator) surfaceQuestion(stageID, phase, id string) (seq uint64, move questionMove) {
	o.answerMu.Lock()
	defer o.answerMu.Unlock()
	if !o.questionOpen(stageID, phase, id) {
		return 0, questionAnswered
	}
	// Сохраняем реальную фазу ДО перехода в awaiting_user_input.
	askPhase := o.correctPhaseForState(o.currentStatus(stageID), phase)
	o.preAskPhase.Store(stageID, askPhase)
	if _, seq, ok := o.triggerWithSeq(stageID, bus.EvAskUser, bus.GuardCtx{Phase: phase}, ""); ok {
		return seq, questionAsked
	}
	if o.currentStatus(stageID) == state.StatusAwaitingUserInput {
		return 0, questionAsked // стадия уже у пользователя по прежнему вопросу: этот задаётся рядом
	}
	o.preAskPhase.CompareAndDelete(stageID, askPhase)
	return 0, questionDeferred
}

func (o *Orchestrator) questionOpen(stageID, phase, id string) bool {
	questions, err := mcp.FindUnansweredQuestions(filepath.Join(o.opts.RunDir, stageID))
	if err != nil {
		return false
	}
	for _, q := range questions {
		if q.Phase == phase && q.ID == id {
			return true
		}
	}
	return false
}

// publishDialogQuestion publishes one identical payload live and to notices.jsonl for history/live
// deduplication.
func (o *Orchestrator) publishDialogQuestion(stageID, phase, id, questionText string) {
	payload := mcp.DialogFeedNotice(phase, id, mcp.DialogSnippet(questionText))
	o.publishNotice(bus.Event{Type: bus.EventDialogQuestion, StageID: stageID, Data: payload})
}

// reconcileMalformedFixes drops every tracked malformed key for stageID whose question.json now parses or
// no longer exists. Runs for every stage type, before FindUnansweredQuestions.
func (o *Orchestrator) reconcileMalformedFixes(malformed map[string]*malformedQuestionState, stageID, stageDir string) {
	prefix := stageID + "|"
	for key := range malformed {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		phase, id, ok := strings.Cut(strings.TrimPrefix(key, prefix), "|")
		if !ok {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(stageDir, phase+"."+id+".question.json"))
		if err != nil || mcp.CanParseQuestion(raw) {
			delete(malformed, key)
		}
	}
}

// handleMalformedQuestion advances the repair state machine for a question.json that failed to parse even
// after jsonrepair. First sighting of broken content: remember it, do nothing.
func (o *Orchestrator) handleMalformedQuestion(malformed map[string]*malformedQuestionState, stageID, stageDir string, q mcp.QuestionFile, key string) {
	stage := o.graph.Stage(stageID)
	if stage == nil {
		return
	}
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

	if st.done != nil {
		// A fix-agent is in flight. Distinguish via the done channel.
		select {
		case <-st.done:
			st.done = nil // agent finished (still broken): fall through to next attempt / exhaustion
		default:
			st.lastRaw = raw
			return // agent still working — keep waiting
		}
	} else if string(raw) != string(st.lastRaw) {
		// Not mid-repair and the content is still changing between ticks — a slow write settling. Refresh the
		// baseline and grant one more grace tick before treating it as stably broken.
		st.lastRaw = raw
		return
	}

	st.lastRaw = raw
	if st.attempts >= maxJSONFixAttempts {
		if stage.Interactive {
			o.giveUpOnMalformedQuestion(stageID, stageDir, q, qPath, raw)
		} else {
			o.autoAnswerMalformed(stageID, stageDir, q)
		}
		delete(malformed, key)
		return
	}

	st.attempts++
	st.done = o.spawnJSONFix(*stage, q.Phase, q.ID)
	notice := fmt.Sprintf(
		"⚙️ afm: question.json %s.%s не парсится даже после jsonrepair — запущен отдельный агент для починки JSON (попытка %d из %d).",
		q.Phase, q.ID, st.attempts, maxJSONFixAttempts,
	)
	o.publishNotice(bus.Event{
		Type:    bus.EventAutoAnswered,
		StageID: stageID,
		Data:    map[string]any{keyID: q.ID, keyPhase: q.Phase, keyAnswer: notice, keyFromOptions: false},
	})
}

// autoAnswerMalformed is the non-interactive terminal fallback: after the fix agents have all failed, the
// question is still unparseable, so its options can't be recovered.
func (o *Orchestrator) autoAnswerMalformed(stageID, stageDir string, q mcp.QuestionFile) {
	// Тот же гейт против дублей, что и в обычной non-interactive ветке
	// pollQuestions: снимаем ДО WriteAnswer, паркинг-стадию покрывает FSM-путь.
	parked := o.currentStatus(stageID) == state.StatusAwaitingUserInput
	answer, fromOptions := mcp.PickAutoAnswer(mcp.QuestionFile{ID: q.ID, Phase: q.Phase})
	// Оригинальный question.json так и не распарсился — реального текста вопроса нет.
	syntheticQuestion := mcp.DialogSnippet(fmt.Sprintf("question %s (unreadable — invalid JSON)", q.ID))
	dialogPath := mcp.DialogPath(stageDir, q.Phase)
	if e, _ := mcp.FindEntry(dialogPath, q.ID); e == nil {
		_ = mcp.AppendQuestion(dialogPath, mcp.Question{ID: q.ID, Question: syntheticQuestion})
	}
	if err := mcp.WriteAnswer(stageDir, q.Phase, q.ID, answer, fromOptions, true); err != nil {
		log.Printf("WARN: auto-answer malformed %s/%s.%s: %v", stageID, q.Phase, q.ID, err)
		return
	}
	o.publishDialogQuestion(stageID, q.Phase, q.ID, syntheticQuestion)
	o.publishNotice(bus.Event{
		Type:    bus.EventAutoAnswered,
		StageID: stageID,
		Data:    mcp.AutoAnsweredNotice(q.Phase, q.ID, answer, fromOptions, syntheticQuestion),
	})
	if !parked {
		o.emitLifecycle(lifecyclehooks.Event{
			Type:      lifecyclehooks.EventStageQuestionAsked,
			StageID:   stageID,
			StageName: o.stageName(stageID),
			Phase:     q.Phase,
			Reason:    q.Question,
			Key:       q.Phase + "/" + q.ID,
		})
		o.emitLifecycle(lifecyclehooks.Event{
			Type:      lifecyclehooks.EventStageQuestionAnswered,
			StageID:   stageID,
			StageName: o.stageName(stageID),
			Phase:     q.Phase,
			Reason:    answer,
			Key:       q.Phase + "/" + q.ID,
		})
	}
	// Если агент уже вышел в awaiting_user_input, после записи ответа выводим стадию из ожидания и
	// перезапускаем агента.
	_ = o.resumeAfterAnswer(stageID, q.Phase, q.ID, answer, false)
}

// giveUpOnMalformedQuestion persists a parseable free-text stub and emits its feed notice immediately
// after repair attempts fail.
func (o *Orchestrator) giveUpOnMalformedQuestion(stageID, stageDir string, q mcp.QuestionFile, qPath string, raw []byte) {
	explanation := fmt.Sprintf(
		"⚠️ Отдельный агент %d раз(а) подряд не смог записать корректный JSON для этого вопроса. Показан необработанный текст файла — ответьте свободным текстом.\n\nСодержимое файла:\n%s",
		maxJSONFixAttempts, string(raw),
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

	dialogPath := mcp.DialogPath(stageDir, q.Phase)
	if e, _ := mcp.FindEntry(dialogPath, q.ID); e == nil {
		_ = mcp.AppendQuestion(dialogPath, mcp.Question{ID: q.ID, Question: explanation, AllowCustom: true})
	}
	seq, move := o.surfaceQuestion(stageID, q.Phase, q.ID)
	if move == questionDeferred {
		return // заглушка теперь валидный вопрос: следующий скан спросит, когда стадия сможет уйти к пользователю
	}
	// Emit dialog_question HERE, on the give-up tick itself — do not wait for the stub to flow through
	// pollQuestions' normal branch on the NEXT tick.
	o.publishDialogQuestion(stageID, q.Phase, q.ID, explanation)
	if move == questionAnswered {
		return
	}
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

// runJSONFixAgent launches a fresh, clean-context agent whose ONLY task is to rewrite one malformed
// question.json as valid JSON, and returns a channel closed when that agent finishes.
const phaseJSONFix = "json_fix"

func (o *Orchestrator) runJSONFixAgent(s flow.Stage, phase, id string) <-chan struct{} {
	done := make(chan struct{})
	ctx := o.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	qPath := filepath.Join(stageDir, phase+"."+id+".question.json")
	logFile := filepath.Join(stageDir, phase+"."+id+".jsonfix.log")

	cmd := s.Command
	var extra []string
	if cmd == "" {
		cmd = o.opts.Config.Client.Command
		extra = o.opts.Config.Client.ExtraArgs
	}
	// All agent commands accept Claude stream-json flags; --print is required for non-interactive execution.
	cfg := executor.Config{
		Command:     cmd,
		ExtraArgs:   executor.ResolveArgs(extra),
		IdleTimeout: o.opts.Config.Executor.IdleTimeout,
		WrapperDir:  executor.WrapperDirFor(cmd, o.opts.WrapperDir, o.opts.GeneratedAgents),
		Dir:         o.opts.RootDir,
		Debug:       o.opts.Debug,
		RunDir:      o.opts.RunDir,
		StageID:     s.ID,
		Phase:       phaseJSONFix,
		UsageHint:   o.usageHintFor(cmd),
		OnUsage:     o.recordUsage(s.ID, phaseJSONFix, ""),
		Reasoning:   o.opts.Config.Client.Reasoning,
	}
	ex := executor.New(cfg)
	prompt := buildJSONFixPrompt(qPath, id)

	o.concurrency.SpawnDetached(ctx, func(ctx context.Context) {
		defer close(done)
		if err := ex.RunAgent(ctx, "jsonfix", s.Name, prompt, logFile); err != nil {
			log.Printf("WARN: json-fix agent %s/%s.%s: %v", s.ID, phase, id, err)
		}
	})
	return done
}

// buildJSONFixPrompt is the narrow, single-purpose instruction for the JSON
// fix agent. It deliberately forbids anything except repairing the one file.
func buildJSONFixPrompt(qPath, id string) string {
	return fmt.Sprintf(`Your ONLY task is to repair one malformed JSON file. Do nothing else.

File (absolute path): %s

This file must contain a single JSON object for afm's file-based dialog protocol.
It is currently NOT valid JSON. Read it, fix ONLY the JSON syntax (unescaped
quotes, raw newlines inside string values, trailing commas, and similar), and
overwrite the SAME file with valid JSON.

Rules:
- Keep the id field exactly %q.
- Preserve the meaning and text of every field (question, options,
  allow_custom). Fix syntax only — do not rewrite, summarize, shorten, or
  translate the content.
- The result MUST parse as exactly one JSON object.
- Do NOT create any other file, do NOT ask any question, do NOT write anything
  to disk except the corrected content of this exact file.

Before finishing, verify it parses:
  cat %q | python3 -c 'import json,sys; json.load(sys.stdin)'
This command must exit 0.`, qPath, id, qPath)
}

// detectDialogViolation scans the agent's stream-json logs for a Write of a *.question.json file OUTSIDE
// the stage directory. Метод вызывается только из поллера — синхронизация не нужна.
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

// dialogPhases возвращает фазы, чьи диалоговые артефакты относятся к стадии: базовые
// planning/implementation/review плюс autonomous, если стадия исполняется в автономном треке.
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

// relocateScanInterval — throttle для скана root_dir внутри relocateMisplacedQuestions: это запасная сеть,
// а не основной путь, лишняя частота (раз в секунду, как основной poll-тик) не нужна.
const relocateScanInterval = 5 * time.Second

// activeDialogPhase возвращает фазу, чей <phase>.jsonl изменялся последним среди dialogPhases — это и есть
// фаза, в которой сейчас реально работает агент.
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

// relocateMisplacedQuestions исправляет неверный phase-префикс и размещение вопроса вне stageDir, сохраняя
// answer-путь агента.
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

// normalizeMisplacedQuestion переносит вопрос в <phase>.<id>.question.json и создаёт answer-симлинк по
// пути, опрашиваемому агентом.
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
	// Файл уже разрешён под СВОИМ именем — не трогаем.
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

// pathInside reports whether file is located inside dir.
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

// correctPhaseForState возвращает корректную фазу для возврата из awaiting_user_input, основываясь на
// текущем состоянии FSM, а не на фазе из имени файла вопроса.
func (o *Orchestrator) correctPhaseForState(current state.StageStatus, filePhase string) string {
	if current == state.StatusPlanning || current == state.StatusRevising {
		return phasePlanning
	}
	return filePhase
}

// popPreAskPhase читает и удаляет сохранённую фазу для стейджа. Если запись отсутствует (напр. перезапуск
// afm без перехода через EvAskUser), возвращает fallback — фазу из имени файла вопроса.
func (o *Orchestrator) popPreAskPhase(stageID, fallback string) string {
	if v, ok := o.preAskPhase.LoadAndDelete(stageID); ok {
		return v.(string)
	}
	return fallback
}
