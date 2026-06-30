# File-based Dialog — замена MCP ask_user на файловый протокол

**Дата:** 2026-06-15
**Статус:** Design approved, ready for implementation plan

## Проблема

Два независимых дефекта в текущей реализации интерактивного диалога:

1. **Контекст не виден рядом с вопросом.** Агент пишет развёрнутый текст (описание вариантов), затем вызывает `ask_user`. В UI текст агента попадает в `$dialogHistory` (история), а в `$dialogPending` показывается только сам вопрос с кнопками. История может быть свёрнута, пользователь не видит контекст рядом с формой ответа.

2. **45-секундный timeout в MCP.** Claude CLI имеет внутренний таймаут MCP-вызовов (~60 с). Сервер вынужден возвращать `"pending"` каждые 45 с, агент повторяет вызов. Хрупко, сложно, создаёт шум в логах.

## Решение

Убрать MCP-сервер (`pkg/mcp/server.go`) полностью. Заменить файловым протоколом:

- Агент пишет `<phase>.q<N>.question.json` — весь контекст + вопрос в одном markdown-поле.
- Агент ждёт `<phase>.q<N>.answer.json` через bash-loop (без таймаута на уровне протокола).
- Оркестратор polling-горутином (1 с) детектирует новый вопрос-файл → событие в UI.
- Пользователь отвечает через существующий POST handler → оркестратор атомарно пишет `answer.json`.

Проблема контекста решается на уровне агента: весь markdown (описание вариантов и т.д.) пишется в поле `question`. UI уже рендерит это поле с markdown в `$dialogPending` — никаких изменений в JS/CSS/HTML не нужно.

## 1. Файловый протокол

### Layout в директории стейджа

```
<stage-id>/
  planning.q1.question.json      ← агент пишет (Write tool)
  planning.q1.answer.json        ← оркестратор пишет (атомарно: tmp → rename)
  planning.q2.question.json
  planning.q2.answer.json
  planning.dialog.jsonl          ← оркестратор ведёт (история для UI, как сейчас)

  implementation.q1.question.json
  implementation.q1.answer.json
  implementation.dialog.jsonl

  review.q1.question.json
  review.q1.answer.json
  review.dialog.jsonl
```

Файлы создаются лениво. Не-интерактивные стейджи не создают ни одного из них.

### Формат `question.json`

```json
{
  "id": "q1",
  "question": "## Какой подход к аутентификации?\n\n**Вариант A: JWT**\n...\n\n**Вариант B: Sessions**\n...\n\nКакой выбрать?",
  "options": ["JWT", "Sessions"],
  "allow_custom": true
}
```

- `id` — строка, стабильный идентификатор вопроса (`q1`, `q2`, …).
- `question` — произвольный markdown любой длины. Весь контекст — сюда.
- `options` — необязательный массив строк (кнопки в UI).
- `allow_custom` — необязательный bool, default `true`.

### Формат `answer.json`

```json
{
  "id": "q1",
  "answer": "JWT",
  "from_options": true
}
```

ID совпадает с `question.json` — агент верифицирует что ответ на его вопрос.

### Атомарность

Оркестратор пишет `answer.json` через tmp + rename:

```
<phase>.q<id>.answer.json.tmp  →  rename  →  <phase>.q<id>.answer.json
```

`rename(2)` атомарен на POSIX. Агент в bash-loop никогда не прочитает частично записанный файл.

## 2. Поток агента

### Env-переменная

Executor передаёт агенту: `FLOWMANAGER_STAGE_DIR=<абсолютный путь к стейдж-директории>`.

### Системный промпт (интерактивные стейджи)

Добавляется одна инструкция:

> «Для вопросов к пользователю используй файловый протокол. Присваивай вопросам последовательные id: `q1`, `q2`, … в порядке возрастания.
>
> Для каждого вопроса:
> 1. Напиши файл `$FLOWMANAGER_STAGE_DIR/<phase>.q<N>.question.json` через Write tool. В поле `question` помести весь контекст: описание вариантов, объяснение — любой markdown.
> 2. Жди ответа: `Bash("while [ ! -f $FLOWMANAGER_STAGE_DIR/<phase>.q<N>.answer.json ]; do sleep 30; done && cat ...")`
> 3. Если bash завершился по таймауту (10 мин) без файла — запусти bash-loop снова с тем же путём.»

### Пример

```
# Вопрос 1
Write("$FLOWMANAGER_STAGE_DIR/planning.q1.question.json", {
  "id": "q1",
  "question": "## Выбор подхода\n\n**JWT**: stateless...\n**Sessions**: stateful...\n\nКакой?",
  "options": ["JWT", "Sessions"],
  "allow_custom": true
})
Bash("while [ ! -f .../planning.q1.answer.json ]; do sleep 30; done && cat .../planning.q1.answer.json")
# → {"id":"q1","answer":"JWT","from_options":true}

# Вопрос 2 — то же с q2
```

Bash-loop спит 30 с между проверками. Если Claude Code прерывает через 10 мин — агент вызывает Bash повторно с тем же путём (инструкция в промпте). Пользователь вообще не ограничен по времени ответа.

## 3. Polling-горутин оркестратора

Запускается при старте оркестратора, живёт до контекста отмены.

```go
func (o *Orchestrator) startQuestionPoller(ctx context.Context) {
    go func() {
        ticker := time.NewTicker(time.Second)
        defer ticker.Stop()
        processed := map[string]bool{} // "stageID|phase|qID" → true

        for {
            select {
            case <-ticker.C:
                o.pollQuestions(ctx, processed)
            case <-ctx.Done():
                return
            }
        }
    }()
}
```

### Логика `pollQuestions`

1. Берёт стейджи в статусах `planning`, `running`, `revising`, `retrying`, `awaiting_user_input`.
2. Для каждого сканирует `<stage-dir>/*.question.json` (glob). Из имени файла `planning.q1.question.json` извлекает `phase=planning`, `id=q1` — отсечь суффикс `.question.json`, разбить по первой точке.
3. Для каждого найденного файла:
   - Если ключ `stageID|phase|qID` уже в `processed` → пропуск.
   - Если соответствующий `*.answer.json` уже существует → помечает processed, пропуск (вопрос уже отвечен).
   - Иначе — новый вопрос:
     - Парсит question.json.
     - `FindEntry(dialog.jsonl, id)` — если уже есть → не дублирует в dialog.
     - `AppendQuestion(dialog.jsonl, ...)` — для истории UI.
     - `bus.Publish(EventAskUser, ...)`.
     - `o.Trigger(stageID, EvAskUser, ...)` → статус `awaiting_user_input`.
     - Добавляет в `processed`.

### Restart-логика

При перезапуске оркестратора `processed` пуст. Горутин сканирует заново:
- Для пар (question.json + answer.json) → помечает processed, ничего не публикует.
- Для вопросов без ответа → публикует EventAskUser снова, UI показывает вопрос.
- `FindEntry` предотвращает дублирование в `dialog.jsonl`.

## 4. Поток ответа

`POST /api/stages/<id>/dialog/answer` — минимальные изменения в `handleDialogAnswer`:

```go
// Существующее: AppendAnswer(dialog.jsonl, ...) + dialogAnswerFn(...)

// НОВОЕ: атомарная запись answer.json для агента
answerPath := filepath.Join(s.runDir, stageID,
    req.Phase+"."+req.ID+".answer.json")
payload, _ := json.Marshal(map[string]any{
    "id": req.ID, "answer": req.Answer, "from_options": req.FromOptions,
})
tmp := answerPath + ".tmp"
if err := os.WriteFile(tmp, payload, 0644); err == nil {
    _ = os.Rename(tmp, answerPath)
}
```

После rename агент в bash-loop читает файл в течение ≤30 секунд.

Статус стейджа, WebSocket-события, история диалога — без изменений.

## 5. UI

**Изменений в `pkg/web/` нет.**

Поле `question` в `$dialogPending` уже рендерится через `renderMarkdownInto` (markdown-it). Агент помещает весь контекст в `question` — пользователь видит его прямо над кнопками ответа.

История `$dialogHistory` продолжает показывать `agent_text` из stream-jsonl и ответные пары — поведение не меняется.

## 6. Что удаляется

| Файл | Действие | Объём |
|------|----------|-------|
| `pkg/mcp/server.go` | Удалить целиком | ~370 строк |
| `pkg/mcp/server_test.go` | Удалить целиком | ~200 строк |
| `pkg/orchestrator/mcp_notifier.go` | Удалить (логика переходит в polling-горутин) | ~60 строк |

Итого: −630 строк сложного кода (JSON-RPC, waiter-каналы, pollingTimeout, Notifier-интерфейс).

## 7. Что меняется

| Файл | Изменения | Δ строк |
|------|-----------|---------|
| `pkg/mcp/dialog.go` | Добавить `FindUnansweredQuestions(stageDir)` — сканирует `*.question.json` без пары `*.answer.json` | +25 |
| `pkg/orchestrator/orchestrator.go` | Добавить `startQuestionPoller` + `pollQuestions`. Убрать создание MCP-сервера | +60 / −10 |
| `pkg/server/handlers.go` | В `handleDialogAnswer`: атомарная запись `answer.json` | +15 |
| `pkg/executor/executor.go` | Убрать генерацию `mcp.json`, убрать `--mcp-config`. Добавить `FLOWMANAGER_STAGE_DIR` в env | −15 / +5 |
| Системный промпт интерактивных стейджей | Заменить инструкцию про `ask_user` MCP на файловый протокол | small |

Чистый итог: **−555 строк** (убираем больше, чем добавляем).

## 8. Тесты

### `pkg/mcp/dialog_test.go`

- `FindUnansweredQuestions`: директория с несколькими вопросами — часть отвечена, часть нет. Возвращает только неотвеченные.
- Существующие тесты `AppendQuestion`/`AppendAnswer`/`ReadDialog` — без изменений.

### `pkg/orchestrator/orchestrator_test.go` (расширение)

- Polling-горутин: fake-стейдж-директория с question.json без answer.json → EventAskUser публикуется в течение 2 с.
- Идемпотентность: тот же question.json сканируется дважды → EventAskUser публикуется ровно один раз.
- Restart: processed очищается, горутин перезапускается → вопросы без ответа переобнаруживаются.

### `pkg/server/handlers_test.go` (расширение)

- `POST /dialog/answer` → `answer.json` создаётся атомарно с правильным содержимым.
- `answer.json` не создаётся если вопрос не найден в `dialog.jsonl`.

### `pkg/orchestrator/integration_interactive_test.go` (расширение)

- Полный цикл: fake-runner пишет `q1.question.json` → горутин детектирует → POST answer → `q1.answer.json` создаётся → fake-runner читает → стейдж завершается.

## 9. Не в скоупе (YAGNI)

- Несколько параллельных вопросов от агента одновременно (крайне редко для interactive CLI-агента).
- Watchman/fsnotify вместо polling (polling 1 с достаточно, меньше зависимостей).
- Миграция существующих runs с dialog.jsonl — файлы лениво создаются, старые runs продолжают работать.

## Риски

| Риск | Митигация |
|------|-----------|
| Агент не следует файловому протоколу (пишет в неверный путь) | Системный промпт с явным примером. Горутин не найдёт файл → вопрос просто не появится в UI, агент получит timeout через 10 мин. |
| Claude Code прерывает bash-loop через 10 мин | Агент вызывает Bash повторно (инструкция в промпте). Пользователь может отвечать сколько угодно долго. |
| question.json повреждён (невалидный JSON) | `json.Unmarshal` в горутине вернёт ошибку → логируем, пропускаем файл. Агент получит timeout, может переписать файл при resume. |
| Двойная запись в dialog.jsonl при restart | `FindEntry` проверяет id перед `AppendQuestion` — дубликатов нет. |
