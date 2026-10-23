package orchestrator

import (
	"context"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// Durable ожидание сброса usage-limit провайдера (state.StatusAwaitingQuota).
//
// Парк — это durable EvAwaitQuota (StageState.Quota хранит until/причину/цель) и немедленный выход хода: lease и
// горутина освобождаются. Пробуждение — таймер на run-scoped context (armQuotaWake), одинаковый для live и
// recovery: источник истины — Quota.Until из журнала, а не память процесса. Отмена run context ничего не пишет
// в журнал: после рестарта resumeQuotaWait восстанавливает ту же работу из StageState.

// quotaJitterFrac — доля resume_margin, в пределах которой until сдвигается в обе стороны: стадии, ждущие один
// и тот же сброс, не стартуют в одну секунду. until остаётся внутри запаса и не раньше самого сброса.
const quotaJitterFrac = 0.1

// planQuotaWait строит запись парка для отказа limit: когда проснуться или почему ждать нельзя. waits — номер
// этого отказа в серии подряд (для provider_hold — число уже случившихся отказов: hold не отказ). Чистая функция:
// время и конфиг приходят снаружи.
func planQuotaWait(cfg config.UsageLimitConfig, limit *executor.UsageLimitError, target string, waits int, now time.Time) state.QuotaWait {
	if limit.Source == state.QuotaSourceProviderHold {
		// Ждём конец чужого парка: его until уже прошёл все проверки (max_wait/no_reset/max_waits), и своего сброса
		// у этой стадии нет — ни джиттера, ни пауз.
		return state.QuotaWait{
			Provider: limit.Provider, Window: limit.Window, Source: state.QuotaSourceProviderHold,
			Target: target, Waits: waits, Until: limit.ResetAt.UTC(),
		}
	}
	q := state.QuotaWait{
		Provider: limit.Provider,
		Window:   limit.Window,
		Source:   limit.Source,
		Target:   target,
		Waits:    waits,
	}
	if !limit.ResetAt.IsZero() {
		q.ResetAt = limit.ResetAt.UTC()
	}
	margin := cfg.ResolvedResumeMargin()
	switch {
	case limit.NoReset:
		q.PauseReason = state.QuotaPauseNoReset
	case waits > cfg.ResolvedMaxWaits():
		q.PauseReason = state.QuotaPauseMaxWaits
	case q.ResetAt.IsZero():
		q.Source = state.QuotaSourceProbe
		q.Until = now.Add(cfg.ResolvedProbeInterval()).UTC()
	default:
		until := q.ResetAt.Add(margin + quotaJitter(margin))
		if until.Before(now) {
			until = now.Add(margin) // сброс уже в прошлом (часы расходятся): дать провайдеру запас
		}
		if until.Sub(now) > cfg.ResolvedMaxWait() {
			q.PauseReason = state.QuotaPauseExceedsMaxWait
		} else {
			q.Until = until.UTC()
		}
	}
	return q
}

// quotaJitter — случайный сдвиг в пределах ±quotaJitterFrac*margin.
func quotaJitter(margin time.Duration) time.Duration {
	span := int64(float64(margin) * quotaJitterFrac)
	if span <= 0 {
		return 0
	}
	// Разброс таймингов, не секрет и не криптография.
	return time.Duration(rand.Int64N(2*span+1) - span) //nolint:gosec // G404: math/rand достаточно для таймингового джиттера
}

// parkForQuota паркует стадию, чей ход (начатый в эпохе epoch) упёрся в limit, и сразу возвращает управление.
// Парк привязан к эпохе: если Pause, Continue или Revise забрали стадию, пока процесс выходил, этот выход
// устарел и ничего не двигает (Revise выполняется через onUserInterrupted). progressed — стадия двигалась с
// прошлой парковки (успешный ход этой горутины или запись прогресса в журнале действий): серия отказов
// начинается заново, как при limit.Progress. limit с Source provider_hold паркует без счёта отказа. feedback — ход
// был feedback-запуском Revise: парк запоминает это, и пробуждение возобновляет именно его (QuotaWait.Feedback).
func (o *Orchestrator) parkForQuota(ctx context.Context, s flow.Stage, phase string, epoch uint64, limit *executor.UsageLimitError, progressed, feedback bool, onUserInterrupted func()) {
	// Новая сессия: старая не доживёт до пробуждения (как при retryable-сбое), прежний контекст соберёт
	// buildRetryContext.
	_ = os.Remove(stagefiles.SessionFile(filepath.Join(o.opts.RunDir, s.ID), phase))

	waits := quotaWaits(o.opts.Store.Snapshot().Stages[s.ID].Quota, phase, progressed, limit)
	q := planQuotaWait(o.opts.Config.UsageLimit, limit, phase, waits, o.clk().Now())
	q.ProgressMark = quotaProgressMark(filepath.Join(o.opts.RunDir, s.ID), phase)
	q.Feedback = feedback
	if !o.commitQuotaPark(ctx, s.ID, &epoch, q) {
		o.handOffRevise(s.ID, epoch, onUserInterrupted)
	}
}

// verifyCounters — verify-счётчики цикла автора на момент парка verify: сколько отклонений уже потрачено из
// бюджета коррекций и сколько подряд stalemate-раундов.
type verifyCounters struct{ Failures, Stalemates int }

// parkVerifyForQuota паркует стадию, чей verify-шаг (ход автора начат в эпохе epoch) упёрся в limit: тот же
// парк, что parkForQuota, но цель — verify. Парк хранит проход, шаг и verify-счётчики: пробуждение вызывает
// автора ноль раз и повторяет ровно этот шаг. Серию отказов продолжает только повторный отказ того же шага того же
// прохода — шаг дальше или новый проход (автор успел отработать) начинает её заново. phase — фаза исполнения хода,
// чей gate запустил проверку: парк хранит её, потому что runnerKind живёт только в памяти. Сессию автора не трогаем:
// автор не падал, а свежая коррекция после отклонения сбросит её сама (dropSessionForFreshCorrection).
func (o *Orchestrator) parkVerifyForQuota(ctx context.Context, s flow.Stage, phase string, epoch uint64, vl *verifyLimitError, counters verifyCounters, feedback bool, onUserInterrupted func()) {
	prev := o.opts.Store.Snapshot().Stages[s.ID].Quota
	sameStep := prev != nil && prev.VerifyID == vl.VerifyID && prev.VerifyStep == vl.Step
	waits := quotaWaits(prev, state.QuotaTargetVerify, !sameStep, vl.Limit)
	q := planQuotaWait(o.opts.Config.UsageLimit, vl.Limit, state.QuotaTargetVerify, waits, o.clk().Now())
	q.VerifyID, q.VerifyStep, q.VerifyPhase = vl.VerifyID, vl.Step, phase
	q.VerifyFailures, q.VerifyStalemates = counters.Failures, counters.Stalemates
	q.Feedback = feedback
	if !o.commitQuotaPark(ctx, s.ID, &epoch, q) {
		o.handOffRevise(s.ID, epoch, onUserInterrupted)
	}
}

// verifyResumeKey — ключ ctx для verifyResume.
type verifyResumeKey struct{}

// verifyResume — одноразовый приказ пробуждения verify-парка: продолжить проход quota.VerifyID с шага
// quota.VerifyStep, не вызывая автора. Едет в ctx хода, который запускает пробуждение, но принадлежит ровно одному
// retry-циклу (claimLoop) и только пока стадия остаётся в эпохе парка: замыкания hand-off проснувшегося хода
// (feedback-запуск Revise, continueParkedStage) пересоздают ход на ТОМ ЖЕ ctx, и их цикл не должен унаследовать
// приказ — иначе автор feedback-хода не запустится, а проход verify продолжит устаревший. Сам приказ
// живёт в ctx, но его намерение durable (QuotaWait.VerifyResume, его пишет EvQuotaReset): рестарт между
// EvQuotaReset и spawn восстанавливает приказ из журнала (resumeWokenVerify).
type verifyResume struct {
	quota state.QuotaWait
	// epoch — эпоха стадии в парке (EvQuotaReset её не меняет); Pause/Continue/Revise меняют, и приказ устаревает.
	epoch uint64
	// claimed — какой-то цикл уже принял приказ; used — RunVerification уже взял его.
	claimed atomic.Bool
	used    atomic.Bool
}

func withVerifyResume(ctx context.Context, q state.QuotaWait, epoch uint64) context.Context {
	return context.WithValue(ctx, verifyResumeKey{}, &verifyResume{quota: q, epoch: epoch})
}

func verifyResumeFrom(ctx context.Context) *verifyResume {
	r, _ := ctx.Value(verifyResumeKey{}).(*verifyResume)
	return r
}

// claimLoop отдаёт приказ ровно одному retry-циклу — первому, что стартовал на этом ctx.
func (r *verifyResume) claimLoop() bool { return r.claimed.CompareAndSwap(false, true) }

// take отдаёт приказ ровно одному RunVerification.
func (r *verifyResume) take() bool { return r.used.CompareAndSwap(false, true) }

// spend гасит приказ: проверка файлов-пробы или иной выход хода прошёл мимо verify, и более поздний проход после
// коррекции автора обязан начаться заново, а не продолжить старый.
func (r *verifyResume) spend() { r.used.Store(true) }

// commitQuotaPark durably parks the stage in awaiting_quota with q and arms its wake timer — or, when q carries
// a PauseReason (the limit cannot be waited out), pauses the stage right after the park so PausedFrom and the
// reason survive replay. expectEpoch fences the park to the turn that decided it (nil — no fence). Reports
// whether the park committed.
func (o *Orchestrator) commitQuotaPark(ctx context.Context, stageID string, expectEpoch *uint64, q state.QuotaWait) bool {
	message := QuotaWaitMessage(&q)
	_, seq, ok := o.triggerWithSeq(stageID, bus.EvAwaitQuota, bus.GuardCtx{ExpectEpoch: expectEpoch, Quota: &q}, message)
	if !ok {
		return false
	}
	o.publishQuotaNotice(eventQuotaWait, stageID, "", quotaNoticeData(&q, message))
	if q.PauseReason != "" {
		o.Trigger(stageID, bus.EvPause, bus.GuardCtx{ExpectStageSeq: &seq}, "usage limit: "+q.PauseReason)
		return true
	}
	o.armQuotaWake(ctx, stageID, seq, q.Until)
	return true
}

// armQuotaWake starts the wake timer of the park seq (afterOnRunContext): cancelling the run stops it without
// touching the journal, and the next start re-derives the same decision from the durable Until (resumeQuotaWait).
func (o *Orchestrator) armQuotaWake(ctx context.Context, stageID string, parkSeq uint64, until time.Time) {
	o.afterOnRunContext(ctx, until, func(ctx context.Context) { o.wakeFromQuota(ctx, stageID, parkSeq) })
}

// afterOnRunContext runs fn once o.clk() reaches at (at once if it already has). The timer rides o.clk() like
// armMeshWaitDeadline and lives on the run context, never a request's (a handler's context ends when it
// returns): cancelling the run drops it without calling fn.
func (o *Orchestrator) afterOnRunContext(ctx context.Context, at time.Time, fn func(context.Context)) {
	d := max(at.Sub(o.clk().Now()), 0)
	o.concurrency.SpawnDetached(o.runContext(ctx), func(ctx context.Context) {
		select {
		case <-o.clk().After(d):
			fn(ctx)
		case <-ctx.Done():
		}
	})
}

// wakeFromQuota ends the wait of the park parkSeq: the stage returns to the phase it was in and a fresh agent
// starts without the before-hook (resumeStageForPhaseAs, like a peer reply); a parked feedback run resumes as the
// feedback run, so the Revise's feedback is not lost. A verify park resumes its execution-phase turn with a
// verifyResume order instead: that turn skips the author and repeats the parked verify step. The transition is fenced by the park's
// seq, so a timer of an earlier park, or a stage that Pause/Continue/Fail already moved, does nothing. A review
// pause holds activation: the wake is then left to recoverQuotaWaits, which the review resume runs.
func (o *Orchestrator) wakeFromQuota(ctx context.Context, stageID string, parkSeq uint64) {
	park := o.opts.Store.Snapshot().Stages[stageID]
	if park.Status != state.StatusAwaitingQuota || park.Seq != parkSeq || park.Quota == nil || ctx.Err() != nil {
		return
	}
	if o.activationBlocked() {
		return
	}
	stage := o.reg().Stage(stageID)
	if stage == nil {
		return
	}
	phase := park.Quota.Target
	spawnCtx := ctx
	switch phase {
	case "":
		phase = executionPhases[o.executionKind(*stage)]
	case state.QuotaTargetVerify:
		// The author's turn already ended: the verify gate runs inside the turn of its execution phase, which
		// goes straight to the parked step (verifyResume) without calling the author.
		phase = o.verifyWakePhase(*stage, park.Quota)
		spawnCtx = withVerifyResume(ctx, *park.Quota, park.Epoch)
	default:
		// The turn after the wait is a fresh session: it gets the earlier actions (buildRetryContext).
		o.armResumeContext(stageID)
	}
	if err := o.resumeStageForPhaseAs(spawnCtx, *stage, phase, bus.EvQuotaReset, parkSeq, park.Quota.Feedback); err != nil {
		log.Printf("WARN: quota: wake stage %q: %v", stageID, err)
	}
	if o.opts.Store.Snapshot().Stages[stageID].Seq == parkSeq {
		o.resumeContextOnce.Delete(stageID) // the wake did not commit: nobody consumes the marker
	}
}

// verifyWakePhase — фаза хода, который возобновляет verify-парк: сохранённая в парке (переживает рестарт), а у парка
// из старого журнала — выведенная из состояния стадии (runnerKind, иначе вид по умолчанию).
func (o *Orchestrator) verifyWakePhase(s flow.Stage, q *state.QuotaWait) string {
	switch q.VerifyPhase {
	case phaseImplementation, phaseReview, phaseAutonomous:
		return q.VerifyPhase
	}
	if p, ok := executionPhases[o.computeResumeKind(s, state.StatusRunning)]; ok {
		return p
	}
	return phaseImplementation
}

// resumeQuotaWait decides what a stage found in awaiting_quota does next — the one path shared by startup, a
// Continue of a quota pause and a review-pause resume: a one-shot manual probe or an expired until wakes it
// now, a future until arms the timer for the remainder, and a park that only had to be paused (the pause did
// not commit before the process stopped) is paused.
func (o *Orchestrator) resumeQuotaWait(ctx context.Context, s flow.Stage) {
	park := o.opts.Store.Snapshot().Stages[s.ID]
	q := park.Quota
	if park.Status != state.StatusAwaitingQuota || q == nil {
		return
	}
	switch {
	case q.ManualProbe || (q.PauseReason == "" && !q.Until.After(o.clk().Now())):
		o.wakeFromQuota(ctx, s.ID, park.Seq)
	case q.PauseReason != "":
		o.Trigger(s.ID, bus.EvPause, bus.GuardCtx{ExpectStageSeq: &park.Seq}, "usage limit: "+q.PauseReason)
	default:
		o.armQuotaWake(ctx, s.ID, park.Seq, q.Until)
	}
}

// recoverQuotaWaits re-decides every stage waiting for a quota — after a review pause released the activation
// hold that blocked their wakes.
func (o *Orchestrator) recoverQuotaWaits(ctx context.Context) {
	for _, s := range o.reg().Stages() {
		o.resumeQuotaWait(ctx, s)
	}
}

// manualProbeQuota is the quota Continue records when it takes a stage out of a quota pause: the same wait with
// the one-shot manual-probe intent, a new series of refusals and no pause reason. It lives in the transition,
// so a crash between Continue and the spawn keeps the probe.
func manualProbeQuota(prev *state.QuotaWait) *state.QuotaWait {
	q := state.QuotaWait{}
	if prev != nil {
		q = *prev
	}
	q.ManualProbe = true
	q.Waits = 0
	q.PauseReason = ""
	return &q
}

// quotaWaits — номер отказа limit в серии подряд для парка цели target: серию продолжает только парк той же
// цели, пока не было продвижения (progressed или limit.Progress). provider_hold — не отказ агента: серию он не
// увеличивает и не обрывает.
func quotaWaits(prev *state.QuotaWait, target string, progressed bool, limit *executor.UsageLimitError) int {
	waits := quotaSeries(prev, target, progressed || limit.Progress)
	if limit.Source != state.QuotaSourceProviderHold {
		waits++
	}
	return waits
}

// quotaSeries — число подряд идущих реальных отказов ДО текущего события. Серию продолжает только парк той же
// фазы (другая фаза значит, что прошлая дошла до конца) и только пока стадия не продвинулась с него.
func quotaSeries(prev *state.QuotaWait, phase string, progressed bool) int {
	if prev == nil || prev.Target != phase || progressed {
		return 0
	}
	return prev.Waits
}

// quotaProgressMark — отметка продвижения стадии в фазе: число зафиксированных агентом действий (вызовов
// инструментов и ответов) в stream-логе фазы. Пишется в парк и переживает рестарт, поэтому прогресс между двумя
// отказами виден и новой горутине (progressedSincePark).
func quotaProgressMark(stageDir, phase string) uint64 {
	return uint64(len(executor.RenderActions(filepath.Join(stageDir, flow.PhaseJSONL(flow.Phase(phase))))))
}

// progressedSincePark сообщает, что стадия зафиксировала действия после последнего парка своей серии отказов:
// следующий отказ начнёт серию заново. Без серии (нет парка или Waits == 0) ничего не читает.
func (o *Orchestrator) progressedSincePark(stageID, stageDir, phase string) bool {
	prev := o.opts.Store.Snapshot().Stages[stageID].Quota
	return prev != nil && prev.Target == phase && prev.Waits > 0 && quotaProgressMark(stageDir, phase) > prev.ProgressMark
}

// admitProvider — единственная проверка hold'а провайдера перед запуском агента: автор стадии (в том числе после
// пробуждения и retry), verify-агент и ход помощника зовут её, а не копируют правило. nil — запускать можно.
// Иначе возвращается *executor.UsageLimitError с Source provider_hold, ResetAt == конец hold'а и Provider:
// вызывающий паркуется по нему тем же путём, что и по настоящему отказу (planQuotaWait ждёт ровно ResetAt без
// джиттера и не считает отказ).
//
// Hold не хранится отдельно, а выводится из журнала: это самый поздний until среди агентов того же provider, чей
// until ещё впереди: стадий в awaiting_quota и помощников в queued с durable парком (TaskState.Quota). Парки,
// созданные самим hold'ом (Source provider_hold), и приостановленные стадии и помощники (Holds) hold не продлевают.
// stageID — стадия, запускающая агента ("" у помощника): её собственный парк не держит её же, а её одноразовая
// ручная проба (Quota.ManualProbe) обходит hold только в первой admission после Continue (spendManualProbe):
// повторная попытка автора и verify проверяют hold. provider "" не ограничивается.
func (o *Orchestrator) admitProvider(provider, stageID string) *executor.UsageLimitError {
	return o.admitAgent(provider, stageID, "")
}

// admitAgent — admitProvider для агента, который может быть и помощником: taskID — запускающийся помощник (""
// у стадии), его собственный парк не держит его же.
func (o *Orchestrator) admitAgent(provider, stageID string, taskID assistants.TaskID) *executor.UsageLimitError {
	if provider == "" || o.opts.Store == nil { // без журнала (часть unit-тестов verify) hold вывести не из чего
		return nil
	}
	snap := o.opts.Store.Snapshot()
	if own := snap.Stages[stageID]; own.Quota != nil && own.Quota.ManualProbe && o.spendManualProbe(stageID, own.Seq) {
		return nil
	}
	now := o.clk().Now()
	var hold *state.QuotaWait
	consider := func(q *state.QuotaWait) {
		if q == nil || q.Provider != provider || q.Source == state.QuotaSourceProviderHold || !q.Until.After(now) {
			return
		}
		if hold == nil || q.Until.After(hold.Until) {
			hold = q
		}
	}
	for id, st := range snap.Stages {
		if id != stageID && st.Status == state.StatusAwaitingQuota {
			consider(st.Quota)
		}
	}
	for id, t := range snap.Assistants.Tasks {
		if id != taskID && t.Status == assistants.StatusQueued && !t.Paused() {
			consider(t.Quota)
		}
	}
	if hold == nil {
		return nil
	}
	return &executor.UsageLimitError{
		Provider: provider, Window: hold.Window, ResetAt: hold.Until, Source: state.QuotaSourceProviderHold,
		Message: "provider usage limit is held by another parked agent",
	}
}

// spendManualProbe тратит одноразовую ручную пробу стадии, записанную переходом seq: true — это первая admission
// после Continue (обходит hold), false — проба уже потрачена, и hold действует как для любой стадии.
func (o *Orchestrator) spendManualProbe(stageID string, seq uint64) bool {
	spent, loaded := o.manualProbeSpent.Swap(stageID, seq)
	return !loaded || spent != seq
}

// admitStageTurn — admission хода автора стадии s. Паркуются только стадии в статусах, из которых разрешён
// EvAwaitQuota: ход revising (feedback) не может уйти в парк, и пропуск стадии без запуска оставил бы её висеть.
func (o *Orchestrator) admitStageTurn(s flow.Stage) *executor.UsageLimitError {
	switch o.currentStatus(s.ID) {
	case state.StatusPlanning, state.StatusRunning, state.StatusRetrying:
		return o.admitProvider(o.stageProvider(s), s.ID)
	default:
		return nil
	}
}

// taskProviderHold — admission хода помощника: hold провайдера его профильной команды или nil. Под hold'ом ход не
// стартует: задача остаётся (или возвращается) в queued, а retryTaskAt повторяет планирование к концу hold'а.
// Собственное durable ожидание помощника после отказа — TaskState.Quota (parkTaskForQuota).
func (o *Orchestrator) taskProviderHold(task assistants.Task) *executor.UsageLimitError {
	return o.admitAgent(o.opts.Config.UsageLimitProvider(task.ProfileSnapshot.Command), "", task.ID)
}

// retryTaskAt повторяет планирование queued-задачи в момент at (конец hold'а или until парка): таймер на
// run-scoped context в памяти. И hold, и until выводятся из журнала (admitProvider, TaskState.Quota), поэтому
// потеря таймера при рестарте безопасна: recovery планирует queued-задачи заново, и maybeScheduleTask снова
// взводит его. Вызывать после того, как задача уже queued, иначе повтор увидит running и ничего не сделает.
func (o *Orchestrator) retryTaskAt(ctx context.Context, taskID assistants.TaskID, at time.Time) {
	o.afterOnRunContext(ctx, at, func(ctx context.Context) { o.maybeScheduleTask(ctx, taskID) })
}
