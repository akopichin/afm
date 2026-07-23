# README & release-notes English Translation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Translate `README.md` and `release-notes.md` fully to English, in place, with no Russian left in either file.

**Architecture:** Not a code change — a content rewrite of two Markdown files, done section by section so each task is independently reviewable and revertible. No new files, no structural reorganization beyond one internal anchor-link fix.

**Tech Stack:** Markdown, git, grep (for verification — no test runner applies).

## Global Constraints

- Full replacement — no `.ru.md` fork is kept; Russian text lives on only in git history.
- Translate everything, including comments and example strings inside code blocks (e.g. `# Одобрить`, `--feedback "Нужно добавить..."`, `description: "Краткое описание задачи"`).
- Do NOT translate: code identifiers, CLI flags, file paths, YAML keys, command names.
- Terminology glossary (apply consistently across both files):
  | Russian | English |
  |---|---|
  | стадия | stage |
  | флоу | flow |
  | супервизор | supervisor |
  | автономный трек | autonomous track |
  | дашборд | dashboard |
  | скилл | skill |
  | план | plan |
  | одобрение | approval |
- The heading `## Супервизор и автономный трек` becomes `## Supervisor and Autonomous Track` (GitHub anchor slug: `#supervisor-and-autonomous-track`) — used by both the task that translates the heading and the task that fixes the link pointing to it.
- Going forward, new `release-notes.md` entries are written in English — no code change enforces this, it's a convention to follow after this plan lands.
- Commit messages stay in Russian (project-wide convention, unrelated to file content language) — per user's global CLAUDE.md instructions.
- After each task, verify with `grep -c` for specific Russian phrases from that section (not line-range greps — translation can shift line counts) — the count must drop to 0, and the English heading/phrase must be present.

---

## File Structure

Only two existing files are modified, no files created:
- `README.md` — translated in 4 tasks, top to bottom by section boundary.
- `release-notes.md` — translated in 2 tasks, split at the `## 2026-07-14` boundary (roughly even split by line count).

A final verification task sweeps both files for any remaining Cyrillic.

---

### Task 1: README.md — intro, "How it works", "Installation" (lines 1–108)

**Files:**
- Modify: `README.md:1-108`

**Interfaces:**
- Produces: the final English heading text `## Supervisor and Autonomous Track` is referenced here (in the anchor link fix) before it exists yet in the file — Task 3 creates the heading with matching text.

- [ ] **Step 1: Read the current section**

Run: `sed -n '1,108p' README.md`

Read Russian source for: title/intro paragraph, `## Как это работает` (how it works, including the autonomous-track paragraph and reliability paragraph), `## Установка` (installation: Homebrew, from-source, prebuilt binary, `### Запуск в Docker`).

- [ ] **Step 2: Translate lines 1–108 in place**

Use `Edit` to rewrite the section into English, applying the glossary from Global Constraints. Preserve all Markdown structure (headings, code fences, bullet lists, bold/italic markers) and every code identifier/flag/path unchanged. Translate code-block comments too (e.g. the Docker/Homebrew example comments).

In this section, update the one internal link:
```
См. [Супервизор и автономный трек](#супервизор-и-автономный-трек).
```
becomes:
```
See [Supervisor and Autonomous Track](#supervisor-and-autonomous-track).
```

- [ ] **Step 3: Verify no Russian remains in this range and the link target is correct**

Run:
```bash
grep -c "Как это работает" README.md
grep -c "супервизор-и-автономный-трек" README.md
grep -c "supervisor-and-autonomous-track" README.md
```
Expected: first two commands print `0`, the third prints `1`.

Run a broader sweep restricted to translated content so far:
```bash
sed -n '1,108p' README.md | grep -oP '[а-яА-ЯёЁ]+' | head
```
Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: перевести intro/установку README на английский"
```

---

### Task 2: README.md — "Quick Start", "Working directory", "flow.yaml" (lines 109–265)

**Files:**
- Modify: `README.md:109-265`

**Interfaces:**
- Consumes: nothing from Task 1 beyond the file already being partially English.
- Produces: nothing consumed by later tasks (Task 3's anchor target is independent).

- [ ] **Step 1: Read the current section**

Run: `sed -n '109,265p' README.md`

Covers: `## Быстрый старт` (Quick Start: create flow, run, approve plans, follow progress), `## Указание рабочей директории` (specifying working directory — flag vs env var), `## Файл flow.yaml` (the flow.yaml reference, including all inline YAML comments and example values like `description: "Краткое описание задачи"`).

- [ ] **Step 2: Translate lines 109–265 in place**

Use `Edit` to rewrite into English per the glossary. Pay particular attention to YAML example comments (`# опционально — скиллы Claude`, `# уникальный ID стадии`, etc.) and example string values (`description: "Реализовать UI по API-контракту"`) — these are illustrative and should read naturally in English.

- [ ] **Step 3: Verify**

Run:
```bash
grep -c "Быстрый старт" README.md
grep -c "Указание рабочей директории" README.md
sed -n '109,265p' README.md | grep -oP '[а-яА-ЯёЁ]+' | head
```
Expected: first two print `0`, third prints no output.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: перевести Quick Start и flow.yaml в README на английский"
```

---

### Task 3: README.md — stage context passing, interactive stages, supervisor, config (lines 266–400)

**Files:**
- Modify: `README.md:266-400`

**Interfaces:**
- Produces: the heading `## Supervisor and Autonomous Track` with anchor `#supervisor-and-autonomous-track`, matching the link Task 1 already wrote.

- [ ] **Step 1: Read the current section**

Run: `sed -n '266,400p' README.md`

Covers: `### Передача контекста между стадиями` (passing context between stages), `### Интерактивные стадии` (interactive stages), `## Супервизор и автономный трек` (the section this task must title exactly `## Supervisor and Autonomous Track`), `### Жёсткий автономный трек: \`agents: [auto]\``, `## Конфигурация` (configuration).

- [ ] **Step 2: Translate lines 266–400 in place**

Use `Edit` to rewrite into English. The heading at line 320 must become exactly:
```
## Supervisor and Autonomous Track
```
(so its GitHub-generated anchor is `#supervisor-and-autonomous-track`, matching the link fixed in Task 1).

- [ ] **Step 3: Verify**

Run:
```bash
grep -c "^## Supervisor and Autonomous Track$" README.md
grep -c "Супервизор и автономный трек" README.md
sed -n '266,400p' README.md | grep -oP '[а-яА-ЯёЁ]+' | head
```
Expected: first prints `1`, second prints `0`, third prints no output.

Confirm the link from Task 1 now resolves to a real heading:
```bash
grep -n "Supervisor and Autonomous Track" README.md
```
Expected: two matches — the link line and the heading line.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: перевести супервизор/автономный трек/конфигурацию README на английский"
```

---

### Task 4: README.md — dashboard, directory structure, usage, stage lifecycle, development (lines 401–513)

**Files:**
- Modify: `README.md:401-513`

- [ ] **Step 1: Read the current section**

Run: `sed -n '401,513p' README.md`

Covers: `## Веб-дашборд` (web dashboard, incl. inline plan comments, resume-on-restart), `## Структура директорий` (directory structure), `## Использование в Claude Code` (usage in Claude Code), `## Жизненный цикл стадии` (stage lifecycle), `## Разработка` (development).

- [ ] **Step 2: Translate lines 401–513 in place**

Use `Edit` to rewrite into English per the glossary.

- [ ] **Step 3: Verify whole file is now fully English**

Run:
```bash
grep -oP '[а-яА-ЯёЁ]+' README.md | head
```
Expected: no output (file is now 100% free of Cyrillic).

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: перевести дашборд/структуру/разработку README на английский"
```

---

### Task 5: release-notes.md — 2026-07-23 through 2026-07-15 (lines 1–123)

**Files:**
- Modify: `release-notes.md:1-123`

- [ ] **Step 1: Read the current section**

Run: `sed -n '1,123p' release-notes.md`

Covers date sections `## 2026-07-23` down through `## 2026-07-15` (inclusive), each with several `### ...` changelog entries.

- [ ] **Step 2: Translate lines 1–123 in place**

Use `Edit` to rewrite into English per the glossary. Each `###` entry title and its bullet points get translated; inline code (`` `root_dir` ``, `` `EvReady` ``, etc.) stays unchanged. Keep the intro line (`Новые возможности — сверху...`) — translate it too, it's part of this range.

- [ ] **Step 3: Verify**

Run:
```bash
grep -c "Release Notes" release-notes.md
sed -n '1,123p' release-notes.md | grep -oP '[а-яА-ЯёЁ]+' | head
```
Expected: first prints `1` (heading already English in source), second prints no output.

- [ ] **Step 4: Commit**

```bash
git add release-notes.md
git commit -m "docs: перевести release-notes (07-15..07-23) на английский"
```

---

### Task 6: release-notes.md — 2026-07-14 through 2026-06-29 (lines 124–263)

**Files:**
- Modify: `release-notes.md:124-263`

- [ ] **Step 1: Read the current section**

Run: `sed -n '124,263p' release-notes.md`

Covers date sections `## 2026-07-14` down through `## 2026-06-29` (the oldest entries).

- [ ] **Step 2: Translate lines 124–263 in place**

Use `Edit` to rewrite into English per the glossary.

- [ ] **Step 3: Verify whole file is now fully English**

Run:
```bash
grep -oP '[а-яА-ЯёЁ]+' release-notes.md | head
```
Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add release-notes.md
git commit -m "docs: перевести release-notes (06-29..07-14) на английский"
```

---

### Task 7: Final verification sweep

**Files:**
- None modified — read-only verification.

- [ ] **Step 1: Confirm zero Cyrillic in both files**

Run:
```bash
grep -oP '[а-яА-ЯёЁ]+' README.md release-notes.md
```
Expected: no output, exit code 1 (grep found nothing).

- [ ] **Step 2: Confirm the anchor link resolves**

Run:
```bash
grep -n "Supervisor and Autonomous Track" README.md
```
Expected: exactly 2 matches (the `[Supervisor and Autonomous Track](#supervisor-and-autonomous-track)` link line, and the `## Supervisor and Autonomous Track` heading line).

- [ ] **Step 3: Full read-through**

Read both files fully (`README.md`, `release-notes.md`) end to end and confirm: fluent English, consistent terminology per the glossary, no leftover Markdown breakage (unclosed code fences, broken tables) introduced by the edits.

- [ ] **Step 4: Update the design spec's status (optional housekeeping)**

No code change. This step is just confirming `docs/superpowers/specs/2026-07-23-readme-release-notes-translation-design.md` requirements are all met — no file edit needed if everything above passed.
