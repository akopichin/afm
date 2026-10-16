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
	// EventAutoAnswered fires when a non-interactive stage's open question was answered by afm itself, not by
	// a real user. Never triggers an FSM transition — the stage's status is unaffected.
	EventAutoAnswered EventType = "auto_answered"
	// EventDialogQuestion surfaces an interactive question to the dashboard Feed — carries the display-ready
	// {phase,id,title} payload. Never triggers an FSM transition itself.
	EventDialogQuestion EventType = "dialog_question"
	// EventDialogAnswer publishes {phase,id,title} once after a human answer is authorized and durably
	// recorded; no FSM transition.
	EventDialogAnswer EventType = "dialog_answer"
	// EventAgentNote fires once, right after a HUMAN note is delivered to a live agent via Revise and the
	// durable EvRevise transition is applied.
	EventAgentNote      EventType = "agent_note"
	EventContextWarning EventType = "context_warning"
	// EventScriptOutput carries one line of output from a script/hook run. Data: map[string]string{"hook":
	// "before"|"script"| "after", "line": "...", "stream": "stdout"|"stderr"}.
	EventScriptOutput EventType = "script_output"
	// EventScriptFailed fires once a script: stage exhausts runScriptWithRetry and the stage is about to
	// transition to failed.
	EventScriptFailed EventType = "script_failed"
	// EventHookFailed fires when a before/after hook exhausts its 3x/1-2-3s retries.
	EventHookFailed EventType = "hook_failed"
	// EventHookResolved fires when the user retries or skips a failed hook.
	// Data: map[string]string{"hook": ..., "resolution": "retried"|"skipped"}.
	EventHookResolved EventType = "hook_resolved"
	// EventLifecycleHookFailed fires when a lifecycle hook exhausts its retries or its delivery queue
	// overflows. Purely informational: never affects the FSM, the stage or the run outcome.
	EventLifecycleHookFailed EventType = "lifecycle_hook_failed"
	// EventReflectFailed fires when a step of the memory pipeline returns an error. Best-effort notice only —
	// never triggers an FSM transition and never fails the stage/run.
	EventReflectFailed EventType = "reflect_failed"
	// EventVerifyStarted records a verify step's start live and in notices.jsonl; verify is neither an FSM
	// transition nor a phase.
	EventVerifyStarted EventType = "verify_started"
	// EventVerifyResult fires right AFTER one AI-verify step's outcome is known —
	// pass/needs_changes/inconclusive, or the step couldn't be evaluated at all.
	EventVerifyResult EventType = "verify_result"
	// EventVerifyRetry fires when a transient verify exec-failure is about to be retried after backoff.
	// Non-FSM: published live + durable to notices.jsonl, like EventVerifyStarted/Result.
	EventVerifyRetry EventType = "verify_retry"
	// EventVerifyTreeModified fires when a working-tree fingerprint taken right before an AI-verify agent
	// step differs from the one taken right after it — the verifier (supposed to be read-only) touched the
	// tree. Purely observational: never changes the step's verdict or the stage outcome. Non-FSM: published
	// live + durable to notices.jsonl, like EventVerifyStarted/Result.
	EventVerifyTreeModified EventType = "verify_tree_modified"
	// EventAssistantTaskCreated fires once a helper task is durably created (task_created control record
	// committed) — non-FSM, never changes the root stage's status.
	EventAssistantTaskCreated EventType = "assistant_task_created"
	// EventAssistantTaskStatus fires on every durable task_status_changed for a helper task (queued, running,
	// completed, failed, cancelled, ...) — the task's own lifecycle, orthogonal to stage FSM.
	EventAssistantTaskStatus EventType = "assistant_task_status"
	// EventHelperTreeModified mirrors EventVerifyTreeModified for a helper task's run: a working-tree
	// fingerprint taken before/after the helper's turn differed. Purely observational — never changes the
	// task's verdict.
	EventHelperTreeModified EventType = "helper_tree_modified"
	// EventHelperResultRepaired fires when a helper task's raw answer needed mechanical repair
	// (assistants.DecodeResultWithRepair) before it could be strictly decoded — e.g. wrapped in a markdown
	// fence or surrounded by prose. Non-FSM, live + durable (notices.jsonl): the task's own verdict is
	// unaffected, this is purely observational. Data: {"task_id": string}.
	EventHelperResultRepaired EventType = "helper_result_repaired"
	// EventDiscussionMessage fires once a discussion_message control record is durably committed (T6.1).
	// Never an FSM transition — a discussion message never changes the root stage's status
	// (TestDiscussion_UserMessageIsNotAutoRevise).
	EventDiscussionMessage EventType = "discussion_message"
	// EventProposalCreated/EventProposalStatus fire on proposal_created/proposal_status_changed control
	// records (T6.4). Non-FSM.
	EventProposalCreated EventType = "proposal_created"
	EventProposalStatus  EventType = "proposal_status"
	// EventFeedbackDelivered fires once an accepted proposal's (or a plain user note's) FeedbackIntent is
	// actually projected into the stage's real execution path (T6.5/T6.6). Data always carries "origin"
	// ("user" | "assistant_proposal") and, for a forwarded proposal, "participants" — the feed must render a
	// forwarded proposal as "user forwarded a proposal from @X", never as raw AI text passed off as the human's
	// own words (TestFeedUI_ShowsUserForwardedProposal).
	EventFeedbackDelivered EventType = "feedback_delivered"
	// eventAgentDrained is an internal-only nudge published on the critical bus right after a script_after
	// hook's goroutine finishes, once pendingAfterHooks has already been decremented.
	eventAgentDrained EventType = "__agent_drained"
)

type Event struct {
	Type    EventType `json:"type"`
	StageID string    `json:"stage_id"`
	Data    any       `json:"data,omitempty"`
	// Seq — реальный seq FSM-transition. Позволяет фронту дедуплицировать историю из /api/events с
	// live-потоком WS по стабильному ключу вместо содержимого.
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

// TryPublish публикует событие неблокирующим best-effort send; возвращает false, если буфер шины полон —
// вызывающий код тогда просто теряет толчок (см. WakeEventLoop).
func (b *CriticalBus) TryPublish(ev Event) bool {
	select {
	case b.ch <- ev:
		return true
	default:
		return false
	}
}

// WakeEventLoop неблокирующе будит Run после after-hook без FSM-перехода; полный буфер уже гарантирует
// перепроверку.
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
