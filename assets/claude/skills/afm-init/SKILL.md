---
name: afm-init
description: Create a new flow.yaml interactively
allowed-tools: [Bash, AskUserQuestion, Read]
---

# afm init — Create a Flow

## Step 0: Verify Installation

```bash
which afm
```

## Step 1: Gather Information

If the user wants a ready-made workflow, check the AFM pipeline catalog through
the bundled `afm-pipelines` skill before designing a new flow. Offer a matching
package when one exists. Continue with a custom flow when that is the user's
preference or no package fits; catalog access is not required for custom flows.

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
