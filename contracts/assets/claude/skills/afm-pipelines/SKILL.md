---
name: afm-pipelines
description: Find, install, configure, update or remove reusable AFM pipelines. Use when the user wants a ready-made AFM workflow or an architecture/refactoring workshop, including per-pipeline models, profiles and skills.
---

# AFM pipeline setup

The catalog is https://github.com/akopichin/afm-pipelines. AFM ships this skill;
the catalog ships packages and the versioned installer. Do not assume the catalog
is already cloned, that a suggested pipeline is installed, or that all projects
use the same models. Installing a pipeline does not authorize running it.

## Find and prepare

Reuse a user-specified catalog checkout. Otherwise clone the catalog into a new
cache directory outside the target project, using Git without executing remote
shell snippets. Resolve the selected release/ref to an immutable commit and
checkout that commit detached. Keep the commit in the installation report. Do
not reset an existing checkout with local edits. A local development checkout is
allowed when requested; clearly report that it is not a clean published version.

Read the checkout's README and `docs/installer.md`. For installer protocol 1,
Node.js 22+ and Git are required; install its locked dependency with
`npm ci --ignore-scripts --no-audit --no-fund` in the catalog checkout.

Run `node scripts/pipelines.mjs list`, then
`node scripts/pipelines.mjs inspect --project /absolute/project/root`.
The target must be a Git project root. Inspect manifests and package READMEs to
match the user's objective. Present relevant choices; do not install every
package or invent entries absent from the catalog.

## Choose settings

For the selected package, offer the required AFM settings, each role's client,
model and reasoning configuration, and optional project-local skills for Claude
and/or Codex. Reuse preferences already supplied. Explain package constraints
and missing dependencies. The initial installer supports Claude/Codex Docker
recipes, including copies of existing compatible AFM profiles. Arbitrary host
commands need a separate adapter; do not pretend the Docker recipes work on host.

Use the documented options JSON format. Each role can use its recommended
settings, copy an existing profile, or override client/model/reasoning. Copies
are isolated by pipeline and role; never edit a shared profile to customize just
one pipeline. Package recommendations are not mandatory model identities.

Provider login, Docker/client installation and paid model probes are separate
steps. Offer the missing setup with concrete commands appropriate to the user's
environment. Do not print credentials or put them in plans. The installer itself
only checks configuration and files; it does not prove provider access.

## Plan and apply

From the catalog checkout:

```sh
node scripts/pipelines.mjs plan refactor --project /absolute/project/root --options /absolute/options.json --out /absolute/install-plan.json
node scripts/pipelines.mjs apply /absolute/install-plan.json
node scripts/pipelines.mjs verify refactor --project /absolute/project/root
```

Replace `refactor` with the selected catalog entry. Omit `--options` to use
package defaults or previously installed choices. Before apply, show the plan
summary: catalog revision, models, settings, skill destinations and changed files.
Proceed when that concrete scope is already authorized; ask only for unresolved
choices or additional changes. The plan file is private local state, not a file
to commit or share. Do not dump its base64 payload into the conversation.

If inputs changed or local files conflict, inspect the differences and create a
new plan; do not force through conflicts. Apply performs backups and AFM flow
validation, rolling back a failed validation. After interruption, use the
documented `recover` command before retrying. Never delete the apply lock blindly.

Report what was installed, its Git revision, selected models, checks performed,
remaining setup and the resulting run command. For worktree runs, explain which
new project configuration/skill files need to be committed so they are present
in the worktree. Preserve existing runs and their snapshots.

## Update or remove

Update by selecting a new catalog revision and making another `plan`; existing
model choices are reused unless explicitly changed. Old generated profile
versions remain available for earlier runs. Removal uses
`remove NAME --project ROOT --out PLAN`, then `apply PLAN`. Confirm removal is
within the user's request and that runs using those profiles are stopped.
Removal preserves plans, run artifacts, credentials and shared AFM settings;
edited managed files cause a conflict instead of being deleted.
