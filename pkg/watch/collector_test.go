package watch

import (
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/executor"
)

// fakeClock is a manually-advanced Clock — the injectable time seam Collector needs for
// TestCollector_DeterministicWithFakeClock (mirrors pkg/orchestrator's fingerprintFn/projectDir
// convention: inject the thing that would otherwise make behavior depend on wall-clock timing).
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1700000000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func obs(seq int, content string) executor.Observation {
	return executor.Observation{Seq: seq, Complete: true, Type: executor.ObservationAssistantMessage, Content: content}
}

func substantialContent(n int) string {
	s := make([]byte, n)
	for i := range s {
		s[i] = 'x'
	}
	return string(s)
}

// TestCollector_HundredsOfEventsDoNotSpawnHundredsOfProcesses is the stage's key test: feeding hundreds of
// observations while the collector is (realistically) busy processing the first batch must never produce more
// than a handful of "ready" signals — certainly nowhere near one per observation.
func TestCollector_HundredsOfEventsDoNotSpawnHundredsOfProcesses(t *testing.T) {
	clock := newFakeClock()
	cfg := Config{Debounce: time.Second, Cooldown: time.Second, MaxBatch: 1000, MinNewContentBytes: 10, MaxPasses: 1000}
	c := NewCollector(cfg, clock, nil)

	fires := 0
	for i := 0; i < 500; i++ {
		c.Observe(obs(i, substantialContent(20)))
		// The watcher's own turn is "in flight" (busy) for the whole burst — exactly what happens in
		// production: the main agent keeps emitting output while the watcher's subprocess is still running.
		if i == 0 {
			c.SetBusy(true)
		}
		clock.Advance(10 * time.Millisecond)
		if _, ready := c.Ready(); ready {
			fires++
		}
	}
	if fires != 0 {
		t.Fatalf("collector fired %d times while busy — coalescing is broken", fires)
	}

	// Now the turn ends: exactly ONE more Ready() (after debounce/cooldown settle) should fire, bundling
	// everything that accumulated, not 500 separate turns.
	c.SetBusy(false)
	clock.Advance(2 * time.Second)
	bundle, ready := c.Ready()
	if !ready {
		t.Fatal("expected collector to be ready after busy cleared and debounce/cooldown elapsed")
	}
	if len(bundle.Observations) != 500 {
		t.Fatalf("expected the single fired batch to carry all 500 coalesced observations, got %d", len(bundle.Observations))
	}
	if c.Passes() != 1 {
		t.Fatalf("passes = %d, want 1 (hundreds of events must cost exactly one turn here)", c.Passes())
	}
}

// TestCollector_CoalescesWhileBusy is the narrower, single-assertion sibling of the above: Ready() must report
// false, by construction, for ANY new observation that arrives while busy==true, regardless of how much
// content has piled up.
func TestCollector_CoalescesWhileBusy(t *testing.T) {
	clock := newFakeClock()
	c := NewCollector(Config{Debounce: time.Second, Cooldown: time.Second, MinNewContentBytes: 1, MaxPasses: 10}, clock, nil)
	c.SetBusy(true)
	for i := 0; i < 10; i++ {
		c.Observe(obs(i, "substantial content here"))
	}
	clock.Advance(5 * time.Second)
	if _, ready := c.Ready(); ready {
		t.Fatal("Ready() must never fire while busy")
	}
}

// TestCollector_NoNewSubstantialContentSkipsInvocation: a trickle of tiny observations that never crosses
// MinNewContentBytes must never fire — empty chatter should not spend a model call.
func TestCollector_NoNewSubstantialContentSkipsInvocation(t *testing.T) {
	clock := newFakeClock()
	c := NewCollector(Config{Debounce: time.Second, Cooldown: time.Second, MinNewContentBytes: 1000, MaxPasses: 10}, clock, nil)
	c.Observe(obs(1, "ok"))
	c.Observe(obs(2, "sure"))
	clock.Advance(5 * time.Second)
	if _, ready := c.Ready(); ready {
		t.Fatal("Ready() must not fire when accumulated content is below MinNewContentBytes")
	}

	// Enough substantial content DOES fire.
	c.Observe(obs(3, substantialContent(2000)))
	clock.Advance(5 * time.Second)
	if _, ready := c.Ready(); !ready {
		t.Fatal("Ready() should fire once substantial content crosses the threshold")
	}
}

// TestCollector_MaxPassesEnforced: once MaxPasses turns have fired, the collector must never fire again, and
// the pass that reaches the cap must be reported as Final (T5.4's bounded-final-pass contract).
func TestCollector_MaxPassesEnforced(t *testing.T) {
	clock := newFakeClock()
	c := NewCollector(Config{Debounce: 0, Cooldown: 0, MinNewContentBytes: 1, MaxPasses: 2}, clock, nil)

	var lastBundle Bundle
	for pass := 0; pass < 2; pass++ {
		c.Observe(obs(pass, "substantial content"))
		clock.Advance(time.Second)
		b, ready := c.Ready()
		if !ready {
			t.Fatalf("pass %d: expected Ready()==true", pass)
		}
		lastBundle = b
		c.SetBusy(false)
	}
	if !lastBundle.Final {
		t.Fatal("the pass that reaches MaxPasses must be reported as Final")
	}

	// A further observation + elapsed time must never fire again.
	c.Observe(obs(99, substantialContent(5000)))
	clock.Advance(time.Hour)
	if _, ready := c.Ready(); ready {
		t.Fatal("collector fired beyond MaxPasses")
	}
	if c.Passes() != 2 {
		t.Fatalf("passes = %d, want 2", c.Passes())
	}
}

// TestCollector_DeterministicWithFakeClock: the exact same sequence of Observe/Advance/Ready calls against a
// FakeClock always produces the exact same firing pattern — proof the numeric defaults are pinned in Config,
// not scattered across real timers (dev-plan.md's own framing for this test).
func TestCollector_DeterministicWithFakeClock(t *testing.T) {
	run := func() []bool {
		clock := newFakeClock()
		c := NewCollector(DefaultConfig(), clock, nil)
		var fired []bool
		for i := 0; i < 20; i++ {
			c.Observe(obs(i, substantialContent(30)))
			clock.Advance(500 * time.Millisecond)
			_, ready := c.Ready()
			fired = append(fired, ready)
			if ready {
				c.SetBusy(false)
			}
		}
		return fired
	}
	a := run()
	b := run()
	if len(a) != len(b) {
		t.Fatalf("length mismatch: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("step %d: non-deterministic firing pattern: %v vs %v", i, a, b)
		}
	}
}

// TestCollector_StopPreventsFurtherFiring backs T5.4's "parent failed stops new passes": once Stop is called,
// no further Ready() call ever returns true again, regardless of new observations or elapsed time.
func TestCollector_StopPreventsFurtherFiring(t *testing.T) {
	clock := newFakeClock()
	c := NewCollector(Config{Debounce: 0, Cooldown: 0, MinNewContentBytes: 1, MaxPasses: 100}, clock, nil)
	c.Observe(obs(1, "substantial"))
	c.Stop()
	clock.Advance(time.Hour)
	if _, ready := c.Ready(); ready {
		t.Fatal("Ready() must never fire after Stop()")
	}
}
