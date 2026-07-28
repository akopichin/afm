# Дизайн: один источник правды для формата `plan.v{N}.md`

**Дата:** 2026-07-28
**Статус:** согласован, готов к плану реализации

## Контекст

Более широкий обзор кодовой базы на предмет дублирования (см. запрос
пользователя — пример с `defaultCommand = "claude"` / `claudeCommand =
"claude"`, продублированным в 5 местах) выявил несколько категорий проблем.
Эта спека закрывает один конкретный пункт, выбранный как первый приоритет:
**два независимых алгоритма для файловой конвенции `plan.v{N}.md`**, которые
могут разойтись, если формат имени когда-нибудь поменяется в одном месте и
не поменяется в другом. Остальные находки обзора (общая консолидация
констант/имён на бэкенде и фронтенде, другие пункты дублирования логики,
перегруженные сигнатуры) — предмет отдельных последующих спек/планов, вне
охвата этой задачи.

## Проблема

`state.VersionPlan(stageDir)` (`pkg/state/state.go:253-274`) вызывается при
клике «Revise» (`cmd/afm/revise.go:59`, `pkg/orchestrator/control_api.go:117`)
— архивирует текущий `plan.md` в `plan.v{N}.md`, находя следующий свободный
`N` **перебором `os.Stat` от 1**, и возвращает найденный `n`. Оба
вызывающих места **выбрасывают** это возвращаемое значение
(`if _, err := state.VersionPlan(stageDir); err != nil { ... }`).

Следом `runPlanningWithFeedback` (`pkg/orchestrator/agents.go:113-135`)
самостоятельно пересканирует ту же директорию своим regexp'ом
(`^plan\.v(\d+)\.md$`), находит максимальную версию среди файлов и читает её
содержимое как `prevPlan` для промпта — то есть заново вычисляет то самое
число, которое уже было посчитано строчкой раньше и тут же выброшено.

Threading числа через память (например, через сигнатуру `spawnAgent`)
не решает задачу до конца: `runPlanningWithFeedback` также вызывается путём
восстановления после краша (`recovery.go`, стадия застряла в `revising`) —
в этом случае никакого «числа из памяти» не существует, единственный
доступный источник — файловая система. Поэтому сканирование директории
неизбежно в любом случае; дублируется сам **алгоритм** сканирования, а не
факт обращения к диску.

Побочная находка по пути: `runPlanningWithFeedback` сейчас тихо глотает
ошибку `os.ReadDir` (`entries, _ := os.ReadDir(stageDir)`) — это единственное
место, читающее список файлов стадии без проверки ошибки.

## Решение

Новая экспортируемая функция в `pkg/state/state.go` (там же, где уже живёт
`VersionPlan` — никакого нового пакета/файла не создаём):

```go
// LatestPlanVersion scans stageDir for plan.v{N}.md files and returns the
// highest N found (0 if none) along with that file's content ("" if none).
func LatestPlanVersion(stageDir string) (version int, content string, err error) {
    entries, err := os.ReadDir(stageDir)
    if err != nil {
        return 0, "", fmt.Errorf("read stage dir: %w", err)
    }

    best := 0
    var bestName string
    for _, e := range entries {
        name := e.Name()
        if !strings.HasPrefix(name, "plan.v") || !strings.HasSuffix(name, ".md") {
            continue
        }
        numPart := strings.TrimSuffix(strings.TrimPrefix(name, "plan.v"), ".md")
        n, convErr := strconv.Atoi(numPart)
        if convErr != nil || n <= best {
            continue
        }
        best = n
        bestName = name
    }
    if bestName == "" {
        return 0, "", nil
    }
    data, err := os.ReadFile(filepath.Join(stageDir, bestName))
    if err != nil {
        return 0, "", fmt.Errorf("read %s: %w", bestName, err)
    }
    return best, string(data), nil
}
```

Без `regexp` — `strings.HasPrefix`/`HasSuffix` + `strconv.Atoi` естественно
отвергает мусорные имена (`plan.vX.md`, `plan.v1extra.md` и т.п.), поскольку
`Atoi` падает на нечисловой середине.

**`VersionPlan`** переходит на `latest+1` вместо петли `os.Stat`:

```go
func VersionPlan(stageDir string) (int, error) {
    planFile := filepath.Join(stageDir, "plan.md")
    if _, err := os.Stat(planFile); err != nil {
        return 0, fmt.Errorf("plan.md not found: %w", err)
    }
    latest, _, err := LatestPlanVersion(stageDir)
    if err != nil {
        return 0, fmt.Errorf("scan plan versions: %w", err)
    }
    n := latest + 1
    dst := filepath.Join(stageDir, fmt.Sprintf("plan.v%d.md", n))
    if err := os.Rename(planFile, dst); err != nil {
        return 0, fmt.Errorf("rename plan: %w", err)
    }
    return n, nil
}
```

**`runPlanningWithFeedback`** (`pkg/orchestrator/agents.go`) заменяет свой
inline-блок (текущие строки 119-135: чтение `feedback.md`, объявление
`prevPlan`, компиляция regexp, скан `os.ReadDir`, цикл с `FindStringSubmatch`)
на:

```go
feedbackData, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
_, prevPlan, err := state.LatestPlanVersion(stageDir)
if err != nil {
    return fmt.Errorf("read previous plan: %w", err)
}
```

Ошибка `LatestPlanVersion` теперь пробрасывается наверх через retry-closure
(`func(retryContext string) error`) вместо тихого проглатывания — маленькое,
оправданное улучшение по ходу того же изменения, не отдельная задача.

Импорты `regexp` и `strconv` удаляются из `pkg/orchestrator/agents.go` (после
удаления единственного блока, где они использовались, оба импорта становятся
неиспользуемыми).

## Совместимость с существующими тестами

- `TestVersionPlan` (`pkg/state/state_test.go:96`) — создаёт пустой
  `stageDir`, ожидает `n == 1` после первого вызова. С новой реализацией:
  `LatestPlanVersion` на пустой директории вернёт `(0, "", nil)`, `n = 0+1 =
  1` — совпадает.
- `TestIntegration_ResumeFromRevising` (`pkg/orchestrator/integration_resume_test.go:214`)
  — раскладывает `plan.v1.md` + `feedback.md` вручную, проверяет что
  `feedback.md`-контент попадает в промпт. Не завязан на конкретный
  алгоритм чтения `prevPlan`, поведение не меняется.

## Новые тесты

Добавляется `TestLatestPlanVersion` в `pkg/state/state_test.go`, покрывающий:
- Пустая директория → `(0, "", nil)`.
- Единственный `plan.v1.md` → `(1, <его содержимое>, nil)`.
- Несколько версий с пропусками (`plan.v1.md`, `plan.v3.md`) → `(3,
  <содержимое v3>, nil)` — подтверждает, что берётся максимум, а не
  количество файлов.
- Мусорные имена рядом (`plan.vX.md`, `plan.v1.txt`, `plan.md`) не мешают и
  не считаются версией.

## Out of scope

- Остальные пункты обзора (константы `"claude"`/auth env vars/имена тем
  и т.п., консолидация на фронтенде, перегруженные сигнатуры вроде
  `runWithRetry`, крупные файлы) — отдельные последующие спеки/планы.
