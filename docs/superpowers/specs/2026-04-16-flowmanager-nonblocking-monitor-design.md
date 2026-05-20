# Design: Non-blocking flowmanager monitoring with feedback loop

**Date:** 2026-04-16  
**Status:** Approved

## Problem

When `/flowmanager flow.yml` is invoked, the main agent context becomes fully occupied by a polling loop (`cat state.json` every 3s + `AskUserQuestion` for approvals). The user cannot interact with the agent while a flow is running.

## Goal

- Main agent context is free for conversation while a flow runs
- Agent proactively notifies user when a plan needs approval
- User's approval/feedback is passed back to `flowmanager revise`/`flowmanager approve`
- The full revision feedback loop works: user response → revise → new plan → ask again

## Components

| Component | Role |
|-----------|------|
| `/flowmanager` skill | Entry point: launches binary + spawns monitor subagent; handles approval loop |
| `flowmanager-monitor` skill | Subagent instructions: polls state.json, returns when work is found |
| `/flowmanager-check` skill | Unchanged — manual status check |
| `flowmanager` binary | Unchanged |

## Communication Protocol

The monitor subagent exits and returns one of three structures in its final text:

**Needs approval:**
```
NEEDS_APPROVAL
stage_id: init
plan_path: .flowManager/runs/app-20260416-225624/init/plan.md
```

**All done:**
```
ALL_DONE
run_dir: .flowManager/runs/app-20260416-225624
```

**Error or timeout:**
```
FAILED
stage_id: init
reason: stage failed | timeout
```

No flag files, no shared state — all context is passed via the subagent's return value.

## Feedback Loop

```
User: /flowmanager flow.yml
  │
  ├─ flowmanager run flow.yml        [background process]
  ├─ spawn monitor subagent          [background agent]
  └─ "Flow started" — main context FREE

  ◄── subagent exits: NEEDS_APPROVAL stage=init
  │
  ├─ read plan.md
  ├─ AskUserQuestion: "Plan for init ready at {path}. Approve?"
  │
  │  User: "add rollback section"
  │
  ├─ flowmanager revise init --feedback "add rollback section"
  ├─ spawn NEW monitor subagent
  └─ main context FREE

  ◄── subagent exits: NEEDS_APPROVAL stage=init  (revised plan)
  │
  ├─ AskUserQuestion: "Updated plan for init at {path}. Approve?"
  │  User: "ok"
  ├─ flowmanager approve init
  ├─ spawn NEW monitor subagent
  └─ main context FREE

  ◄── subagent exits: ALL_DONE
  └─ flowmanager check → display result → STOP
```

`revise` resets stage status back to `awaiting_approval`, so the next monitor subagent naturally finds the revised plan — the revision loop is automatic.

## flowmanager-monitor Subagent Behavior

- Polls `state.json` every 3 seconds
- Exits immediately when it finds the first `awaiting_approval` stage
- Exits when all stages are `done` or any stage is `failed`
- Exits with `FAILED reason=timeout` after 15 minutes of no state change
- Does NOT interact with the user
- Does NOT approve or revise anything
- One run = "find me one piece of work"

## Edge Cases

| Case | Handling |
|------|----------|
| Multiple `awaiting_approval` stages simultaneously | Return first found; next subagent finds the next |
| Stage `failed` | Return `FAILED`; main agent reports and stops |
| 15 min idle timeout | Return `FAILED reason=timeout`; suggest `/flowmanager-check` |
| User sends message while waiting for subagent | Main agent responds normally; subagent notification arrives in next turn |
| `/flowmanager-check` called manually during run | Works independently — just reads `state.json`; doesn't interfere |

## Files to Create/Modify

- `assets/claude/skills/flowmanager/SKILL.md` — rewrite: remove polling loop, add subagent spawn + result handling
- `assets/claude/skills/flowmanager-monitor/SKILL.md` — new skill for the monitor subagent
- `~/.claude/skills/flowmanager/SKILL.md` — update installed copy
- `~/.claude/skills/flowmanager-monitor/SKILL.md` — install new skill
