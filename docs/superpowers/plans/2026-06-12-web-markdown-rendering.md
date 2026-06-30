# Корректный рендер markdown в веб-интерфейсе — план имплементации

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Веб-интерфейс flowManager рендерит полноценный markdown (таблицы, ссылки, курсив, вложенные списки, цитаты) в просмотре плана и в диалоге с агентом.

**Architecture:** Вендорим один файл `markdown-it.min.js` в `pkg/web/` (попадает в бинарь через go:embed, билд-процесса нет). Самописный блочный парсер `renderMarkdown()` в `app.js` заменяется на рендер библиотекой с постобработкой (спецсекции, чекбоксы, ссылки). Режим ревью остаётся построчным, но инлайн-форматирование переводится на `md.renderInline()`. Спека: `docs/superpowers/specs/2026-06-12-web-markdown-rendering-design.md`.

**Tech Stack:** Go (go:embed, без новых Go-зависимостей), vanilla JS, markdown-it 14.1.0 (вендоренный, инициализация `html: false, linkify: true`).

**Важно для исполнителя:**
- Весь фронтенд — три файла: `pkg/web/app.js` (~1100 строк, IIFE, vanilla JS), `pkg/web/index.html`, `pkg/web/style.css`. JS-тестов в проекте нет — JS проверяется руками, Go — тестами.
- Номера строк в плане даны по состоянию до правок; после первых правок ищи по содержимому.
- Коммиты — на русском, без Co-Authored-By.
- Линт: `make lint`. Сборка: `go build ./...`.

---

### Task 1: Вендорить markdown-it и раздавать его сервером

**Files:**
- Create: `pkg/web/markdown-it.min.js`
- Modify: `pkg/web/embed.go`
- Modify: `pkg/web/index.html` (строка 131, перед `<script src="app.js">`)
- Test: `pkg/server/server_test.go`

- [x] **Step 1: Написать падающий Go-тест на раздачу файла**

В `pkg/server/server_test.go` добавить (рядом с `TestServerRouteStages`, используя тот же `setupTestServer`):

```go
func TestServerServesMarkdownIt(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/markdown-it.min.js", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /markdown-it.min.js: ожидался 200, получен %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "markdownit") {
		t.Error("в теле ответа нет глобала markdownit")
	}
}
```

Если `strings` ещё не импортирован в файле — добавить в import.

- [x] **Step 2: Убедиться, что тест падает**

Run: `go test ./pkg/server/ -run TestServerServesMarkdownIt -v`
Expected: FAIL — статика раздаётся через `http.FileServer(http.FS(web.FS))` (pkg/server/server.go:66), файла в embed.FS нет, придёт 404.
(Если упадёт компиляция из-за отсутствия файла в go:embed после Step 4 — это нормальный порядок: сначала скачиваем файл.)

- [x] **Step 3: Скачать markdown-it 14.1.0**

```bash
curl -fsSL -o pkg/web/markdown-it.min.js https://cdn.jsdelivr.net/npm/markdown-it@14.1.0/dist/markdown-it.min.js
head -c 200 pkg/web/markdown-it.min.js
```

Expected: файл ~100 КБ, в начале — баннер `markdown-it 14.1.0 https://github.com/markdown-it/markdown-it`. Глобал — `window.markdownit`.

- [x] **Step 4: Добавить файл в go:embed**

`pkg/web/embed.go`, строка 5:

```go
//go:embed index.html style.css app.js markdown-it.min.js favicon.svg
var FS embed.FS
```

- [x] **Step 5: Подключить скрипт в index.html**

`pkg/web/index.html`, строка 131 — добавить ПЕРЕД `app.js`:

```html
    <script src="markdown-it.min.js"></script>
    <script src="app.js"></script>
```

- [x] **Step 6: Прогнать тест и сборку**

Run: `go test ./pkg/server/ -run TestServerServesMarkdownIt -v && go build ./...`
Expected: PASS, сборка чистая.

- [x] **Step 7: Commit**

```bash
git add pkg/web/markdown-it.min.js pkg/web/embed.go pkg/web/index.html pkg/server/server_test.go
git commit -m "feat: вендоренный markdown-it 14.1.0 в веб-интерфейсе"
```

---

### Task 2: Рендер плана через markdown-it

**Files:**
- Modify: `pkg/web/app.js` — заголовочный комментарий (строки 1–2), блок «Markdown helpers» (строка 114), функция `renderMarkdown` (строки 673–740), вызов в `loadPlan` (строка 668)

- [x] **Step 1: Инициализировать markdown-it и добавить helpers**

В `app.js` обновить комментарий в шапке (строки 1–2):

```js
// flowManager Dashboard — client-side logic.
// No frameworks, no npm. Единственная зависимость — вендоренный markdown-it.min.js.
```

После функции `escapeHTML` (строка 129) добавить:

```js
    var md = window.markdownit({ html: false, linkify: true });

    // Рендерит markdown в элемент: спецсекции, чекбоксы, ссылки в новой вкладке.
    function renderMarkdownInto(el, text) {
        el.classList.add("md");
        el.innerHTML = renderMarkdownHTML(text || "");
        decorateCheckboxes(el);
        decorateLinks(el);
    }

    // Режет текст по заголовкам спецсекций (## Assumptions / ## Acceptance Criteria),
    // каждый кусок рендерит markdown-it'ом, секции оборачивает в сворачиваемые блоки.
    function renderMarkdownHTML(text) {
        var lines = text.split("\n");
        var html = [];
        var buf = [];
        var inSection = false;
        var inCode = false;

        function flush() {
            if (buf.length) {
                html.push(md.render(buf.join("\n")));
                buf = [];
            }
        }

        for (var i = 0; i < lines.length; i++) {
            var line = lines[i];
            if (line.trim().indexOf("```") === 0) {
                inCode = !inCode;
                buf.push(line);
                continue;
            }
            if (inCode) {
                buf.push(line);
                continue;
            }
            var section = detectSection(line);
            if (section) {
                flush();
                if (inSection) html.push("</div></div>");
                html.push('<div class="plan-section-wrapper ' + section.css + '">' +
                    '<h2 class="section-header">' + section.icon + " " + section.label + "</h2>" +
                    '<div class="plan-section-body">');
                inSection = true;
                continue;
            }
            if (inSection && isH2(line)) {
                flush();
                html.push("</div></div>");
                inSection = false;
            }
            buf.push(line);
        }
        flush();
        if (inSection) html.push("</div></div>");
        return html.join("");
    }

    // Заменяет [x] / [ ] в начале пунктов списка на стилизованные чекбоксы.
    function decorateCheckboxes(root) {
        root.querySelectorAll("li").forEach(function (li) {
            var html = li.innerHTML;
            if (/^\s*\[x\]/.test(html)) {
                li.innerHTML = html.replace("[x]", '<span class="cb cb-done">&#10003;</span>');
            } else if (/^\s*\[ \]/.test(html)) {
                li.innerHTML = html.replace("[ ]", '<span class="cb cb-open">&#9744;</span>');
            }
        });
    }

    // Внешние ссылки открываются в новой вкладке, чтобы не терять состояние дашборда.
    function decorateLinks(root) {
        root.querySelectorAll("a").forEach(function (a) {
            a.target = "_blank";
            a.rel = "noopener";
        });
    }
```

Примечание: `detectSection` и `isH2` уже существуют (app.js:58, 66) — определены до этого места? Нет: они на строках 58–69, то есть ВЫШЕ — ок, hoisting в любом случае покрывает (function declarations внутри IIFE).

- [x] **Step 2: Переключить loadPlan на новый рендер**

В `loadPlan()` (строка 665–669) заменить:

```js
            } else {
                // Regular markdown render for other statuses
                $planContent.classList.remove("hidden");
                $planContent.innerHTML = renderMarkdown(text);
            }
```

на:

```js
            } else {
                // Regular markdown render for other statuses
                $planContent.classList.remove("hidden");
                renderMarkdownInto($planContent, text);
            }
```

- [x] **Step 3: Удалить старый renderMarkdown**

Удалить функцию `renderMarkdown(text)` целиком (строки 673–740, от `function renderMarkdown(text) {` до закрывающей `}` перед `function loadLog()`). Убедиться grep'ом, что вызовов не осталось:

Run: `grep -n "renderMarkdown(" pkg/web/app.js`
Expected: только `renderMarkdownInto` и `renderMarkdownHTML`, голого `renderMarkdown(` нет.

- [x] **Step 4: Проверить сборку и Go-тесты**

Run: `go build ./... && go test ./pkg/server/`
Expected: PASS (JS вкомпилирован, синтаксис JS Go не проверяет — ручная проверка в Task 6).

- [x] **Step 5: Быстрая ручная JS-проверка синтаксиса**

Run: `node --check pkg/web/app.js`
Expected: без вывода (синтаксис валиден).

- [x] **Step 6: Commit**

```bash
git add pkg/web/app.js
git commit -m "feat: рендер плана через markdown-it вместо самописного парсера"
```

---

### Task 3: Markdown в диалоге с агентом

**Files:**
- Modify: `pkg/web/app.js` — рендер истории диалога (строки 435–448) и pending-вопроса (строка 483)

- [x] **Step 1: Рендерить agent_text как markdown**

В цикле `dialogEntries.forEach` (строка 435) заменить:

```js
            if (e.type === "agent_text") {
                var msg = document.createElement("div");
                msg.className = "agent-msg";
                msg.textContent = e.text || "";
                $dialogHistory.appendChild(msg);
                return;
            }
```

на:

```js
            if (e.type === "agent_text") {
                var msg = document.createElement("div");
                msg.className = "agent-msg";
                renderMarkdownInto(msg, e.text);
                $dialogHistory.appendChild(msg);
                return;
            }
```

- [x] **Step 2: Рендерить вопрос в истории как markdown, ответ оставить текстом**

Там же (строки 442–448) заменить:

```js
            if (e.answer !== undefined && e.answer !== null) {
                var qa = document.createElement("div");
                qa.className = "qa";
                qa.innerHTML = "<div class='q'>" + escapeHTML(e.question) + "</div>" +
                    "<div class='a'>→ " + escapeHTML(e.answer) + "</div>";
                $dialogHistory.appendChild(qa);
            }
```

на:

```js
            if (e.answer !== undefined && e.answer !== null) {
                var qa = document.createElement("div");
                qa.className = "qa";
                var qDiv = document.createElement("div");
                qDiv.className = "q";
                renderMarkdownInto(qDiv, e.question);
                var aDiv = document.createElement("div");
                aDiv.className = "a";
                aDiv.textContent = "→ " + (e.answer || "");
                qa.appendChild(qDiv);
                qa.appendChild(aDiv);
                $dialogHistory.appendChild(qa);
            }
```

- [x] **Step 3: Рендерить pending-вопрос как markdown**

В `renderPendingQuestion` (строка 483) заменить:

```js
        $dialogPending.querySelector(".dialog-question").textContent = q.question;
```

на:

```js
        renderMarkdownInto($dialogPending.querySelector(".dialog-question"), q.question);
```

Кнопки-опции (`btn.textContent = opt`, строка 493) и поле ввода НЕ трогать — остаются plain text.

- [x] **Step 4: Проверка синтаксиса и сборка**

Run: `node --check pkg/web/app.js && go build ./...`
Expected: чисто.

- [x] **Step 5: Commit**

```bash
git add pkg/web/app.js
git commit -m "feat: markdown в сообщениях и вопросах агента в диалоге"
```

---

### Task 4: Инлайн-markdown в режиме ревью

**Files:**
- Modify: `pkg/web/app.js` — `inlineFormat` (строки 115–125), конец `renderPlanReview` (строка ~228)

- [x] **Step 1: Перевести inlineFormat на md.renderInline**

Заменить (строки 115–125):

```js
    function inlineFormat(text) {
        text = escapeHTML(text);
        // checkboxes: [x] and [ ]
        text = text.replace(/\[x\]/g, '<span class="cb cb-done">&#10003;</span>');
        text = text.replace(/\[ \]/g, '<span class="cb cb-open">&#9744;</span>');
        // bold: **text**
        text = text.replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>");
        // inline code: `text`
        text = text.replace(/`([^`]+)`/g, "<code>$1</code>");
        return text;
    }
```

на:

```js
    // Инлайн-рендер одной строки для построчного режима ревью:
    // bold, курсив, ссылки, code — через markdown-it; чекбоксы — свои спаны.
    function inlineFormat(text) {
        var html = md.renderInline(text);
        html = html.replace(/\[x\]/g, '<span class="cb cb-done">&#10003;</span>');
        html = html.replace(/\[ \]/g, '<span class="cb cb-open">&#9744;</span>');
        return html;
    }
```

Внимание: `inlineFormat` теперь использует `md`, объявленный в Task 2 — порядок объявлений в IIFE не важен (вызовы происходят после загрузки), но `var md = ...` должен стоять до первого ВЫЗОВА. Он инициализируется при загрузке скрипта, вызовы — по событиям. Конфликтов нет.

- [x] **Step 2: Прогнать ссылки через decorateLinks в режиме ревью**

В конце `renderPlanReview` (строка 228, после `$planContent.appendChild(fragment);`) добавить:

```js
        $planContent.appendChild(fragment);
        decorateLinks($planContent);
        updateReviseButton();
```

(`escapeHTML` остаётся — она по-прежнему используется в код-блоках ревью, логах и отображении комментариев.)

- [x] **Step 3: Проверка синтаксиса и сборка**

Run: `node --check pkg/web/app.js && go build ./...`
Expected: чисто.

- [x] **Step 4: Commit**

```bash
git add pkg/web/app.js
git commit -m "feat: инлайн-markdown в построчном режиме ревью плана"
```

---

### Task 5: CSS для новых элементов markdown

**Files:**
- Modify: `pkg/web/style.css` — добавить блок в конец файла (~строка 1293)

- [x] **Step 1: Добавить стили .md**

В конец `style.css` добавить (палитра — существующие переменные из `:root`, style.css:23–56):

```css
/* === Markdown (общие стили для plan-content, agent-msg, вопросов) === */
.md p { margin: 6px 0; }
.md > :first-child { margin-top: 0; }
.md > :last-child { margin-bottom: 0; }

.md table {
  border-collapse: collapse;
  margin: 8px 0;
  font-size: 12px;
}
.md th, .md td {
  border: 1px solid var(--mint-soft);
  padding: 4px 10px;
  text-align: left;
}
.md th {
  color: var(--ink-hi);
  background: var(--bg-elev);
  text-transform: uppercase;
  font-size: 10px;
  letter-spacing: 0.08em;
}
.md tr:nth-child(even) td { background: var(--grid); }

.md blockquote {
  margin: 8px 0;
  padding: 2px 12px;
  border-left: 2px solid var(--mint);
  color: var(--ink-dim);
}

.md em { color: var(--ink-hi); font-style: italic; }

.md a { color: var(--mint); text-decoration: underline; }
.md a:hover { color: var(--ink-hi); }

.md ul, .md ol { margin: 4px 0; padding-left: 22px; }
.md ul ul, .md ol ol, .md ul ol, .md ol ul { margin: 2px 0; }

.md hr {
  border: none;
  border-top: 1px solid var(--mint-soft);
  margin: 12px 0;
}
```

Примечание: `#plan-content` уже имеет стили для `h1/h2/h3/code/pre/strong/li` (style.css:678+) — они продолжают действовать; `.md` только дополняет недостающее. Класс `.md` навешивается в `renderMarkdownInto` (Task 2).

- [x] **Step 2: Сборка**

Run: `go build ./...`
Expected: чисто.

- [x] **Step 3: Commit**

```bash
git add pkg/web/style.css
git commit -m "feat: стили markdown-элементов в тёмной палитре дашборда"
```

---

### Task 6: Финальная проверка

**Files:** ничего не создаётся; проверка по чек-листу из спеки.

- [x] **Step 1: Полная сборка, тесты, линт**

Run: `go build ./... && go test ./... && make lint`
Expected: всё зелёное, линт без замечаний.

- [x] **Step 2: Собрать бинарь в рабочий путь**

Бинарь живёт в `~/homebrew/bin/` (НЕ `$GOPATH/bin`, `go install` его не обновляет):

```bash
go build -o ~/homebrew/bin/flowmanager ./cmd/flowmanager
ls -la ~/homebrew/bin/flowmanager
```

Expected: дата файла — текущая.

- [x] **Step 3: Ручная проверка UI**

Запустить flowmanager на тестовом флоу (например `example-flow-interactive.yaml`), открыть дашборд в браузере и проверить:

1. **План** (стейдж не в `awaiting_approval`): таблицы рендерятся рамками, вложенные и нумерованные списки — с отступами, ссылки кликабельны и открываются в новой вкладке, `*курсив*` и `> цитаты` оформлены. Секции Assumptions / Acceptance Criteria — в своих сворачиваемых блоках, чекбоксы — спанами.
2. **Диалог**: длинное сообщение агента (`agent_text`) с заголовками/списками/таблицей читаемо; вопрос с markdown в pending-панели оформлен; ответы и кнопки — обычный текст.
3. **Режим ревью** (`awaiting_approval`): строки нумеруются, клик по строке открывает форму комментария, комментарий добавляется; в строках работают курсив/ссылки/код.
4. **Безопасность**: в плане с текстом `<script>alert(1)</script>` и `<b>raw</b>` HTML показывается как текст, не исполняется.

Expected: все 4 пункта проходят. Если что-то не так — чинить до зелёного состояния, НЕ коммитить сломанное.

- [x] **Step 4: Финальный коммит (если были правки на шаге 3)**

```bash
git add -p
git commit -m "fix: правки по результатам ручной проверки рендера markdown"
```
