# Preventive Agent Questions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Planning-агент явно фиксирует assumptions и acceptance criteria в плане; дашборд визуально выделяет эти секции для удобного ревью.

**Architecture:** Изменения в трёх файлах: промпт (`planning.md`), JS-рендер (`app.js`), стили (`style.css`). Промпт требует секции с фиксированными заголовками `## Assumptions` и `## Acceptance Criteria`. JS при рендере плана оборачивает содержимое этих секций в wrapper-div с CSS-классом. CSS рисует цветной бордер и фон.

**Tech Stack:** Vanilla JS, CSS, Markdown prompt

---

### Task 1: Обновить planning-промпт

**Files:**
- Modify: `assets/prompts/planning.md`

- [ ] **Step 1: Добавить секцию Required sections в промпт**

Открыть `assets/prompts/planning.md`. Вставить перед последней строкой (`Output ONLY the plan markdown — no preamble, no explanation, no questions.`):

```markdown
## Required sections

If you made any non-obvious choices or assumptions (technology, approach, scope boundaries), add a section:

## Assumptions
- Each assumption on its own line
- Explain WHY you chose this over alternatives

If the stage involves testing, validation, or verifiable behavior, add a section:

## Acceptance Criteria
- [ ] Each criterion as a checkbox
- [ ] Be specific: endpoint, input, expected output
- [ ] Cover both happy path and error cases

These section names are exact — the dashboard UI highlights them for the reviewer.
```

Итоговый файл должен выглядеть так:

```markdown
# Planning Agent

You are a planning agent. Your task is to create a detailed implementation plan for the stage described below.

Write a plan in markdown format. The plan should contain:
- An overview of what needs to be done
- Numbered tasks with specific checkboxes
- Each task should be concrete and actionable
- Include file paths where relevant

## Rules

- Do NOT ask questions. Make all decisions autonomously based on the stage description and project context.
- Do NOT propose interactive workflows, browser previews, or anything that requires real-time user interaction.
- Do NOT wait for approval or confirmation. Produce the complete plan in one go.
- If you are unsure about a detail, make the best choice and note the assumption in the plan.

## Required sections

If you made any non-obvious choices or assumptions (technology, approach, scope boundaries), add a section:

## Assumptions
- Each assumption on its own line
- Explain WHY you chose this over alternatives

If the stage involves testing, validation, or verifiable behavior, add a section:

## Acceptance Criteria
- [ ] Each criterion as a checkbox
- [ ] Be specific: endpoint, input, expected output
- [ ] Cover both happy path and error cases

These section names are exact — the dashboard UI highlights them for the reviewer.

Output ONLY the plan markdown — no preamble, no explanation, no questions.
```

- [ ] **Step 2: Коммит**

```bash
git add assets/prompts/planning.md
git commit -m "feat: добавить Required sections (Assumptions, Acceptance Criteria) в planning промпт"
```

---

### Task 2: CSS-стили для выделенных секций

**Files:**
- Modify: `pkg/web/style.css`

- [ ] **Step 1: Добавить стили в конец `style.css`**

Вставить в конец файла (после `.comment-hint`):

```css
/* === Plan highlighted sections (Assumptions, Acceptance Criteria) === */
.plan-section-wrapper {
    margin: 8px 0;
    border-radius: 0 4px 4px 0;
    padding: 8px 0 8px 12px;
}

.plan-section-assumptions {
    border-left: 3px solid #f0ad4e;
    background: rgba(240, 173, 78, 0.05);
}

.plan-section-criteria {
    border-left: 3px solid #5cb85c;
    background: rgba(92, 184, 92, 0.05);
}

.plan-section-wrapper .section-header {
    cursor: pointer;
    user-select: none;
    display: flex;
    align-items: center;
    gap: 6px;
    margin-bottom: 6px;
}

.plan-section-wrapper .section-header .toggle {
    font-size: 12px;
    transition: transform 0.2s;
    display: inline-block;
}

.plan-section-wrapper.collapsed .section-header .toggle {
    transform: rotate(-90deg);
}

.plan-section-body {
    /* visible by default */
}

.plan-section-wrapper.collapsed .plan-section-body {
    display: none;
}
```

- [ ] **Step 2: Коммит**

```bash
git add pkg/web/style.css
git commit -m "feat: CSS-стили для выделенных секций Assumptions и Acceptance Criteria"
```

---

### Task 3: JS — выделение секций в `renderPlanReview`

**Files:**
- Modify: `pkg/web/app.js`

- [ ] **Step 1: Добавить helper-функцию `detectSection`**

Вставить внутри IIFE (после объявления `var activeCommentLine = null;`, строка 40), перед `// ---- Status labels`:

```javascript
// ---- Special section detection ----
var SPECIAL_SECTIONS = {
    "## Assumptions": { css: "plan-section-assumptions", icon: "\u26A0", label: "Assumptions" },
    "## Acceptance Criteria": { css: "plan-section-criteria", icon: "\u2713", label: "Acceptance Criteria" }
};

function detectSection(line) {
    var trimmed = line.trim();
    for (var key in SPECIAL_SECTIONS) {
        if (trimmed === key) return SPECIAL_SECTIONS[key];
    }
    return null;
}

function isH2(line) {
    return line.trim().indexOf("## ") === 0;
}
```

- [ ] **Step 2: Обновить `renderPlanReview` — обернуть спецсекции в wrapper-div**

Заменить тело функции `renderPlanReview` (строки 102–159). Новая логика: при встрече спецсекции создаём wrapper-div, складываем туда строки до следующего `##` или конца файла.

```javascript
function renderPlanReview(text) {
    $planContent.innerHTML = "";
    $planEmpty.classList.add("hidden");
    $planContent.classList.remove("hidden");

    if (!text || !text.trim()) {
        $planEmpty.classList.remove("hidden");
        $planContent.classList.add("hidden");
        return;
    }

    var lines = text.split("\n");
    var fragment = document.createDocumentFragment();
    var currentWrapper = null; // active special section wrapper
    var currentBody = null;    // body div inside wrapper

    var inCodeBlock = false;
    var codeBlockStart = 0;
    var codeLines = [];

    for (var i = 0; i < lines.length; i++) {
        var line = lines[i];
        var lineNum = i + 1;

        // Handle code blocks
        if (line.trim().indexOf("```") === 0) {
            if (!inCodeBlock) {
                inCodeBlock = true;
                codeBlockStart = i;
                codeLines = [line];
                continue;
            } else {
                inCodeBlock = false;
                codeLines.push(line);
                var codeDiv = createPlanLine(codeBlockStart + 1, codeLines.join("\n").length, "<pre><code>" + escapeHTML(codeLines.join("\n").trim()) + "</code></pre>");
                appendToTarget(currentBody || fragment, codeDiv);
                continue;
            }
        }

        if (inCodeBlock) {
            codeLines.push(line);
            continue;
        }

        // Check for special section start
        var section = detectSection(line);
        if (section) {
            // Close previous wrapper if any
            if (currentWrapper) {
                fragment.appendChild(currentWrapper);
                currentWrapper = null;
                currentBody = null;
            }
            currentWrapper = document.createElement("div");
            currentWrapper.className = "plan-section-wrapper " + section.css;

            var header = document.createElement("h2");
            header.className = "section-header";
            header.innerHTML = section.icon + " " + section.label + ' <span class="toggle">\u25BE</span>';
            header.addEventListener("click", (function (w) {
                return function () { w.classList.toggle("collapsed"); };
            })(currentWrapper));

            currentBody = document.createElement("div");
            currentBody.className = "plan-section-body";

            currentWrapper.appendChild(header);
            currentWrapper.appendChild(currentBody);
            continue;
        }

        // If we hit another ## while inside a special section, close the wrapper
        if (currentWrapper && isH2(line)) {
            fragment.appendChild(currentWrapper);
            currentWrapper = null;
            currentBody = null;
        }

        // Regular line
        var html = formatLine(line);
        var div = createPlanLine(lineNum, lineNum, html);
        appendToTarget(currentBody || fragment, div);
    }

    // Close unclosed wrapper
    if (currentWrapper) {
        fragment.appendChild(currentWrapper);
    }

    // Unclosed code block
    if (inCodeBlock && codeLines.length > 0) {
        var codeDiv2 = createPlanLine(codeBlockStart + 1, lines.length, "<pre><code>" + escapeHTML(codeLines.join("\n")) + "</code></pre>");
        fragment.appendChild(codeDiv2);
    }

    $planContent.appendChild(fragment);
    updateReviseButton();
}

function appendToTarget(target, element) {
    target.appendChild(element);
}
```

- [ ] **Step 3: Обновить `renderMarkdown` — подсветка для не-review режимов**

Заменить функцию `renderMarkdown` (строки 428–476). Добавить ту же логику с wrapper-div, но без inline-комментариев (обычные HTML-строки вместо `plan-line`):

```javascript
function renderMarkdown(text) {
    if (!text) return "";

    text = text.replace(/```([\s\S]*?)```/g, function (_, code) {
        return "<pre><code>" + escapeHTML(code.trim()) + "</code></pre>";
    });

    var lines = text.split("\n");
    var html = [];
    var inList = false;
    var inSection = null; // "assumptions" or "criteria"

    for (var i = 0; i < lines.length; i++) {
        var line = lines[i];
        var section = detectSection(line);

        if (section) {
            if (inList) { html.push("</ul>"); inList = false; }
            if (inSection) { html.push("</div></div>"); }
            inSection = section.css;
            html.push('<div class="plan-section-wrapper ' + section.css + '">');
            html.push('<h2 class="section-header">' + section.icon + " " + section.label + "</h2>");
            html.push('<div class="plan-section-body">');
            continue;
        }

        if (inSection && isH2(line)) {
            if (inList) { html.push("</ul>"); inList = false; }
            html.push("</div></div>");
            inSection = null;
        }

        if (line.indexOf("### ") === 0) {
            if (inList) { html.push("</ul>"); inList = false; }
            html.push("<h3>" + inlineFormat(line.slice(4)) + "</h3>");
            continue;
        }
        if (line.indexOf("## ") === 0) {
            if (inList) { html.push("</ul>"); inList = false; }
            html.push("<h2>" + inlineFormat(line.slice(3)) + "</h2>");
            continue;
        }
        if (line.indexOf("# ") === 0) {
            if (inList) { html.push("</ul>"); inList = false; }
            html.push("<h1>" + inlineFormat(line.slice(2)) + "</h1>");
            continue;
        }

        if (/^[-*]\s+/.test(line)) {
            if (!inList) { html.push("<ul>"); inList = true; }
            html.push("<li>" + inlineFormat(line.replace(/^[-*]\s+/, "")) + "</li>");
            continue;
        }

        if (inList) { html.push("</ul>"); inList = false; }

        if (line.trim() === "") {
            html.push("");
            continue;
        }

        html.push("<p>" + inlineFormat(line) + "</p>");
    }

    if (inList) { html.push("</ul>"); }
    if (inSection) { html.push("</div></div>"); }
    return html.join("\n");
}
```

- [ ] **Step 4: Коммит**

```bash
git add pkg/web/app.js
git commit -m "feat: визуальное выделение секций Assumptions и Acceptance Criteria в дашборде"
```

---

### Task 4: Проверка backward compatibility и ручной тест

**Files:**
- Нет файловых изменений

- [ ] **Step 1: Собрать проект**

```bash
make build
```

Expected: успешная сборка без ошибок.

- [ ] **Step 2: Проверить рендер плана без спецсекций**

Создать тестовый файл плана без Assumptions/Acceptance Criteria и убедиться что он рендерится как раньше. Открыть дашборд, выбрать стадию — план должен отображаться без wrapper-div.

- [ ] **Step 3: Проверить рендер плана со спецсекциями**

Создать тестовый файл плана с секциями:

```markdown
# Test Stage Plan

## Overview
Set up integration tests for the auth module.

## Tasks
- [ ] Write test for login endpoint
- [ ] Write test for token refresh

## Assumptions
- Using Go standard testing package, not testify — project has no external test dependencies
- Tests hit a real SQLite database, not mocks — per project convention

## Acceptance Criteria
- [ ] POST /api/login with valid credentials returns 200 and JWT token
- [ ] POST /api/login with invalid password returns 401
- [ ] GET /api/protected without token returns 401
- [ ] GET /api/protected with expired token returns 401
```

Открыть в дашборде. Проверить:
- Assumptions — жёлтый левый бордер, иконка warning
- Acceptance Criteria — зелёный левый бордер, иконка check
- Оба блока сворачиваются/разворачиваются по клику на заголовок
- Inline-комментарии работают внутри спецсекций (только в awaiting_approval режиме)
- Остальной план рендерится без изменений

- [ ] **Step 4: Проверить линт**

```bash
make lint
```

Expected: без ошибок.
