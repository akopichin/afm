# Stage Artifacts — передача контекста между стадиями

## Проблема

Сейчас стадии flowManager изолированы друг от друга. `depends_on` гарантирует порядок запуска, но не передаёт данные. Стадия-потребитель не видит ни план, ни результаты зависимой стадии.

## Решение

Два новых поля в Stage YAML: `artifacts` (что стадия производит) и `inputs` (что стадия потребляет). Планы зависимых стадий подтягиваются автоматически через `depends_on`.

## YAML-формат

```yaml
stages:
  - id: backend
    name: "Backend API"
    description: "Реализовать REST API"
    agents: [planning, implementation]
    artifacts:
      - name: api-contract
        path: docs/api-contract.yaml
        description: "OpenAPI schema для frontend"
      - name: db-schema
        path: ./schema.sql              # ./ = относительно stage-директории в run
        description: "SQL миграция"
        inline: false   # по умолчанию true

  - id: frontend
    name: "Frontend"
    depends_on: [backend]
    agents: [planning, implementation]
    inputs:
      - backend.api-contract
      - ref: backend.db-schema
        optional: true
```

### Поля `artifacts[]`

| Поле | Обязательно | Описание |
|------|-------------|----------|
| `name` | да | Уникальное имя артефакта в рамках стадии |
| `path` | да | Путь к файлу. Относительный от корня проекта, или от stage-директории если начинается с `./` |
| `description` | да | Описание для промпта агента-потребителя |
| `inline` | нет | Вставлять содержимое в промпт (true, по умолчанию) или передать путь к файлу (false) |

### Поля `inputs[]`

Каждый элемент — строка `"<stage-id>.<artifact-name>"` или объект:

| Поле | Обязательно | Описание |
|------|-------------|----------|
| `ref` | да | Ссылка формата `<stage-id>.<artifact-name>` |
| `optional` | нет | Если true — отсутствие файла не блокирует стадию (по умолчанию false) |

## Как контекст попадает в промпт

### Планы зависимых стадий (автоматически)

Для каждой стадии из `depends_on` план инлайнится в промпт:

```
## Context from dependent stages

### Stage: Backend API (backend)
<содержимое .flowManager/runs/<run>/backend/plan.md>
```

### Именованные артефакты (через `inputs`)

Для `inline: true` (по умолчанию):

```
## Artifacts

### api-contract (from backend): OpenAPI schema для frontend
<содержимое docs/api-contract.yaml>
```

Для `inline: false`:

```
## Artifacts

### db-schema (from backend): SQL миграция
File path: .flowManager/runs/<run>/backend/schema.sql
(Use Read tool to access this file)
```

### Порядок секций в промпте

1. Шаблон (template)
2. Stage name + description + skills
3. Context from dependent stages (планы зависимых стадий)
4. Artifacts (именованные артефакты из `inputs`)
5. Plan (только для implementation агента)

## Валидация

### На этапе парсинга flow.yaml

- Дубликат `artifact.name` в рамках одной стадии → ошибка парсинга
- `inputs` ссылается на несуществующий `stage-id` → ошибка парсинга
- `inputs` ссылается на несуществующий `artifact-name` → ошибка парсинга
- `inputs` ссылается на стадию, которой нет в `depends_on` → ошибка (нет гарантии порядка)

### На этапе запуска стадии (runtime)

- Обязательный артефакт, файл не найден → стадия получает `failed`, не запускается
- `optional: true`, файл не найден → warning в лог, стадия запускается без артефакта
- План зависимой стадии не найден → warning в лог

### Что НЕ валидируем

- Содержимое артефакта — flowManager не знает формат
- Размер файла — ответственность автора flow

## Изменения в коде

### `pkg/flow/flow.go` — типы и парсинг

Новые типы:

```go
type Artifact struct {
    Name        string `yaml:"name"`
    Path        string `yaml:"path"`
    Description string `yaml:"description"`
    Inline      *bool  `yaml:"inline"` // nil = true по умолчанию
}

type Input struct {
    Ref      string `yaml:"ref"`
    Optional bool   `yaml:"optional"`
}
```

Новые поля в `Stage`:

```go
Artifacts []Artifact `yaml:"artifacts"`
Inputs    []Input    `yaml:"inputs"`
```

`Input` поддерживает unmarshalling из строки (`"backend.api-contract"`) и из объекта (`{ref: ..., optional: ...}`) через кастомный `UnmarshalYAML`.

Валидация в `flow.validate()`:
- `inputs[].ref` ссылается на существующий `stage.artifact`
- Stage из `inputs` присутствует в `depends_on`

### `pkg/orchestrator/orchestrator.go` — промпты и runtime

- `buildPlanningPrompt` и `buildImplementationPrompt` получают контекст зависимых стадий
- Новая функция `collectStageContext(runDir string, stage flow.Stage, allStages []flow.Stage) string` — собирает планы из `depends_on` и содержимое/пути артефактов из `inputs`
- Runtime-валидация файлов артефактов перед запуском стадии

### Затронутые файлы

1. `pkg/flow/flow.go` — типы, парсинг, валидация
2. `pkg/flow/flow_test.go` — тесты парсинга и валидации
3. `pkg/orchestrator/orchestrator.go` — промпты, runtime проверки
4. `pkg/orchestrator/orchestrator_test.go` — тесты промптов
