# Release Notes

Newest features at the top, older ones further down. Dates follow commits to `fix`/`master`.

## 2026-07-23

### Dashboard: remove a line comment with an ✕
- A comment left on a plan line (or a dialog question line) now has an ✕ button in its header — click it to remove the comment in one click, without reopening the edit form. Previously the only way to drop a comment was: click the line → open the form → "Delete".
- In the dialog, removing the last remaining comment brings back the normal answer UI (option buttons + ▸ SEND) instead of "Send feedback" — the switch is driven by the comment count.

### Fix: retry context no longer clipped by `truncate_output`
- When a stage was retried (after a rate-limit / server error), the "previously completed actions" block in the retry prompt was built from the human-readable `<phase>.log`, whose per-action detail is truncated by `executor.truncate_output`. With a small `truncate_output`, a retried agent saw an abbreviated view of its own prior work.
- **Fix:** retry context is now built from the raw, untruncated `<phase>.jsonl` stream. `truncate_output` still applies to the log and dashboard event feed as designed — only the retry continuation prompt sees the full detail.

### New: explicit warning when a dependency's plan is missing
- `CollectDependencyPlans` used to silently substitute `(plan not available)` into a stage's prompt when a dependency's `plan.md` / `execution_summary.md` was missing or empty — the operator had no way to notice the downstream stage was running with degraded context.
- A `context_warning` event is now published to the dashboard event feed (distinct amber styling), naming the dependency whose plan was missing. The stage still runs; the loss of context is just no longer invisible.

### New config: `executor.truncate_output` (default: no truncation)
- Agent tool-action output (text blocks, Bash commands) logged to `<phase>.log` and the `agent_action` event feed was previously always truncated at hardcoded lengths (100 chars for text, 80 for Bash/other tool details) — permanently, not just a display convenience (the full-screen dashboard view and the API don't recover it; only the raw `<phase>.jsonl` stream kept the untruncated original).
- New `executor.truncate_output` config (default `0` = no truncation; set to `N` to cap logged text/Bash-command detail at `N` chars, matching the old hardcoded behavior when set to 100 or 80).

### Fix: empty stage badge in dashboard event feed
- For a stage without its own `command` (uses the default client), `agent_action` events (Bash/Read/Skill/text) went out with an empty `stageId` — the dashboard didn't render the stage badge, even though status-change rows had one. Cause: `runnerFor` returned a shared runner whose `OnAction` was bound to an empty stageID.
- **Fix:** each stage now gets a per-stage runner with the correct stageID (the injected runner stays test-only). This also fixes attribution for parallel stages.

### New flow field `root_dir` — project root for agents
- `flow.yaml` can now set `root_dir` — the working directory (CWD) agents run in. Needed when the `afm` process's CWD doesn't match the project root (e.g. Docker setup: sources in `/workspace`, `.afm/` elsewhere). Without it, relative project paths (`docs/arch/…`) resolved against different roots for different stages → one stage writes a file, another can't find it.
- A relative `root_dir` resolves from the afm root (`--dir`); empty keeps prior behavior. `AFM_STAGE_DIR` (dialog files) stays tied to the afm root regardless. See README, "The flow.yaml file" section.

## 2026-07-22

### GitHub CI + automatic release on push to main
- Tag `v0.5.6` synced with upstream; further development happens in this (public GitHub) repo.
- `.github/workflows/ci.yml`: `validate` job (build+test+lint) on every push/PR; on push to `main`, the `auto-release-tag` job automatically bumps the patch version and pushes a tag using `RELEASE_TOKEN` (not the default `GITHUB_TOKEN` — otherwise the tag wouldn't trigger another workflow, a GitHub safeguard against loops).
- `.github/workflows/release.yml`: triggered by any `v*.*.*` tag (auto or manual `make release-minor/major`) — builds a multi-arch (`linux/amd64`+`linux/arm64`, via `docker/setup-qemu-action`) docker image to Docker Hub, plus `goreleaser`: cross-platform binaries, GitHub Release, Homebrew cask.
- `scripts/release.sh` simplified — no longer builds docker itself, only bumps the version and pushes the tag; all building now lives in `release.yml` (a single release entry point regardless of tag source).
- New: `brew install --cask akopichin/afm` (tap `akopichin/homebrew-afm`, `.goreleaser.yml` → `homebrew_casks:`). The post-install hook strips `com.apple.quarantine` and ad-hoc signs the binary (`codesign -f -s -`) — without both steps macOS kills the downloaded binary (`SIGKILL`); codesign alone isn't enough.

## 2026-07-21

### Hard autonomous track: `agents: [auto]`
- A stage's YAML can now statically declare the autonomous track — `agents: [auto]`. The stage runs directly via the autonomous agent, **skipping the supervisor's LLM decision and the fallback** to normal phases (no `plan.md`, no approval, dialog still available, writes `execution_summary.md`). For cases where the supervisor doesn't always classify autonomy correctly.
- Validation: `auto` must be the stage's only agent; `auto` + `supervisor: true` → parse error (conflicting intents). See `docs/superpowers/specs/2026-07-21-auto-phase-design.md`.

### Dashboard: comments on questions (like on plans)
- The dialog channel now supports clicking question lines and leaving per-line comments — same as the plan panel. Once at least one comment exists, the options and free-text answer field are hidden and a single **"Send feedback"** button appears: clicking it sends the comments (line quote + text) to the agent as the answer.
- **Ctrl/Cmd+Enter** in the dialog answer field submits the answer.
- **Dark theme by default** (previously taken from the system; the manual toggle is kept).
- **Full-screen logs** — an expand button like dialog/plan have; lines are truncated less, and not truncated at all in expanded view.
- **Project description in the header** — `/api/status` returns `description` from the flow root; useful for telling multiple running pipelines apart.
- The "↓ to latest" button is now labeled "↓ latest"; the dialog window scrolls to the end when expanded.

### Reliability: `events.jsonl` integrity
- **A truncated last record missing the trailing `\n`** (newline lost on crash) is now safely truncated instead of extending the log with a null byte and then quarantining it.
- **Corruption in the middle of the log** is now handled consistently on the `afm run` and `afm check`/run-lookup paths (single parser): the read path no longer silently falls back to a stale prefix — it signals `ErrCorruptLog`; `FindLatestRunForStage` no longer silently falls back to an older run.

### Internal: orchestrator refactor + test harness
- `pkg/orchestrator/orchestrator.go` was split from a god-file (1625→~400 lines) into focused files (concurrency/dialog_poller/agents/scheduling/control_api/supervisor_track/runner_factory) — a pure move, no behavior change. The phase domain vocabulary moved into `pkg/flow` (single source of truth). Deduplicated the autonomous-track helper (`startWithSupervisor`) and phase lists (`dialogPhases`).
- Added a scenario-driven integration test harness with a "synthetic" model (`pkg/orchestrator/scenario_*_test.go`): declarative happy-path and failure scenarios (mis-prefixed/misplaced question, stuck dialog, corrupt log, wrong supervisor track) to catch regressions across versions. Also closed coverage gaps (`afm check`/`list`, ErrRunLocked CLI, resume-from-revising, MaxParallel).

## 2026-07-20

### Fix: `startReadyStages` could launch the implementation agent for an autonomous stage
- Retrying a failed autonomous stage (`retryStage`) transitions it `Pending → Ready` via `EvReady`, then itself takes `EvStartRun`. In the narrow window between these two transitions, a concurrent call to `startReadyStages` from another event-loop branch (e.g. `onAgentCompleted` for a sibling stage) could win the `EvStartRun` CAS first — and `startReadyStages` blindly launched `runImplementationAgent` for any Ready stage, without checking `autonomous.flag`. `runImplementationAgent` reads `plan.md`, which an autonomous stage doesn't have → the stage failed with `open .../plan.md: no such file or directory` instead of re-running autonomously.
- **Fix:** `startReadyStages` now checks `isAutonomousStage` before spawning and launches `runAutonomousAgent` for such stages — symmetric with the existing checks in `recovery.go` and in `retryStage`.
- The regression reproduced reliably (5/5) in `TestIntegration_RetryFailedAutonomousStaysAutonomous`; after the fix the test is green (5/5 in a row, `-race`).

### goga-accept: CODEMANIFEST sync (second pass)
- `assets.ReadPrompt`: the manifest didn't reflect the third return value `fromOverride bool` (prompt source, for logging).
- `pkg/docker.ScanCommands`: the manifest didn't reflect the third parameter `generated map[string]bool` (commands covered by the autoShim wrapper).
- `pkg/orchestrator.Orchestrator.Retry`: the annotation didn't describe the retry branch for autonomous stages or the session/jsonl cleanup for interactive ones.
- `cmd/afm`: the manifest documented a nonexistent local helper `findLatestRunDir(stageID)` — approve/retry/revise actually call `state.FindLatestRunForStage` (added to `pkg/state/CODEMANIFEST`).
- `pkg/web/dashboard/src/app`: `App()` didn't document the `status === 'failed'` exception for hiding `showPlan` on an autonomous stage — the Retry button in PlanPanel must stay available for a failed autonomous stage too.

### UI: theme toggle moved from footer to header
- The dark/light button (`useThemeMode`, ☾/☀ icon) moved from `Footer.tsx` to `FlowHeader.tsx` — now in the header's top-right corner, next to the WS connection indicator, instead of the footer at the bottom. Behavior and appearance unchanged (same `.icon-btn`, same `aria-label`, same `localStorage` persistence), only the render location changed.
- `skins/base/header.css` (source `public/skins/base/header.css`, copied to root by `vite build`): added a column to `#header { grid-template-columns }` and spacing `#header .icon-btn { margin-left: 4px }` for the new button.
- `Footer.test.tsx`/`FlowHeader.test.tsx`: the theme-toggle test moved along with the button.
- CODEMANIFEST `footer`/`flow-header` synced with the code (`goga lint`/`goga contract` confirm no drift).

### Data race on package-level globals (websocket keepalive + retry)
- **`pkg/server` (websocket):** `wsPongWait`/`wsPingPeriod`/`wsWriteWait` were package-level `var`s that tests mutated between connections. Server goroutines (`PongHandler` in `readPump`, the ticker in `writePump`) read them on every iteration → data race against test writes (goroutines from a previous connection were still alive). **Fix:** snapshot the values into locals at the start of `readPump`/`writePump` (keepalive is fixed for the connection's lifetime — no need to re-read globals). `TestWebSocket_ClosesSilentClient` is now green under `-race`.
- **`pkg/orchestrator` (retry):** `RetryBackoff`/`MaxRetries` had the same problem. Agent goroutines (`runWithRetry`) outlive `Run()`'s return (it doesn't wait for them), and tests restored the globals in `t.Cleanup` → a race in `TestIntegration_RetryExhausted` under `-race`. **Fix:** `RetryBackoff`/`MaxRetries` are snapshotted per-instance in `New()`/`NewSupervisor()` (`Orchestrator.maxRetries`/`retryBackoff`, `Supervisor.maxRetries`/`retryBackoff`); `runWithRetry` and `EvaluateStage` read the instance's immutable fields instead of the globals. The package-level `RetryBackoff`/`MaxRetries` remain as defaults, including for tests.

### Lint: goconst + identical-switch-branches
- `supervisor.go`: the literal `"autonomous_execution"` replaced with the constant `phaseAutonomous` (goconst).
- `orchestrator.go`: identical `case phaseImplementation` and `case phaseAutonomous` branches (both did running→done) merged (revive `identical-switch-branches`).
- `golangci-lint run ./...` = 0 issues; `setstatuslinter` clean.

### Test: stale `TestIntegration_DialogViolationDetected` → relocate
- The test checked **old** behavior (fail-fast via `detectDialogViolation` when `question.json` was written outside stageDir), replaced by auto-relocate in commit `2a759dd`. Because of that, the mock (emitting a Write event with no real file) made the stage hang → 15s timeout. **Fix:** rewritten as `TestIntegration_MisplacedQuestionRelocated` — the mock creates the file in the wrong directory, and the test checks that the poller relocates it into stageDir and the stage moves to `awaiting_user_input`. `CLAUDE.md` docs updated to match the current relocate behavior.

### goga-accept: CODEMANIFEST sync
- `pkg/state/CODEMANIFEST`: `SetApplyHook(h TransitionCallback)` → `SetApplyHook(h)` — the `TransitionCallback` type doesn't exist in the code (a dangling reference; the real signature is `func SetApplyHook(h func(Transition))`); aligned with the sibling convention `SetExecFunc(f)`.

## 2026-07-16

### Supervisor agent — auto-assessing stage autonomy
- A stage with `supervisor: true` is assessed at start by an LLM supervisor: can it run **autonomously** (a single step through the attached skill — writes `execution_summary.md`, skipping planning/approval/review) or does it need the standard multi-phase cycle. The decision is written to `<runDir>/supervisor.jsonl` (`events.jsonl` is untouched).
- **Fallback safety:** any LLM-call error (timeout, bad JSON) → safe fallback to base phases. 529/502/503/504 are survived via retry (see below). The supervisor can **only shorten** phases, never extend them. Inline artifacts → always the standard track.
- **Config:** `flow.supervisor_command` (e.g. `glm47`) > `config.supervisor.command` > `config.client.command`; `stage.supervisor: true` + optional `stage.supervisor_prompt`. Prompt template: `assets/prompts/autonomous.md`.
- **FSM:** new transition `EvSupervisorApproved` (planning→ready), bus event `EventSupervisorDecision`. Resuming autonomous stages (`autonomous.flag` + `execution_summary.md`) handled in `recovery.go`. `CollectDependencyPlans` reads `execution_summary.md` instead of `plan.md` for autonomous dependencies.
- **Robust parsing:** `Supervisor.parseDecision` extracts the decision JSON from: raw JSON / claude envelope `{"type":"result","result":"…"}` / claude event array `[…]` / ```json fences. Covers both `claude --output-format json` shapes (container — single envelope, host — array).
- Spec/plan: `docs/superpowers/specs/2026-07-16-supervisor-agent-design.md`, `docs/superpowers/plans/2026-07-16-supervisor-agent.md`.

### Fix: supervisor LLM call + claude wrappers for `--output-format json`
- **RunJSONQuery** (`pkg/executor`) inherited `e.cfg.ExtraArgs`, which `executor.New` defaults to `DefaultClaudeArgs` (`--print --output-format stream-json --verbose --dangerously-skip-permissions`). That `stream-json` conflicted with the supervisor's `--output-format json` → claude exited 1. **Fix:** a clean invocation `-p <prompt> --output-format json` without ExtraArgs, plus stderr captured into the error (diagnostics).
- **docker wrapper** (`pkg/docker/wrapper.go`): `--include-partial-messages` is now added **only when `output-format=stream-json`** (previously always — which gave `requires --output-format=stream-json` for the json call). The same fix applied to host wrappers `glm47/51/52` (ai-free): partial only with stream-json, not with `-p`.
- Live validation: `afm run` in docker mode with `feature.yaml` (`supervisor: true`, `supervisor_command: glm47`) — `supervisor.jsonl` now contains a real decision (`decision=standard`, reasoning); before the fix it silently fell back on every stage.

### Supervisor: UI visibility + retries + autonomous dialog
- **Supervisor now retries transient errors** (529/502/503/504) with the same `RetryBackoff`×`MaxRetries` as stage agents (`EvaluateStage`), instead of falling back immediately. On non-retryable errors it still fails straight to fallback.
- **`validateDecision` is strict only for autonomous** (`== ["autonomous_execution"]`); for the standard phase it's advisory (`DetermineStagePhases` returns base phases anyway). Removed a false fallback that triggered when the LLM wrote base phases as a single string `"planning implementation"` (an artifact of Go-slice rendering) — a valid decision no longer gets hidden from the log/UI. `BasePhases` now renders as JSON `["planning","implementation"]`.
- **Supervisor decision is now published to the UI for both tracks** (`EventSupervisorDecision` + `supervisor.jsonl`); previously the standard track wasn't published — the UI never saw the resolution. In the dashboard, `supervisor_decision` gets its own highlight (`.feed-entry.supervisor`) in the Event feed.
- **Autonomous-agent logs now visible in the middle "Log" panel**: `handleLog` (`/api/stages/<id>/log`) and `buildDialogEntries` now read `autonomous.log`/`autonomous.jsonl` (previously only planning/implementation/review → the panel was empty for autonomous stages).
- **The autonomous phase supports dialog**: `phaseAutonomous = "autonomous_execution"` — a single phase with no planning/impl/review (the skill does everything itself, writes `execution_summary.md`), BUT the skill can still ask the user via the same file-based dialog protocol (questions `autonomous_execution.q<N>.*`, a valid phase, resume via `onUserAnswered`). The runner gets `AFM_STAGE_DIR`, the prompt includes `<interactive_rules>`.
- **Persistent supervisor-decision badge** in the stage header in the dashboard (the decision stays visible even after the event scrolls out of the feed).
- **Fix for host-mode `supervisor_command`**: the wrapper is now generated even if no stage uses the command as an agent; the secret is resolved in the host branch too (`UsedRecipes`).

## 2026-07-15

### Retry on 529/502/503/504 + removal of proxy and accounting
- `orchestrator.Classify` now classifies `API Error: 529/502/503/504` (raw text from glm wrappers) as `ClassRetryable` (previously `ClassFatal` → the stage failed). 500 stays fatal.
- The built-in reverse proxy (`pkg/proxy`) is fully removed: the ZAI transform is redundant after adding retry, and routing isn't needed (autoShim wrappers bake in the direct upstream URL). Removed the associated threading infra in `run.go`/`orchestrator`/`executor`/`docker`.
- Token accounting (`pkg/accounting`) is fully removed: it lost its data source without the proxy. Removed `/api/usage`, the dashboard `ConsumptionPanel`, and config `proxy`/`pricing`/`accounting`.
- **Backward compat:** `yaml.Unmarshal` is lenient → old configs with `proxy`/`pricing`/`accounting` still parse (the sections are silently ignored). `autoShim:false` is neutral (glm wrappers already went direct). Usage accounting is deferred.

### claude wrappers: bounded retry + stream-json + `--bare` (config `claude_bare`)
- **Bounded retry loop** (`pkg/orchestrator/retry.go`): fixed `RetryBackoff=5s` × `MaxRetries=15` (as in ralphex), replacing the previous exponential `[5s,10s,30s]` (4 attempts). z.ai 529 is transient; this survives the overload window. Confirmed: claude sends `stream:true` itself (via `--output-format stream-json`), so force-streaming isn't needed.
- **`--output-format stream-json` + `--include-partial-messages`** added to generated claude wrappers (`pkg/docker/wrapper.go`): covers non-interactive stages (which the executor doesn't pass ExtraArgs to) and gives partial deltas. `--output-format` is deduplicated (interactive stages already get it via the executor).
- **`--bare` + config `client.claude_bare`**: `--bare` = claude Code minimal mode (skips CLAUDE.md/hooks/skills/memory), body ~4 KB instead of ~127 KB (lower load on z.ai). **BUT `--bare` breaks the Skill tool** — goga-* skills stop resolving (the agent has to imitate them itself). So **default is `claude_bare: false`** (skills matter more). `claude_bare: true` is for flows WITHOUT skills.

### `type: cursor` — Cursor Cloud Agents API
- The Cursor Cloud API (`api.cursor.com`) **has no** synchronous OpenAI `/v1/chat/completions` (returns 404) — it's a **Cloud Agents API**: an async, run-based API where "chat" means starting a cloud code agent. So `type: openai` (which hits `${BASE_URL}/chat/completions`) **never worked and cannot work** with Cursor. The historical note about "Cursor via `api2.cursor.sh`" under `type: openai` was wrong — removed.
- New recipe type `type: cursor` → a wrapper with `CURSOR_*` env (`CURSOR_API_KEY`/`CURSOR_BASE_URL`/`CURSOR_MODEL`) and `exec /usr/local/bin/cursor-as-claude`. The adapter: reads the prompt from stdin → `POST /v1/agents` (no-repo, `mode:"agent"`) → polls `GET /agents/{id}/runs/{runId}` until a terminal status (`FINISHED`/`ERROR`/`CANCELLED`/`EXPIRED`) → emits claude stream-json (an `assistant` envelope with the `result` text + a `result` event) → archives the agent (best-effort, to avoid leaving clutter). `model: auto` (or empty) → the `model` field is omitted, Cursor uses its default.
- `auth.to` for cursor is any `env:VAR` (conventionally `CURSOR_API_KEY`); `url` is required (`https://api.cursor.com/v1`). Doesn't require `claude` in PATH (unlike openai). Requires `jq`+`curl` in the image. Tests: `TestAgentRecipe_CursorType`, `TestCreateWrappers_CursorTemplate`/`_CursorNoClaudeRequired`.
- **Note:** the first response takes ~30–90s (cloud-VM startup when the agent is created); the run itself is fast afterward (`durationMs` in seconds). Tolerable for interactive dialog, but not instant.

## 2026-07-14

### Docker `autoShim` — generated wrappers without mounting
- With the flag `docker.autoShim: true` afm **generates claude-compatible wrappers** for recipe agents (`docker.agents.<cmd>`) directly in the container — without `-v` mounting the host binary and without `extra_mounts` for tokens. The real wrappers (`glm47`/`glm51`/`glm52`/`deepseek-v4`) are "model+url+auth+sysprompt → `exec claude`", so they're described as a recipe and regenerated.
- **Recipe:** `model` (required → `ANTHROPIC_DEFAULT_{HAIKU,SONNET,OPUS}_MODEL`, one for all 3 tiers), `url` (gateway), `system_prompt` (`file:<path>` → `--append-system-prompt-file`), `auth.from` (`env:VAR` | `file:<path>` — where afm reads the secret on the host) + `auth.to` (`env:<VAR>` ∈ {`CLAUDE_CODE_OAUTH_TOKEN`,`ANTHROPIC_API_KEY`,`ANTHROPIC_AUTH_TOKEN`}).
- **Data flow:** the host reads the secret and sysprompt content from host-only files → transient env `AFM_SECRET_<CMD>`/`AFM_SYSPROMPT_<CMD>` (bare-form `-e`, value doesn't end up in the `docker run` argv); the container gets `url`/`model`/`auth.to` from the mounted `config.yaml`. The wrapper bakes in `ANTHROPIC_BASE_URL` (by host-match against `proxy.upstream` — z.ai routes through the proxy for 529-protection, deepseek goes direct), substitutes the secret from the transient env, `unset`s it, and `exec`s the absolute `claude`.
- **Unified wrapper-dir** (`docker.CreateWrappers`) = claude proxy-shim + generated wrappers; `proxy.CreateShim` removed. `orchestrator.proxyForCmd` is now generated-aware (`generated` → self-route via the baked `BASE_URL`, wrapper-dir on PATH). `docker.ScanCommands` skips generated (not mounted); `docker.UsedRecipes` — secrets are resolved only for recipes actually used in the flow (no false fail-fast / no leaking secrets of unused agents). Missing secret → fail-fast with the agent's name. `afm-init` adds `.afm/secrets.env` to `.gitignore`.
- **Bonus:** a recipe can describe a docker-only agent whose binary doesn't exist on the host (e.g. `deepseek-v4`) — `autoShim` generates it in the container.
- Spec: `docs/superpowers/specs/2026-07-14-docker-autoshim-design.md`.

### `type: openai` — OpenAI-compatible providers
- A recipe with `type: openai` → a wrapper with `OPENAI_*` env (`OPENAI_API_KEY`/`OPENAI_BASE_URL`/`OPENAI_MODEL`) and `exec /usr/local/bin/openai-as-claude` — a bash translator: reads the prompt from stdin, calls `${OPENAI_BASE_URL}/chat/completions` (stream=true), translates SSE into claude stream-json. Supports Cursor (`api2.cursor.sh`), DeepSeek, local LLMs, and any OpenAI-compatible endpoints.
- `auth.to` for openai is any `env:VAR` (NOT limited to ClaudeAuthEnvVars); `url` is required. Requires `jq`+`curl` in the image (added to `Dockerfile.runtime`).
- Backward compat: empty `type` (or `"claude"`) = previous claude behavior; unknown `type` value → validation error.

### Fix: generated wrapper wasn't found (executor LookPath)
- `exec.Command` resolved the bare command (`glm47`) via `LookPath` against the parent process's (afm's) PATH, while the wrapper-dir (`ProxyShimDir`) was only added to the child's env → `start glm47: executable file not found`. The executor now resolves the command to `ProxyShimDir/<cmd>` (absolute path); for mounted binaries it falls back to the bare name. Regression test `TestRunAgentResolvesWrapperCommand`. Without this fix autoShim didn't work end-to-end.

## 2026-07-13

### Dashboard on React
- The web dashboard rewritten from vanilla JS (`app.js` + `markdown-it.min.js`) to **React 18 + Vite + TypeScript** (`pkg/web/dashboard/src`); markdown-it is bundled in, no more separate file.
- **`go:embed` restricted** to only the served static assets (`index.html`, `assets/`, styles, icons). Previously `dashboard/*` pulled `node_modules` (~96 MB) into the binary — the binary was 163 MB; now **14 MB**.
- **Frontend build wired into `make`**: the `web` target (`npm run build`) is now a prerequisite of `build`/`install`/`docker-build` — the web is always rebuilt and compiled into the binary.
- **`Dockerfile.runtime` multi-stage**: a node stage builds React, a go stage embeds it. `make release-*` now also builds the web (the release image always ships the current dashboard from source).
- `.dockerignore`: `**/node_modules/` excluded from the docker context.

### WebSocket keepalive
- The server pings connections (gorilla `PingMessage` + `SetReadDeadline` 60s + `PongHandler`) and drops "dead" clients; app-level `{"type":"heartbeat"}` every 30s (`pkg/server/websocket.go`, single-writer via `select`).
- Client (`use-event-feed`): auto-reconnect with backoff (existing) + **watchdog** (silence >75s → forced reconnect); heartbeat refreshes liveness but doesn't show up in the event feed.

### Resizable layout and maximize
- Panels built on **`react-resizable-panels`**: 3 columns (`stages | central | feed`) and vertical splits `plan/dialog/log` within central; sizes persisted to `localStorage`. Default 15/60/25 (columns), 30/45/25 (rows).
- **Maximize** (⛶ icon) for plan/dialog/feed panels to full screen via a React portal; internal state (scroll, input) is preserved, `Esc`/✕ collapses.

### "Waiting for user" signal
- For statuses `awaiting_user_input`/`awaiting_approval`: a pulsing stage element in the sidebar + a dot in the header + panel glow + `document.title` blinking in a background tab + auto-scroll of the central column to the waiting panel.

### Auto-scroll for dialog and feed
- Dialog and event feed stay pinned to the bottom as content arrives, until the user scrolls up themselves (a "↓ to latest" button); if a question is awaiting an answer, the dialog scrolls to it (both on load and on a new question).

### Dialog: only Q/A, no agent "thoughts"
- The dialog section no longer includes agent `text` blocks from the stream-json log (for GLM these are thinking-aloud that duplicated the log panel) — only questions/answers. Reasoning context stays in `LogPanel`. Answer-option buttons highlight the selection (`selected`).

### goga theme after the React migration
- `style-goga.css` rebuilt as `@import "style.css"` + goga design-tokens (previously a separate 1100-line file for vanilla-DOM — broke after the React migration). Both themes now share the structure from `style.css`, goga differs only by palette + overrides; the themes no longer diverge.
- goga overrides: "goga" logo (teal), plain background with no novacorps grid or `.ray`, panels on `--bg-elev`.
- `pkg/server/server.go`: CSS swap for `href="./style.css"` (Vite `base: './'`) — fixes style switching for goga.

### Server tests under React
- `TestServerServesMarkdownIt` → `TestServerServesReactBundle` (markdown-it is in the bundle); `TestServer_IndexDefaultTheme`/`_IndexGogaTheme` updated for the built React `index.html` (`./style.css`, `#root`, theme-class).

## 2026-07-09

### Dashboard theme `goga`
- A second web-dashboard theme, enabled via the `theme: goga` flag in `~/.afm/config.yaml` (top-level). Visually inspired by qarium.ru/goga: dark-blue background `#0A0E1A`, teal accent `#20D4BF`, sans-serif font, rounded corners, no neon decoration. Default theme is `novacorps` (the previous hi-tech mint one). Unknown value → warning + `novacorps`.
- Self-contained `pkg/web/dashboard/style-goga.css` (styled from scratch; `style.css`/`index.html` for the default untouched). Theme delivery — server-side replace of `style.css`→`style-goga.css` and a `<body>` class when serving `/` (no FOUC, no `/api/config`).
- Quarium logo + "Goga" title in the goga theme (CSS: Nova hexagon hidden, `background quarium-logo.png`, `h1`→"Goga" teal via `::before`).
- Consumption-chart palette (`app.js USAGE_COLORS`) read from CSS tokens with a mint fallback — chart is teal in goga, unchanged in novacorps.
- UI translated to English for both themes (`index.html`, `app.js`, CSS `content`).

### `open_browser` defaults to `false`
- `server.open_browser` (in `~/.afm/config.yaml`) now defaults to `false`: the browser is NOT opened automatically on dashboard startup — the URL is printed to the log with a `→ open this URL in your browser to follow the run` hint. `server.open_browser: true` restores the previous auto-open. Works for local runs and Docker (host-side opener).
- Note: macOS 26 "binary-signing issues" (SIGKILL of an unsigned binary) are NOT related to browser opening — fixed by `make install` (ad-hoc codesign), not this flag.

## 2026-07-08

### Global `prompt` (root-level)
- **Root-level field `prompt:`** in `flow.yaml` — a shared instruction injected into the system prompt of **every stage and every phase** (planning/implementation/review): rendered as a `<global_prompt>…</global_prompt>` block right after `</system_rules>`.
- Not to be confused with `stage.prompt` (2026-07-02) — that one addresses a specific stage after `</stage>`; the root one is shared across the whole run.
- Optional: empty/absent → the block isn't written, output is byte-identical to before (backward compatible). Content is escaped (`escapeTags`) — XML tags can't be injected.
- Threading: `flow.Flow.Prompt` → `orchestrator.Options.GlobalPrompt` → `prompts.Inputs.GlobalPrompt` → `Build` (5 call sites in orchestrator).

### Reverse-proxy: silent usage for non-200
- `captureUsage` no longer logs a warning for responses without a usage field — non-200 (errors, 429/529 rate-limit) are silently skipped (`pkg/proxy/proxy.go`). Previously every failed proxy response cluttered the log with an invalid-usage warning.

### Docker image versioning (SemVer + auto-bump)
- `make release-{patch,minor,major}` — versioned release: pushes the immutable `akopichin/afm:vX.Y.Z` and a rolling `:latest`. The tag auto-bumps from the last git tag (`scripts/release.sh`); the git tag is created locally after a successful push.
- Version baked into the binary: `afm --version` (including `docker run … afm --version`).
- `make docker-push` stays dev-only `:latest`. The `dockers` section removed from `.goreleaser.yml` (docker is now the Makefile's job).

## 2026-07-07

### Per-stage consumption accounting (Consumption / Accounting)
- afm tracks agent consumption (tokens / cost / KB) and attributes it to run stages. New package `pkg/accounting`: stage execution windows (`StageWindow`/`LoadStageWindows`), reading `usage.jsonl` and terminal result events, aggregation by metric and time bucket, deriving cost from tokens (`DeriveCost`), a query facade `Accountant.Query`.
- Data source — the reverse-proxy: uniformly captures usage from proxied responses (`UsageRecord`/`ParseUsage` → `usage.jsonl`), `proxy.New` accepts `usageLogPath`. No-double-counting rule: a stage with a proxy record doesn't get a result-usage fallback.
- **Config**: `pricing.models.<model>` (`input_per_mtok`/`output_per_mtok`/`cache_per_mtok`, USD per million tokens; nil/empty → cost hidden, exact model-name match, no fuzzy matching); `accounting.bucket_minutes` (aggregation bucket width, default 5).
- **HTTP**: `GET /api/usage?metric=tokens|cost|kb&stage=<id>` (`UsageHandler` from `Config.Accountant`).
- **Dashboard**: a consumption panel in `pkg/web/dashboard`.

## 2026-07-05

### fix(dialog): interactive stage while awaiting an answer
- An interactive stage no longer fails while waiting for a user answer: the agent may exit before the answer arrives, but the stage stays in `awaiting_user_input` (not `failed`) — `NotifyAnswer` restarts the agent after the answer.

## 2026-07-02

### Stage field `prompt`
- **`stage.prompt`** — an optional field: an explicit instruction to the agent, placed in a separate `<prompt>…</prompt>` block right after the stage context (`</stage>`).
- Unlike `description` (task background/context), this is a direct instruction on what to do. Content is escaped (`escapeTags`) — XML tags (`</stage>`, `</prompt>`, `<plan>`) can't be injected.
- The builder reads `Stage.Prompt` directly (like `description`/`skills`) — no separate `prompts.Inputs.Prompt` field or threading through `Build()` calls in orchestrator.

### Stage `name` in the dashboard
- **`RunState.stage_names`** (id→name, `omitempty`) threaded through the existing `/api/status`; populated from the flow file in `run.go` (works for both new and resumed runs). `SetStageNames`/`Snapshot()` copy the map (`maps.Clone`) — later mutations by caller code don't corrupt store state.
- **UI**: the left panel shows `id` (large, uppercase) + `name` below it in small text; the central panel's title is `name`, else `id`. Stages without `name` look as before.
- **README**: the `name` field corrected to optional (validation doesn't require it); added descriptions of `prompt` and `name` display in the dashboard.

## 2026-07-01

### Embedded skills in the binary
- **`afm install-skills`** — Claude skills (`/afm`, `/afm-check`, `/afm-init`, `/afm-retry`, `/afm-review`) embedded in the binary via `assets.SkillsFS`.
- One-command install: `afm install-skills [--skills-dir <path>] [--force]`. Idempotent — without `--force` existing files are skipped, with `--force` they're overwritten.
- `install.sh` delegates skill installation to the binary with an interactive `[Y/n]` prompt (default is to install).
- `install.sh` UX: explicit error + a `make build` hint if `bin/afm` isn't built; a "Done!" block only shown when skills are installed.

### Docker mode: stabilizing interactive flows
- Runs under **host-uid** (gosu entrypoint): no root-owned writes, volume files belong to the host user; claude allows `--dangerously-skip-permissions`.
- `isatty` check (`golang.org/x/term`) — `-it` only added on a real TTY.
- Dashboard port forwarding; browser opens on the host (host-side opener); `IS_SANDBOX=1`; `extra_mounts` for custom-agent tokens; HOME set after gosu.
- Security: secrets not in argv (`-e KEY` with no value); absolute `--dir` in the container; `.dockerignore`.

## 2026-06-30

### Docker mode
- **afm automatically re-execs itself inside Docker** (`docker.enabled` in config or `AFM_USE_DOCKER=1`).
- `Dockerfile.runtime` (ubuntu 24.04 + node 22 + python 3.12 + go 1.26 + gosu); `make docker-build/push/run`; goreleaser docker.
- Auto-mounting: project + `.afm/`, `~/.claude/`, `~/.afm/`, non-standard agents (`command: glm51` → binary mounted `:ro`); `extra_mounts` for configs/tokens.
- Dashboard reachable from the host (port forwarding `-p`).

### `--dir` and the rename to afm
- Flag **`--dir`** (`AFM_DIR`) — custom directory for `.afm` (runs, flows, config); priority flag > env > current directory.
- Rename **flowmanager → afm**: binary, command, env `AFM_*`, skills `/afm*`. (Repo directory and git name unchanged; module path stays the same.)

## 2026-06-29

### Built-in reverse-proxy
- **The built-in proxy** intercepts agents' HTTP traffic to Anthropic-compatible gateways and applies transforms.
- **`ZAITransform`** — a workaround for `api.z.ai` 529: rewrites a non-streaming request into streaming, collects the SSE, and reassembles it into a single Anthropic JSON response.
- **`CreateShim`** — support for wrapper commands (`glm51` and others): the shim wraps `claude`, the proxy address still reaches the real client even if the wrapper overwrites `ANTHROPIC_BASE_URL`.
- `ProxyConfig` in config: `proxy.enabled/upstream/port/transforms.zai` (nil/absent → enabled, auto-detects `api.z.ai` by host).
