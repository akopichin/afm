# AFM Desktop changelog

## 2026-11-21

### Improvement:

- Show an outcome and run report in finished run tabs after the dashboard closes.
- Use Command on macOS or Ctrl on Linux for Desktop Home, notification navigation,
  the quick model chooser, and numbered run tabs. Open the project picker with Shift+K and the same modifier.
- Add a Costs screen with spend, tokens, Elapsed, Idle and Stage work for
  observed runs, 24h and 7d ranges, an As of slider for the last seven days,
  Compare runs, and Hide prices.
- Show a cumulative 24-hour cost line per active run on Home project tiles.

## 2026-11-20

### Improvement:

- Mark the macOS Dock icon while requests are pending and briefly bounce it
  for new questions, approvals, or decisions in the background.
- Use a translucent macOS title bar with native window controls.
- Show native notifications for questions, approvals and decisions. Configure
  categories in Settings and click a banner to open its run and stage.
- Delete a stopped Desktop run and remove its clean, dedicated worktree after
  confirmation.
- Unlock a passphrase-protected SSH key from Library and pipeline preflight.
- Add background watch tasks: ask the assistant to watch a run, a project or all
  projects, manage them in a new Tasks tab and read reports in a wide dialog.
  Native banners open the run, stage or report.
- Resume the provider session between assistant messages and clear the chat
  locally with `/clear`.
- Reply to assistant messages faster by caching the imported shell environment.
  Refresh it in Settings → Environment.
- Review each Desktop development task through the AFM mesh, verify with Astra,
  and ask for approval of the prepared merge candidate in the dashboard.

### Fix:

- Handle Desktop navigation, Settings, and Assistant shortcuts while an
  embedded AFM run has keyboard focus.
- Preserve the destination of a clicked notification across AFM host outages.
- Announce a pending question once across pauses and reconnects, based on its
  dialog identity.
- Reuse dialog identities between polls to keep large attention queues responsive.
- Pass the configured Desktop environment to Git pipeline discovery and inspection.
