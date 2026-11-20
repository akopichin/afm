# Deliver the first working AFM Desktop on macOS

This is the initial task used for the first Desktop delivery. For new work, pass the current task to desktop/workflow. The behavioral contract is [spec.md](spec.md). Work on the existing desktop branch. Preserve the seed renderer, its screenshots and the shared AFM styles. Implement the real paths from the ordinary application entry point. Keep deterministic sample data behind the explicit demo switch.

## 0. Freeze the starting point

Record the seed revision, Node, Electron, AFM version and MCP capabilities. Open desktop/README.md, the repository instructions and the relevant local subsystem guides. Inventory existing normal paths and demo paths. Run npm ci in desktop in the execution environment. Check the pipeline/agent auth bootstrap before starting work. Keep one product writer at a time. Each stage reports its changes, executable tests and unmet criteria into its own execution_summary.md.

## 1. First real project and run (DS-01–03)

Trace the bundled binary, serve discovery file, MCP client, project allowlist and run identity through desktop/src/host.js and pkg/serve. Start from a disposable home and empty folder. Implement durable project registration and source configuration, flow discovery, local/Git pipeline selection, idempotent launch, attach, cancel and reliable restart. Test an externally started run alongside a managed one; close/reopen the client and prove both identities. Handle selected Git mode before launch and preserve project changes on error. Inspect CLI worktree support and the flow parser before mapping options. Own child process lifetimes explicitly.

## 2. Production workspace (DS-04–06)

Use the seed Home tiles, picker, runtime dashboard, empty state, launcher, queues and assistant grip directly. Preserve the prototype hierarchy and CSS. Wire real stage data and actions to the UI. Subscribe to active runs; reconcile bounded background state. Deliver a notice to a precise project/run/stage even when another project is active. Verify the shortcut matrix from each focus owner, including live content, plus focus restoration after a modal closes. Compare to the three committed reference images. Headless checks exercise input routing and state; native layout evidence belongs in the Mac handoff.

## 3. Desktop-wide assistant (DS-07)

Detect CLIs in the environment a Finder-launched app actually receives. Discover effective auth without reading token values into renderer state. Implement a configurable provider registry, setup, local settings and a real streaming conversation with selected context, cancellation and bounded history. Enforce provider-supported reasoning/permission modes in the main process. Use the existing AFM host/run MCP contracts for agent tools. Test streamed partial output, missing credentials, CLI failure, cancellation, scope and mode mapping using deterministic provider subprocesses.

## 4. Embedded stage browsing (DS-08)

Trace the shipped AFM managed Chrome executable/pipe handshake, pinned chrome-devtools-mcp contract and host/container boundary in contracts/browser and docs/browser.md. Spike the actual AFM launch path and document the handoff. Deliver an opt-in, per-stage bridge to an Electron WebContentsView with its own partition and CDP session. If a companion executable or small AFM change is needed, include and version the matching native component in the package. Keep the shell, dashboard and other projects inaccessible to the stage transport. Authorize stage identity across the container/host boundary, expire handles and clean up on disconnect/end. Exercise creation by the AFM adapter, commands/events against the correct target, isolation, reconnect and cleanup with a deterministic transport fixture. Keep the real same-page interactive smoke as a native Mac check.

## 5. Performance, independent review and native delivery (DS-09–10)

Measure performance with the explicit workloads and targets in the spec. Record measurements and verify bounded idle work and target reclamation. The three independent reviewers cover runtime/package, UI/shortcuts/performance and assistant/CDP. The reviewers are required pre-completion helpers on implementation. The following doc stage updates the Desktop README and affected requirements and evidence in the spec. Report reproducible findings and keep the worktree read-only during review. The delivery writer resolves findings and keeps the guide and spec current. Run static checks, integration tests, perf, package build and artifact inspection. The final verifier is Codex gpt-6.1-sol. Keep tests headless in Docker. Validate the packaged entry on macOS.

Deliver an app with the same offline dashboard/theme assets and sidecar that tests used. The Mac wrapper reinstalls native dependencies and builds from the final worktree after a successful flow. When changes require a new sidecar, make the wrapper stage the correct native binary and verify its capability before packaging. Never bundle a Linux binary inside the Mac app. Keep CI, publishing and Homebrew setup for the next iteration.

## Morning acceptance on Mac

1. Open the bundle in Finder with a clean profile. Complete setup and select an empty folder.
2. Launch a real script flow. Open its dashboard in the project and in the external browser. Also open a separately launched run.
3. Navigate queued questions in two projects, answer one, test every shortcut from the dashboard and Live page, and resize/reopen the assistant.
4. Send a real assistant request using the selected provider/permissions and observe a streamed reply and an authorized AFM action.
5. Trigger a browser-enabled stage. Its Live tab must show the same DOM the agent changes. Switch away/back, finish the stage and check resource cleanup.
6. Record native cold start, keyboard timings and memory results; report any missing checks. A screenshot, fixture reply or directly opened demo page cannot establish production integration.

The seed passed Finder launch and smoke:mac on Apple Silicon after an initial signing assessment interrupted startup. Keep that native gate for the final revision. Record a native pass only when the packaged production window and its temporary AFM host both respond.

### Packaging evidence from the running Desktop branch

DS-08 now uses the AFM_CHROME_EXECUTABLE hook in cmd/afm/run_browser.go. The Mac host passed TestStartBrowser_ChromeExecutableEnvOverridesConfig with GOWORK=off. The unmodified AFM 3.1.11 from PATH lacks this hook. During this run the host can cross-build the branch sidecar for the Linux verifier and build it natively on macOS using cached private-module dependencies. Look for desktop/dist/sidecars/{linux,darwin}-arm64/afm; these are local build outputs. The delivery writer and runtime reviewer should verify the selected sidecar's capability and platform, reject a stale override, and keep a documented reproducible native build for a clean checkout. Rebuild if the Go hook changes during review. Running --version alone cannot distinguish capability-compatible executables.

On this Mac the owner may need to approve each newly built app at first launch. After packaging, retain that exact bundle, tell the owner when to open/approve it and rerun the native window check on the approved bundle. Preserve evidence of headless success and a pending native approval separately. Rebuilding between approval and retry may invalidate the approval.

For a native browser-enabled run, AFM also resolves the pinned chrome-devtools-mcp 1.12.0 executable on the child PATH. The AFM Docker image supplies it; the development Mac has no system executable. Verify and document the native path on a clean Mac. Keep the stage client bundled when promising installation from the app alone, and distinguish agent/provider credentials from bundled browser tooling. No runtime dependency download.
