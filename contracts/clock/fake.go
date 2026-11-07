package clock

import (
	"sync"
	"time"
)

// entry — одна ожидающая точка срабатывания: одноразовая (period == 0, от
// After/Timer) или повторяющаяся (period > 0, от Ticker).
type entry struct {
	deadline time.Time
	period   time.Duration
	ch       chan time.Time
}

// FakeClock — часы для тестов, проматываемые вручную. Время само по себе не
// движется; вызывающий код управляет им через Advance, а BlockUntil
// синхронизируется с горутиной под тестом перед проматыванием — без него
// Advance мог бы обогнать горутину, которая ещё не зарегистрировала своё ожидание.
type FakeClock struct {
	mu      sync.Mutex
	cond    *sync.Cond
	now     time.Time
	waiters []*entry
}

// NewFake возвращает FakeClock, чей Now() стартует со start.
func NewFake(start time.Time) *FakeClock {
	c := &FakeClock{now: start}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *FakeClock) After(d time.Duration) <-chan time.Time {
	return c.register(d, 0).ch
}

func (c *FakeClock) NewTimer(d time.Duration) Timer {
	return &fakeTimer{c: c, e: c.register(d, 0)}
}

func (c *FakeClock) NewTicker(d time.Duration) Ticker {
	return &fakeTicker{c: c, e: c.register(d, d)}
}

func (c *FakeClock) register(d, period time.Duration) *entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := &entry{deadline: c.now.Add(d), period: period, ch: make(chan time.Time, 1)}
	c.waiters = append(c.waiters, e)
	c.cond.Broadcast()
	return e
}

// removeLocked убирает e из c.waiters, если он там есть, и сообщает, был ли он
// ещё активен (та же семантика, что у time.Timer.Stop/Reset).
func (c *FakeClock) removeLocked(e *entry) bool {
	for i, w := range c.waiters {
		if w == e {
			c.waiters = append(c.waiters[:i], c.waiters[i+1:]...)
			return true
		}
	}
	return false
}

// Advance двигает now вперёд на d и срабатывает у каждого waiter'а, чей дедлайн
// наступил. Одноразовый срабатывающий убирается; тикер перепланируется,
// пропуская интервалы, которые он проспал (coalescing, как у time.Ticker), и
// роняет тик, если канал уже держит непотреблённый.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	now := c.now

	remaining := make([]*entry, 0, len(c.waiters))
	for _, e := range c.waiters {
		if e.deadline.After(now) {
			remaining = append(remaining, e)
			continue
		}
		select {
		case e.ch <- now:
		default:
		}
		if e.period <= 0 {
			continue
		}
		next := e.deadline.Add(e.period)
		for !next.After(now) {
			next = next.Add(e.period)
		}
		e.deadline = next
		remaining = append(remaining, e)
	}
	c.waiters = remaining
	c.cond.Broadcast()
}

// BlockUntil блокируется, пока не зарегистрировано хотя бы n waiter'ов
// (вызовы After/NewTimer/NewTicker, ещё не сработавшие и не остановленные).
func (c *FakeClock) BlockUntil(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.waiters) < n {
		c.cond.Wait()
	}
}

type fakeTimer struct {
	c *FakeClock
	e *entry
}

func (t *fakeTimer) C() <-chan time.Time { return t.e.ch }

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	wasLive := t.c.removeLocked(t.e)
	t.c.cond.Broadcast()
	return wasLive
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	wasLive := t.c.removeLocked(t.e)
	// Drain a pending, unconsumed tick from a prior expiry — matching stdlib's
	// guarantee that a receive after Reset cannot see a value from the
	// previous setting.
	select {
	case <-t.e.ch:
	default:
	}
	t.e.deadline = t.c.now.Add(d)
	t.c.waiters = append(t.c.waiters, t.e)
	t.c.cond.Broadcast()
	return wasLive
}

type fakeTicker struct {
	c *FakeClock
	e *entry
}

func (t *fakeTicker) C() <-chan time.Time { return t.e.ch }

func (t *fakeTicker) Stop() {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	t.c.removeLocked(t.e)
	t.c.cond.Broadcast()
}
