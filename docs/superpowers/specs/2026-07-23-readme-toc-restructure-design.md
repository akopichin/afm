# Design: README table of contents + section reordering

## Goal

`README.md` has 12 top-level (`##`) sections but no table of contents, and the
section order interleaves onboarding steps with reference material in a way
that doesn't match how someone actually uses afm. Add a TOC and reorder
sections so the read order is: onboard → write a flow → understand runtime
concepts → monitor → configure → contribute.

## Table of contents

- New `## Contents` section, placed immediately after the intro paragraph and
  before `## How It Works`.
- Flat bullet list, one entry per `##` section, linking to its GitHub-style
  anchor slug (lowercased, spaces→hyphens, punctuation stripped).
- `###` subsections do NOT get their own TOC entries — keeps the list
  scannable instead of duplicating the full heading tree.

## Section reordering

Current order (12 `##` sections):
How It Works → Installation → Quick Start → Specifying the Working
Directory → The flow.yaml File → Supervisor and Autonomous Track →
Configuration → Web Dashboard → Directory Structure → Usage in Claude Code →
Stage Lifecycle → Development.

New order:

1. How It Works — unchanged position
2. Installation — unchanged
3. Quick Start — unchanged
4. Usage in Claude Code — moved up (was #10). It's an alternate "how to run
   afm" entry point, belongs right after Quick Start rather than near the
   bottom.
5. The flow.yaml File — unchanged content/subsections (Passing Context
   Between Stages, Interactive Stages)
6. Supervisor and Autonomous Track — unchanged
7. Stage Lifecycle — moved up (was #11). Explains the states referenced by
   flow.yaml's `agents`/`supervisor` fields and by the dashboard, so it reads
   better here than buried after Directory Structure.
8. Configuration — absorbs "Specifying the Working Directory" (was its own
   `##` section, #4 in the old order) as a new first subsection,
   `### Working Directory`. Both describe a settings-precedence chain
   (`--dir`/`AFM_DIR`/cwd vs. CLI flags/project config/global config/
   defaults), so unifying them under one "Configuration" umbrella removes a
   redundant top-level section making the same kind of argument.
9. Web Dashboard — unchanged
10. Directory Structure — moved down (was #9). Reads as an "under the hood,
    here's what's on disk" wrap-up right before Development.
11. Development — unchanged, stays last

No section content is rewritten beyond this move — headings, prose, code
blocks, and the stage-fields table stay as they are (already fully English
per the prior translation work). The internal anchor link to
`#supervisor-and-autonomous-track` (in the "How It Works" intro) is
unaffected since that heading's text and slug don't change.

## Additional fix: Homebrew install command

`README.md:24` currently reads `brew install --cask akopichin/afm`, which
does not actually work — the correct, working invocation is
`brew install --cask akopichin/afm/afm` (full `<tap>/<cask>` form, tap
`akopichin/afm`, cask `afm`). Fix this line while touching the Installation
section for the reorder. The `brew upgrade --cask afm` line (`README.md:28`)
is left as-is — untouched by this report, and Homebrew resolves the
already-installed cask by its short name for upgrades.

## Verification

- Every `##` heading has a corresponding TOC entry, and every TOC link
  resolves to a heading with a matching anchor slug.
- No content is lost or duplicated during the move — a diff of rendered
  section bodies (ignoring order) matches the pre-change file.
- `git mv`-free reordering: implemented as in-place edits, not file moves
  (this is a single Markdown file).
