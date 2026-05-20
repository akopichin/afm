# Stage Completion Contract

## Проблема

Сейчас единственный сигнал завершения стадии — exit code процесса AI-агента. Агент может выйти с exit 0, но не выполнить задачу: не создать план, не реализовать фичу, не произвести объявленные артефакты. Зависимые стадии запускаются, получая невалидный контекст.

## Решение

Стадия считается завершённой только при выполнении явного **completion contract**:

- **Planning**: файл `plan.md` существует в stage-директории и не пустой
- **Implementation**: файл `.done` существует в stage-директории и не пустой
- **Artifacts**: все `artifacts` объявленные в секции `artifacts` стадии существуют на диске (стадия обещала их произвести — проверяем)

## Файл `.done`

Агент получает инструкцию в промпте: после завершения всей работы создать файл `.done` в stage-директории через Write tool. Содержимое — свободный текст: summary проделанной работы, список изменений, заметки. Формат не валидируется, проверяется только наличие и непустота.

## Retry-стратегия

### Три уровня retry

**1. Rate limit** (существующий)
- Паттерны: "hit your limit", "rate limit", "too many requests", "overloaded", "capacity"
- Backoff: 5s → 10s → 30s
- Максимум 3 попытки

**2. Server error 500** (новый)
- Паттерны: "500", "internal server error"
- Backoff: 5s → 10s → 30s
- Максимум 3 попытки

**3. Incomplete work** (новый)
- Условие: агент вышел с exit 0, но completion contract не выполнен
- Без backoff — немедленный перезапуск
- Максимум 1 попытка с контекстом из лога ("ты не завершил работу, продолжи и создай .done файл")
- После повторного провала → `failed`
- Исключение: отсутствие обязательных artifacts → сразу `failed` без retry

Rate limit / 500 retry и incomplete retry независимы и могут комбинироваться: внутри incomplete retry агент может словить rate limit и пойти в свой backoff-цикл.

## Промпт-инструкция

В `buildImplementationPrompt` добавляется:

> When you have completed all work for this stage, create a file `.done` in the stage directory `{stageDir}` using the Write tool. Write a brief summary of what you accomplished. This file is REQUIRED — the stage will not be marked as complete without it.

Для planning агента `.done` не требуется — достаточно проверки `plan.md`.

## Проверка завершения

```
checkCompletion(stageDir, stage):
  1. .done существует и не пустой → ok
  2. .done отсутствует или пустой → error("missing .done")
  3. Для каждого обязательного artifact: файл существует → ok
  4. Artifact отсутствует → error("missing artifact: {name}")

checkPlanCompletion(stageDir):
  1. plan.md существует и не пустой → ok
  2. Иначе → error("missing plan.md")
```

## Поведение при resume

При перезапуске `flowmanager run`, если стадия в статусе `running`:
- Проверить `checkCompletion` — если `.done` есть и артефакты на месте → поставить `done` без перезапуска агента
- Иначе → перезапустить implementation агента (как сейчас)

Это покрывает случай: агент создал `.done`, но процесс упал до того как orchestrator обработал event.

## Полная схема

```
Agent exits (exit 0)
    │
    ├─ Planning agent
    │   └─ plan.md существует и не пустой?
    │       ├─ да → EventAgentCompleted("planning") → awaiting_approval
    │       └─ нет → incomplete retry (1 раз с контекстом)
    │               └─ опять нет → failed
    │
    └─ Implementation agent
        └─ checkCompletion:
            ├─ .done существует и не пустой?
            │   └─ нет → incomplete retry (1 раз с контекстом)
            │           └─ опять нет → failed
            └─ обязательные artifacts существуют?
                ├─ да → EventAgentCompleted("implementation") → done
                └─ нет → failed (без retry)

Agent exits (error)
    │
    ├─ rate limit / 500 → backoff retry (до 3 раз)
    └─ другая ошибка → failed
```

## Затрагиваемые файлы

- **`pkg/orchestrator/orchestrator.go`**: `checkCompletion`, `checkPlanCompletion`, расширить `isRateLimitError` → `isRetryableError`, изменить `runWithRetry` для incomplete retry, обновить `runImplementationAgent` и `runPlanningAgent`
- **`pkg/orchestrator/orchestrator.go`** (промпты): добавить инструкцию про `.done` в `buildImplementationPrompt`
- **`pkg/orchestrator/orchestrator.go`** (resume): в `startPlanningForPending` проверять `.done` для стадий в `running`
