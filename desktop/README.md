# AFM Desktop

A local Electron app for AFM projects. The first target is macOS on Apple Silicon. Design, acceptance criteria, reference screenshots and the overnight task live in [the Desktop spec](../specs/desktop/spec.md) and [implementation plan](../specs/desktop/plan.md).

## Run the overnight development flow

From the primary AFM checkout, with the dedicated .worktrees/desktop checkout in place, Docker running, Node 22.12+ and AFM available on the Mac:

```sh
caffeinate -i node .worktrees/desktop/desktop/scripts/overnight.js
```

Keep the Mac awake and connected. The wrapper validates the package, starts a fresh desktop-dev run on the existing desktop branch, prints the local dashboard URL and passes the plan as its initial task. It uses the primary checkout's AFM configuration, Claude OAuth reference and configured Codex login. After all stages finish successfully, it installs native dependencies on the Mac, reruns checks and builds the bundle from the final checkout. Build failures exit with a nonzero status. Run with `--inspect` to validate configuration without starting any agents.

```sh
open ".worktrees/desktop/desktop/dist/AFM Desktop-darwin-arm64/AFM Desktop.app"
```

For manual recovery, AFM supports resuming an interrupted pipeline with the same source and worktree, omitting the task flag. Run the following from the primary checkout, then use the native build commands below once delivery succeeds:

```sh
afm run --pipeline .worktrees/desktop/desktop/workflow --worktree desktop --exit-on-fail
```

## Local development

From the desktop package directory:

```sh
npm ci
npm start
npm run demo
```

`npm start` opens the real project inventory. `npm run demo` enables the approved prototype fixtures in a separate storage namespace, including a native Chromium sample tab. The seed connects to afm serve, lists registered runs, opens verified dashboards, and starts and cancels runs in host-registered projects. The production stage-to-browser bridge, full project registration and streaming assistant are scheduled in the implementation plan. See the spec for the complete shortcut matrix.

## Checks and packaging

From the desktop package directory:

```sh
npm ci
npm run check && npm test && npm run perf
npm run build && npm run check:package
```

Packaging includes the native afm executable from PATH, or the executable specified by AFM_DESKTOP_BINARY. The Finder app resolves the packaged sidecar directly. The build uses the repository's Graphite and base dashboard assets. On Apple Silicon the application is written to dist/AFM Desktop-darwin-arm64/AFM Desktop.app. This is a local development bundle.

For the Docker check, run `./scripts/docker-check.sh` in the desktop directory. It copies just the inputs into a disposable container and installs Linux dependencies there. It runs JS checks, keyboard tests, an actual temporary AFM host/launch/dashboard/cancel integration, the inventory benchmark, and native-to-container packaging. The container exits after verifying the artifact. The Mac build is generated on the Mac. Finder startup, native focus, accessibility, compositor behavior and end-to-end stage browsing need native verification.

The Mac launcher finishes with `npm run smoke:mac`. It opens the packaged production window in a temporary profile, checks first-run setup, assistant focus and communication with the bundled AFM host, then closes it. Successful package inspection alone does not pass this gate.

On the development Mac, the first launch attempt was terminated during signature assessment. After retrying the packaged app, both Finder launch and the isolated production window check passed. The local build uses an ad-hoc signature. Set AFM_DESKTOP_SIGN_IDENTITY to use an available signing identity when building on a Mac that requires one.
