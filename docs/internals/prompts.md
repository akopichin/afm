# Prompts

All prompts are assembled in `pkg/prompts/builder.go` by the `Build()` function. The
templates themselves live under `assets/prompts/` and can be overridden per project
via the `prompts_dir` config option. This page documents each prompt type — its
structure and the default template content.

## Assembly structure (`Build`)

```
<system_rules>
  {Template}                    ← planning.md / implementation.md / review.md
  {OutputContractMD}            ← planning only: "## Output Contract (mandatory)..."
  [<interactive_rules>]         ← only if stage.Interactive = true
</system_rules>

[<context>
  [<dependency_plans>]          ← plans of stages from depends_on
  [<artifacts>]                 ← artifact files from dependencies
</context>]

<stage id="..." name="...">
  <description>...</description>
  [<skills>...</skills>]
</stage>

[<prompt>...]                   ← stage.Prompt from flow.yaml
[<plan>...]                     ← plan.md (implementation/review)
[<previous_plan>...]            ← plan.vN.md (planning with feedback)
[<feedback>...]                 ← feedback.md (planning with feedback)
[{RetryContext}]                ← context on retry
```

The contents of `<description>`, `<prompt>`, `<plan>`, `<previous_plan>`,
`<feedback>` and `<example_output>` pass through `escapeTags()` — XML tags are
neutralized to prevent prompt injection.

## 1. Planning agent

**When:** the first run of a stage → creates `plan.md`.

Template (`assets/prompts/planning.md`):

```
# Planning Agent

You are a planning agent. Your task is to create a detailed implementation plan for the stage described below.

## Output Contract (mandatory)

The plan MUST be markdown with these top-level sections (exact names):
- `## Tasks` — numbered checkboxes with concrete, actionable steps.
- `## Assumptions` — every non-obvious choice. Use `- none` if no assumptions.
- `## Acceptance Criteria` — checkboxes for verifiable behavior.

Any missing section will cause the stage to be re-prompted once, then failed.

## Rules

- Do NOT ask questions. Make decisions autonomously.
- Do NOT propose interactive workflows or browser previews.
- Do NOT wait for approval. Produce the complete plan in one go.
- Output ONLY the plan markdown — no preamble, no explanation.
```

An additional `planningContract` block is appended after the template, restating the
required sections.

## 2. Planning with feedback (Revise)

**When:** the user clicked Revise → `plan.md` is recreated taking the notes into
account. Same as Planning, plus the previous plan (`plan.vN.md`) and the feedback
(`feedback.md`) are added to the context.

## 3. Planning re-prompt (missing sections)

**When:** a plan is missing a required section → a single re-prompt. This is **not**
built through `Build()` — it's a plain string that asks the agent to add only the
missing sections to the existing plan, without rewriting the rest.

## 4. Implementation agent

**When:** after the plan is approved → executes the tasks.

Template (`assets/prompts/implementation.md`):

```
# Implementation Agent

You are an implementation agent. Execute the implementation plan provided in `<plan>`.

## Output Contract (mandatory)

When ALL work is complete:
1. Verify all `## Tasks` checkboxes from the plan are done.
2. If the plan defines success criteria or blocking conditions, re-run the checks and confirm EVERY one of them passes.
3. If `<Required Artifacts>` section appears, every listed file MUST exist at the EXACT path shown.
4. Create a `.done` file in the stage directory with a brief summary of what was accomplished.

Without `.done` the stage is treated as incomplete (one retry, then failed).
Missing declared artifact fails the stage immediately.

## No Self-Justified Completion

NEVER create `.done` while any success criterion from the plan is unmet.
Excuses do not change the result: "pre-existing failure", "unrelated to this stage",
"out of scope", "was already broken before me" — none of these turn a failed
criterion into a passed one. If the plan says "all tests must pass" and any test
fails, the criterion is unmet, period.

If a criterion cannot be met, do NOT write `.done`. Instead write `report.md`
in the stage directory describing exactly what is unmet, the evidence (command
output), and what you tried. Let the stage fail honestly — a false "done" is
worse than a failed stage.

## Process

Work task by task. Run tests after each. Commit after each completed task.
Follow TDD: write tests first.
```

If the stage declares `artifacts:` in flow.yaml, a "Required output artifacts" list
(name — description → path) is appended to the artifacts context.

## 5. Review agent

**When:** after implementation (if the stage has `review` in `agents`) or as a
standalone stage.

Template (`assets/prompts/review.md`):

```
# Review Agent

Review the changes made during the implementation stage.

## Output Contract (mandatory)

Output MUST contain these sections (exact names):
- `## Verdict` — `approved` or `needs_changes` (one word).
- `## Critical issues` — blockers, or `- none`.
- `## Suggestions` — non-blocking improvements, or `- none`.

## What to review

- Correctness: matches the plan?
- Code quality: clean, readable, well-structured?
- Test coverage: adequate?
- Edge cases: error conditions handled?
```

## 6. Summary agent

**When:** the final stage of a flow.

Template (`assets/prompts/summary.md`):

```
# Summary Agent

Produce the final report for the completed flow run.

## Output Contract (mandatory)

Output MUST contain these sections:
- `## Summary` — one paragraph overview.
- `## Per stage` — bullet list `- <stage>: <what happened>`.
- `## Issues` — concerns from review phase, or `- none`.

Read implementation and review logs from each stage in the run directory.
```

## 7. Interactive rules

Added to `system_rules` when `stage.Interactive = true`:

```xml
<interactive_rules>
Use the file-based dialog protocol to ask the user questions.
Your stage directory is AFM_STAGE_DIR="{stageDir}"
Assign sequential IDs: q1, q2, … (never reuse an ID within a phase).

For each question:
1. Write the question file using the Write tool:
   Path: {stageDir}/{phase}.q<N>.question.json  (== $AFM_STAGE_DIR/{phase}.q<N>.question.json)
   Write the file to this path and NOWHERE ELSE. Do NOT invent paths like .afm/stages/... — always use $AFM_STAGE_DIR.
   Content: {"id":"qN","question":"## Full context here\n\nYour question?","options":["A","B"],"allow_custom":true}
   Put ALL context in 'question': descriptions, trade-offs, examples. Use markdown freely.
2. Wait for the answer via a blocking Bash polling loop:
   while [ ! -f "$AFM_STAGE_DIR/{phase}.qN.answer.json" ]; do sleep 15; done && cat "$AFM_STAGE_DIR/{phase}.qN.answer.json"
3. The polling loop may be cut short by a command timeout after a few minutes — this is EXPECTED and is NOT a signal to stop.
   When that happens, immediately run the EXACT SAME polling-loop command again. Keep re-launching it until the answer file appears.
4. You MUST keep waiting until $AFM_STAGE_DIR/<phase>.qN.answer.json exists. Do NOT end your turn, do NOT stop, do NOT write "I'll wait" and return control.
5. Do NOT use ScheduleWakeup, background tasks, async waits, or "wait for a notification" — those mechanisms DO NOT EXIST here. The ONLY way to receive the user's answer is the blocking Bash polling loop above.
6. Do NOT write to plan.md / output artifact yet — finish waiting for and processing all answers first, then produce the artifact in one go.
Ask ONE question at a time.
</interactive_rules>
```
