// Package clocktest держит тестовые хелперы над pkg/clock, общие для разных
// test-пакетов (internal/external split не даёт шарить неэкспортируемые
// хелперы между package foo и package foo_test в одном каталоге).
package clocktest

import (
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/clock"
)

// NewFakeWithPump возвращает fake-часы, которые фоновая горутина непрерывно
// проматывает (намного больше любого прод-backoff'а) на всё время теста. Тесты,
// которым не важна точная длительность backoff'а (это отдельно проверяется
// TestRunScriptWithRetry_RespectsBackoffTiming), используют это, чтобы
// часозависимый ретрай разрешался почти мгновенно, а не спал реальные секунды.
func NewFakeWithPump(t *testing.T) *clock.FakeClock {
	t.Helper()
	fc := clock.NewFake(time.Unix(0, 0))
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
				fc.Advance(time.Hour)
			}
		}
	}()
	t.Cleanup(func() { close(stop) })
	return fc
}
