Domain: assembling the system prompt for a single agent phase. Audience: `pkg/orchestrator`.

## Building a prompt

```go
text := prompts.Build(prompts.Inputs{
    Template:        tmpl,
    Stage:           stage,          // flow.Stage
    PhaseAgent:      "planning",     // or "implementation" / "review"
    DependencyPlans: depPlans,
    Artifacts:       artifactsSection,
    Plan:            currentPlan,
    StageDir:        stageDir,       // enables AFM_STAGE_DIR dialog protocol for this phase
    Interactive:     stage.Interactive,
})
```

## Validating a plan before accepting it

```go
issues := prompts.ValidatePlan(planMarkdown, requiredSections)
if !issues.IsClean() {
    // issues.MissingSections lists what's absent — feed back into a re-prompt
    reprompt := prompts.EscapeTagsForReprompt(feedbackText)
}
```

`ValidatePlan`/`PlanIssues` is the sole gate `pkg/orchestrator` uses to decide whether a stage's
planning output is acceptable — a plan is never rejected for reasons outside the checked section list.
