# README Table of Contents + Restructure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a linked table of contents to `README.md` and reorder its top-level sections so the read order matches actual usage (onboard → write a flow → runtime concepts → monitor → configure → contribute).

**Architecture:** Not a code change — a single-file Markdown restructure, done in two tasks: (1) reorder existing section blocks + two small content fixes, (2) add the `## Contents` navigation section on top of the final structure (TOC anchors depend on final heading order/text, so it must come second).

**Tech Stack:** Markdown, git, `sed`/`cat` for precise line-range extraction and reassembly (no prose is rewritten — every line moves verbatim except two exact edits called out below).

## Global Constraints

- No prose rewrites beyond exactly two edits: (1) `brew install --cask akopichin/afm` → `brew install --cask akopichin/afm/afm` (the untargeted form doesn't actually work), (2) the heading `## Specifying the Working Directory` → `### Working Directory` (demoted to a subsection, folded into Configuration).
- Final top-level (`##`) section order, 11 sections total: How It Works, Installation, Quick Start, Usage in Claude Code, The flow.yaml File, Supervisor and Autonomous Track, Stage Lifecycle, Configuration, Web Dashboard, Directory Structure, Development.
- `### Working Directory` becomes the FIRST subsection of `## Configuration`, immediately after the `## Configuration` heading and before the existing "Create `.afm/config.yaml`..." intro paragraph.
- New `## Contents` section, placed immediately after the intro paragraph (before `## How It Works`), as a flat bullet list — one entry per `##` heading (11 entries), each linking to its GitHub-style anchor slug. No `###` subsections get their own entry.
- No content loss: every line from the original 12 sections must appear exactly once in the final file, except the two specified edits.
- Commit messages in Russian (project convention).

---

## File Structure

Only `README.md` is modified (in place), no new files created. Both tasks use a scratch temp directory (`mktemp -d`) for intermediate extraction — nothing there is committed.

---

### Task 1: Reorder sections, fold Working Directory into Configuration, fix brew install line

**Files:**
- Modify: `README.md` (in place, full rewrite via reassembly — see below)

**Interfaces:**
- Produces: the final section order and exact heading text (`### Working Directory`, `## Configuration`, etc.) that Task 2's TOC links must match exactly.

- [ ] **Step 1: Verify current section boundaries haven't drifted**

Run:
```bash
grep -n '^## \|^### ' README.md
```
Expected output — these exact line numbers (if they differ, the file has changed since this plan was written; stop and re-derive ranges before continuing):
```
5:## How It Works
20:## Installation
45:### Running in Docker (without a local install)
68:#### Authentication in Docker Mode
90:#### Non-Standard Agents in Docker (autoShim)
109:## Quick Start
111:### 1. Create a flow
119:### 2. Run
130:### 3. Approve plans
153:### 4. Follow progress
171:## Specifying the Working Directory
186:## The flow.yaml File
266:### Passing Context Between Stages
294:### Interactive Stages
320:## Supervisor and Autonomous Track
347:### Hard Autonomous Track: `agents: [auto]`
360:## Configuration
401:## Web Dashboard
410:### Inline Plan Comments
417:### Resume on Restart
428:## Directory Structure
458:## Usage in Claude Code
468:## Stage Lifecycle
491:## Development
```
(`#### Authentication...` and `#### Non-Standard Agents...` are `####`, matched by the `### ` pattern too since grep matches the prefix — that's expected and fine.)

- [ ] **Step 2: Extract each section into a scratch directory by exact line range**

Run:
```bash
WORKDIR=$(mktemp -d)
echo "$WORKDIR"

sed -n '1,4p'     README.md > "$WORKDIR/00-title.md"
sed -n '5,19p'    README.md > "$WORKDIR/01-how-it-works.md"
sed -n '20,108p'  README.md > "$WORKDIR/02-installation.md"
sed -n '109,170p' README.md > "$WORKDIR/03-quick-start.md"
sed -n '171,185p' README.md > "$WORKDIR/04-working-directory.md"
sed -n '186,319p' README.md > "$WORKDIR/05-flow-yaml.md"
sed -n '320,359p' README.md > "$WORKDIR/06-supervisor.md"
sed -n '360,361p' README.md > "$WORKDIR/07a-configuration-head.md"
sed -n '362,400p' README.md > "$WORKDIR/07b-configuration-body.md"
sed -n '401,427p' README.md > "$WORKDIR/08-web-dashboard.md"
sed -n '428,457p' README.md > "$WORKDIR/09-directory-structure.md"
sed -n '458,467p' README.md > "$WORKDIR/10-usage-claude-code.md"
sed -n '468,490p' README.md > "$WORKDIR/11-stage-lifecycle.md"
sed -n '491,513p' README.md > "$WORKDIR/12-development.md"

wc -l "$WORKDIR"/*.md | tail -1
```
Expected: the last `wc -l` line (the `total`) reads `513 total` — this confirms every line of the original 513-line file was captured across the fragments with no overlap or gap.

- [ ] **Step 3: Fix the Homebrew install command in the extracted Installation fragment**

Use the `Edit` tool on `$WORKDIR/02-installation.md`:
- old_string: `brew install --cask akopichin/afm`
- new_string: `brew install --cask akopichin/afm/afm`

- [ ] **Step 4: Demote the Working Directory heading**

Use the `Edit` tool on `$WORKDIR/04-working-directory.md`:
- old_string: `## Specifying the Working Directory`
- new_string: `### Working Directory`

- [ ] **Step 5: Reassemble README.md in the new order**

Run:
```bash
cat \
  "$WORKDIR/00-title.md" \
  "$WORKDIR/01-how-it-works.md" \
  "$WORKDIR/02-installation.md" \
  "$WORKDIR/03-quick-start.md" \
  "$WORKDIR/10-usage-claude-code.md" \
  "$WORKDIR/05-flow-yaml.md" \
  "$WORKDIR/06-supervisor.md" \
  "$WORKDIR/11-stage-lifecycle.md" \
  "$WORKDIR/07a-configuration-head.md" \
  "$WORKDIR/04-working-directory.md" \
  "$WORKDIR/07b-configuration-body.md" \
  "$WORKDIR/08-web-dashboard.md" \
  "$WORKDIR/09-directory-structure.md" \
  "$WORKDIR/12-development.md" \
  > README.md
```
This inserts the edited `04-working-directory.md` (now `### Working Directory`) between the Configuration heading (`07a`) and the rest of the Configuration body (`07b`), and moves `10-usage-claude-code.md` and `11-stage-lifecycle.md` to their new positions — all other fragments keep their relative content unchanged, just reordered.

- [ ] **Step 6: Verify no content was lost and the new structure is correct**

Run:
```bash
wc -l README.md
```
Expected: `513 README.md` (exact same total line count as the original — every line was moved, none added or removed, since only two single-line text edits were made and both preserve line count).

Run:
```bash
grep -c '^## ' README.md
```
Expected: `11` (was 12; "Specifying the Working Directory" is now a `###` subsection).

Run:
```bash
grep -n '^## \|^### Working Directory$' README.md
```
Expected — exact line numbers in the new arrangement (deterministic from the fragment sizes in Step 2; if these don't match exactly, an earlier step went wrong):
```
5:## How It Works
20:## Installation
109:## Quick Start
171:## Usage in Claude Code
181:## The flow.yaml File
315:## Supervisor and Autonomous Track
355:## Stage Lifecycle
378:## Configuration
380:### Working Directory
434:## Web Dashboard
461:## Directory Structure
491:## Development
```

Run:
```bash
grep -n 'brew install --cask' README.md
```
Expected: one match reading `brew install --cask akopichin/afm/afm`.

Run:
```bash
grep -c '^## Supervisor and Autonomous Track$' README.md
grep -n 'Supervisor and Autonomous Track' README.md
```
Expected: first command prints `1`; second prints exactly 2 matches (the intro link + the heading) — confirms the reorder didn't disturb the existing anchor link from the "How It Works" intro (heading text/anchor unaffected by reordering).

- [ ] **Step 7: Commit**

```bash
git add README.md
git commit -m "$(cat <<'EOF'
docs: реорганизовать разделы README + починить команду brew install
EOF
)"
```

---

### Task 2: Add the Contents (table of contents) section

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: the final section order and exact heading text from Task 1 (11 `##` headings, in the order listed in Task 1's Global Constraints).

- [ ] **Step 1: Confirm the insertion point**

Run:
```bash
sed -n '1,6p' README.md
```
Expected: title (`# afm`), blank, the intro paragraph, blank, then `## How It Works` on its own line. The Contents section goes right before `## How It Works`.

- [ ] **Step 2: Insert the Contents section**

Use the `Edit` tool on `README.md`:
- old_string:
```
Works with `claude` and with any claude-compatible agents (GLM, DeepSeek, Cursor, etc.).

## How It Works
```
- new_string:
```
Works with `claude` and with any claude-compatible agents (GLM, DeepSeek, Cursor, etc.).

## Contents

- [How It Works](#how-it-works)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [Usage in Claude Code](#usage-in-claude-code)
- [The flow.yaml File](#the-flowyaml-file)
- [Supervisor and Autonomous Track](#supervisor-and-autonomous-track)
- [Stage Lifecycle](#stage-lifecycle)
- [Configuration](#configuration)
- [Web Dashboard](#web-dashboard)
- [Directory Structure](#directory-structure)
- [Development](#development)

## How It Works
```

- [ ] **Step 3: Verify every TOC link resolves to a real heading**

Run this check for each of the 11 anchors (heading text → expected slug): confirm the heading exists and the slug matches GitHub's algorithm (lowercase, spaces→hyphens, punctuation stripped).

```bash
grep -c '^## How It Works$' README.md
grep -c '^## Installation$' README.md
grep -c '^## Quick Start$' README.md
grep -c '^## Usage in Claude Code$' README.md
grep -c '^## The flow.yaml File$' README.md
grep -c '^## Supervisor and Autonomous Track$' README.md
grep -c '^## Stage Lifecycle$' README.md
grep -c '^## Configuration$' README.md
grep -c '^## Web Dashboard$' README.md
grep -c '^## Directory Structure$' README.md
grep -c '^## Development$' README.md
```
Expected: every command prints `1`.

Run:
```bash
grep -c '^## Contents$' README.md
grep -n '\](#' README.md | head -15
```
Expected: first command prints `1`; second shows the 11 new Contents links (each `- [Name](#slug)`) plus the pre-existing `[Supervisor and Autonomous Track](#supervisor-and-autonomous-track)` link from the "How It Works" intro — 12 total matches.

- [ ] **Step 4: Full read-through**

Read the complete `README.md` top to bottom. Confirm: the Contents list order matches the actual heading order in the file, no Markdown breakage (list renders as a flat bullet list, blank lines around the new section match the surrounding style), and clicking through each link would land on the right heading (verify by eye — line-by-line correspondence between the TOC and the `grep -n '^## '` output).

- [ ] **Step 5: Commit**

```bash
git add README.md
git commit -m "$(cat <<'EOF'
docs: добавить содержание (TOC) в README
EOF
)"
```
