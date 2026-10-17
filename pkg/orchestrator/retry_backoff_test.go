package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/flow"
)

// TestBackoffFor_Schedule закрепляет расписание из D2: 5s 10s 20s 40s 60s, дальше потолок.
func TestBackoffFor_Schedule(t *testing.T) {
	base := 5 * time.Second
	limit := 60 * time.Second
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 60 * time.Second, 60 * time.Second, 60 * time.Second}
	for attempt, w := range want {
		got := backoffFor(attempt, base, limit)
		if got != w {
			t.Errorf("backoffFor(%d, %v, %v) = %v, want %v", attempt, base, limit, got, w)
		}
	}
}

// TestBackoffFor_RespectsCapForLargeAttempt — сдвиг не должен переполнить int64 на больших attempt.
func TestBackoffFor_RespectsCapForLargeAttempt(t *testing.T) {
	base := 5 * time.Second
	limit := 60 * time.Second
	for _, attempt := range []int{62, 1000} {
		got := backoffFor(attempt, base, limit)
		if got != limit {
			t.Errorf("backoffFor(%d, %v, %v) = %v, want exactly max %v", attempt, base, limit, got, limit)
		}
	}
}

// TestBackoffFor_NonDecreasing — пауза не должна убывать с ростом attempt.
func TestBackoffFor_NonDecreasing(t *testing.T) {
	base := 5 * time.Second
	limit := 60 * time.Second
	prev := backoffFor(0, base, limit)
	for attempt := 1; attempt <= 20; attempt++ {
		got := backoffFor(attempt, base, limit)
		if got < prev {
			t.Fatalf("backoffFor(%d) = %v < backoffFor(%d) = %v: not monotonic", attempt, got, attempt-1, prev)
		}
		prev = got
	}
}

// TestBackoffFor_ZeroBase — тесты выставляют нулевой backoff, поведение не должно ломаться.
func TestBackoffFor_ZeroBase(t *testing.T) {
	got := backoffFor(0, 0, 60*time.Second)
	if got != 0 {
		t.Errorf("backoffFor(0, 0, limit) = %v, want 0", got)
	}
	got = backoffFor(5, 0, 60*time.Second)
	if got != 0 {
		t.Errorf("backoffFor(5, 0, limit) = %v, want 0", got)
	}
}

// TestJittered_WithinBounds — джиттер вычитающий: результат в (0.8*d, d].
func TestJittered_WithinBounds(t *testing.T) {
	d := 10 * time.Second
	lower := d - d/5
	for i := 0; i < 1000; i++ {
		got := jittered(d, defaultJitterFrac)
		if got <= lower || got > d {
			t.Fatalf("jittered(%v) = %v, want in (%v, %v]", d, got, lower, d)
		}
	}
}

// TestJittered_NeverExceedsInput — результат никогда не превышает вход, на разных масштабах d.
func TestJittered_NeverExceedsInput(t *testing.T) {
	durations := []time.Duration{1 * time.Millisecond, 100 * time.Millisecond, time.Second, 5 * time.Second, 60 * time.Second}
	for _, d := range durations {
		for i := 0; i < 1000; i++ {
			got := jittered(d, defaultJitterFrac)
			if got > d {
				t.Fatalf("jittered(%v) = %v > input", d, got)
			}
		}
	}
}

// TestJitteredBackoff_NeverExceedsCap — ключевая регрессия D2: заявленный потолок настоящий, а не
// "потолок плюс джиттер". Вычитающий джиттер держит инвариант по построению.
func TestJitteredBackoff_NeverExceedsCap(t *testing.T) {
	base := 5 * time.Second
	limit := 60 * time.Second
	for i := 0; i < 1000; i++ {
		for attempt := 0; attempt <= 20; attempt++ {
			got := jittered(backoffFor(attempt, base, limit), defaultJitterFrac)
			if got > limit {
				t.Fatalf("jittered(backoffFor(%d, %v, %v)) = %v > max", attempt, base, limit, got)
			}
		}
	}
}

// TestJittered_ZeroStaysZero — нулевая пауза не должна стать отрицательной или паниковать.
func TestJittered_ZeroStaysZero(t *testing.T) {
	if got := jittered(0, defaultJitterFrac); got != 0 {
		t.Errorf("jittered(0, defaultJitterFrac) = %v, want 0", got)
	}
}

// TestJittered_ZeroFracReturnsExactNominal — frac<=0 даёт delay == d РОВНО, по построению (T8, dev-plan.md):
// это и есть инвариант, на который опирается Orchestrator.jitterFrac'а нулевое значение по умолчанию у
// структурных литералов в тестах.
func TestJittered_ZeroFracReturnsExactNominal(t *testing.T) {
	for _, d := range []time.Duration{0, time.Millisecond, 5 * time.Second, 60 * time.Second} {
		if got := jittered(d, 0); got != d {
			t.Errorf("jittered(%v, 0) = %v, want exactly %v", d, got, d)
		}
	}
}

// TestRunWithRetry_ZeroJitterFrac_DelayMatchesBackoffForExactly is T8's top-level assertion: with
// jitterFrac==0, the wait between attempts equals EXACTLY backoffFor's nominal for each attempt (including
// the retryBackoffMax ceiling) — stronger than the old "elapsed >= N" style of check, and only possible to
// assert deterministically once the backoff is driven by a fake clock instead of real sleeps.
func TestRunWithRetry_ZeroJitterFrac_DelayMatchesBackoffForExactly(t *testing.T) {
	fc := clock.NewFake(time.Unix(0, 0))
	o, _ := setupHookOrch(t, "s1")
	o.clock = fc
	o.jitterFrac = 0
	o.retryBackoff = 5 * time.Second
	o.retryBackoffMax = 20 * time.Second
	o.maxRetries = 3

	var calls int
	var waits []time.Duration
	var last time.Time
	agentFn := func(string) error {
		now := fc.Now()
		calls++
		if calls > 1 {
			waits = append(waits, now.Sub(last))
		}
		last = now
		return errors.New("connection reset by peer")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation, agentFn, nil,
			func() { t.Error("onUserInterrupted should not be called") })
	}()

	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second} // backoffFor(0..2, 5s, 20s)
	for _, d := range want {
		fc.BlockUntil(1)
		fc.Advance(d)
	}
	<-done

	if calls != len(want)+1 {
		t.Fatalf("agentFn called %d times, want %d (initial + %d retries)", calls, len(want)+1, len(want))
	}
	if len(waits) != len(want) {
		t.Fatalf("recorded %d inter-attempt waits, want %d", len(waits), len(want))
	}
	for i, w := range want {
		if waits[i] != w {
			t.Errorf("wait before attempt %d = %v, want exactly %v", i+2, waits[i], w)
		}
	}
}
