package orchestrator

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
	EventSupervisorDecision EventType = "supervisor_decision"
	EventContextWarning     EventType = "context_warning"
)

type Event struct {
	Type    EventType `json:"type"`
	StageID string    `json:"stage_id"`
	Data    any       `json:"data,omitempty"`
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
