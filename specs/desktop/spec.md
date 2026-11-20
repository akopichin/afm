# AFM Desktop v0.0.1

The first delivery is a local macOS application. A person opens AFM Desktop.app, selects a folder, starts an AFM run and operates its dashboard and live browser in the same project window. Electron supplies Chromium. The app bundle carries the matching AFM executable, including its dashboard. Users can open a run's loopback URL in their own browser concurrently.

## Acceptance contract

| ID | Observable behavior |
| --- | --- |
| DS-01 | Double-click the app in Finder with a clean profile and no AFM on PATH. See the first-run screen immediately. Connect to the existing local serve singleton or start the bundled executable. Reopening the app preserves active runs. Handle an incompatible host, disconnection, occupied ports and restart visibly. Identify ownership before offering process controls. |
| DS-02 | Pick any folder, including a new folder with zero runs. Keep it on Home across restarts. Select a local flow or reusable pipeline from project files, configured folders or Git packages. Support adding/removing sources and refreshing their inventory. A Git collection contributes each discovered pipeline. Remove a recent project from Home while preserving its folder and runs. Register a user-selected project through the host's real allowlist contract. Represent Desktop prefs under a Desktop namespace in ~/.afm/ and reuse existing AFM configuration and secret resolution. Preserve unrelated entries when editing shared configuration. Git choices are current checkout, new branch and separate worktree. Initial selection follows the flow's declared policy when available, then Desktop preference. An explicit launch choice overrides either. Show effective mode and branch before launch. Non-Git folders use their existing directory. Branch-only is a Desktop operation and must handle dirty trees and branch conflicts without discarding work. |
| DS-03 | Reconcile externally launched and Desktop-launched runs using serve's discovery and identity checks. Opening a project shows the actual AFM dashboard for its selected run. Submitting twice with the same request ID produces one run. Start run immediately shows a busy button and disables additional clicks while Desktop checks pipeline requirements and waits for readiness. Early launch failures stay visible in the launcher with an option to open the log. The launcher shows missing variable names without displaying secret values. The same verified URL and port open in an external browser. Offer stop/cancel for managed runs and only supported operations for attached runs. Show reconnect, finished, offline and API-only states accurately. A stale port must never display a different run. |
| DS-04 | Retain the approved Home project tiles, active/attention states, spacing, Graphite controls and workspace composition. Use the AFM hexagon and dot for the application icon. Show a small afm prefix and prominent .desktop wordmark in the shell. Show the running Desktop version next to Settings. In the embedded dashboard, start the header with the run title and preserve the run controls. Search projects using the fast overlay. The only persistent left rail inside a run is the existing AFM stage list. The real dashboard owns stage rendering. Reuse repository skin assets and semantic tokens. Custom selects support keyboard and pointer. The assistant is a right drawer with a thin left grip that turns blue on hover/focus. Persist width and clamp it on resize. |
| DS-05 | Shortcuts below work from the shell, focused dashboard and embedded Live tab. Focus mode affects the entire window. Preserve text editing, IME composition, menu navigation, modal precedence, escape and focus restoration. All shortcuts act only while the application is focused. |
| DS-06 | Attention appears to the right of the logo across projects. Selecting a notice navigates to its exact run and stage. Next/previous visit the stable outstanding queue, including multiple stages in a run. Viewing a notice preserves unresolved work. Reply/approval, reconnect and external changes update the queue without duplicates. |
| DS-07 | First launch discovers authenticated Claude Code, Codex and other available providers; allow manual executable/endpoint and credential-reference setup and testing. Persist model, reasoning, provider and permission mode. Offer the provider default, listed models, and a custom model ID. Codex choices come from the installed client's account-specific list. Show the outgoing message and clear the composer as soon as the user sends it. Restore the text when submission is refused and show the error with the message. The app-wide assistant streams actual replies, supports cancellation and uses AFM MCP to inspect or control the selected project/run. Show effective scope before an operation, and surface tool errors. Support provider default, approval and explicit provider-supported unrestricted/YOLO modes. Map flags from installed CLI capabilities and show the selected effective mode. Never log or persist token values in renderer storage or argv. Reopen chat or switch models with a shortcut. |
| DS-08 | When a Desktop-launched AFM stage requests a browser, its real Chromium page appears in a sibling Live tab in that project. User and stage agent operate the same DOM, history and session. Switching to the dashboard preserves the page. Reflect navigation and closure; isolate simultaneous runs/stages. Reconnect without assigning a page to a different stage. Release the target when the owning stage ends. External standalone browsers retain their existing lifecycle. |
| DS-09 | Measure startup, keyboard navigation, cross-project bursts, background work and tab lifetime. Apply the budgets and instrumentation below. Keep discovery, provider startup and network calls off the first-paint path. |
| DS-10 | One native application bundle includes AFM and offline UI assets. User launches it without npm, Go, Homebrew or a separate AFM install. Docker validates logic and package contents. A successful overnight run on a Mac ends with a locally built, runnable .app from the final revision. Desktop releases publish macOS and Linux archives for arm64 and x64. The macOS app has an ad-hoc signature and a Homebrew cask. Notarization and Windows packaging are follow-up work. |
| DS-11 | In Settings, import exported variables from zsh or bash startup files, load a dotenv file, and add or remove individual variables. Manual entries take precedence over the file, the shell and inherited environment, in that order. Show variable names in Settings. Desktop prepends its browser wrapper to the effective PATH and keeps shell or user PATH entries. New assistant requests use the updated environment. An existing host applies updates after restart. Existing runs retain the environment they started with. AFM's default secret resolution uses the project's secrets.env, then the user-level secrets.env, then the host environment. An explicit secrets-file override retains AFM's existing behavior. |
| DS-12 | Edit pipeline options from Library or the project launch dialog. Missing preflight requirements open the editor with the reason. Show effective roles, model settings, and credential references. Load the saved user options when present, otherwise initialize the editor from the package's options.example.yaml resource or the empty YAML scaffold. Use lazy-loaded Monaco with YAML highlighting and the selected Graphite light or dark theme. Show loading, validation, save success, and validation errors. Keep the draft after a rejected save. Validate and save to ~/.afm/pipelines/<name>/options.yaml, then register that path for subsequent runs. Preserve existing settings on refresh and protect an options path from conflicting sources. |

## Keyboard contract

| Action | Mac | Other desktop platforms |
| --- | --- | --- |
| Home | Option H | Alt H |
| Project picker | Command K | Ctrl K |
| Open Settings | Command + comma | Ctrl + comma |
| Launch flow/pipeline in project | Command Shift Enter | Ctrl Shift Enter |
| Next / previous item needing attention | Option J / K | Alt J / K |
| Toggle Desktop assistant | Option A | Alt A |
| Give the keyboard to the Live view | Option L | Alt L |
| Quick provider, model and reasoning chooser | Option M | Alt M |
| Enter / leave dashboard stage focus | F / Escape | F / Escape |
| Picker, launcher and theme selects | Arrow keys, Enter, Escape; tab restores normal focus traversal | Same |
| Resize assistant at focused divider | Left / Right, Home / End; pointer drag from left edge | Same |

## Visual references

The starting renderer in desktop/renderer and its explicit demo mode preserve the accepted interaction design. Compare both initial and subsequent screens at 1360 × 860 and verify at a narrow window width. Preserve real content scrolling, hit targets, focus rings and overlay placement above native views.

[Home](reference/home.png) · [Workspace](reference/workspace.png) · [Live](reference/live.png)

## Performance acceptance

Run the same workload before and after changes and report OS, architecture and runtime version. Inventory workload is 100 projects and 1,000 runs, warm up then record p50 and p95 over at least 50 samples. The seed gate is p95 under 50 ms with zero render/persistence updates for identical results. Add burst tests for notice ordering, streamed chat and repeated project switches. Count outstanding requests, subscribers and native targets after 100 switches. Keep idle discovery single-flight, suspend unnecessary hidden work, coalesce updates, bound caches and close subscriptions. Hidden active stage pages keep their workflow alive.

On a Mac measure ten packaged launches to usable Home (initial p95 target 3 s) and 100 keyboard switches at idle and during a burst (p95 target 100 ms). Record process-tree RSS before and after opening and closing 20 project tabs. Verify target and listener counts return to baseline and report sustained growth. Report unmeasured native metrics separately from headless figures.

## Release follow-up

Desktop tags use the ds-v* release workflow. It publishes macOS ZIP and Linux tar.gz archives for arm64 and x64, SHA-256 checksums, and the macOS Homebrew cask. Windows packaging and Apple notarization remain follow-up work.

## Delivery evidence (headless, Linux arm64 container)

Run from `desktop/`. Node v22.23.5, Electron 45.4.1 (package only), AFM sidecar `afm version 3.1.11-desktop.dev` (sha256 `7417dc42…23c8`, built from this branch on the host; the container cannot fetch the private engine module, so `npm run sidecar` is run by the Mac wrapper).

| Command | Expected output | Observed |
| --- | --- | --- |
| `npm run check` | exit 0, no output after the banner | pass |
| `npm test` | `# pass 125`, `# fail 0` | pass (includes real `afm serve`/`afm run` in temporary homes) |
| `npm run perf` | inventory p95 < 50 ms, 0 identical updates, 0 outstanding requests/subscribers | p95 0.9 ms / 2.6 ms, 0, 0 |
| `npm run build && npm run check:package` | `… afm version 3.1.11-desktop.dev linux/arm64`, `AFM sha256 …` equal to the tested AFM, `chrome-devtools-mcp 1.12.0 (packaged files, no external PATH)` | pass; the packaged runtime itself is not executed in this container (missing GUI libraries), Mac only |
| `AFM_DESKTOP_BINARY=/usr/local/bin/afm node scripts/build.js` | refused: no `AFM_CHROME_EXECUTABLE` support | refused |

Review findings are resolved with regressions in `test/review-fixes.test.js` and `test/sidecar.test.js` (runtime RR-01–06, interface UI-1–6, assistant/browser R1–R8).

On macOS arm64, the packaged production app opened a real dashboard stage after launching a flow from an empty project with ⌘⇧Enter. The external browser URL matched the dashboard, the assistant drawer opened and cancellation worked. The packaged MCP reported version 1.12.0. A separate stage-browser check reached the MCP but returned an empty initial page list. Desktop now loads about:blank before announcing that page. The corrected stage passed in real Electron on Linux arm64 under Xvfb: the packaged MCP listed the page, navigated and changed the DOM, and the sibling Live tab displayed that same DOM. The stage used a deterministic local provider. Repeating this path on macOS requires approval for the bundled AFM executable. The updated headless suite passed 126/126 in Docker, and package inspection passed.

Remaining native checks include layout against `reference/*.png`; drag over a native Live view; the real DOM shared with an agent through chrome-devtools-mcp and Electron's debugger; blocking of a real AFM dashboard inside a stage page; hidden-view CPU; the quit handler with a real provider; cold start, keyboard latency and process-tree RSS; real Claude Code/Codex replies. Windows is out of scope (no host locking). The Go side of this round (`afm run --no-worktree`, `no_worktree` and `pid` in the serve tools) is exercised by the staged sidecar, which includes it; the container cannot compile Go (private engine module), so the added Go unit test (`TestDisableWorktreesOutranksFlowAndConfig`) and `make lint` run on the host.
