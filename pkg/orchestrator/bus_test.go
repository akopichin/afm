package orchestrator

import (
	"context"
	"testing"
	"time"
)

func TestCriticalBus_Blocking(t *testing.T) {
	b := NewCriticalBus(2)

	if err := b.Publish(context.Background(), Event{Type: EventApproved, StageID: "a"}); err != nil {
		t.Fatalf("publish 1: %v", err)
	}
	if err := b.Publish(context.Background(), Event{Type: EventApproved, StageID: "b"}); err != nil {
		t.Fatalf("publish 2: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := b.Publish(ctx, Event{Type: EventApproved, StageID: "c"}); err == nil {
		t.Fatal("publish 3: expected ctx timeout")
	}

	ev := <-b.Recv()
	if ev.StageID != "a" {
		t.Errorf("first event = %q, want %q", ev.StageID, "a")
	}
}

func TestUIBus_FanOutAndDrop(t *testing.T) {
	b := NewUIBus()
	_, ch1 := b.Subscribe(1)
	_, ch2 := b.Subscribe(1)

	b.Publish(Event{Type: EventAgentAction, StageID: "a", Data: "msg1"})
	b.Publish(Event{Type: EventAgentAction, StageID: "a", Data: "msg2"})

	ev1 := <-ch1
	if ev1.Data != "msg1" {
		t.Errorf("ch1 first = %v, want msg1", ev1.Data)
	}
	ev2 := <-ch2
	if ev2.Data != "msg1" {
		t.Errorf("ch2 first = %v, want msg1", ev2.Data)
	}

	if got := b.DroppedCount(); got != 2 {
		t.Errorf("DroppedCount = %d, want 2", got)
	}
}

func TestUIBus_Unsubscribe(t *testing.T) {
	b := NewUIBus()
	id, ch := b.Subscribe(4)
	b.Unsubscribe(id)

	b.Publish(Event{Type: EventAgentAction})
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel closed after Unsubscribe")
		}
	case <-time.After(50 * time.Millisecond):
		t.Error("channel neither closed nor delivered")
	}
}
