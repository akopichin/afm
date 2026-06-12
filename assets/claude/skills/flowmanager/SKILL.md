---
description: Run a flowManager flow with stage plan approval
allowed-tools: [Bash, Read, AskUserQuestion]
---

# flowmanager — Run a Flow

**SCOPE**: Launch a flowManager flow. Fire-and-forget — the main context is freed immediately.

## Step 0: Verify Installation

```bash
which flowmanager
```

If not found: `go install github.com/akopichin/afm/cmd/flowmanager@latest`

## Step 1: Select Flow

```bash
flowmanager list
```

If argument was provided to the skill, use it directly. Otherwise show available flows via AskUserQuestion.

## Step 2: Launch Flow

Tell the user to run the flow in a separate terminal:

> "Запусти в отдельном терминале: `flowmanager run {selected-flow}`"
> "Дашборд откроется автоматически на http://localhost:9876"
> "Для статуса используй `/flowmanager-check`, для ревью плана — `/flowmanager-review <stage>`"

**STOP. Контекст свободен.**

## Constraints

- Do NOT spawn any subagents
- Do NOT poll or wait for anything
- Do NOT modify any code
