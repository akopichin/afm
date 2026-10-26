# plan-one

Упростить ядро pkg/orchestrator и прояснить границы его подсистем (mesh/agentcomm, assistants/helpers, verify, диалог, scheduling/recovery и др.): уменьшить связность и размер перегруженных компонентов без изменения поведения.

Source revision: 665223bbe2fecf75b5e52a726df7594b3149a800
Proposal revision: 1
Approval event: 60

## Required plans

None.

## Constraints

- Анализируется снимок run-worktree /Users/alexander.kopichin/work/personal/afm/.worktrees/refactor (ветка refactor, HEAD 665223bb); незакоммиченные изменения основного checkout в снимок не входят.
- Это только план: исходники, конфигурация, зависимости и история Git не изменяются на этом этапе.
- Поведение сохраняется: CLI, HTTP API, формат events.jsonl/state.json/notices.jsonl и replay, YAML-контракт flow/config.
- Соблюдать инварианты из AGENTS.md и pkg/orchestrator/AGENTS.md: durable намерение до возврата API, CAS-победитель запускает работу, run-scoped context, clean shutdown, общие хелперы запуска/завершения (семафор, Pause, каскад).
- Версию Go в go.mod не менять; новых зависимостей не добавлять.
- Не трогать: vendor/, sdk/, schema/, docs/, dashboard (pkg/web).
- Внешние пакеты (state, server, executor, config, flow, agentcomm и др.) — только как контекст связей, не как цель изменений.
- Пороги strictacode — сигналы для исследования, а не доказательство необходимости переписывания.

## Steps

### 1. Глубокое копирование стадий в graph.Registry (registry-deep-copy)

Вернуть Registry обещанный контракт неизменяемого снимка: сейчас cloneStage копирует только DependsOn, и вложенные данные flow.Stage общие у вызывающих, реестра и старых ревизий. До: изоляция держится на том, что никто не мутирует вложенные поля. После: каждый accessor и каждая ревизия владеют своими данными. Новых абстракций нет, сигнатуры и форматы не меняются.

Effort: small
Prerequisites: None

1. До правки добавить в pkg/orchestrator/graph/registry_test.go тесты изоляции: изменение исходного []flow.Stage после NewRegistry/WithStages и изменение значений, полученных через Stages(), Stage(id) и StageByID(id), не меняет последующие чтения и старый снимок. Минимальная репродукция — Agents, Verify.Steps[0].Run, *Browser (как scratch-проба Codex); затем Skills, Artifacts и Artifact.Inline, Inputs, Verify.MaxFailures/ReviewPatience/FreshCorrection, AutoRun, Buttons, Reflect, MemoryUse, Hooks (Events, SkipEvents, Env, ResolvedEnv), Assistants. Сравнивать содержимое, а не адреса; отдельно проверить nil против пустого slice.
2. В тот же файл добавить рефлексивный guard: обход полей flow.Stage и вложенных типов (VerifySpec, VerifyStep, Artifact, lifecyclehooks.Hook, StageAssistant) падает, если появилось reference-поле, которого нет в списке, обработанном cloneStage. Рефлексия только в тесте, не в production-копии.
3. В graph/registry.go расширить cloneStage до явной глубокой копии всех reference-полей: slices.Clone для slice без вложенных ссылок, в том числе []VerifyStep и StageAssistants (у VerifyStep и StageAssistant только скалярные поля); поэлементная копия для []Artifact (Inline *bool) и []lifecyclehooks.Hook (Events.Events, SkipEvents, Env, ResolvedEnv); maps.Clone для map, копия значения для *bool/*int/*Reflect; nil остаётся nil. VerifySpec копировать как структуру и заменять в ней только reference-поля, чтобы неэкспортируемый fromScalar сохранился без изменения pkg/flow.
4. В newRegistryAt заменить ручное клонирование DependsOn на s = cloneStage(s) (deps по-прежнему строить из клона); Stages/Stage/StageByID уже идут через cloneStage.
5. Исправить doc-комментарии Registry и cloneStage под фактическое поведение, убрать из package doc (graph/registry.go:1-3) ссылку «dev-plan.md Этап 8, T8.1» и удалить устаревший абзац Store про «Stage 8 / Этап 9» (graph/registry.go:166-167): Replace уже вызывается из graph_mutate.go:211.

**Verification**

- go test -race ./pkg/orchestrator/graph/... — новые тесты изоляции падают до шага 3 и проходят после.
- go test -race ./pkg/orchestrator/... (registry_migration, graph_mutate, replay, browser_policy, verify wiring) — без изменений результата.
- scripts/check-comments.sh <base> чист для диффа этапа; make lint без замечаний.

**Risks**

- Глубокая копия на каждом чтении добавляет аллокации в reg().Stage(id); ожидаемо пренебрежимо (десятки стадий), но проверить, что accessor не вызывается в плотном цикле.
- Пропущенное вложенное поле (например, внутри StageAssistant или EventSelector) оставит alias — закрывается рефлексивным guard'ом шага 2.
- Production-вызова, мутирующего вложенные поля, не найдено: это исправление латентного дефекта, а не наблюдавшегося сбоя. Если какой-то тест опирался на alias, он упадёт — разбирать по месту.
- Альтернатива «только честная документация: вложенные поля не мутировать» отклонена: контракт уже обещает изоляцию concurrent-читателям, а исправление локально. Откат — revert коммита; сохранённые журналы и определения стадий не затрагиваются.

### 2. Выделить решения о бюджетах ретраев и коррекции verify из runRetryLoop (retry-budget-decisions)

Сделать правила бюджетов проверяемыми без subprocess и FSM и убрать шесть переменных коррекции из тела runRetryLoop, не трогая порядок жизненного цикла хода. До: правила бюджетов рассыпаны по циклу и проверяются только интеграционно. После: две чистые единицы с табличными тестами; цикл остаётся владельцем epoch, парков, FSM и ожиданий. Это первый, самостоятельно полезный шаг по retry-loop-overload; основное упрощение ветки успеха — отдельный этап retry-success-branch.

Effort: small
Prerequisites: None

1. До правки зафиксировать последовательности тестами, дополнив retry_budget_test.go, verify_quota_test.go, retry_test.go и verify_stalemate_loop_test.go, где сценария нет: unknown → network → success; коррекция verify → транспортная ошибка; unknown на последнем разрешённом attempt (attempt == maxRetries); verify-отклонение при исчерпанном attempt; quota-парк и пробуждение с теми же VerifyFailures/VerifyStalemates; Pause/Revise во время completion-check и backoff. Проверять число запусков агента, последовательность FSM-событий и тексты EventRetryScheduled/EventRetryExhausted.
2. В retry.go выделить чистую функцию transportRetryDecision(class Classification, attempt, unknownRetries, maxRetries, maxUnknownRetries int) (retryOK bool, budget, numerator, nextUnknownRetries int) — дословно логика retry.go:537-545, включая обязательное attempt < maxRetries в обеих ветках; покрыть табличным тестом. Функция не получает Store, Orchestrator, context и функций публикации.
3. Собрать состояние коррекции verify в локальную структуру verifyCorrection{failures, maxFailures int; fresh bool; patience int; prevFingerprint string; unchanged int}, инициализируемую из verifyMaxFailures/verifyFreshCorrection/verifyReviewPatience и verifyResume.quota (VerifyFailures, VerifyStalemates), с методами canRetry(attempt, maxRetries int, isVerifyReject bool) bool (нынешнее выражение retryIncomplete, retry.go:459-461) и counters() verifyCounters для parkVerifyForQuota; handleStalemate сохраняет сигнатуру.
4. runRetryLoop остаётся владельцем регистрации interruptChans, epoch, retry-контекста, admission и парков, проверок turnOwnsStage/handOffRevise, трёх мест verifyResume.spend(), verifyOutcomeGuardHook, FSM-переходов, notice и select backoff. Комментарии изменённых участков сжать до 1–2 строк сути (ссылки T5/D1/G2/B1 убрать, обоснование «attempt < maxRetries обязательно» сохранить).

**Verification**

- go test -race ./pkg/orchestrator -run 'Retry|Budget|Backoff|Verify|Quota|Stalemate|Completion|Pause|Revise' до и после — одинаковый результат.
- Табличный тест transportRetryDecision: ClassRetryable/ClassRetryableUnknown × attempt < maxRetries / == maxRetries × unknownRetries в пределах и за пределами бюджета.
- git diff --color-moved: тело цикла меняется только заменой выражений на вызовы; scripts/check-comments.sh <base> чист для диффа этапа; make lint без замечаний.

**Risks**

- Ошибка в условии attempt < maxRetries вернёт тихое зависание стадии в running — ловится регрессией UnknownErrorOnLastAllowedAttemptStillFails (retry_budget_test.go).
- verifyCorrection живёт только в горутине хода, как и нынешние счётчики; durable — только через verifyCounters при парке.
- Альтернатива меньшего риска (Codex): лишь переименовать и сгруппировать счётчики без функции решения; бюджеты тогда остаются непроверяемыми отдельно. Откат — revert коммита; payload quota и виды событий не меняются.

### 3. Одна база executor.Config для interactive и non-interactive хода стадии (stage-executor-base)

Убрать второй список общей стадийной проводки executor: runnerFor и nonInteractiveRunner одинаково вычисляют 20 полей executor.Config и InterruptCh, и фичевые коммиты добавляли поле в оба литерала. До: два списка одной ответственности. После: одна база стадийной проводки и явные различия (сессия/resume, аргументы). Verify и helper намеренно остаются со своими конфигами.

Effort: small
Prerequisites: None

1. До правки закрепить тестами (runner_factory_test.go, browser_policy_test.go, usage_provider_wiring_test.go, reasoning_wiring_test.go) поля executor.Config для default/custom command × interactive/non-interactive и для fallback при ошибке сессии: Command, IdleTimeout, TruncateOutput, Debug, OnAction со stage ID, OnLimitWarning, WrapperDir, Dir, RunDir/StageID/Phase, абсолютный StageDir при отличном RootDir, UsageHint/Provider/OnUsage, BrowserMCP, MeshClient, OnDelivered, Reasoning, RunID, OnObservation, InterruptCh.
2. В runner_factory.go выделить private baseStageExecutorConfig(s flow.Stage, phase, cmd string, extraArgs []string) executor.Config с общими полями, включая InterruptCh из interruptChans; runnerFor дописывает SessionID/Resume и ResolveArgs, nonInteractiveRunner — свою политику command/ExtraArgs. Подмена o.runner для дефолтной команды остаётся до сборки конфига.
3. runnerForVerify и helperExecutorConfig (assistants_lifecycle.go:35-53) не переводить на baseStageExecutorConfig: у verify и helper другие гарантии capability (нет BrowserMCP, StageDir и mesh стадии; у helper — ScratchDir и Redact/Secrets).

**Verification**

- go test -race ./pkg/orchestrator -run 'Runner|Browser|Usage|Reasoning|Execprep|Lease|Interrupt|Fallback'.
- Тест шага 1 сравнивает полный executor.Config (кроме функций — по наличию и привязке stage ID) до и после.
- scripts/check-comments.sh <base> чист для диффа этапа; make lint без замечаний.

**Risks**

- Случайная передача стадийной capability (браузер, dialog env) в verify/helper — исключена шагом 3 и тестами browser_policy.
- Альтернатива (Codex): оставить оба литерала и закрепить общие гарантии контрактным тестом — дешевле, если новых полей не ожидается; история (84c33f59, 5451eb52) показывает, что поля добавляются регулярно. Откат — в пределах файла.

### 4. Общая база prompts.Inputs для раннеров фаз (turn-prompt-base)

Убрать пять копий семи сквозных полей промпта и четыре копии блока mkdir + EvFail. До: каждое новое сквозное поле правится в пяти местах. После: baseStageInputs задаёт общие поля, фазы дописывают только своё; тексты промптов не меняются.

Effort: small
Prerequisites: None

1. Характеризационный тест: фейковый Runner сохраняет собранный prompt для planning (fresh, feedback), implementation (fresh, feedback, embedded review), review и autonomous (fresh, feedback); эталон — текущий текст с включёнными блоками memory, browser и mesh.
2. В agents.go добавить baseStageInputs(s flow.Stage, stageDir, template string) prompts.Inputs с семью общими полями (Stage, StageDir, GlobalPrompt, MemoryBlock = memoryBlockForStage, BrowserRuntime = browserRuntimeFor, AgentComm = agentCommBlockFor, AgentCommInbox = agentCommInboxBlock) и Interactive = s.Interactive; пять литералов (176, 268, 327, 385, 434) дописывают только своё: PhaseAgent, RetryContext, DependencyPlans и Artifacts (у всех фаз, кроме planning), Plan, OutputContractMD, Autonomous, Interactive = true для autonomous. Вызов остаётся внутри closure попытки — блоки вычисляются на каждой попытке, как сейчас.
3. Повтор mkdir + EvFail «mkdir failed» (agents.go:68, 120, 365, 413) свести в ensureStageDir(s) bool с тем же текстом причины.
4. Различия RetryContext (discussionQuestionNote есть у implementation/review/autonomous и нет у embedded review и planning) не менять: зафиксировать тестом шага 1, вопрос о намеренности — в open questions.

**Verification**

- Эталон промптов шага 1 совпадает до и после.
- go test -race ./pkg/orchestrator -run 'Prompt|Planning|Implementation|Review|Autonomous|Memory|Browser|AgentComm|Inbox'.
- scripts/check-comments.sh <base> чист для диффа этапа; make lint без замечаний.

**Risks**

- Порядок и наличие блоков в промпте — контракт для агентов; эталон должен сравнивать полный текст.
- Если вычисление блоков случайно вынести из closure попытки, memory и inbox устареют между попытками.

### 5. Удалить мёртвый CapabilityFor с ложной документацией (remove-capability-api)

Сократить поверхность mesh-core и убрать документацию, противоречащую коду. До: экспортированный API, который «читают roster/UI и Send», но не вызывает никто. После: его нет. Остальные неиспользуемые API (FollowUp, StopAndClarify, RetryTask, CreateProposal) не трогаются до решения пользователя.

Effort: small
Prerequisites: None

1. Удалить pkg/orchestrator/agentcomm_capability.go (Capability, CapabilityFor) и agentcomm_capability_test.go: продакшн-вызовов нет (поиск по всему репозиторию без _test.go).
2. FollowUp, StopAndClarify (assistants_followup.go:60-107), RetryTask (assistants_cascade.go:67-104) и CreateProposal (proposal.go:96-145) в этом этапе не трогать — их судьба вынесена в open questions.

**Verification**

- go build ./...; go vet ./pkg/orchestrator/...; make sdk-test (sdk — отдельный модуль).
- go test -race ./pkg/orchestrator -run 'Mesh|Roster|AgentComm'; make lint.

**Risks**

- Внешний потребитель вне репозитория не известен; API внутренний для бинаря afm. Откат — revert.

### 6. Перенести общие примитивы к их владельцам (чистый перенос) (shared-primitives-move)

Сделать путь запуска стадии и общие примитивы находимыми по имени файла. До: spawn-путь и отображение kind → раннер лежат в reviewpause.go, orchestrator.go и agents.go; runContext — в control_api.go; форматирование quota — в notices.go. После: stage_spawn.go владеет запуском агента стадии и отображением kind → раннер; runContext — рядом с Run; notices.go — только publishNotice. Логика не меняется; это подготовка к runner-kind-dispatch и упрощение навигации.

Effort: small
Prerequisites: None

1. Отдельный коммит только с переносами, без изменения тел: spawnAgentLeased, spawnKind, computeResumeKind, withFeedbackRunner, plainRunner (reviewpause.go:35-67, 244-270), kind-константы, kindOfPhase и turnRunner (orchestrator.go:466-470, 1153-1173), feedbackTurnRunner (agents.go:469-480) — в новый pkg/orchestrator/stage_spawn.go. В reviewpause.go остаётся только review pause (включая newOperationID, resumeOwner, runResumeTransaction).
2. runContext (control_api.go:104-111) перенести в orchestrator.go рядом с Run, который пишет runMu/runCtx.
3. Форматирование usage-limit (QuotaWaitMessage, StageQuota, StageQuotaMessage, quotaResetMessage, quotaNoticeData, publishQuotaNotice — notices.go:64-150) перенести в quota.go; в notices.go остаётся publishNotice и общие уведомления.
4. projectDir (verify_stalemate.go:85-93), stageDir (orchestrator.go:620-624) и stageDirFor (feedback_intent.go:391-394) собрать в pkg/orchestrator/paths.go с doc о различии абсолютного и относительного пути; inline filepath.Join(o.opts.RunDir, id) не заменять и пути не унифицировать. secretValues/RedactSecrets/redactAll/sameRedacted* (agentcomm.go:364-407) — в secret_redaction.go.
5. Обновить комментарии, ссылающиеся на файлы (например, assistants_declared.go:13-16 → reviewpause.go), и сжать перенесённые комментарии по правилу AGENTS.md.

**Verification**

- go build ./... и go vet ./pkg/orchestrator/...; git diff --color-moved=dimmed-zebra показывает только перенос.
- make test; scripts/check-comments.sh <base> чист для диффа этапа; make lint без замечаний.

**Risks**

- Конфликты слияния с параллельными ветками, которые правят эти файлы; влить быстро отдельным PR.
- Экспортированные QuotaWaitMessage/StageQuota/StageQuotaMessage остаются в том же пакете — API pkg/server не меняется.

## Architecture snapshots

- [Before](01-plan-1.before.json)
- [After](01-plan-1.after.json)

Snapshots follow the full execution order in index.json, including earlier independent plans. Revalidate the source and tests before each implementation.

This is an approved plan; no code has been changed.
