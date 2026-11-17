# Pipeline packages

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- The `pipeline.yaml` manifest (schema v2) accepted by `afm run --pipeline` and `afm pipelines inspect` (`afm:pkg/pipeline`, `afm:cmd/afm/pipelines.go`, `afm:cmd/afm/run_pipeline*.go`).
- In-memory assembly of agent configuration from base config, package and options layers.
- Sources (local, Git), immutable snapshots, run binding and resume.
- Out of scope: the flow file itself ([../flow-config/spec.md](../flow-config/spec.md)), config loading ([../flow-config-settings/spec.md](../flow-config-settings/spec.md)). This is an afm-side feature; the engine only receives the assembled flow and config.

## Terms
- **Package / snapshot**: a folder with `pipeline.yaml`; its read-only copy under `~/.afm/cache/pipelines/snapshots/<sha256>`.
- **Layers**: base (global+project `config.yaml`) < package (`agents` file) < options (`--pipeline-options`).
- **Binding**: `.afm/pipelines/runs/<run-id>.json` tying a run to source, commit, content hash and options.

## Requirements

### FLOW-R40 The manifest is strict and versioned
`pipeline.yaml` is decoded with `KnownFields(true)`; an unknown key is an error. It must be a regular file (no symlink) of at most 1 MiB. `schema_version` is required and must be 2; version 1 is refused with a message that it is the catalog installer format. `name` matches `^[a-z][a-z0-9-]*[a-z0-9]$`, `version` is required, role IDs and `roles.<id>.command` are validated, and package paths must be non-empty, clean, relative and stay inside the package root.

Evidence: `afm:pkg/pipeline/manifest_test.go` `TestLoadManifestValid`, `TestParseManifestRejects`, `TestLoadManifestRejectsSymlink`; `afm:pkg/pipeline/manifest.go`; `afm:docs/pipelines.md`

#### FLOW-S40 Old catalog manifest
- GIVEN a package with `schema_version: 1`
- WHEN `afm run --pipeline <dir>` starts
- THEN it fails before creating a run, telling the user to run that package's `flow.yaml` directly

### FLOW-R41 Agent layers allow only recipes and helper profiles
Each layer may contain only `docker.agents` and `collaboration.profiles`; any other key is rejected with its path. A record from a later layer replaces the same-named record as a whole (no field merge), explicit zero values are kept, and the base config is not mutated. Package records must be prefixed `<name>-`, their `auth.from` may only be `env:NAME`, and `system_prompt: file:` must be a regular file inside the package. Options have no prefix rule and resolve relative `file:` paths against the options file directory.

Evidence: `afm:pkg/pipeline/layer_test.go` `TestAssemblePrecedenceAndWholeReplacement`, `TestAssembleDoesNotMutateBase`, `TestAssembleKeepsExplicitZeroValues`, `TestLayerRejectsDisallowedKeys`, `TestLayerRejectsPackageFileReferences`, `TestLayerPackagePromptMustBeRegularFile`, `TestValidatePackageNamesAndRoles`, `TestOptionsKeepFullSemanticsAndPinRelativePaths`; `afm:pkg/pipeline/layer.go`

#### FLOW-S41 Options replace a package recipe
- GIVEN a package recipe `demo-writer` and an options file defining `demo-writer` with only `type: codex`
- WHEN the run config is assembled
- THEN the effective `demo-writer` is exactly the options record; package fields are not inherited

### FLOW-R42 Shadowing and secret routing are warned, never silent
`afm run` warns when a package recipe shadows a base recipe of the same name, and when a package recipe sends a secret to a custom `url`. Nothing is written to the user's `config.yaml`.

Evidence: `afm:pkg/pipeline/layer_test.go` `TestAssembleWarnsWhenPackageShadowsBase`, `TestAssembleWarnsWhenAPackageRecipeSendsASecretToAURL`; `afm:docs/pipelines.md`

### FLOW-R43 Sources are parsed safely and Git refs are pinned
A source is a local folder or `<repo>[//<subdir>][@<ref>]`. URLs with embedded credentials are refused without echoing them; refspec-like refs are rejected. A branch or tag always fetches; the ref is pinned to a full commit before the run starts; a full commit already in the cache needs no network. Git subprocesses drop transport environment.

Evidence: `afm:pkg/pipeline/source_test.go` `TestParseSource`, `TestParseSourceRejectsCredentialsWithoutLeakingThem`, `TestParseSourceRejectsUnsafe`; `afm:pkg/pipeline/git_test.go` `TestResolveGitRefs`, `TestResolveGitOffline`, `TestCheckRefSyntaxRejectsRefspecForms`, `TestGitCommandDropsTransportEnvironment`; `afm:cmd/afm/pipelines_test.go` `TestPipelinesInspectNeverEchoesURLCredentials`

### FLOW-R44 Snapshots are content-addressed, declared-only and immutable
Before the run, the entrypoint, the `agents` file and declared `resources` are copied into a read-only snapshot named by its content hash (covering the exec bit); identical content reuses the snapshot. Symlinks, special files, missing resources and a directory entrypoint are rejected; limits are 50 000 files and 256 MiB. Stages see the snapshot as absolute `AFM_PIPELINE_DIR`; package `skills_dir` is pinned to an absolute snapshot path and may not escape the package. Loading verifies the snapshot and rejects tampering.

Evidence: `afm:pkg/pipeline/snapshot_test.go` `TestBuildSnapshotCopiesOnlyDeclaredResources`, `TestSnapshotIgnoresLaterSourceEdits`, `TestBuildSnapshotIsReusedForSameContent`, `TestBuildSnapshotRejectsSymlinksAndSpecialFiles`, `TestVerifySnapshotDetectsTampering`, `TestSnapshotHashCoversExecBit`; `afm:pkg/pipeline/load_test.go` `TestLoadFlowPinsSkillsDirsInsideSnapshot`, `TestLoadFlowRejectsSkillsDirEscapingPackage`, `TestLoadSnapshotRejectsTamperedSnapshot`

#### FLOW-S42 Source edited mid-run
- GIVEN a run started from a local package
- WHEN the source folder is edited and the same command is rerun
- THEN the run continues on the recorded snapshot and prints a notice

### FLOW-R45 Requirements are checked before a run exists
`requirements` (`docker_auto_shim`, `collaboration`, `browser`, `tools`) are reported by `inspect` with exit 0 when missing (exit 1 only for an invalid package); `afm run` stops at preflight before creating a run. `inspect` starts no agent, writes no config or run state, and never prints secret values.

Evidence: `afm:cmd/afm/run_pipeline_test.go` `TestPipelineRunReportsMissingRequirementsBeforeCreatingRun`, `TestPipelineRunRejectsInvalidPackages`; `afm:cmd/afm/pipelines_test.go` `TestPipelinesInspectJSON`, `TestPipelinesInspectTextShowsRolesAndMissing`, `TestPipelinesInspectRejectsInvalidInput`

### FLOW-R46 Flags are exclusive and a plain flow ignores the manifest
`--pipeline` with a flow-file argument, `--pipeline-options` without `--pipeline`, and `--pipeline-launch` without `--pipeline` or outside the container side are rejected before anything is created. `afm run flow.yaml` never reads a project's `pipeline.yaml`.

Evidence: `afm:cmd/afm/run_pipeline_test.go` `TestPipelineRunFlagConflicts`, `TestPipelinePlainFlowIgnoresManifest`; `afm:cmd/afm/run_pipeline.go`

### FLOW-R47 Runs are bound to their snapshot and resume on it
A run's binding records source, commit, content hash, options path/hash and executor types, never secrets. Rerunning the same command continues the newest unfinished run of that name on the recorded snapshot (Git: restored from the local cache without fetching; failing if the commit or a local snapshot is gone). Options are applied fresh on every start, with a notice when the options file disappears or changes. A run bound to another source, or an unbound run with progress, is never taken over; `--worktree-clear` starts a new run.

Evidence: `afm:pkg/pipeline/binding_test.go` `TestBindingRoundTripAndFind`, `TestBindingCarriesNoSecretFields`; `afm:cmd/afm/run_pipeline_test.go` `TestPipelineRunBindsRunToSnapshot`, `TestPipelineResumeContinuesSameRunOnRecordedSnapshot`, `TestPipelineResumeWarnsWhenOptionsDisappear`, `TestPipelineUnfinishedRunOfAnotherSourceStopsBeforeStart`, `TestPipelineUnfinishedPlainRunStopsPackageStart`, `TestPipelineClearFlagStartsFreshRun`, `TestPipelineResumeFromRecordedCommitWithoutNetwork`, `TestPipelineResumeFailsWhenLocalSnapshotIsGone`; `afm:cmd/afm/run_pipeline.go`

#### FLOW-S43 Another source claims the run name
- GIVEN an unfinished run of name `demo` bound to package A
- WHEN `afm run --pipeline B` resolves to the same run name
- THEN the start stops with advice to use `--worktree <new name>` or `--worktree-clear`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| FLOW-E40 | Unknown manifest key / bad schema_version | `pipeline manifest pipeline.yaml: …` | `TestParseManifestRejects` |
| FLOW-E41 | Disallowed layer key | error with the key path | `TestLayerRejectsDisallowedKeys` |
| FLOW-E42 | Credentials in Git URL | refused, credentials not echoed | `TestParseSourceRejectsCredentialsWithoutLeakingThem` |
| FLOW-E43 | Symlink/special file in package | snapshot build fails | `TestBuildSnapshotRejectsSymlinksAndSpecialFiles` |
| FLOW-E44 | Flag conflict | `--pipeline and a flow file argument are mutually exclusive …` etc. | `TestPipelineRunFlagConflicts` |
| FLOW-E45 | Rebinding a run of another pipeline | `run <id> already belongs to pipeline …; refusing to rebind it` | `afm:cmd/afm/run_pipeline.go` |
| FLOW-E46 | Container run without launch record | `a pipeline run in a container needs the host's launch record (--pipeline-launch) …` | `afm:cmd/afm/run_pipeline.go` |

## Coverage gaps and discrepancies
- **FLOW-G40** (doc-only): docs say changing a recipe mid-run makes a team agent/helper fail with `executor_changed`; that behaviour belongs to the engine helper identity and was not traced to a pipeline test here.
- **FLOW-G41** (doc-only): the snapshot is "not tamper-proof against a process that can write to your home directory" — a stated non-guarantee; `VerifySnapshot` detection on load is tested, but runtime protection is not.
- **FLOW-G42** (untested): `--pipeline-launch` refusal outside the container is code-only (`afm:cmd/afm/run_pipeline.go`); Docker pipeline launch tests (`run_pipeline_docker_test.go`) were not read in detail.
