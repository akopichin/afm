# Dashboard load fixture

A flow with a fake agent (no LLM) that streams tens of thousands of real feed events,
so dashboard memory and render cost on a long run can be measured and compared
between two builds. A real multi-hour run is not reproducible; this one takes about
10 minutes at the default rate.

Files:

- `flow.yaml` — stage `load` (the fake agent) and stage `peer` (50 script lines, used to switch stages).
- `fake_agent.py` — prints claude stream-json: markdown messages (headings, lists, code fences,
  links, 1–3 KB, a few above 20 KB) and Bash/Read tool-call series. Every load event carries
  exactly one `load #<n>` token in its visible text; `n` is numbered across phases 1–3.

## Run

Use an isolated directory and its own port so no real run is touched:

```sh
FIXTURE=$(mktemp -d)
mkdir -p "$FIXTURE/.afm" "$FIXTURE/gates"
cp scripts/dashboard-load/flow.yaml "$FIXTURE/"
cp -p scripts/dashboard-load/fake_agent.py "$FIXTURE/"   # -p keeps the executable bit
cat > "$FIXTURE/.afm/config.yaml" <<'YAML'
docker:
  enabled: false
browser:
  enabled: false
server:
  open_browser: false
collaboration:
  enabled: true
YAML
cd "$FIXTURE"
env -i HOME="$HOME" PATH="$PATH" AFM_SIDEAGENT=0 \
  afm run flow.yaml --dir "$FIXTURE" --port 18090
```

The server listens on `127.0.0.1` only. When the browser runs outside the container or
VM, put a TCP forwarder in front of it (for example `0.0.0.0:18080 → 127.0.0.1:18090`).

Parameters (environment variables of `afm run`):

| Variable | Default | Meaning |
|---|---|---|
| `LOAD_PHASE1` | 10000 | events in phase 1 |
| `LOAD_PHASE2` | 20000 | events in phase 2 |
| `LOAD_BOARD` | 400 | consecutive `afm agent board publish` calls (one long `mesh_board` group) |
| `LOAD_PHASE3` | 1000 | events in phase 3 (reconnect check) |
| `LOAD_RATE` | 50 | events per second |
| `LOAD_ANSWER_TIMEOUT` | 1800 | seconds to wait for the human answer before continuing |
| `LOAD_GATES_DIR` | `gates` | gate directory, relative to the fixture |

## Sequence and gates

A gate is an empty file in `gates/`. Create one with `touch "$FIXTURE/gates/<name>"`.

1. The agent sends a mesh request to the user and waits. Open stage `load` in the dashboard and
   answer in the composer.
2. Phase 1 runs (`LOAD_PHASE1` events), then the agent waits for gate `phase2`.
3. Phase 2 runs (`LOAD_PHASE2` events), then waits for gate `finale`.
4. After `finale` the agent publishes `LOAD_BOARD` board entries (`load/1`, `load/2`, …) one
   after another, then replies to your earlier answer, which gives a fresh quote of an old post.
   It then waits for gate `phase3`.
5. Phase 3 runs (`LOAD_PHASE3` events), writes `execution_summary.md` and waits for gate `stop`;
   the stage stays running until then.

Gate `stop` works at any waiting point and during streaming: the agent writes the summary and
exits. Gate `answered` skips the wait for the human answer (the agent then replies to its own
request).

The stream stops at the end of each phase, so heap measurements at 10k and 30k events are taken
at rest, after the page has processed everything.

## Probes

Run in the dashboard page (browser console or an automation `evaluate_script`). Use the same
probes for every build being compared.

Counts:

```js
({
  documentNodes: document.getElementsByTagName('*').length,
  feedNodes: document.querySelector('#feed-content')?.getElementsByTagName('*').length,
  feedRows: document.querySelectorAll(
    '#feed-content [data-feed-item], #feed-content ul.feed-conversation > li').length,
  usedJSHeapSize: performance.memory?.usedJSHeapSize,
})
```

Expand every collapsed tool group, then read the markers in DOM order:

```js
document.querySelectorAll('#feed-content details.feed-tool-group:not([open])')
  .forEach((d) => { d.open = true })
// after React has rendered the expanded rows:
[...document.querySelector('#feed-content').textContent.matchAll(/load #(\d+)/g)].map((m) => +m[1])
```

To check a range, expand only the groups inside it and compare with the server markers:

```js
const events = await (await fetch('/api/events?stage=load')).json()
[...JSON.stringify(events).matchAll(/load #(\d+)/g)].map((m) => +m[1])
```

Long tasks (install before the burst, read after 60 s):

```js
window.__longTaskMs = 0
new PerformanceObserver((list) => {
  list.getEntries().forEach((e) => { window.__longTaskMs += e.duration })
}).observe({ entryTypes: ['longtask'] })
```

Heap snapshot: take it from DevTools (Memory → Heap snapshot) or the Chrome DevTools MCP
`take_heapsnapshot`; both force a GC first. Sum the `self_size` of all nodes for the retained size.

Payload sizes: take `/api/events` and sum `JSON.stringify(event).length` per `type`.
