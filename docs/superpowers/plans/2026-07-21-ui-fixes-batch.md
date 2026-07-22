# Dashboard UI-фиксы (батч из 7) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** 7 пользовательских UI-фиксов дашборда (`pkg/web/dashboard`, React/TS), точечно, в рамках существующей дизайн-системы.

**Architecture:** Три задачи: (1) мелкие независимые (#1 Ctrl+Enter, #3 dark-by-default, #6 перевод строки); (2) панельные (#4 logs maximize+truncation, #5 header description, #7 dialog scroll-on-maximize); (3) killer feature #2 (комментарии к вопросам, переиспользуя plan-comment паттерн). Порядок: 1 → 2 → 3.

**Tech Stack:** React 18 + TypeScript + Vite + vitest. Существующие: `Maximizable`, `PanelFrame`, `useStickToBottom`, `useThemeMode`, `PlanPanel` plan-comment паттерн.

## Global Constraints
- Только `pkg/web/dashboard`. Бэкенд/API не трогать. `accept.yaml` не трогать. go.mod не трогать.
- Коммиты на русском, без Co-Authored-By.
- После КАЖДОЙ задачи: в `pkg/web/dashboard` — `npm run typecheck` и `npm test` зелёные.
- Следовать существующим паттернам компонентов (CODEMANIFEST/.usages обновлять при смене публичного поведения).
- Редактировать ИСХОДНИКИ (`src/`, `index.dev.html`, `public/`), НЕ корневые build-артефакты. Финальная пересборка (`npm run build`) — в конце (отдельный шаг контроллера), т.к. корневые `index.html`/`assets` эмбедятся через go:embed.

---

### Task 1: мелкие фиксы (#1 Ctrl+Enter, #3 dark-default, #6 перевод)

**Files:** `src/components/dialog-channel/DialogChannel.tsx`, `src/components/event-feed/EventFeedPanel.tsx`, `index.dev.html` (+ `public/index.html` если содержит тот же bootstrap), их `*.test.tsx`.

- [ ] **#6 — перевод «к последнему» (2 вхождения).**
  - `DialogChannel.tsx:201`: `↓ к последнему` → `↓ latest`.
  - `EventFeedPanel.tsx:62`: `↓ к последнему` → `↓ latest`.

- [ ] **#1 — Ctrl+Enter в диалоге.** В `DialogChannel.tsx` на `<textarea className="dialog-custom">` добавить:
  ```tsx
  onKeyDown={(e) => { if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') { e.preventDefault(); void sendAnswer() } }}
  ```
  (`sendAnswer` уже определён в компоненте.)

- [ ] **#3 — тёмная тема по умолчанию.** В `index.dev.html` строка ~14 сейчас:
  `: (window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');`
  Заменить на дефолт `dark` при отсутствии stored:
  `: 'dark';`
  (Оставить чтение `localStorage.getItem('afm-mode')` — ручной toggle сохраняется.) Проверить `public/index.html` (build-артефакт HTML-шаблона): если там тот же инлайн-bootstrap — синхронно поправить его исходник; если он генерится из `index.dev.html` через `restore-index.js` — править только источник.

- [ ] **Тесты.** Добавить/дополнить: `DialogChannel.test.tsx` — Ctrl+Enter (и Cmd+Enter) при непустом customText вызывает POST на `/dialog/answer` (мок fetch, как в существующих тестах). Перевод — существующие тесты, ищущие `к последнему`, обновить на `latest` (если такие есть — грепнуть). Тема: если есть тест bootstrap — проверить дефолт `dark` без localStorage; иначе добавить лёгкий тест логики выбора режима.

- [ ] **Verify + commit.** `cd pkg/web/dashboard && npm run typecheck && npm test` → зелёные.
  ```
  git add pkg/web/dashboard/src pkg/web/dashboard/index.dev.html pkg/web/dashboard/public
  git commit -m "feat(dashboard): Ctrl+Enter в диалоге, тёмная тема по умолчанию, перевод jump-to-latest"
  ```

---

### Task 2: панельные фиксы (#4 logs, #5 header, #7 dialog scroll)

**Files:** `src/components/log-panel/LogPanel.tsx`, `src/components/flow-header/FlowHeader.tsx` (+ данные из `App.tsx`/types), `src/components/dialog-channel/DialogChannel.tsx`, `src/components/layout/Maximizable.tsx` (только чтение — понять API), их `*.test.tsx`, CODEMANIFEST/.usages задетых.

Сначала ПРОЧИТАЙ: `LogPanel.tsx`, `Maximizable.tsx`, `FlowHeader.tsx`, `App.tsx` (как прокидываются данные и как `dialog`/`plan` используют `Maximizable`+`PanelFrame maximizeId`), `use-stick-to-bottom.ts`.

- [ ] **#4 — LogPanel maximize + меньше резать.** Обернуть корень `LogPanel` в `<Maximizable id="logs">` + использовать `PanelFrame` с `maximizeId="logs"` — точно как `DialogChannel.tsx:147-148` / `PlanPanel.tsx:146-147` (это даёт кнопку «на весь экран»). Найти текущую обрезку строки лога (truncate/slice/CSS `text-overflow`/`max-width`) и: увеличить лимит; когда панель maximized — не обрезать (полные строки). Если обрезка через CSS — добавить модификатор класса для maximized-состояния; если через JS slice — поднять константу и снять обрезку при maximized (пробросить признак maximized, если `Maximizable` его даёт; иначе — CSS-подход через класс `.maximized`).

- [ ] **#5 — description проекта в хедере.** В `FlowHeader.tsx` отрендерить `description` (из корня flow). Проверить в `App.tsx`/types, приходит ли description (flow-level) в проп хедера; если нет — пробросить из существующих данных флоу (не добавляя новый API-вызов — description уже есть во flow-данных). Отрисовать рядом с текущим заголовком (подзаголовок/строка), не ломая лейаут.

- [ ] **#7 — скролл диалога в конец при раскрытии.** В `DialogChannel.tsx`: при переходе панели `dialog` в maximized-состояние вызвать `feed.jumpToBottom()` (через `requestAnimationFrame`, после layout). Определить сигнал «стал maximized» из `Maximizable` API (проп/контекст/событие — см. чтение `Maximizable.tsx`). Если `Maximizable` не отдаёт наружу состояние — минимально расширить его, чтобы отдать `maximized`-флаг детям (или колбэк onMaximize), сохранив обратную совместимость остальных использований (`plan`, `logs`).

- [ ] **Тесты.** `LogPanel.test.tsx` — есть кнопка maximize (как в тестах dialog/plan); truncation-поведение (лимит/при maximized не режется). `FlowHeader.test.tsx` — хедер содержит переданный description. `DialogChannel.test.tsx` — при maximize вызывается jumpToBottom (мок хука/спай).

- [ ] **Verify + commit.** `npm run typecheck && npm test` зелёные.
  ```
  git add pkg/web/dashboard/src
  git commit -m "feat(dashboard): логи на весь экран + меньше обрезки, description проекта в хедере, скролл диалога в конец при раскрытии"
  ```

---

### Task 3: #2 — комментарии к вопросам (killer feature)

**Files:** `src/components/dialog-channel/DialogChannel.tsx` (+ возможно вынести общий line-comment хелпер), `DialogChannel.test.tsx`, CODEMANIFEST/.usages.

Сначала ПРОЧИТАЙ `PlanPanel.tsx` (`parseReviewPlan`, `renderPlanLine`, `comments`/`activeCommentLine`/`draft` state, `saveComment`/`deleteComment`, `buildFeedback`, CSS-классы `plan-line`/`line-comment-form`/`line-comment-marker`) и `plan-comment.ts` тип — это переиспользуемый паттерн.

- [ ] **Рендер вопроса пронумерованными кликабельными строками.** В pending-блоке `DialogChannel` вместо (или в дополнение к) `MarkdownRenderer source={pending.question}` рендерить строки вопроса как в review-плане: разбить `pending.question` на строки, каждая кликабельна (клик → инлайн-форма комментария). Переиспользовать CSS-классы плана (`plan-line`, `line-comment-*`) — визуальная консистентность.

- [ ] **Состояние комментариев.** Добавить `comments: Record<number,string>`, `activeCommentLine`, `draft` (как в PlanPanel). Клик по строке → форма add/update/delete; сбрасывать `comments` при смене pending-вопроса (в существующем refresh-эффекте, где уже сбрасываются selectedOption/customText).

- [ ] **Переключение UI по наличию комментариев.** `const commentCount = Object.keys(comments).length`.
  - `commentCount === 0` → показывать опции + `<textarea className="dialog-custom">` + кнопка `▸ SEND` (как сейчас).
  - `commentCount > 0` → СКРЫТЬ опции и свободный textarea; показать ОДНУ кнопку `Send feedback` (+ можно счётчик `(N)`).

- [ ] **Отправка feedback как ответа.** Кнопка `Send feedback` → собрать текст (для каждой прокомментированной строки: цитата строки вопроса + `Line N: <comment>`, отсортировано, joined `\n\n` — аналог `buildFeedback`), затем:
  ```ts
  await postJson(`/api/stages/${encodeURIComponent(stage.id)}/dialog/answer`, {
    id: pending.id, phase: pending.phase ?? '', answer: feedbackText, from_options: false,
  })
  await reload()
  ```
  (Тот же эндпоинт/форма, что у текущего `sendAnswer`; агент читает answer как ответ.)

- [ ] **Ctrl+Enter в форме комментария (#1 согласованность).** На textarea комментария — тот же onKeyDown: Ctrl/Cmd+Enter сохраняет комментарий (save), не отправляет весь feedback (чтобы не отправить случайно).

- [ ] **Тесты (`DialogChannel.test.tsx`).**
  - Клик по строке вопроса → появляется форма; add комментария → опции+textarea исчезают, появляется `Send feedback`.
  - `Send feedback` → POST `/dialog/answer` с `from_options:false` и телом, содержащим текст комментария и `Line N:`.
  - Удаление единственного комментария (→ 0) → возвращаются опции+textarea.

- [ ] **Verify + commit.** `npm run typecheck && npm test` зелёные.
  ```
  git add pkg/web/dashboard/src
  git commit -m "feat(dashboard): комментарии к вопросам как к планам — Send feedback отправляет комментарии как ответ агенту"
  ```

---

### Финальный шаг (контроллер, после всех задач + финального review): пересборка + полная верификация
`cd pkg/web/dashboard && npm run typecheck && npm test && npm run build`; затем `go build ./...` (эмбед свежих ассетов) — всё зелёное. Закоммитить пересобранные корневые артефакты отдельным коммитом, если билд их изменил.

## Self-Review
- Все 7 фиксов покрыты (#1/#3/#6 → Task 1; #4/#5/#7 → Task 2; #2 → Task 3). ✓
- Placeholder-scan: #6/#1/#3 даны точным кодом/строками; #4/#5/#7/#2 дают точный подход + конкретные reuse-цели (PlanPanel plan-comment, Maximizable, useStickToBottom) с указанием «прочитай X» — это привязка к реальному коду, т.к. точное CSS/JS-место обрезки и API `Maximizable` варьируются и должны читаться реализатором, не угадываться. ✓
- Consistency: `/dialog/answer` с `from_options:false` — тот же контракт, что существующий `sendAnswer`; `buildFeedback`-формат — из PlanPanel. ✓
- Порядок: мелкие → панельные → #2; каждая задача изолирована и отдельно тестируется/коммитится.
