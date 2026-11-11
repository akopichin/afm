# Dashboard load fixture

A flow with a fake agent (no LLM) that streams tens of thousands of real feed events,
so dashboard memory and render cost on a long run can be measured and compared
between two builds. A real multi-hour run is not reproducible; this one takes about
10 minutes at the default rate.

Files:

- `flow.yaml` — stages `load` (the fake agent), `peer` (50 script lines, used to switch stages),
  `history` (a script group of `LOAD_HISTORY_LINES` lines, one `history line <k>` marker per line) and
  `history-report` (a fake agent that prints 150 short `report prelude line <k>` rows and ends, whose AI verify step, `fake_verifier.py`, writes a report of
  about 130 KB behind **Show full report**; a script stage cannot carry `verify`, hence two stages) and
  `witness` (an idle fake agent alive until the `stop` gate: the second recipient that lets the conversation
  use two mesh threads, since one thread holds at most 200 messages).
- `fake_agent.py` — prints claude stream-json: history shapes, then markdown messages (headings, lists,
  code fences, links, 1–3 KB, a few above 20 KB) and Bash/Read tool-call series. Every phase event
  carries exactly one `load #<n>` token in its visible text; `n` is numbered across phases 1–3.
- `fake_worker.py` — the controllable helper behind gate `worker`; `fake_verifier.py` and
  `fake_report_agent.py` serve `history-report`.
  The prelude rows put the verify row far below the top of the feed: open **Show full report** on it in
  stage `history-report`, scroll to the top to unmount the verify row, then scroll back to remount it.
- `config.yaml` — project config for the fixture; copy it to `<fixture>/.afm/config.yaml`.

## Run

Use an isolated directory and its own port so no real run is touched:

```sh
FIXTURE=$(mktemp -d)
mkdir -p "$FIXTURE/.afm" "$FIXTURE/gates"
cp scripts/dashboard-load/flow.yaml "$FIXTURE/"
cp -p scripts/dashboard-load/*.py "$FIXTURE/"   # -p keeps the executable bit
cp scripts/dashboard-load/config.yaml "$FIXTURE/.afm/config.yaml"
cd "$FIXTURE"
env -i HOME="$HOME" PATH="$PATH" AFM_SIDEAGENT=0 \
  afm run flow.yaml --dir "$FIXTURE" --port 18090
```

The server listens on `127.0.0.1` only. When the browser runs outside the container or
VM, put a TCP forwarder in front of it (for example `0.0.0.0:18080 → 127.0.0.1:18090`).

Parameters (environment variables of `afm run`):

| Variable | Default | Meaning |
|---|---|---|
| `LOAD_TOOL_SERIES` | 2000 | consecutive tool calls, each with one `series #<k>` marker; 0 turns the shape off |
| `LOAD_CONVERSATION` | 300 | agent-to-user messages (`afm agent send --kind message`), each with one `conv #<k>` marker, 150 per mesh thread (the second thread is with user + the `witness` stage); max 300; 0 = off |
| `LOAD_JSON_RESULT` | 1 | one message that is a v1 JSON result (the feed offers it under Raw); 0 = off |
| `LOAD_IMAGE` | 1 | the marker `[AFM image: delayed.png]`; a 1000×700 PNG appears in `$AFM_STAGE_DIR/artifacts/` about 1.5 s later; 0 = off |
| `LOAD_HISTORY_LINES` | 1500 | lines in stage `history`; keep it under the 2 000 events a stage keeps, so the whole group stays reachable |
| `LOAD_PHASE1` | 10000 | events in phase 1 |
| `LOAD_PHASE2` | 20000 | events in phase 2 |
| `LOAD_BOARD` | 400 | consecutive `afm agent board publish` calls (one long `mesh_board` group) |
| `LOAD_PHASE3` | 1000 | events in phase 3 (reconnect check) |
| `LOAD_RATE` | 50 | events per second |
| `LOAD_ANSWER_TIMEOUT` | 1800 | seconds to wait for the human answer before continuing |
| `LOAD_GATES_DIR` | `gates` | gate directory, relative to the fixture |

## Sequence and gates

A gate is an empty file in `gates/`. Create one with `touch "$FIXTURE/gates/<name>"`.
Stages `peer`, `history` and `history-report` finish in the first seconds; `witness` stays until `stop`.

1. The agent sends a mesh request to the user and waits. Open stage `load` in the dashboard and
   answer in the composer.
2. **Shapes** run in this order: the tool series, the conversation, the JSON result, the delayed
   image. A series comes first so the JSON result and the image stay in the stage buffer; the oldest
   series calls are pushed out by later events, which is expected.
3. **H1** — the agent waits for gate `phase1`. Check the shapes now: no `load #` event exists yet.
   If `phase1` already exists, there is no wait.
4. Phase 1 runs (`LOAD_PHASE1` events), then **H2**: the agent waits for gate `phase2`.
5. Phase 2 runs (`LOAD_PHASE2` events), then **H3**: waits for gate `finale`.
6. After `finale` the agent publishes `LOAD_BOARD` board entries (`load/1`, `load/2`, …) one
   after another, then replies to your earlier answer, which gives a fresh quote of an old post.
   It then waits for gate `phase3` (**H4**).
7. Phase 3 runs (`LOAD_PHASE3` events), writes `execution_summary.md` and waits for gate `stop`;
   the stage stays running until then.

Gate `stop` works at any waiting point and during streaming: the agent writes the summary and
exits. Gate `answered` skips the wait for the human answer; the agent then replies to its own
request, which afm rejects, and the fixture logs that and carries on.

Gate `worker`, created at any wait, starts a helper with `afm agent spawn --profile load-worker`.
Its process (`fake_worker.py`) shows as a working participant of stage `load`, so the feed shows
WorkingLine. Gate `worker-done` (or `stop`) finishes it with a valid advisory result. It needs
`config.yaml` in `.afm/` and a container (`/.dockerenv`): on a host afm allows helpers only with
the `codex-as-claude` command, and the fixture logs the refusal to stderr and continues.

The stream stops at the end of each phase, so heap measurements at 10k and 30k events are taken
at rest, after the page has processed everything.

Do not reload the page until the shape checks are done: a reload replays stage events, and the
replay keeps a different head of the history than the live client.

## Probes

Run in the dashboard page (browser console or an automation `evaluate_script`). Use the same
probes for every build being compared.

Counts. The feed is virtual, so count mounted rows, not the whole history:

```js
({
  documentNodes: document.getElementsByTagName('*').length,
  mountedRows: document.querySelectorAll('#feed-content [data-virtual-row-key]').length,
  mountedEvents: document.querySelectorAll('#feed-content [data-feed-item]').length,
})
```

Read markers range by range: scroll the feed through the history in steps, click the series
headers (`button.feed-tool-summary[aria-expanded="false"]`) inside the visible rows, collect the
markers of the mounted rows and deduplicate them across steps:

```js
const seen = new Set()
const collect = () => {
  document.querySelectorAll('#feed-content .feed-tool-summary[aria-expanded="false"]')
    .forEach((button) => button.click())
  const text = document.querySelector('#feed-content').textContent
  for (const m of text.matchAll(/(?:load|series|conv) #(\d+)|history line (\d+)/g)) seen.add(m[0])
}
// scroll #feed-content.feed-scroll by one viewport, wait a frame, call collect(), repeat
```

Compare the result with the server (the conversation is in `/api/mesh/stages/load/messages`):

```js
const events = await (await fetch('/api/events?stage=load')).json()
;[...JSON.stringify(events).matchAll(/series #(\d+)/g)].map((m) => +m[1])
```

Long tasks (install before the burst, read after 60 s):

```js
window.__longTaskMs = 0
new PerformanceObserver((list) => {
  list.getEntries().forEach((e) => { window.__longTaskMs += e.duration })
}).observe({ entryTypes: ['longtask'] })
```

Heap: take a heap snapshot from DevTools (Memory → Heap snapshot) or the Chrome DevTools MCP
`take_heapsnapshot`; both force a GC first. Sum the `self_size` of all nodes for the retained size.
Do not take an accessibility snapshot (`take_snapshot`) before it: it inflates the heap with
accessibility objects. `performance.memory` is too coarse to compare builds.

Payload sizes: take `/api/events` and sum `JSON.stringify(event).length` per `type`.
