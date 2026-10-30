package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/akopichin/afm/schema"
)

// maxJSONFixAttempts — сколько раз afm запускает свежий изолированный агент для починки question.json,
// который не парсится даже после jsonrepair, прежде чем сдаться.
const maxJSONFixAttempts = 3

// malformedQuestionState отслеживает прогресс починки одного, чей question.json не парсится. Живёт всё
// время работы поллинг-горутины, параллельно с `processed` (см. startQuestionPoller).
type questionFixOutcome struct {
	Err error
}

type malformedQuestionState struct {
	retryAfter time.Time
	lastRaw    []byte                    // сырые байты, увиденные на предыдущем тике (torn-read grace + детект перезаписи)
	attempts   int                       // сколько свежих fix-агентов уже запущено (0..maxJSONFixAttempts)
	done       <-chan questionFixOutcome // != nil, пока fix-агент в полёте; закрывается по его завершении
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
		ticker := o.clk().NewTicker(time.Second)
		defer ticker.Stop()
		processed := map[string]bool{}                    // "stageID|phase|id" → true
		malformed := map[string]*malformedQuestionState{} // "stageID|phase|id" → retry state
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C():
				o.pollQuestions(processed, malformed)
				if o.pollTickHook != nil {
					o.pollTickHook()
				}
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
		if !isDialogActive(st.Status) {
			continue
		}
		if stage := o.reg().Stage(id); stage != nil && stage.Interactive {
			activeInteractiveCount++
		}
	}

	for stageID, st := range snap.Stages {
		if !isDialogActive(st.Status) {
			continue
		}
		stageDir := filepath.Join(o.opts.RunDir, stageID)
		stage := o.reg().Stage(stageID)
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
				if o.autoAnswerQuestion(stageID, stageDir, q) {
					processed[key] = true
				}
				continue
			}
			if o.publishQuestionToUser(stageID, q) {
				processed[key] = true
			}
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

func isDialogActive(status state.StageStatus) bool {
	switch status {
	case state.StatusPlanning, state.StatusRunning, state.StatusRevising,
		state.StatusRetrying, state.StatusAwaitingUserInput:
		return true
	default:
		return false
	}
}

func (o *Orchestrator) autoAnswerQuestion(stageID, stageDir string, q mcp.QuestionFile) bool {
	if o.reviewTxnActive() {
		// Leave questions unanswered during review-pause; retry after the reviewer releases the flow.
		return false
	}
	// Гейт снимаем ДО WriteAnswer: паркинг-стадию покрывает FSM-путь — не-FSM пара ниже эмитится ТОЛЬКО
	// когда стадия НЕ была запаркована, иначе stage_question_asked/answered задваивались бы.
	parked := o.currentStatus(stageID) == state.StatusAwaitingUserInput
	answer, fromOptions := mcp.PickAutoAnswer(q)
	if err := mcp.WriteAnswer(stageDir, q.Phase, q.ID, answer, fromOptions, true); err != nil {
		log.Printf("WARN: auto-answer %s/%s.%s: %v", stageID, q.Phase, q.ID, err)
		// НЕ помечаем processed: неудачная запись (диск / O_EXCL-гонка) должна ретраиться на следующем тике,
		// иначе стадия зависнет в ожидании answer.json до idle-таймаута исполнителя (~30 мин).
		return false
	}
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
	return true
}

func (o *Orchestrator) publishQuestionToUser(stageID string, q mcp.QuestionFile) bool {
	// Триггерим ПЕРЕД публикацией ask_user, чтобы приложить к событию реальный seq этой transition — фронт
	// дедуплицирует по нему live- событие с историческим двойником из /api/events.
	seq, move := o.surfaceQuestion(stageID, q.Phase, q.ID)
	if move == questionDeferred {
		return false // a later scan asks it once the stage can be the user's
	}
	// Первое всплытие ЭТОГО вопроса пользователю — ровно тот же гейт, что уже гарантирует единственность
	// EventAskUser.
	o.publishDialogQuestion(stageID, q.Phase, q.ID, q.Question)
	if move == questionAnswered {
		return true
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
	return true
}

// questionMove is what surfaceQuestion did with a question a scan found open.
type questionMove int

const (
	questionAsked    questionMove = iota // the stage is the user's for this question
	questionAnswered                     // answered since the scan: nobody is asked
	questionDeferred                     // the stage cannot be the user's now (a Revise's feedback run owns it)
)

// surfaceQuestion moves the stage to the user for question id, unless it was answered since the scan: decided under
// answerMu, so an answer is either applied after this transition or seen by it.
func (o *Orchestrator) surfaceQuestion(stageID, phase, id string) (uint64, questionMove) {
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
		return 0, questionAsked // already the user's for an earlier question: this one is asked beside it
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
	stage := o.reg().Stage(stageID)
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
		case outcome := <-st.done:
			st.done = nil
			var limit *executor.UsageLimitError
			if errors.As(outcome.Err, &limit) {
				st.retryAfter = limit.ResetAt
				if !st.retryAfter.After(o.clk().Now()) {
					st.retryAfter = o.clk().Now().Add(o.opts.Config.UsageLimit.ResolvedProbeInterval())
				}
			} else {
				st.attempts++
			}
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
	if o.clk().Now().Before(st.retryAfter) {
		return
	}
	if st.attempts >= maxJSONFixAttempts {
		if stage.Interactive {
			o.giveUpOnMalformedQuestion(stageID, stageDir, q, qPath, raw)
		} else {
			o.autoAnswerMalformed(stageID, stageDir, q)
		}
		delete(malformed, key)
		return
	}

	st.done = o.spawnJSONFix(*stage, q.Phase, q.ID, st.attempts+1)
	notice := fmt.Sprintf(
		"⚙️ afm: question.json %s.%s не парсится даже после jsonrepair — запущен отдельный агент для починки JSON (попытка %d из %d).",
		q.Phase, q.ID, st.attempts+1, maxJSONFixAttempts,
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
	if committed, err := mcp.CommitQuestionRepair(qPath, raw, data); err != nil || !committed {
		if err != nil {
			log.Printf("WARN: persist malformed-question stub %s/%s.%s: %v", stageID, q.Phase, q.ID, err)
		}
		return
	}

	dialogPath := mcp.DialogPath(stageDir, q.Phase)
	if e, _ := mcp.FindEntry(dialogPath, q.ID); e == nil {
		_ = mcp.AppendQuestion(dialogPath, mcp.Question{ID: q.ID, Question: explanation, AllowCustom: true})
	}
	seq, move := o.surfaceQuestion(stageID, q.Phase, q.ID)
	if move == questionDeferred {
		return // the stub is a valid question now: a later scan asks it once the stage can be the user's
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

const phaseJSONFix = "json_fix"

// The formatter returns a candidate; only AFM may replace the still-current question.
func (o *Orchestrator) runJSONFixAgent(s flow.Stage, phase, id string, attempt int) <-chan questionFixOutcome {
	done := make(chan questionFixOutcome, 1)
	ctx := o.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	qPath := filepath.Join(stageDir, phase+"."+id+".question.json")
	raw, err := os.ReadFile(qPath)
	info, statErr := os.Stat(qPath)
	if err != nil || statErr != nil {
		done <- questionFixOutcome{Err: errors.Join(err, statErr)}
		close(done)
		return done
	}
	epoch := o.opts.Store.Snapshot().Stages[s.ID].Epoch
	cmd := s.Command
	if cmd == "" {
		cmd = o.opts.Config.Client.Command
	}
	cfg := o.outputFormatterConfig(cmd, s.ID)
	if s.Command == "" {
		cfg.ExtraArgs = executor.ResolveArgs(o.opts.Config.Client.ExtraArgs)
	}
	prefix := phase + "." + id + ".jsonfix-" + randomOpSuffix()
	o.concurrency.SpawnDetached(ctx, func(ctx context.Context) {
		outcome := questionFixOutcome{}
		defer func() { done <- outcome; close(done) }()
		owner := fmt.Sprintf("stage/%s/epoch/%d/question/%s/%s", s.ID, epoch, phase, id)
		formatter := o.outputFormatter(cfg, stageDir, prefix, "")
		recovered, recoveryErr := recoverOutput(ctx, schema.Question, owner, raw, questionOutputValidator, 1, func(ctx context.Context, contract schema.Contract, prompt string, _ int) ([]byte, error) {
			return formatter(ctx, contract, prompt, attempt)
		})
		outcome.Err = recoveryErr
		o.answerMu.Lock()
		defer o.answerMu.Unlock()
		current, readErr := os.ReadFile(qPath)
		currentInfo, infoErr := os.Stat(qPath)
		stage := o.opts.Store.Snapshot().Stages[s.ID]
		if ctx.Err() != nil || readErr != nil || infoErr != nil || !os.SameFile(info, currentInfo) || !info.ModTime().Equal(currentInfo.ModTime()) || !bytes.Equal(current, raw) || stage.Epoch != epoch || !isDialogActive(stage.Status) {
			return
		}
		if _, err := os.Stat(filepath.Join(stageDir, phase+"."+id+".answer.json")); !errors.Is(err, os.ErrNotExist) {
			return
		}
		if err := persistOutputRecovery(stageDir, prefix, recovered); err != nil {
			log.Printf("WARN: persist question recovery %s/%s.%s: %v", s.ID, phase, id, err)
			return
		}
		if recoveryErr != nil {
			return
		}
		if o.questionOutputCommitHook != nil {
			o.questionOutputCommitHook()
		}
		o.opts.Store.View(func(rs *state.RunState) {
			stage := rs.Stages[s.ID]
			if ctx.Err() != nil || stage.Epoch != epoch || !isDialogActive(stage.Status) {
				return
			}
			if _, err := mcp.CommitQuestionRepair(qPath, raw, recovered.Candidate); err != nil {
				log.Printf("WARN: commit question recovery %s/%s.%s: %v", s.ID, phase, id, err)
			}
		})
	})
	return done
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
		now := o.clk().Now()
		if !seen || now.Sub(last) >= relocateScanInterval {
			o.lastRootScan[stageID] = now
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
