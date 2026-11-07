package clock

import (
	"sync"
	"testing"
	"time"
)

func TestFake_NowDoesNotMoveOnItsOwn(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	c := NewFake(start)
	time.Sleep(10 * time.Millisecond)
	if got := c.Now(); !got.Equal(start) {
		t.Fatalf("Now() = %v, want unchanged %v", got, start)
	}
}

func TestFake_AdvanceMovesExactlyByD(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	c := NewFake(start)
	c.Advance(5 * time.Second)
	want := start.Add(5 * time.Second)
	if got := c.Now(); !got.Equal(want) {
		t.Fatalf("Now() = %v, want %v", got, want)
	}
}

func TestFake_AfterFiresExactlyOnceOnAdvance(t *testing.T) {
	c := NewFake(time.Unix(0, 0))
	ch := c.After(time.Second)

	select {
	case <-ch:
		t.Fatal("After channel fired before Advance")
	default:
	}

	c.BlockUntil(1)
	c.Advance(time.Second)

	select {
	case got := <-ch:
		if want := c.Now(); !got.Equal(want) {
			t.Fatalf("After fired with %v, want %v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("After channel did not fire after Advance")
	}

	select {
	case v := <-ch:
		t.Fatalf("After channel fired a second time: %v", v)
	default:
	}
}

func TestFake_AdvanceWakesOnlyDueWaiters(t *testing.T) {
	c := NewFake(time.Unix(0, 0))
	soon := c.After(1 * time.Second)
	later := c.After(10 * time.Second)
	c.BlockUntil(2)

	c.Advance(1 * time.Second)

	select {
	case <-soon:
	default:
		t.Fatal("soon waiter should have fired")
	}
	select {
	case <-later:
		t.Fatal("later waiter must not fire yet")
	default:
	}

	c.Advance(9 * time.Second)
	select {
	case <-later:
	default:
		t.Fatal("later waiter should fire once its deadline is reached")
	}
}

func TestFake_BlockUntilWaitsForRegistration(t *testing.T) {
	c := NewFake(time.Unix(0, 0))
	done := make(chan struct{})
	go func() {
		c.BlockUntil(1)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("BlockUntil(1) returned before any waiter was registered")
	case <-time.After(50 * time.Millisecond):
	}

	c.After(time.Second)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("BlockUntil(1) did not return after a waiter was registered")
	}
}

func TestFake_TimerResetMovesDeadlineAndReportsActive(t *testing.T) {
	c := NewFake(time.Unix(0, 0))
	timer := c.NewTimer(time.Second)
	c.BlockUntil(1)

	// Reset before expiry: must report active (true) and move the deadline.
	if active := timer.Reset(5 * time.Second); !active {
		t.Fatal("Reset on a pending timer must report active=true")
	}

	c.Advance(time.Second)
	select {
	case <-timer.C():
		t.Fatal("timer fired at the old 1s deadline after Reset moved it to 5s")
	default:
	}

	c.Advance(4 * time.Second)
	select {
	case <-timer.C():
	default:
		t.Fatal("timer did not fire at the new 5s deadline")
	}

	// Reset on an already-fired timer must report inactive (false).
	if active := timer.Reset(time.Second); active {
		t.Fatal("Reset on an already-fired timer must report active=false")
	}
}

// TestFake_TimerResetAfterExpiryDiscardsStaleTick guards the exact bug codex
// caught in review: Advance pushes an expired tick into the buffered channel,
// and if Reset re-arms the entry without draining it, a receive right after
// Reset wrongly observes that stale tick — violating stdlib's guarantee that a
// receive after Reset cannot see a value from the previous setting (used by
// the executor's idle timer: Reset on every stdout line must not let an old
// tick cause a false idle timeout).
func TestFake_TimerResetAfterExpiryDiscardsStaleTick(t *testing.T) {
	c := NewFake(time.Unix(0, 0))
	timer := c.NewTimer(time.Second)
	c.BlockUntil(1)

	c.Advance(time.Second) // expires the timer; tick sits unconsumed in the channel

	if active := timer.Reset(time.Second); active {
		t.Fatal("Reset on an already-expired, unconsumed timer must report active=false")
	}

	select {
	case v := <-timer.C():
		t.Fatalf("Reset must discard the stale pre-expiry tick, got %v", v)
	default:
	}

	c.Advance(time.Second)
	select {
	case <-timer.C():
	default:
		t.Fatal("timer did not fire at its new deadline after Reset")
	}
}

func TestFake_TimerStopPreventsFire(t *testing.T) {
	c := NewFake(time.Unix(0, 0))
	timer := c.NewTimer(time.Second)
	c.BlockUntil(1)

	if stopped := timer.Stop(); !stopped {
		t.Fatal("Stop on a pending timer must report true")
	}
	if stopped := timer.Stop(); stopped {
		t.Fatal("second Stop on an already-stopped timer must report false")
	}

	c.Advance(10 * time.Second)
	select {
	case v := <-timer.C():
		t.Fatalf("stopped timer fired: %v", v)
	default:
	}
}

func TestFake_TickerCoalescesMissedTicks(t *testing.T) {
	c := NewFake(time.Unix(0, 0))
	ticker := c.NewTicker(time.Second)
	c.BlockUntil(1)

	// time.Ticker drops ticks the receiver hasn't consumed yet — a single
	// Advance spanning several intervals must deliver exactly one buffered
	// tick, not one per interval crossed.
	c.Advance(3 * time.Second)

	select {
	case <-ticker.C():
	default:
		t.Fatal("ticker should have fired at least once")
	}
	select {
	case v := <-ticker.C():
		t.Fatalf("ticker coalescing failed: got a second buffered tick %v", v)
	default:
	}

	// The ticker must still be running on its regular cadence afterwards.
	c.Advance(time.Second)
	select {
	case <-ticker.C():
	default:
		t.Fatal("ticker did not continue ticking after a coalesced advance")
	}

	ticker.Stop()
	c.Advance(10 * time.Second)
	select {
	case v := <-ticker.C():
		t.Fatalf("stopped ticker fired: %v", v)
	default:
	}
}

func TestFake_ConcurrentAfterAndAdvanceRace(t *testing.T) {
	c := NewFake(time.Unix(0, 0))
	const n = 50

	var wg sync.WaitGroup
	fired := make([]int32, n)
	for i := 0; i < n; i++ {
		wg.Go(func() {
			<-c.After(time.Duration(i+1) * time.Millisecond)
			fired[i] = 1
		})
	}

	c.BlockUntil(n)
	c.Advance(time.Duration(n+1) * time.Millisecond)
	wg.Wait()

	for i, f := range fired {
		if f != 1 {
			t.Fatalf("waiter %d never fired", i)
		}
	}
}

func TestReal_AfterBehavesLikeStdlib(t *testing.T) {
	c := Real()
	start := time.Now()
	select {
	case got := <-c.After(10 * time.Millisecond):
		if got.Before(start) {
			t.Fatalf("After fired with a time before it was scheduled: %v < %v", got, start)
		}
	case <-time.After(time.Second):
		t.Fatal("Real().After(10ms) did not fire within 1s")
	}
}

func TestReal_NowIsMonotonicallyIncreasing(t *testing.T) {
	c := Real()
	a := c.Now()
	time.Sleep(time.Millisecond)
	b := c.Now()
	if !b.After(a) {
		t.Fatalf("Now() did not advance: a=%v b=%v", a, b)
	}
}

func TestReal_NewTimerAndTickerFire(t *testing.T) {
	c := Real()
	timer := c.NewTimer(5 * time.Millisecond)
	select {
	case <-timer.C():
	case <-time.After(time.Second):
		t.Fatal("real timer did not fire")
	}

	ticker := c.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	select {
	case <-ticker.C():
	case <-time.After(time.Second):
		t.Fatal("real ticker did not fire")
	}
}
