package state

import (
	"sync"

	"github.com/akopichin/afm/pkg/clock"
)

// stateClockMu/stateClock — часы пакета; nil означает clock.Real(). Package-level test-seam, тот же приём,
// что applyHook/appendControlHook (store.go): pkg/state не использует Options-инъекцию по экземпляру, в
// отличие от pkg/orchestrator, поэтому для Store.Apply/AppendControl/NewRunState (и легаси-пути в Open,
// где Store ещё не существует) достаточно одного пакетного seam'а.
var stateClockMu sync.Mutex
var stateClock clock.Clock

// clk возвращает текущие часы пакета; nil-safe (clock.Real() по умолчанию).
func clk() clock.Clock {
	stateClockMu.Lock()
	c := stateClock
	stateClockMu.Unlock()
	if c == nil {
		return clock.Real()
	}
	return c
}

// SetClockForTest подменяет часы пакета. Production-код не должен его вызывать. nil сбрасывает на
// clock.Real().
func SetClockForTest(c clock.Clock) {
	stateClockMu.Lock()
	stateClock = c
	stateClockMu.Unlock()
}
