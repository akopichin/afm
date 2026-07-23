# Design: configurable `executor.truncate_output`

## Goal

Agent tool-action output (text blocks, Bash commands) is currently truncated
at hardcoded lengths (100 chars for text, 80 chars for Bash/other tool
details) before being written to `<phase>.log` and published as the
`agent_action` event's `detail` field — this is permanent, not just a display
convenience (the full-screen dashboard view and the API don't recover it;
only the raw `<phase>.jsonl` stream retains the untruncated original). Make
this configurable, defaulting to no truncation at all.

## Background

Root cause investigated this session: `pkg/executor/executor.go`'s
`contentToAction` (lines ~161-206) hardcodes:
- text content blocks: `if len(d) > 100 { d = d[:100] + "..." }`
- Bash commands: `if len(cmd) > 80 { cmd = cmd[:80] + "..." }`
- other tool_use fallback detail: `if len(d) > 80 { d = d[:80] + "..." }`

`Write`/`Edit`/`Read`/`Glob`/`Grep` log only a file path and are never
truncated today — no change needed there.

## Config field

Add to `ExecutorConfig` in `pkg/config/config.go`:

```go
type ExecutorConfig struct {
    IdleTimeout    time.Duration `yaml:"idle_timeout"`
    MaxParallel    int           `yaml:"max_parallel"`
    TruncateOutput int           `yaml:"truncate_output"`
}
```

- Default: Go zero value (`0`) = no truncation. No change needed to
  `DefaultConfig()`.
- Merge/override (project config over global config) follows the exact
  existing pattern used for `MaxParallel`:
  `if overlay.Executor.TruncateOutput != 0 { dst.Executor.TruncateOutput = overlay.Executor.TruncateOutput }`.
  This has the same known limitation as `MaxParallel` (an overlay can't
  explicitly force back to `0`/unlimited if the base config set a nonzero
  value) — accepted for consistency with the existing convention rather than
  introducing pointer semantics (`*int`) for this one field alone.

## Threading through the executor

`executor.Config` (`pkg/executor/executor.go`) gets a matching
`TruncateOutput int` field. Wired at all 5 sites that construct an
`executor.Config{}`:
- `cmd/afm/run.go:201`
- `pkg/orchestrator/runner_factory.go:40,71,88`
- `pkg/orchestrator/orchestrator.go:146`

Each site passes through the merged config's `cfg.Executor.TruncateOutput`.

## Truncation logic

`contentToAction` gains a `limit int` parameter, replacing both hardcoded
cutoffs with one rule: `limit > 0 && len(d) > limit` truncates (keeping the
existing `+ "..."` suffix behavior); `limit <= 0` never truncates. The same
`limit` value governs both the text-block cap and the Bash/other-tool
fallback cap — one config knob, not two, per explicit choice during
brainstorming (simpler to configure and reason about than separate knobs for
each content type).

`ParseToolAction` (the exported wrapper in the same file — currently has no
production caller, only its own unit test `TestParseToolAction`) gets the
same `limit int` parameter added, so there is exactly one truncation rule
regardless of entry point, with no hidden inconsistency between it and the
internal streaming closures.

The two internal streaming closures that already call `contentToAction`
(`executor.go`, around the two `LogAction`/`OnAction` call sites) already
have `e.cfg` in scope — they pass `e.cfg.TruncateOutput`.

## Documentation

- `config.example.yaml`: add
  `# truncate_output: 0  # 0 = no limit; max chars for logged agent text/Bash commands (default: 0)`
  to the `executor:` block.
- `README.md`'s Configuration section: add a matching commented line to the
  `executor:` block in its YAML example, consistent with how `idle_timeout`/
  `max_parallel` are documented there (inline comment, no separate prose
  paragraph needed).
- `release-notes.md`: add a dated entry describing the behavior change
  (default truncation removed; new opt-in `truncate_output` config), per this
  project's existing convention of logging behavior-affecting changes there.

## Verification

- Config: YAML parsing of `truncate_output`, merge/override behavior
  (matching the `MaxParallel` precedent), and that the default is `0`.
- Executor: `contentToAction`/`ParseToolAction` unit tests covering
  `limit == 0` (no truncation regardless of length, for both text and
  Bash/other-tool content) and `limit == N > 0` (truncates at exactly `N`
  chars with the `"..."` suffix, for both content types), and confirms
  `Write`/`Edit`/`Read`/`Glob`/`Grep` file-path details remain untouched
  either way.
- Full existing test suite (`go test ./...`) stays green — this is a
  behavior change (default flips from "always truncate" to "never
  truncate unless configured"), not a bug fix, so no regressions are
  expected but full-suite confirmation is required since `contentToAction`
  is exercised by multiple packages' integration tests.
