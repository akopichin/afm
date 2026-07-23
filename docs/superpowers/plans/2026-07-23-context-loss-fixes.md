# Context-loss fixes: retry-context from raw jsonl + missing-dependency-plan warning

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/superpowers/specs/2026-07-23-context-loss-audit-design.md`

**Goal:** Two independent bug fixes found during a context-loss audit of afm's inter-stage handoff. (1) `buildRetryContext` currently reads the human-readable `.log` file, whose per-action detail is truncated by `executor.Config.TruncateOutput` — a retried stage sees an abbreviated version of its own prior work instead of the full untruncated action. (2) `CollectDependencyPlans` silently degrades to a placeholder string when a dependency's `plan.md`/`execution_summary.md` is missing or empty, with no signal to the operator.

**Architecture:** Two independent tasks, no shared files, can be done in either order or in parallel.
- Task 1 touches `pkg/executor` (new `RenderActions` helper) and `pkg/orchestrator/retry.go` (`buildRetryContext` source file switch).
- Task 2 touches `pkg/orchestrator/bus.go` (new event type), `pkg/orchestrator/context.go` (`CollectDependencyPlans` signature), `pkg/orchestrator/agents.go` (5 call sites), and the dashboard frontend (`EventFeedPanel.tsx` + `event-feed.css` in both `skins/` and `public/skins/`).

**Tech Stack:** Go, `go test`, existing table-driven/temp-dir test conventions in `pkg/executor` and `pkg/orchestrator`; React/TSX + plain CSS for the dashboard (no build step touches the two static CSS copies — both are edited by hand and must stay identical).

## Global Constraints

- Commit messages in Russian, no `Co-Authored-By`.
- Do not touch the `TruncateOutput` truncation applied to `.log`/`agent_action`/dashboard — that is working as designed (reduces log/event-feed noise). Only the retry-context *source* changes.
- `EventContextWarning` goes through `o.ui.Publish` (transient dashboard bus), NOT `o.critical`/`state.Store` — it is not part of the durable event log, matching the existing precedent for non-fatal signals (`retry.go:123-127`'s "incomplete work, retrying" message via `EventStageStatusChanged`).
- `pkg/web/dashboard/skins/base/event-feed.css` and `pkg/web/dashboard/public/skins/base/event-feed.css` are today byte-identical with no sync step in the repo — edit both, verify they stay identical (`diff` the two after editing).
- Do not use `executor.ParseToolAction` for the new `RenderActions` helper — it only returns the FIRST content block of a stream-json line and would silently drop additional tool calls/text in the same assistant message. Iterate `ev.Message.Content` directly, calling the unexported `contentToAction(c, limit)` per block, mirroring the existing loops inside `RunPlanning`/`RunAgent`.

---

## File Structure

- `pkg/executor/retry_context.go` (new) — `RenderActions(jsonlPath string) []string`.
- `pkg/executor/retry_context_test.go` (new) — tests for `RenderActions`.
- `pkg/orchestrator/retry.go` — `buildRetryContext` reads `.jsonl` via `executor.RenderActions` instead of `.log`.
- `pkg/orchestrator/retry_test.go` (new) — unit tests for `buildRetryContext`.
- `pkg/orchestrator/bus.go` — `EventContextWarning` event type.
- `pkg/orchestrator/context.go` — `CollectDependencyPlans` gains a `warn func(depID, msg string)` parameter.
- `pkg/orchestrator/context_test.go` (new) — unit test asserting `warn` is invoked on missing/empty plan.
- `pkg/orchestrator/agents.go` — 5 call sites pass a `warn` closure that publishes `EventContextWarning`.
- `pkg/web/dashboard/src/components/event-feed/EventFeedPanel.tsx` — new `case 'context_warning'` in `toFeedLine`.
- `pkg/web/dashboard/skins/base/event-feed.css` and `pkg/web/dashboard/public/skins/base/event-feed.css` — `.feed-msg.warning` style.

No files are deleted.

---

### Task 1: retry-context reads raw `.jsonl` instead of truncated `.log`

**Files:**
- Create: `pkg/executor/retry_context.go`, `pkg/executor/retry_context_test.go`
- Modify: `pkg/orchestrator/retry.go`
- Create: `pkg/orchestrator/retry_test.go`

**Interfaces:**
- Produces: `executor.RenderActions(jsonlPath string) []string` — one string per tool/text action, untruncated, in file order. Missing/unreadable file → `nil`.
- Consumes: nothing new (only existing unexported `parseStreamEvent`/`contentToAction` inside `pkg/executor`).

- [ ] **Step 1: Write the failing test for `RenderActions`**

In `pkg/executor/retry_context_test.go` (package `executor_test`, mirrors `TestWrittenFiles`'s style):

```go
package executor_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/executor"
)

func TestRenderActions(t *testing.T) {
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "implementation.jsonl")
	longText := strings.Repeat("x", 500)
	longCmd := strings.Repeat("echo hi; ", 50)
	lines := []string{
		fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":"%s"}]}}`, longText),
		fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"%s"}}]}}`, longCmd),
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/tmp/a.md"}}]}}`,
		`{"type":"result","subtype":"success"}`,
	}
	if err := os.WriteFile(jsonl, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		t.Fatal(err)
	}

	got := executor.RenderActions(jsonl)
	if len(got) != 3 {
		t.Fatalf("RenderActions returned %d lines, want 3: %v", len(got), got)
	}
	if !strings.Contains(got[0], longText) {
		t.Errorf("text action truncated, want full %d-char text in %q", len(longText), got[0])
	}
	if !strings.Contains(got[1], longCmd) {
		t.Errorf("bash action truncated, want full command in %q", got[1])
	}
	if !strings.Contains(got[2], "/tmp/a.md") {
		t.Errorf("write action missing file path: %q", got[2])
	}
}

func TestRenderActionsMissingFile(t *testing.T) {
	if got := executor.RenderActions(filepath.Join(t.TempDir(), "nope.jsonl")); got != nil {
		t.Errorf("expected nil for missing file, got %v", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./pkg/executor/... -run TestRenderActions -v`
Expected: FAIL — compile error, `executor.RenderActions` undefined.

- [ ] **Step 3: Implement `RenderActions`**

Create `pkg/executor/retry_context.go`:

```go
package executor

import (
	"bufio"
	"fmt"
	"os"
)

// RenderActions parses a stage's raw stream-json log (<phase>.jsonl) and
// returns one line per tool/text action, WITHOUT the Config.TruncateOutput
// limit applied to <phase>.log — the log's detail is intentionally abbreviated
// for the dashboard/event feed, but a retry continuation prompt needs to see
// what the stage actually did in full. Missing or unreadable file returns nil.
func RenderActions(jsonlPath string) []string {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	// Stream-json lines carrying full Write-tool content easily exceed the
	// scanner's default 64KB limit (same reasoning as WrittenFiles).
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		ev, ok := parseStreamEvent(sc.Text())
		if !ok {
			continue
		}
		for _, c := range ev.Message.Content {
			if tool, detail, actionOK := contentToAction(c, 0); actionOK {
				lines = append(lines, fmt.Sprintf("%-6s  %s", tool, detail))
			}
		}
	}
	return lines
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./pkg/executor/... -v`
Expected: PASS, including `TestRenderActions` and `TestRenderActionsMissingFile`, and all pre-existing tests unaffected.

- [ ] **Step 5: Write the failing test for `buildRetryContext`**

Create `pkg/orchestrator/retry_test.go` (package `orchestrator`, internal — `buildRetryContext` is unexported):

```go
package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildRetryContext_FullActionNotTruncated(t *testing.T) {
	stageDir := t.TempDir()
	longOutput := strings.Repeat("output-line ", 100) // far longer than any sane truncate_output limit
	line := fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"%s"}}]}}`, longOutput)
	if err := os.WriteFile(filepath.Join(stageDir, "implementation.jsonl"), []byte(line+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got := buildRetryContext(stageDir, phaseImplementation)
	if !strings.Contains(got, longOutput) {
		t.Errorf("retry context does not contain full action text — got truncated/missing content: %q", got)
	}
}

func TestBuildRetryContext_MissingLogReturnsEmpty(t *testing.T) {
	stageDir := t.TempDir()
	if got := buildRetryContext(stageDir, phaseImplementation); got != "" {
		t.Errorf("expected empty context for missing jsonl, got %q", got)
	}
}
```

- [ ] **Step 6: Run the test to verify it fails**

Run: `go test ./pkg/orchestrator/... -run TestBuildRetryContext -v`
Expected: FAIL — `buildRetryContext` still reads `<phase>.log`, which doesn't exist in this test (only `.jsonl` was written), so the first test currently gets an empty context instead of the full action text.

- [ ] **Step 7: Switch `buildRetryContext`'s source file to `.jsonl`**

In `pkg/orchestrator/retry.go`, change:

```go
func buildRetryContext(stageDir, phase string) string {
	var logName string
	switch phase {
	case phasePlanning:
		logName = "planning.log"
	case phaseReview:
		logName = "review.log"
	case phaseAutonomous:
		logName = "autonomous.log"
	default:
		logName = "implementation.log"
	}

	data, err := os.ReadFile(filepath.Join(stageDir, logName))
	if err != nil {
		return ""
	}

	lines := strings.Split(string(data), "\n")
	const maxLines = 200
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
```

to:

```go
func buildRetryContext(stageDir, phase string) string {
	var jsonlName string
	switch phase {
	case phasePlanning:
		jsonlName = "planning.jsonl"
	case phaseReview:
		jsonlName = "review.jsonl"
	case phaseAutonomous:
		jsonlName = "autonomous.jsonl"
	default:
		jsonlName = "implementation.jsonl"
	}

	lines := executor.RenderActions(filepath.Join(stageDir, jsonlName))
	if len(lines) == 0 {
		return ""
	}
	const maxLines = 200
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
```

Add `"github.com/akopichin/afm/pkg/executor"` to the import block. Remove the now-unused `strings.Split`/`os.ReadFile` call from this function only if `strings`/`os` become unused elsewhere in the file — check with the build in Step 8 before removing any import (both packages are still used by the rest of `retry.go`, e.g. `os.Remove(sessionFile(...))`, so no import removal is expected).

The rest of the function (the `buf.WriteString` loop and trailing instructions) is unchanged — it already iterates `lines` and skips blanks, which still works since `RenderActions` entries are never empty strings.

- [ ] **Step 8: Run the tests to verify they pass**

Run: `go build ./... && go test ./pkg/orchestrator/... -v`
Expected: build succeeds; `TestBuildRetryContext_*` pass; all pre-existing orchestrator tests (including `integration_retry_test.go`'s `TestIntegration_Retry*`, which exercise `buildRetryContext` indirectly through real stub-agent runs that produce both `.log` and `.jsonl`) still pass.

- [ ] **Step 9: Full package sanity check**

Run: `go test ./pkg/executor/... ./pkg/orchestrator/... -v`
Expected: 0 failures.

- [ ] **Step 10: Commit**

```bash
git add pkg/executor/retry_context.go pkg/executor/retry_context_test.go pkg/orchestrator/retry.go pkg/orchestrator/retry_test.go
git commit -m "$(cat <<'EOF'
fix: retry-context теперь читает полный .jsonl, а не обрезанный .log

buildRetryContext брал последние 200 строк human-readable .log,
чей detail уже урезан executor.Config.TruncateOutput — при
маленьком truncate_output ретраящаяся стадия видела обрезанную
версию собственных прошлых действий. Источник переключён на
неурезанный .jsonl (executor.RenderActions).
EOF
)"
```

---

### Task 2: explicit warning when a dependency's plan is missing/empty

**Files:**
- Modify: `pkg/orchestrator/bus.go`
- Modify: `pkg/orchestrator/context.go`
- Create: `pkg/orchestrator/context_test.go`
- Modify: `pkg/orchestrator/agents.go`
- Modify: `pkg/web/dashboard/src/components/event-feed/EventFeedPanel.tsx`
- Modify: `pkg/web/dashboard/skins/base/event-feed.css`, `pkg/web/dashboard/public/skins/base/event-feed.css`

**Interfaces:**
- Produces: `orchestrator.EventContextWarning EventType = "context_warning"`; `CollectDependencyPlans(runDir string, stage flow.Stage, allStages []flow.Stage, warn func(depID, msg string)) string` (signature change — all 5 existing call sites in `agents.go` must be updated in the same commit or the package won't compile).

- [ ] **Step 1: Write the failing test for `CollectDependencyPlans`'s new parameter**

Create `pkg/orchestrator/context_test.go` (package `orchestrator`):

```go
package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
)

func TestCollectDependencyPlans_WarnsOnMissingPlan(t *testing.T) {
	runDir := t.TempDir()
	depDir := filepath.Join(runDir, "dep-stage")
	if err := os.MkdirAll(depDir, 0755); err != nil {
		t.Fatal(err)
	}
	// No plan.md written — simulates a dependency that hasn't produced one.

	stage := flow.Stage{ID: "s2", DependsOn: []string{"dep-stage"}}
	allStages := []flow.Stage{{ID: "dep-stage", Name: "Dep Stage"}, stage}

	var warned []string
	got := CollectDependencyPlans(runDir, stage, allStages, func(depID, msg string) {
		warned = append(warned, depID+": "+msg)
	})

	if !strings.Contains(got, "(plan not available)") {
		t.Errorf("prompt text should still contain placeholder, got %q", got)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "dep-stage") {
		t.Fatalf("expected exactly one warn() call naming dep-stage, got %v", warned)
	}
}

func TestCollectDependencyPlans_NoWarnWhenPlanPresent(t *testing.T) {
	runDir := t.TempDir()
	depDir := filepath.Join(runDir, "dep-stage")
	if err := os.MkdirAll(depDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(depDir, "plan.md"), []byte("## Tasks\n- do thing\n"), 0644); err != nil {
		t.Fatal(err)
	}

	stage := flow.Stage{ID: "s2", DependsOn: []string{"dep-stage"}}
	allStages := []flow.Stage{{ID: "dep-stage", Name: "Dep Stage"}, stage}

	var warned []string
	CollectDependencyPlans(runDir, stage, allStages, func(depID, msg string) {
		warned = append(warned, depID)
	})
	if len(warned) != 0 {
		t.Errorf("expected no warn() calls when plan is present, got %v", warned)
	}
}

func TestCollectDependencyPlans_NilWarnIsSafe(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s2", DependsOn: []string{"missing-dep"}}
	allStages := []flow.Stage{stage}

	got := CollectDependencyPlans(runDir, stage, allStages, nil)
	if !strings.Contains(got, "(plan not available)") {
		t.Errorf("expected placeholder text with nil warn callback, got %q", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./pkg/orchestrator/... -run TestCollectDependencyPlans -v`
Expected: FAIL — compile error, `CollectDependencyPlans` called with 4 arguments but declared to take 3.

- [ ] **Step 3: Add `EventContextWarning`**

In `pkg/orchestrator/bus.go`, add to the `EventType` const block:

```go
const (
	EventStageStatusChanged EventType = "stage_status_changed"
	EventAgentAction        EventType = "agent_action"
	EventAgentCompleted     EventType = "agent_completed"
	EventApproved           EventType = "approved"
	EventRetryScheduled     EventType = "retry_scheduled"
	EventRetryExhausted     EventType = "retry_exhausted"
	EventAskUser            EventType = "ask_user"
	EventUserAnswered       EventType = "user_answered"
	EventSupervisorDecision EventType = "supervisor_decision"
	EventContextWarning     EventType = "context_warning"
)
```

- [ ] **Step 4: Update `CollectDependencyPlans`'s signature**

In `pkg/orchestrator/context.go`:

```go
// CollectDependencyPlans reads plan.md from each stage in DependsOn
// and returns a formatted prompt section. Missing plans produce a warning
// comment in the prompt AND, if warn is non-nil, a callback invocation so the
// caller can surface a visible signal (e.g. an event-feed warning) — a
// missing dependency plan means the stage silently loses context otherwise.
func CollectDependencyPlans(runDir string, stage flow.Stage, allStages []flow.Stage, warn func(depID, msg string)) string {
	if len(stage.DependsOn) == 0 {
		return ""
	}

	nameIndex := make(map[string]string, len(allStages))
	for _, s := range allStages {
		nameIndex[s.ID] = s.Name
	}

	var buf strings.Builder
	buf.WriteString("\n\n## Context from dependent stages\n")

	for _, depID := range stage.DependsOn {
		stageDir := filepath.Join(runDir, depID)
		name := nameIndex[depID]
		if name == "" {
			name = depID
		}
		fmt.Fprintf(&buf, "\n### Stage: %s (%s)\n\n", name, depID)

		var data []byte
		if _, err := os.Stat(filepath.Join(stageDir, "autonomous.flag")); err == nil {
			data, _ = os.ReadFile(filepath.Join(stageDir, "execution_summary.md"))
		} else {
			data, _ = os.ReadFile(filepath.Join(stageDir, "plan.md"))
		}

		if len(data) == 0 {
			buf.WriteString("(plan not available)\n")
			if warn != nil {
				warn(depID, "dependency stage plan is missing or empty — downstream stage sees no context from it")
			}
			continue
		}
		buf.WriteString(string(data))
		buf.WriteString("\n")
	}

	return buf.String()
}
```

(Only the function signature and the `if len(data) == 0` branch change; the rest of the function and file — `resolveArtifactPath`, `CollectArtifacts` — are untouched.)

- [ ] **Step 5: Run the test to verify it fails differently (compile error gone, now update call sites)**

Run: `go build ./pkg/orchestrator/...`
Expected: FAIL — 5 call sites in `agents.go` still call `CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages)` with 3 arguments.

- [ ] **Step 6: Update the 5 call sites in `agents.go`**

In each of `runPlanningAgent`, `runPlanningWithFeedback`, `runImplementationAgent`, `runReviewAgent`, `runAutonomousAgent`, change:

```go
depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages)
```

to:

```go
depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages, func(depID, msg string) {
	o.ui.Publish(Event{Type: EventContextWarning, StageID: s.ID, Data: fmt.Sprintf("%s: %s", depID, msg)})
})
```

`runReviewAgent` calls it as `depPlans := CollectDependencyPlans(...)` at line ~267 (outside the `runWithRetry` closure, unlike the others) — same substitution applies there too, `o`/`s` are both in scope. `fmt` is already imported in `agents.go` (used elsewhere in the same file, e.g. `fmt.Sprintf` in `rePromptMissingSections`).

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go build ./... && go test ./pkg/orchestrator/... -v`
Expected: build succeeds; `TestCollectDependencyPlans_*` pass; all pre-existing orchestrator tests pass (none currently assert on `CollectDependencyPlans`'s old 3-arg form outside `agents.go`, per the audit).

- [ ] **Step 8: Render `context_warning` in the dashboard event feed**

In `pkg/web/dashboard/src/components/event-feed/EventFeedPanel.tsx`, `toFeedLine`, add a new case right after `case 'supervisor_decision': { ... }` and before `default:`:

```tsx
    case 'context_warning':
      msg = `context warning: ${stringify(data)}`
      msgClass = 'feed-msg warning'
      break
```

- [ ] **Step 9: Style the new class**

In `pkg/web/dashboard/skins/base/event-feed.css`, add right after the existing `.feed-msg.error { color: var(--coral); }` line:

```css
.feed-msg.warning { color: var(--amber); }
```

Make the identical edit in `pkg/web/dashboard/public/skins/base/event-feed.css` (same line, same position). After both edits, run `diff pkg/web/dashboard/skins/base/event-feed.css pkg/web/dashboard/public/skins/base/event-feed.css` — expect no output (files stay identical, matching their current state).

- [ ] **Step 10: Frontend sanity check**

Run: `cd pkg/web/dashboard && npm run build`
Expected: build succeeds (this project's Go build already runs this — see `make build` — so this step just verifies the TSX change type-checks and compiles before the full `make build`/commit-hook run does it again).

- [ ] **Step 11: Full-suite verification**

Run: `go build ./... && go test ./... `
Expected: 0 failures across all packages.

- [ ] **Step 12: Commit**

```bash
git add pkg/orchestrator/bus.go pkg/orchestrator/context.go pkg/orchestrator/context_test.go pkg/orchestrator/agents.go pkg/web/dashboard/src/components/event-feed/EventFeedPanel.tsx pkg/web/dashboard/skins/base/event-feed.css pkg/web/dashboard/public/skins/base/event-feed.css
git commit -m "$(cat <<'EOF'
feat: явное предупреждение в event-фиде при отсутствующем плане зависимости

CollectDependencyPlans молча деградировала до "(plan not available)"
без какого-либо сигнала наружу — оператор мог узнать о потере
контекста только вручную прочитав промпт следующей стадии. Добавлен
context_warning event, публикуемый через o.ui при отсутствующем или
пустом plan.md/execution_summary.md зависимой стадии, и отдельный
стиль в дашборде.
EOF
)"
```

---

## Final Verification (after both tasks)

- [ ] Run: `make lint` (or the project's pre-commit hook equivalent) — expect 0 issues.
- [ ] Run: `go test ./...` — expect 0 failures.
- [ ] Confirm both spec acceptance criteria are met:
  1. `buildRetryContext` returns full (untruncated) prior-action text regardless of `executor.truncate_output`.
  2. A missing/empty dependency plan produces a visible, distinctly-styled `context_warning` entry in the event feed.
