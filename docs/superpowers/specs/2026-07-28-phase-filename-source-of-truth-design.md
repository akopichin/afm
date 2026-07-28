# Дизайн: `pkg/flow` — единственный источник правды для имён файлов по фазе

**Дата:** 2026-07-28
**Статус:** согласован, готов к плану реализации

## Контекст

Продолжение обзора кодовой базы на дублирование (см. `docs/superpowers/specs/2026-07-28-plan-version-scan-dedup-design.md` — уже закрытый пункт про `plan.v{N}.md`). Приоритет 2 из этого обзора — «форматы» (имена фаз/файлов), сведённые к одному источнику правды на бэкенде (фронтенд для `StageStatus` уже централизован — `types/stage.ts` 1:1 совпадает с `state.StageStatus`, отдельная проверка подтвердила; `Phase`-тип на фронте вообще не существует, phase гоняется как непрозрачная строка — вне охвата этой спеки).

`pkg/flow/phase.go` уже объявляет себя единым источником правды для рантайм-фаз (`planning`/`implementation`/`review`/`autonomous_execution`) и даёт `Phases()`, `IsValidPhase()`, `PhaseJSONL()`, `PhaseStreamLogs()`. Но раскопки показали: это верно только для *части* потребителей. Расхождение симметричное — есть и на **читающей**, и на **пишущей** стороне:

- **Читающая сторона** (4 места) вручную реализует список фаз или маппинг фаза→файл вместо вызова `flow.Phases()`/`flow.PhaseJSONL()`.
- **Пишущая сторона** (`pkg/orchestrator/agents.go`) строит имена `.log`-файлов литералами (`"planning.log"`, `"review.log"` и т.п.), вообще не обращаясь к `pkg/flow` — то есть даже если читатели свести к одному источнику, писатель остаётся независимым фактом, который просто пока совпадает вручную.
- Копнув глубже, нашлись ещё 2 читающих места с **уже неполными** ad hoc списками (не просто дублирование, а поведенческий пробел): `pkg/server/handlers.go` и `cmd/afm/check.go` не видят логи `review`/`autonomous`-фаз или части feedback-вариантов.

Итого — 8 точек изменения, все вокруг одного факта: «как называется файл для фазы X». Мелкое, но крупное по охвату изменение — 8 файлов, никакого нового публичного API/поведения кроме двух явных, целенаправленных расширений покрытия (handlers.go, check.go).

## Что уже хорошо (не трогаем)

- Само содержимое stream-json (`.jsonl`) — пишется И парсится одним пакетом, `pkg/executor` (запись в `RunPlanning`/`RunAgent`, парсинг в `ParseToolAction`/`RenderActions`). Никакого дублирования формата записи здесь нет — вопрос пользователя («один пакет формирует и парсит») уже удовлетворён для содержимого файлов. Эта спека — про **имена** файлов, не про их содержимое.
- `state.StageStatus` (10 значений) — уже единственный источник правды на бэкенде, скопирован 1:1 во фронтенд (`types/stage.ts`). Не трогаем.
- Пакетные константы `phasePlanning`/`phaseImplementation`/`phaseReview`/`phaseAutonomous` в `pkg/orchestrator/orchestrator.go:24-27` уже корректно выведены из `flow.Phase` (`phasePlanning = string(flow.PhasePlanning)`) — их **значения** не могут разойтись со значениями `flow.Phase`. Проблема не в значениях, а в том, что *списки*, перечисляющие «все фазы» или «фаза → файл», реализованы вручную в нескольких местах вместо вызова `flow.Phases()`.

## Решение

### 1. Новые функции в `pkg/flow/phase.go`

По образцу уже существующих `PhaseJSONL`/`PhaseStreamLogs`:

```go
// PhaseLogFile returns the phase's canonical human-readable log file name.
// autonomous logs to autonomous.log (not autonomous_execution.log),
// mirroring PhaseJSONL's naming.
func PhaseLogFile(p Phase) string {
	if p == PhaseAutonomous {
		return "autonomous.log"
	}
	return string(p) + ".log"
}

// PhaseLogFiles returns all human-readable log files a phase may produce,
// in chronological/reading order: the canonical file first, then any
// retry/revise variants. Mirrors PhaseStreamLogs but for *.log, not *.jsonl.
func PhaseLogFiles(p Phase) []string {
	switch p {
	case PhasePlanning:
		return []string{"planning.log", "planning-reprompt.log", "planning-revision.log"}
	case PhaseImplementation:
		return []string{"implementation.log", "implementation-feedback.log"}
	case PhaseReview:
		return []string{"review.log", "review-feedback.log"}
	case PhaseAutonomous:
		return []string{"autonomous.log", "autonomous-feedback.log"}
	default:
		return nil
	}
}
```

### 2. Пишущая сторона — `pkg/orchestrator/agents.go`

6 мест, строящих канонические (не вариантные) имена, переводятся на `flow.PhaseLogFile`:

| Строка (текущая) | Было | Стало |
|---|---|---|
| 63 | `filepath.Join(stageDir, "planning.log")` | `filepath.Join(stageDir, flow.PhaseLogFile(flow.PhasePlanning))` |
| 225 | `filepath.Join(stageDir, "implementation.log")` | `filepath.Join(stageDir, flow.PhaseLogFile(flow.PhaseImplementation))` |
| 243 | `filepath.Join(stageDir, "review.log")` | `filepath.Join(stageDir, flow.PhaseLogFile(flow.PhaseReview))` |
| 284 | `filepath.Join(stageDir, "review.log")` | (то же) |
| 337 | `filepath.Join(stageDir, "autonomous.log")` | `filepath.Join(stageDir, flow.PhaseLogFile(flow.PhaseAutonomous))` |
| 430 | `filepath.Join(stageDir, "review.log")` | (то же) |

5 вариантных мест (`planning-reprompt.log` :96, `planning-revision.log` :147, `implementation-feedback.log` :412, `review-feedback.log` :474, `autonomous-feedback.log` :520) **не трогаем** — они специфичны для конкретного retry/feedback-флоу внутри самого `agents.go`, наружу по списку не читаются, абстрагировать их в `pkg/flow` было бы избыточно (YAGNI).

### 3. Читающая сторона — 4 места, дублирующие список/маппинг фаз

- **`pkg/orchestrator/retry.go`** (`buildRetryContext`) — свой `switch phase { case phasePlanning: "planning.jsonl"; ... default: "implementation.jsonl" }` заменяется на `jsonlName := flow.PhaseJSONL(flow.Phase(phase))`. (Поведение идентично на практике: `phase` сюда всегда приходит из `runWithRetry`, вызываемого только с одной из 4 канонических констант — старый `default → "implementation.jsonl"` для мусорного входа никогда не срабатывал и не сработает.)
- **`pkg/server/events_handler.go`** (`reconstructAgentActions`) — цикл `for _, phase := range []string{"planning", "implementation", "review", autonomousLabel}` заменяется на `for _, p := range flow.Phases() { path := filepath.Join(stageDir, flow.PhaseJSONL(p)) ... }`. Требует новый импорт `github.com/akopichin/afm/pkg/flow` в этом файле. Doc-комментарий у `autonomousLabel` (строки 34-37) обновляется — после фикса эта константа используется ТОЛЬКО для лейбла supervisor-track («standard»/«autonomous», Task 3), а не для имени фазы; текущий комментарий ошибочно объединяет два разных понятия (ровно то смешение, на которое указал обзор фронтенда).
- **`pkg/orchestrator/recovery.go`** (`detectInterruptedPhase`) — список `[]string{phasePlanning, phaseImplementation, phaseReview, phaseAutonomous}` заменяется на цикл по `flow.Phases()` (с `string(p)` при вызове `sessionFile`).
- **`pkg/orchestrator/dialog_poller.go`** (`dialogPhases`) — текущая логика (3 базовых фазы + условно autonomous) переписывается через фильтрацию `flow.Phases()`:
  ```go
  func dialogPhases(stageDir string) []string {
      var phases []string
      for _, p := range flow.Phases() {
          if p == flow.PhaseAutonomous && !isAutonomousStage(stageDir) {
              continue
          }
          phases = append(phases, string(p))
      }
      return phases
  }
  ```
  Порядок сохраняется (`Phases()` возвращает Planning, Implementation, Review, Autonomous — тот же порядок, что и раньше).

### 4. Два места с уже неполным покрытием — расширение поведения

- **`pkg/server/handlers.go`** (`handleLog`) — список `[]string{"planning.log", "planning-revision.log", "implementation.log", "review.log", "autonomous.log"}` (сейчас не видит `planning-reprompt.log`/`implementation-feedback.log`/`review-feedback.log`/`autonomous-feedback.log`) заменяется на:
  ```go
  var logContent string
  for _, p := range flow.Phases() {
      for _, name := range flow.PhaseLogFiles(p) {
          data, err := os.ReadFile(filepath.Join(stageDir, name))
          if err == nil {
              logContent += string(data)
          }
      }
  }
  ```
  Порядок обхода (planning → implementation → review → autonomous, внутри фазы — канонический файл, затем варианты) сохраняет читаемый хронологический порядок, который подразумевал текущий (неполный) список.

- **`cmd/afm/check.go`** (`lastLogAction`) — список `[]string{"implementation.log", "planning.log"}` (implementation умышленно проверялся ПЕРЕД planning — предпочтение более поздней фазе, если стадия уже продвинулась) заменяется на:
  ```go
  func lastLogAction(stageDir string) string {
      var last string
      for _, p := range flow.Phases() {
          for _, name := range flow.PhaseLogFiles(p) {
              data, err := os.ReadFile(filepath.Join(stageDir, name))
              if err != nil || len(data) == 0 {
                  continue
              }
              lines := strings.Split(strings.TrimSpace(string(data)), "\n")
              if len(lines) > 0 && lines[len(lines)-1] != "" {
                  last = lines[len(lines)-1]
              }
          }
      }
      if len(last) > 60 {
          last = last[:60] + "..."
      }
      return last
  }
  ```
  Идём по `flow.Phases()` **вперёд** (planning → ... → autonomous) и не возвращаемся из цикла раньше времени — каждое найденное совпадение перезаписывает `last`, поэтому в итоге побеждает лог самой поздней фазы (и самого позднего варианта внутри неё), что естественно продолжает прежнее намерение («implementation бьёт planning») на review/autonomous и на feedback-варианты, вместо жёстко зашитого 2-элементного приоритета.

## Тестирование

- `pkg/flow`: новый `TestPhaseLogFile`/`TestPhaseLogFiles` в уже существующем `pkg/flow/phase_test.go`, по образцу существующих `TestPhaseJSONL`/`TestPhaseStreamLogs` (та же map/slices.Equal-структура) — проверить autonomous-спецкейс и обычный `<phase>.log`/полный список вариантов для каждой фазы.
- `pkg/orchestrator`: существующие `TestBuildRetryContext_*` (retry_test.go), `TestDialogPhases_*` (phase_helpers_test.go) и интеграционные тесты `detectInterruptedPhase`/`recovery` — не должны потребовать изменений в самих тестах, только продолжать проходить.
- `pkg/server`: новый юнит-тест для `reconstructAgentActions` (сейчас нет вообще ни одного прямого теста) — записать `planning.jsonl` и `autonomous.jsonl` (граничный случай спецкейса) в temp-директорию, проверить что обе фазы дают действия в `out`. Существующий `TestHandleLog` должен продолжать проходить без изменений (тестовые данные пишут `planning.log`, который остаётся в списке).
- `cmd/afm`: новый юнит-тест для `lastLogAction` (сейчас нет ни одного) — покрыть: (a) только planning.log → его строка; (b) planning.log + implementation.log → implementation побеждает (как и раньше); (c) только review.log (сценарий, который раньше вообще не работал) → review строка возвращается.

## Out of scope

- `Phase`-тип на фронтенде (там его сейчас нет вообще, phase — непрозрачная строка) — отдельный, гораздо менее срочный пункт (нет дублирования, только отсутствие валидации).
- Остальные пункты категории 1 из первоначального обзора (`"claude"`/auth env vars/`openai`/`cursor` типы, имена тем скина, `.afm`/`runs`-путь, `plan.md`/`autonomous.flag`/`feedback.md` как хардкод-литералы, идентичный таймаут по умолчанию в двух местах) — отдельные последующие спеки.
- Пять «-feedback»/«-reprompt»/«-revision» лог-файлов в `agents.go` не абстрагируются как отдельные константы за пределами `flow.PhaseLogFiles` (используются только для чтения обратно, что уже покрыто) — не создаём избыточную индирекцию там, где она не нужна вызывающему коду.
