# Testable time

`clock.Clock` (`Now`/`After`/`NewTimer`/`NewTicker`) заменяет прямой
`time.Now()`/`time.After`/`time.NewTimer`/`time.NewTicker` в reliability-core,
чтобы тесты могли `clock.NewFake(start).Advance(d)` вместо реального сна.
`FakeClock.BlockUntil(n)` ждёт, пока горутина реально подпишется на
таймер/тикер — иначе `Advance` может промотать время раньше подписки.

- **Nil-safe `clk()`-аксессор в каждом потребителе**, не прямое поле:
  `Orchestrator.clock`, `concurrency.Manager.Clock`, `executor.Config.Clock`,
  пакетный `pkg/state`'s `clk()` (var + мьютекс — `state.Open` имеет >200
  call sites, threading `Options.Clock` через все них несоразмерно). Nil
  везде означает `clock.Real()`; прод-поведение не меняется, если вызывающий
  явно не передал часы (`cmd/afm` не передаёт).
- Прямое обращение к полю (`o.clock.X()`) паникует на nil — десятки тестов
  конструируют `&Orchestrator{}`/`&Executor{}` литералом в обход конструктора.
- `orchestrator.New` НЕ прокидывает `opts.Clock` в `conc.Clock`: fake-часы
  гонят `WaitAgents`'ов drain-таймаут мгновенно и рвут happens-before
  `cancel → WaitAgents → Close`. Тесты `concurrency`, которым нужен
  fake-driven drain-таймаут, ставят `Manager.Clock` на отдельном `Manager`,
  в обход `Orchestrator`.
- `parseEventLog` (replay) использует историческое `t.Time` из лога, не
  текущие часы — в отличие от `Store.Apply`/`AppendControl`/`NewRunState`.

См. также [state/AGENTS.md](../state/AGENTS.md) (`Persistent run timers`) и
[orchestrator/AGENTS.md](../orchestrator/AGENTS.md) (retry/backoff, dialog poller).
