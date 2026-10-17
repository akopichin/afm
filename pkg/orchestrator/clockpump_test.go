package orchestrator_test

import "github.com/akopichin/afm/pkg/clock/clocktest"

// newFakeClockWithPump — алиас на clocktest.NewFakeWithPump. Сам хелпер живёт
// в pkg/clock/clocktest, т.к. Go test-пакеты не шарят неэкспортируемые хелперы
// между package orchestrator_test (этот файл) и package orchestrator
// (hooks_test.go) в одном каталоге.
var newFakeClockWithPump = clocktest.NewFakeWithPump
