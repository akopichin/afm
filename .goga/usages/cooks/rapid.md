# rapid (pgregory.net/rapid)

Property-based тестирование конечного автомата (FSM) — вместо перечисления сценариев вручную,
случайно генерируются последовательности событий и проверяется инвариант. Аудитория: клеточка
`pkg/orchestrator` (тестовый контур `fsm_test.go`).

## Property-тест: "любая последовательность событий рано или поздно завершает FSM"

`rapid.Check` запускает переданную функцию много раз с разными сгенерированными входами (shrinking при
падении — минимизирует контрпример). `rapid.SampledFrom` выбирает случайный элемент из фиксированного
множества значений на каждом шаге:

```go
func TestFSM_Property_LivenessTerminates(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		fsm, store := newTestFSMRapid(t, []string{"a"})
		defer store.Close()

		events := []FSMEvent{
			EvStartPlanning, EvPlanReady, EvApprove, EvRevise,
			EvStartRun, EvComplete, EvFail, EvUserAnswered,
			EvScheduleRetry, EvResumeAfterRetry, EvManualRetry, EvBlockedByDep,
		}

		const maxSteps = 200
		for i := 0; i < maxSteps; i++ {
			ev := rapid.SampledFrom(events).Draw(t, "event")
			_, _, _ = fsm.Apply("a", ev, GuardCtx{Phase: "implementation"}, "")
			if IsTerminal(store.Get("a")) {
				return // property holds for this run
			}
		}
		t.Errorf("did not reach terminal in %d steps; last status: %q", maxSteps, store.Get("a"))
	})
}
```

## Особенности

- `rapid.Check(t, f)` принимает `*testing.T`; внутри `f` работает с `*rapid.T` — методы генераторов
  (`Draw`) вызываются на сгенерированных значениях, а не на `t` верхнего уровня.
- Хелперы, создающие тестовое окружение внутри property-теста (например, `newTestFSMRapid`), принимают
  `*rapid.T`, а не `*testing.T`, чтобы падения корректно участвовали в shrinking.
- Ошибки при переходах FSM (`fsm.Apply` возвращает `error`) внутри property-теста намеренно
  игнорируются (`_, _, _ =`) — тест проверяет только достижимость терминального состояния, а не
  отсутствие ошибок на промежуточных шагах.
- Это чисто тестовая зависимость: не используется в рантайм-коде и не появляется в контракте ни одной
  клеточки, кроме как в аннотациях, описывающих требования к тестам `pkg/orchestrator`.
