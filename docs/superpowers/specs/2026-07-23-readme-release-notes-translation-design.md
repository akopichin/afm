# Design: Translate README.md and release-notes.md to English

## Goal

`README.md` (513 lines) and `release-notes.md` (263 lines) are entirely in
Russian. Translate both to English so the public/GitHub-facing docs are
accessible to a wider audience.

## Scope

- **Full replacement.** Both files become entirely English. Russian originals
  remain available via git history — no `.ru.md` fork is kept.
- **Includes text inside code blocks**: inline comments in bash/yaml examples
  (e.g. `# Одобрить`, `# Флаг (разовый запуск)`), and example string literals
  used purely for illustration (e.g. `afm revise backend-auth --feedback
  "Нужно добавить Redis..."`, `description: "Краткое описание задачи"`).
- **Excludes**: code identifiers, CLI flags, file paths, YAML keys, command
  names — anything that must match actual afm behavior stays unchanged.
- Out of scope: other repo docs (`docs/design/*`, `docs/superpowers/*`) that
  merely reference `release-notes.md` by filename — no content there needs
  to change.

## Terminology glossary

Kept consistent across both files during translation:

| Russian | English |
|---|---|
| стадия | stage |
| флоу | flow |
| супервизор | supervisor |
| автономный трек | autonomous track |
| дашборд | dashboard |
| скилл | skill |
| план | plan |
| одобрение | approval |

Terms already in English in the source (Planning, Execution, etc.) are left
as-is.

## Structural fixes

- The one internal anchor link, `[Супервизор и автономный
  трек](#супервизор-и-автономный-трек)`, is updated to match the translated
  heading's new (English) anchor slug.
- No other internal or cross-file links need changes.

## Process

Direct manual translation, in place, section by section — not delegated to
subagents. Chosen over parallel per-file subagent translation because
terminology consistency and technical nuance matter more than speed here, and
a subagent pass would still need a full review afterward, eating back most of
the time saved.

## Going forward

New `release-notes.md` entries are written in English from this point on, to
keep the file internally consistent. This does not change the project's
commit message convention (commits stay in Russian).

## Verification

- Manual read-through of both translated files for fluency and terminology
  consistency.
- Confirm the anchor link resolves (heading slug matches link target).
- Confirm no Cyrillic remains in either file except inside git history.
