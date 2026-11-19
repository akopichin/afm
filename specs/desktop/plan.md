# Deliver the first working AFM Desktop on macOS

Use this file as the initial task for desktop/workflow. The behavioral contract is [spec.md](spec.md). Work on the existing desktop branch. Preserve the seed renderer, its screenshots and the shared AFM styles. Implement the real paths from the ordinary application entry point. Keep deterministic sample data behind the explicit demo switch.

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

Complete the performance stage with the explicit workloads and targets in the spec. Record measurements and verify bounded idle work and target reclamation. The three independent reviewers cover runtime/package, UI/shortcuts/performance and assistant/CDP. Each records reproducible findings. The final writer resolves the findings and runs static checks, integration tests, perf, package build and artifact inspection. Final verifier is Codex gpt-6.1-sol. Update the README with working paths, missing evidence and known limitations. Keep tests headless in Docker. Validate the packaged entry on macOS.

Deliver an app with the same offline dashboard/theme assets and sidecar that tests used. The Mac wrapper reinstalls native dependencies and builds from the final worktree after a successful flow. When changes require a new sidecar, make the wrapper stage the correct native binary and verify its capability before packaging. Never bundle a Linux binary inside the Mac app. Keep CI, publishing and Homebrew setup for the next iteration.

## Morning acceptance on Mac

1. Open the bundle in Finder with a clean profile. Complete setup and select an empty folder.
2. Launch a real script flow. Open its dashboard in the project and in the external browser. Also open a separately launched run.
3. Navigate queued questions in two projects, answer one, test every shortcut from the dashboard and Live page, and resize/reopen the assistant.
4. Send a real assistant request using the selected provider/permissions and observe a streamed reply and an authorized AFM action.
5. Trigger a browser-enabled stage. Its Live tab must show the same DOM the agent changes. Switch away/back, finish the stage and check resource cleanup.
6. Record native cold start, keyboard timings and memory results; report any missing checks. A screenshot, fixture reply or directly opened demo page cannot establish production integration.

The seed passed Finder launch and smoke:mac on Apple Silicon after an initial signing assessment interrupted startup. Keep that native gate for the final revision. Record a native pass only when the packaged production window and its temporary AFM host both respond.
