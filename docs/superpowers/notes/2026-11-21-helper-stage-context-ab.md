# Helper `stage_context`: A/B and dev-spec reviewer trial

Sanitized summary of the live acceptance of `stage_context` (afm-engine v1.2.4,
contracts v0.4.1). Only the helper profile differed between variants.

## A/B (same model `claude-sonnet-5-5`, effort low, same container and task)

| Metric | A (no `stage_context`) | B (`inputs: [prep.rules]`) |
|---|---|---|
| Helper `context.md` / stdin | 137 307 bytes | 14 453 bytes |
| First request, total input tokens | 84 022 | 35 449 |
| First request cache read / write (1h) | 11 332 / 72 688 | 12 933 / 22 514 |
| Cost of the helper turn | $0.2923 | $0.0919 |
| Stage prompt | 134 576 / 135 983 bytes | identical |

The large unselected input (122 630 bytes) stayed in the stage prompt and
disappeared from the helper context; the stage kept all its inputs. Only the 1h
cache bucket was used. Separate token counts of the Claude Code system prompt and
tool schemas are unavailable: they appear only inside cache read/write, and the
cache read differs by 1 601 tokens because of cache warmth.

## dev-spec reviewer trial (`implement` reviewers, `stage_context: {inputs: []}`)

Run on the dev-spec 2.2.1 `implement` inputs. TA kept the inputs, TB used
`inputs: []`; the brief was identical apart from the absolute project path.

- TB context had no Artifacts section; TA listed the plan, project and delta paths.
- Both reviewers returned `needs_changes` with the same major finding.
- Tokens: TA input 37 546 + cache read 193 792; TB input 47 543 + cache read
  133 632. Model variance dominates, and the saving is small because dev-spec
  already passes these inputs as paths. Sol cost is unavailable (`priced=false`).

## Recommendation

Apply `inputs: []` to dev-spec temporary reviewers only if their brief carries the
criteria, paths and scenarios; expect a small gain there. The decision belongs to
the dev-spec package, outside this repository.
