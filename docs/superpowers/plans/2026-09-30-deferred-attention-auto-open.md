# Deferred attention: не терять авто-переход из Feed — план реализации

> **Для agentic workers:** небольшой frontend-only reliability fix. Работать TDD; шаги отмечены `- [ ]`. Старый one-shot контракт ручного возврата в Feed сохранить.

**Цель:** если новая стадия переходит в `awaiting_user_input` или другой attention-статус, пока dashboard временно не имеет права красть фокус, действие должно остаться видимым как beacon и автоматически открыться сразу после снятия suppression — если оно всё ещё актуально. Случайный фокус на checkbox `Pause on focus` suppression включать не должен.

**Наблюдаемый баг:** вопрос появляется в Feed и создаёт светящуюся вкладку `Question`, но workspace остаётся на Feed и уже никогда не переходит к вопросу автоматически.

## Root cause

`App.tsx` сводит три разные причины в один `suppressed: boolean`:

1. сфокусирован любой `TEXTAREA`, `INPUT` или `contenteditable`;
2. открыт файловый/модальный overlay;
3. у любой стадии есть активная операция `Pause on focus`.

`workspaceReducer.reduceSync` при первом снимке attention-очереди добавляет сигнатуры **всех** arrivals в `autoOpenedSignatures` даже при `suppressed === true`. В этот момент view остаётся `feed`, но arrival уже считается использованным. Следующий `sync(..., suppressed=false)` не может открыть его: элемент уже был в предыдущей очереди и его сигнатура уже помечена обработанной.

Дополнительный ложный триггер: `isEditableFocused()` считает редактированием любой `INPUT`, включая checkbox toggle `Pause on focus`. После клика checkbox остаётся `document.activeElement`, хотя пользователь ничего не вводит; вопрос, пришедший в этот момент, сгорает по описанному выше пути.

Нормальный канал доставки не является источником проблемы: `dialog_question`, `ask_user` и `stage_status_changed` входят в `SIGNIFICANT_EVENT_TYPES`, вызывают `/api/status`, а несупрессированный arrival уже покрыт App-тестом авто-перехода на невыбранную стадию.

## Поведенческий контракт

| Сценарий | Ожидаемое поведение |
|---|---|
| Новое attention без suppression | Сразу открыть attention, как сейчас |
| Arrival во время text editing / overlay / `Pause on focus` op | Оставить текущий view (Feed) и показать beacon; НЕ помечать arrival handled |
| Suppression снят, status всё ещё attention | Автоматически открыть первый ещё не handled item в серверном топологическом порядке |
| Item разрешился до снятия suppression | Убрать из очереди; ничего не открывать задним числом |
| Пользователь вручную открыл deferred beacon | Считать item handled; после возврата в Feed не открывать его повторно |
| Auto-open уже состоялся, пользователь вручную вернулся в Feed | Не переоткрывать тот же episode на следующих poll'ах — существующий one-shot контракт |
| Та же стадия вошла в тот же attention-kind с новым `updatedAt` | Новый episode снова eligible для auto-open |
| Во время suppression пришло несколько items | После снятия открыть первый в server order; остальные помечаются handled и остаются в очереди — доступны через prev/next и resolution-advance, но по одному после ручного возврата в Feed НЕ авто-открываются |
| Активный attention-item разрешился, пока view === `attention` (в т.ч. под suppression) | Advance активного элемента на следующий по `items` — suppression НЕ замораживает intra-attention переход (иначе attention-панель показала бы пустоту). Это осознанное исключение: suppression запрещает только кражу фокуса из non-attention view В attention, а не продвижение уже открытого attention |
| Click-control `input` (checkbox/radio/button/submit/reset/image) просто в фокусе | Не считать редактированием; arrival открывается сразу. Прочие input (в т.ч. date/time/range/color/file) остаются editing — подавляют, как и раньше |

Решение применяется ко всем attention kinds (`approval`, `question`, `failed`, `hook_failed`, `paused`): suppression означает «не красть фокус из non-attention view **сейчас**», а не «навсегда считать unseen action просмотренным». Ручной `openFeed` после реального auto/manual open остаётся явным dismiss текущего episode.

## Архитектура

### Один набор `handledSignatures` вместо двух очередей

**Не** вводить отдельное `deferredSignatures`. «Deferred» — это производное
состояние: элемент deferred ⟺ его `sig` есть в текущих `items` И его ещё нет в
наборе handled. Два связанных массива (`autoOpenedSignatures` + `deferredSignatures`)
завели бы инвариант непересечения и дублирующую filter/dedupe-логику. Достаточно
**переименовать** `autoOpenedSignatures` → `handledSignatures` (после этой задачи
туда попадают и авто-открытые, и вручную открытые, и «показанные под suppression и
затем консумленные» элементы). `prevSigs` становится не нужен и удаляется.

`handled` = «этот arrival уже получил своё единственное auto-open (или был
открыт вручную) — больше сам фокус не крадёт».

На каждом `sync(items, suppressed)`:

1. Отфильтровать `handledSignatures` по текущим `items` — разрешённые/исчезнувшие
   сигнатуры забываются, чтобы повторное появление стало новым arrival.
2. Resolution-advance: если `view === 'attention'` и активный элемент исчез из
   `items` — перейти на `items[0]`, а если очередь пуста — вернуться на
   `returnView`. Эта ветка **не зависит** от `suppressed` (см. строку контракта
   про intra-attention advance). Отдельного consume внутри advance **нет** — за
   пометку handled активного элемента отвечает единое правило (шаг 5).
3. Сформировать candidates = элементы `items` (в их порядке), чья `sig` ещё **не**
   в `handledSignatures`. Это и есть текущие «не-handled» (в т.ч. отложенные ранее
   под suppression и новоприбывшие) — единый источник, без `prevSigs`.
4. Если candidates не пуст:
   - при `suppressed=true` — **ничего не делать** (не помечать handled, не
     открывать): элементы остаются не-handled и просто светятся beacon'ом;
   - при `suppressed=false` — пометить **все** candidates handled; если текущий
     `view !== 'attention'`, открыть **первый** (записав `returnView` от текущего
     view В МОМЕНТ открытия, не в момент arrival) и перевести view в `attention`.

5. **Единое правило «активный attention = показан = handled» (Finding round-3
   CRITICAL).** В конце `sync`, если финальный `view === 'attention'`, найти
   активный item **по ФИНАЛЬНЫМ локалам** (`items.find(it => it.stageId ===
   activeStageId)` от вычисленных в этом же проходе `items`/`activeStageId`), а НЕ
   через `activeItem(state)` по входному state (round-5 CRITICAL: селектор читает
   поля переданного объекта — устаревшие `state.view`/`state.activeStageId`/
   `state.items`; при `A→B` заconsumился бы старый A, при `A/t1→A/t2` — `t1`, а не
   показанный `t2`). Собрать `nextState` из локалов и звать селектор от него —
   эквивалентно. Добавить его **полную** `sig` в `handledSignatures` — **всегда,
   независимо от `suppressed`**. Active резолвится по `stageId`, поэтому при смене
   эпизода уже активной стадии
   (`A/t1 → A/t2` под suppression: `activeStageId` остаётся `A`, resolution-advance
   не срабатывает, панель уже показывает `t2`) новый эпизод `t2` иначе остался бы
   не-handled и после `openFeed`+release ошибочно авто-открылся бы повторно. Это
   правило заменяет частный «consume при advance»: и advance-к-следующему, и
   in-place episode-rollover, и первичное открытие покрываются одной строкой.
   Suppression запрещает красть фокус НА attention из другого view/на другой
   элемент, но текущий активный attention-item показан в панели по определению.

6. В `openAttention` считать вручную открытый target handled: найти его элемент в
   `items` по РЕЗОЛВНУТОМУ `stageId` (после fallback-логики), вычислить полную
   `sig` (со `episode`) и добавить её в `handledSignatures`. Ключ — полная
   сигнатура, а не `stageId`: у стадии может быть несколько эпизодов. Это не даст
   элементу повторно украсть фокус после `openFeed`.
   **Плюс (round-5 IMPORTANT): при переходе из non-attention в attention
   `openAttention` записывает `returnView = globalReturnView(state.view)`** —
   как это делает auto-entry в reduceSync. Иначе вход через beacon из history
   (`cost → history → beacon → resolve`) вернул бы в устаревший `cost`, хотя из
   history контракт требует `feed`. Только на переходе non-attention→attention
   (если уже в attention — не трогать).

`openFeed` сам по себе **не** consume: элемент помечается handled только реальным
auto/manual open. Ещё не открытый пользователем не-handled элемент `openFeed` не
теряет.

Не выводить эту логику в отдельный React state/effect и не открывать таймером:
reducer должен атомарно решать `items + suppression + navigation`, иначе polling
и ручной клик создадут две конкурирующие state machine.

**Тесты проверяют наблюдаемое поведение (view/activeStageId/переоткрытие после
release), а НЕ членство в служебном массиве** — иначе тест закрепит реализацию
вместо контракта.

### Узкое определение editable focus

Вынести из `App.tsx` в тестируемый hook-модуль `hooks/use-is-editing/` ДВЕ вещи:

1. **Чистый предикат `isEditableElement(el: Element | null): boolean`** — принимает
   элемент аргументом (не читает `document`), поэтому тестируется без фокус-возни.
2. **Хук `useIsEditing()`** — обёртка, которая читает `document.activeElement`,
   зовёт `isEditableElement` и подписывается на `focusin`/`focusout`.

`isEditableFocused()` = `isEditableElement(document.activeElement)` (для lazy-init
хука). Такое разделение убирает единственную неудобную для jsdom зависимость из
чистой логики и даёт DRY-точку.

**Минимальный DENYLIST, а не allowlist (round-5 IMPORTANT).** Исходное поведение —
«любой сфокусированный `INPUT` = editing». Доказанно ложен только чистый
click-control (`checkbox` — из репорта, плюс родственные не-текстовые кнопки).
Allowlist текстовых типов был бы scope creep: он newly перестал бы подавлять
`date`/`time`/`range`/`color`/`file`, где пользователь РЕАЛЬНО взаимодействует, а
auto-open, размонтирующий workspace посреди работы с picker'ом, так же неприятен.
Поэтому фиксим ровно доказанный случай и держимся ближе к исходному поведению.

**Editable** (`isEditableElement` → `true`):

- `TEXTAREA`;
- contenteditable-элемент. **jsdom-гоча:** `HTMLElement.isContentEditable` в jsdom
  не реализован (всегда `undefined`), поэтому предикат обязан иметь fallback на
  атрибут: `el.isContentEditable === true || el.getAttribute?.('contenteditable') === '' || el.getAttribute?.('contenteditable') === 'true'`.
  Иначе contenteditable-ветка мертва и в проде, и в тестах.
- `INPUT`, **кроме** чистых click-control типов из denylist:
  `checkbox`, `radio`, `button`, `submit`, `reset`, `image` (case-insensitive).
  Всё остальное (`text`, `search`, `email`, `url`, `tel`, `password`, `number`,
  `date`, `time`, `range`, `color`, `file`, отсутствующий/пустой/неизвестный
  type) — editing, как и в исходном поведении.

**Не editing** (`false`): `INPUT` с типом из denylist; `SELECT` (вне suppression,
как и сейчас); `null`; любой иной элемент.

События `focusin`/`focusout` сохранить (mount-only эффект). На mount
инициализировать состояние фактическим `isEditableFocused()`
(lazy `useState(() => isEditableFocused())`), а не жёстким `false`, чтобы remount
при уже сфокусированном поле не создавал окно ложного auto-open.

**Reconcile-after-commit (round-4 CRITICAL) — обязательно.** `focusin`/`focusout`
покрывают только смену фокуса. Если сфокусированный composer **удаляется из DOM**
(стадия B `running → awaiting_user_input` → `noteTarget=null` → textarea
размонтируется), `focusout` может не выстрелить, и `editing` навсегда застрянет в
`true` — вопрос самой B останется deferred насовсем. Фикс без новых слоёв/
MutationObserver: добавить эффект **без массива зависимостей** (выполняется после
КАЖДОГО commit) `useEffect(() => setEditing(isEditableFocused()))` — он сверяет
состояние с фактическим `document.activeElement` после мутаций DOM. `setEditing`
с тем же значением — no-op в React (без лишних ре-рендеров и цикла). Оба нужны:
listeners — на чистую смену фокуса (не вызывает ре-рендер сама по себе),
reconcile-эффект — на unmount сфокусированного узла (не даёт focus-события, но
даёт commit родителя). Значение, возвращаемое хуком, — из `editing` state; после
unmount reconcile-эффект переведёт его в `false` на следующем commit.

### Как тесты обязаны драйвить фокус (jsdom)

Предикат читает `document.activeElement`, а hook слушает `focusin`/`focusout`.
В jsdom **только** реальный `element.focus()`/`element.blur()` выставляет
`document.activeElement` и диспатчит `focusin`/`focusout`. `fireEvent.focus(el)`/
`fireEvent.blur(el)` (как в текущем `pause on focus`-тесте App) диспатчат лишь
не-всплывающие `focus`/`blur`, **не трогают `activeElement` и не эмитят
`focusin`** — поэтому `useIsEditing` под ними НЕ переключится. Существующий
pause-on-focus App-тест это переживает только потому, что там suppression даёт
`selfOwnedActive`, а не `useIsEditing`. Любой новый тест, который должен
проверить именно suppression от ввода, ОБЯЗАН фокусировать через `.focus()`/
`.blur()` (конвенция уже используется по репозиторию: `StagesList.test.tsx`,
`PasteableTextarea.test.tsx`, `FileBrowserProvider.test.tsx`). Иначе тест —
ложно-зелёный: suppression не наступает, а ассерт «Feed остался» проходит по
другой причине или падает не там.

## Global constraints

- Frontend-only: не менять FSM, dialog poller, `/api/status`, WebSocket payload или серверную attention-классификацию.
- Не менять `AttentionItem.sig`: `stageId + kind + updatedAt` остаётся идентичностью episode.
- Не менять server-provided топологический порядок `items`.
- `suppressed` обязан остаться зависимостью эффекта `sync` в `use-workspace-view.ts` (сейчас `[itemsKey, suppressed]`). Release deferred-элемента опирается на то, что эффект перезапускается при переходе suppression `true→false`; удаление `suppressed` из массива зависимостей молча ломает авто-открытие после снятия suppression.
- Beacon, desktop notifications, title flash и favicon продолжают отражать всю текущую attention-очередь даже при deferred auto-open.
- Не терять draft FeedComposer и не размонтировать workspace во время suppression; переключение происходит только после снятия причины suppression.
- Записи `CHANGELOG.md` — на английском; commit messages — на русском, без `Co-Authored-By`.
- Dashboard gate: `npm run typecheck`, полный `npx vitest run`, затем `npm run build` с обновлением встроенного bundle.

## Task 1: Зафиксировать reducer-контракт failing-тестами

**Files:**
- Modify: `pkg/web/dashboard/src/hooks/use-workspace-view/workspace-view.test.ts`

Все тесты — на НАБЛЮДАЕМОМ поведении (`view`, `activeStageId`, факт
переоткрытия), не на членстве в служебном массиве.

- [ ] RED-тест: arrival при `suppressed=true` оставляет `view=feed`; следующий `sync(..., false)` с тем же item auto-opens его (view=attention, active=item).
- [ ] Deferred item исчез до release: `sync([X], true)` → `sync([], true/false)` — ничего не открывается, view остаётся исходным.
- [ ] Ручной `openAttention(stageId)` во время suppression consumes **именно target, не всю очередь** (round-4 IMPORTANT): defer `[A,B]` (`sync([A,B], true)`), `openAttention('b')`, `openFeed`, release (`sync([A,B], false)`) — авто-открывается **A** (его eligibility НЕ уничтожена открытием B). Тест с одним элементом пропустил бы багу «openAttention помечает handled всю очередь».
- [ ] Несколько candidates, server-order важен: сначала defer B (`sync([B], true)`), затем `sync([A,B], true)` где A стоит перед B в `items`; release (`sync([A,B], false)`) открывает **A** (первый по `items`, не по порядку добавления).
- [ ] «Не украдут фокус позже»: после предыдущего сценария — `openFeed`, затем ещё один `sync([A,B], false)`; B (второй candidate, уже помечен handled на release) сам НЕ авто-открывается. Убедиться, что B при этом остаётся в `items` (доступен через advance).
- [ ] **Новый episode под suppression** (`updatedAt=t2`) после обработанного `t1`. **Важно (round-5 CRITICAL): между `t1` и `t2` вставить `openFeed`** — иначе после открытия `t1` view остаётся `attention`, `t2` показывается как активный и по единому правилу шага 5 становится handled сразу (даже под suppression), и «release открывает t2» недостижимо. Правильная последовательность: `sync([A/t1])` (без suppression → auto-open, t1 handled); `openFeed` (view=feed); `sync([A/t2], true)` (та же стадия, новый эпизод, view=feed → остаётся не-handled, светится); `sync([A/t2], false)` (release) → открывает `t2`.
- [ ] Suppression снят, когда view УЖЕ `attention` (пользователь ранее вручную открыл A): deferred B помечается handled и остаётся в `items` (advance работает), активный A НЕ сбивается на B. Затем `openFeed` + ещё `sync(..., false)` доказывает, что B не украдёт фокус позже.
- [ ] **Active-resolution во время suppression** (Finding 2): открыт A; `sync([A,B], true)` (B defer); `sync([B], true)` — A разрешился. Зафиксировать семантику: intra-attention advance активен независимо от suppression → active переходит на B, view остаётся attention. **B как активный помечается handled** (единое правило шага 5). Продолжение: затем `openFeed` (до release), потом `sync([B], false)` — B НЕ переоткрывается (уже был показан как active).
- [ ] **In-place episode rollover под suppression** (round-3 CRITICAL): открыт `A/t1` (active, handled); persistent suppression; `sync([A/t2], true)` — та же стадия A, новый эпизод, `activeStageId` остаётся A, панель показывает `A/t2`. Затем `openFeed`; `sync([A/t2], false)` (release) — ожидать `view=feed`, `A/t2` НЕ авто-открывается (был показан как активный → handled единым правилом). Без правила шага 5 (consume только при advance) этот тест падал бы: advance не срабатывает, т.к. activeStageId=A всё ещё в items.
- [ ] **Delayed `returnView` — базовый**: старт `view=cost` (`openCost`); suppressed arrival; release открывает attention (returnView=`cost`); resolution (item ушёл) возвращает в `cost`.
- [ ] **Delayed `returnView` — берётся в момент ОТКРЫТИЯ, не arrival** (round-4 IMPORTANT): `openCost`; suppressed arrival; во время suppression `openHistory('plan-history')` (в отличие от `openFeed`, `openHistory` НЕ пишет `returnView` — поэтому тест не ложно-зелёный); release открывает attention → returnView должен вычислиться от текущего view `plan-history` → `feed`; resolution возвращает в `feed`, НЕ в `cost`. (Через `openFeed` баг был бы незаметен: `openFeed` сам ставит returnView=feed.)
- [ ] **Invalid-stage fallback consume на НЕ handled очереди** (round-4 IMPORTANT): defer `[A]` через `sync([A], true)` (A ещё не handled); `openAttention('does-not-exist')` → fallback выбирает фактический A; `openFeed`; release (`sync([A], false)`) — A НЕ переоткрывается (значит консумлен именно фактический fallback, а не запрошенный несуществующий id). На уже-handled очереди тест был бы бессодержательным.
- [ ] **Manual openAttention пишет returnView из history** (round-5 IMPORTANT): `openCost` → `sync([A], true)` (deferred beacon) → `openHistory('plan-history')` → `openAttention('a')` (вход из history) → resolution (`sync([], false)`) — ожидать возврат в `feed` (globalReturnView(history)=feed), НЕ в `cost`.
- [ ] Сохранить/уточнить существующий тест ручного возврата в Feed: уже открытый episode не переоткрывается.
- [ ] Запустить `npx vitest run src/hooks/use-workspace-view/workspace-view.test.ts`; новые тесты должны падать на текущей реализации.

## Task 2: Реализовать handled-set state machine (single set, no deferred queue)

**Files:**
- Modify: `pkg/web/dashboard/src/hooks/use-workspace-view/workspace-view.ts`
- Modify only if type threading is needed: `pkg/web/dashboard/src/hooks/use-workspace-view/use-workspace-view.ts`

- [ ] **Переименовать** `autoOpenedSignatures` → `handledSignatures` в `WorkspaceState`/`initialWorkspaceState`. НЕ добавлять `deferredSignatures`. Удалить `prevSigs` из `reduceSync`.
- [ ] Переписать `reduceSync` по алгоритму из «Один набор `handledSignatures`»: candidates = `items`, чья `sig` не в handled; при `suppressed=true` — no-op; при `suppressed=false` — пометить все candidates handled и (если view!=attention) открыть первый, записав `returnView` в момент открытия. Resolution-advance-ветка не завязана на suppressed и сама handled НЕ трогает.
- [ ] Добавить **единое правило шага 5**: в конце `reduceSync`, если финальный `view === 'attention'`, найти активный item **по ФИНАЛЬНЫМ локалам** (`items.find(it => it.stageId === activeStageId)` от вычисленных в этом проходе `items`/`activeStageId`, ЛИБО собрать финальный `nextState` и звать `activeItem(nextState)`) — **не** `activeItem(state)` по входному устаревшему state (round-5/6 CRITICAL: иначе при `A→B` заconsumится старый A, а B переоткроется после openFeed+release; аналогично сломается rollover `A/t1→A/t2`). Если найден — добавить его полную `sig` в `handledSignatures` (всегда, независимо от suppressed). Это единственная точка, где активный/показанный attention помечается handled; покрывает первичное открытие, advance и in-place episode-rollover. Убрать любой отдельный consume-при-advance.
- [ ] Обновить `openAttention`: manual open добавляет полную `sig` фактически выбранного (после fallback) item в `handledSignatures`. При невалидном `stageId` consume именно фактически выбранный fallback, не запрошенный id. **И** при переходе non-attention→attention записать `returnView = globalReturnView(state.view)` (round-5 IMPORTANT); если уже в attention — не трогать returnView.
- [ ] `openFeed` НЕ трогает `handledSignatures` (не consume): не-handled элемент, который пользователь ещё не открывал, `openFeed` терять не должен.
- [ ] Вынести повторяющуюся фильтрацию `sig`-множеств в маленький pure helper, если она встречается >1 раза (не копипастить set-логику). `items` order не менять.
- [ ] Обновить комментарии reducer/hook: `suppressed` теперь ОТКЛАДЫВАЕТ auto-open (candidate остаётся не-handled и светится), а не permanently marks handled. Убрать устаревший комментарий «Помечаем прибытие обработанным ВСЕГДА (и при suppressed)».
- [ ] Запустить reducer tests → GREEN.

## Task 3: Убрать ложный suppression от checkbox и покрыть hook

**Files:**
- Create: `pkg/web/dashboard/src/hooks/use-is-editing/use-is-editing.ts`
- Create: `pkg/web/dashboard/src/hooks/use-is-editing/use-is-editing.test.ts`
- Create: `pkg/web/dashboard/src/hooks/use-is-editing/index.ts`
- Modify: `pkg/web/dashboard/src/app/App.tsx`

- [ ] Вынести из `App.tsx`: чистый `isEditableElement(el)` (принимает элемент, contenteditable-fallback на атрибут для jsdom), `isEditableFocused()` = `isEditableElement(document.activeElement)`, и хук `useIsEditing()` с lazy-init `useState(() => isEditableFocused())`.
- [ ] **Табличный** unit-тест чистого `isEditableElement` (без фокуса, элемент передаётся напрямую) под DENYLIST-семантику: `true` для `textarea`, `contenteditable="true"`, `contenteditable=""`, и `INPUT` типов `text`/`search`/`email`/`url`/`tel`/`password`/`number`/`date`/`time`/`range`/`color`/`file`, а также input без `type`, с `type=""` и с неизвестным `type="quux"` (всё, что не в denylist); `false` для `INPUT` из denylist (`checkbox`, `radio`, `button`, `submit`, `reset`, `image`), для `select` и для `null`. Denylist case-insensitive (`CHECKBOX` → `false`); неизвестный тип → `true`.
- [ ] Тест `useIsEditing` с реальным `document.activeElement`, драйвя фокус через `element.focus()`/`.blur()` (НЕ `fireEvent.focus`): фокус на textarea → `true`; на checkbox → `false` (перед этим assert `document.activeElement === checkbox`); blur → `false`. Элемент — смонтирован в документ и focusable.
- [ ] **Lazy-init тест**: сфокусировать textarea ДО `renderHook(useIsEditing)`, ожидать `true` сразу на первом рендере (отличает lazy-init от старого `useState(false)`, который дал бы `true` только после focusin).
- [ ] Focus transfer editable→checkbox и checkbox→editable: `focusout`/`focusin` не оставляют stale `true`/`false`.
- [ ] **Reconcile-after-unmount тест (round-4 CRITICAL)**: смонтировать фокусируемый `<textarea>` внутри тест-компонента, `textarea.focus()` → `editing=true`; затем **удалить textarea из DOM** (ре-рендер без него), НЕ вызывая `.blur()` вручную; ожидать `editing=false` (reconcile-эффект после commit). Именно этот кейс focusin/focusout не ловят.
- [ ] Подключить хук в `App` без изменения прочих suppression sources (`anyModalOpen`, `selfOwnedActive`). Теперь все три источника ОТКЛАДЫВАЮТ (не consume) arrivals.
- [ ] Запустить hook tests + `npm run typecheck`.

## Task 4: App-level regression tests реального report-сценария

**Files:**
- Modify: `pkg/web/dashboard/src/app/App.test.tsx`

- [ ] `Question while Feed textarea is focused`: выбрать running stage, открыть Feed, `composer.focus()`, **ввести непустой текст в composer**, перевести другую стадию в `awaiting_user_input` через status mock + significant WS event; assert Feed остаётся, beacon виден, **draft сохранён**. `composer.blur()`; assert Question workspace auto-opens.
- [ ] `Question resolves before blur`: после suppressed arrival — пока composer ещё сфокусирован — вернуть stage в `running` и **дождаться, что beacon исчез / running-снимок применён**; только затем `composer.blur()`; assert позднего auto-open нет и нет transient-навигации (иначе release успеет открыть Question, следующий status закроет — и финальный assert ложно пройдёт).
- [ ] `Overlay defers, close releases` (Finding 7, покрывает `anyModalOpen`-проводку): открыть файловый (или другой) overlay, получить question на другой стадии; assert beacon/shortcut виден, Feed под overlay сохранён; закрыть overlay; assert актуальный question auto-opens. Reducer-тест с абстрактным `suppressed` этого App→hook проводку не доказывает.
- [ ] `Pause-on-focus op does not consume another stage's question`: self-owned pause на A с непустым draft; **`composer.blur()` и дождаться сохранённого ownership**, чтобы единственным источником suppression был `selfOwnedActive`, а не остаточный фокус textarea; получить question на B; assert draft/Feed сохранены и beacon виден. Завершить op через Send/Resume + отдать authoritative status без owned pause; assert B auto-opens.
- [ ] `Pause checkbox is not editing`: `checkbox.focus()` и **assert `document.activeElement === checkbox`** (клик сам по себе фокус в jsdom не гарантирует); получить question на другой стадии; assert auto-open сразу.
- [ ] `Manual beacon open during suppression`: открыть Question beacon вручную, вернуться в Feed, снять suppression; assert episode повторно не auto-opens.
- [ ] `Same-stage question after typing surfaces without manual blur` (round-4 CRITICAL, реальный репорт-кейс). Одна стадия B: `running`, выбрана, Feed открыт; `composerB.focus()` и ввести текст (`editing=true`). B переходит `running → awaiting_user_input` (status mock + significant WS) — composer B размонтируется (`noteTarget=null`), `.blur()` вручную НЕ вызывать. Assert: `editing` схлопывается в `false` через reconcile-эффект, и вопрос B **auto-opens** (не остаётся deferred навсегда). Без reconcile-фикса тест виснет на Feed.
- [ ] `New episode through the real pipeline` (round-3/4 IMPORTANT — корректный порядок). **Suppression на ОТДЕЛЬНОЙ running-стадии A** (composer самой B размонтируется при уходе из running). Порядок строго: (1) B входит `awaiting_user_input` `t1` БЕЗ фокуса где-либо → авто-открытие attention на B/t1 (t1 handled); (2) выбрать A, открыть Feed, `composerA.focus()` (suppressed); (3) вернуть B в `running` затем снова `awaiting_user_input` с `t2` — на снимке с `t2` A-composer сфокусирован → assert Feed/A остаётся, beacon B виден (t2 — новый arrival после handled t1, отложен suppression, а не «первое arrival после исчезновения»); (4) `composerA.blur()` → assert B auto-opens по `t2`. Доказывает через реальный App→hook пайплайн, что `stageView.updated_at`-параметризация работает и identity semantics живут в проде.
- [ ] Не ослаблять существующий тест `pending action on a NON-selected stage surfaces itself` и тест сохранения draft при self-owned paused stage.

Хелпер `stageView` (App.test.tsx) сейчас жёстко ставит `updated_at: ''` — **параметризовать `updated_at`**, чтобы моделировать разные episodes (`t1`, `t2`). Без этого тест нового episode под suppression на App-уровне невозможен и production identity semantics не проверяются.

Все фокус-манипуляции — через реальные `.focus()`/`.blur()` на нужном элементе (не `fireEvent.focus`, см. «Как тесты обязаны драйвить фокус»): в текстовых кейсах фокусируется composer, в checkbox-кейсе — сам checkbox. Где suppression даёт `Pause on focus`-операция или overlay, а не текстовый фокус, источник — `selfOwnedActive`/`anyModalOpen`; там убедиться, что остаточный фокус textarea НЕ подмешивает `useIsEditing` (сделать blur).

## Task 5: Документация, полная проверка и bundle

**Files:**
- Modify: `docs/dashboard.md`
- Modify: `CHANGELOG.md`
- Generated by build: `pkg/web/dashboard/assets/*`, `pkg/web/dashboard/index.html` при изменении hash

- [ ] В dashboard docs описать: attention не крадёт фокус во время ввода/overlay/owned pause, beacon появляется сразу, а всё ещё актуальное действие открывается после release. Checkbox toggle сам по себе editing не считается.
- [ ] Добавить английскую запись `### Fix:` в верхнюю датированную секцию `## 2026-09-30`: suppressed attention is deferred instead of permanently consumed; non-text inputs no longer suppress auto-open.
- [ ] Выполнить:

```bash
cd pkg/web/dashboard
npm run typecheck
npx vitest run
npm run build
```

- [ ] Повторить `npm run typecheck && npx vitest run` после build, чтобы sync/build scripts не изменили исходники неожиданно.
- [ ] Проверить `git diff --check` и убедиться, что diff содержит только планируемые frontend/docs/generated изменения.

## Ручная smoke-проверка

1. Открыть Feed running-стадии и поставить курсор в пустой composer.
2. Дождаться вопроса другой стадии: Feed не прыгает во время фокуса, `Question` beacon виден.
3. Кликнуть вне composer: всё ещё открытый вопрос автоматически становится активным workspace.
4. Повторить с включённым `Pause on focus` и непустым draft: вопрос не уничтожает draft; после Send/Resume открывается.
5. Кликнуть только по checkbox `Pause on focus`, не входя в textarea; следующий вопрос открывается сразу.
6. Пока suppression активен, вручную открыть beacon и вернуться в Feed; после release тот же question не открывается второй раз.
7. Ответить на вопрос до release (во второй вкладке/API): после blur не должно быть ghost-navigation.

## Non-goals

- Не менять момент публикации `dialog_question` относительно `EvAskUser`.
- Не вводить клиентскую оптимистическую смену stage status из WS payload — `/api/status` остаётся авторитетным.
- Не менять порядок attention navigation или правила возврата в `feed`/`cost`/`full-feed`.
- Не сохранять drafts между reload; задача только предотвращает их размонтирование до release suppression.
- Не переделывать ownership/state machine `usePauseOnFocus`, кроме интеграционных тестов её suppression-сигнала.

## Acceptance criteria

- Ни один актуальный attention episode не становится permanently consumed только потому, что его первый status snapshot пришёл при suppression.
- Воспроизведение репорта (`Question` появился, Feed остался) заканчивается auto-open после release suppression.
- Фокус на checkbox `Pause on focus` не блокирует auto-open.
- Ручной dismiss уже просмотренного episode и повторные episodes сохраняют прежнюю семантику.
- Full dashboard test suite, typecheck и production build зелёные.
