# Plan Feedback Loop

**Date:** 2026-04-16

## Overview

When a stage plan is ready for approval, the agent shows the user the path to `plan.md` and waits for a response. If the user provides feedback, the planning agent revises the plan and shows it again. The loop continues until the user approves. No changes if the user approves immediately.

## User-Facing Flow

```
Agent:  "Plan ready: .flowManager/runs/jwt-auth-20260416-152543/backend-auth/plan.md
         Review and reply 'ok' to approve or write your feedback."

User:   "Add a section on rollback strategy"

Agent:  [runs revise] "Plan updated. Review and reply 'ok' to approve or write feedback."

User:   "ok"

Agent:  [runs approve] "Stage 'backend-auth' approved."
```

## Components

### 1. Binary: `flowmanager revise <stage-id> [--feedback "..."]`

**File:** `cmd/flowmanager/revise.go`

- Locate run directory via `findLatestStateFile()` (reuse from `approve.go`)
- Read current `plan.md` from `{runDir}/{stage-id}/plan.md`
- Read feedback from `--feedback` flag; if absent, read from stdin
- Determine revision number N = count of existing `planning-revision-*.log` files in stage dir
- Build revised planning prompt (see Prompt section below)
- Call `executor.RunPlanning(ctx, prompt, planFile, logFile)` where `logFile = planning-revision-N.log`
- Set stage status back to `awaiting_approval` via `state.Load` → `SetStageStatus` → `state.Save`

**New Cobra command registered in `main.go`:**
```go
rootCmd.AddCommand(newReviseCmd())
```

### 2. Revised Planning Prompt

```
{planning template}

## Current Plan

{contents of plan.md}

## Feedback

{user feedback}

Please revise the plan taking the feedback into account. Output ONLY the revised plan markdown.
```

Built by a local `buildRevisionPrompt(template, currentPlan, feedback string) string` function inside `revise.go`. No need for stage metadata — the current plan already contains full context. No changes to `orchestrator.go`.

### 3. Skill: `flowmanager/SKILL.md` — Step 3 replacement

Replace current Step 3 (poll + AskUserQuestion "Approve?") with:

```
When stage shows awaiting_approval:
1. Note the path: .flowManager/runs/{run}/{stage-id}/plan.md
2. AskUserQuestion: "Plan ready: {path}. Review and reply 'ok' to approve or write your feedback."
3. If response matches approval (ok / да / approve / yes / lgtm / выглядит хорошо):
     → flowmanager approve {stage-id}
4. Else (response is feedback):
     → flowmanager revise {stage-id} --feedback "{response}"
     → goto 2
```

Multiple stages can be in `awaiting_approval` simultaneously. Handle each via a separate `AskUserQuestion` (sequential is fine — unblock one stage at a time).

## File Layout After a Revision

```
.flowManager/runs/jwt-auth-20260416-152543/
  backend-auth/
    plan.md                    ← always the latest plan
    planning.log               ← original planning run
    planning-revision-1.log    ← first revision
    planning-revision-2.log    ← second revision (if any)
    implementation.log
```

## What Does NOT Change

- `orchestrator.go` — untouched (no new functions needed)
- `state.go` — untouched  
- `executor.go` — untouched
- `approve.go` — untouched
- `WaitForApprovals` polling loop — untouched (revise sets status back to `awaiting_approval`, so the skill re-enters the loop naturally)

## Error Handling

- If `plan.md` does not exist when `revise` is called: return error "no plan found for stage {id}"
- If stage is not in `awaiting_approval`: return error "stage {id} is {status}, not awaiting_approval"
- If planning agent fails: set status to `failed`, return error (same as `runPlanningAgent`)
