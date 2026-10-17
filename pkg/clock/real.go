package clock

import "time"

// realClock — лишённая состояния обёртка над stdlib: прод-поведение — это
// time.* по построению, а не по соглашению.
type realClock struct{}

// Real возвращает прод-часы: Now/After/NewTimer/NewTicker делегируют прямо в
// пакет time.
func Real() Clock { return realClock{} }

func (realClock) Now() time.Time { return time.Now() }

func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func (realClock) NewTimer(d time.Duration) Timer { return &realTimer{t: time.NewTimer(d)} }

func (realClock) NewTicker(d time.Duration) Ticker { return &realTicker{t: time.NewTicker(d)} }

type realTimer struct{ t *time.Timer }

func (r *realTimer) C() <-chan time.Time { return r.t.C }

func (r *realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }

func (r *realTimer) Stop() bool { return r.t.Stop() }

type realTicker struct{ t *time.Ticker }

func (r *realTicker) C() <-chan time.Time { return r.t.C }

func (r *realTicker) Stop() { r.t.Stop() }
