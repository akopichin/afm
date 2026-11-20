# AFM Desktop

A local Electron app for AFM projects. Releases are available for macOS and Linux on arm64 and x64. Design, acceptance criteria, reference screenshots and the overnight task live in [the Desktop spec](../specs/desktop/spec.md) and [implementation plan](../specs/desktop/plan.md).

## Install and open Desktop

On macOS, install via Homebrew and open **AFM Desktop** from Applications:

```sh
brew install --cask akopichin/afm/afm-desktop
```

On Linux, extract the archive for your CPU and run the `AFM Desktop` executable.
Each archive includes AFM and Chromium. During first launch, choose an assistant
provider and use its login or a credential reference. Open a project folder to
start a run. See the [Desktop release guide](../docs/releases.md#afm-desktop-releases)
for archives, checksums and upgrades.

## Run the overnight development flow

The pipeline package is [desktop/workflow](workflow/pipeline.yaml), with its stage
graph in [flow.yaml](workflow/flow.yaml). The `doc` stage updates this guide and
[the Desktop spec](../specs/desktop/spec.md) from the implemented behavior and
check results. Three required AFM helper profiles review the delivery candidate
before final verify. Their findings can send it back for correction.

From the primary AFM checkout, with the dedicated .worktrees/desktop checkout in place, Docker running, Node 22.12+ and AFM available on the Mac:

```sh
caffeinate -i node .worktrees/desktop/desktop/scripts/overnight.js
```

Keep the Mac awake and connected. The wrapper validates the package, starts a fresh desktop-dev run on the existing desktop branch, enables AFM collaboration for the review helpers, prints the local dashboard URL and passes the plan as its initial task. It uses the primary checkout's AFM configuration, Claude OAuth reference and configured Codex login. After all stages finish successfully, it installs native dependencies on the Mac, reruns checks and builds the bundle from the final checkout. Build failures exit with a nonzero status. Run with `--inspect` to validate configuration without starting any agents.

```sh
open ".worktrees/desktop/desktop/dist/AFM Desktop-darwin-arm64/AFM Desktop.app"
```

For manual recovery, AFM supports resuming an interrupted pipeline with the same source and worktree, omitting the task flag. Run the following from the primary checkout, then use the native build commands below once delivery succeeds:

```sh
AFM_COLLABORATION=1 afm run --pipeline .worktrees/desktop/desktop/workflow --worktree desktop --exit-on-fail
```

## Local development

From the desktop package directory:

```sh
npm ci
npm start
npm run demo
```

`npm start` opens the real project inventory. `npm run demo` enables the approved prototype fixtures in a separate storage namespace, including a native Chromium sample tab. The seed connects to afm serve, lists registered runs, opens verified dashboards, and starts and cancels runs in host-registered projects. See the spec for the complete shortcut matrix.

## Projects, sources and runs (host layer)

The main process (`src/desktop.js`) owns project and run state; the renderer only calls IPC.

- **Host.** `src/host.js` connects to the user's `afm serve`, or starts the bundled one with its log in `~/.afm/desktop/host.log`. A host that is not AFM, or lacks the required tools, is reported as `incompatible`. A host Desktop started is recorded in `~/.afm/desktop/host-owner.json`; restarting any other host needs explicit confirmation. The host keeps running after the app quits because runs it manages are only reachable through it.
- **Projects.** Home projects live in `~/.afm/desktop/projects.json` and are registered in `~/.afm/serve.yaml`, the host's allowlist, through the YAML document model, so comments and entries Desktop did not write survive. The host reads `serve.yaml` once, so a project registered later makes the next launch restart the host; running runs are unaffected. Removing a project deletes only entries Desktop created.
- **Sources.** Pipeline sources (local folders and Git packages) live in `~/.afm/desktop/sources.json`. Folders are scanned for `pipeline.yaml` packages; Git packages are inspected with `afm pipelines inspect`. Each pipeline is registered in `serve.yaml` under a stable `desktop-` id and removed with its source.
- **Launch.** A launch is idempotent by request id, recorded in `~/.afm/desktop/launches.json` together with the exact host arguments, so retries, reloads and restarts never start a second run, and a retry resends what was accepted even if the flow, the Git preference or the source changed since. The same id with different parameters is refused, also when two calls race. A host Desktop did not start is restarted only after the launcher asks and the user confirms; the same request then repeats. Git mode: explicit choice, then the flow's `worktrees:`, then `AFM_WORKTREES`, then the selected project's and global config (resolved for the chosen project, also for library pipelines), then the Desktop preference. An explicit current or branch launch of a flow that declares `worktrees: true` is sent to AFM as `no_worktree` (new in this branch: `afm run --no-worktree`, `afm_run_start.no_worktree`), so the run uses the checkout itself; the flow file is untouched. Branch-only creates the branch with `git switch -c`, which keeps uncommitted work, refuses an existing branch, and is undone if the host refuses the launch.
- **Runs.** A run is identified by `instance_id` plus `run_id`. Its state (`running`, `finished`, `offline`, `api-only`, `mismatch`) and allowed operations come from the host listing; attached runs offer no cancel or release. `open` re-checks identity, so a reused port never shows another run.

Covered headlessly by `test/desktop-real.test.js`, which runs real `afm serve` and `afm run` processes in temporary homes.

## Production workspace

`npm start` runs the approved renderer against the main process. `npm run demo` keeps the deterministic sample data and never talks to the host.

- **One snapshot.** `src/workspace.js` builds a single snapshot of projects, runs and notices and pushes it to the window only when it changes. Discovery is single-flight, sleeps while the window is hidden, and the renderer does no polling of its own. First paint does not wait for it.
- **Notices.** For runs the host flags as needing a person, Desktop reads the run's own `/api/status` after proving `run_id` and `instance_id`, and turns approval, question, decision and failed stages into notices with the exact stage id. Their order is fixed when they first appear; a resolved notice leaves the queue, a returning one goes last. A failed read or an unreachable host keeps the known queue. Alt/Option J and K walk the queue, and the target page opens the stage once it reports ready.
- **Pages.** A dashboard page is created when its run is first shown, kept while the user moves around, and closed when its run disappears or when more than eight are open (pages waiting for a reply are closed last). Opening a run, closing a modal or leaving stage focus returns the keyboard to the page the user was on.
- **Shortcuts.** Command (Mac) or Control (elsewhere) is the only primary modifier, so Control-K and Control-Shift-Enter keep their editing meaning on a Mac. Alt/Option L, from the shell, a dashboard page or the Live view, opens the run's Live tab and gives the keyboard to its native view (it is outside the page's tab order); Alt/Option H, J, K, A, M and Command/Control K leave it again. The same table is implemented for the shell (`renderer/`), dashboard pages (`src/dashboard-keys.cjs`) and native views (`src/shortcuts.js`) and covered by `test/shortcuts.test.js` and `test/workspace.test.js`.
- **Network policy.** `src/protocol.js` limits the app session to its own files, inline data and the dashboards the user opened; other local ports, other schemes and file paths are cancelled. The set shrinks as runs end. The same verified origin is opened in the external browser.
- **Launcher.** Flows come from the project's `.afm/flows`, pipelines from the library sources, and the effective Git mode and branch come from the main process before launch. A retry of the same launch reuses its request id.

Known limits: runs on folders that are not on Home are shown in an ephemeral project and are registered only when a run is started from it; there is no UI yet to remove a project from Home; a reply given inside a dashboard page reaches the queue at the next snapshot (a few seconds, or at once when the window regains focus); no real AFM stage in the tests waits for a person (that needs an agent), so notices are covered with a fake run API and the real API is only checked for identity and an empty queue. Stage browsers are described in the next sections. Native layout, focus behaviour in Electron and process memory still need the Mac checks.

## Desktop assistant

`npm start` runs a real, app-wide assistant (`src/assistant/`); `npm run demo` keeps the scripted sample replies.

- **Providers.** Claude Code and Codex are built in; a custom provider is a Claude or Codex CLI at an absolute path, or an OpenAI-compatible endpoint (https, or http on this machine). Detection runs only when the assistant or settings opens, never on the first-paint path. It searches `PATH`, the login shell's `PATH` and the usual install folders, because a Finder-launched app has a minimal environment. Capabilities (streaming, permission modes, reasoning levels) are read from the installed CLI's own `--help`.
- **Credentials.** Only references are stored in `~/.afm/desktop/assistant.json`: the CLI's own login, an environment variable name or a key file path. Values are read in the main process and passed to the provider through its environment (never argv), under the variable that provider's CLI expects (`ANTHROPIC_API_KEY` or `OPENAI_API_KEY`); a configured file or variable name beats a standard variable already set. They are removed from streamed text, tool input and output, errors and the stored history. Quitting the app waits for provider processes to stop (SIGTERM, then SIGKILL after the grace period, at most a few seconds). The renderer sees only "ready / needs sign-in" and a label.
- **Settings.** Provider, model, reasoning level and permission mode persist and show as the effective mode in the chat footer. A mode the installed CLI cannot enforce is not offered, and a stale one is refused at send time instead of being approximated. Alt/Option M opens the quick chooser.
- **Modes.** Claude: Ask = default/manual, Read only = `plan`, Allow edits = `acceptEdits`, YOLO = `--dangerously-skip-permissions`. Codex: Read only = `--sandbox read-only`, Ask and Allow edits = `workspace-write` with `approval_policy` `on-request` and `never`, YOLO = `--dangerously-bypass-approvals-and-sandbox`. An endpoint provider is chat only.
- **Streaming and control.** One reply per conversation and at most three at once. The prompt goes through stdin. Output is parsed line by line and reaches the window as it arrives. Stop kills the provider's whole process group; the partial text is kept. History is bounded (60 messages, and a size budget for what is sent back to the provider).
- **AFM tools.** Every turn gets a private stdio bridge (`src/assistant/bridge.js`) to the AFM host's MCP endpoint. The bridge, not the provider, enforces the scope picked in the UI (all projects, one project or one run, resolved in the main process) and the permission mode: read-only offers no control tools, Ask and destructive actions wait for an in-app Approve/Deny card, Allow edits runs non-destructive control tools directly, YOLO runs everything. Refusals and tool errors are returned to the model and shown in the feed.

Covered headlessly by `test/assistant*.test.js` with deterministic fake `claude`/`codex` executables (`test/fake-provider.js`), a local streaming endpoint and a real `afm serve` host. No test calls a paid provider.

Not verified here: the real Claude Code and Codex CLIs, Codex's handling of MCP calls under `on-request`, the Electron-as-Node launch of the bridge inside `app.asar`, and a Finder-launched `PATH`. Those belong to the Mac handoff.

## Stage browser (Live tab)

When a stage of a run that Desktop's host started uses the managed browser (`afm_chrome`), its page opens inside the project as a **Live** tab next to the run's dashboard. The agent and the user operate the same Chromium page: DOM, history and session are shared.

- **Handoff.** AFM launches a "Chrome" executable with `--remote-debugging-pipe` (CDP on fds 3 and 4) and a private profile directory named `<epoch>__<stage>`. Desktop starts its host with `AFM_CHROME_EXECUTABLE` pointing at `~/.afm/desktop/bin/afm-stage-chrome`, a small wrapper that starts the companion (`src/stage-browser/companion-main.js`) with the app's own runtime (no Node install). The companion forwards the pipe to a private unix socket owned by Desktop (`src/stage-browser/broker.js`, directory mode 0700, token in `~/.afm/desktop/stage-browser.json`, mode 0600). Nothing is listened on a TCP port and no debugging endpoint is exposed to the machine or to the agent. The hook is the single opt-in line in `cmd/afm/run_browser.go`: without the variable AFM behaves as before, so standalone runs, Docker runs from a terminal and a host Desktop did not start keep their Chrome.
- **Router.** `src/stage-browser/cdp-router.js` answers the browser-level CDP calls the pinned chrome-devtools-mcp makes (`Target.*`, `Browser.getVersion`, ...) on top of the stage's pages and forwards session commands to the page's debugger. One router serves one stage, so a session or target id from another stage is refused. Browser-wide commands (`Storage.*`, `SystemInfo.*`, browser contexts) are not implemented, and only `http`/`https` pages can be opened. A page session gets the same filter: `Browser.*`, `Storage.*`, `SystemInfo.*` and `Target.*` (except `setAutoAttach`) are refused there too, and an answer that finishes after the client was replaced is dropped. The stage partition also cancels every request to an AFM server known to Desktop (the host endpoint and each run's API and dashboard origin, in every loopback spelling), including redirects, subresources and WebSockets; a run started in the last seconds appears in that list at the next snapshot.
- **Page.** Each stage gets its own in-memory partition and a `WebContentsView` without preload, Node or access to the shell, dashboards or other projects (`src/stage-browser/electron-page.js`). Requests to `file:`, `chrome:` and other non-web schemes are cancelled, permissions and downloads are denied, and the partition is cleared on release. Pages stay alive while the user is on the dashboard or another project; the view is only hidden.
- **Identity.** A companion is accepted only with the broker token, an AFM profile directory under `afm-browser`, a working folder and the parent process id. A relaunched browser of the same stage and process gets its page back; another process asking for the same stage is refused. A stage appears as a Live tab only after the run in its folder proves through its own API that it has started that stage (`src/stage-browser/bind.js`); an ambiguous match ties nothing, an unreachable candidate counts as ambiguity (another run answering never proves ownership), and a tie never moves to another run. The companion is a child of its run's process, so a candidate must also be the run whose process id the host reports (`pid` in `afm_runs_list`, new in this branch); a host that reports none ties nothing. A Docker run's process is the container launcher, so whether its companion matches is a Mac/Docker check.
- **Lifetime.** The page is released when the stage reports `done`/`failed`/`hook_failed`, when its run ends, on `Browser.close`, when the companion stays disconnected for 20 s, and when Desktop quits. A hidden window keeps watching open stage browsers at a slower interval (5× the visible one), so a finished stage frees its page; with none open there is no background work. At most eight stage browsers are open. Closing a page in the Live tab, navigating or opening a popup is reflected to the agent's client and the other way round.
- **Preference.** `stageBrowser` in `~/.afm/desktop/prefs.json` (default `true`) turns the bridge off; it applies to the next host Desktop starts. A host started earlier, by a terminal or by an older Desktop, keeps its previous Chrome behavior, so for those runs the stage browser stays external. The package ships the pinned chrome-devtools-mcp 1.12.0 (unpacked next to `app.asar`); Desktop writes a `chrome-devtools-mcp` wrapper into `~/.afm/desktop/bin` and puts that folder first on the host's `PATH`, running it with the app's own runtime, with update checks and usage statistics turned off, so a clean Mac needs no install; AFM's `browser.enabled` tri-state and a stage's `browser: false` still gate everything.

Covered headlessly by `test/stage-browser.test.js` (the real wrapper and companion started the way AFM starts Chrome, a fake page backend, real unix socket) and `test/stage-browser-ui.test.js`: page creation on the first request, commands and events on the right page, navigation and closure, refusals, two stages at once, reconnect, release on stage end, grace expiry, `Browser.close` and shutdown.

A Linux arm64 check under Xvfb exercised a real AFM run and packaged Electron with a local test provider. Through the packaged chrome-devtools-mcp, the stage listed the first page, navigated and changed its DOM. The project’s sibling Live tab displayed the same changed DOM. The corresponding macOS check still needs manual approval for the bundled AFM process.

## Checks and packaging

From the desktop package directory:

```sh
npm ci
npm run check && npm test && npm run perf
npm run build && npm run check:package
```

`npm run sidecar` builds the AFM executable of this branch for this machine (`go build` with `GOWORK=off`) into `dist/sidecars/<platform>-<arch>/afm`; on a clean checkout that is the reproducible native build, and the Mac launcher runs it before the checks. The tests, `npm run build` and `npm run check:package` resolve AFM the same way (`src/binary.js`): `AFM_DESKTOP_BINARY` alone if set, otherwise the staged sidecar, otherwise `afm` from PATH; the build bundles only an executable that matches this OS and CPU (read from its header) and contains the `AFM_CHROME_EXECUTABLE` hook; a Linux binary never goes into the Mac app and an AFM 3.1.11 from Homebrew is rejected. `npm run check:package` verifies the executable inside the package the same way, prints its version, platform and sha256, fails unless that hash equals the AFM the tests resolve, and starts the packaged chrome-devtools-mcp with no external copy on PATH (the app's own runtime is also run where it can load; on a Linux container without GUI libraries it reports that it did not). The Finder app resolves the packaged sidecar directly. The build uses the repository's Graphite and base dashboard assets. On Apple Silicon the application is written to dist/AFM Desktop-darwin-arm64/AFM Desktop.app. This is a local development bundle.

`npm run perf` runs two headless workloads. `scripts/perf.js` reconciles 100 projects and 1,000 runs in the renderer. `scripts/perf-runtime.js` drives the main process: idle polling with identical results, a burst of 200 attention notices (at most six run reads in flight, stable order), 100 project switches (no leaked subscribers, no polling while hidden) and a stream of 2,500 provider deltas (batched into a few IPC messages every 40 ms). Native cold start, keyboard latency and process-tree RSS are still pending a Mac measurement.

For the Docker check, run `./scripts/docker-check.sh` in the desktop directory. It cross-compiles the Linux AFM sidecar on the host using the checkout’s pinned Go modules, copies the inputs into a disposable container and installs Linux dependencies there. It runs JS checks, keyboard tests, an actual temporary AFM host/launch/dashboard/cancel integration, the inventory benchmark, and native-to-container packaging. The container exits after verifying the artifact. The Mac build is generated on the Mac. Finder startup, native focus, accessibility, compositor behavior and end-to-end stage browsing need native verification.

Production run dashboards open in sandboxed top-level Electron views. Each view connects to the verified origin for its run and respects the AFM dashboard’s framing policy.

The Mac launcher finishes with `npm run smoke:mac`. It opens the packaged production window in a temporary profile, checks first-run setup, assistant focus and a real stage row served by the bundled AFM host, then closes the window and cancels the temporary run. Successful package inspection alone does not pass this gate.

On the development Mac, the first launch attempt was terminated during signature assessment. After retrying the packaged app, both Finder launch and the isolated production window check passed. The local build uses an ad-hoc signature. Set AFM_DESKTOP_SIGN_IDENTITY to use an available signing identity when building on a Mac that requires one.

On a Mac that requires manual approval, the overnight launcher builds the complete app before starting its executable checks. Open the reported bundle and approve the macOS request. If a check was interrupted, keep that build and rerun `npm test && npm run perf && npm run check:package && npm run smoke:mac` from the desktop package directory.
