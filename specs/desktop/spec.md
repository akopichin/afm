# AFM Desktop v0.0.1

The first delivery is a local macOS application. A person opens AFM Desktop.app, selects a folder, starts an AFM run and operates its dashboard and live browser in the same project window. Electron supplies Chromium. The app bundle carries the matching AFM executable, including its dashboard. Users can open a run's loopback URL in their own browser concurrently.

## Acceptance contract

| ID | Observable behavior |
| --- | --- |
| DS-01 | Double-click the app in Finder with a clean profile and no AFM on PATH. See the first-run screen immediately. Connect to the existing local serve singleton or start the bundled executable. Reopening the app preserves active runs. Handle an incompatible host, disconnection, occupied ports and restart visibly. Identify ownership before offering process controls. |
| DS-02 | Pick any folder, including a new folder with zero runs. Keep it on Home across restarts. Select a local flow or reusable pipeline from project files, configured folders or Git packages. Support adding/removing sources and refreshing their inventory. Register a user-selected project through the host's real allowlist contract. Represent Desktop prefs under a Desktop namespace in ~/.afm/ and reuse existing AFM configuration and secret resolution. Preserve unrelated entries when editing shared configuration. Git choices are current checkout, new branch and separate worktree. Initial selection follows the flow's declared policy when available, then Desktop preference. An explicit launch choice overrides either. Show effective mode and branch before launch. Non-Git folders use their existing directory. Branch-only is a Desktop operation and must handle dirty trees and branch conflicts without discarding work. |
| DS-03 | Reconcile externally launched and Desktop-launched runs using serve's discovery and identity checks. Opening a project shows the actual AFM dashboard for its selected run. Launch twice with the same request ID produces one run. The same verified URL and port open in an external browser. Offer stop/cancel for managed runs and only supported operations for attached runs. Show reconnect, finished, offline and API-only states accurately. A stale port must never display a different run. |
| DS-04 | Retain the approved Home project tiles, active/attention states, spacing, logo, Graphite controls and workspace composition. Search projects using the fast overlay. The only persistent left rail inside a run is the existing AFM stage list. The real dashboard owns stage rendering. Reuse repository skin assets and semantic tokens. Custom selects support keyboard and pointer. The assistant is a right drawer with a thin left grip that turns blue on hover/focus. Persist width and clamp it on resize. |
| DS-05 | Shortcuts below work from the shell, focused dashboard and embedded Live tab. Focus mode affects the entire window. Preserve text editing, IME composition, menu navigation, modal precedence, escape and focus restoration. All shortcuts act only while the application is focused. |
| DS-06 | Attention appears to the right of the logo across projects. Selecting a notice navigates to its exact run and stage. Next/previous visit the stable outstanding queue, including multiple stages in a run. Viewing a notice preserves unresolved work. Reply/approval, reconnect and external changes update the queue without duplicates. |
| DS-07 | First launch discovers authenticated Claude Code, Codex and other available providers; allow manual executable/endpoint and credential-reference setup and testing. Persist model, reasoning, provider and permission mode. The app-wide assistant streams actual replies, supports cancellation and uses AFM MCP to inspect or control the selected project/run. Show effective scope before an operation, and surface tool errors. Support provider default, approval and explicit provider-supported unrestricted/YOLO modes. Map flags from installed CLI capabilities and show the selected effective mode. Never log or persist token values in renderer storage or argv. Reopen chat or switch models with a shortcut. |
| DS-08 | When a Desktop-launched AFM stage requests a browser, its real Chromium page appears in a sibling Live tab in that project. User and stage agent operate the same DOM, history and session. Switching to the dashboard preserves the page. Reflect navigation and closure; isolate simultaneous runs/stages. Reconnect without assigning a page to a different stage. Release the target when the owning stage ends. External standalone browsers retain their existing lifecycle. |
| DS-09 | Measure startup, keyboard navigation, cross-project bursts, background work and tab lifetime. Apply the budgets and instrumentation below. Keep discovery, provider startup and network calls off the first-paint path. |
| DS-10 | One native application bundle includes AFM and offline UI assets. User launches it without npm, Go, Homebrew or a separate AFM install. Docker validates logic and package contents. A successful overnight run on a Mac ends with a locally built, runnable .app from the final revision. macOS signing/distribution, Windows and Linux packaging and release automation are follow-up work. |

## Keyboard contract

| Action | Mac | Other desktop platforms |
| --- | --- | --- |
| Home | Option H | Alt H |
| Project picker | Command K | Ctrl K |
| Launch flow/pipeline in project | Command Shift Enter | Ctrl Shift Enter |
| Next / previous item needing attention | Option J / K | Alt J / K |
| Toggle Desktop assistant | Option A | Alt A |
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

Consider desktop-only ds-v* tags (first candidate ds-v0.0.1), a macOS archive/DMG and Homebrew cask, a Windows installer and Linux package after native validation. Add mutually exclusive tag filters for the CLI and desktop release jobs when introducing CI. Confirm native host locking support per platform before advertising it.
