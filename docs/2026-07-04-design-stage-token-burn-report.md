# Отчёт: почему токены съелись за 12 минут (flow goga)

**Дата:** 2026-07-04
**Run:** `.afm/runs/Goga feature-20260703-111149`
**Flow:** `goga.yaml`
**Симптом:** после сброса квоты flow перезапущен, за ~12 минут сожжены все доступные токены, стадия `design` упёрлась в 429.

---

## Вердикт

Это **не баг afm в коде**. Это **архитектурный взрыв контекста** в skills `goga-design` / `goga-design-by-changes` / `goga-cell`, помноженный на авто-retry `claude` против ошибки 429.

Лимит (five_hour rate limit) сбрасывается в **14:00 UTC (17:00 МСК)** — перезапуск flow до этого упрётся в тот же лимит.

---

## Где утекли деньги — стадия `design`, фаза `implementation`

Только эта стадия за 4 попытки съела **~$35** ($10.9 + $10.3 + $0 + $14.1). Остальные стадии (propose, brainstorm, apply…) — дешёвые. Проблема локализована чётко.

Run сегодня (`09:06 → 09:16`, ~10 минут wall-clock):

| result | turns | cost | что написал агент |
|---|---|---|---|
| #1 | 29 | $3.71 | «All 5 subagents are running in the background…» |
| #2 | 6 | $7.71 | «I'll wait for the remaining agent completions» |
| #3 | 23 | **$13.46** | «You've hit your session limit» (429) |
| #4–#10 | 1 | **$13.52 → $14.10** | те же «session limit» 7 раз подряд |

---

## Root cause — три уровня

### 1. Мультипликативный контекст через фоновые subagents

Implementation-агент (skill `goga-design` → `goga-design-by-changes` → `goga-cell`) запустил **12 фоновых `Agent`-сабагентов**:
- 5× «Design trace: <пакеты>»
- 7× «Deep verification pkg/<X> cell/CODEMANIFEST»

Каждый `general-purpose` сабагент тянет **копию родительского контекста**. Кумулятивный `cacheRead = 11 802 326` токенов — вместо одного контекста их 12 параллельных.

### 2. Retry против 429, который не останавливается

Орга упёрлась в `five_hour` rate limit (`rate_limit_event`: utilization 0.96 → 0.99 → `rejected`). **Сам `claude` (и сабагенты) ретраит 429 с backoff** — afm тут ни при чём, в `pkg/executor/executor.go` нет retry-логики. 8× `ScheduleWakeup` — сабагенты планируют wake-up и повторяют.

Каждый retry снова пересылает контекст → cost монотонно растёт **даже при `num_turns:1`** ($13.46 → $14.10 в результатах #3 → #10).

### 3. Почему контекст огромный

Миграция на goga (стадия `apply`) наплодила **16 `CODEMANIFEST` + 13 `.usages/`** (по всем пакетам — видны в `git status`). `goga-cell` skills читают их **все**, чтобы построить design doc → контекст распухает до ~500K–1M, и этот объём **копируется в каждый из 12 сабагентов**.

**Дополнительный множитель:** у `design` стоит `agents: [planning, implementation, review]` → 3 фазы последовательно, каждая со своим полным контекстом (`pkg/flow/flow.go:89`). До `review` сейчас не доходит — implementation фейлится первым.

---

## Доказательства из логов

- `design/implementation.jsonl` = **2.9 MB**, `planning.jsonl` = 0 байт, `review.jsonl` нет — работала только implementation-фаза.
- 12 вызовов `Agent`, 293 `tool_use`, 8 `ScheduleWakeup`, 5 `rate_limit_event`, 10 `type:result` (8 с 429), все с одним `session_id`.
- `events.jsonl` (seq 52 / 66 / 75 / 84): четыре фейла `design` подряд, все 429:
  - `2026-07-03 16:06` — $10.91 («org's monthly spend limit»)
  - `2026-07-03 17:28` — $10.29 («session limit · resets 10:20pm UTC») + мгновенный $0 retry
  - `2026-07-04 09:16` — $14.10 («session limit · resets 2pm UTC»)

---

## Что делать

### Прямо сейчас (чтобы не жечь токены)

1. **Не перезапускать flow до 17:00 МСК** — упрётся в тот же 5-часовой лимит.
2. Живой процесс: `docker run … akopichin/afm:latest run goga.yaml` (PID 79639, хост). Контейнер держит dashboard, но если что-то ретраит — прибить:
   - `kill 79639`
   - или `docker ps` → `docker stop <id>`

### Починить, чтобы не повторилось

1. **Главный рычаг:** убрать/ограничить фоновый параллелизм сабагентов в `goga-design` / `goga-cell`. Сейчас 12× клонирование контекста — основная дыра. Варианты:
   - `run_in_background: false` (последовательный режим);
   - жёсткий ceiling на число фоновых агентов (например, 2–3).
2. `goga-cell` должен читать `CODEMANIFEST`/`.usages` **только по изменившимся пакетам** (design-**by-changes** буквально для этого), а не все 16 сразу.
3. Снизить `IdleTimeout` с 30 минут (`pkg/executor/executor.go:78`) — чтобы ретраящийся против 429 агент быстрее умирал, а не висел до таймаута.
4. Не жать manual retry подряд после 429 (в `events.jsonl` видны 4 попытки за полтора дня).

---

## Ключевые файлы для расследования

| Файл | Что показывает |
|---|---|
| `.afm/runs/Goga feature-20260703-111149/design/implementation.jsonl` | stream-json сессии design (2.9 MB), 12 вызовов Agent, 429-retry-loop |
| `.afm/runs/Goga feature-20260703-111149/design/implementation.log` | 10 финальных результатов (8 с 429) |
| `.afm/runs/Goga feature-20260703-111149/events.jsonl` | таймлайн stage-переходов, 4 фейла design подряд |
| `pkg/executor/executor.go:78` | `IdleTimeout = 30 * time.Minute` — рамка для ретраящегося агента |
| `pkg/flow/flow.go:89` | обход `Stage.Agents` → 3 фазы (planning/implementation/review) последовательно |
