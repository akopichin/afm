# Typed Board Tasks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Board-задачи новых ранов становятся типизированными: явное назначение, жизненный цикл assigned → in_progress → done, статус карточки считается на сервере из назначений, а не из текста отчётов.

**Architecture:** Новая проекция `MeshState.BoardTasks` с durable-эффектами в `pkg/state`, одна точка входа оркестратора `BoardTask(ctx, req)` (create/assign/claim/start/progress/done/release/reopen/edit), тонкие HTTP/CLI-адаптеры. Протокол `typed_v1` фиксируется control-записью при создании рана; старые раны работают по-прежнему. Dashboard получает задачи в том же `GET /api/mesh/board` и рисует их теми же карточками.

**Tech Stack:** Go (pkg/agentcomm, pkg/state, pkg/orchestrator, pkg/server, pkg/prompts, cmd/afm), React + TS + Vitest (pkg/web/dashboard).

**Spec:** `/Users/alexander.kopichin/work/personal/afm/tmp/fix-board-tasks.md` (прошёл codex-ревью; §8.2 — вариант с уведомлением координатору). Связанный отложенный план: `tmp/fix-deadlock.md` (вне объёма).

## Global Constraints

- Go-версию в `go.mod` не менять; новых зависимостей не добавлять.
- Только новые раны: протокол `typed_v1` пишется, только если `store.Snapshot().LastSeq == 0` при старте. Resume без marker — прежний режим, GET ничего не пишет.
- Статус задачи НИКОГДА не выводится из текста (progress/result/description).
- Личность актора — из bearer token / UI actor, не из тела запроса.
- `BoardTask` ≠ helper `assistants.Task`; имена событий не `task_*` (они заняты helper-задачами) — префикс `mesh_board_task_`.
- Все мутации — через `Store.ApplyRunOperation`; post-commit доставка через `deliverCommittedMessages`; run context из `o.runContext(ctx)`.
- Коммиты на русском, без Co-Authored-By. CHANGELOG/docs — английский (отдельный этап пайплайна).
- Тесты оркестратора: cleanup = cancel run ctx → дождаться Run → закрыть Store.
- После правок: `make lint` чистый.

## Decisions vs spec (зафиксировано при грумминге кода)

- `agentcomm.Message` не имеет поля метаданных. Сообщения Assign/уведомления — читабельный текст с key и assignment ID; исполнитель берёт ID из блока `<your_board_tasks>` в промпте (Task 7) и из `task list`. Ничего не парсится из body.
- Claim = эффект `assigned` с `Start: true` (один эффект → одна смена версии).
- Done допустим из `assigned` и `in_progress`; Progress не меняет состояние назначения.
- UI получает задачи в ответе `GET /api/mesh/board` (`tasks`, `typed_tasks`), отдельного poll нет. Агенты читают через action `tasks`.
- Create принимает необязательный `assign_to` — создание+назначение одной операцией (форма dashboard).
- Редактирование описания — только API/CLI; в UI в этой итерации нет.
- SDK board-методов не имеет — SDK не трогаем.

## Review Focus

1. Повтор запроса с тем же request ID после того, как состояние изменилось (уведомление было/не было пропущено) — должен вернуть исходный receipt, а не conflict и не новую операцию. → Task 4, `TestBoardTaskRetryReturnsOriginalReceipt`.
2. Агент пишет `done — ...` прогрессом или вызывает `board publish task/x` в typed-ране — статус не меняется, publish отклонён. → Task 3/5 тесты.
3. Исполнитель исчез из live roster (стадия завершилась) — карточка сохраняет исполнителя и состояние. → Task 9 (`taskFromView` с участником не из roster), Task 3 (`Status()` не зависит от roster).
4. Старый ран (нет marker) открыт новым бинарником — UI идёт по старому пути, POST task отклоняется без записи. → Task 2, Task 8.
5. Смена runId во время ответа `GET board` — старые tasks не попадают в новый ран. → Task 9 (`use-mesh-board` тест с tasks).

---

## File Structure

| Файл | Ответственность |
|---|---|
| `pkg/agentcomm/board_task.go` (new) | Типы BoardTask/Assignment/TaskView, `Status()`, `Coordinator()`, `Active()` |
| `pkg/state/mesh_board_tasks.go` (new) | Payload-типы, apply-функции 7 эффектов, проверки переходов/прав/CAS |
| `pkg/state/mesh_board_protocol.go` (new) | Control `mesh_board_protocol_set`, `Store.RecordBoardTaskProtocol` |
| `pkg/state/record.go`, `assistants_projection.go`, `mesh_projection.go` | Регистрация kinds, dispatch, newMeshState/clone/deepClone |
| `pkg/orchestrator/agentcomm_board_tasks.go` (new) | `BoardTask(ctx, req)`, планировщики действий, replay, уведомления |
| `pkg/orchestrator/agentcomm_board.go` | Вынести общий `planNoticeTo`; reject `task/` в typed-ране |
| `pkg/orchestrator/agentcomm_inbox.go`, `pkg/prompts/agentcomm.go` | Блок `<your_board_tasks>`, typed-инструкции |
| `pkg/server/agent_mesh.go`, `pkg/server/mesh.go`, `cmd/afm/run_agent_mesh.go`, `cmd/afm/run_mesh.go` | HTTP-адаптеры |
| `cmd/afm/agent_task.go` (new), `cmd/afm/agent.go`, `cmd/afm/agent_mesh_client.go` | CLI `afm agent task …`, `--request-id` |
| `cmd/afm/run.go` | Запись протокола для свежего рана |
| dashboard: `api/mesh-client.ts`, `hooks/use-mesh/use-mesh-board.ts`, `lib/board-tasks.ts`, `components/team/Board*.tsx`, `TeamView.tsx` | TaskView в UI |

---

### Task 1: Типы BoardTask и агрегация статуса

**Files:**
- Create: `pkg/agentcomm/board_task.go`
- Test: `pkg/agentcomm/board_task_test.go`

**Interfaces:**
- Produces:
  ```go
  type BoardTaskProtocol string            // "" (legacy) | BoardTaskProtocolTypedV1 = "typed_v1"
  type AssignmentState string             // AssignmentAssigned="assigned", AssignmentInProgress="in_progress", AssignmentDone="done"
  type AssignmentEnd string               // "" | AssignmentReleased="released" | AssignmentReopened="reopened"
  type BoardTaskStatus string             // BoardTaskTodo="todo", BoardTaskInProgress="in_progress", BoardTaskDone="done"
  type BoardTaskAssignment struct {
      ID            string          `json:"id"`
      ParticipantID ParticipantID   `json:"participant_id"`
      State         AssignmentState `json:"state"`
      AssignedBy    ParticipantID   `json:"assigned_by"`
      Progress      string          `json:"progress,omitempty"`
      Result        string          `json:"result,omitempty"`
      ArtifactRefs  []string        `json:"artifact_refs,omitempty"`
      AssignedSeq   uint64          `json:"assigned_seq"`
      StartedSeq    uint64          `json:"started_seq,omitempty"`
      CompletedSeq  uint64          `json:"completed_seq,omitempty"`
      Ended         AssignmentEnd   `json:"ended,omitempty"`   // set only in History
      EndedBy       ParticipantID   `json:"ended_by,omitempty"`
      EndReason     string          `json:"end_reason,omitempty"`
      EndedSeq      uint64          `json:"ended_seq,omitempty"`
  }
  type BoardTask struct {
      Key           string                `json:"key"`
      AuthorID      ParticipantID         `json:"author_id"`
      CoordinatorID ParticipantID         `json:"coordinator_id,omitempty"`
      Title         string                `json:"title"`
      Description   string                `json:"description,omitempty"`
      Version       int                   `json:"version"`
      Generation    int                   `json:"generation"`
      Assignments   []BoardTaskAssignment `json:"assignments"`          // active only
      History       []BoardTaskAssignment `json:"history,omitempty"`    // released/archived, oldest first
      CreatedSeq    uint64                `json:"created_seq"`
      UpdatedSeq    uint64                `json:"updated_seq"`
      CreatedAt     time.Time             `json:"created_at"`
      UpdatedAt     time.Time             `json:"updated_at"`
  }
  func (t BoardTask) Status() BoardTaskStatus
  func (t BoardTask) Coordinator() ParticipantID   // CoordinatorID, else AuthorID unless AuthorID == "user" (then "")
  func (t BoardTask) Assignment(id string) (BoardTaskAssignment, bool)        // active
  func (t BoardTask) EndedAssignment(id string) (BoardTaskAssignment, bool)   // history
  func (t BoardTask) ActiveFor(p ParticipantID) (BoardTaskAssignment, bool)
  type BoardTaskView struct { BoardTask; Status BoardTaskStatus `json:"status"` }
  func ViewOf(t BoardTask) BoardTaskView
  func ValidBoardTaskKey(key string) bool   // "task/<slug>", slug non-empty, no further "/", ≤ 200 bytes
  ```
  Пользовательский participant ID — существующая константа `"user"`; если в agentcomm её нет, объявить `UserParticipantID ParticipantID = "user"` здесь и использовать в orchestrator вместо литерала.

- [ ] **Step 1: Write failing tests** `TestBoardTaskStatus` (table): нет назначений → todo; все assigned → todo; assigned+in_progress → in_progress; done+assigned → in_progress; все done → done; только History(done) без активных → todo. `TestBoardTaskCoordinator`: author agent → author; author user без coordinator → ""; coordinator задан → coordinator. `TestValidBoardTaskKey`: `task/a` ok; `task/` no; `task/a/b` no; `fact/a` no.
- [ ] **Step 2:** `go test ./pkg/agentcomm -run BoardTask` → FAIL (undefined).
- [ ] **Step 3:** Реализовать файл.
- [ ] **Step 4:** тест → PASS.
- [ ] **Step 5:** Commit `Добавить типы типизированных задач Board`.

---

### Task 2: Durable-протокол рана

**Files:**
- Create: `pkg/state/mesh_board_protocol.go`, `pkg/state/mesh_board_protocol_test.go`
- Modify: `pkg/state/record.go` (kind const + `controlKinds`), `pkg/state/assistants_projection.go` (`dispatchControlKind`), `pkg/state/mesh_projection.go` (поле `BoardTaskProtocol agentcomm.BoardTaskProtocol` в `MeshState`; скаляр — clone не трогать), `cmd/afm/run.go` (после `resumed := …`)

**Interfaces:**
- Produces: `const kindMeshBoardProtocolSet = "mesh_board_protocol_set"`; `func (s *Store) RecordBoardTaskProtocol(p agentcomm.BoardTaskProtocol, actor Actor) error` — plain `AppendControl` (как `EnsurePolicyRecorded`); no-op, если протокол уже записан; отказ (`OperationRejectedError`, code invalid_status), если `LastSeq > 0` и протокола нет (resume старого рана не мигрирует). `RunState.Assistants.Mesh.BoardTaskProtocol`.
- В `run.go`: `if !resumed { if err := store.RecordBoardTaskProtocol(agentcomm.BoardTaskProtocolTypedV1, state.Actor{Kind: "system", ID: "afm"}); err != nil { return fmt.Errorf("record board task protocol: %w", err) } }` — до `startCollaboration`.

- [ ] **Step 1: Failing tests:** `TestRecordBoardTaskProtocolFreshRun` (Open пустого dir → Record → reopen Store → протокол `typed_v1`); `TestRecordBoardTaskProtocolRefusesOldRun` (журнал с одним любым control, без marker → Record возвращает ошибку, журнал не вырос); `TestBoardTaskProtocolSurvivesMissingSnapshot` (удалить state.json → reopen → typed_v1); `TestRecordBoardTaskProtocolIdempotent` (второй вызов не пишет).
- [ ] **Step 2:** FAIL.
- [ ] **Step 3:** Реализовать; payload `{"protocol":"typed_v1"}`.
- [ ] **Step 4:** PASS + `go test ./cmd/afm -run Run` зелёный.
- [ ] **Step 5:** Commit `Фиксировать протокол задач Board при создании рана`.

---

### Task 3: Проекция и эффекты задач

**Files:**
- Create: `pkg/state/mesh_board_tasks.go`, `pkg/state/mesh_board_tasks_test.go`
- Modify: `record.go` (7 kinds + allowlist), `assistants_projection.go` (dispatch), `mesh_projection.go` (`BoardTasks map[string]*agentcomm.BoardTask` в newMeshState/clone/deepClone — deepClone копирует задачу вместе со slices `Assignments`, `History`, `ArtifactRefs`)

**Interfaces:**
- Consumes: Task 1 types, `boardRejected`, `as.resolvePendingWaits` (вызывать после каждого эффекта, как board — threadless waits будятся сообщениями, но порядок сохраняем единообразным).
- Produces (kinds and payloads; Target.BoardKey = key, Target.ParticipantID = actor):
  ```go
  kindMeshBoardTaskCreated   = "mesh_board_task_created"   // {Title, Description, CoordinatorID, ExpectedVersion(=0)}
  kindMeshBoardTaskAssigned  = "mesh_board_task_assigned"  // {AssignmentID, ParticipantID, Start bool, ExpectedVersion}
  kindMeshBoardTaskStarted   = "mesh_board_task_started"   // {AssignmentID, ExpectedVersion}
  kindMeshBoardTaskProgressed= "mesh_board_task_progressed"// {AssignmentID, Text, ArtifactRefs, ExpectedVersion}
  kindMeshBoardTaskCompleted = "mesh_board_task_completed" // {AssignmentID, Result, ArtifactRefs, ExpectedVersion}
  kindMeshBoardTaskReleased  = "mesh_board_task_released"  // {AssignmentID, Reason, ExpectedVersion}
  kindMeshBoardTaskReopened  = "mesh_board_task_reopened"  // {Reason, ExpectedVersion}
  kindMeshBoardTaskEdited    = "mesh_board_task_edited"    // {Title?, Description?, CoordinatorID?, ExpectedVersion}
  ```
  Экспортировать payload-типы (`MeshBoardTaskAssignedPayload` и т.д.) — оркестратор их маршалит и читает при replay. Ошибки: `ErrBoardTaskStaleVersion`, `ErrBoardTaskNotAuthorized`, `ErrBoardTaskInvalidTransition`, `ErrBoardTaskProtocol` — все через `boardRejected` с кодами `stale_revision` / `forbidden` / `invalid_status` / `invalid_status`.

Правила в apply (единственное место проверки — live и replay одинаковы):
- Любой эффект: `as.Mesh.BoardTaskProtocol == typed_v1`, иначе `ErrBoardTaskProtocol`. Ключ валиден. Задача существует (кроме created; created на существующий ключ → stale_revision). `ExpectedVersion == task.Version`. Успех → `Version++`, `UpdatedSeq/At` из Control.
- Права (`actor` = Target.ParticipantID): управление (assign чужого, release, reopen, edit) — `actor == "user"` или `actor == task.Coordinator()`. Claim (`Start && ParticipantID == actor`): нет активных назначений. Self-assign координатором без Start — разрешено. Start/Progress/Done — `actor == assignment.ParticipantID`, назначение активно. Release своего назначения исполнителем — запрещено (только управление).
- Assigned: у участника нет другого активного назначения; `ParticipantID != "user"`; AssignmentID уникален среди Assignments+History.
- Started: только из assigned. Progressed: из assigned|in_progress, состояние не меняется. Completed: из assigned|in_progress → done. Released: только не-done; переносит в History с Ended=released. Reopened: переносит ВСЕ активные в History с Ended=reopened, `Generation++`. Edited: только переданные поля.
- Текст progress/result/description ограничен `agentcomm.MaxBodyBytes`, refs — `MaxArtifactRefs` и префикс `artifacts/` (как board).

- [ ] **Step 1: Failing tests** (каждый — через `ApplyRunOperation` на Store с записанным протоколом; helper `applyTaskEffects(t, store, actor, effects...)`):
  - `TestBoardTaskLifecycle`: create → assign(B) → статус todo, B виден → start → in_progress → progress("done — all good") → состояние in_progress (текст не влияет) → done → done.
  - `TestBoardTaskMultiAssignee`: A и B; done A → in_progress; done B → done.
  - `TestBoardTaskStaleDoneAfterReassign`: assign B(a1) → release a1 → assign B(a2) → done(a1) отклонён, a2 не изменился.
  - `TestBoardTaskReopenArchivesAll`: две активных → reopen → Assignments пусто, History 2 с ended=reopened, status todo, generation 1.
  - `TestBoardTaskReleaseDoneRejected` (одиночный и двойной случай).
  - `TestBoardTaskDuplicateActiveAssignmentRejected` (assign B дважды; claim после assign B тем же B).
  - `TestBoardTaskClaim`: свободная → claim → in_progress; вторая claim другим агентом (тот же expected version и следующая) → отклонена.
  - `TestBoardTaskAuthorization`: не-координатор assign → forbidden; исполнитель start чужого → forbidden; user может всё управление; агент-создатель — координатор; задача user с coordinator X → X управляет, автор `user` без coordinator → только user.
  - `TestBoardTaskStaleVersion`: два assign с одним expected version → второй stale, проекция неизменна.
  - `TestBoardTaskRejectedBatchDoesNotLeak`: операция [assigned ok, started invalid] отклонена → `Snapshot()` без назначения (проверка deepClone slices).
  - `TestBoardTaskReplayMatchesLive`: прогнать lifecycle, закрыть, удалить state.json, Open → `reflect.DeepEqual` BoardTasks.
  - `TestBoardTaskRequiresProtocol`: Store без marker → created отклонён, журнал не вырос.
- [ ] **Step 2:** FAIL.
- [ ] **Step 3:** Реализовать.
- [ ] **Step 4:** `go test -race ./pkg/state` PASS.
- [ ] **Step 5:** Commit `Добавить durable-проекцию задач Board`.

---

### Task 4: Сервис оркестратора (без сообщений)

**Files:**
- Create: `pkg/orchestrator/agentcomm_board_tasks.go`, `pkg/orchestrator/agentcomm_board_tasks_test.go`
- Modify: `pkg/orchestrator/agentcomm.go` (opKind `opKindMeshBoardTask = "mesh_board_task"`, методы в `Service` interface)

**Interfaces:**
- Produces:
  ```go
  type BoardTaskAction string // "create","assign","claim","start","progress","done","release","reopen","edit"
  type BoardTaskRequest struct {
      OperationID     string
      Action          BoardTaskAction
      Actor           agentcomm.ParticipantID   // set by adapter from token/UI, never from body
      Key             string
      ExpectedVersion int
      AssignmentID    string
      To              string                    // assign / create.assign_to: id or alias
      Title, Description string
      CoordinatorID   string                    // create/edit (alias or id)
      Text            string                    // progress text / done result / release|reopen reason
      ArtifactRefs    []string
  }
  type BoardTaskReceipt struct {
      Key          string                    `json:"key"`
      Version      int                       `json:"version"`
      Status       agentcomm.BoardTaskStatus `json:"status"`
      AssignmentID string                    `json:"assignment_id,omitempty"`
      Seq          uint64                    `json:"seq"`
      MessageID    agentcomm.MessageID       `json:"message_id,omitempty"`
      ThreadID     agentcomm.ThreadID        `json:"thread_id,omitempty"`
      NoticeSkipped string                   `json:"notice_skipped,omitempty"` // reason
  }
  func (o *Orchestrator) BoardTask(ctx context.Context, req BoardTaskRequest) (BoardTaskReceipt, error)
  func (o *Orchestrator) BoardTasks(ctx context.Context, key string) ([]agentcomm.BoardTaskView, error) // sorted by key; unknown key → not found
  func (o *Orchestrator) BoardTasksEnabled() bool // snapshot protocol == typed_v1
  ```
- Assignment ID = `deriveMeshID("board-assignment", opID)` (детерминирован для retry).
- Receipt хранит состояние после ЭТОЙ операции: строится из effect-payload + `OperationResult.Seq`; Version = expected+1, Status — вычислен на копии проекции до commit.
- Replay: `replayedBoardTask(opID, req)` через `Store.CommittedOperation(opID, opKindMeshBoardTask)`. Если операция есть — сравнить запрос с payload её первичного эффекта `mesh_board_task_*` (key из Target, expected version, assignment ID, participant, text/title/description/refs); совпал → собрать receipt из этих payload'ов и эффекта `mesh_message_sent` (если был) + `Result.Seq`; не совпал → `OperationIDConflictError`. Каждый payload Task 3 дополнительно несёт `Status agentcomm.BoardTaskStatus` (статус ПОСЛЕ эффекта, заполняет оркестратор при планировании, apply сверяет с вычисленным и отклоняет расхождение) и `NoticeSkipped string json:"notice_skipped,omitempty"` (apply игнорирует) — так receipt повтора не зависит от текущей проекции.
- В этой задаче action assign/release/reopen/done/create(assign_to) работают без сообщений (Task 5 добавит). Отказ, если `!BoardTasksEnabled()` — code `invalid_status`, «board tasks are not enabled for this run».

- [ ] **Step 1: Failing tests** (orchestrator test harness как в `agentcomm_board_test.go`; Store с протоколом):
  - `TestBoardTaskServiceLifecycle`: create/claim/progress/done через `BoardTask`, receipts содержат version/status.
  - `TestBoardTaskRetryReturnsOriginalReceipt`: done с opID X → затем reopen → повтор done с X возвращает первый receipt (status done, version исходный), журнал не вырос; тот же X с другим Text → conflict.
  - `TestBoardTaskDisabledForLegacyRun`: Store без marker → любое действие отклонено, `BoardTasks` → пусто без ошибки, журнал не вырос.
  - `TestBoardTaskActorFromRequestOnly`: `Actor` агента B пытается start назначения C → forbidden.
  - `TestBoardTaskAliasResolution`: assign `To: "dev-backend"` (alias) → ParticipantID резолвится через `resolveRecipients`; неизвестный alias → not found.
- [ ] **Step 2:** FAIL. **Step 3:** реализовать. **Step 4:** `go test -race ./pkg/orchestrator -run BoardTask` PASS.
- [ ] **Step 5:** Commit `Добавить сервис задач Board в оркестратор`.

---

### Task 5: Сообщение при Assign, уведомления координатору, запрет `task/` в publish

**Files:**
- Modify: `pkg/orchestrator/agentcomm_board_tasks.go`, `pkg/orchestrator/agentcomm_board.go`
- Test: `pkg/orchestrator/agentcomm_board_tasks_messages_test.go` (new), `agentcomm_board_test.go`

**Interfaces:**
- Produces (extract from `planBoardNotices`, used by both):
  ```go
  // planNoticeTo plans one message from sender to recipient in a fresh/reused conversation thread.
  // skip != "" means it cannot be delivered now (unreachable, inbox full, thread full) — not an error.
  func (o *Orchestrator) planNoticeTo(rs state.RunState, opID string, sender, recipient agentcomm.ParticipantID, body string, actor state.Actor) (msg plannedMessage, skip string, err error)
  func assignableParticipant(rs state.RunState, id agentcomm.ParticipantID) error // nil or OperationRejectedError
  ```
- `assignableParticipant`: неизвестен → not_found; `user` → invalid_request; не Reachable → invalid_status; helper в `watching`/`waiting_parent` → invalid_status «passive helper is not woken for new work»; stage в `awaiting_assistants` → invalid_status.
- Assign (и create с assign_to) другому участнику: `assignableParticipant` + `planNoticeTo`; skip != "" → вся операция отклоняется (`invalid_status`, причина skip); эффекты = thread/message + assigned; `Expect.Check = o.recipientsReachable(msgs)`; post-commit `deliverCommittedMessages(runCtx, msgs, res.ReadyWaits)`; receipt.MessageID/ThreadID. Self-assign и claim — без сообщения. Body: `"Board task <key> assigned to you (assignment <id>): <title>. Run \`afm agent task start --key <key> --assignment-id <id> --expected-version <v>\` when you begin, \`done\` when finished."` (обрезать до `MaxBodyBytes`).
- Done/Release/Reopen: `coord := task.Coordinator()`; если `coord == "" || coord == actor` — без уведомления. Иначе `planNoticeTo(…, actor→coord, body)`; skip → мутация коммитится без сообщения, `NoticeSkipped = skip`. Body: `"Board task <key>: assignment <id> of <participant> is <outcome>[: <result|reason>]"`, для reopen — список назначений; итог обрезать до `MaxBodyBytes`. Один message на операцию.
- Ретраи как в `Publish`: до 3 попыток при `worthAnotherPublishAttempt`.
- `Publish`: если `BoardTasksEnabled()` и `strings.HasPrefix(req.Key, "task/")` → `invalid_request` «board tasks use `afm agent task …` in this run». Проверку делать до планирования.

- [ ] **Step 1: Failing tests:**
  - `TestBoardTaskAssignSendsMessageAtomically`: assign → в одном seq есть message B и назначение; receipt.MessageID == сообщение; B получает его в inbox.
  - `TestBoardTaskAssignRejectsUndeliverable`: B недоступен / inbox полон (заполнить до `MaxInboxUnread`) / passive helper (`watching`) / стадия `awaiting_assistants` → отказ, ни назначения, ни сообщения.
  - `TestBoardTaskSelfAssignAndClaimNoMessage`.
  - `TestBoardTaskDoneNotifiesCoordinator`: A создал и назначил B; B «принял» (Send reply), A его прочитал (ack); A регистрирует `Wait{addressed_message}` без thread и с deadline → pending; B done без Reply → wait ready, в inbox A уведомление с outcome done.
  - `TestBoardTaskNoticeGoesToCoordinatorNotAssigner`: user назначил B при coordinator A → done B будит A; B claim задачи A → done будит A; user release self-assignment A → A получает released.
  - `TestBoardTaskNoNoticeForCoordinatorOrUser`: done самим координатором; задача user без coordinator → уведомления нет.
  - `TestBoardTaskNoticeSkippedStillCommits`: inbox A полон → done коммитится, `NoticeSkipped != ""`; A завершён (стадия done) → то же.
  - `TestBoardTaskReopenOneNotice`: reopen двух назначений → одно сообщение координатору.
  - `TestBoardTaskReplayNoDuplicateNotice`: после done перезапустить оркестратор на том же Store → сообщений не прибавилось.
  - `TestPublishRejectsTaskNamespaceInTypedRun` и `TestPublishAllowsTaskNamespaceInLegacyRun`.
  - Существующие `TestPublish*` / board-notify тесты зелёные (рефакторинг `planNoticeTo`).
- [ ] **Step 2–4:** FAIL → реализовать → `go test -race ./pkg/orchestrator` PASS.
- [ ] **Step 5:** Commit `Назначение с доставкой и уведомления координатору для задач Board`.

---

### Task 6: HTTP и CLI

**Files:**
- Modify: `pkg/server/agent_mesh.go` (actions `tasks` GET, `task` POST), `pkg/server/mesh.go` (`GET /api/mesh/board` += `tasks`, `typed_tasks`; `POST /api/mesh/tasks` с sameOrigin), `pkg/server/handlers.go` (`capabilities.BoardTasks json:"board_tasks"`), `cmd/afm/run_agent_mesh.go`, `cmd/afm/run_mesh.go`
- Create: `cmd/afm/agent_task.go`; Modify `cmd/afm/agent.go` (register `task`), `cmd/afm/agent_mesh_client.go` (общий `--request-id`)
- Test: `pkg/server/agent_mesh_test.go`, `pkg/server/mesh_test.go`, `cmd/afm/agent_task_test.go`

**Interfaces:**
- HTTP body (оба namespace): `{"request_id","action","key","expected_version","assignment_id","to","title","description","coordinator","text","artifact_refs"}` → `BoardTaskReceipt`. Actor: agent — из token; UI — `"user"`. Ошибки — существующий `writeAssistantError`.
- `AgentMeshActions` += `BoardTask(ctx, requestID, actor string, req MeshBoardTaskRequest) (orchestrator.BoardTaskReceipt, error)`, `BoardTasks(ctx, key string) ([]agentcomm.BoardTaskView, error)`; `MeshActions` += те же без actor. Ключ в query для GET `tasks?key=` экранировать `url.QueryEscape` на клиенте.
- CLI: `afm agent task list|get --key|create --key --title [--description] [--assign-to] [--coordinator]|assign --key --to --expected-version|claim --key --expected-version|start|progress --text|done --summary [--artifacts]|release --reason|reopen --reason|edit` (start/progress/done/release — `--assignment-id`). Все мутации принимают `--request-id` (по умолчанию `newAgentRequestID()`). Вывод — существующий `runAgentCmd` JSON-контракт. Также добавить `--request-id` к `send` и `board publish` через общий helper `requestIDFlag(cmd) *string`.

- [ ] **Step 1: Failing tests:** server: agent POST `task` с `actor`/`participant_id` в body игнорирует их (actor из token); UI POST без same-origin → 403; GET `/api/mesh/board` на legacy → `typed_tasks:false`, без `tasks`; на typed → tasks со `status`; `/api/status` capabilities.board_tasks. CLI: `task done` формирует правильное тело и `--request-id` уходит как `request_id`; `task get --key 'task/a b'` экранирует query.
- [ ] **Step 2–4:** FAIL → реализовать → `go test -race ./pkg/server ./cmd/afm` PASS.
- [ ] **Step 5:** Commit `API и CLI для задач Board`.

---

### Task 7: Промпты агентов

**Files:**
- Modify: `pkg/prompts/agentcomm.go` (`AgentCommInputs.TypedBoardTasks bool`; новый `AgentCommBoardTasksBlock(tasks []AssignedBoardTask, omitted int) string`), `pkg/orchestrator/agentcomm.go` (`agentCommBlockFor`, `agentCommBlockForTask` передают флаг), `pkg/orchestrator/agentcomm_inbox.go` (после requests-блока добавить блок задач участника)
- Test: `pkg/prompts/agentcomm_test.go`, `pkg/orchestrator/agentcomm_board_tasks_prompt_test.go`

**Interfaces:**
- `type AssignedBoardTask struct{ Key, Title, AssignmentID, State, Progress string; Version int }`. Блок `<your_board_tasks>`: до 20 активных назначений участника (по `AssignedSeq`), Progress обрезан до 200 байт, хвост `… and N more — afm agent task list`. Пусто → "".
- Typed-инструкции заменяют абзац про `in progress:`/`done:` (agentcomm.go:62): координатор — `task create` → `task assign` → ждать `afm agent wait --condition addressed_message --deadline-seconds N`, по пробуждению `afm agent inbox` + `afm agent task get`; исполнитель — `task start` / `progress` / `done`; `board publish` только для фактов, не `task/`. Legacy-текст без изменений, когда флаг false.

- [ ] **Step 1: Failing tests:** prompts — typed=true содержит `afm agent task start` и не содержит `in progress:`; typed=false совпадает с прежним выводом (golden из текущего кода); блок задач: усечение и хвост. Orchestrator — участник с назначением видит `<your_board_tasks>` с assignment ID в собранном промпте следующего хода; legacy-ран — блока нет.
- [ ] **Step 2–4:** FAIL → реализовать → PASS (`go test -race ./pkg/prompts ./pkg/orchestrator`).
- [ ] **Step 5:** Commit `Инструкции и контекст задач Board в промптах агентов`.

---

### Task 8: Dashboard — данные

**Files:**
- Modify: `src/api/mesh-client.ts` (`MeshBoardTask` типы, `getMeshBoard` возвращает `{entries, tasks, typedTasks}`, `postMeshTask(req)`), `src/hooks/use-mesh/use-mesh-board.ts` (snapshot += `tasks`, `typedTasks`), `src/lib/board-tasks.ts`
- Test: `api/mesh-client.test.ts`, `hooks/use-mesh/use-mesh-board.test.ts`, `lib/board-tasks.test.ts`

**Interfaces:**
- `MeshBoard = { entries; tasks: MeshBoardTask[]; typedTasks: boolean; status; reload }`.
- `BoardAssignee = { id: string; state: 'assigned'|'in_progress'|'done' }` (legacy: marker in_progress → 'in_progress', done → 'done'; `inProgress` удалить, обновить потребителей).
- `BoardTask` += `title: string; description: string; note?: string /* latest progress or result */; assignments?: MeshBoardTaskAssignment[]; history?: …; version: number`. Legacy заполняет title/description через `splitTaskText` (перенести функцию в lib).
- `taskFromView(view: MeshBoardTask): BoardTask` и `boardTasksOf(board: MeshBoard, participants): BoardTask[]` — `typedTasks ? tasks.map(taskFromView) : boardTasks(entries, participants)`. `boardTaskCountOf(board)`; `boardTopLevelOf(board)` — в typed-режиме только факты (ключи не `task/`).
- `postMeshTask({requestId, action, key, expectedVersion?, assignmentId?, to?, title?, description?, text?}): Promise<BoardTaskReceipt>` → POST `/api/mesh/tasks`.

- [ ] **Step 1: Failing tests:** client маппит snake_case и `typed_tasks`; hook: смена runId отбрасывает поздний ответ с tasks; lib: `taskFromView` — assigned-only → todo с assignee state 'assigned', участник не из roster сохраняется; `boardTasksOf` legacy-путь не изменился (существующие тесты зелёные).
- [ ] **Step 2–4:** FAIL → реализовать → `make web-test web-typecheck` PASS.
- [ ] **Step 5:** Commit `Данные типизированных задач Board в dashboard`.

---

### Task 9: Dashboard — UI

**Files:**
- Modify: `components/team/BoardView.tsx`, `BoardPanel.tsx`, `TeamView.tsx` (заменить вызовы `boardTasks/boardTopLevel/boardTaskCount` на `*Of(board…)`), `BoardTaskCard.tsx` (title/description/note; assignee chip показывает Assigned), `BoardTaskDetails.tsx` (typed: назначения с состоянием, история с ended/by/reason; действия Assign (select reachable агентов из `boardNotifyCandidates`), Release (на активном не-done назначении), Reopen (когда есть назначения)), `BoardTaskForm.tsx` (typed: поле «Assignee» (опционально) вместо notify-чекбоксов, `postMeshTask({action:'create', key, title, description, to})`)
- Test: соответствующие `*.test.tsx`

Требования: действия через `useRetryableSubmit`; конфликт версии (`stale_revision`) → сообщение «Task changed, reloaded» + `board.reload()`, введённый текст формы сохраняется; `useModalDialog` для деталей не меняется; фильтр исполнителя по `assignees[].id` включает недоступных; в legacy-режиме UI и запросы прежние (никаких вызовов `/api/mesh/tasks`).

- [ ] **Step 1: Failing tests:** BoardView typed: assigned-only задача в Todo с именем исполнителя (не Unassigned); done-задача в Done; фильтр по недоступному участнику. Details: Release вызывает `postMeshTask({action:'release', assignmentId, expectedVersion})`; stale → reload и сообщение. Form typed: create с `to`; без notify-чекбоксов. Legacy BoardView тесты без изменений. TeamView badge считает typed tasks.
- [ ] **Step 2–4:** FAIL → реализовать → `make web-test web-typecheck` PASS.
- [ ] **Step 5:** Commit `Отображение и управление типизированными задачами Board`.

---

### Task 10: Финальная проверка

- [ ] `go test -race ./pkg/agentcomm ./pkg/state ./pkg/orchestrator ./pkg/server ./pkg/prompts ./cmd/afm`
- [ ] `make web-test web-typecheck`
- [ ] `make lint build test` (lint без изменений файлов после прогона)
- [ ] Инвариант в `pkg/orchestrator/AGENTS.md` (одна строка): «Board task state is never derived from free text; assignee/actor identity is a durable participant ID».
- [ ] Commit `Инвариант задач Board в AGENTS.md`.

Docs/CHANGELOG, codex code review и live run — следующие этапы dev-pipeline, не задачи этого плана.
