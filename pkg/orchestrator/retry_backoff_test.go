package orchestrator

import (
	"testing"
	"time"
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
		got := jittered(d)
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
			got := jittered(d)
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
			got := jittered(backoffFor(attempt, base, limit))
			if got > limit {
				t.Fatalf("jittered(backoffFor(%d, %v, %v)) = %v > max", attempt, base, limit, got)
			}
		}
	}
}

// TestJittered_ZeroStaysZero — нулевая пауза не должна стать отрицательной или паниковать.
func TestJittered_ZeroStaysZero(t *testing.T) {
	if got := jittered(0); got != 0 {
		t.Errorf("jittered(0) = %v, want 0", got)
	}
}
