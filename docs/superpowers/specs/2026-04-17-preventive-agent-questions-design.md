# Превентивные вопросы агентов: Assumptions & Acceptance Criteria

**Date:** 2026-04-17

## Проблема

Агенты работают в неинтерактивном режиме (`--print`). Если при планировании агент сталкивается с неоднозначностью (например, "какие тест-кейсы покрыть?"), он не может задать вопрос пользователю. Вместо этого агент принимает решение молча, и пользователь узнаёт о нём только при ревью готовой реализации — когда переделывать дорого.

## Решение

Превентивный подход: заставить planning-агента **явно фиксировать** assumptions и acceptance criteria в плане. Пользователь проверяет их при approve/revise через дашборд. UI визуально выделяет эти секции, чтобы их было невозможно пропустить.

## Компоненты

### 1. Промпт `assets/prompts/planning.md`

Добавить в конец (перед финальной строкой "Output ONLY the plan markdown"):

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
```

Названия секций (`## Assumptions`, `## Acceptance Criteria`) фиксированы — UI ищет именно эти строки.

### 2. UI: визуальное выделение в `app.js`

В функциях `renderPlanReview` и `renderMarkdown` — при встрече заголовка `## Assumptions` или `## Acceptance Criteria` все строки до следующего `##` оборачиваются в `<div>` с CSS-классом.

**Детекция:** в цикле по строкам, при встрече `## Assumptions` устанавливается флаг `currentSection = "assumptions"`, при `## Acceptance Criteria` — `currentSection = "criteria"`. При следующем `##` или конце файла — флаг сбрасывается. Строки внутри секции получают дополнительный wrapper-div.

**Рендер заголовков секций:**
- `## Assumptions` → `<h2 class="section-header section-assumptions">⚠ Assumptions <span class="toggle">▾</span></h2>`
- `## Acceptance Criteria` → `<h2 class="section-header section-criteria">✓ Acceptance Criteria <span class="toggle">▾</span></h2>`

**Сворачивание:** клик по заголовку toggle-ит класс `.collapsed` на wrapper-div. По умолчанию развёрнуто.

**Inline-комментарии** работают как раньше — строки внутри секций остаются `plan-line` с номерами, просто вложены в wrapper.

### 3. UI: стили в `style.css`

```css
.plan-section-assumptions {
    border-left: 3px solid #f0ad4e;
    background: rgba(240, 173, 78, 0.05);
    padding: 8px 0 8px 12px;
    margin: 8px 0;
    border-radius: 0 4px 4px 0;
}

.plan-section-criteria {
    border-left: 3px solid #5cb85c;
    background: rgba(92, 184, 92, 0.05);
    padding: 8px 0 8px 12px;
    margin: 8px 0;
    border-radius: 0 4px 4px 0;
}

.section-header {
    cursor: pointer;
    user-select: none;
}

.section-header .toggle {
    font-size: 12px;
    margin-left: 4px;
    transition: transform 0.2s;
}

.collapsed .section-header .toggle {
    transform: rotate(-90deg);
}

.collapsed .plan-section-body {
    display: none;
}
```

### 4. Что НЕ меняется

- `orchestrator.go` — без изменений
- `executor.go` — без изменений
- `handlers.go` — без изменений
- `websocket.go` — без изменений
- `server.go` — без изменений
- `state.go` — без изменений
- `flow.yaml` формат — без изменений
- Inline-комментарии — без изменений
- Approve/Revise механизм — без изменений

## Файлы для изменения

| Файл | Изменение |
|------|-----------|
| `assets/prompts/planning.md` | Добавить секцию Required sections (~10 строк) |
| `pkg/web/app.js` | Детекция секций + wrapper-div + toggle (~30 строк) |
| `pkg/web/style.css` | Стили для `.plan-section-*` (~20 строк) |

## Тестирование

- Создать flow с тестовой стадией, убедиться что planning-агент генерирует секции Assumptions и Acceptance Criteria
- В дашборде проверить: секции визуально выделены, сворачиваются/разворачиваются, inline-комментарии работают внутри них
- Проверить что планы без этих секций рендерятся как раньше (backward compatibility)
