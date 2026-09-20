# Фаза 1 — PhaseExecutor: убрать дублирование агентных запусков

**Проект:** `akopichin/afm`
**Дата:** 19 сентября 2026 года
**Статус:** план изменений; код не изменён, тесты в среде анализа не запускались.
**Происхождение:** это самодостаточная выжимка **Фазы 1 из трёх** (`tmp/AFM_REFACTORING_PLAN_3_PHASES.md`). Общий контекст, нужный именно для Фазы 1 (цели, границы, поправки к архитектуре, карта затрагиваемых частей, матрица приёмки по Фазе 1, safety gate, исходники), включён сюда целиком, чтобы фазу можно было исполнять и ревьюить без родительского документа. Фазы 2 (DialogController) и 3 (StageWorkspace) остаются в родительском документе.

---

## 1. Цель и ограничения

Сохранить текущую функциональность AFM, одновременно сократив повторение решений, количество мест для изменения одной возможности и сложность сопровождения. Главный приоритет — надёжность исполнения, восстановления и взаимодействия с пользователем, а не минимальное число строк.

Фаза 1 из согласованной программы трёх фаз:

1. **PhaseExecutor (эта фаза):** объединить повторяющуюся подготовку и выполнение агентных фаз.
2. DialogController: выделить управление файловым диалогом и его временным состоянием. *(вне scope этого файла)*
3. StageWorkspace: довести существующий `stagefiles` до последовательного API для файлов стадии. *(вне scope этого файла)*

Подготовка тестового baseline включена в первую фазу и не является отдельной программой наращивания покрытия. Существующие тесты — основная страховочная сетка. Новые тесты нужны только для конкретной изменяемой границы, если её наблюдаемое поведение ещё не закреплено.

**Не входит в эти три фазы (и, следовательно, не входит в Фазу 1):** новая FSM, новый scheduler, изменение event log, переписывание recovery, новый формат YAML, новый HTTP/MCP-протокол, смена файловой раскладки, AI-verify как новая возможность, объединение memory/jsonfix/script с обычными агентными фазами, общий framework плагинов. Улучшения безопасности и исправления найденных ошибок поведения оформляются отдельно, а не прячутся в «механический рефакторинг».

Нулевые регрессии — целевой критерий приёмки, но не обещание, которое можно доказать одним зелёным прогоном. Нужны сохранение контрактов, проверка опасных сценариев и возможность откатить каждое изменение.

### 1.1. Что проверено и что не проверено

Просмотрены реализация запуска фаз, retry, фабрика runner, scheduling/recovery/control API, файловый poller, обработка диалога в `mcp` и `server`, операции `stagefiles`, часть `state`, review-pause, concurrency, примеры существующих регрессионных тестов, Makefile и CI. Полный инвентаризационный аудит всех файлов backend/frontend не заявляется.

Здесь нет выдуманного процента покрытия, результата `go test`, измеренной экономии строк или подтверждённого SHA HEAD. Источники (§10) — **реальные файлы этого репозитория** по путям в таблице; **перед реализацией зафиксировать SHA локального checkout и сопоставить перечисленные функции/имена с ним** (за время между анализом и реализацией код мог измениться). Перечисленные ниже проверки являются **планом запуска**, а не отчётом об уже выполненных тестах.

## 2. Поправки к первоначальной архитектурной идее (относящиеся к Фазе 1)

### 2.1. Общий механизм retry уже есть

`pkg/orchestrator/retry.go: runWithRetry` уже оборачивает попытки выполнения. Поэтому PhaseExecutor не должен становиться вторым retry-engine. Его полезная граница — подготовка и запуск агентной работы внутри существующего жизненного цикла. [S02]

### 2.2. Тип фаз и файловые помощники уже есть

`pkg/flow/phase.go` содержит `Phase`, `Phases`, `PhaseJSONL`, `PhaseStreamLogs`, `PhaseLogFile`, `PhaseLogFiles`. В `pkg/orchestrator/stagefiles` уже находятся проверки завершения, сбор контекста и операции с session-файлами. Повторять эти сущности под другими именами не нужно. [S04] [S05] [S06] [S07]

### 2.3. Review — не одно и то же во всех путях

В `agents.go` есть самостоятельный review-запуск и review, встроенный в попытку implementation. Это различие нужно сохранить: унификация вызова subprocess не даёт права переносить review на отдельный retry-бюджет или делать его новым scheduler-шагом. [S01]

### 2.6. AI-verify оставить отдельным изменением

В просмотренном `stagefiles/completion.go` проверка `CheckAutonomousCompletion` читает `execution_summary.md`, а `RunVerify` вызывается из `CheckCompletion`. Не следует незаметно добавлять verify в автономный completion в рамках унификации. Проектирование verify-агента — отдельная фича после стабилизации границ. [S05]

## 3. Карта затрагиваемых частей (для Фазы 1)

| Зона | Исходные точки | Почему затрагивается | Ограничение |
|---|---|---|---|
| Агентные фазы | `pkg/orchestrator/agents.go` | Четыре семейства запусков и варианты с feedback | Схлопнуть тела, не изменить маршрут исполнения |
| Повторные попытки | `pkg/orchestrator/retry.go` | Вызов общей работы, completion, interruption | Сохранить алгоритм и бюджет |
| Создание subprocess runner | `pkg/orchestrator/runner_factory.go` | Команды, аргументы, session, CWD, accounting | Не заменить per-stage runner общим экземпляром |
| Фазы и имена логов | `pkg/flow/phase.go` | Уже общий справочник | Использовать, не создавать второй |
| Координация | `orchestrator.go`, `scheduling.go`, `recovery.go`, `control_api.go` | Адаптеры новых компонентов | FSM, CAS и порядок побочных эффектов оставить |
| Review-pause | `pkg/orchestrator/reviewpause.go` | Пересекается с запуском, диалогом и feedback | Только контрактные проверки, не новый контроллер |
| Concurrency | `pkg/orchestrator/concurrency/concurrency.go` | Все обычные и detached-запуски | Сохранить семафор, activity markers и shutdown |

Детали исходных функций: [S01]–[S08], [S12]–[S16]. Размер файла сам по себе не является доказательством плохой архитектуры. Особенно не следует удалять защитные проверки только потому, что рядом есть похожая проверка в другом окне гонки.

## 4. Общая целевая конструкция (владение обязанностями)

```text
Orchestrator
  ├── FSM/Trigger, scheduling, recovery, control API     — прежние владельцы
  ├── runWithRetry                                      — прежний lifecycle попыток
  │     └── PhaseExecutor                               — подготовка/выполнение работы
  │           ├── существующие prompts
  │           └── существующие runnerFor/executor
  └── (DialogController и stagefiles.StageWorkspace — фазы 2 и 3)

State/event log, review-pause transaction, concurrency  — сохраняются
```

Диаграмма описывает **владение обязанностями**, а не требование завести по Go-пакету и интерфейсу на каждую строку.

### 4.1. Правила границ

**PhaseExecutor не решает, когда запускать стадию.** Он не читает DAG ради активации соседей, не владеет lifecycle run, не записывает новый статус напрямую и не создаёт дополнительный семафор.

**Никаких абстракций на вырост.** Не добавлять registry плагинов, DI-контейнер, универсальную шину эффектов, набор middleware для фаз, generic repository или интерфейс файловой системы только ради удобства моков. Если вынос требует передать почти весь `Orchestrator` через десятки callback, граница выбрана слишком широко.

---

## 1.A. Результат фазы

После фазы обычный запуск и запуск с feedback используют одну реализацию соответствующей агентной работы. Общие части контекста и вызова runner не копируются между семействами. Существующие scheduler, FSM, retry, interruption, completion и recovery продолжают работать прежним способом.

**Рекомендуемое размещение первого изменения:** внутри `package orchestrator`, например `phase_execution.go` и `phase_context.go`. Новый самостоятельный пакет не является критерием успеха. Вынос через границу пакета допустим позже, только если для него образовался небольшой естественный контракт и не требуется экспортировать внутреннюю механику оркестратора.

## 1.B. Подготовка: зафиксировать baseline, не переписывать тесты

### Задача 1.1. Зафиксировать исходное состояние

- Записать SHA, версию Go/Node и состояние рабочей копии.
- Снять результат существующего CI-набора на этом SHA.
- Зафиксировать уже существующие сбои и пропущенные тесты отдельно от результатов рефакторинга.
- Не смешивать обновление зависимостей, gofmt по всему репозиторию и переносы файлов с изменением поведения.
- Выписать все места вызова восьми агентных entrypoint-функций; для каждого отметить: кто уже выполнил transition, кто держит concurrency slot, какой context используется.

Начальные команды:

```bash
git status --short
git rev-parse HEAD
go version
node --version

rg -n 'run(Planning|Implementation|Review|Autonomous)(Agent|WithFeedback)' pkg/orchestrator
rg -n 'runWithRetry|runnerFor|spawnKind|setRunnerKind' pkg/orchestrator
rg -n 'Check(Plan|Autonomous)?Completion|RunVerify' pkg
```

`rg` — удобный инструмент инвентаризации, не новая зависимость AFM. При отсутствии можно использовать поиск IDE.

### Задача 1.2. Составить таблицу сохранения поведения

Проверять не только конечный статус, но и состав вызовов runner, файлы и последовательность значимых действий. Для каждого семейства покрыть **fresh** и **feedback**. Retry и восстановление после pause — отдельные условия запуска, а не замена этим двум вариантам.

| Семейство | Контракт, который надо удержать | Особое условие |
|---|---|---|
| Planning | Выходной plan, contract, validation/adoption/reprompt | Не слить revision и fresh в одинаковую подготовку |
| Implementation | Чтение plan, требования к outputs, optional embedded review | Review остаётся в той же попытке |
| Standalone review | Собственный вызов и completion-путь | Не перепутать с embedded review |
| Autonomous | Собственный template/summary-контракт | Не требовать plan или `.done` |
| Fresh/feedback | Сохранить источник добавочного контекста и имя лога | Не передавать pre-note вместо feedback |

Исходный участок для инвентаризации: все `run*Agent` и `run*WithFeedback` в `agents.go`. Таблица является перечнем проверок реализации, а не предложением уравнять различия. [S01]

### Задача 1.3. Проверить существующую тестовую сетку

Сначала найти подходящий тест и расширить его входные варианты. Не создавать рядом второй большой интеграционный harness.

Проверенные примеры существующих опор:

- `orchestrator_test.go`: `TestPlanningPhaseMarksPlanningStatus`, `TestCollectDependencyPlans`.
- `retry_test.go`: `TestBuildRetryContext_FullActionNotTruncated`, `TestBuildRetryContext_MissingLogReturnsEmpty`, `TestIsRetryableError`, `TestRunWithRetry_CompletionMarkerOverridesStaleOpenQuestion`.
- `pause_continue_test.go`: `TestIntegration_AutoRunGatesScriptStage`, `TestIntegration_AutoRunGatesRegularStage`, `TestIntegration_AutoRunGateFiresOnceOnly`.

Эти примеры действительно присутствуют в просмотренных исходниках; это не полный список тестов проекта и не свидетельство их прохождения в текущей среде. [S17] [S18] [S19]

Для каждого изменяемого пути записать: существующий тест → что наблюдает → чего не хватает. Добавлять тест только для последнего столбца.

## 1.C. Спроектировать минимальное объединение

### Задача 1.4. Развести три понятия

1. **Runtime phase** — использовать существующий `flow.Phase`.
2. **Вариант запуска** — обычный или с пользовательским feedback.
3. **Retry/resume-контекст** — вход, который по-прежнему формирует существующий lifecycle.

Не вводить новый YAML-параметр, пользовательскую настройку режима или второй enum фаз. Не смешивать `flow.AgentType`, phase subprocess и runner-kind: эти обозначения участвуют в разных контрактах. [S32]

Внутренний запрос к общему коду может содержать стадию, фазу, вариант и retry-context. Это проектная форма данных, не требование прямо сейчас добавлять exported `PhaseSpec` со множеством опциональных полей. Тип должен иметь только реальные потребители.

### Задача 1.5. Сначала убрать чистые повторы подготовки

Кандидаты на небольшие именованные функции:

- построение блока требований к выходным артефактам;
- сбор общего `prompts.Inputs` без фазоспецифичных полей;
- форматирование пользовательского feedback-блока;
- одинаковая публикация предупреждения о недоступном dependency-context;
- выбор имени лога для варианта запуска без дублирования уже существующего справочника фаз.

**Не делать одновременно:** менять формулировки промптов, порядок блоков, warning-политику и обработку отсутствующих файлов. Даже «эквивалентный по смыслу» новый prompt — уже изменение входа LLM.

При переносе учитывать, что одни чтения в исходном коде находятся до retry-цикла, а другие внутри callback попытки. Сохранить момент чтения. Нельзя ради единого helper случайно превратить повторное чтение в кеш или, наоборот, начать перечитывать ранее зафиксированный контекст.

### Задача 1.6. Объединить четыре пары, а не строить мегаметод

Предпочтительная форма: один общий координирующий вход и четыре короткие, явно различающиеся реализации агентной работы. Например, отдельная функция planning-attempt и отдельная implementation-attempt; режим feedback настраивает входные данные, но не создаёт второе тело фазы.

Старые `run*Agent` / `run*WithFeedback` на переходный период оставить адаптерами. Они сохраняют входную подготовку и необходимые FSM-вызовы, затем передают работу общему коду. Это позволяет не переподключать одновременно scheduling, recovery и HTTP actions.

**Важно:** конечная цель — не обязательное удаление восьми имён. Если тонкие методы объясняют смысл вызова лучше универсальной комбинации флагов, их можно сохранить. Удалить нужно повторяющиеся алгоритмы, а не полезные названия.

### Задача 1.7. Не выделять встроенному review новый lifecycle

Общий helper запуска review может использоваться обоими видами review, но adapter implementation продолжает вызывать его последовательно внутри своей попытки. Не добавлять между implementation и embedded review новый `spawnKind`, новый слот семафора, независимый retry или новый transition.

Приёмочные наблюдения:

- та же последовательность обращений к runner;
- тот же внешний retry-путь при ошибке embedded review;
- то же привязка логов и usage для реально выполняемого subprocess;
- тот же owner/runner-kind для приостановки и возобновления работы стадии;
- самостоятельный review не начинает требовать implementation-plan из-за общего кода.

### Задача 1.8. Сохранить фабрику runner как единственную точку конфигурации

Общий исполнитель должен пользоваться `runnerFor`, а не создавать `executor.New` в каждой фазе. Сначала допустимо вообще не менять фабрику.

Если после объединения полезно убрать повторение её общих полей, вынести только общий initializer, сохранив ветви interactive/non-interactive/fallback. Проверить как минимум:

| Наблюдение | Что сравнивать |
|---|---|
| Команда | Default client и stage override |
| Аргументы | Правила ExtraArgs и ResolveArgs в соответствующей ветви |
| Session | Проверка существования, создание и флаг resume |
| Файловое окружение | StageDir, RootDir/CWD, RunDir, WrapperDir |
| Прерывание | Тот же InterruptCh |
| Телеметрия | StageID, phase, OnAction, OnUsage, UsageHint |
| Тестовая инъекция | Прежние условия использования injected Runner |
| Ошибка session | Прежний fallback, без незаметного «улучшения» его настроек |

Основание — `runner_factory.go`. Не делать общий singleton runner ради сокращения конструкторов. [S03]

## 1.D. Сохранить опасные контракты

### Задача 1.9. Retry и completion не переписывать

Сохранить точные условия и порядок в `runWithRetry`: различение ошибок, сброс session, interruption, ограничение повторов, построение контекста и публикацию результата. Особенно проверить условие дополнительной попытки incomplete-work относительно номера attempt; не заменять его общим правилом «всегда ещё один retry».

Не переносить решение «открытый вопрос против completion marker» одновременно в PhaseExecutor и DialogController. Вызов чтения вопроса можно делегировать, но владелец решения должен остаться один. [S02]

### Задача 1.10. Запуск и завершение оставить под прежней координацией

При миграции адаптеров проверить:

- сохранение `spawnKind` и учёта выполняющейся работы;
- pause во время ожидания concurrency slot;
- pause между допуском к запуску и регистрацией interruption;
- pause во время `script_before`;
- feedback/revise во время работы;
- resume уже выполненной стадии и активацию её dependents;
- отсутствие второго запуска при конкурирующих управляющих действиях;
- порядок завершения run, остановки агентов и финальных уведомлений.

Существующие решения искать в `concurrency.go`, `recovery.go`, `scheduling.go`, `control_api.go` и lifecycle-части `orchestrator.go`. Не заменять эти гарантии новой универсальной функцией `RunPhaseAndTransition`. [S08] [S12] [S13] [S14] [S16]

### Задача 1.11. Не затронуть соседние механизмы

`runScriptStage`, before/after scripts, JSON-fix, memory pipeline и review-pause не становятся обычными phase implementations. У них иные условия завершения, учёта активности или повторов. Допустимо использовать уже существующий низкоуровневый executor; недопустимо унифицировать их lifecycle без отдельной задачи.

## 1.E. Последовательность небольших PR

| PR | Содержание | Обязательное условие merge |
|---|---|---|
| P1.1 | Baseline и карта контрактов; минимальные недостающие characterization-тесты | Зафиксирован результат до изменений; нет production-рефакторинга |
| P1.2 | Чистые helpers подготовки контекста | Prompt-входы и side effects неизменны |
| P1.3 | Первая пара fresh/feedback, затем остальные пары небольшими коммитами | После каждой пары целевые тесты и основной gate |
| P1.4 | Общий вход/адаптеры; удаление оставшегося дублирования | Нет двух реализаций одного алгоритма |
| P1.5 | Необязательный небольшой cleanup фабрики runner | Только доказуемо общие поля; fallback не меняется |

P1.3 можно разделить на несколько PR, если diff перестаёт быть удобно проверяемым. Начать с пары, у которой в актуальном checkout наиболее понятный и уже закреплённый тестами контракт; autonomous обычно проще planning, но это решение подтвердить по тестам, а не по числу строк.

## 1.F. Критерии завершения фазы

- Обычный/feedback запуск каждого семейства использует одно тело агентной работы.
- Нет нового retry-engine и новых публичных настроек.
- Справочник фаз по-прежнему один — `flow.Phase` и связанные функции.
- Сохранены prompt-входы, имена файлов, вызовы runner, transitions и момент чтения контекста.
- Embedded review не стал отдельной scheduler-фазой.
- Пройден общий gate и целевые проверки interruption/recovery.
- **Каждый изменённый путь имеет наблюдающий его тест — жёсткий критерий merge.** Для любого пути (семейство × fresh/feedback, embedded review, retry/interruption/resume-ветвь), затронутого в PR, в PR-отчёте (§6.4) зафиксировано сопоставление `изменённый путь → тест, который его наблюдает`: существующий тест (при необходимости расширенный новым входным вариантом) или новый characterization-тест. Тест должен наблюдать не только конечный статус, но и соответствующую строку контракта из §1.B/§5 (состав вызовов runner, файлы, порядок значимых действий — см. §5.1). **Изменённый путь без наблюдающего теста блокирует merge**; «косвенно покрыт» допустимо только с явной записью в отчёте, почему прямого наблюдения достаточно для этого diff. Это привязка к границам изменения, а не цель по проценту coverage (§6.4, §1.B/Задача 1.3).
- Удалённое дублирование можно показать в diff; добавленная инфраструктура не больше задачи, которую она решает.
- Архитектурные расхождения и реальные баги поведения вынесены в отдельный backlog, а не исправлены попутно.

## 1.G. Карта миграции конкретных entrypoint

Эта таблица помогает не потерять различия при объединении. Она не является новым конфигурационным API. Перед правками сверить её с зафиксированным SHA. [S01] [S04]

| Существующий entrypoint | Что передать общей реализации | Что не уравнивать с соседним вариантом |
|---|---|---|
| `runPlanningAgent` | Planning, fresh, соответствующий retry-context | Подготовку каталога, удаление stale autonomous flag, pre-note, fresh log |
| `runPlanningWithFeedback` | Planning, feedback | Чтение feedback и последней версии плана внутри попытки, revision log |
| `runImplementationAgent` | Implementation, fresh | Pre-note; optional review остаётся частью этой же попытки |
| `runImplementationWithFeedback` | Implementation, feedback | Feedback вместо fresh pre-note; отдельное имя feedback-log |
| `runReviewAgent` | Standalone review, fresh | Собственный completion; контекст зависимостей сейчас собирается до retry |
| `runReviewWithFeedback` | Standalone review, feedback | Feedback-вход и log; не превращать в embedded review |
| `runAutonomousAgent` | Autonomous, fresh | Создание autonomous flag, summary-контракт, собственный prompt |
| `runAutonomousWithFeedback` | Autonomous, feedback | Не повторять автоматически fresh-подготовку каталога/флага |

Отдельно закрепить autonomous-вход `prompts.Inputs.Interactive`: в просмотренном коде он выставляется в `true`, а не просто копируется из `s.Interactive`. Не смешивать эту настройку prompt-протокола с выбором interactive runner. [S01]

В таблице специально не предлагается общий этап «прочитать всё в начале запуска». Он изменил бы свежесть контекста между попытками. Проверка эквивалентности должна учитывать не только значения входов, но и момент их получения.

---

## 5. Матрица сохранения поведения (строки, относящиеся к Фазе 1)

Это матрица обязательных наблюдений, а не утверждение, что для каждой строки требуется новый тест. В P1.1 сопоставить строки с уже имеющимися тестами. Если assertion уже существует, сохранить его; если проверка косвенная, решить, достаточно ли её для конкретного diff. Столбец «Зона риска» указывает, что эти инварианты защищают ещё и Фазы 2/3, но в Фазе 1 они проверяются в первую очередь.

| ID | Контракт | Основная зона риска | Где искать существующую страховку |
|---|---|---|---|
| I01 | Fresh и feedback каждой фазы сохраняют prompt-входы и outputs | Фаза 1 | Orchestrator tests, prompt tests, mock runner |
| I02 | Planning сохраняет validation/adoption/reprompt и версии | Фазы 1, 3 | Planning/revision tests, stagefiles completion |
| I03 | Embedded review остаётся в implementation attempt | Фаза 1 | Runner-call assertions, retry tests |
| I04 | Retry сохраняет бюджет, типы ошибок и session reset | Фазы 1, 3 | `retry_test.go`, session/runner tests |
| I05 | Retry-context сохраняет необходимые action lines | Фаза 1 | Проверки `BuildRetryContext` |
| I06 | Completion и открытый вопрос разрешаются прежним владельцем | Фазы 1, 2 | `TestRunWithRetry_CompletionMarkerOverridesStaleOpenQuestion` |
| I07 | Pause в очереди семафора не допускает запрещённый запуск | Фазы 1, 2 | `concurrency_test.go`, pause/continue tests |
| I08 | Resume выбирает прежний runner-kind и не создаёт двойной запуск | Фазы 1, 2 | Recovery, review-pause, pause/continue scenarios |
| I09 | Завершённая при recovery стадия разблокирует dependents | Фазы 1, 3 | Recovery/scheduling scenarios |
| I10 | Auto-run gate и script stages сохраняют прежние условия | Фаза 1 | Указанные тесты gate для script, regular и fire-once |
| I20 | Verify запускается в прежнем месте и CWD, ошибки не теряют свой класс | Фазы 1, 3 | `TestCheckCompletion_Verify`, retry tests |
| I23 | Shutdown сохраняет cancellation/wait/terminal-event порядок | Фазы 1, 2 | Orchestrator lifecycle/concurrency tests |
| I24 | YAML, JSON Schema, HTTP/SDK и dashboard payload не изменены | Все | Schema-check, server, SDK, UI и generation checks |

Имена и существование показанных примеров подтверждены источниками [S17] [S18] [S19] [S27] [S29]. Остальные ячейки задают направления поиска в локальном checkout, а не заявляют полноту покрытия.

### 5.1. Что сравнивать кроме конечного статуса

Для опасной ветви полезно зафиксировать короткую последовательность наблюдаемых действий: runner call → значимый filesystem effect → transition/event → следующий запуск. Использовать текущий fake/mock runner и имеющиеся каналы тестов; не создавать полноценный второй runtime ради differential testing.

Сравнивать старую и новую реализацию на одинаковых детерминированных входах. Временные значения, UUID и абсолютные temp-path можно нормализовать только там, где они не являются предметом проверки. Нельзя нормализовать различия phase, stage, sequence number, типа ошибки и порядка причинно связанных действий.

Проверять порядок внутри одной цепочки причинности, а не требовать произвольного глобального порядка независимых параллельных стадий.

Реальные LLM-запуски допустимы как финальная smoke-проверка, но не заменяют детерминированную проверку orchestration: другой текст ответа модели не объясняет, корректен ли новый retry/transition.

## 6. Один safety gate после каждого PR

### 6.1. Baseline и окружение

В просмотренном CI используются Go 1.26 и Node 22; точный minimum Go сверять с `go.mod` текущего checkout. Не начинать с обновления toolchain и dependencies. Результаты до и после должны быть сравнимы. [S24] [S25]

Первый полный запуск выполняется до production-правок. Его ошибки не «лечатся» изменением assertions в рефакторинговом PR. Для известного baseline failure указать reproducer и отдельную задачу; не отмечать весь gate зелёным при фактическом сбое.

### 6.2. Целевой быстрый прогон

После небольшого коммита запускать соответствующие затронутые пакеты. Пример широкого целевого набора, **не заменяющего полный CI**:

```bash
go test -race -count=1 \
  ./pkg/orchestrator/... \
  ./pkg/mcp/... \
  ./pkg/server/... \
  ./pkg/state/... \
  ./pkg/prompts/... \
  ./pkg/flow/...
```

При чистом checkout сначала выполнить обычную подготовку проекта, включая dashboard build, если она нужна для embedded assets. Не объявлять проблему окружения регрессией нового кода.

Для узкого конкурентного regression test допустим повторный прогон, например:

```bash
go test -race -count=20 \
  -run '^TestSpawnAgent_SkipsRunWhenPausedWhileQueuedBehindSemaphore$' \
  ./pkg/orchestrator/concurrency
```

Сначала убедиться, что выбранный test существует на текущем SHA и действительно был запущен. Не считать `no tests to run` прохождением сценария.

### 6.3. Полный обязательный набор перед merge

Воспроизводить текущие шаги CI: [S23] [S24]

```bash
make build
make test
make web-test
make web-typecheck
make sdk-test
make lint-ci
```

`make test` уже включает `-race`; `make build` сам по себе не заменяет UI tests/typecheck. SDK запускается отдельной make-целью. Для проверки, а не автоматического исправления, использовать `lint-ci`, не `lint --fix`-поведение основной lint-цели. `generate-check` входит в зависимости `lint-ci`; неожиданный diff generated status types — повод остановиться и выяснить причину. [S23]

**Linux-only код не покрывается host-прогоном на macOS.** CI работает на Ubuntu, а часть кода/тестов помечена `//go:build linux` (например, `pkg/server/workspace/*_test.go` с openat2/git — на macOS `go test` их молча пропускает: «no tests to run»). Кроме того `lint-ci`, в отличие от основной `lint`-цели, **не** делает отдельный `GOOS=linux`-проход. Поэтому «полный gate», выполненный только на macOS, может объявиться зелёным, пропустив ошибки в Linux-only файлах. Обязательное условие перед merge — одно из двух: (а) прогнать этот же набор в Linux-контейнере против прогретого кэша модулей, например `docker run --rm -v "$PWD":/app -w /app -v afm-gomod:/go/pkg/mod golang:1.26 make test`, плюс `GOOS=linux go vet ./...` / `GOOS=linux golangci-lint run ./...`; либо (б) требовать зелёный hosted CI на этом коммите (см. §8 про auto-release: main-merge без зелёного CI недопустим). Это соответствие требованию отдельной Linux-проверки из `AGENTS.md` (раздел про linux-tagged workspace-тесты).

После полного набора:

```bash
git diff --check
git status --short
git diff --stat
```

Просмотреть незапланированные изменения lockfiles, generated assets и schemas. Не коммитить их автоматически как побочный результат запуска инструментов.

### 6.4. Минимальный отчёт PR

В каждом PR зафиксировать:

```text
Исходный SHA / итоговый SHA:
Какая повторяющаяся логика удалена:
Какие публичные и файловые контракты затронуты:
Связанные инварианты Ixx:
Сопоставление изменённый путь → наблюдающий тест (ОБЯЗАТЕЛЬНО, по одной строке
  на каждый затронутый путь; путь без наблюдающего теста блокирует merge —
  см. §1.F; для «косвенно покрыт» указать, почему этого достаточно):
Существующие тесты, подтверждающие эквивалентность:
Новые тесты и конкретная причина их появления:
Команды, фактически выполненные, и результат:
Пропуски/ошибки окружения/непроверенные сценарии:
Проверенный способ отката:
Отложенные замечания вне scope:
```

## 7. Merge, release и rollback

В текущем CI после успешного push в `main` выполняется создание patch tag. Поэтому нельзя планировать промежуточный неработоспособный main с исправлением «следующим PR»: каждый merge должен быть самостоятельно пригоден к выпуску. [S24]

Один PR — одна понятная смена внутренней границы. Механические перемещения и смысловые изменения должны быть различимы в коммитах; merge-политика должна сохранять возможность понять и откатить изменение. Не добавлять feature flags для выбора старого/нового исполнителя без доказанной необходимости: два runtime-пути увеличивают риск рассинхронизации.

План не меняет форматы данных, что упрощает code revert. Но фактический откат проверять на копии runtime fixtures: новый процесс мог уже оставить файлы, и одно наличие `git revert` не доказывает совместимость их чтения.

При выявленной регрессии не компенсировать её во второй/третьей фазе. Остановить следующую миграцию, локализовать первый изменивший контракт PR и вернуть прежнее поведение в текущей границе.

## 8. Отложенные направления: не включать в Фазу 1 незаметно

**AI-verify.** После рефакторинга отдельная задача должна определить контракт вердикта, артефакт feedback, полномочия review-агента и правило повторного запуска. Эта фаза только упрощает точку подключения; она не создаёт новый verify YAML или автономный review-loop.

**CWD и artifact resolution.** Различие `"."` в agent call sites и RootDir в runner config требует самостоятельного воспроизведения, прежде чем объявлять его багом или менять. [S01] [S03]

**Fallback runner config.** Ветви normal и fallback имеют разный набор параметров. Это повод отдельно проверить intended behavior, а не автоматически выровнять поля при дедупликации. [S03]

**FSM, review-pause и authoritative state.** Возможные улучшения этих механизмов требуют отдельного анализа. Их размер и количество guards не доказывают необходимость переписывания. В этой программе они защищаемые контракты.

Ни один пункт этого раздела не объявлен подтверждённым дефектом без воспроизведения. Их назначение — не дать полезным соседним идеям расширить текущий scope.

## 9. Инструкция для локального исполнителя

Текст ниже можно передать агенту вместе с этим файлом и checkout AFM:

```text
Используй этот файл как план Фазы 1, но проверяй все имена и утверждения
по текущему checkout. Сначала зафиксируй SHA, состояние рабочей копии и
baseline существующего CI. Не стирай чужие изменения.

Начни только с P1.1: сопоставь I01–I10, I20, I23, I24 с существующими
тестами и выпиши минимальные реальные пробелы для PhaseExecutor. Не
расширяй покрытие ради процента и не создавай второй интеграционный harness.

Дальнейшие production-изменения выполняй по одному PR-пакету (P1.2→P1.5).
Сохраняй runtime-поведение, YAML/HTTP/MCP, файловую раскладку,
retry/completion, FSM, recovery и review-pause. Не добавляй AI-verify,
новые настройки, generic framework, persisted-кеши и новые retry layers.

Не изменяй промпты по смыслу или тексту в рефакторинговом PR. Не выравнивай
различия ветвей только потому, что их общий код выглядит красивее.
Найденные возможные баги оформляй отдельно с reproducer.

В конце текущего PR-пакета покажи diff, удалённое дублирование,
сопоставление с инвариантами, реально выполненные команды и результаты.
Явно укажи skipped/unverified проверки. Не заявляй pass без запуска.
Не переходи к Фазе 2, пока gate Фазы 1 не закрыт.
```

## 10. Исходники и проверяемость

Источники — **реальные файлы этого репозитория** (пути ниже относительны корня repo). Номера строк намеренно не зашиты: на активном проекте они быстро меняются; основная привязка — файл и имя функции. Перед исполнением сверить имена функций с зафиксированным SHA baseline.

| Ссылка | Исходник | Использование в плане |
|---|---|---|
| [S01] | `pkg/orchestrator/agents.go` | Entry points, prompts, embedded review |
| [S02] | `pkg/orchestrator/retry.go` | Retry/completion/interruption |
| [S03] | `pkg/orchestrator/runner_factory.go` | Runner config, session, fallback |
| [S04] | `pkg/flow/phase.go` | Типы фаз и имена логов |
| [S05] | `pkg/orchestrator/stagefiles/completion.go` | Completion и verify |
| [S06] | `pkg/orchestrator/stagefiles/context.go` | Dependency plans и artifacts |
| [S07] | `pkg/orchestrator/stagefiles/session.go` | Session files |
| [S08] | `pkg/orchestrator/orchestrator.go` | Coordination, lifecycle, answer dispatch |
| [S12] | `pkg/orchestrator/recovery.go` | Recovery как сохраняемый контракт |
| [S13] | `pkg/orchestrator/scheduling.go` | Scheduling как сохраняемый контракт |
| [S14] | `pkg/orchestrator/control_api.go` | NotifyAnswer и resumeAfterAnswer |
| [S16] | `pkg/orchestrator/concurrency/concurrency.go` | SpawnAgent и SpawnDetached |
| [S17] | `pkg/orchestrator/orchestrator_test.go` | Существующие orchestration tests |
| [S18] | `pkg/orchestrator/retry_test.go` | Существующие retry regressions |
| [S19] | `pkg/orchestrator/pause_continue_test.go` | Pause/continue и auto-run gate |
| [S23] | `Makefile` | Команды проверок |
| [S24] | `.github/workflows/ci.yml` | Реальный CI и auto-release |
| [S25] | `go.mod` | Go module/toolchain |
| [S27] | `pkg/orchestrator/stagefiles/completion_test.go` | Существующие completion/verify tests |
| [S29] | `pkg/orchestrator/concurrency/concurrency_test.go` | Semaphore/detached/activity regressions |
| [S32] | `pkg/flow/flow.go` | Stage и AgentType как неизменяемый внешний контракт |

При расхождении описаний в `AGENTS.md`, комментариев и выполняемого кода не менять код по тексту автоматически. Зафиксировать расхождение и сначала определить фактический контракт через реализацию и тест. Документация — помощь для навигации, не замена проверке поведения.

---

## 11. Реконсиляция с пост-verify кодом (addendum, 2026-09-20)

**Этот план написан ДО фичи AI-verify, которая уже вмержена в `main` и трогает ровно те же файлы, что Фаза 1.** Ветка исполнения — `phazzer` (от `main`, baseline SHA `47114f9`). Ниже — что изменилось и как это влияет на задачи; сами задачи 1.1–1.11 остаются в силе, но с поправками ниже. Карта снята свежим обзором кода на этом SHA.

### 11.1. Что AI-verify УЖЕ извлёк в общие хелперы — НЕ переизвлекать (правит Задачу 1.5/1.7)

- `(*Orchestrator).preNoteBlock(stageDir) string` (`agents.go:47`) — блок «## User note (added before this stage started)». Уже общий, вызывается before-loop в fresh-runner'ах 1/3/4/5.
- `(*Orchestrator).verifyFeedbackBlock(stageDir) string` (`verify.go:931`) — блок «## Замечания автоматической проверки», читается **внутри closure (per-attempt)** во всех 6 impl/review/auto runner'ах. НЕ трогать момент чтения (иначе коррекция после verify-rejection не увидит свежий feedback).
- `(*Orchestrator).gateWithVerify(ctx, s, phase, baseCheck) func() error` (`verify.go:954`) — обёртка completionCheck для 6 execution-runner'ов (planning НЕ оборачивается). Общий runWithRetry получает УЖЕ обёрнутый completionCheck. Рефактор обязан сохранить это оборачивание: тонкий адаптер каждой фазы строит свой `gateWithVerify(...)`-completionCheck и передаёт в неизменный `runWithRetry`.
- Прочие уже общие: `verifyStageNote(v) string` (`agents.go:27`), `rePromptMissingSections` (`agents.go:162`), `memoryBlockForStage`, `runnerFor`, `setRunnerKind`, `spawnKind`, `spawnAgentLeased`.

### 11.2. Остаточные цели извлечения (Задача 1.5/1.7 — это и делаем)

- **`feedbackNote`** («## User note (added while this stage was running)») сейчас инлайнится в каждом `*WithFeedback`-runner'е (читается before-loop). Это симметричный близнец `preNoteBlock` — извлечь в общий хелпер (напр. `feedbackNoteBlock(stageDir) string`), как preNoteBlock.
- **Inline-review блок дублируется ДОСЛОВНО** между `runImplementationAgent` (**agents.go:308-329**) и `runImplementationWithFeedback` (**agents.go:516-536**): тот же `if s.HasAgent(flow.AgentReview)`, тот же `prompts.Build(PhaseAgent: prompts.AgentReview, RetryContext: verifyNote)`, тот же `rr := o.runnerFor(s, phaseReview)` + `rr.RunAgent(ctx, phaseReview, ...)`. Извлечь в общий helper запуска embedded-review, **вызываемый последовательно внутри implementation-попытки** (Задача 1.7 — без нового spawnKind/семафора/transition). Совпадение имени лога review внутри impl с standalone-review логом сохранить как есть.
- **Порядок конкатенации RetryContext и момент чтения** (снято по коду, СОХРАНИТЬ побайтово):
  - runImplementationAgent: `retryContext + stageDirNote + preNote + verifyNote` (preNote before-loop; stageDirNote/verifyNote внутри closure).
  - runImplementationWithFeedback: `retryContext + stageDirNote + feedbackNote + verifyNote`.
  - runReviewAgent: `retryContext + preNote + verifyNote`; runReviewWithFeedback: `retryContext + feedbackNote + verifyNote`.
  - runAutonomousAgent: `retryContext + summaryNote + preNote + verifyNote`; runAutonomousWithFeedback: `retryContext + summaryNote + feedbackNote + verifyNote`.
  - Универсальная дельта fresh↔feedback: (i) `preNote`↔`feedbackNote`; (ii) feedback impl/review/auto шлёт `EvStartRun`, planning-feedback — `EvStartPlanning`; (iii) fresh лог `PhaseLogFile(...)`, feedback — хардкод `*-feedback.log`/`planning-revision.log`. Planning (1↔2) структурно иной: feedback читает `feedback.md`+`LatestPlanVersion` и кладёт в поля `Feedback`/`PreviousPlan`, НЕ в RetryContext — не сливать (уже отмечено в Задаче 1.2/1.6).
  - **Pre-agent setup у каждого fresh-runner'а РАЗНЫЙ (не «общая fresh-ветка» — фактическая проверка codex):** fresh planning — `MkdirAll` + `clearStaleAutonomousFlag` + `EvStartPlanning`; fresh implementation — НИЧЕГО из этого (сразу preNoteBlock); fresh review — только `MkdirAll`; fresh autonomous — `MkdirAll` + запись `autonomous.flag`. Feedback impl/review/auto — `EvStartRun`, planning-feedback — `EvStartPlanning`; ни один feedback не делает MkdirAll/flag. **Реализовать этот setup как явную per-adapter подготовку в тонком адаптере, НЕ как generic «fresh setup» ветку.** В частности НЕ добавлять MkdirAll/flag в implementation ради симметрии.

### 11.5. Инварианты реализации (из codex-ревью плана — обязательно соблюсти)

- **`runnerFor` ДОЛЖЕН вызываться ВНУТРИ per-attempt closure (`agentFn`), а не в адаптере.** `runWithRetry` регистрирует generation-specific interrupt-канал ПЕРЕД вызовом `agentFn` (`retry.go:155`), а `runnerFor` читает этот канал при сборке executor'а (`runner_factory.go:68`) и заново проверяет существование interactive-сессии на каждой сборке (`runner_factory.go:75`), после того как retry-путь мог удалить session-файл (`retry.go:388`). Сборка/кеширование runner'а в адаптере (вне `agentFn`) сломает Pause/Revise-прерывание и сброс сессии между попытками. То же правило — для runner'а embedded-review.
- **Helper embedded-review сохраняет точный снимок и владение retry:** в текущей implementation-попытке `verifyNote` читается ДО запуска implementation и тем же значением переиспользуется для inline-review (`agents.go:285`→`322`); `depPlans`/`artCtx` переиспользуются (не пересобираются после implementation); `memoryBlockForStage` для review-промпта пересчитывается (не переиспользуется весь `prompts.Inputs` implementation'а). Ошибка review возвращается в ОБЪЕМЛЮЩИЙ implementation-`runWithRetry` — у review нет своего retry/transition/lease/completion-gate (`agents.go:325`). Поэтому helper принимает уже захваченные dependency/artifact/verify значения, строит review-промпт inline, зовёт `runnerFor(s, phaseReview)` и возвращает его ошибку напрямую.
- **Разделение чистое (подтверждено codex):** planning остаётся негейченным; остальные шесть адаптеров сохраняют свой family-specific `gateWithVerify`; неизменный `runWithRetry` продолжает владеть verify-failure/retry/pause/revise reconciliation. Verdict codex: план безопасен к исполнению при соблюдении matrix выше и timing runner/inline-review.

### 11.3. Задача 1.9 усилена — `retry.go` теперь ВЛАДЕЕТ и verify-логикой (НЕ рефакторить его)

`runWithRetry` (`retry.go:148`) после verify дополнительно содержит: verify-outcome reconciliation, `commitVerifyFailure` (`retry.go:74`, атомарный `EvVerifyFail` — `bus/fsm.go`), обработку `revising`/`paused` для всех verify-исходов, и получает `gateWithVerify`-обёрнутый completionCheck. **Фаза 1 схлопывает ТОЛЬКО тела 8 entrypoint'ов в `agents.go`; `retry.go`, `gateWithVerify`, `RunVerification`, `commitVerifyFailure`, `EvVerifyFail`, `runnerForVerify`, `spawnAgentLeased`, `concurrency.Lease` НЕ трогаются.** completionCheck (включая его gateWithVerify-обёртку) конструируется тонким адаптером и передаётся в неизменный runWithRetry.

### 11.4. Baseline и §1.G

- Задача 1.1 baseline: ветка `phazzer` @ `47114f9` (не старый SHA плана). Пере-снять инвентаризацию `rg` на этом SHA (номера строк в §1.G/этом addendum — по нему).
- §1.G карта 8 entrypoint'ов верна по составу; строки сместились (см. свежие: runPlanningAgent 92, runPlanningWithFeedback 185, runImplementationAgent 247, runReviewAgent 337, runAutonomousAgent 399, runImplementationWithFeedback 447, runReviewWithFeedback 544, runAutonomousWithFeedback 591).
- Дополнительный regression-набор для §1.F (verify-контракты, которые рефактор не должен сломать): существующие verify/gate/pause-revise тесты в `pkg/orchestrator` (`*verify*_test.go`, `pause_continue_test.go`) — прогонять в целевом gate наравне с retry/recovery.
