# plan-two

Упростить ядро pkg/orchestrator и прояснить границы его подсистем (mesh/agentcomm, assistants/helpers, verify, диалог, scheduling/recovery и др.): уменьшить связность и размер перегруженных компонентов без изменения поведения.

Source revision: 665223bbe2fecf75b5e52a726df7594b3149a800
Proposal revision: 1
Approval event: 60

## Required plans

- [plan-one](01-plan-1.md)

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

### 1. Разделить runHelperTask на подготовку, классификацию неуспеха и коммит результата (helper-turn-phases)

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

### 2. Собрать оформление исходов verify-шага в verificationPass (verify-pass-outcomes)

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

### 3. Именованные входы collaboration-домена в Run (collab-entry-points)

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

### 4. Разделить pollQuestions на отбор, авто-ответ и показ пользователю (dialog-poll-split)

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

### 5. Одна таблица kind → раннеры вместо трёх switch и двух ручных развилок (runner-kind-dispatch)

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

## Architecture snapshots

- [Before](02-plan-2.before.json)
- [After](02-plan-2.after.json)

Snapshots follow the full execution order in index.json, including earlier independent plans. Revalidate the source and tests before each implementation.

This is an approved plan; no code has been changed.
