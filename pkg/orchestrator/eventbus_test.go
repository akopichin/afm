package orchestrator

import (
	"sync"
	"testing"
	"time"
)

const testStageID = "s1"

func TestEventBus_PublishAndSubscribe(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	sub := bus.Subscribe()
	bus.Publish(Event{Type: EventStageStatusChanged, StageID: testStageID})

	select {
	case ev := <-sub:
		if ev.Type != EventStageStatusChanged || ev.StageID != testStageID {
			t.Errorf("unexpected event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestEventBus_MultipleSubscribers(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	sub1 := bus.Subscribe()
	sub2 := bus.Subscribe()

	bus.Publish(Event{Type: EventAgentCompleted, StageID: testStageID})

	for _, sub := range []<-chan Event{sub1, sub2} {
		select {
		case ev := <-sub:
			if ev.StageID != testStageID {
				t.Errorf("unexpected: %+v", ev)
			}
		case <-time.After(time.Second):
			t.Fatal("timeout")
		}
	}
}

func TestEventBus_Unsubscribe(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	sub := bus.Subscribe()
	bus.Unsubscribe(sub)

	bus.Publish(Event{Type: EventStageStatusChanged, StageID: testStageID})

	// After Unsubscribe the channel is closed; reading from a closed channel
	// returns the zero value immediately. Verify it is the zero value.
	ev, ok := <-sub
	if ok {
		t.Fatalf("channel should be closed, got event: %+v", ev)
	}
}

func TestEventBus_ConcurrentPublish(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	sub := bus.Subscribe()
	const n = 100

	// Use a goroutine to consume events so the channel buffer does not fill up,
	// then publish from multiple goroutines concurrently.
	var mu sync.Mutex
	received := 0
	done := make(chan struct{})
	go func() {
		for range sub {
			mu.Lock()
			received++
			r := received
			mu.Unlock()
			if r == n {
				close(done)
				return
			}
		}
	}()

	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			bus.Publish(Event{Type: EventAgentAction, StageID: testStageID, Data: i})
		}()
	}
	wg.Wait()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		mu.Lock()
		r := received
		mu.Unlock()
		t.Fatalf("received only %d/%d events", r, n)
	}
}
