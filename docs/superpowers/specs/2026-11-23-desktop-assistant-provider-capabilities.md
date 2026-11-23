# Desktop Assistant provider capabilities and agent activity

## Scope

This specification covers the Desktop model catalog and reply probes, live
subagent activity, saved MCP servers, and keyboard focus in AFM Desktop 0.4.0.
Provider accounts continue to use their configured Desktop credentials or
native CLI sign-in. A provider's model list is specific to the route and
credential used by the Assistant.

## Model and reasoning discovery

| Route | Model catalog | Reasoning levels | Reply probe |
| --- | --- | --- | --- |
| Codex CLI | Codex app-server `model/list` and `config/read`, with the selected credential | Per-model efforts from Codex, with CLI capabilities as a fallback | Connection and catalog checks |
| Claude Code CLI | CLI model aliases and custom model IDs | Efforts advertised by the installed CLI | Connection and sign-in checks |
| Local or remote ACP | Session model options or the ACP model list | Session `thought_level` options, refreshed after changing the model | Temporary ACP session |
| OpenAI-compatible chat endpoint | Authenticated model catalog | Model or gateway metadata | Short completion |
| Deep Agent with an OpenAI or Anthropic endpoint | Authenticated provider catalog and compatible gateway metadata | Model or gateway metadata, sent using the endpoint's API dialect | Short completion using the same endpoint model adapter |

- An endpoint uses its standard models endpoint first. When it is missing or
  empty, compatible gateway model group names and public deployment aliases can
  populate the list. An explicit authorization failure on the standard listing
  remains an authorization failure. Gateway metadata enriches existing catalog
  entries without replacing their IDs. With several deployments reporting lists
  for one route, show the levels common to those deployments.
- Prefer the endpoint's explicit levels. If it advertises reasoning support
  without enumerating levels, offer `low`, `medium`, `high` and `xhigh`
  as candidates. An explicit empty list suppresses effort choices. Missing
  metadata keeps Provider default and lets the user enter an endpoint-specific
  level. No reasoning effort override is sent for Provider default.
- A catalog describes what's advertised to the current route. Successful
  inference confirms that the selected model and effort are accepted at the
  time of the probe. Account changes, gateway routing and quotas can change
  what subsequently succeeds.
- For Codex CLI, resolve an unavailable configured default to the app-server's
  advertised default when available. If its inherited effort is incompatible
  with the selected model's catalog, use that model's advertised default effort.
  Codex ACP prefers models shared by the adapter and the executable's catalog
  where an intersection is available.
- **Settings → Models** and **Command+M / Ctrl+M** use the same model and effort
  options. Eligible selection changes trigger a probe. The quick picker probes
  the draft without saving, debounces changes and ignores superseded responses.
  **Check connection** refreshes setup and discovery. **Test reply** forces a
  new validation or joins an identical probe already running. A pending status
  indicates that validation is in progress. A rejection stays visible for the
  selection so the user can return to Provider default or try another level.
- A probe uses a short fixed prompt with no AFM bridge, external MCP servers,
  images, project history or chat history. ACP permission requests are denied
  for a probe. A reply must produce output. The main process bounds probe
  duration and parallelism, deduplicates in-flight requests, and caches recent
  successful results by route, environment, credential, model and reasoning.
  Tests do not add chat messages. Endpoint probes may use provider quota.

## Live subagent activity

- **Shift+Down / Shift+Up** opens the agent switcher. Main is represented once.
  The list reflects subagent identities and states reported by the current
  provider. AFM runs started by a chat remain their existing activity entries.
- Provider adapters emit normalized delegate and tool events. ACP reads
  registered child-session updates and uses Codex collaboration events when
  available. Codex CLI combines its JSON stream and the local rollout records
  for recognized thread IDs. Claude uses child tool-use events. Deep Agents
  correlate nested LangGraph events with the tool call that launched the child.
- Desktop renders the selected child's reported reasoning summaries or text
  blocks and its tool calls. The available detail varies by provider and run.
  Opaque Codex message payloads, unavailable message placeholders and encrypted
  reasoning are omitted. Reported tool arguments and visible reasoning pass
  through the same credential and session-ID redaction used by the main chat.
- A completed child remains visible while selected. Leaving it returns it to
  the normal list filter and removes it from navigation immediately. A child
  also leaves the active list once its parent reply ends, unless it is currently
  selected. No timer is used. Chat snapshots can retain bounded tool and
  reasoning details for rendering and redaction. The child activity view does
  not take input.
- The Desktop activity view consumes provider events and local records. Opening
  the switcher or selecting a child does not send a model request.

## Saved MCP servers

- Save a server once in **Settings → MCP**. Desktop resolves it for each
  eligible interactive turn. Global servers are available across projects.
  Project servers follow their project and its runs, including worktrees.
  **Test connection** checks authentication and the handshake and shows the
  latest result. It does not control whether the saved server is supplied.
- The registry resolves file, environment and stored token references in the
  main process. A failed credential or executable lookup yields a scoped
  notice, and other servers remain available. Secrets stay out of renderer
  responses, notices and process arguments.
- Deep Agents receive discovery and invocation tools scoped to the configured
  server names. Discovery opens a connection on demand and returns that
  server's paginated tool schemas. Calls reuse the connection during the
  turn. Discovery itself does not require approval. Calls require Desktop
  approval unless the chat uses YOLO. A rejected or unreachable tool result
  can be handled by the model without aborting the entire chat.
- Local ACP receives configured MCP servers in the ACP session setup. When
  the agent lacks Streamable HTTP support, Desktop exposes those tools through
  a local stdio relay. Supported Claude Code and Codex CLIs receive the same
  scoped saved servers through native MCP configuration and use their own
  tool permission mechanisms. A relay wraps the tool list and tool call
  protocols, keeping the external credentials in turn-scoped private
  configuration and removing it when the turn exits.
- HTTP connections support Streamable HTTP with bearer credentials and reject
  redirects where Desktop owns the transport. Stdio children receive a narrow
  environment. Connections belong to their turn or to the explicit diagnostic
  check. Desktop-supplied external MCP servers are excluded from read-only,
  scheduled, and chat-only replies. Remote ACP sessions use the remote host's
  own MCP configuration. The built-in AFM bridge retains its scope and
  permission policy independently of external connectors.

## Focus styling

The Desktop shell and embedded dashboards use the same muted focus token.
Keyboard focus remains identifiable on buttons, inputs, and the chat composer.
Forced-colors mode uses the platform Highlight color. The embedded dashboard
loads the styling only for Desktop and uses the dashboard's active palette.

## Verification

- Use deterministic provider and MCP fixtures to exercise detection, advertised
  model controls, reply rejection, scoped stdio and HTTP discovery, approval
  before MCP invocation, and session reuse.
- Exercise the quick picker and delegate switching in the Desktop renderer tests,
  including stale probe responses and hiding a finished child after leaving it.
- Run the Desktop syntax checks, tests, performance checks, package inspection
  and macOS startup smoke test. The release workflow repeats these checks for
  its supported macOS and Linux architectures. Real account access to specific
  models and working external services remains dependent on local credentials.
