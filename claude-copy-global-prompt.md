# Implementation Plan: claude-copy-global-prompt

## Tasks

1. [ ] **Verify implementation is complete**
   - Run `go build ./...` - all packages must compile without errors
   - Run `go vet ./...` - static analysis must pass
   - Run `go test ./pkg/flow/ ./pkg/prompts/ ./pkg/orchestrator/` - all tests must pass
   - Run `go test -race ./pkg/flow/ ./pkg/prompts/ ./pkg/orchestrator/` - race detector must pass
   - Run `gofmt -l .` - should produce no output (all code properly formatted)
   - Run `goga lint` - should report 17 cells, 0 errors

2. [ ] **Verify contract compliance**
   - Confirm `pkg/flow/CODEMANIFEST` declares `prompt string` property for `Flow` entity
   - Confirm `pkg/prompts/CODEMANIFEST` declares `globalPrompt string` property for `Inputs` entity and algorithm step 1b for `Build` routine
   - Confirm `pkg/orchestrator/CODEMANIFEST` declares `globalPrompt string` property for `Options` entity and clarifies all 5 call sites forward it
   - Confirm `cmd/afm/CODEMANIFEST` documents `Flow.Prompt` → `Options.GlobalPrompt` wiring
   - Verify all 4 CODEMANIFEST files are present and syntactically valid

3. [ ] **Verify data flow implementation**
   - Trace `flow.yaml` → `yaml.Unmarshal` → `Flow.Prompt` via struct tag `yaml:"prompt"`
   - Trace `Flow.Prompt` → `Options.GlobalPrompt` at `cmd/afm/run.go:174`
   - Trace `Options.GlobalPrompt` → all 5 `prompts.Build` call sites in `orchestrator.go` (lines 937, 1033, 1109, 1127, 1165)
   - Trace `Inputs.GlobalPrompt` → `<global_prompt>` block rendering in `pkg/prompts/builder.go:75-79`
   - Confirm `escapeTags` function neutralizes tag injection attacks

4. [ ] **Verify backward compatibility**
   - Run `TestBuild_Golden_PlanningSimple` - confirms empty `GlobalPrompt` produces pre-change output
   - Run `TestBuild_NoGlobalPromptBlock_WhenEmpty` - confirms no `<global_prompt>` tag when empty
   - Run `TestParseRootPromptEmpty` - confirms absent YAML key yields zero value

5. [ ] **Verify tag injection protection**
   - Run `TestBuild_GlobalPromptEscapesOwnClosingTag` - confirms injected `</global_prompt>` is escaped
   - Verify `escapeTags` inserts zero-width space (U+200B) after all 12 known tag delimiters
   - Confirm no real tag injection can escape the block

6. [ ] **Verify end-to-end integration**
   - Run `TestIntegration_GlobalPromptReachesAssembledPrompt` - confirms full chain works
   - Verify `Options.GlobalPrompt` → `o.opts.GlobalPrompt` → `prompts.Inputs.GlobalPrompt` → `Build` → agent process

7. [ ] **Optional: Update `pkg/flow/.usages/flow_facade.md`** (DEFERRED - non-blocking)
   - Add subsection "## Reading the root prompt"
   - Document that `f.Prompt` is forwarded to `orchestrator.Options.GlobalPrompt`
   - Note that empty value means "no root prompt", not an error
   - This is documentation-only; no code changes required

8. [ ] **Optional: Update `pkg/orchestrator/.usages/orchestrator_facade.md`** (DEFERRED - non-blocking)
   - Locate `Options{...}` code snippet in "Starting an orchestrator (cmd/afm)" section
   - Add `GlobalPrompt: f.Prompt,` to the snippet (next to existing `ProxyURL`/`ProxyShimDir` fields)
   - This is documentation-only; no code changes required

9. [ ] **Final validation**
   - Confirm all tests pass: `go test ./pkg/flow/ ./pkg/prompts/ ./pkg/orchestrator/`
   - Confirm no unintended file changes: `git diff --stat` should show only expected changes
   - Confirm `docs/arch/claude-copy-global-prompt.md` and `docs/tasks/claude-copy-global-prompt.md` are unmodified
   - Mark implementation as COMPLETE

## Assumptions

- **Implementation is already complete** - The design review stage confirmed all 4 CODEMANIFEST changes are implemented and verified in the working tree
- **All tests pass** - `go build`, `go vet`, `go test`, and `goga lint` all pass without errors
- **Backward compatibility is preserved** - Flows without a `prompt:` key produce byte-identical output to pre-change behavior
- **Documentation tasks are optional** - Tasks 7 and 8 are low-priority, documentation-only updates that can be deferred indefinitely without affecting functionality
- **No new cells are created** - This feature only adds properties to existing entities within the 4 existing cells
- **Tag injection protection is adequate** - The existing `escapeTags` mechanism (now covering 12 tag names including `global_prompt`) sufficiently neutralizes injection attacks
- **No concurrent write hazards** - `Options.GlobalPrompt` is set once at construction and never mutated, so concurrent reads across stage goroutines require no synchronization
- **Design document is accurate** - The traces, diagrams, and test scenarios documented in `docs/design/claude-copy-global-prompt.md` match the actual implementation

## Acceptance Criteria

- [ ] All 4 CODEMANIFEST files (`pkg/flow`, `pkg/prompts`, `pkg/orchestrator`, `cmd/afm`) declare the new `prompt`/`globalPrompt` properties correctly
- [ ] All 5 `prompts.Build` call sites in `orchestrator.go` forward `GlobalPrompt: o.opts.GlobalPrompt`
- [ ] `Flow.Prompt` is decoded from YAML via struct tag `yaml:"prompt"` without custom unmarshaling
- [ ] `Build` renders `<global_prompt>{escaped text}</global_prompt>` only when `in.GlobalPrompt != ""`
- [ ] Block placement is correct: after `</system_rules>`, before `<context>`/`<stage>`
- [ ] `escapeTags` function neutralizes all 12 known tag delimiters including `global_prompt`
- [ ] All 6 test scenarios pass (`TestParseRootPrompt`, `TestBuild_Golden_PlanningSimple`, `TestBuild_NoGlobalPromptBlock_WhenEmpty`, `TestBuild_GlobalPromptBlockAppears`, `TestBuild_GlobalPromptEscapesOwnClosingTag`, `TestIntegration_GlobalPromptReachesAssembledPrompt`)
- [ ] Backward compatibility is maintained: flows without `prompt:` produce identical output to before the change
- [ ] All validation commands pass: `go build ./...`, `go vet ./...`, `go test ./pkg/flow/ ./pkg/prompts/ ./pkg/orchestrator/`, `gofmt -l .`, `goga lint`
- [ ] No package boundaries were expanded (no new cells created)
- [ ] Implementation is complete and functional (tasks 1-6 verified)
- [ ] Optional documentation tasks (7-8) are explicitly marked as DEFERRED and non-blocking
