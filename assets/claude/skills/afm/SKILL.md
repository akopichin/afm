---
description: Run a afm flow with stage plan approval
allowed-tools: [Bash, Read, AskUserQuestion]
---

# afm — Run a Flow

**SCOPE**: Launch a afm flow. Fire-and-forget — the main context is freed immediately.

## Step 0: Verify Installation

```bash
which afm
```

If not found: `go install github.com/akopichin/afm/cmd/afm@latest`

## Step 1: Select Flow

```bash
afm list
```

If argument was provided to the skill, use it directly. Otherwise show available flows via AskUserQuestion.

## Step 2: Launch Flow

Tell the user to run the flow in a separate terminal:

> "Запусти в отдельном терминале: `afm run {selected-flow}`"
> "Дашборд откроется автоматически на http://localhost:9876"
> "Для статуса используй `/afm-check`, для ревью плана — `/afm-review <stage>`"

**STOP. Контекст свободен.**

## Constraints

- Do NOT spawn any subagents
- Do NOT poll or wait for anything
- Do NOT modify any code
