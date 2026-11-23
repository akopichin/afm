# AFM Desktop changelog

## 2026-11-23

### Improvement:

- Configure and test global and project MCP servers for local ACP assistants. Import local servers from OpenCode and use securely stored tokens or credential files.

### Fix:

- Keep Assistant tool calls at their position in the reply as the chat streams and reopens.
- Bundle the Deep Agents runtime dependencies so Anthropic and OpenAI providers can reply in the packaged Desktop app.

## 2026-11-22

### Improvement:

- Show when each Library source last refreshed successfully, indicate refresh progress, and label each pipeline with its source.
- Connect a Claude Code container to the Assistant through an authenticated remote ACP bridge.
- Group Tasks by creation date and delete stopped or completed tasks from their cards.
- Edit a remote ACP provider’s workspace and start a fresh session from the saved chat.
- Start and resume saved Assistant conversations, search their titles, and confirm clearing a chat in the composer.
- Save project plans through the Assistant and manage open and completed plans with backlog commands.
- Connect OpenAI and Anthropic endpoints to a Deep Agent and see start and finish times for tool calls.
- Review run changes in a searchable file tree, with a remembered diff layout and Command D or Ctrl D.

### Fix:

- Handle `/backlog` locally for every provider and show all top-level slash commands in autocomplete.
- Draw interval lines in Costs, recover elapsed spans from AFM clocks, and place finished-run tokens from matching job totals.

## 2026-11-21

### Improvement:
- Pin projects on Home and copy their full paths from a compact popover on either card size.
- Remove a custom Assistant provider directly from its card with confirmation.
- Connect ACP agents to the Desktop Assistant and use their model and mode choices.
- Give Assistant updates their own timestamped bubbles after a pause.

- Label Costs and Home chart axes and show total and per-run series with values on hover.

- Open `afm://library/install` links to review and add Git pipeline sources in Library.
- Send queued Assistant follow-ups immediately during a managed wait and keep background AFM runs working.
- Switch between active and finished runs in Costs, with final report totals and recorded usage history for finished runs.
- Schedule repeating Assistant prompts with a per-task model, result history, and optional Telegram delivery.
- Show local timestamps in Assistant chat and status dots on run tabs.
- Queue Assistant messages during a reply and paste images into the composer.
- Show an outcome and run report in finished run tabs after the dashboard closes.
- Use Command on macOS or Ctrl on Linux for Desktop Home, notification navigation,
  the quick model chooser, and numbered run tabs. Open the project picker with Shift+K and the same modifier.
- Add a Costs screen with spend, tokens, Elapsed, Idle and Stage work for
  observed runs, 24h and 7d ranges, an As of slider for the last seven days,
  Compare runs, and Hide prices.
- Show a cumulative 24-hour cost line per active run on Home project tiles.

### Fix:

- Disable Add provider while the request is pending to prevent duplicate Assistant profiles from repeated submissions.
- Keep Assistant tool errors in reply order and let readers collapse and reopen their details.
- Keep Settings tabs visible in narrow windows and clarify credential and argument fields for custom providers.

- Use AFM counters for runs within the selected Costs period and preserve readings across gaps.

- Avoid late banners for questions already open when observation starts.
- Preserve interrupted watch summaries and link their events to the exact run.
- Open Assistant chat at the latest message and keep numbered run shortcuts working after entering a finished tab.

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
