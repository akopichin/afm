# flowmanager Non-blocking Monitor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Переписать `/flowmanager` скилл так, чтобы мониторинг выполнялся фоновым субагентом, а основной контекст оставался свободным для разговора с пользователем.

**Architecture:** Новый скилл `flowmanager-monitor` исполняется как субагент — поллит `state.json` и завершается как только находит `awaiting_approval`, `done` или `failed`. Основной агент запускает и перезапускает этот субагент, а между запусками свободен для общения. Вся логика аппрува остаётся в основном контексте.

**Tech Stack:** Claude Code Agent tool, Bash, SKILL.md files (markdown)

---

## File Map

| Действие | Файл |
|----------|------|
| Создать | `assets/claude/skills/flowmanager-monitor/SKILL.md` |
| Изменить | `assets/claude/skills/flowmanager/SKILL.md` |
| Изменить | `Makefile` — добавить таргет `install-skills` |
| Установить | `~/.claude/skills/flowmanager-monitor/SKILL.md` |
| Обновить | `~/.claude/skills/flowmanager/SKILL.md` |

---

## Task 1: Добавить `install-skills` в Makefile

**Files:**
- Modify: `Makefile`

- [ ] **Step 1: Открыть Makefile и добавить таргет**

Добавить в конец `Makefile` (после таргета `install`):

```makefile
SKILLS_DIR=$(HOME)/.claude/skills
SKILLS_SRC=$(CURDIR)/assets/claude/skills

.PHONY: install-skills
install-skills:
	@for skill in $(SKILLS_SRC)/*/; do \
		name=$$(basename $$skill); \
		mkdir -p $(SKILLS_DIR)/$$name; \
		cp $$skill/SKILL.md $(SKILLS_DIR)/$$name/SKILL.md; \
		echo "installed skill: $$name"; \
	done
```

- [ ] **Step 2: Проверить что Makefile парсится**

```bash
make --dry-run install-skills
```

Ожидаемый вывод — команды без ошибок синтаксиса.

- [ ] **Step 3: Commit**

```bash
git add Makefile
git commit -m "feat: добавить make install-skills для установки скиллов"
```

---

## Task 2: Создать скилл `flowmanager-monitor`

**Files:**
- Create: `assets/claude/skills/flowmanager-monitor/SKILL.md`

- [ ] **Step 1: Создать директорию и файл**

Создать `assets/claude/skills/flowmanager-monitor/SKILL.md` с содержимым:

```markdown
---
description: Monitor a flowManager run and return when user action is needed
allowed-tools: [Bash]
---

# flowmanager-monitor — Background Monitor

Poll `state.json` every 3 seconds until a stage needs approval, the flow completes, or 15 minutes elapse.

## Steps

**1. Find the latest run directory:**

```bash
ls -t .flowManager/runs/ | head -1
```

Save as `{run_dir}`.

**2. Poll loop (max 300 iterations = 15 minutes):**

```bash
cat .flowManager/runs/{run_dir}/state.json
```

On each poll, inspect all stage statuses:

- If any stage has status `awaiting_approval` → output `NEEDS_APPROVAL` result and **STOP**
- If all stages have status `done` → output `ALL_DONE` result and **STOP**
- If any stage has status `failed` → output `FAILED` result and **STOP**
- Otherwise → wait 3 seconds (`sleep 3`) and poll again

If 300 iterations complete with no terminal state → output `FAILED reason=timeout` and **STOP**

## Output Format

Output EXACTLY one block below, then stop immediately. No other text.

**Needs approval:**
```
NEEDS_APPROVAL
stage_id: {stage-id}
plan_path: .flowManager/runs/{run_dir}/{stage-id}/plan.md
```

**All done:**
```
ALL_DONE
run_dir: .flowManager/runs/{run_dir}
```

**Failed or timeout:**
```
FAILED
stage_id: {stage-id or "unknown"}
reason: {stage failed | timeout}
```

## Constraints

- Do NOT approve, revise, or modify anything
- Do NOT interact with the user — output ONLY the structured result
- Do NOT read plan files — only `state.json`
```

- [ ] **Step 2: Проверить что файл существует**

```bash
cat assets/claude/skills/flowmanager-monitor/SKILL.md
```

Ожидаемый вывод: содержимое файла без ошибок.

- [ ] **Step 3: Установить скилл локально**

```bash
make install-skills
```

Ожидаемый вывод:
```
installed skill: flowmanager
installed skill: flowmanager-check
installed skill: flowmanager-init
installed skill: flowmanager-monitor
```

- [ ] **Step 4: Проверить установку**

```bash
cat ~/.claude/skills/flowmanager-monitor/SKILL.md | head -5
```

Ожидаемый вывод — первые строки с frontmatter `---`.

- [ ] **Step 5: Commit**

```bash
git add assets/claude/skills/flowmanager-monitor/
git commit -m "feat: добавить скилл flowmanager-monitor для фонового мониторинга"
```

---

## Task 3: Переписать скилл `flowmanager`

**Files:**
- Modify: `assets/claude/skills/flowmanager/SKILL.md`

- [ ] **Step 1: Заменить содержимое файла**

Перезаписать `assets/claude/skills/flowmanager/SKILL.md`:

```markdown
---
description: Run a flowManager flow with stage plan approval
allowed-tools: [Bash, Read, AskUserQuestion, Agent, TaskOutput]
---

# flowmanager — Run a Flow

**SCOPE**: Launch a flowManager flow and handle plan approvals via a non-blocking background monitor.

## Step 0: Verify Installation

```bash
which flowmanager
```

If not found:
- macOS: `brew install <owner>/tap/flowmanager`
- Any platform: `go install github.com/akopichin/afm/cmd/flowmanager@latest`

## Step 1: Select Flow

```bash
flowmanager list
```

If argument was provided to the skill, use it directly. Otherwise show available flows via AskUserQuestion.

## Step 2: Launch Flow

```bash
flowmanager run {selected-flow}
```

Run with `run_in_background: true`. Save the task_id.

Tell the user: "Флоу запущен. Уведомлю когда план будет готов к аппруву."

## Step 3: Spawn Monitor Subagent

Dispatch a background Agent using the `flowmanager-monitor` skill.

Read the skill content from `~/.claude/skills/flowmanager-monitor/SKILL.md` and use it as the agent prompt.

Run the agent with `run_in_background: true`. Save the monitor agent ID.

## Step 4: Handle Monitor Result

When the monitor subagent completes, read its result output and branch:

---

### Result: `NEEDS_APPROVAL`

1. Read the plan file:

```bash
cat {plan_path}
```

2. Ask the user via AskUserQuestion:

> "Plan for stage `{stage_id}` is ready at `{plan_path}`.
>
> {first 50 lines of plan content}
>
> Reply **ok** to approve, or write your feedback for revision."

3. **If approved** (ok / да / yes / lgtm / approve / выглядит хорошо / хорошо):

```bash
flowmanager approve {stage_id}
```

4. **If feedback** (any other text):

```bash
flowmanager revise {stage_id} --feedback "{user response verbatim}"
```

5. In both cases: go back to **Step 3** — spawn a new monitor subagent.

---

### Result: `ALL_DONE`

```bash
flowmanager check
```

Display the output. **STOP.**

---

### Result: `FAILED`

Report to the user:

> "Stage `{stage_id}` failed: {reason}. Run `/flowmanager-check` for details."

**STOP.**

---

## Constraints

- Do NOT modify any code yourself
- Do NOT take any actions on the codebase outside of `flowmanager` commands
- After ALL_DONE or FAILED: stop immediately
```

- [ ] **Step 2: Проверить что файл записан корректно**

```bash
cat assets/claude/skills/flowmanager/SKILL.md | head -10
```

Ожидаемый вывод — frontmatter и заголовок `# flowmanager — Run a Flow`.

- [ ] **Step 3: Установить обновлённый скилл**

```bash
make install-skills
```

- [ ] **Step 4: Проверить что `Agent` добавлен в allowed-tools**

```bash
grep "allowed-tools" ~/.claude/skills/flowmanager/SKILL.md
```

Ожидаемый вывод:
```
allowed-tools: [Bash, Read, AskUserQuestion, Agent, TaskOutput]
```

- [ ] **Step 5: Commit**

```bash
git add assets/claude/skills/flowmanager/SKILL.md
git commit -m "feat: переписать скилл flowmanager — неблокирующий мониторинг через субагент"
```

---

## Task 4: Smoke test

Ручная проверка что скиллы установлены и синтаксически корректны.

- [ ] **Step 1: Проверить оба скилла установлены**

```bash
ls ~/.claude/skills/flowmanager*/
```

Ожидаемый вывод: директории `flowmanager` и `flowmanager-monitor`, каждая с `SKILL.md`.

- [ ] **Step 2: Проверить frontmatter flowmanager-monitor**

```bash
head -5 ~/.claude/skills/flowmanager-monitor/SKILL.md
```

Ожидаемый вывод:
```
---
description: Monitor a flowManager run and return when user action is needed
allowed-tools: [Bash]
---
```

- [ ] **Step 3: Проверить frontmatter flowmanager**

```bash
head -5 ~/.claude/skills/flowmanager/SKILL.md
```

Ожидаемый вывод:
```
---
description: Run a flowManager flow with stage plan approval
allowed-tools: [Bash, Read, AskUserQuestion, Agent, TaskOutput]
---
```

- [ ] **Step 4: Убедиться что polling loop (Step 3 Monitor) отсутствует в flowmanager скилле**

```bash
grep -n "Poll\|every 3\|state.json" ~/.claude/skills/flowmanager/SKILL.md
```

Ожидаемый вывод: пусто (polling теперь только в flowmanager-monitor).

- [ ] **Step 5: Итоговый commit если нужны правки**

```bash
git add -A
git commit -m "fix: уточнить скиллы по результатам smoke test"
```
