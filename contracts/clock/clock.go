// Package clock абстрагирует время за интерфейсом, чтобы тесты могли
// проматывать его вручную вместо реального сна. В проде — Real(), тонкая
// обёртка над stdlib time; в тестах — NewFake() с Advance/BlockUntil.
package clock

import "time"

// Clock — минимальный набор операций над временем, которым пользуется прод-код.
// Sleep в интерфейс не входит: ни одна точка в кодовой базе его не использует,
// After покрывает всё.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
	NewTimer(d time.Duration) Timer
	NewTicker(d time.Duration) Ticker
}

// Timer — часть интерфейса над time.Timer. Reset обязателен: без него нельзя
// было бы реализовать idle-таймер исполнителя (Reset на каждой строке stdout).
type Timer interface {
	C() <-chan time.Time
	Reset(d time.Duration) bool
	Stop() bool
}

// Ticker — часть интерфейса над time.Ticker.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}
