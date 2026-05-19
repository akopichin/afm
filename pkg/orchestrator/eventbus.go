package orchestrator

import "sync"

// EventType is the type of an event.
type EventType string

const (
	EventStageStatusChanged EventType = "stage_status_changed"
	EventAgentAction        EventType = "agent_action"
	EventAgentCompleted     EventType = "agent_completed"
	EventFeedbackReceived   EventType = "feedback_received"
	EventApproved           EventType = "approved"
	EventRevised            EventType = "revised"
	EventRetryScheduled     EventType = "retry_scheduled"
	EventRetryExhausted     EventType = "retry_exhausted"
	EventManualRetry        EventType = "manual_retry"
)

// Event is an event in the system.
type Event struct {
	Type    EventType `json:"type"`
	StageID string    `json:"stage_id"`
	Data    any       `json:"data,omitempty"`
}

// EventBus is a pub/sub event bus.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[<-chan Event]chan Event
	closed      bool
}

// NewEventBus creates an EventBus.
func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[<-chan Event]chan Event),
	}
}

// Subscribe returns a channel for reading events.
func (b *EventBus) Subscribe() <-chan Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan Event, 64)
	b.subscribers[ch] = ch
	return ch
}

// Unsubscribe removes a subscriber.
func (b *EventBus) Unsubscribe(ch <-chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if c, ok := b.subscribers[ch]; ok {
		delete(b.subscribers, ch)
		close(c)
	}
}

// Publish sends an event to all subscribers.
func (b *EventBus) Publish(ev Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return
	}
	for _, ch := range b.subscribers {
		select {
		case ch <- ev:
		default:
			// subscriber too slow — skip
		}
	}
}

// Close closes all subscriber channels.
func (b *EventBus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for key, ch := range b.subscribers {
		delete(b.subscribers, key)
		close(ch)
	}
}
