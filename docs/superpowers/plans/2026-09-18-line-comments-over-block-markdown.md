# План: построчные комментарии поверх блочного markdown-рендера

> **Статус: РЕАЛИЗОВАНО (2026-09-19).** Исторический дизайн-документ; источник истины —
> код (`markdown.ts` `renderAnchoredSections`, `use-line-comments.ts`,
> `LineCommentedDocument.tsx`, миграции `PlanPanel`/`DialogChannel`). Реализация прошла
> дополнительный раунд codex-ревью после написания плана; шипнутые пост-ревью правки
> (доступность Approve при незагруженном плане; сохранение `markdown-body`-типографики
> на комментируемом плане; сброс нативной отрисовки у `<button>`-заголовка секции)
> дополняют текст ниже. Итог и границы — CHANGELOG (2026-09-19) и `docs/dashboard.md`.

Статус (исходный, на момент проектирования): спроектировано, не реализовано. Дизайн
прошёл 7 раундов критического ревью через codex (критов нет с раунда 3; вердикт
финального раунда — «готов к записи в план»). Все замечания закрыты; раздел «Как
закрыты замечания ревью» внизу.

## Проблема

Сейчас после markdown-рендера ответа агента комментарий можно поставить только на **markdown-
БЛОК верхнего уровня**. `blockSpans` (`pkg/web/dashboard/src/components/plan-panel/markdown.ts`)
режет текст на верхнеуровневые блоки (`md.parse`, токены level 0 c `map`), каждый рендерится
целиком `renderBlock` и якорится на ПЕРВУЮ исходную строку. `PlanPanel.parseReviewPlan` рисует
блок одним рядом `plan-line` (`data-line=block.line`, `.line-num`, форма комментария вложена в
ряд), `DialogChannel.renderQuestionLine` — то же. `buildFeedback` шлёт агенту `Line N: <text>`.
Итог: список из 5 пунктов, таблица или многострочный параграф — это ОДИН блок → один
комментарий на всю конструкцию, привязанный к её первой строке.

Раньше был построчный рендер (`formatLine`), но он ломал CommonMark: списки, нумерация `<ol>`,
таблицы, цитаты разваливались построчно. Блочный рендер это починил, но потерял построчную
адресацию. Нужно вернуть адресацию по строке, **не потеряв корректный markdown-рендер**.

## Утверждённые решения (с пользователем)

- **Гранулярность — по ПОД-ЭЛЕМЕНТУ** (каждый `<li>`, строка таблицы `<tr>`, заголовок,
  параграф, code-fence), блок рендерится ЦЕЛИКОМ, якорь = исходная строка под-элемента.
- **UI — ховер-подсветка** под-элемента, клик → форма комментария.
- **Форма для контейнеров (список/таблица, а также абзац внутри многоабзацного blockquote) —
  ПОД БЛОКОМ + компенсации:** автоскролл к форме, ПОСТОЯННАЯ подсветка выбранной цели, метка
  «Line N» в шапке формы. Для прозы верхнего уровня (одиночный параграф/заголовок) форма и так
  прямо под элементом.
- **Без постоянного гаттера номеров;** «Line N» — нативный `title` на ховере якоря + в шапке
  формы/индикатора.
- **Контракт с агентом НЕ меняется:** `comments: Record<line, text>`, `buildFeedback` → `Line N`.

## Архитектурное ядро

**Рендерить markdown целиком, аннотировать токены `data-line`; форма/индикатор — обычные
React-узлы (НЕ порталы в императивные хосты).** Внутрь `innerHTML` попадают только БЕЗОПАСНЫЕ
атрибуты/классы, которыми React не владеет: `data-line`, `has-comment`, `is-active`, `is-hover`,
roving `tabindex`, `title`, `aria-keyshortcuts`/`aria-description`. Форма и «сохранённый
комментарий» рендерятся как обычные keyed React-узлы, чередующиеся с блоками ВНУТРИ delegated-
контейнера. Это ключевой выбор: он снимает целый класс проблем (bootstrap портала, ownership
portal-host, teardown на poll, невалидный HTML `div` в `p/pre/tr`, comment-row/colspan/zebra),
и делает live/replay-неважным (это чистый фронтенд поверх уже приходящего текста).

## 1. markdown.ts — один парс, рендер срезов токенов, отдельный anchored-инстанс

1. **Отдельный anchored-инстанс** markdown-it — НЕ мутируем общий `md` (он обслуживает
   `renderPlainMarkdown`, ленту, обычный рендер плана; глобальная правка правил протекла бы к
   ним). Копирует конфиг: `html:false`, `linkify:true`, `link_open`→`target=_blank`/`rel`.
2. `tokens = anchoredMd.parse(fullText, env)` — ОДИН раз → `token.map` ГЛОБАЛЬНЫЕ (по всему
   документу). Никакого re-parse срезов текста (иначе номера строк после первого блока неверны).
3. **Аннотация якорей + разрешение коллизий:**
   - Predicate кандидата ЯВНЫЙ: whitelist типов + `nesting===1` для open-токенов
     (`heading_open`, `paragraph_open`, `list_item_open`, `blockquote_open`, `tr_open` вкл.
     header); отдельно `hr` (`nesting===0`), `fence`, `code_block`. `hidden`-токены (tight-list
     `paragraph_open`) и closing-токены (`nesting===-1`) исключаем.
   - Группируем кандидатов по стартовой строке `map[0]`. Если на одной строке несколько (`- # H`
     level li=1/heading=2; `1. # H`; `- > quote`; li+fence на одной строке) → выбираем самый
     ВНЕШНИЙ (минимальный `level`); ничьи (штатный парсер их почти не создаёт) — по порядку
     токенов. Вложенные кандидаты на ДРУГИХ строках (loose-list 2-й абзац, вложенный `li`, абзац
     `blockquote`) — сохраняются как самостоятельные якоря.
   - Выбранному ставим `data-line = map[0]+1` (1-based), `title="Comment on line N"`, roving
     `tabindex`, `aria-keyshortcuts="Enter"`, `aria-description`. **Роли НЕ переопределяем**
     (`role="button"` на `li`/`tr`/heading уничтожил бы семантику списка/таблицы) — `li`
     остаётся listitem, `tr` — row.
   - **Контракт:** НЕ биекция со всеми строками. Гарантия — «каждый якорный элемент имеет
     УНИКАЛЬНУЮ стартовую строку»; строки без элемента (setext-подчёркивание, table-разделитель,
     continuation многострочного параграфа) якоря не имеют. Идентичность комментария = стартовая
     строка якоря. Вложенные якоря на РАЗНЫХ строках существуют штатно.
4. **fence/code_block wrapper-rule:** номер кладём в `token.meta.dataLine`, НЕ в attrs (иначе
   стандартный fence кладёт атрибут на внутренний `<code>`, клик по padding `<pre>` промахнётся,
   и/или двойной `data-line`). Правила `fence`/`code_block` переопределяем сигнатурой
   `(tokens, idx, options, env, self)`: вызвать СОХРАНЁННОЕ дефолтное правило, обернуть его
   безопасную строку в `<div class="md-fence-anchor" data-line="N" title=… tabindex=…>…</div>`.
   Не конкатенируем `token.info/content` (XSS).
5. **Сегментация — сбалансированные срезы ЦЕЛЫХ top-level блоков:** режем по диапазонам полных
   верхнеуровневых блоков (от open-токена до matching close на level 0), блок не рвём (иначе
   удаление одного `heading_open` оставит `inline + heading_close`). Рендер среза
   `anchoredMd.renderer.render(slice, anchoredMd.options, env)` — тот же env/options; `map`
   остаётся глобальным. Reference-definitions и linkify уже разрешены полным parse до рендера
   (регресс-тест: reference-def ВНЕ блока + ссылка ВНУТРИ).
6. **Спец-секции — по СЫРОЙ строке, режим Plan/Dialog:**
   - Распознавание — существующим `isSpecialSection(lines[token.map[0]])` (точный case-sensitive
     raw-текст `## Assumptions`/`## Acceptance Criteria`), НЕ по нормализованному inline-тексту
     (иначе совпали бы `## Assumptions #`, setext-вариант, `## **Assumptions**`).
   - Условие ОТКРЫТИЯ секции = raw спец-заголовок; условие ЗАКРЫТИЯ = следующий top-level H2
     (`isHeading2` на raw-строке) — ровно как сейчас (закрывает и обычный `## Other`).
   - Удаление спец-заголовка берёт ВЕСЬ его heading-блок (open+inline+close).
   - **Режим** `renderAnchoredSections(text, {specialSections})`: `PlanPanel` → `true` (секции
     сворачиваются, заголовок вырезан — как сейчас); `DialogChannel` → `false` (плоские блоки,
     спец-секции не трогаются — сохраняет текущее поведение Dialog). Поведение Dialog не меняем.
7. **Чекбоксы token-aware, с сохранением экранирования:** кастомное правило `text` — вызвать
   СОХРАНЁННЫЙ дефолтный text-рендерер (он экранирует HTML), затем заменить литеральные
   `[x]`/`[ ]` в его БЕЗОПАСНОМ результате; только для `text`-токенов (не `code_inline`/`fence`/
   `code_block`). Прежний строковый `decorateCheckboxes` бил `[x]` даже внутри `<code>`/fence —
   прямой markdown-регресс, т.к. fence теперь самостоятельная цель. **Документированное
   ограничение:** `\[x]` не отличается от `[x]` (оба становятся одним text-токеном после
   inline-парсинга) — как и у прежнего декоратора.
8. **Кэш:** без внутреннего безлимитного `Map` (иначе держал бы все планы/вопросы вечно).
   Мемоизация — на стороне потребителя (`useMemo` по точному тексту + opts).
9. **API:** `renderAnchoredSections(text, opts): Section[]`,
   `Section = {kind, special?, blocks: Block[]}`,
   `Block = {startLine, endLineExclusive /* = token.map[1]+1 */, html}` (строгая exclusive-
   граница для проверки `startLine ≤ line < endLineExclusive`).

## 2. Владелец состояния и атомарный reset

- **line-comment state живёт в пределах ОДНОЙ версии документа.** Идентичность: Plan =
  `(stage.id, ТОЧНЫЙ финальный текст плана)` (ревизия меняет текст → сброс); Dialog =
  `(stage.id, phase, id, ТОЧНЫЙ текст вопроса)` (сброс и при смене текста того же questionKey, а
  не только `(phase,id)`).
- **Механизм — keyed document-owner компонент** `<LineCommentedDocument key={documentIdentity}
  text=… opts=…>`, ВНУТРИ которого живёт ВЕСЬ связанный state (`comments`, `draft`,
  `activeCommentLine`, свёрнутые секции, `rovingLine`). Смена `documentIdentity` → React
  размонтирует старый и монтирует новый → состояние обнуляется АТОМАРНО, без промежуточного
  кадра «старый state на новом DOM» (что дал бы reset через effect + autoFocus/scroll старой
  формы). Старый документ другой стадии не рендерим до загрузки нового.
- **Action bar (Send revision / Approve) и `commentCount` — ВНУТРИ границы owner** (или owner
  отдаёт state через context/render-prop, а bar его потребляет). `buildFeedback`/`commentCount`/
  disabled читают state напрямую; зеркала state в родителе НЕТ (иначе нарушится атомарность
  reset и возможен старый snapshot).
- **Orphan-comments исключены:** в пределах одной версии якоря не исчезают (текст неизменен);
  при смене документа сбрасывается всё → осиротевшие строки не копятся, `commentCount` не
  завышается.

## 3. Взаимодействие — общий хук `useLineComments`

- **Привязка к DOM через callback-ref / node-in-state, НЕ стабильный `RefObject`:**
  `.dialog-question`/`#plan-content` монтируются условно (после poll), смена `ref.current` сама
  эффект не перезапускает. Хук получает узел через callback-ref (как `useStickToBottom`) или
  живёт внутри keyed-owner; эффект делегирования зависит от фактического node.
- Делегирование скоупнуто на markdown-контейнер. Перед резолвом якоря ПЕРВЫМ исключаем служебный
  UI: `if (target.closest('[data-comment-ui]')) return` (формы/индикаторы лежат ВНУТРИ
  контейнера — обязательно, не belt-and-suspenders). Затем `el = target.closest('[data-line]')`,
  `container.contains(el)`. Игнор клика по интерактивному потомку (`a`/`button`/`input`/
  `textarea`).
- **Hover — класс `is-hover` на `closest('[data-line]')`, владение у `pointerover`/`pointerout`-
  хендлеров:** вложенные `data-line` на разных строках → берём ближайший. Текущий hover-элемент
  храним в ref; императивный класс-слой (§5) `is-hover` НЕ трогает (иначе при смене roving/state
  под неподвижным курсором подсветка пропала бы до следующего движения мыши). Аффорданс = фон-
  подсветка на `.is-hover`/`.is-active`, БЕЗ `::after`-иконки (pseudo-element ломается на
  `tr`/`hr`).
- **Единый `openComment(line)`** (и клик по телу якоря без drag/без пересекающего выделения, и
  Enter) АТОМАРНО ставит `activeCommentLine=line` И `rovingLine=line` (иначе после save/Esc фокус
  вернётся на якорь с `tabindex=-1`). Порядок внутри: ПЕРВЫМ синхронный `beforeOpen()`-колбэк
  (Dialog передаёт `feed.release`, Plan — noop), затем `setActiveCommentLine`; после коммита —
  фокус в textarea, автоскролл к форме `scrollIntoView({block:'nearest'})`, `is-active`
  подсветка цели. Без `beforeOpen` `MutationObserver` стика при `stick=true` после нашего скролла
  утащил бы канал вниз; stick после закрытия не восстанавливаем.
- **Клавиатура — roving tabindex** без смены ролей: среди ВИДИМЫХ `[data-line]` активен
  `tabindex=0` у одного, прочие `-1`; ↑/↓ двигают; Enter/Space → `openComment`, только если
  `event.target === anchor` (иначе Enter на вложенной ссылке откроет и ссылку, и форму). Скрытые
  (в свёрнутой секции) якоря исключаем из roving; при collapse roving переходит на ближайший
  видимый якорь/на toggle. Esc/save/delete → фокус назад на якорь строки.
- Открытие только при клике без drag и без выделения, пересекающего якорь (следим
  `pointerdown`→`pointerup`; не полагаемся на глобальный `getSelection` без проверки пересечения).

## 4. Рендер форм/индикаторов (JSX-контракт)

- Ref делегирования: PlanPanel — на `#plan-content`; DialogChannel — на `.dialog-question`.
- Внутри контейнера рендерим по `sections→blocks`; на каждый блок keyed Fragment:
  `<div className="md" dangerouslySetInnerHTML=…/>` (было `<span>` → `<div>`: в span
  недопустимо класть `p/ul/table/pre`) + `<LineCommentDisplay data-comment-ui
  data-comment-line=N>` для каждой `line∈comments` этого блока + `<LineCommentForm data-comment-ui
  key=form-${line}>` если `activeCommentLine` в диапазоне блока. Ключи РАЗДЕЛЬНЫЕ
  `display-${line}` / `form-${line}`.
- В PlanPanel фрагменты идут внутрь `.plan-section-body` (для секций) либо прямо в
  `#plan-content` (обычные блоки).
- Формы/индикаторы — обычные React-узлы (не порталы) → caret/StrictMode/poll безопасны.

## 5. Императивный класс-слой — `useLayoutEffect`, scoped, идемпотентный

Зависит от (мемо-`sections` ref, `comments`, `activeCommentLine`, `rovingLine`,
свёрнутые-секции/visibility). Через `container.querySelectorAll('[data-line]')`: снять
`has-comment`/`is-active` и roving `tabindex`, затем навесить актуальные (`has-comment` для
`line∈comments`, `is-active` для активной, `tabindex=0` для roving-строки). **`is-hover` этот
проход НЕ трогает** (владение у pointer-хендлеров). Только атрибуты/классы — никаких вставок/
удалений узлов (нет teardown/detached-host). `useLayoutEffect` идёт ПОСЛЕ коммита → если React
перерисовал `innerHTML` (сменился текст), классы восстановятся до paint; StrictMode безопасен
(идемпотентно). `document.querySelector` не используем.

## 6. Collapse спец-секции

- Header секции — настоящая `<button aria-expanded>` (сейчас кликабельный `h2` без семантики),
  чтобы корректно возвращать фокус.
- Collapse с НЕПУСТЫМ draft в этой секции — ЗАПРЕЩЁН (либо header показывает «unsaved comment» и
  секция раскрывается при попытке действия); фокус после collapse → на header-кнопку.
- Форма/индикатор — React-узлы внутри тела секции; collapse (`display:none`) их скрывает, но
  React-состояние (draft) сохраняется. Скрытые якоря выпадают из roving.

## 7. DOM-контракт и CSS

- **Миграция контракта:** форма больше НЕ потомок `[data-line="N"]` (нельзя валидно вложить
  `div` в `p`/`pre`/`tr`). Новый контракт: форма/индикатор помечены `data-comment-ui` +
  `data-comment-line="N"` + доступным именем. Тесты DialogChannel/PlanPanel, опиравшиеся на
  `[data-line="N"] .line-comment-form` и `.has-comment` на ряду, переписываются: клик по
  `[data-line="N"]`; форма — sibling блока с `data-comment-ui`/`data-comment-line`;
  `has-comment` — класс на самом якоре.
- `.line-content` `<span>` → `<div>`.
- **Типографика:** `.md > :first-child`/`:last-child` (`skins/base/markdown.css`) обнуляют края
  КАЖДОГО отдельного `.md`-блока → без явного отступа блоки слипнутся. Добавить spacing между
  anchored-блоками (margin/gap на обёртке блока); визуальный тест `paragraph → heading → list →
  paragraph`.
- **Dialog CSS:** правила, завязанные на `.plan-line .line-content` (`skins/base/
  dialog-channel.css` — workspace-шрифт, code/pre) перенести на новый `.md`-контейнер, иначе
  шрифт/код-стили пропадут. Гаттер номеров (`.line-num`/marker) убираем осознанно.
- **Состояния таблиц — СТРОГО выше специфичности zebra:** zebra `.md tr:nth-child(even) td` =
  (0,2,2); наивный `.md tr.has-comment > :is(th,td)` тоже (0,2,2) — порядок ненадёжен. Поднимаем
  специфичность ancestor-классом контейнера/owner:
  `.line-commented .md tr.has-comment > :is(th,td)` = (0,3,2) > (0,2,2). Три состояния — в
  порядке `has-comment` → `is-hover` → `is-active` (равная специфичность между собой → позже
  выигрывает), ПОСЛЕ zebra-правил, покрыв header-`th` и чётную data-row. Без `!important`, без
  pseudo-element.
- **`hr` как цель:** `<hr>` почти нулевой высоты — фон-подсветка невидима, hit-area ~1px. Дать
  `hr[data-line]` реальную область попадания и видимое состояние через `padding`/`outline`/
  `border`/`box-shadow`.

## 8. Оба потребителя

PlanPanel (`specialSections:true`) и DialogChannel (`false`) переходят на
`renderAnchoredSections` + `useLineComments` внутри `LineCommentedDocument`. Существующая логика
Dialog (poll pending, `retainedTarget`, `#qa-*` якоря, автопрыжок) не трогается — мемо по тексту
+ отсутствие teardown. Проверить, что контейнер не клиппит форму (`overflow`/фикс-высота).

## Тесты

**markdown.ts (структурная целостность — главная гарантия «не развалить markdown»):**
`<ol start="2">` продолжает нумерацию; tight/loose/nested/mixed списки; `<li>` только в `ul/ol`;
`<tr>` только в `thead/tbody/table`; таблица без header-разделителя (→ параграф в markdown-it) →
якорь-параграф; несколько параграфов в blockquote (каждый — свой якорь на своей строке);
fence + indented code (один якорь на обёртке `<pre>`); ссылки сохраняют `target/rel`; спец-
секция со списком/таблицей; reference-def вне блока + ссылка внутри. ГЛОБАЛЬНЫЕ строки: блок не с
первой строки и внутри спец-секции → верный `data-line`. Коллизии: `- # H`, `1. # H`,
`- > quote`, li+fence на строке → один внешний якорь; loose-list 2-й абзац / вложенный li /
абзац blockquote → свой якорь на своей строке; setext → якорь на первой строке. Чекбокс не
декорируется в code/fence и сохраняет escaping; `\[x]` — документированное поведение. Спец-
секции: `special → ## Other → content`; Dialog-режим не вырезает заголовок. XSS: `html:false`,
опасные URL, fence info не инъектится.

**useLineComments / потребители:** hover ставит `is-hover` только на ближайший якорь (nested
list/loose-list/blockquote); клик по ссылке НЕ открывает форму; выделение/drag НЕ открывает;
клики в форме (`data-comment-ui`) не ловятся. Клик/Enter(на якоре) → форма с верным `Line N` +
автоскролл + `is-active` подсветка; коммент по строке; `buildFeedback`→`Line N`; удаление
снимает `has-comment`. Poll с равным pending НЕ теряет caret и НЕ прыгает; StrictMode без
ошибок; замена `innerHTML` новым текстом восстанавливает классы; смена документа атомарно
сбрасывает всё состояние. Collapse: header-кнопка `aria-expanded`; collapse с непустым draft
запрещён/раскрывает; скрытые якоря вне roving; фокус на header. Клавиатура: roving ↑/↓ по
видимым, Enter только на якоре, Esc/save/delete возвращают фокус. Список: несколько комментариев
на разные строки → несколько форм/индикаторов под блоком, у каждого верный `Line N`; выбранная
цель подсвечена постоянно, форма проскроллена.

## Затронутые файлы

- `pkg/web/dashboard/src/components/plan-panel/markdown.ts` — anchored-инстанс,
  `renderAnchoredSections`, аннотация + коллизии + явный predicate, fence-wrapper через
  `token.meta`, token-aware чекбоксы, режим `specialSections` (+ тест)
- NEW `pkg/web/dashboard/src/components/plan-panel/use-line-comments.ts` — callback-ref к узлу,
  делегирование, hover-владение, roving tabindex, `openComment(active+roving)` + `beforeOpen`,
  императивный класс-слой (+ тест)
- NEW `LineCommentedDocument` компонент — keyed по `documentIdentity`, владелец
  `comments/draft/active/collapsed/roving` + action bar; атомарный reset через remount (+ тест)
- `pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx` — переход на sections+hook внутри
  owner, фрагменты блок+display+form, header-кнопка + collapse-guard, `span→div` (+ тест)
- `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx` — то же,
  `specialSections:false`, identity `(stage,phase,id,question)`, `beforeOpen=feed.release`
  (+ тест миграции)
- CSS (`skins/base` markdown.css/dialog-channel.css/plan-panel.css): `[data-line]` cursor/
  `is-hover`/`is-active`/`has-comment` (вкл. `.line-commented .md tr.*`), spacing между блоками,
  перенос `.line-content`-стилей на `.md`, `hr` hit-area, форма `data-comment-ui`
- убрать прежних потребителей `blockSpans`/`parseLineBlocks`/`renderBlock`/`formatLine` (grep;
  удалить/оставить по факту)

## Компромиссы (явно)

- Форма для любой цели, чей top-level блок — контейнер (список, таблица, абзац внутри
  многоабзацного blockquote), появляется под всем этим блоком, не строго под под-элементом;
  компенсируется автоскроллом + постоянной `is-active`-подсветкой цели + меткой `Line N`
  (утверждено пользователем; распространяется на все вложенные цели). Для прозы верхнего уровня
  форма и так прямо под элементом.
- Нет постоянного гаттера номеров; «Line N» на ховере (`title`) и в форме (утверждено).
- `role` markdown-узлов не меняем → клавиатура через roving tabindex + `aria-keyshortcuts`
  (лёгкая потеря «нативной кнопочности», зато семантика списка/таблицы цела).

## Как закрыты замечания ревью codex (7 раундов)

- **Неверные глобальные номера строк** (re-parse среза текста) → один парс + рендер срезов
  ТОКЕНОВ; `token.map` глобальный.
- **offsetTop-позиционирование формы** (перекрытие, клиппинг, протухание) → форма — normal-flow
  React-узел после блока + компенсации.
- **Слом DOM-контракта** (форма как потомок `[data-line]`, невалидный HTML) → явная миграция на
  `data-comment-ui`/`data-comment-line`, `span→div`.
- **fence attrs на `<code>` / двойной data-line** → `token.meta` + wrapper на внешнем `<div>`.
- **Дубли `data-line` / вложенность** → канонизация «самый внешний на строку» + hover по
  ближайшему `closest`.
- **`role="button"` убивает семантику** → роли не меняем, клавиатура через roving tabindex +
  aria-keyshortcuts, keydown только при `target===anchor`.
- **portal-host bootstrap / ownership / teardown на poll / tr-hr хосты / colspan / zebra** →
  форма/индикатор — обычные React-узлы, в innerHTML только классы/атрибуты.
- **has-comment императивно** → `useLayoutEffect` scoped идемпотентный, только классы;
  `is-hover` — у pointer-хендлеров.
- **Мутация общего `md`** → отдельный anchored-инстанс.
- **decorateCheckboxes в коде** → token-aware, с сохранением escaping.
- **Спец-секции по inline-тексту** → по сырой строке через существующий `isSpecialSection`; режим
  Plan/Dialog.
- **Атомарный reset / orphan-comments** → keyed document-owner (remount), сброс всего state.
- **callback-ref для условно монтируемого узла;** **`beforeOpen=feed.release` vs
  useStickToBottom;** **`openComment` ставит active+roving атомарно;** **табличные состояния
  строго выше zebra;** **`hr` hit-area;** **компенсации на все вложенные цели** — закрыты в
  раундах 4-6.
- Финальный вердикт раунда 7: готов к записи в план (критов нет с раунда 3).
