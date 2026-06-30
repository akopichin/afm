---
description: Retry a failed stage in the current afm run
allowed-tools: [Bash]
---

# afm-retry — Retry Failed Stage

**SCOPE**: Retry a failed stage, then show the status.

## Step 1: Find the failed stage

If argument was provided, use it as stage ID. Otherwise:

```bash
afm check
```

If no failed stages found, tell the user and **STOP**.

## Step 2: Retry

```bash
afm retry {stage_id}
```

## Step 3: Show status

```bash
afm check
```

**STOP immediately.** The user needs to run `afm run` separately to restart the flow.
