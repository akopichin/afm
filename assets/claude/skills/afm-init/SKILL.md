---
description: Create a new flow.yaml interactively
allowed-tools: [Bash, AskUserQuestion, Read]
---

# afm init — Create a Flow

## Step 0: Verify Installation

```bash
which afm
```

## Step 1: Gather Information

Use AskUserQuestion to collect:
1. Flow name (kebab-case)
2. Flow description
3. For each stage: id, name, description, depends_on, agents

## Step 2: Run Init

Pass the answers interactively to:

```bash
afm init
```

Or construct the YAML manually and save to `.afm/flows/{name}.yaml`.

## Step 3: Confirm

Show the created file path and contents. Ask if user wants to run it now.
