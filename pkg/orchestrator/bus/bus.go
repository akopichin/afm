package bus

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
)

type EventType string

const (
	EventStageStatusChanged EventType = "stage_status_changed"
	EventAgentAction        EventType = "agent_action"
	EventAgentCompleted     EventType = "agent_completed"
	EventApproved           EventType = "approved"
	EventRetryScheduled     EventType = "retry_scheduled"
	EventRetryExhausted     EventType = "retry_exhausted"
	EventAskUser            EventType = "ask_user"
	EventUserAnswered       EventType = "user_answered"
	// EventAutoAnswered fires when a non-interactive stage's open question was
	// answered by afm itself (see pkg/mcp.PickAutoAnswer), not by a real user.
	// Never triggers an FSM transition — the stage's status is unaffected.
	EventAutoAnswered EventType = "auto_answered"
	// EventDialogQuestion surfaces an interactive question to the dashboard
	// Feed — carries the display-ready {phase,id,title} payload (see
	// mcp.DialogFeedNotice/DialogSnippet). Contract is exactly-one-feed-ROW,
	// not exactly-one-emission: within a process the in-memory `processed` map
	// normally emits it once per surfacing, but it can legitimately be emitted
	// AGAIN — the malformed-question give-up fallback and, after an afm
	// restart, a still-unanswered question re-scanned from awaiting_user_input
	// both re-publish it. Duplicates are collapsed by content (type+stage+
	// phase+id+title): the live path dedupes in use-event-feed.ts and the
	// replay path in reconstructNotices' dialogDedupTypes allowlist, so the
	// user sees one row. A genuinely new question reusing an answered id (with
	// different text) has a different title → its own row. Never triggers an
	// FSM transition itself.
	EventDialogQuestion EventType = "dialog_question"
	// EventDialogAnswer fires exactly once, right after a HUMAN answer to an
	// interactive dialog question is authorized and durably recorded (i.e.
	// after NotifyAnswer succeeds and the dialog.jsonl history entry is
	// written) — carries the same display-ready {phase,id,title} payload
	// shape as EventDialogQuestion (see mcp.DialogFeedNotice/DialogSnippet),
	// with title derived from the answer text. Mirrors EventDialogQuestion on
	// the answer side; never fires on a rejected/rolled-back answer (e.g. the
	// review-pause 409/TOCTOU path in handleDialogAnswer). Never triggers an
	// FSM transition itself — NotifyAnswer already did that.
	EventDialogAnswer EventType = "dialog_answer"
	// EventAgentNote fires once, right after a HUMAN note is delivered to a live
	// agent via Revise (the messenger-style feed composer, or the plan-panel
	// revise) and the durable EvRevise transition is applied. Carries
	// map[string]any{"id": <transition seq>, "text": <mcp.DialogSnippet>} — the
	// id makes the live event and its persisted notice reconcile without
	// collapsing two distinct notes that happen to share text (see
	// use-event-feed.ts). Never fires on a no-op Revise (wrong status / lost
	// CAS) and never triggers an FSM transition itself — Revise already did.
	EventAgentNote      EventType = "agent_note"
	EventContextWarning EventType = "context_warning"
	// EventScriptOutput carries one line of stdout from a script/hook run.
	// Data: map[string]string{"hook": "before"|"script"|"after", "line": "..."}.
	EventScriptOutput EventType = "script_output"
	// EventHookFailed fires when a before/after hook exhausts its 3x/1-2-3s
	// retries. Data: map[string]string{"hook": ..., "error": "..."}.
	EventHookFailed EventType = "hook_failed"
	// EventHookResolved fires when the user retries or skips a failed hook.
	// Data: map[string]string{"hook": ..., "resolution": "retried"|"skipped"}.
	EventHookResolved EventType = "hook_resolved"
	// EventLifecycleHookFailed fires when a lifecycle (observer) hook exhausts
	// its retries or its delivery queue overflows. Purely informational: never
	// affects the FSM, the stage or the run outcome.
	EventLifecycleHookFailed EventType = "lifecycle_hook_failed"
	// EventReflectFailed fires when a step of the memory pipeline
	// (maybeRunReflection/distill: reflect→aggregate→prioritize→update)
	// returns an error. Best-effort notice only — never triggers an FSM
	// transition and never fails the stage/run. Data: map[string]string{
	// "stage": ..., "message": ...}.
	EventReflectFailed EventType = "reflect_failed"
	// eventAgentDrained is an internal-only nudge published on the critical
	// bus right after a script_after hook's goroutine finishes
	// (maybeRunAfterHook, hooks.go), once pendingAfterHooks has already been
	// decremented. handleEvent's switch doesn't match it (falls through as a
	// no-op) — its only purpose is to wake Run()'s select loop so it
	// re-checks shouldExit() with a guaranteed-fresh pendingAfterHooks value.
	// Needed because a script_after hook resolving (RetryHook/SkipHook, or
	// the script finally succeeding) never itself triggers an FSM
	// transition/EventAgentCompleted, so without this Run() could sit
	// blocked on select forever after the hook settles, never noticing every
	// stage is actually done. Never published to ui —
	// must not reach the dashboard.
	eventAgentDrained EventType = "__agent_drained"
)

type Event struct {
	Type    EventType `json:"type"`
	StageID string    `json:"stage_id"`
	Data    any       `json:"data,omitempty"`
	// Seq — реальный seq FSM-transition (0, если событие не привязано к
	// transition, напр. EventContextWarning/EventAgentCompleted). Позволяет
	// фронту дедуплицировать историю из /api/events с live-потоком WS по
	// стабильному ключу вместо содержимого.
	Seq uint64 `json:"seq,omitempty"`
}

type CriticalBus struct {
	ch chan Event
}

func NewCriticalBus(buf int) *CriticalBus {
	if buf <= 0 {
		buf = 16
	}
	return &CriticalBus{ch: make(chan Event, buf)}
}

func (b *CriticalBus) Publish(ctx context.Context, ev Event) error {
	select {
	case b.ch <- ev:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TryPublish публикует событие неблокирующим best-effort send; возвращает
// false, если буфер шины полон — вызывающий код тогда просто теряет толчок
// (см. WakeEventLoop).
func (b *CriticalBus) TryPublish(ev Event) bool {
	select {
	case b.ch <- ev:
		return true
	default:
		return false
	}
}

// WakeEventLoop будит select Run()'а неблокирующей отправкой внутреннего
// маркер-события — используется concurrency.Manager.WakeEventLoop после
// того, как after-hook горутина завершилась без движения FSM (script_after
// никогда не публикует EventAgentCompleted сама). Best-effort: если буфер
// полон, там уже стоят другие события — их обработка и так вызовет
// перепроверку состояния, потеря толчка безвредна.
func (b *CriticalBus) WakeEventLoop() bool {
	return b.TryPublish(Event{Type: eventAgentDrained})
}

func (b *CriticalBus) Recv() <-chan Event { return b.ch }

type uiSub struct {
	ch    chan Event
	drops atomic.Uint64
}

type UIBus struct {
	mu        sync.RWMutex
	subs      map[uint64]*uiSub
	nextID    uint64
	dropped   atomic.Uint64
	logOnce   map[uint64]bool
	logOnceMu sync.Mutex
}

func NewUIBus() *UIBus {
	return &UIBus{
		subs:    make(map[uint64]*uiSub),
		logOnce: make(map[uint64]bool),
	}
}

func (b *UIBus) Subscribe(bufSize int) (uint64, <-chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	id := b.nextID
	if bufSize <= 0 {
		bufSize = 64
	}
	sub := &uiSub{ch: make(chan Event, bufSize)}
	b.subs[id] = sub
	return id, sub.ch
}

func (b *UIBus) Unsubscribe(id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if sub, ok := b.subs[id]; ok {
		delete(b.subs, id)
		close(sub.ch)
	}
	b.logOnceMu.Lock()
	delete(b.logOnce, id)
	b.logOnceMu.Unlock()
}

func (b *UIBus) Publish(ev Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for id, sub := range b.subs {
		select {
		case sub.ch <- ev:
		default:
			b.dropped.Add(1)
			sub.drops.Add(1)
			if b.shouldLog(id) {
				log.Printf("uibus: dropped event for slow subscriber id=%d (further drops counted silently)", id)
			}
		}
	}
}

func (b *UIBus) DroppedCount() uint64 { return b.dropped.Load() }

func (b *UIBus) SubscriberDroppedCount(id uint64) uint64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if sub, ok := b.subs[id]; ok {
		return sub.drops.Load()
	}
	return 0
}

func (b *UIBus) shouldLog(id uint64) bool {
	b.logOnceMu.Lock()
	defer b.logOnceMu.Unlock()
	if b.logOnce[id] {
		return false
	}
	b.logOnce[id] = true
	return true
}
