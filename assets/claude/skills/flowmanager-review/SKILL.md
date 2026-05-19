---
description: Review a stage plan and provide feedback or approve
allowed-tools: [Bash, Read, AskUserQuestion]
---

# flowmanager-review — Review Stage Plan

**SCOPE**: Read a stage plan, ask for approval or feedback, then call approve/revise.

## Step 1: Find the stage

If argument was provided, use it as stage ID. Otherwise:

```bash
flowmanager check
```

Ask the user which stage to review via AskUserQuestion.

## Step 2: Read the plan

Find the latest run directory:

```bash
ls -t .flowManager/runs/ | head -1
```

Read the plan file:

```bash
cat .flowManager/runs/{run_dir}/{stage_id}/plan.md
```

## Step 3: Show plan and ask for feedback

Show the plan content to the user via AskUserQuestion:

> "Plan for stage `{stage_id}`:"
>
> {plan content}
>
> Reply **ok** to approve, or write your feedback for revision.

## Step 4: Act on feedback

**If approved** (ok / да / yes / lgtm / approve):

```bash
flowmanager approve {stage_id}
```

**If feedback** (any other text):

```bash
flowmanager revise {stage_id} --feedback "{user response verbatim}"
```

## Step 5: STOP

Report the result and **STOP immediately**. Do NOT poll or wait.
