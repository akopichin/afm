# AFM Desktop changelog

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
- Review each Desktop development task through the AFM mesh, verify with Astra,
  and ask for approval of the prepared merge candidate in the dashboard.

### Fix:

- Preserve the destination of a clicked notification across AFM host outages.
- Announce a pending question once across pauses and reconnects, based on its
  dialog identity.
- Reuse dialog identities between polls to keep large attention queues responsive.
- Pass the configured Desktop environment to Git pipeline discovery and inspection.
