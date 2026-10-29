package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// verifyOutcomeStillOwned — неатомарная проверка Pause/Revise перед обработкой verify; финальный fail
// защищает CAS EvVerifyFail.
func (o *Orchestrator) verifyOutcomeStillOwned(stageID string, onUserInterrupted func()) bool {
	switch o.currentStatus(stageID) {
	case state.StatusPaused:
		return false
	case state.StatusRevising:
		onUserInterrupted()
		return false
	default:
		return true
	}
}

// commitVerifyFailure — H1: атомарная фиксация verify-driven fail хода, начатого в epoch.
func (o *Orchestrator) commitVerifyFailure(stageID string, epoch uint64, reason string, onUserInterrupted func()) bool {
	if !o.verifyOutcomeStillOwned(stageID, onUserInterrupted) {
		return false
	}
	if o.verifyOutcomeGuardHook != nil {
		o.verifyOutcomeGuardHook(stageID)
	}
	if _, ok := o.Trigger(stageID, bus.EvVerifyFail, bus.GuardCtx{ExpectEpoch: &epoch}, reason); ok {
		return true
	}
	// CAS проиграл гонку: конкурентный Pause/Continue/Revise успел закоммититься МЕЖДУ fast-path чтением выше и этим
	// Trigger — стадия больше не наша.
	o.handOffRevise(stageID, epoch, onUserInterrupted)
	return false
}

// completeAgent публикует терминальный success стадии — durable-нотис EventAgentCompleted ПЛЮС
// critical-событие, из которого Run-цикл двигает FSM в completed. epoch — эпоха исполнения завершившегося хода
// (nil — без привязки): Run-цикл игнорирует завершение хода, который больше не владеет стадией.
func (o *Orchestrator) completeAgent(ctx context.Context, stageID, phase string, epoch *uint64) {
	stagefiles.AppendNotice(o.opts.RunDir, stageID, string(bus.EventAgentCompleted), phase)
	o.publishCritical(ctx, bus.Event{Type: bus.EventAgentCompleted, StageID: stageID, Data: phase, Epoch: epoch})
}

// agentAnswers marks the turn's agent as the one an answer goes to (answerToLiveAgent) until the turn decides.
func (o *Orchestrator) agentAnswers(stageID string) {
	o.answerMu.Lock()
	defer o.answerMu.Unlock()
	o.liveAgents.Store(stageID, struct{}{})
}

// stopAnswering ends that without a dialog decision: an answer from now on starts its own continuation.
func (o *Orchestrator) stopAnswering(stageID string) {
	o.answerMu.Lock()
	defer o.answerMu.Unlock()
	o.liveAgents.Delete(stageID)
}

type dialogExit int

const (
	dialogNone      dialogExit = iota // no question left open: the turn goes on
	dialogLeft                        // the stage waits for the user; the answer starts the continuation
	dialogTakenOver                   // a Pause/Revise took the stage before the turn could leave it
)

// leaveWithUser is the turn's one dialog decision, under answerMu like every answer: the open question is read and the
// stage handed to the user in the same critical section, so an answer is either seen here or starts its own continuation.
func (o *Orchestrator) leaveWithUser(stageID, phase string, epoch uint64, dialog bool) dialogExit {
	o.turnChecksDialog(stageID)
	o.answerMu.Lock()
	defer o.answerMu.Unlock()
	o.liveAgents.Delete(stageID)
	if !dialog || !o.hasOpenQuestion(stageID, phase) {
		return dialogNone
	}
	if o.currentStatus(stageID) == state.StatusAwaitingUserInput {
		return dialogLeft
	}
	if _, ok := o.Trigger(stageID, bus.EvAskUser, bus.GuardCtx{Phase: phase, ExpectEpoch: &epoch}, ""); !ok {
		return dialogTakenOver
	}
	return dialogLeft
}

func (o *Orchestrator) turnChecksDialog(stageID string) {
	if o.dialogHook != nil {
		o.dialogHook(stageID, false)
	}
}

func (o *Orchestrator) turnLeftForAnswer(stageID string) {
	if o.dialogHook != nil {
		o.dialogHook(stageID, true)
	}
}

// turnOwnsStage reports whether the turn that started in epoch still owns the stage: nobody paused, continued
// or revised it since.
func (o *Orchestrator) turnOwnsStage(stageID string, epoch uint64) bool {
	return o.opts.Store.Snapshot().Stages[stageID].Epoch == epoch
}

// reviseOfTurn reports whether the stage is revising because of a Revise of the turn that started in epoch —
// the one takeover right after it (a Revise of a later turn, after Pause → Continue, is not this turn's).
func reviseOfTurn(st state.StageState, epoch uint64) bool {
	return st.Status == state.StatusRevising && st.Epoch == epoch+1
}

// handOffRevise starts the feedback run for a Revise of the exiting turn (onUserInterrupted), and only for one:
// a turn that lost the stage to anything else leaves it alone. The feedback run's own first transition claims
// the Revise (claimRevise), so a second hand-off for the same Revise runs no agent.
func (o *Orchestrator) handOffRevise(stageID string, epoch uint64, onUserInterrupted func()) {
	if reviseOfTurn(o.opts.Store.Snapshot().Stages[stageID], epoch) {
		onUserInterrupted()
	}
}

// isRetryableError checks if the error should be retried at all (with backoff). Recognizes both
// ClassRetryable (known transient pattern, full budget) and ClassRetryableUnknown (unrecognized crash,
// limited budget) — budget differentiation itself is wired separately (T5), this just answers "retry or not".
func isRetryableError(err error) bool {
	switch Classify(err) {
	case ClassRetryable, ClassRetryableUnknown:
		return true
	default:
		return false
	}
}

// Both retry budgets stop at the outer attempt limit, so exhaustion cannot leave the stage running.
func transportRetryDecision(class Classification, attempt, unknownRetries, maxRetries, maxUnknownRetries int) (retryOK bool, budget, numerator, nextUnknownRetries int) {
	budget = maxRetries
	numerator = attempt + 1
	nextUnknownRetries = unknownRetries
	retryOK = attempt < maxRetries
	if class == ClassRetryableUnknown {
		nextUnknownRetries++
		budget = maxUnknownRetries
		numerator = nextUnknownRetries
		retryOK = nextUnknownRetries <= maxUnknownRetries && attempt < maxRetries
	}
	return
}

type verifyCorrection struct {
	failures        int
	maxFailures     int
	fresh           bool
	patience        int
	prevFingerprint string
	unchanged       int
}

func (v verifyCorrection) canRetry(isVerifyReject bool, attempt, maxRetries int) bool {
	return attempt < maxRetries &&
		((isVerifyReject && v.failures < v.maxFailures) ||
			(!isVerifyReject && attempt == 0))
}

func (v verifyCorrection) counters() verifyCounters {
	return verifyCounters{Failures: v.failures, Stalemates: v.unchanged}
}

// verifyMaxFailures — бюджет отклонений verify для стадии: сколько коррекций автора разрешить, прежде чем
// стадия провалится.
func (o *Orchestrator) verifyMaxFailures(s flow.Stage) int {
	n := o.opts.Config.Verify.ResolvedMaxFailures()
	if s.Verify.MaxFailures != nil {
		n = *s.Verify.MaxFailures
	}
	return n
}

// verifyReviewPatience — бюджет stalemate для стадии: сколько ПОДРЯД идущих verify-отклонений, НЕ
// изменивших рабочее дерево, допустить до приёма стадии. Зеркалит verifyMaxFailures.
func (o *Orchestrator) verifyReviewPatience(s flow.Stage) int {
	n := o.opts.Config.Verify.ResolvedReviewPatience()
	if s.Verify.ReviewPatience != nil {
		n = *s.Verify.ReviewPatience
	}
	return n
}

// verifyFreshCorrection — давать ли автору после verify-reject чистую сессию. Per-stage override >
// config > дефолт ON. Зеркалит verifyMaxFailures.
func (o *Orchestrator) verifyFreshCorrection(s flow.Stage) bool {
	if s.Verify.FreshCorrection != nil {
		return *s.Verify.FreshCorrection
	}
	return o.opts.Config.Verify.ResolvedFreshCorrection()
}

// dropSessionForFreshCorrection, when enabled, drops the session file so the NEXT attempt starts a fresh
// session instead of resuming one that defends its prior approach (anchoring) — the reconstruction (actions
// + Q&A digest + verify/feedback.md + incompleteReason) covers the lost context. No-op for non-interactive
// stages (no session file to drop) and when freshCorrection is disabled.
func dropSessionForFreshCorrection(freshCorrection bool, stageDir, phase string) {
	if !freshCorrection {
		return
	}
	_ = os.Remove(stagefiles.SessionFile(stageDir, phase))
}

// buildRetryContext reads the last N lines from the agent's raw stream-json
// log and formats them as a continuation context for the retry prompt.
func buildRetryContext(stageDir, phase string) string {
	jsonlName := flow.PhaseJSONL(flow.Phase(phase))

	lines := executor.RenderActions(filepath.Join(stageDir, jsonlName))
	if len(lines) == 0 {
		return ""
	}
	const maxLines = 200
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}

	var buf strings.Builder
	buf.WriteString("\n\n## Previously completed actions (resuming after interruption)\n\n")
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			buf.WriteString(l)
			buf.WriteString("\n")
		}
	}
	buf.WriteString("\nContinue from where you left off. Do NOT redo work that is already done.\n")
	return buf.String()
}

// RetryBackoff — база растущего backoff'а (см. RetryBackoffMax, D2): пауза между попытками растёт как
// RetryBackoff*2^attempt до потолка. Прежний exponential [5s,10s,30s] сдавался после 4 попыток — z.ai
// overload длится дольше, поэтому потолок подняли, а не вернули фиксированную паузу.
var RetryBackoff = 5 * time.Second

// MaxRetries — число повторных попыток после первого запуска. См. RetryBackoff: фиксируется в
// Orchestrator.maxRetries при New.
var MaxRetries = 15

// MaxUnknownRetries — бюджет ретраев для ClassRetryableUnknown (D1): процесс стартовал и упал с текстом,
// которого Classify не узнал. Детерминированный сбой (сломанный wrapper, отсутствующий jq) не должен жечь
// полный MaxRetries наравне с настоящим транспортным сбоем — отсюда отдельный, маленький бюджет.
var MaxUnknownRetries = 3

// RetryBackoffMax — потолок паузы. База остаётся 5s (см. RetryBackoff): терпение растёт за счёт роста
// паузы, а не за счёт сокращения числа попыток — прежнее решение про долгий overload не ломается.
var RetryBackoffMax = 60 * time.Second

// backoffFor — min(base*2^attempt, limit). Сдвиг ограничен, чтобы большой attempt не переполнил int64.
// Параметр назван limit, а не max, — max с Go 1.21 встроенная функция, затенять её линтер запрещает.
func backoffFor(attempt int, base, limit time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	// 62 бита сдвига для int64 уже гарантированно перевалят за любой разумный limit — дальше
	// можно сразу возвращать потолок, не вычисляя степень двойки.
	const maxShift = 62
	if attempt < 0 {
		attempt = 0
	}
	if attempt > maxShift {
		return limit
	}
	d := base << attempt
	if d <= 0 || d > limit {
		return limit
	}
	return d
}

// defaultJitterFrac — доля паузы, которую jittered вычитает в проде (см. jittered). Orchestrator.jitterFrac
// снимается с этой константы в New; структурные литералы в тестах (>40 мест в pkg/orchestrator) получают
// нулевое значение поля по умолчанию — jittered(d, 0) возвращает d без изменений, поэтому такие тесты видят
// точный номинал backoffFor, а не случайный шум.
const defaultJitterFrac = 0.2

// jittered ВЫЧИТАЕТ из паузы 0..frac: при max_parallel > 1 стадии не бьются в rate-limited API в такт.
// Именно вычитание, а не прибавление: иначе на потолке пауза уходила бы за RetryBackoffMax и потолок
// стал бы ложным. При frac<=0 результат РОВНО d (инвариант "не больше max" держится по построению в обоих
// случаях — с джиттером результат в (d*(1-frac), d], без него — ровно d).
func jittered(d time.Duration, frac float64) time.Duration {
	if d <= 0 || frac <= 0 {
		return d
	}
	maxCut := time.Duration(float64(d) * frac)
	if maxCut <= 0 {
		return d
	}
	// Джиттер против синхронного ретрая нескольких стадий — не секрет и не криптография.
	cut := time.Duration(rand.Int64N(int64(maxCut))) //nolint:gosec // G404: math/rand достаточно для таймингового джиттера
	return d - cut
}

// parkForPeers parks a stage whose turn yielded on waits (episode) in awaiting_peer and, if an answer already
// arrived, continues it at once. The park is fenced with the execution epoch the turn started in: if Pause,
// Continue or Revise took the stage over while the process was exiting, this exit is stale and moves nothing —
// a Revise is then carried out through onUserInterrupted, the path a revised turn takes.
func (o *Orchestrator) parkForPeers(ctx context.Context, stageID, phase string, epoch uint64, episode []agentcomm.WaitID, onUserInterrupted func()) {
	o.prePeerPhase.Store(stageID, phase)
	// prePeerPhase is in-memory only (sync.Map) — a restart loses it exactly like preAskPhase. Persist the
	// phase durably too, so recoverMeshWaits can resume into the right phase after a crash, mirroring
	// detectInterruptedPhase's role for awaiting_user_input but without depending on a session file that
	// non-interactive stages never create.
	saveMeshPeerWaitPhase(o.stageDir(stageID), phase)
	if _, ok := o.Trigger(stageID, bus.EvAwaitPeer, bus.GuardCtx{Phase: phase, ExpectEpoch: &epoch, PeerWaits: episode}, ""); !ok {
		o.prePeerPhase.CompareAndDelete(stageID, phase)
		o.handOffRevise(stageID, epoch, onUserInterrupted)
		return
	}
	o.continueParkedStage(ctx, stageID)
}

// claimVerifyResume решает, чей приказ пробуждения verify-парка держит этот цикл. nil — цикл обычный (приказа нет
// или его уже принял другой цикл того же ctx: hand-off проснувшегося хода). ok == false — приказ принят, но стадию
// забрали между пробуждением и стартом цикла (эпоха сменилась): цикл устарел и лишь передаёт стадию feedback-ходу
// своего Revise, если он был.
func (o *Orchestrator) claimVerifyResume(ctx context.Context, stageID string, onUserInterrupted func()) (order *verifyResume, ok bool) {
	order = verifyResumeFrom(ctx)
	if order == nil || !order.claimLoop() {
		return nil, true
	}
	if o.opts.Store.Snapshot().Stages[stageID].Epoch != order.epoch {
		order.spend()
		o.handOffRevise(stageID, order.epoch, onUserInterrupted)
		return nil, false
	}
	return order, true
}

// runWithRetry runs a fresh (not a Revise's) turn of the stage under runRetryLoop.
func (o *Orchestrator) runWithRetry(ctx context.Context, s flow.Stage, phase string, agentFn func(retryContext string) error, completionCheck func() error, onUserInterrupted func()) {
	o.runRetryLoop(ctx, s, phase, false, agentFn, completionCheck, onUserInterrupted)
}

// runRetryLoop wraps an agent function with automatic retry on rate limit errors. After exhausting all
// retries: publishes EventRetryExhausted. feedback marks the turn as the feedback run of a Revise: a usage-limit
// park of it records that (state.QuotaWait.Feedback), so the wake resumes the feedback run and not a fresh one.
func (o *Orchestrator) runRetryLoop(ctx context.Context, s flow.Stage, phase string, feedback bool, agentFn func(retryContext string) error, completionCheck func() error, onUserInterrupted func()) {
	// SpawnAgent checks pause after acquiring its slot; interruptChans handles pauses after the agent starts.
	epoch, granted := turnEpochFrom(ctx)
	if !granted {
		epoch = o.stageEpoch(s.ID)
	}
	turn := &turnRecord{epoch: epoch}
	o.liveTurns.Store(s.ID, turn)
	defer o.liveTurns.CompareAndDelete(s.ID, turn)
	interruptCh := make(chan struct{}, 1)
	o.interruptChans.Store(s.ID, interruptCh)
	// G3: CompareAndDelete, а не безусловный Delete — тот же приём, что E3 применил к stageLeases.
	// CompareAndDelete удаляет запись, только если она всё ещё РОВНО ТА, что сохранил этот вызов.
	defer o.interruptChans.CompareAndDelete(s.ID, interruptCh)

	// resumeCtx однократно включает контекст прежних действий на attempt 0 после Continue; LoadAndDelete
	// потребляет маркер.
	resumeCtx := false
	if _, ok := o.resumeContextOnce.LoadAndDelete(s.ID); ok {
		resumeCtx = true
	}

	incompleteReason := ""
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	// A turn that continues the stage after a peer wait (state.StageState.PeerContinued, from the journal — it
	// survives a restart before the agent started) is a fresh session unless the agent's session is resumed:
	// it gets the earlier turns' actions, so it goes on where they stopped instead of starting its task over.
	if o.opts.Store != nil && o.opts.Store.Snapshot().Stages[s.ID].PeerContinued && !willResume(s, stageDir, phase) {
		resumeCtx = true
	}
	// maxRetries/retryBackoff/maxUnknownRetries/retryBackoffMax — снапшоты с инстанса (см.
	// Orchestrator-комментарий): эта горутина может пережить возврат Run(), поэтому globals не читаем.
	maxRetries := o.maxRetries
	retryBackoff := o.retryBackoff
	maxUnknownRetries := o.maxUnknownRetries
	retryBackoffMax := o.retryBackoffMax
	// Unknown transport failures have their own budget, independent of verify corrections.
	unknownRetries := 0
	// Пробуждение verify-парка продолжает цикл с теми же счётчиками: парк не тратит и не обнуляет бюджеты verify.
	verifyResume, ok := o.claimVerifyResume(ctx, s.ID, onUserInterrupted)
	if !ok {
		return // приказ устарел: стадию забрали, пока ход ждал слот
	}
	correction := verifyCorrection{
		maxFailures: o.verifyMaxFailures(s),
		fresh:       o.verifyFreshCorrection(s),
		patience:    o.verifyReviewPatience(s),
	}
	// Quota resume restores counters; the fingerprint stays local to this loop and starts empty.
	if verifyResume != nil {
		correction.failures = verifyResume.quota.VerifyFailures
		correction.unchanged = verifyResume.quota.VerifyStalemates
	}
	fingerprintDir := o.projectDir()
	// progressed — ход этой горутины уже успешно завершался: следующий usage-limit начинает серию отказов заново.
	progressed := false
	for attempt := 0; attempt <= maxRetries; attempt++ {
		retryCtx := ""
		if attempt > 0 || resumeCtx {
			retryCtx = buildRetryContext(stageDir, phase)
			if incompleteReason != "" {
				retryCtx += "\n\n## Completion check failed\n\n" + incompleteReason +
					"\n\nFix the underlying problem and make the check pass before finishing.\n"
			}
			// Сессии не будет — агент не увидит прежние ответы пользователя в разговоре; дайджест их
			// реконструирует. Resumed-агенту НЕ даём: он получил бы дубль того, что и так видит.
			if !willResume(s, stageDir, phase) {
				retryCtx += dialogDigestBlock(stageDir, phase)
			}
		}
		resumeCtx = false // только самая первая попытка резюмируемого запуска получает его

		if cur := o.currentStatus(s.ID); cur == state.StatusPaused || cur == state.StatusDone || cur == state.StatusFailed {
			return // paused in the shouldRun->register gap, or ended while queued: do not start the subprocess
		}
		// A takeover since the grant (Pause, Continue, Revise — also while this runner waited for its slot) owns the stage.
		if !o.turnOwnsStage(s.ID, epoch) {
			o.handOffRevise(s.ID, epoch, onUserInterrupted)
			return
		}
		// Пробуждение verify-парка: автор уже отработал, первая попытка идёт сразу в completionCheck, и verify
		// продолжает припаркованный проход (RunVerification).
		resumesVerify := attempt == 0 && verifyResume != nil
		if !resumesVerify {
			// Admission провайдера — после слота команды и before-hook (мы внутри lease), на каждом ходе, в том числе на
			// пробуждении: другая стадия того же провайдера, запаркованная пока мы ждали слот, держит и эту. Парк по
			// hold'у не расходует retry, unknownRetries и серию отказов.
			progressed = progressed || o.progressedSincePark(s.ID, stageDir, phase)
			if hold := o.admitStageTurn(s); hold != nil {
				o.parkForQuota(ctx, s, phase, epoch, hold, progressed, feedback, onUserInterrupted)
				return
			}
		}
		waitsBefore := o.meshWaitStatusesOf(meshStageParticipantID(s.ID))
		markerBefore := o.readCompletionMarker(s, stageDir, phase)
		var err error
		if !resumesVerify {
			o.agentAnswers(s.ID)
			err = agentFn(retryCtx)
			if err != nil {
				o.stopAnswering(s.ID)
			}
		}
		// Everything below acts for this turn, whatever the agent returned: if Pause, Continue or Revise took the stage
		// over while the process was exiting, the stage is another turn's now (or the Revise's feedback run's).
		if !o.turnOwnsStage(s.ID, epoch) {
			o.stopAnswering(s.ID)
			if resumesVerify {
				verifyResume.spend() // feedback-ход, который стартует следом, не продолжит этот проход
			}
			o.handOffRevise(s.ID, epoch, onUserInterrupted)
			return
		}
		if err == nil {
			progressed = true
			// The turn ran to its end, so its <inbox> messages were handed over. The executor marks them at
			// process start (OnDelivered); repeating it here (idempotent) also covers a runner that never calls
			// it — otherwise they would look unread and earn the stage an extra turn (agentcomm_unread.go).
			if !resumesVerify {
				o.deliverRenderedInbox(meshStageParticipantID(s.ID))()
			}
			// afm bug: интерактивный агент может завершиться, не дождавшись ответа пользователя — на диске остаётся
			// question.json без answer.json. Вопрос, который поллер уже показал, пока агент работал, агент бросил: если
			// работа сделана и ход ни на кого не ждёт, стадия завершается.
			dialog := s.Interactive || phase == phaseAutonomous
			if dialog && o.currentStatus(s.ID) == state.StatusAwaitingUserInput && o.hasOpenQuestion(s.ID, phase) &&
				len(o.turnEpisode(meshStageParticipantID(s.ID), waitsBefore)) == 0 && (completionCheck == nil || completionCheck() == nil) {
				o.stopAnswering(s.ID)
				o.completeAgent(ctx, s.ID, phase, &epoch)
				return
			}
			switch o.leaveWithUser(s.ID, phase, epoch, dialog) {
			case dialogLeft:
				o.turnLeftForAnswer(s.ID)
				return
			case dialogTakenOver:
				o.handOffRevise(s.ID, epoch, onUserInterrupted)
				return
			}
			// Agent Mesh Phase 5, T5.2: the executor registered a mesh Wait on a colleague and exited
			// cooperatively — park in awaiting_peer instead of running it through the completion/incomplete-
			// work machinery below. This is NOT a false completion, NOT an IncompleteWorkError retry, and NOT
			// a user question: the lease this invocation held is released simply by this function returning,
			// exactly like the awaiting_user_input branch above.
			// The turn yielded when it ended for waits — still pending, or already answered or ended while the
			// process was exiting (the live resume then found the stage running and did nothing).
			if episode := o.turnEpisode(meshStageParticipantID(s.ID), waitsBefore); len(episode) > 0 {
				if resumesVerify {
					verifyResume.spend()
				} else if marker := o.readCompletionMarker(s, stageDir, phase); marker.freshSince(markerBefore) {
					o.publishCompletionMarkerNotice(s.ID, marker.file, episode)
				}
				o.parkForPeers(ctx, s.ID, phase, epoch, episode, onUserInterrupted)
				return
			}
			var checkErr error
			if completionCheck != nil {
				checkErr = completionCheck()
			}
			if resumesVerify {
				verifyResume.spend() // verify мимо этого прохода (проба файлов не прошла) не продолжит старый проход позже
			}
			// F1: completionCheck может занять сколько угодно времени ПОСЛЕ того, как автор уже вернулся, — за это
			// время Pause/Revise мог долговечно перевести стадию в paused/revising.
			if !o.turnOwnsStage(s.ID, epoch) {
				o.handOffRevise(s.ID, epoch, onUserInterrupted)
				return
			}
			if checkErr == nil {
				o.completeAgent(ctx, s.ID, phase, &epoch)
				return
			}
			// AI-verify: верификатор слушает тот же interruptChans, что и автор — Pause/Revise во время verify
			// всплывает сюда либо этим сентинелом, либо уже долговечным paused/revising-статусом.
			if errors.Is(checkErr, executor.ErrUserInterrupted) {
				onUserInterrupted()
				return
			}
			// Usage-limit verify-агента — не провал и не отклонение: стадия паркуется на шаге verify с текущими
			// счётчиками, бюджеты коррекции и stalemate не тратятся.
			var verifyLimit *verifyLimitError
			if errors.As(checkErr, &verifyLimit) {
				o.parkVerifyForQuota(ctx, s, phase, epoch, verifyLimit, correction.counters(), feedback, onUserInterrupted)
				return
			}
			// Only verify rejections spend the correction budget; transport and plan retries leave it intact.
			if stagefiles.IsIncompleteWorkError(checkErr) {
				var vre *VerifyRejectedError
				isVerifyReject := errors.As(checkErr, &vre)
				// B1 stalemate: если автор перестал менять рабочее дерево в ответ на повторные verify-отклонения —
				// принять стадию как завершённую вместо очередной коррекции.
				if isVerifyReject && correction.patience > 0 {
					var done bool
					done, correction.prevFingerprint, correction.unchanged = o.handleStalemate(
						ctx, s, phase, vre, correction.patience, fingerprintDir,
						correction.prevFingerprint, correction.unchanged, onUserInterrupted)
					if done {
						return
					}
				}
				// На последней попытке continue оставил бы стадию активной без следующей итерации; проверяем attempt <
				// maxRetries.
				if correction.canRetry(isVerifyReject, attempt, maxRetries) {
					// G2: повторная проверка ПРЯМО ПЕРЕД incompleteReason/continue — см. verifyOutcomeStillOwned.
					if o.verifyOutcomeGuardHook != nil {
						o.verifyOutcomeGuardHook(s.ID)
					}
					if !o.verifyOutcomeStillOwned(s.ID, onUserInterrupted) {
						return
					}
					if isVerifyReject {
						correction.failures++
						dropSessionForFreshCorrection(correction.fresh, stageDir, phase)
					}
					incompleteReason = checkErr.Error()
					// Раньше публиковалось как EventStageStatusChanged с Data- сообщением — фронт рисовал это пустым
					// бейджем "→ ", и на reload сообщение терялось.
					msg := "incomplete work, retrying: " + checkErr.Error()
					o.publishNotice(bus.Event{Type: bus.EventRetryScheduled, StageID: s.ID, Data: msg})
					continue
				}
				// verify-бюджет исчерпан ИЛИ план-незавершёнка после attempt 0 —
				// падаем в общий fail-путь ниже.
			}
			// Missing artifact or second incomplete attempt — fail. См. commitVerifyFailure.
			if isVerifyDrivenError(checkErr) {
				if o.commitVerifyFailure(s.ID, epoch, checkErr.Error(), onUserInterrupted) {
					o.failBlockedStages()
				}
			} else {
				o.failTurn(s.ID, epoch, checkErr.Error(), onUserInterrupted)
			}
			return
		}

		if errors.Is(err, executor.ErrUserInterrupted) {
			if o.currentStatus(s.ID) == state.StatusPaused {
				return // Pause() already recorded the durable transition — nothing to restart
			}
			onUserInterrupted()
			return
		}

		// Usage limit провайдера — не сбой: стадия паркуется durable до сброса и не расходует ни retry-бюджет, ни
		// unknownRetries (parkForQuota). Тип проверяет errors.As внутри Classify.
		class := Classify(err)
		var limit *executor.UsageLimitError
		if class == ClassUsageLimit && errors.As(err, &limit) {
			o.parkForQuota(ctx, s, phase, epoch, limit, progressed, feedback, onUserInterrupted)
			return
		}
		if class != ClassRetryable && class != ClassRetryableUnknown {
			// Drop the session file so a later retry starts a fresh Claude session instead of resuming a
			// conversation that was never created. Mirrors the retryable branch.
			_ = os.Remove(stagefiles.SessionFile(stageDir, phase))
			o.failTurn(s.ID, epoch, err.Error(), onUserInterrupted)
			return
		}

		// Retryable errors are typically caused by an overgrown session context. Drop the session file so the
		// next attempt starts a fresh Claude session.
		_ = os.Remove(stagefiles.SessionFile(stageDir, phase))

		retryOK, budget, numerator, nextUnknownRetries := transportRetryDecision(class, attempt, unknownRetries, maxRetries, maxUnknownRetries)
		unknownRetries = nextUnknownRetries

		if !retryOK {
			if seq, ok := o.failTurn(s.ID, epoch, "retries exhausted", onUserInterrupted); ok {
				o.publishCritical(ctx, bus.Event{Type: bus.EventRetryExhausted, StageID: s.ID, Seq: seq})
			}
			// Бюджет исчерпан: unknownRetries может пройти это раньше, чем attempt достигнет maxRetries —
			// явный return (в отличие от прежнего кода, где это совпадало с последней итерацией цикла).
			return
		}

		// Сохраняем сообщение retry в reason, чтобы reload восстановил тот же payload; seq дедуплицирует live и
		// историю. delay растёт с attempt до потолка retryBackoffMax и вычитающе джиттерится (D2) — сообщение
		// показывает фактическую паузу, а не номинальный RetryBackoff.
		delay := jittered(backoffFor(attempt, retryBackoff, retryBackoffMax), o.jitterFrac)
		msg := fmt.Sprintf("attempt %d/%d in %v", numerator, budget, delay)
		_, seq, ok := o.triggerWithSeq(s.ID, bus.EvScheduleRetry, bus.GuardCtx{Phase: phase, ExpectEpoch: &epoch}, msg)
		if !ok {
			o.handOffRevise(s.ID, epoch, onUserInterrupted)
			return
		}
		o.ui.Publish(bus.Event{
			Type:    bus.EventRetryScheduled,
			StageID: s.ID,
			Data:    msg,
			Seq:     seq,
		})
		select {
		case <-o.clk().After(delay):
		case <-interruptCh:
			return // Pause() already transitioned to paused — nothing to resume here
		case <-ctx.Done():
			o.failTurn(s.ID, epoch, "cancelled during retry", onUserInterrupted)
			return
		}
		resumePhase := phaseImplementation
		if phase == phasePlanning {
			resumePhase = phasePlanning
		}
		o.Trigger(s.ID, bus.EvResumeAfterRetry, bus.GuardCtx{Phase: resumePhase, ExpectEpoch: &epoch}, "")
	}
}

// failTurn fails the stage for the turn that started in epoch: a takeover since then owns the stage, and a Revise of
// this turn gets its feedback run instead.
func (o *Orchestrator) failTurn(stageID string, epoch uint64, reason string, onUserInterrupted func()) (seq uint64, ok bool) {
	_, seq, ok = o.triggerWithSeq(stageID, bus.EvFail, bus.GuardCtx{ExpectEpoch: &epoch}, reason)
	if !ok {
		o.handOffRevise(stageID, epoch, onUserInterrupted)
		return 0, false
	}
	o.failBlockedStages()
	return seq, true
}
