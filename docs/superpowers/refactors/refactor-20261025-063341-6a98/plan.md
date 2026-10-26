# pkg/orchestrator · упрощение ядра и границ подсистем — refactoring plan

Упростить ядро pkg/orchestrator и прояснить границы его подсистем (mesh/agentcomm, assistants/helpers, verify, диалог, scheduling/recovery и др.): уменьшить связность и размер перегруженных компонентов без изменения поведения.

Source revision: 665223bbe2fecf75b5e52a726df7594b3149a800
Proposal revision: 1
Approval event: 60

## Constraints

- Анализируется снимок run-worktree /Users/alexander.kopichin/work/personal/afm/.worktrees/refactor (ветка refactor, HEAD 665223bb); незакоммиченные изменения основного checkout в снимок не входят.
- Это только план: исходники, конфигурация, зависимости и история Git не изменяются на этом этапе.
- Поведение сохраняется: CLI, HTTP API, формат events.jsonl/state.json/notices.jsonl и replay, YAML-контракт flow/config.
- Соблюдать инварианты из AGENTS.md и pkg/orchestrator/AGENTS.md: durable намерение до возврата API, CAS-победитель запускает работу, run-scoped context, clean shutdown, общие хелперы запуска/завершения (семафор, Pause, каскад).
- Версию Go в go.mod не менять; новых зависимостей не добавлять.
- Не трогать: vendor/, sdk/, schema/, docs/, dashboard (pkg/web).
- Внешние пакеты (state, server, executor, config, flow, agentcomm и др.) — только как контекст связей, не как цель изменений.
- Пороги strictacode — сигналы для исследования, а не доказательство необходимости переписывания.

## Agreed stages

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

### 5. Разделить runHelperTask на подготовку, классификацию неуспеха и коммит результата (helper-turn-phases)

Сделать исходы хода помощника обозримыми: подготовка, классификация неуспешных исходов и коммит результата становятся отдельными методами при неизменном порядке и атомарности коммитов. До: одна функция на 283 строки (цикломатика 49). После: runHelperTask — последовательность трёх фаз, сигналы pause/clarify и cleanup остаются в ней. Исход required-задачи остаётся входом барьера завершения root-стадии: терминальные коммиты идут через finishTask* с прежним afterFinish, а FSM-переходом по-прежнему владеет общий completion-путь (completeStageFenced); этап его не меняет.

Effort: medium
Prerequisites: None

1. Характеризация исходов runHelperTask (assistants_lifecycle.go:133-415) существующими и новыми тестами: shutdown рана, usage-limit, ошибка исполнения, interrupted/finalizedByOther, timeout, !ProcessOK с requeue по taskPausePending/taskClarifyPending, yield (parkYieldedTurn), protocol error, ошибка декодирования/repair, delegate, неподдерживаемый kind, pass при открытом required-ребёнке и четыре варианта коммита успеха. Для required-задачи зафиксировать влияние исхода на root-стадию: гейт закрывает только completed с вердиктом pass (или waiver), и EvComplete root-стадии случается, только если она уже в awaiting_assistants и других открытых required-задач текущего поколения нет; failed, delegate (waiting_children), yield (waiting_peer), requeue и отказ «pass при открытом required-ребёнке» гейт не закрывают и EvComplete не вызывают.
2. Выделить три метода без смены порядка: prepareHelperTurn (workspace, context.md, startTurn, очистка прежнего raw-ответа) → данные хода; classifyHelperFailure (ветки до успешного декодирования) → handled bool; commitHelperResult (хвост успеха с четырьмя вариантами коммита). defer CompareAndDelete(taskCancels) и порядок LoadAndDelete taskPausePending/taskClarifyPending остаются в runHelperTask.
3. Каждый вариант коммита остаётся одной операцией Store.ApplyRunOperation (через finishTaskTurnReplying и др.): статус задачи, ответы mesh и разрешение waits не делить на независимые коммиты. Post-commit хвост afterFinish (publishNotice → announceMessage → announceAndWakeEnded → tryResumeParent → releaseUnanswerableWaitsAfterFinish → maybeCompleteAfterAssistants → WakeEventLoop → notifyAndResumeWaits) не переносить и не дублировать. Новые методы не вызывают completeStage, maybeCompleteAfterAssistants и Trigger напрямую — только через finishTask*. Generation-aware RequiredBlocker и CAS EvComplete в completeStageFenced не трогать.

**Verification**

- go test -race ./pkg/orchestrator -run 'Assistant|Task|Helper|Watch|Delegate|Waiver|Cascade|Pause|Quota|Yield|RunAgent|Shutdown|Turn|Barrier|Completion'.
- Регрессии барьера завершения проходят без изменений: TestBarrier_AfterHookNotRunEarly, TestBarrier_VerifyStillIndependentGate, TestBarrier_SlotReleasedWhileWaiting, TestBarrier_RestoredAfterRestart, TestBarrier_GenerationAwareCas, TestCompletionGoesThroughCascade (assistants_barrier_test.go).
- Критерий завершения этапа — полный go test -race ./pkg/orchestrator/... зелёный, а не только шаблон выше.
- git diff --color-moved: переносы без переупорядочивания; scripts/check-comments.sh <base> чист для диффа этапа; make lint без замечаний.

**Risks**

- Исход required-задачи — вход барьера завершения root-стадии. Ошибка классификации (коммит completed с вердиктом pass вместо failed, requeue, waiting_peer или waiting_children; потеря проверки finalizedByOther; перенос коммита из-под afterFinish) преждевременно вызовет EvComplete, after-hook, reflection и запуск зависимых стадий. Закрывается шагами 1 и 3 и Barrier/Completion-регрессиями.
- Порядок сигналов pause/clarify и проверка finalizedByOther; семантика consumed turn.
- Если по open question StopAndClarify будет удалён, ветка clarify упростится — решить до этого этапа или учесть в нём.

### 6. Собрать оформление исходов verify-шага в verificationPass (verify-pass-outcomes)

Отделить протокол артефактов и уведомлений шага от control flow прохода и убрать 7 одинаковых копий хвоста отказа; 3 вариантных хвоста (гонка дедлайна и storage-сбой quota-парка) остаются явными. До: RunVerification владеет и исполнением шагов, и записью manifest/уведомлений. После: RunVerification — lease, resume, контекст и цикл шагов; verificationPass (verify_pass.go) — состояние прохода и оформление исхода шага в прежнем порядке. Нового пакета и сервиса нет.

Effort: medium
Prerequisites: None

1. Характеризация per-outcome артефактов и порядка до правки: agent pass, needs_changes, inconclusive, protocol error, неизвестный verdict; shell pass и ненулевой exit; timeout шага; гонка дедлайна после успеха (verifyStepDeadlineRace); прерывание Pause/Revise; исчерпание транзиентных exec-ретраев; quota-парк и resume прохода (restoreVerifyPasses); storage-ошибка каждого критичного файла. Явно зафиксировать текущее: pass-уведомление шага публикуется до финального persistManifest (verify.go:387, 486 против 489), failure-ветки игнорируют ошибку записи manifest. Для трёх вариантных хвостов добавить точные проверки; существующие verify_deadline_race_test.go проверяют только strings.Contains. (а) Гонка дедлайна agent-шага (362-366; seam runVerifyAgent ждёт <-ctx.Done() и возвращает вердикт) и shell-шага (475-480; seam runVerifyShell ждёт <-ctx.Done() и возвращает nil): возвращается *VerifyExecError с Reason == verifyTimedOutReason (без двойного префикса), Step == idx, Transient == false; err.Error() == «verify execution failed at step N: » + verifyTimedOutReason; reason уведомления verify_result в notices.jsonl равен err.Error(), exec_error_kind = exec_failure; outcome шага в manifest = error. (б) Storage-сбой quota-парка (333-338; seam runVerifyAgent делает запись manifest невозможной, например заменяет manifest.json каталогом, и возвращает *executor.UsageLimitError): VerifyExecError с Reason «verify storage failure: …» и Step == idx, а не verifyLimitError; ровно одно уведомление verify_result с тем же reason; стадия не паркуется по квоте. Без сбоя quota-ветка по-прежнему возвращает verifyLimitError, а manifest содержит outcome quota_wait. Порядок «manifest до уведомления» проверить, читая manifest.json в обработчике live-уведомления verify_result, если доставка синхронна. Число записей manifest существующими seam'ами не наблюдаемо: production-seam ради него не добавлять, а сохранить эти ветки дословно (шаг 2).
2. Ввести private тип verificationPass в новом файле pkg/orchestrator/verify_pass.go: stage, stageDir, verID, phase, manifestSteps, passedSteps. Методы: persist() error (нынешнее замыкание persistManifest) и failStep(i, idx int, kind, command, reason string) error. failStep в текущем порядке ставит outcome = verifyOutcomeError, вызывает persist с игнорированием ошибки, публикует emitVerifyResult(exec_failure, reason) и возвращает &VerifyExecError{Reason: reason, Step: idx}. Заменить им только 7 одинаковых хвостов: verify.go:373, 380, 403, 429, 436, 452, 461. Остальные хвосты не переводить и сохранить дословно (в новых функциях шага). Гонка дедлайна (362-366, 475-480) публикует raceErr.Error() и возвращает тот же raceErr; второй helper ради двух мест не вводить. Storage-сбой quota-парка (333-338) проверяет ошибку единственной записи manifest и возвращает storage-ошибку вместо verifyLimitError. Typed error с enrichExecReason (342-357), inconclusive (394-398), interrupted (413-417, 441-446) и отказ через persistVerifyRejection тоже остаются отдельными.
3. Вынести тела шагов в runAgentVerifyStep и runShellVerifyStep (возвращают error; pass записывает outcome и строку passedSteps внутри), сохранив seam'ы runVerifyAgentStep/runVerifyShell, persistVerifyRejection, декодер verify и stagefiles-примитивы. stepCtx и cancelStep создавать в функции шага: отмена в конце шага, а не прохода — потребителей stepCtx после шага нет, подтвердить тестами verify_timeout и verify_deadline_race.
4. В RunVerification оставить lease-defer со swap-back только для VerifyRejectedError, verID и resume-курсор, чтение plan/summary/artifacts, цикл шагов, финальный persist и ClearActiveFeedback. verificationPass не завершает стадию и не трогает FSM.

**Verification**

- go test -race ./pkg/orchestrator -run 'Verify' (31 файл verify_*) и go test -race ./pkg/orchestrator/stagefiles/... ./pkg/orchestrator/verify/....
- Точные проверки шага 1 для гонки дедлайна agent/shell и storage-сбоя quota-парка (Reason, Step, Error(), identity ошибки, reason и exec_error_kind уведомления, outcome в manifest) совпадают до и после; git diff --color-moved показывает эти три хвоста перенесёнными без изменений.
- verify_observability_test.go подтверждает прежний порядок событий; характеризация шага 1 совпадает.
- scripts/check-comments.sh <base> чист для диффа этапа; make lint без замечаний.

**Risks**

- Подстановка failStep в вариантные хвосты изменила бы поведение: для гонки дедлайна — либо reason уведомления, либо Reason ошибки (двойной префикс в причине EvVerifyFail); для quota-storage — перезапись outcome quota_wait и повторную запись manifest. Поэтому шаг 2 ограничен 7 одинаковыми хвостами, а шаг 1 фиксирует вариантные точными проверками.
- Порядок persist → emit и fail-closed — контракт (pkg/orchestrator/AGENTS.md:53-54). Наблюдаемое отступление (pass-уведомление до manifest) сохраняется как есть; его исправление — отдельное решение (verify-manifest-order).
- Перенос defer cancelStep в функцию шага меняет момент отмены stepCtx.
- Отдельный verify-сервис или пакет не выделять — дороже и не обосновано (оба архитектора). Откат — revert; форматы артефактов и журналов не меняются.

### 7. Вынести ветку успешного выхода агента из runRetryLoop в метод решения (retry-success-branch)

Снизить когнитивную нагрузку самой меняемой функции ядра: ветку err == nil (retry.go:364-494, ~130 строк) вынести в метод, возвращающий решение «ход завершён» или «повторить с причиной», сохранив дословный порядок проверок. До: решение о выходе хода смешано с циклом попыток. После: цикл — подготовка попытки, запуск, ошибки exec и backoff; ветка успеха читается отдельно. Этап необязателен: «остановиться на retry-budget-decisions» — допустимый исход.

Effort: medium
Prerequisites: retry-budget-decisions

1. Предусловие: тесты последовательностей из retry-budget-decisions зелёные. Добавить характеризацию порядка решений ветки успеха: потеря владения до и после completionCheck (handOffRevise), открытый вопрос при статусе ≠ и = awaiting_user_input, парк по peers (turnEpisode), verify-парк по verifyLimitError, ErrUserInterrupted из verify, stalemate, исчерпанный verify-бюджет → commitVerifyFailure, план-незавершёнка на attempt 0 и позже.
2. Выделить метод decideSucceededTurn, принимающий локальную структуру хода (s, phase, feedback, epoch, attempt, maxRetries, resumesVerify, waitsBefore, verifyResume, *verifyCorrection, completionCheck, onUserInterrupted) и возвращающий (retryReason string, retry bool): retry=false — ход завершён (как нынешний return), retry=true — continue с incompleteReason = retryReason.
3. Сохранить порядок: turnOwnsStage → deliverRenderedInbox → открытый вопрос → turnEpisode/parkForPeers → completionCheck → verifyResume.spend() → turnOwnsStage → completeAgent | ErrUserInterrupted | verifyLimitError → parkVerifyForQuota | IncompleteWork (stalemate → canRetry → verifyOutcomeGuardHook → verifyOutcomeStillOwned → счётчики → notice) | commitVerifyFailure или EvFail + failBlockedStages.
4. runRetryLoop после выделения: подготовка попытки, admission, agentFn, вызов decideSucceededTurn, ветка ошибок exec (ErrUserInterrupted, quota, классы, transportRetryDecision, backoff). Если после шага 1 метод требует больше одной структуры состояния, остановиться и не выполнять этап.

**Verification**

- Характеризация шага 1 и go test -race ./pkg/orchestrator -run 'Retry|Verify|Quota|Peer|Yield|Stalemate|Completion|Pause|Revise|OpenQuestion|Interactive'.
- git diff --color-moved: перенос без переупорядочивания операторов; make test.
- scripts/check-comments.sh <base> чист для диффа этапа; make lint без замечаний.

**Risks**

- Любое переупорядочивание фенсинга или spend меняет поведение при гонке Pause/Revise с выходом процесса.
- Метод с большим набором параметров может оказаться не проще исходной ветки — тогда этап отменяется.
- State machine или стратегии на интерфейсах отвергнуты обоими архитекторами.

### 8. Удалить мёртвый CapabilityFor с ложной документацией (remove-capability-api)

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

### 9. Перенести общие примитивы к их владельцам (чистый перенос) (shared-primitives-move)

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

### 10. Именованные входы collaboration-домена в Run (collab-entry-points)

Сделать владельцем порядка восстановления и запуска циклов collaboration сам домен. До: Run перечисляет шесть recover-вызовов и два цикла домена. После: Run вызывает recoverCollaboration и startCollaborationLoops в тех же точках; triggerWithSeq и handleEvent не меняются (синхронная воронка — контрдовод Codex). Польза — локальная ясность Run; «ничего не менять» — допустимый выбор.

Effort: small
Prerequisites: None

1. Добавить в новый pkg/orchestrator/collaboration.go метод recoverCollaboration(ctx) — ровно текущая последовательность orchestrator.go:813-818 в том же порядке (recoverAssistants, recoverFeedbackIntents, recoverMeshWaits, recoverUnreadOfFinishedStages, releaseUnanswerableWaits(ctx, o.resumeWaiterAfterTerminalWaitAtStartup), recoverMeshWaitDeadlines); в Run вызывать его сразу после startPlanningForPending под тем же !poisoned.
2. Добавить startCollaborationLoops(ctx, poisoned bool) (stop func()): startWatchPoller при AssistantsEnabled и startLoopScheduler при AssistantsEnabled && !poisoned, stop — нынешний stopLoops или no-op. В Run вызывать после startQuestionPoller и оставить defer stop() в той же позиции (LIFO: стоп циклов раньше cancel и WaitAgents).
3. triggerWithSeq и handleEvent не менять: порядок пост-эффектов (waiters/unread/unanswerable до TryPublish, watchers и feedback intents после lifecycle) — осознанная единая воронка.

**Verification**

- go test -race ./pkg/orchestrator -run 'Recover|Restart|Startup|Shutdown|Mesh|Unread|Unanswerable|Watch|Feedback|Loop|Peer|ReviewPause'.
- Построчная сверка последовательности вызовов Run до и после (git diff --color-moved); scripts/check-comments.sh <base> чист для диффа этапа; make lint без замечаний.

**Risks**

- Смена позиции defer stop() нарушит гарантию «тик не переживёт ран и не пишет в закрытый Store».
- Отклонены: подпакет collaboration, реестр наблюдателей переходов, асинхронная шина.

### 11. Разделить pollQuestions на отбор, авто-ответ и показ пользователю (dialog-poll-split)

Сделать цикл поллера читаемым: общий предикат активного статуса и две ветки как именованные методы. До: два дублированных switch и две длинные встроенные ветки. После: цикл отбора вызывает autoAnswerQuestion или surfaceQuestion; порядок операций прежний.

Effort: small
Prerequisites: None

1. Проверить характеризацию существующими dialog_* тестами: порядок question → auto_answered, O_EXCL-запись ответа до FSM, неудачная запись не помечается processed, seq перехода прикреплён к ask_user, preAskPhase; добавить недостающий сценарий, если какой-то из них не покрыт.
2. В dialog_poller.go выделить isDialogActive(status state.StageStatus) bool для двух циклов (71-74, 83-86), autoAnswerQuestion(...) — ветку авто-ответа и surfaceQuestion(...) — ветку показа пользователю (preAskPhase, EvAskUser с seq, EventAskUser). Порядок операций без изменений.

**Verification**

- go test -race ./pkg/orchestrator -run 'Dialog|Poller|AutoAnswer|Question|Interactive|Malformed|Digest'.
- scripts/check-comments.sh <base> чист для диффа этапа; make lint без замечаний.

**Risks**

- Поллер не должен начать выводить завершение стадии из файлов (AGENTS.md: runWithRetry — канон).

### 12. Одна таблица kind → раннеры вместо трёх switch и двух ручных развилок (runner-kind-dispatch)

Свести выбор раннера по kind к одному месту в stage_spawn.go. До: три switch и две ручные развилки autonomous/implementation. После: одна функция отображения; onAgentCompleted и reviseAwaitingAssistants используют withFeedbackRunner(kind).

Effort: small
Prerequisites: shared-primitives-move

1. В stage_spawn.go (после shared-primitives-move) заменить три switch (withFeedbackRunner, plainRunner, feedbackTurnRunner) одной функцией runnersFor(kind) с полями fresh, withFeedback, feedbackTurn; обёртки сохраняют имена и сигнатуры для вызывающих.
2. orchestrator.go:983-993 (onAgentCompleted, revising) и control_api.go:200-206 (reviseAwaitingAssistants) заменить на o.spawnKind(ctx, *stage, kind, o.withFeedbackRunner(kind)), где kind — kindOfPhase(agentType) и executionKind(*stage) соответственно; эквивалентно, так как там возможны только autonomous и implementation.
3. Прямой SpawnAgent для planning-revise в reviseAwaitingApproval (control_api.go:228) не трогать: он намеренно без lease и auto-attach. Функции определения kind (executionKind, computeResumeKind, revisingKind, kindOfPhase) не объединять.

**Verification**

- Тест эквивалентности: revising autonomous и implementation стадии после completion и после reviseAwaitingAssistants запускают тот же раннер, что до правки.
- go test -race ./pkg/orchestrator -run 'Revise|Resume|Recover|Autonomous|Review|Planning|ReviewPause|Feedback'.
- scripts/check-comments.sh <base> чист для диффа этапа; make lint без замечаний.

**Risks**

- Неверный kind при замене ручной развилки запустит не тот раннер — закрывается тестом эквивалентности.

## Implementation plans

1. [plan-one](plans/01-plan-1.md)
2. [plan-two](plans/02-plan-2.md)
3. [plan-tree-hard](plans/03-plan-3.md)

Execute these plans separately in the listed dependency order. Revalidate the working tree and tests between plans.

## Deferred proposals


## Analysis limits

- Scope: pkg/orchestrator на ревизии 665223bb — корневой пакет (59 не-тестовых файлов) и подпакеты bus, concurrency, graph, stagefiles, verify. cmd/afm, pkg/server, pkg/state и pkg/executor показаны только как контекст границ; vendor/, sdk/, schema/, docs/ и dashboard pkg/web не анализировались. Отсутствие анализа означает «неизвестно», а не «здорово».
- Граф — курируемая карта компонентов (38 узлов, 66 рёбер), а не полный call graph. Рёбра подтверждены чтением кода и синтаксическим go/ast-графом вызовов Claude (без type-checking: вызовы через интерфейсы и значения-функции учтены только там, где имя встречается текстуально). Число «различных функций» на рёбрах collaboration — метрика этого графа.
- Метрики: loc — строки файла или диапазона символа (wc -l, с комментариями; у корневого пакета — loc strictacode); complexity — цикломатика, посчитанная go/ast-инструментом Claude; rp/op — сигналы strictacode 0.0.12. Strictacode для Go здесь неполон: top-N отчёт, *Orchestrator посчитан с 36 методами вместо 526, stagefiles не оценён, «critical» получили однострочные геттеры. Узлы без метрик не измерялись.
- Тесты: Codex прогнал go test -race ./pkg/orchestrator/... (go1.26.10 linux/arm64) — все 6 пакетов зелёные; Claude тесты не запускал; make lint не запускался (он применяет исправления). Дефект graph.Registry воспроизведён scratch-программой Codex; гипотеза гонки cancelRun не воспроизводилась.
- Глубина чтения: детально — orchestrator.go, retry.go, agents.go, runner_factory.go, verify.go, scheduling.go, control_api.go, recovery.go, reviewpause.go (запуск), graph/registry.go, concurrency, bus/fsm.go, notices.go, dialog_poller.go (pollQuestions), assistants_lifecycle.go (runHelperTask), ключевые функции assistants.go; выборочно — остальной collaboration-домен (agentcomm_*, proposal*, discussion*, feedback_intent.go, watch_tasks.go, team.go), hooks.go, graph_mutate.go, quota.go. Непрочитанные ветки — неизвестны.
- Churn — git log ветки refactor с 2026-07-01; атрибуция к функциям по заголовкам хунков (приблизительно).

The target graph describes an agreed design. Its implementation and future quality metrics have not been measured.
