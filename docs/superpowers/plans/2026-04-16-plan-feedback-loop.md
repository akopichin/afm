# Plan Feedback Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `flowmanager revise <stage-id> --feedback "..."` command and update the flowmanager skill so plan approvals loop through user feedback until approved.

**Architecture:** New `cmd/flowmanager/revise.go` command re-runs the planning executor with original plan + feedback, writes a new `plan.md`, and sets status back to `awaiting_approval`. The flowmanager skill Step 3 is updated to wait for user chat input instead of a binary approve/reject, passing feedback to `revise` and looping until the user approves.

**Tech Stack:** Go (cobra, existing executor/state/config packages), SKILL.md (markdown)

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `cmd/flowmanager/revise.go` | Create | `revise` command + `buildRevisionPrompt` + `nextRevisionNumber` |
| `cmd/flowmanager/revise_test.go` | Create | Unit tests for helper functions |
| `cmd/flowmanager/main.go` | Modify | Register `newReviseCmd()` |
| `~/.claude/skills/flowmanager/SKILL.md` | Modify | Replace Step 3 approval flow |

---

### Task 1: Unit tests for `revise.go` helpers

**Files:**
- Create: `cmd/flowmanager/revise_test.go`

- [x] **Step 1: Write failing tests for `buildRevisionPrompt` and `nextRevisionNumber`**

Create `cmd/flowmanager/revise_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildRevisionPrompt(t *testing.T) {
	got := buildRevisionPrompt("TEMPLATE", "# Old Plan\n\nstep 1", "add rollback section")
	if !strings.Contains(got, "TEMPLATE") {
		t.Error("missing planning template")
	}
	if !strings.Contains(got, "# Old Plan") {
		t.Error("missing current plan")
	}
	if !strings.Contains(got, "add rollback section") {
		t.Error("missing feedback")
	}
	if !strings.Contains(got, "## Current Plan") {
		t.Error("missing Current Plan header")
	}
	if !strings.Contains(got, "## Feedback") {
		t.Error("missing Feedback header")
	}
}

func TestNextRevisionNumber(t *testing.T) {
	dir := t.TempDir()

	// No logs yet → first revision is 1
	if got := nextRevisionNumber(dir); got != 1 {
		t.Errorf("want 1, got %d", got)
	}

	// Create two revision logs
	os.WriteFile(filepath.Join(dir, "planning-revision-1.log"), []byte("x"), 0644) //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "planning-revision-2.log"), []byte("x"), 0644) //nolint:errcheck

	if got := nextRevisionNumber(dir); got != 3 {
		t.Errorf("want 3, got %d", got)
	}
}

func TestNextRevisionNumberNonExistentDir(t *testing.T) {
	// Should not panic, should return 1
	got := nextRevisionNumber("/tmp/flowmanager-nonexistent-dir-xyz")
	if got != 1 {
		t.Errorf("want 1, got %d", got)
	}
}
```

- [x] **Step 2: Run tests — verify they fail (functions not defined yet)**

```bash
cd /Users/alexander.kopichin/work/flowManager && go test ./cmd/flowmanager/... 2>&1 | head -20
```

Expected: compile error `undefined: buildRevisionPrompt` or similar.

- [x] **Step 3: Commit failing tests**

```bash
cd /Users/alexander.kopichin/work/flowManager
git add cmd/flowmanager/revise_test.go
git commit -m "test: тесты для хелперов команды revise"
```

---

### Task 2: Implement `cmd/flowmanager/revise.go`

**Files:**
- Create: `cmd/flowmanager/revise.go`

- [x] **Step 1: Create `revise.go` with full implementation**

```go
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/state"
)

func newReviseCmd() *cobra.Command {
	var feedback string

	cmd := &cobra.Command{
		Use:   "revise <stage-id>",
		Short: "Revise a stage plan with user feedback (re-runs planning agent)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			stageID := args[0]

			if feedback == "" {
				data, err := io.ReadAll(os.Stdin)
				if err != nil {
					return fmt.Errorf("read stdin: %w", err)
				}
				feedback = strings.TrimSpace(string(data))
			}
			if feedback == "" {
				return fmt.Errorf("feedback is required (use --feedback or stdin)")
			}

			stateFile, err := findLatestStateFile()
			if err != nil {
				return err
			}
			rs, err := state.Load(stateFile)
			if err != nil {
				return fmt.Errorf("load state: %w", err)
			}
			st, ok := rs.Stages[stageID]
			if !ok {
				return fmt.Errorf("stage %q not found", stageID)
			}
			if st.Status != state.StatusAwaitingApproval {
				return fmt.Errorf("stage %q is %v, not awaiting_approval", stageID, st.Status)
			}

			runDir := filepath.Dir(stateFile)
			stageDir := filepath.Join(runDir, stageID)
			planFile := filepath.Join(stageDir, "plan.md")

			planData, err := os.ReadFile(planFile)
			if err != nil {
				return fmt.Errorf("no plan found for stage %q: %w", stageID, err)
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			prompts, err := loadPrompts(cfg.PromptsDir)
			if err != nil {
				return err
			}

			n := nextRevisionNumber(stageDir)
			logFile := filepath.Join(stageDir, fmt.Sprintf("planning-revision-%d.log", n))
			prompt := buildRevisionPrompt(prompts.Planning, string(planData), feedback)

			exec := executor.New(executor.Config{
				Command:     cfg.Client.Command,
				ExtraArgs:   cfg.Client.ExtraArgs,
				IdleTimeout: cfg.Executor.IdleTimeout,
			})

			rs.SetStageStatus(stageID, state.StatusPlanning)
			if err := rs.Save(stateFile); err != nil {
				return fmt.Errorf("save state: %w", err)
			}

			if err := exec.RunPlanning(context.Background(), prompt, planFile, logFile); err != nil {
				rs.SetStageStatus(stageID, state.StatusFailed)
				rs.Save(stateFile) //nolint:errcheck
				return fmt.Errorf("revise planning: %w", err)
			}

			rs.SetStageStatus(stageID, state.StatusAwaitingApproval)
			if err := rs.Save(stateFile); err != nil {
				return fmt.Errorf("save state: %w", err)
			}

			fmt.Printf("revised plan for stage %q: awaiting approval\n", stageID)
			return nil
		},
	}
	cmd.Flags().StringVar(&feedback, "feedback", "", "feedback text for plan revision")
	return cmd
}

func buildRevisionPrompt(planningTemplate, currentPlan, feedback string) string {
	return fmt.Sprintf(
		"%s\n\n## Current Plan\n\n%s\n\n## Feedback\n\n%s\n\nPlease revise the plan taking the feedback into account. Output ONLY the revised plan markdown.",
		planningTemplate, currentPlan, feedback,
	)
}

func nextRevisionNumber(stageDir string) int {
	entries, err := os.ReadDir(stageDir)
	if err != nil {
		return 1
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "planning-revision-") && strings.HasSuffix(e.Name(), ".log") {
			n++
		}
	}
	return n + 1
}
```

- [x] **Step 2: Run tests — verify they pass**

```bash
cd /Users/alexander.kopichin/work/flowManager && go test ./cmd/flowmanager/... -v -run "TestBuildRevisionPrompt|TestNextRevisionNumber" 2>&1
```

Expected:
```
--- PASS: TestBuildRevisionPrompt
--- PASS: TestNextRevisionNumber
--- PASS: TestNextRevisionNumberNonExistentDir
PASS
```

- [x] **Step 3: Run lint**

```bash
cd /Users/alexander.kopichin/work/flowManager && golangci-lint run ./cmd/flowmanager/... 2>&1
```

Expected: no errors.

- [x] **Step 4: Commit**

```bash
cd /Users/alexander.kopichin/work/flowManager
git add cmd/flowmanager/revise.go
git commit -m "feat: команда revise для перепланирования стейджа с фидбеком"
```

---

### Task 3: Register `revise` in `main.go`

**Files:**
- Modify: `cmd/flowmanager/main.go`

- [x] **Step 1: Add `newReviseCmd()` to root command**

In `cmd/flowmanager/main.go`, replace:

```go
	root.AddCommand(
		newRunCmd(),
		newCheckCmd(),
		newApproveCmd(),
		newInitCmd(),
		newListCmd(),
		newResumeCmd(),
	)
```

With:

```go
	root.AddCommand(
		newRunCmd(),
		newCheckCmd(),
		newApproveCmd(),
		newReviseCmd(),
		newInitCmd(),
		newListCmd(),
		newResumeCmd(),
	)
```

- [x] **Step 2: Verify binary builds**

```bash
cd /Users/alexander.kopichin/work/flowManager && go build ./cmd/flowmanager/... 2>&1
```

Expected: no output (success).

- [x] **Step 3: Verify command shows up in help**

```bash
cd /Users/alexander.kopichin/work/flowManager && go run ./cmd/flowmanager/... --help 2>&1
```

Expected output includes:
```
  revise      Revise a stage plan with user feedback (re-runs planning agent)
```

- [x] **Step 4: Run all tests**

```bash
cd /Users/alexander.kopichin/work/flowManager && go test ./... 2>&1
```

Expected: all PASS.

- [x] **Step 5: Commit**

```bash
cd /Users/alexander.kopichin/work/flowManager
git add cmd/flowmanager/main.go
git commit -m "feat: регистрация команды revise в main"
```

---

### Task 4: Update flowmanager skill

**Files:**
- Modify: `~/.claude/skills/flowmanager/SKILL.md`

- [x] **Step 1: Read current Step 3**

Read `~/.claude/skills/flowmanager/SKILL.md` to get the exact content of Step 3 for replacement.

- [x] **Step 2: Replace Step 3**

Replace the current Step 3 block:

```markdown
## Step 3: Monitor and Handle Approvals

Poll the latest `state.json` every 3 seconds:

```bash
cat .flowManager/runs/$(ls -t .flowManager/runs/ | head -1)/state.json
```

When a stage shows `"status": "awaiting_approval"`:
1. Read its plan: `cat .flowManager/runs/{run}/{stage-id}/plan.md`
2. Show plan to user via AskUserQuestion: "Stage '{name}' plan is ready. Approve?"
3. On approval: `flowmanager approve {stage-id}`
4. Continue polling
```

With:

```markdown
## Step 3: Monitor and Handle Approvals

Poll the latest `state.json` every 3 seconds:

```bash
cat .flowManager/runs/$(ls -t .flowManager/runs/ | head -1)/state.json
```

When a stage shows `"status": "awaiting_approval"`:
1. Note the plan path: `.flowManager/runs/{run}/{stage-id}/plan.md`
2. Ask via AskUserQuestion:
   > "Plan for stage `{stage-id}` is ready at `.flowManager/runs/{run}/{stage-id}/plan.md`.
   > Review it and reply **ok** to approve, or write your feedback."
3. If response matches approval (`ok` / `да` / `approve` / `yes` / `lgtm`):
   - Run: `flowmanager approve {stage-id}`
4. If response is feedback text:
   - Run: `flowmanager revise {stage-id} --feedback "{response}"`
   - Go back to step 2 for the same stage (plan was revised, awaiting new review)
5. Continue polling for other stages
```

- [x] **Step 3: Verify skill file is valid markdown**

```bash
cat ~/.claude/skills/flowmanager/SKILL.md
```

Visually verify the new Step 3 is correctly formatted, no broken code fences.

- [x] **Step 4: Commit**

```bash
cd ~/.claude/skills/flowmanager
git add SKILL.md 2>/dev/null || true
# If not a git repo, just confirm the file is saved
echo "Skill updated"
```

---

### Task 5: Smoke test end-to-end

**Files:** None (verification only)

- [x] **Step 1: Build final binary**

```bash
cd /Users/alexander.kopichin/work/flowManager && go build -o /tmp/flowmanager-test ./cmd/flowmanager/... 2>&1
```

Expected: no errors.

- [x] **Step 2: Verify `revise --help`**

```bash
/tmp/flowmanager-test revise --help 2>&1
```

Expected:
```
Revise a stage plan with user feedback (re-runs planning agent)

Usage:
  flowmanager revise <stage-id> [flags]

Flags:
      --feedback string   ...
  -h, --help              help for revise
```

- [x] **Step 3: Run full test suite**

```bash
cd /Users/alexander.kopichin/work/flowManager && go test ./... 2>&1
```

Expected: all PASS, no failures.

- [x] **Step 4: Run lint on entire project**

```bash
cd /Users/alexander.kopichin/work/flowManager && golangci-lint run ./... 2>&1
```

Expected: no errors.

- [x] **Step 5: Final commit if any cleanup needed**

```bash
cd /Users/alexander.kopichin/work/flowManager
git status
# commit any remaining changes
```
