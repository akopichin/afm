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

// maxJSONFixAttempts — сколько раз afm запускает свежий изолированный агент
// для починки question.json, который не парсится даже после jsonrepair,
// прежде чем сдаться (interactive-стадия → показать сырой текст человеку;
// non-interactive → авто-ответить fallback'ом, чтобы стадия не зависла).
//
// Каждая попытка — это полный запуск отдельного агентского процесса с чистым
// контекстом и единственной задачей «почини этот один файл» (см.
// runJSONFixAgent). Такой агент существенно надёжнее прежнего in-context
// нуджа тому же агенту (он не отвлечён своей основной задачей и видит только
// битый файл), поэтому и попыток нужно меньше — но потолок оставляем на
// случай принципиально нечинибельного содержимого.
const maxJSONFixAttempts = 3

// malformedQuestionState отслеживает прогресс починки одного
// (stageID,phase,id), чей question.json не парсится. Живёт всё время работы
// поллинг-горутины, параллельно с `processed` (см. startQuestionPoller).
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
		// Once a fix-agent (or the original agent) has made a tracked
		// question.json valid again, stop tracking it — from here it flows
		// through the normal question path. Applies to every stage type: the
		// fresh-agent repair mechanism is no longer interactive-only.
		o.reconcileMalformedFixes(malformed, stageID, stageDir)
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
			// repair state machine (torn-read grace → fresh fix-agent →
			// terminal fallback). Applies to EVERY stage type: a malformed
			// file cannot be parsed, so a non-interactive stage's auto-answer
			// path (which needs parsed options) can't run on it either — the
			// old "interactive-only" gate here left non-interactive stages
			// hanging forever on an unparseable question.
			if q.Malformed {
				if stage != nil {
					o.handleMalformedQuestion(malformed, stageID, stageDir, q, key)
				}
				continue
			}

			if processed[key] {
				continue
			}
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
			// FSM status is left untouched (no EvAskUser transition) in the normal
			// case where the agent is still polling.
			if stage != nil && !stage.Interactive {
				answer, fromOptions := mcp.PickAutoAnswer(q)
				if err := mcp.WriteAnswer(stageDir, q.Phase, q.ID, answer, fromOptions, true); err != nil {
					log.Printf("WARN: auto-answer %s/%s.%s: %v", stageID, q.Phase, q.ID, err)
					// НЕ помечаем processed: неудачная запись (диск / O_EXCL-гонка)
					// должна ретраиться на следующем тике, иначе стадия зависнет в
					// ожидании answer.json до idle-таймаута исполнителя (~30 мин).
					continue
				}
				processed[key] = true
				o.ui.Publish(bus.Event{
					Type:    bus.EventAutoAnswered,
					StageID: stageID,
					Data: map[string]any{
						keyID: q.ID, keyPhase: q.Phase, keyAnswer: answer, keyFromOptions: fromOptions,
					},
				})
				stagefiles.AppendNotice(o.opts.RunDir, stageID, string(bus.EventAutoAnswered), map[string]any{
					keyID: q.ID, keyPhase: q.Phase, keyAnswer: answer, keyFromOptions: fromOptions,
				})
				// Если агент уже вышел, оставив стадию запаркованной в
				// awaiting_user_input, написать answer.json недостаточно —
				// стадию надо вывести из этого статуса и перезапустить агента
				// (тем же путём, что human-ответ). В нормальном случае (агент
				// ещё жив и опрашивает answer.json) это no-op. См.
				// resumeAfterAnswer / TestPollQuestions_ParkedNonInteractiveStageIsUnparked.
				_ = o.resumeAfterAnswer(stageID, q.Phase, q.ID, answer, false)
				continue
			}
			processed[key] = true

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

// reconcileMalformedFixes drops every tracked malformed key for stageID whose
// question.json now parses (a fix-agent — or the original agent — repaired it)
// or no longer exists. Once dropped, the id flows through the normal question
// path (auto-answer for non-interactive, EvAskUser for interactive) on this
// same tick. Runs for every stage type, before FindUnansweredQuestions.
//
// Because the fresh-agent repair mechanism rewrites question.json in place and
// never writes a synthetic answer.json, there is nothing stale to clean up on
// disk here — a valid file simply stops being tracked. This is what makes the
// whole class of "stale tracking deletes a later real answer" bug (which the
// old nudge-based unblockRewrittenMalformedQuestions had to guard against)
// impossible by construction.
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

// handleMalformedQuestion advances the repair state machine for a
// question.json that failed to parse even after jsonrepair (the "fix via the
// library" step already happened inside FindUnansweredQuestions — reaching
// here means it failed). The unified mechanism, identical for interactive and
// non-interactive stages, is:
//
//   - Torn-read grace: a single failed parse is frequently the poller catching
//     the file mid-write, not a genuine mistake (root-caused byte-for-byte in a
//     real production log — the SAME bytes parsed perfectly one tick later once
//     the write finished). First sighting of broken content: remember it, do
//     nothing. If it was a torn read, the next tick re-reads valid bytes and we
//     never even get called (FindUnansweredQuestions no longer flags Malformed).
//   - Fresh fix-agent: same broken bytes on a second tick means the write is
//     genuinely done. Launch a fresh, clean-context agent whose only task is to
//     rewrite this one file as valid JSON (runJSONFixAgent), up to
//     maxJSONFixAttempts times. We wait (via the state's done channel) while an
//     agent is in flight; each finished-but-still-broken attempt spends one of
//     the attempts budget.
//   - Terminal fallback once attempts are exhausted: interactive stage → persist
//     a parseable stub and surface the raw text to a human (giveUpOnMalformedQuestion);
//     non-interactive stage → auto-answer a fallback so the stage never hangs
//     (autoAnswerMalformed).
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
		// A fix-agent is in flight. If it had already made the file valid,
		// FindUnansweredQuestions would no longer flag Malformed and we would
		// not be here — so being here means it is either still working or
		// finished without fixing it. Distinguish via the done channel.
		select {
		case <-st.done:
			st.done = nil // agent finished (still broken): fall through to next attempt / exhaustion
		default:
			st.lastRaw = raw
			return // agent still working — keep waiting
		}
	} else if string(raw) != string(st.lastRaw) {
		// Not mid-repair and the content is still changing between ticks — a
		// slow write settling. Refresh the baseline and grant one more grace
		// tick before treating it as stably broken.
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
	o.ui.Publish(bus.Event{
		Type:    bus.EventAutoAnswered,
		StageID: stageID,
		Data:    map[string]any{keyID: q.ID, keyPhase: q.Phase, keyAnswer: notice, keyFromOptions: false},
	})
	stagefiles.AppendNotice(o.opts.RunDir, stageID, string(bus.EventAutoAnswered), map[string]any{
		keyID: q.ID, keyPhase: q.Phase, keyAnswer: notice, keyFromOptions: false,
	})
}

// autoAnswerMalformed is the non-interactive terminal fallback: after the fix
// agents have all failed, the question is still unparseable, so its options
// can't be recovered. PickAutoAnswer with an empty question yields the standard
// "decide autonomously" fallback text; writing it as the answer unblocks the
// stage's agent (its bash loop is waiting on <phase>.<id>.answer.json) instead
// of leaving it polling forever. Mirrors the normal non-interactive auto-answer
// path (EventAutoAnswered + notices.jsonl), leaving the stage FSM untouched.
func (o *Orchestrator) autoAnswerMalformed(stageID, stageDir string, q mcp.QuestionFile) {
	answer, fromOptions := mcp.PickAutoAnswer(mcp.QuestionFile{ID: q.ID, Phase: q.Phase})
	if err := mcp.WriteAnswer(stageDir, q.Phase, q.ID, answer, fromOptions, true); err != nil {
		log.Printf("WARN: auto-answer malformed %s/%s.%s: %v", stageID, q.Phase, q.ID, err)
		return
	}
	o.ui.Publish(bus.Event{
		Type:    bus.EventAutoAnswered,
		StageID: stageID,
		Data:    map[string]any{keyID: q.ID, keyPhase: q.Phase, keyAnswer: answer, keyFromOptions: fromOptions},
	})
	stagefiles.AppendNotice(o.opts.RunDir, stageID, string(bus.EventAutoAnswered), map[string]any{
		keyID: q.ID, keyPhase: q.Phase, keyAnswer: answer, keyFromOptions: fromOptions,
	})
}

// giveUpOnMalformedQuestion runs once fix attempts are exhausted on an
// interactive stage: persists a real, parseable stub to disk —
// handleDialogAnswer (pkg/server/handlers.go) re-parses question.json strictly
// on every answer submission, so leaving broken JSON in place would 500 forever
// — and surfaces it to the user with no options, free text only; whatever they
// type becomes the literal answer.
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

// runJSONFixAgent launches a fresh, clean-context agent whose ONLY task is to
// rewrite one malformed question.json as valid JSON, and returns a channel
// closed when that agent finishes. This is the production value of
// Orchestrator.spawnJSONFix (tests inject a synchronous stub).
//
// Key isolation choices:
//   - Fresh session (SessionID/Resume unset) — the whole point is a clean
//     context untainted by the stage agent's history; unlike runnerFor, we
//     never resume.
//   - No StageDir/AFM_STAGE_DIR — the fix agent is NOT a dialog participant; it
//     only edits the single absolute-path file named in its prompt, so it can
//     never itself write a new question.json the poller would pick up.
//   - concurrency.SpawnDetached, not SpawnAgent — the stage's main agent is
//     blocked polling for the answer to this very question while holding the
//     stage's command semaphore slot; routing the fix agent through the same
//     semaphore would deadlock, and SpawnAgent's markActive would clobber the
//     main agent's active marker.
//   - Separate <phase>.<id>.jsonfix.log — keeps the fix agent's tool actions
//     out of the stage's own <phase>.jsonl (event feed / WrittenFiles).
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
	// Every afm agent command is claude-stream-json compatible (claude itself,
	// autoShim wrappers, openai(-agent)-as-claude, …); the default flags are
	// required to run non-interactively (--print) and are safe everywhere, the
	// same assumption runnerFor makes for interactive stages.
	cfg := executor.Config{
		Command:     cmd,
		ExtraArgs:   executor.ResolveArgs(extra),
		IdleTimeout: o.opts.Config.Executor.IdleTimeout,
		WrapperDir:  wrapperDirFor(cmd, o.opts.WrapperDir, o.opts.GeneratedAgents),
		Dir:         o.opts.RootDir,
		Debug:       o.opts.Debug,
		RunDir:      o.opts.RunDir,
		StageID:     s.ID,
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
