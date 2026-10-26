# plan-tree-hard

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

### 1. Вынести ветку успешного выхода агента из runRetryLoop в метод решения (retry-success-branch)

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

## Architecture snapshots

- [Before](03-plan-3.before.json)
- [After](03-plan-3.after.json)

Snapshots follow the full execution order in index.json, including earlier independent plans. Revalidate the source and tests before each implementation.

This is an approved plan; no code has been changed.
