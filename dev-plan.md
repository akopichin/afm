# Managed Chrome DevTools Browser Capability — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give an afm stage agent a managed Chrome DevTools MCP (`afm_chrome`) that
drives a real, visible Chrome on the Mac host — so "сделай живой прогон" works
end-to-end both when the agent runs inside the afm Docker container and when afm
runs natively on the host, without the user launching Chrome, enabling SSH, or
wiring a CDP port by hand.

**Architecture:** One shared primitive — `pkg/browser.Manager` — owns a loopback
WebSocket listener, lazily launches a dedicated Chrome (separate profile,
`--remote-debugging-pipe`) on the host on the first browser tool call, and proxies
WebSocket frames ⇄ null-terminated CDP pipe messages. The agent always reaches
Chrome through a stdio launcher `afm browser mcp` that runs the pinned Chrome
DevTools MCP and connects to the manager over loopback WebSocket. Only three things
differ between the two run modes: the WebSocket endpoint host the MCP connects to
(Docker → `host.docker.internal`, native → `127.0.0.1`), how the tested app URL is
produced (Docker → the host picks a free loopback port and publishes it fixed,
`-p 127.0.0.1:<hostPort>:<appPort>`, before start; native → the app is served on the
host port directly), and where the MCP package comes from (Docker → pinned in the
image, native → the already-installed pinned package resolved on the host). The
manager is created and torn down
by `executeFlow` via `defer` in both modes.

**Tech Stack:** Go (new `pkg/browser`, reusing `github.com/gorilla/websocket`
v1.5.3 already in `go.mod`), Chrome DevTools MCP 1.10.1 (Node), Docker Desktop on
macOS, Chrome on macOS. First delivery hosts: **macOS + Docker Desktop + Chrome**
(Docker mode) **and macOS native** (non-Docker), agents `command: claude` /
claude-type recipes plus the Codex adapter.

**Spec:** `./dev-spec.md` (the user's research + design doc; the plan argues from
it and executors read both). The one scope change agreed after the spec was
written: **native (non-Docker) host mode is in this plan, not deferred** — it reuses
the same `pkg/browser` primitive (see Architecture).

## Global Constraints

- **Chrome DevTools MCP version is pinned to `1.10.1` everywhere** — `ARG
  CHROME_DEVTOOLS_MCP_VERSION=1.10.1` in `Dockerfile.runtime`; native resolution
  requires the **already-installed** `chrome-devtools-mcp` executable and verifies
  its version is `1.10.1` at preflight. Never `@latest`, never a runtime install —
  **no `npx -y` / `npm install` anywhere on the serving or preflight path** (`npx -y`
  silently downloads on a cache miss, which violates this).
- **Do NOT change the `go` version in `go.mod`.** Reuse `github.com/gorilla/websocket`
  v1.5.3 (already present); add no new third-party dependency without flagging it.
- **No transport secret ever appears in `docker run` argv** (visible in `ps aux`).
  The token reaches the container via bare `-e AFM_BROWSER_TRANSPORT_TOKEN` (value
  taken from the afm process env), exactly like lifecycle-hook / autoShim transport
  already does (`pkg/docker/launcher.go`). From there the container afm writes the
  token **only** into each per-invocation `0600` MCP descriptor file (consumed by
  `afm browser mcp --descriptor <path>`) — never into the agent's own process env, the
  generated `.mcp.json`, or any argv (see the env-isolation constraint below).
- **The tested-app host URL is known BEFORE `docker run` starts.** `cmd.Env` is fixed
  before `cmd.Start` and `docker run` then blocks until exit, so nothing can be
  injected into a running container. The host therefore picks a free loopback port
  itself, publishes it fixed (`-p 127.0.0.1:<hostPort>:<appPort>`), and bakes
  `AFM_BROWSER_APP_URL=http://127.0.0.1:<hostPort>` into the container env before
  start. No `docker inspect`/cidfile port-discovery, no post-start metadata channel.
- **Chrome is launched with `--remote-debugging-pipe`, never
  `--remote-debugging-port`** — no unauthenticated CDP TCP port is ever opened
  (spec "Что фактически проверено", point 13; the TCP variant failed with the Host
  header error).
- **The manager only ever kills Chrome processes and deletes profile dirs it
  created itself.** Never touch a user's existing Chrome windows/profiles.
- **An unauthenticated / unverified-token request must not launch Chrome** (spec
  points 2–4): HTTP and WebSocket handshakes without a valid token get 401 and do
  not start a browser.
- **Browser env isolation is centralized and default-deny.** Read-only filesystem
  does NOT stop an MCP from mutating an external app, so the capability must be scoped
  to normal author runners only. One sanitizer strips every `AFM_BROWSER_*` variable
  from **every** child process environment (author runners, interactive, fallback,
  AI verify, shell verify, JSON-fix, memory pipeline, `RunJSONQuery`, lifecycle
  hooks). Only after stripping does an author runner re-add the **non-secret**
  `AFM_BROWSER_APP_URL`, `AFM_BROWSER_APP_PORT`, and `AFM_BROWSER_DESCRIPTOR` (the
  `0600` descriptor-file *path*, which the codex adapter reads to build its `-c`
  overrides); the secret token is never re-added to an agent env — it lives only inside
  that descriptor file. Leaving `BrowserMCP` nil is NOT sufficient isolation, because
  the token is ambient in the container afm process and would otherwise be inherited.
- **Per-stage browser session identity is deterministic, not RPC-assigned.** The WS
  path is `/session/<epoch>/<stageID>`; the host `Manager` lazily creates the
  session (its own Chrome + profile) on the first authorized connect for that path.
  Both the host and the in-container `afm browser mcp` compute the same path from the
  descriptor + stage id, so no host-side registration call is needed from the
  container. One stage ⇒ one session/profile; retry/resume of the same stage id
  reconnect to the same session.
- **Comments and CHANGELOG are English; commits are Russian** (repo convention,
  `AGENTS.md` / CHANGELOG conventions). CHANGELOG uses the dated `## YYYY-MM-DD`
  section with `### Improvement:` / `### Fix:`, no keepachangelog.com reference.
- **SIGKILL of the host afm process is explicitly out of cleanup scope** — document
  the limitation and sweep stale owned profiles safely on the next start; do not add
  a watchdog or promise kill-safe cleanup.
- **Capability is opt-out-safe:** `browser` unset/nil → auto (tools offered on a
  supported host with Chrome; Chrome launches only on first use); `enabled: false`
  → fully off; `enabled: true` → preflight must succeed or the run fails (no silent
  skip). Auto mode with no Chrome/MCP must NOT break a CLI/API-only flow.

## Review Focus

These are the input classes the spec implies but that are easy to leave untested;
each line names the condition and the behavior a reasonable person expects, and the
task that must pin it with a test.

- **WebSocket handshake carrying a wrong/absent token** → 401, Chrome NOT launched
  (not just HTTP GET rejected). Pinned in Task 2.
- **A CDP pipe read returning a partial frame, or several NUL-terminated frames in
  one read** → the proxy reassembles by NUL boundaries, never splits/merges a JSON
  message. Pinned in Task 2.
- **Two stages' first browser call racing** → each gets its own Chrome/profile;
  concurrent first-connect to one session does a single lazy launch (singleflight),
  not two Chromes. Pinned in Task 2.
- **`server.port == browser.app_port`, but ONLY when the manager will actually
  start** → when the browser is `EnableOn` (or `EnableAuto` with deps available),
  preflight returns a clear config error before any launch; when `EnableOff` or
  `EnableAuto`-degraded (no Chrome/MCP), the identical port pair must NOT fail the
  run — a CLI/API-only project already using the dashboard on 18080 stays valid.
  Pinned in Task 1.
- **A verifier / JSON-fix / memory / `RunJSONQuery` / shell-verify invocation** → no
  `afm_chrome` MCP arg and no `AFM_BROWSER_*` env present (central strip, not a
  per-call nil check). Pinned in Task 5.
- **A stage with `browser: false`, and the interactive-fallback runner** → opt-out
  gets no capability; eligible fallback runner DOES get it (one policy helper, used
  everywhere). Pinned in Task 5.
- **`AFM_BROWSER=1` with `enabled:` unset (nil)** → preflight treats it as explicit
  enable (hard-fail on missing dep), without dereferencing a nil `*Enabled`. Pinned
  in Task 1.
- **An oversized unterminated CDP frame / a stalled or early-exiting Chrome** →
  bounded frame + read limits error out; launch/handshake deadline and cancellation
  fire; no unbounded memory growth or hung first tool call. Pinned in Task 2.
- **A stale AFM-owned profile left by a prior SIGKILL** → next start sweeps only
  provably-dead-owned dirs; a live manager's profiles are untouched. Pinned in Task 2.

---

## File Structure

New files:
- `pkg/config/browser.go` — `BrowserConfig`, defaults, supported-client check, host preflight.
- `pkg/browser/manager.go` — `Manager`, listener, token auth, deterministic per-stage sessions, lazy launch, startup stale-profile sweep.
- `pkg/browser/chrome.go` — Chrome process launch (pipe fds, owned profile dir + ownership marker), owned-process cleanup.
- `pkg/browser/proxy.go` — WebSocket ⇄ CDP-pipe framing (bounded) + CDP request-id remap.
- `pkg/browser/descriptor.go` — versioned runtime descriptor (base endpoint, token, app URL, filesystem root) + encode/decode for the transport; `StageEndpoint(stageID)` derives the per-stage WS URL.
- `pkg/browser/mcp.go` — resolve & launch the pinned Chrome DevTools MCP as a stdio child with a private config file.
- `cmd/afm/browser.go` — `afm browser mcp` and `afm browser status --json` subcommands.
- `cmd/afm/run_browser.go` — host-side lifecycle: build Manager, own it via defer, wire descriptor into both run modes.
- `assets/claude/skills/live-run/SKILL.md` — built-in live-run skill (variant B).

Modified files:
- `pkg/config/config.go` — add `Browser BrowserConfig` to `Config`; merge in `mergeFile`.
- `pkg/flow/flow.go` — add stage-level `Browser *bool` switch + validation.
- `schema/config.schema.json`, `schema/flow.schema.json`, `config.example.yaml` — document the new keys.
- `pkg/docker/launcher.go` — `ReExecConfig` fields for browser transport token (bare `-e`), base endpoint, and the host-chosen fixed app publish (`-p 127.0.0.1:<hostPort>:<appPort>`). No cidfile / `docker inspect`.
- `cmd/afm/run_environment.go`, `cmd/afm/run.go` — call the Task 4 lifecycle owner in both branches.
- `pkg/orchestrator/orchestrator.go` (`Options`) + `pkg/orchestrator/runner_factory.go` — one stage-aware policy helper that threads a per-stage browser descriptor into `executor.Config` for eligible author runners only (normal + interactive + fallback).
- `pkg/executor/executor.go` — `Config.BrowserMCP` field; claude `--mcp-config` injection; the central `AFM_BROWSER_*` strip applied to every child env (with non-secret re-add for author runners).
- `scripts/codex-as-claude.sh` — read the descriptor, add one-off `-c mcp_servers.afm_chrome.*` overrides; translate `mcp_tool_call` to a visible action.
- `Dockerfile.runtime` — pin + install `chrome-devtools-mcp@1.10.1`.
- `pkg/prompts/builder.go` — minimal runtime browser prompt block when capability is live.
- `docs/docker.md`, `docs/config-reference.md`, `README.md`, `CHANGELOG.md`.

---

## Task 1: Config & capability contract

**Files:**
- Create: `pkg/config/browser.go`
- Modify: `pkg/config/config.go:504` (add field to `Config`), `mergeFile` (copy new layer)
- Modify: `pkg/flow/flow.go` (add `Browser *bool` to `Stage`, validate)
- Modify: `schema/config.schema.json`, `schema/flow.schema.json`, `config.example.yaml`
- Test: `pkg/config/browser_test.go`, `pkg/flow/flow_test.go`

**Interfaces:**
- Produces:
  - `type BrowserConfig struct { Enabled *bool; Headless *bool; AppPort int; ExecutablePath string }` (yaml `enabled`/`headless`/`app_port`/`executable_path`, all `omitempty`).
  - `type EnableState int` with `EnableAuto`, `EnableOn`, `EnableOff`.
  - `func (b BrowserConfig) EffectiveEnable() EnableState` — resolves `AFM_BROWSER`
    env (`"1"→EnableOn`, `"0"→EnableOff`) over `enabled` (`nil→EnableAuto`,
    `true→EnableOn`, `false→EnableOff`). **This is the single source of truth**; no
    other code dereferences `*Enabled`, which removes the nil-deref when
    `AFM_BROWSER=1` and `enabled` is unset.
  - `func (b BrowserConfig) IsEnabled() bool` — `EffectiveEnable() != EnableOff`
    (auto and on both offer tools; preflight decides auto-vs-hard-fail).
  - `func (b BrowserConfig) GetAppPort() int` — default `18080`.
  - `func (c Config) BrowserSupportedFor(command string) bool` — true for `claude` and for a `docker.agents` recipe whose `RecipeType` is a claude-family or codex adapter (reuse the same resolution `verify_resolve.go` uses; do NOT string-match an alias).
  - `func (s flow.Stage) BrowserDisabled() bool` — explicit stage `browser: false`.

- [ ] **Step 1: Write the failing test — precedence, default port, port conflict**

```go
// pkg/config/browser_test.go
func TestBrowserConfig_EffectiveEnable(t *testing.T) {
	tr := true
	fa := false
	if (BrowserConfig{}).EffectiveEnable() != EnableAuto {
		t.Fatal("nil enabled should be auto")
	}
	if (BrowserConfig{Enabled: &fa}).EffectiveEnable() != EnableOff {
		t.Fatal("explicit false disables")
	}
	t.Setenv("AFM_BROWSER", "0")
	if (BrowserConfig{Enabled: &tr}).EffectiveEnable() != EnableOff {
		t.Fatal("AFM_BROWSER=0 overrides explicit true")
	}
}

func TestBrowserConfig_EnvOnWithNilEnabled_NoNilDeref(t *testing.T) {
	t.Setenv("AFM_BROWSER", "1")
	if (BrowserConfig{}).EffectiveEnable() != EnableOn { // Enabled is nil
		t.Fatal("AFM_BROWSER=1 with nil enabled must resolve to EnableOn without nil-deref")
	}
}

func TestBrowserConfig_DefaultAppPort(t *testing.T) {
	if (BrowserConfig{}).GetAppPort() != 18080 {
		t.Fatalf("want 18080, got %d", (BrowserConfig{}).GetAppPort())
	}
}

// Port collision is a *preflight* concern, gated on the browser actually starting —
// not an unconditional static-config error (that would break disabled/degraded flows).
func TestBrowserPreflight_PortConflict_OnlyWhenStarting(t *testing.T) {
	p := 18080
	tr, fa := true, false
	// EnableOn + same port → hard error.
	on := Config{Server: ServerConfig{Port: &p}, Browser: BrowserConfig{Enabled: &tr, AppPort: 18080}}
	if _, err := on.BrowserPreflight(BrowserModeNative); err == nil {
		t.Fatal("EnableOn + server.port==app_port must be a hard preflight error")
	}
	// EnableOff + same port → fully off, NO error (CLI/API-only project stays valid).
	off := Config{Server: ServerConfig{Port: &p}, Browser: BrowserConfig{Enabled: &fa, AppPort: 18080}}
	if _, err := off.BrowserPreflight(BrowserModeNative); err != nil {
		t.Fatalf("EnableOff must never fail on port collision, got %v", err)
	}
	// EnableAuto but deps unavailable → degrade (Available:false), NO error, even on collision.
	stubMCPVersionProbe(t, "" /* no chrome-devtools-mcp on PATH */)
	auto := Config{Server: ServerConfig{Port: &p}, Browser: BrowserConfig{AppPort: 18080}} // nil → auto
	st, err := auto.BrowserPreflight(BrowserModeNative)
	if err != nil || st.Available {
		t.Fatalf("auto-degraded must not fail the run on port collision: st=%+v err=%v", st, err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/config/ -run TestBrowserConfig -v`
Expected: FAIL — `BrowserConfig` / `ValidateBrowser` undefined.

- [ ] **Step 3: Write minimal implementation**

Add `BrowserConfig` + methods in `pkg/config/browser.go`; add `Browser BrowserConfig \`yaml:"browser"\`` to `Config` (near `Verify`, `config.go:527`); in `mergeFile` copy `Browser` fields (pointer-aware, like `Docker.FileBrowser.Enabled`). Implement `ValidateBrowser()` with **only** the always-safe static field sanity (negative/privileged `app_port`) and call it from the existing `Config.Validate`/`validate` path; it is a no-op when `EffectiveEnable() == EnableOff`. **The `GetAppPort() == server.GetPort()` collision is NOT checked here** — it moves into `BrowserPreflight` (Step 5), so it only fires when the manager will actually start (`EnableOn`, or `EnableAuto` with deps available); `EnableOff` / auto-degraded never fail on a shared port. Add `BrowserSupportedFor` delegating to the recipe-type resolver. Add `flow.Stage.Browser *bool` + `BrowserDisabled()`; `Flow.validate()` rejects nothing new beyond type (any stage may carry it).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/config/ ./pkg/flow/ -v`
Expected: PASS.

- [ ] **Step 5: Write the host-preflight test + implementation**

Preflight is one function used by both run modes (Task 4 calls it). Signature:
`func (c Config) BrowserPreflight(mode BrowserMode) (BrowserStatus, error)` where
`BrowserMode` is `BrowserModeDocker|BrowserModeNative` and
`BrowserStatus{Available bool; Reason string}`.

Rules (test each):
- Docker mode: MCP comes from the image → no host Node/MCP check; still needs Chrome
  resolvable (`ExecutablePath` or default `/Applications/Google Chrome.app/...`).
- Native mode: needs an **already-installed** `chrome-devtools-mcp` executable on
  PATH **whose reported version is `1.10.1`** (run `chrome-devtools-mcp --version`
  via the injected seam; a wrong/absent version is a missing prerequisite), **and**
  Chrome. **No install is ever attempted** — a missing package is reported, not fixed.
- Severity keys off `EffectiveEnable()`, not a raw pointer: `EnableOn` + missing dep
  → non-nil error (hard fail); `EnableAuto` + missing dep →
  `BrowserStatus{Available:false, Reason:...}` and `nil` error (degraded, CLI/API
  flow unaffected); `EnableOff` → `{Available:false}`, `nil` (never checks deps).
- **Port collision is a prerequisite checked here, not in `ValidateBrowser`:** when
  deps are otherwise satisfied (the manager would start) AND
  `GetAppPort() == server.GetPort()`, treat it exactly like a missing dep —
  `EnableOn` → hard error; `EnableAuto` → degrade (`Available:false`, Reason names the
  port clash); `EnableOff` → never reached. This keeps a disabled/degraded browser
  from breaking an existing project that already serves the dashboard on that port.

```go
func TestBrowserPreflight_AutoMissingChromeIsDegradedNotError(t *testing.T) {
	c := Config{Browser: BrowserConfig{ExecutablePath: "/nonexistent/chrome"}}
	st, err := c.BrowserPreflight(BrowserModeNative)
	if err != nil || st.Available {
		t.Fatalf("auto+missing: want degraded nil-err, got avail=%v err=%v", st.Available, err)
	}
}
func TestBrowserPreflight_ExplicitMissingChromeIsError(t *testing.T) {
	tr := true
	c := Config{Browser: BrowserConfig{Enabled: &tr, ExecutablePath: "/nonexistent/chrome"}}
	if _, err := c.BrowserPreflight(BrowserModeNative); err == nil {
		t.Fatal("explicit enable + missing chrome must error")
	}
}
func TestBrowserPreflight_NativeWrongMCPVersionIsMissing(t *testing.T) {
	tr := true
	c := Config{Browser: BrowserConfig{Enabled: &tr}}
	// seam reports chrome-devtools-mcp present but version 1.9.0
	st, err := c.browserPreflight(BrowserModeNative, seamWithMCPVersion("1.9.0", chromeOK))
	if err == nil || st.Available {
		t.Fatal("wrong pinned MCP version must count as a missing prerequisite (hard-fail when explicit)")
	}
}
func TestBrowserPreflight_DockerSkipsHostMCPCheck(t *testing.T) {
	tr := true
	c := Config{Browser: BrowserConfig{Enabled: &tr}}
	// no host MCP at all, but Docker mode provides it from the image
	st, err := c.browserPreflight(BrowserModeDocker, seamNoMCP(chromeOK))
	if err != nil || !st.Available {
		t.Fatalf("docker must not require host MCP; got avail=%v err=%v", st.Available, err)
	}
}
```

Implement with an injectable seam (`lookPath func(string) (string, error)`,
`statFile func(string) error`, `mcpVersion func() (string, error)`) defaulting to
`exec.LookPath`/`os.Stat`/`chrome-devtools-mcp --version`, so tests don't depend on
the real host. Update `schema/config.schema.json`, `schema/flow.schema.json`, and
`config.example.yaml` with the new keys and run `make schema-check`.

- [ ] **Step 6: Run the full config/flow/schema check**

Run: `go test ./pkg/config/ ./pkg/flow/ && make schema-check`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/config/browser.go pkg/config/config.go pkg/config/browser_test.go pkg/flow/flow.go pkg/flow/flow_test.go schema/ config.example.yaml
git commit -m "browser: конфиг capability и preflight (docker+native)"
```

---

## Task 2: `pkg/browser` core — manager, listener, lazy launch, CDP pipe proxy

**Files:**
- Create: `pkg/browser/manager.go`, `pkg/browser/chrome.go`, `pkg/browser/proxy.go`, `pkg/browser/descriptor.go`
- Test: `pkg/browser/manager_test.go`, `pkg/browser/proxy_test.go`

**Interfaces:**
- Consumes: `BrowserConfig` (Task 1).
- Produces:
  - `type Manager struct { ... }`; `func New(cfg Options) (*Manager, error)` where
    `Options{ChromePath string; Headless bool; Epoch string; LaunchTimeout time.Duration}`.
    `New` creates a loopback listener on an OS-assigned port, an owned profiles parent
    `<os.TempDir>/afm-browser/<epoch>/` (`0700`), and sweeps stale profiles (Step 9);
    it does **not** launch Chrome. `Epoch` is a host-generated random id (`"b-"+8 hex`)
    that `startBrowser` creates and passes in (Task 4) — not a run id.
  - `func (m *Manager) Addr() string` — `host:port` of the loopback listener.
  - `func (m *Manager) Token() string` — the transport token (random, created in `New`).
  - `func (m *Manager) Close() error` — bounded teardown: close listener, kill owned
    Chromes, remove owned profile dirs. Idempotent.
  - **No `RegisterStage` RPC.** Session identity is deterministic from the WS path
    `GET /session/{epoch}/{stageID}`: the handler upgrades iff `Authorization: Bearer
    <token>` matches, then the first authorized frame triggers a single lazy Chrome
    launch for that `(epoch,stageID)` session (its own profile under the owned
    parent); later connects to the same path reuse it (retry/resume reconnect). The
    host and the in-container `afm browser mcp` both compute this path from the
    descriptor + stage id — no cross-process registration call is needed. An
    unauthorized handshake never launches Chrome.

- [ ] **Step 1: Write the failing auth test (no token ⇒ 401, no Chrome)**

```go
// pkg/browser/manager_test.go
func TestManager_WSWithoutTokenIs401_NoChrome(t *testing.T) {
	launched := 0
	m := newTestManager(t, func() { launched++ }) // fake launcher counts launches; Epoch "ep1"
	url := "ws://" + m.Addr() + "/session/ep1/build"
	_, resp, err := websocket.DefaultDialer.Dial(url, nil) // no Authorization
	if err == nil {
		t.Fatal("want handshake failure")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %v", resp)
	}
	if launched != 0 {
		t.Fatal("Chrome must not launch on unauthorized handshake")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./pkg/browser/ -run TestManager_WSWithoutToken -v`
Expected: FAIL — package/`New` undefined.

- [ ] **Step 3: Minimal manager + listener + auth**

Implement `New` (loopback `net.Listen("tcp", "127.0.0.1:0")`, random 32-byte token
hex, `os.MkdirTemp` profiles root `0700`), an `http.Server` with the upgrade handler
that checks the bearer token before `Upgrader.Upgrade`, and `Close`. The launcher is
an injectable field (`launch func(sessionID string) (*chromeConn, error)`) so tests
substitute a fake Chrome; production wires `chrome.go`.

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./pkg/browser/ -run TestManager_WSWithoutToken -v`
Expected: PASS.

- [ ] **Step 5: Write the proxy framing + id-remap tests (fake pipe)**

`proxy.go` converts WebSocket text frames ⇄ NUL-terminated CDP pipe messages and
remaps CDP `id` fields so a reconnect's local ids never collide with a prior
connection's in-flight ids.

```go
// pkg/browser/proxy_test.go
func TestPipeFramer_SplitsOnNUL_AcrossReads(t *testing.T) {
	// one Read returns 1.5 messages, next read the remainder
	fr := newPipeFramer()
	got := fr.feed([]byte(`{"id":1}` + "\x00" + `{"id":`))
	got = append(got, fr.feed([]byte(`2}` + "\x00")...)...)
	if len(got) != 2 || string(got[0]) != `{"id":1}` || string(got[1]) != `{"id":2}` {
		t.Fatalf("framing wrong: %q", got)
	}
}

func TestProxy_RemapsIDsAcrossReconnect(t *testing.T) {
	px := newProxy()
	outA := px.fromClient(connA, []byte(`{"id":1,"method":"Page.enable"}`))
	// outA carries a global id; response routed back to connA as local id 1
	resp := px.fromChrome([]byte(idOf(outA) + `-result`))
	if !routedTo(resp, connA, /*localID=*/1) {
		t.Fatal("response must route to connA local id 1")
	}
	// connB reusing local id 1 must not receive connA's pending reply
	px.fromClient(connB, []byte(`{"id":1,"method":"Page.enable"}`))
	if deliveredToWrongConn(px) {
		t.Fatal("local id collision leaked across connections")
	}
}
```

- [ ] **Step 6: Implement proxy framing + id remap, run tests**

Implement `pipeFramer` (buffer that splits on `\x00`), and a `proxy` that keeps a
per-connection `localID → globalID` table and a reverse map for routing Chrome
responses back. Drop pending requests when a connection disconnects.

Run: `go test ./pkg/browser/ -run 'TestPipeFramer|TestProxy' -v`
Expected: PASS.

- [ ] **Step 7: Write the lazy-launch singleflight + per-stage isolation tests**

```go
func TestManager_ConcurrentFirstConnect_SingleLaunch(t *testing.T) {
	var launches int32
	m := newTestManager(t, func() { atomic.AddInt32(&launches, 1) }) // Epoch "ep1"
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); dialAuthed(t, m, "ep1", "build") }()
	}
	wg.Wait()
	if atomic.LoadInt32(&launches) != 1 {
		t.Fatalf("want 1 lazy launch, got %d", launches)
	}
}

func TestManager_TwoStages_SeparateProfiles(t *testing.T) {
	m := newTestManager(t, nil) // Epoch "ep1"
	dialAuthed(t, m, "ep1", "build"); dialAuthed(t, m, "ep1", "test")
	if profileDirOf(m, "ep1", "build") == profileDirOf(m, "ep1", "test") {
		t.Fatal("stages must not share a Chrome profile")
	}
}

func TestManager_SameStageReconnect_ReusesSession(t *testing.T) {
	var launches int32
	m := newTestManager(t, func() { atomic.AddInt32(&launches, 1) })
	dialAuthed(t, m, "ep1", "build")
	dialAuthed(t, m, "ep1", "build") // retry/resume: same path
	if atomic.LoadInt32(&launches) != 1 {
		t.Fatalf("reconnect to same stage must reuse Chrome, got %d launches", launches)
	}
}
```

Implement with a `sync.Map` keyed by `epoch/stageID`, each session guarding its
launch with a `sync.Once`/singleflight; `chrome.go` gives each session its own
profile under the owned parent and an `exec.Cmd` with `ExtraFiles` for pipe fds 3/4
and `--remote-debugging-pipe` (no port). `Close` kills only these owned processes.

- [ ] **Step 8: Write resource-bound tests + implement**

The proxy and launch must be bounded so a malformed/stalled Chrome cannot grow memory
unboundedly or hang the first tool call.

```go
func TestPipeFramer_OversizedUnterminatedFrameErrors(t *testing.T) {
	fr := newPipeFramer() // maxFrameBytes e.g. 8 MiB
	_, err := fr.feedChecked(bytes.Repeat([]byte("A"), maxFrameBytes+1)) // no NUL
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatal("unterminated oversized frame must error, not buffer forever")
	}
}
func TestManager_ChromeLaunchDeadline(t *testing.T) {
	m := newTestManager(t, blockForever) // fake launcher never returns
	m.launchTimeout = 50 * time.Millisecond
	_, err := m.firstConnect(context.Background(), "ep1", "build")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("launch must time out, got %v", err)
	}
}
func TestManager_ClientCancelReleasesConnect(t *testing.T) {
	m := newTestManager(t, blockForever)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := m.firstConnect(ctx, "ep1", "build"); err == nil {
		t.Fatal("client cancellation must abort the pending launch")
	}
}
```

Implement: `const maxFrameBytes = 8 << 20`; `conn.SetReadLimit(maxFrameBytes)` on the
WebSocket; `pipeFramer.feedChecked` returns `ErrFrameTooLarge` past the cap; a
per-session **bounded** write queue (buffered channel) drained by one serialized
writer goroutine — a full queue applies backpressure / drops the slow session with a
logged error rather than growing; `firstConnect` runs the lazy launch under a context
bounded by `LaunchTimeout` and the client's ctx, and tears the half-started session
down on timeout/cancel. Logs never contain the token or config contents.

- [ ] **Step 9: Write the stale-profile sweep test + implement**

SIGKILL skips `Close`, leaving owned profile dirs behind. The next `New` sweeps only
provably-dead-owned dirs.

```go
func TestManager_SweepsStaleOwnedProfiles(t *testing.T) {
	parent := t.TempDir()
	stale := writeOwnedProfile(t, parent, "sess-dead", /*pid=*/deadPID(t)) // marker pid not alive
	live := writeOwnedProfile(t, parent, "sess-live", /*pid=*/os.Getpid()) // marker pid alive
	sweepStaleProfiles(parent, isAlive) // called by New
	if dirExists(stale) {
		t.Fatal("stale owned profile must be removed")
	}
	if !dirExists(live) {
		t.Fatal("a live manager's profile must NOT be touched")
	}
}
```

Implement: each session profile dir gets an `owner.json` marker (`{pid, epoch}`)
written at launch. `sweepStaleProfiles(parent, isAlive)` scans the owned parent,
reads each marker, and `RemoveAll`s only dirs whose owner pid is not alive
(`isAlive` = `syscall.Kill(pid, 0)` on unix). Dirs without a readable marker are left
untouched (conservative). This is the honest counterpart to the SIGKILL limitation —
no watchdog, best-effort sweep on next start.

- [ ] **Step 10: Run the whole package**

Run: `go test ./pkg/browser/ -race -v`
Expected: PASS. (Real-Chrome smoke is a separate opt-in test, not a unit test — see Task 7.)

- [ ] **Step 11: Commit**

```bash
git add pkg/browser/
git commit -m "browser: manager, loopback listener, ленивый запуск Chrome, CDP pipe-прокси"
```

---

## Task 3: Stdio MCP launcher & image packaging

**Files:**
- Create: `cmd/afm/browser.go`, `pkg/browser/mcp.go`
- Modify: `cmd/afm/main.go` (register `browser` command), `Dockerfile.runtime`
- Test: `cmd/afm/browser_test.go`, `pkg/browser/mcp_test.go`

**Interfaces:**
- Consumes: the per-invocation `0600` descriptor file written by the executor (Task 5),
  passed as `afm browser mcp --descriptor <path>`, holding `{wsEndpoint, token, appURL,
  roots}`. The token comes from this file, never from env or argv.
- Produces:
  - `afm browser mcp --descriptor <path>` — reads the descriptor file, then launches the
    pinned Chrome DevTools MCP as a child with `--config=<private 0600 file>` holding
    `wsEndpoint`, `wsHeaders`, and `filesystemRoot` derived from `roots`. **`wsHeaders`
    is a JSON-encoded *string*, not an object** — pinned chrome-devtools-mcp 1.10.1
    declares `wsHeaders` as a `string` that it then `JSON.parse`s (`src/config/
    browser-options.ts`, confirmed by its `tests/cli.test.ts`), so the private config
    must carry `wsHeaders: "{\"Authorization\":\"Bearer <token>\"}"` (the serialized
    form); writing a bare JSON object makes config parsing fail before the MCP starts.
    A Task 3 test asserts the generated file round-trips through the pinned MCP's config
    parser (or the executable) with a string `wsHeaders`. These roots allow screenshots
    into the stage `artifacts/` and reads under the effective `flow.root_dir`, even when
    afm-root ≠ `root_dir`. MCP
    stdio is piped straight through; afm's own stdout stays clean (no banner). On broken
    pipe / SIGINT the child is terminated; the private config **and** the descriptor
    file are removed on exit.
  - `afm browser status --json` — prints `{available, app_url, reason}` **without**
    launching Chrome and **without** printing any credential.
  - `afm browser protocol-version` — prints the single integer
    `browser.TransportProtocolVersion` (a compiled-in const, start at `1`) and exits 0.
    This is the host↔container transport-compatibility probe target (Task 4): the host
    runs it inside the configured image before committing to `docker run`. An older
    image either prints a different integer or lacks the subcommand entirely (non-zero
    exit / unknown command) — both are treated as "incompatible" by the host probe.
  - `func ResolveMCP(mode BrowserMode) (cmd string, args []string, err error)` in
    `mcp.go`: Docker → the image-installed `chrome-devtools-mcp` binary; native → the
    **already-installed** `chrome-devtools-mcp` executable resolved via `exec.LookPath`
    (never `npx`, never an install). Absent/wrong-version → a clear error naming the
    required pinned version (the Task 1 preflight already blocks this earlier; this is
    defense in depth, not an install path).

- [ ] **Step 1: Write the failing test — status never prints the token**

```go
// cmd/afm/browser_test.go
func TestBrowserStatus_JSON_NoSecret(t *testing.T) {
	out := runBrowserStatus(t, descriptorWithToken("super-secret-xyz"))
	if strings.Contains(out, "super-secret-xyz") {
		t.Fatal("status must never leak the transport token")
	}
	var s struct{ Available bool `json:"available"`; AppURL string `json:"app_url"` }
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("status must be valid JSON: %v", err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./cmd/afm/ -run TestBrowserStatus -v`
Expected: FAIL — subcommand missing.

- [ ] **Step 3: Implement the subcommands + ResolveMCP**

Add the `browser` cobra command with `mcp` and `status` subcommands in
`cmd/afm/browser.go`; register in `main.go`. `mcp` writes the private config
(`0600`, in a `0700` temp dir), execs the resolved MCP with stdio inherited, and on
exit removes the config. `status` reads the descriptor + runs a no-launch check.

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./cmd/afm/ -run TestBrowserStatus -v`
Expected: PASS.

- [ ] **Step 5: Write the private-config-cleanup test + implement**

```go
func TestBrowserMCP_RemovesPrivateConfigOnExit(t *testing.T) {
	dir := t.TempDir()
	cfgPath := startBrowserMCPWithFakeChild(t, dir) // child exits immediately
	waitChildExit(t)
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Fatalf("private MCP config must be deleted on exit, stat err=%v", err)
	}
}
```

Use an injectable child command (a fake `sh -c 'exit 0'`) so the test needs no real
MCP. Verify `0600` mode on the config while alive.

- [ ] **Step 6: Pin + install the MCP in the image**

Edit `Dockerfile.runtime` (near the pinned `CLAUDE_CODE_VERSION`/`CODEX_VERSION`):
```dockerfile
ARG CHROME_DEVTOOLS_MCP_VERSION=1.10.1
RUN npm install -g chrome-devtools-mcp@${CHROME_DEVTOOLS_MCP_VERSION}
```
Document the pin in `AGENTS.md` "Пины версий в образе — бампить руками".

- [ ] **Step 7: Run package + build the image**

Run: `go test ./cmd/afm/ ./pkg/browser/ -v && docker build -f Dockerfile.runtime -t afm-browser-test .`
Expected: PASS and a successful image build.

- [ ] **Step 8: Commit**

```bash
git add cmd/afm/browser.go cmd/afm/browser_test.go cmd/afm/main.go pkg/browser/mcp.go pkg/browser/mcp_test.go Dockerfile.runtime AGENTS.md
git commit -m "browser: stdio-лаунчер afm browser mcp/status + пин Chrome DevTools MCP в образе"
```

---

## Task 4: Host lifecycle & app access (Docker + native)

**Files:**
- Create: `cmd/afm/run_browser.go`
- Modify: `cmd/afm/run.go:88` (`executeFlow`), `cmd/afm/run_environment.go:75` (`reexecFlow`), `pkg/docker/launcher.go:36` (`ReExecConfig`) / `ReExec`
- Test: `cmd/afm/run_browser_test.go`, `pkg/docker/launcher_test.go`

**Interfaces:**
- Consumes: `BrowserConfig`, `BrowserPreflight` (Task 1), `browser.Manager` (Task 2).
- Produces:
  - `func startBrowser(cfg config.Config, mode BrowserMode) (*browser.Manager, *browser.Descriptor, func(), error)`
    — nil manager + nil descriptor + no-op cleanup when the capability is off/degraded;
    otherwise a live manager, a runtime descriptor, and a `defer`-able cleanup. Called
    from `executeFlow` (native) and before the Docker branch. **`startBrowser`
    generates its own random epoch** (`"b-"+8 hex`) — it does NOT take a run id.
    `reexecFlow` has no run id (the run is resolved later, inside the container by
    `resolveRun`), and native resume starts a fresh manager anyway, so a per-process
    random epoch is both sufficient (sessions/Chrome never outlive the process) and
    the only value available at this point. The epoch is returned in the descriptor
    and (Docker) forwarded via `-e AFM_BROWSER_EPOCH`.
  - `browser.Descriptor{Version int; Endpoint string; Token string; AppURL string;
    Epoch string}`; `Descriptor.StageEndpoint(stageID)` →
    `ws://<Endpoint>/session/<Epoch>/<stageID>`. The in-container afm rebuilds the
    descriptor from `AFM_BROWSER_*` env (`AFM_BROWSER_ENDPOINT`,
    `AFM_BROWSER_TRANSPORT_TOKEN`, `AFM_BROWSER_APP_URL`, `AFM_BROWSER_EPOCH`), gated on
    `config.ReExecedIntoContainer()`. **Filesystem roots are NOT in the descriptor** —
    they are computed at injection time (Task 5) from the effective `RootDir` + stage
    dir (which the orchestrator holds, in the correct container-or-host path space) and
    written into the per-invocation `0600` descriptor file consumed by `afm browser
    mcp`.
  - `ReExecConfig` gains: `BrowserEnabled bool`, `BrowserToken string` (sent bare
    `-e AFM_BROWSER_TRANSPORT_TOKEN`), `BrowserEndpoint string`
    (`host.docker.internal:<managerPort>`), `BrowserEpoch string`, `BrowserAppHostPort
    int` and `BrowserAppContainerPort int` (→ one fixed `-p
    127.0.0.1:<hostPort>:<containerPort>` publish), `BrowserAppURL string`
    (`http://127.0.0.1:<hostPort>`, baked as `-e AFM_BROWSER_APP_URL` before start).
  - **No post-start channel.** The host picks a free loopback TCP port on the host
    *before* `docker run`, uses it as `BrowserAppHostPort`, and bakes the resulting
    `AFM_BROWSER_APP_URL` into the container env up front (see the Global Constraint).
  - `func probeImageBrowserProtocol(image string) (int, error)` — Docker mode only,
    and only when the browser would actually start. Runs `docker run --rm <image>
    browser protocol-version` — **args only, NO leading `afm`**: the runtime image's
    `ENTRYPOINT` is `docker-entrypoint.sh`, which `exec`s `/usr/local/bin/afm "$@"`
    (`docker-entrypoint.sh:36`), so Docker args are already appended to `afm`. Passing
    `afm browser protocol-version` would run `afm afm browser protocol-version` (the
    first `afm` an invalid subcommand) and reject every compatible image. It then
    parses the single integer and compares it against the
    host's compiled-in `browser.TransportProtocolVersion`. **Mismatch / unknown
    subcommand / non-zero exit → "incompatible image".** Severity mirrors
    `EffectiveEnable()`: `EnableOn` → a clear hard error naming host vs image version
    and the fix ("rebuild/pull the matching afm image"), aborting before `docker run`;
    `EnableAuto` → degrade (skip the browser, log a one-line warning, run continues).
    This satisfies dev-spec.md:361-362 ("несовместимый transport protocol должен дать
    понятную ошибку, а не зависание старого pinned image"). Native mode needs no probe
    (host and launcher are the same binary, same version). The probe runs once, after
    `BrowserPreflight` says the manager will start and before `ReExec`.

- [ ] **Step 1: Write the failing test — Docker argv never contains the token value**

```go
// pkg/docker/launcher_test.go
func TestReExec_BrowserTokenNotInArgv(t *testing.T) {
	var gotArgv []string
	restore := swapExecFunc(func(a0 string, argv, env []string) error { gotArgv = argv; return nil })
	defer restore()
	_ = ReExec(ReExecConfig{Image: "img", ProjectDir: "/p",
		BrowserEnabled: true, BrowserToken: "tok-SECRET", BrowserEndpoint: "host.docker.internal:5001",
		BrowserEpoch: "ep1", BrowserAppHostPort: 54321, BrowserAppContainerPort: 18080,
		BrowserAppURL: "http://127.0.0.1:54321"})
	for _, a := range gotArgv {
		if strings.Contains(a, "tok-SECRET") {
			t.Fatalf("token leaked into argv: %q", a)
		}
	}
	if !argvHasBareEnv(gotArgv, "AFM_BROWSER_TRANSPORT_TOKEN") {
		t.Fatal("expected bare -e AFM_BROWSER_TRANSPORT_TOKEN")
	}
	if !argvHasValuedEnv(gotArgv, "AFM_BROWSER_APP_URL", "http://127.0.0.1:54321") {
		t.Fatal("expected app URL baked into env before start")
	}
	if !argvHasPublishFixed(gotArgv, "127.0.0.1", 54321, 18080) {
		t.Fatal("expected fixed loopback app publish 127.0.0.1:54321:18080")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./pkg/docker/ -run TestReExec_BrowserToken -v`
Expected: FAIL — fields undefined.

- [ ] **Step 3: Implement ReExec transport wiring**

Add the fields; when `BrowserEnabled`, emit bare `-e AFM_BROWSER_TRANSPORT_TOKEN`
(value set in the afm process env before exec, mirroring the hook-secret transport),
valued `-e AFM_BROWSER_ENDPOINT=<host.docker.internal:managerPort>`, `-e
AFM_BROWSER_EPOCH=<epoch>`, valued `-e AFM_BROWSER_APP_URL=http://127.0.0.1:<hostPort>`,
and the fixed publish `-p 127.0.0.1:<hostPort>:<containerPort>`. No cidfile, no
`docker inspect`.

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./pkg/docker/ -run TestReExec_BrowserToken -v`
Expected: PASS.

- [ ] **Step 5: Write the native lifecycle-ownership test + implement `run_browser.go`**

```go
// cmd/afm/run_browser_test.go
func TestStartBrowser_NativeOwnsAndCleansUp(t *testing.T) {
	m, d, cleanup, err := startBrowser(nativeEnabledConfig(t), BrowserModeNative)
	if err != nil || m == nil { t.Fatalf("want live manager, err=%v", err) }
	if !strings.HasPrefix(d.Endpoint, "127.0.0.1:") {
		t.Fatalf("native endpoint must be loopback, got %q", d.Endpoint)
	}
	if d.Epoch == "" { t.Fatal("startBrowser must generate a non-empty epoch") }
	if d.AppURL != "http://127.0.0.1:18080" { // native: app served on the host port directly, no publish
		t.Fatalf("native app URL wrong: %q", d.AppURL)
	}
	cleanup()
	if managerStillListening(m) { t.Fatal("cleanup must close the manager") }
}

func TestStartBrowser_OffWhenDisabled(t *testing.T) {
	fa := false
	m, _, cleanup, err := startBrowser(config.Config{Browser: config.BrowserConfig{Enabled: &fa}}, BrowserModeNative)
	if err != nil || m != nil { t.Fatalf("disabled must be nil manager") }
	cleanup() // must be a safe no-op
}
```

Wire `startBrowser` into `executeFlow` (native) with `defer cleanup()`, and into the
Docker branch (`run_environment.go` / `run.go`) so the manager is created on the host
before `docker run` and its descriptor flows through `ReExecConfig`. **The manager
must already be serving while `defaultExecFunc` blocks waiting on Docker** (it
already runs Docker as a child and waits — `launcher.go:140`), otherwise the first
in-container browser call deadlocks.

- [ ] **Step 6: Write the host-port-pick + in-container descriptor test + implement**

The host picks a free loopback port *before* `docker run` (so the app URL is known up
front — no post-start injection), and the in-container afm rebuilds the descriptor
from env only when `config.ReExecedIntoContainer()`.

```go
func TestPickFreeLoopbackPort_IsBindable(t *testing.T) {
	p, err := pickFreeLoopbackPort()
	if err != nil { t.Fatalf("pick failed: %v", err) }
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)) // TOCTOU window is tiny; a late bind failure is a clear startup error
	if err != nil { t.Fatalf("picked port not bindable: %v", err) }
	_ = l.Close()
}

func TestDescriptorFromEnv_OnlyInContainer(t *testing.T) {
	t.Setenv("AFM_BROWSER_ENDPOINT", "host.docker.internal:5001")
	t.Setenv("AFM_BROWSER_TRANSPORT_TOKEN", "tok")
	t.Setenv("AFM_BROWSER_APP_URL", "http://127.0.0.1:54321")
	t.Setenv("AFM_BROWSER_EPOCH", "ep1")
	if d := descriptorFromEnv(/*inContainer=*/false); d != nil {
		t.Fatal("host must not build a transport descriptor from env")
	}
	d := descriptorFromEnv(true)
	if d == nil || d.StageEndpoint("build") != "ws://host.docker.internal:5001/session/ep1/build" {
		t.Fatalf("in-container descriptor wrong: %+v", d)
	}
}
```

Implement `pickFreeLoopbackPort` (listen on `127.0.0.1:0`, read the port, close) and
`descriptorFromEnv(inContainer bool)`. In `reexecFlow`, call `startBrowser(cfg,
BrowserModeDocker)` to get the manager + base endpoint + **host-generated epoch**
(`descriptor.Epoch` — `reexecFlow` has no run id, so the epoch comes from
`startBrowser`, not from a run), pick the host app port, and fill the new
`ReExecConfig` fields (`BrowserEpoch = descriptor.Epoch`). In the in-container
`executeFlow`, build the descriptor via
`descriptorFromEnv(config.ReExecedIntoContainer())` and pass it to the orchestrator
`Options`. Add tests for both a fresh and a resumed Docker path (both start a fresh
host manager + fresh epoch — Chrome never outlives the host process).

- [ ] **Step 7: Write the image protocol-version probe test + implement**

The probe guards the host↔container transport contract (dev-spec.md:361-362): an old
pinned image must fail with a clear error (or degrade in auto), never hang.

```go
func TestProbeImageBrowserProtocol_Compatibility(t *testing.T) {
	// The docker-exec seam captures the real argv AND returns stubbed output/exit.
	var gotArgv []string
	withDockerExecSeam(t, func(argv []string) (string, int) {
		gotArgv = argv
		return fmt.Sprintf("%d\n", browser.TransportProtocolVersion), 0
	})
	v, err := probeImageBrowserProtocol("img")
	if err != nil || v != browser.TransportProtocolVersion {
		t.Fatalf("matching image must be compatible: v=%d err=%v", v, err)
	}
	// Argv assertion: the entrypoint prepends `afm`, so the probe must pass ONLY
	// `browser protocol-version` — never a leading `afm` that would become `afm afm …`.
	if a := argvAfterImage(gotArgv, "img"); !reflect.DeepEqual(a, []string{"browser", "protocol-version"}) {
		t.Fatalf("probe must run exactly `browser protocol-version` (no leading afm), got %q", a)
	}
}

func TestProbeImageBrowserProtocol_IncompatibleSeverity(t *testing.T) {
	// Older image: different integer → EnableOn aborts with a clear error before docker run.
	withDockerExecSeam(t, func([]string) (string, int) { return "0\n", 0 })
	if err := startBrowserForDockerOrError(t, enableOnDockerCfg(t)); err == nil ||
		!strings.Contains(err.Error(), "protocol") {
		t.Fatalf("version mismatch under EnableOn must be a clear hard error, got %v", err)
	}
	// Older image with no subcommand → non-zero exit; EnableAuto degrades (no error, no browser).
	withDockerExecSeam(t, func([]string) (string, int) { return "unknown command \"protocol-version\"\n", 1 })
	m, _, _, err := startBrowser(autoDockerCfg(t), BrowserModeDocker)
	if err != nil || m != nil {
		t.Fatalf("auto + incompatible image must degrade silently, got m=%v err=%v", m, err)
	}
}
```

Implement `probeImageBrowserProtocol` (via the injected docker-exec seam) and call it
from the Docker branch of `startBrowser`/`reexecFlow` right after `BrowserPreflight`
reports the manager will start and before `ReExec`. The exec runs `docker run --rm
<image> browser protocol-version` — **args only; the image `ENTRYPOINT`
(`docker-entrypoint.sh:36`) already `exec`s `/usr/local/bin/afm "$@"`**, so a leading
`afm` would duplicate to `afm afm …` and fail. Define `browser.TransportProtocolVersion`
(const `1`) and the `afm browser protocol-version` subcommand in Task 3; bump the const
only on a breaking transport change.

- [ ] **Step 8: Run tests**

Run: `go test ./cmd/afm/ ./pkg/docker/ -v`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add cmd/afm/run_browser.go cmd/afm/run_browser_test.go cmd/afm/run.go cmd/afm/run_environment.go pkg/docker/launcher.go pkg/docker/launcher_test.go
git commit -m "browser: host-владелец manager (docker+native), фиксированный app-порт, descriptor из env, probe версии протокола образа"
```

---

## Task 5: Executor & adapter injection

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go:63` (`Options`), `pkg/orchestrator/runner_factory.go:34` (`runnerFor`, `runnerForFallback`, `runnerForVerify`)
- Modify: `pkg/executor/executor.go:109` (`Config`), arg build (`:819`), env filter (`:850`+), `RunJSONQuery` env (`:767`+)
- Modify: `scripts/codex-as-claude.sh`, `pkg/lifecyclehooks/runner.go` (`stripTransportVars`)
- Test: `pkg/executor/executor_test.go`, `pkg/orchestrator/runner_factory_test.go`, `pkg/lifecyclehooks/runner_test.go`, a codex-adapter arg test

**Interfaces:**
- Consumes: runtime descriptor (Task 2/4). `Options` gains
  `BrowserDescriptor *browser.Descriptor` (nil = capability off).
- Produces:
  - `executor.Config.BrowserMCP *BrowserMCP` where `type BrowserMCP struct {
    ServerName string /* "afm_chrome" */; LauncherPath string /* abs afm */;
    WSEndpoint string /* per-stage ws:// */; Token string; AppURL string; AppPort int;
    Roots []string /* filesystem roots */ }`.
  - **Transport to the MCP server is a per-invocation `0600` descriptor file + a
    non-secret `--descriptor <path>` arg — NOT env vars and NOT the token in argv.**
    At invocation the executor writes a `0600` JSON file (in the existing `WrapperDir`/
    temp discipline) holding `{wsEndpoint, token, appURL, roots}`, then launches
    `afm browser mcp --descriptor <path>` as the `afm_chrome` server. `afm browser mcp`
    (Task 3) reads that file, builds the private Chrome-DevTools-MCP config
    (`wsEndpoint`/`wsHeaders` as a JSON-encoded string `"{\"Authorization\":\"Bearer <token>\"}"`/`filesystemRoot` from
    `roots`), and deletes both files on exit. The token thus never enters the agent
    env **or** argv — only the descriptor path (non-secret) appears.
  - `func (o *Orchestrator) browserForStage(s flow.Stage, cmd string) *executor.BrowserMCP`
    — the single stage-aware policy helper. Returns nil when: descriptor is nil,
    `s.BrowserDisabled()`, or `!o.opts.Config.BrowserSupportedFor(cmd)`. Otherwise
    builds the `BrowserMCP` with `desc.StageEndpoint(s.ID)` and
    `Roots = dedup(effectiveRoot, stageDir)` computed from `o.opts.RootDir` (effective
    `flow.root_dir`, may differ from afm-root) and the stage dir
    `filepath.Join(o.opts.RunDir, s.ID)` (where the agent writes `artifacts/`). Used by
    `runnerFor` (both branches), `runnerForFallback`, **and** the prompt builder
    (Task 6) — one policy, no copy-paste.
  - claude family: write the per-invocation `.mcp.json` whose `afm_chrome` server entry
    is `command=<LauncherPath>` (the resolved abs afm path, never the bare literal
    `afm`), `args=["browser","mcp","--descriptor","<path>"]`, and append
    `--mcp-config <that file>` (+ keep the user's existing
    `--mcp-config`/`--strict-mcp-config`; never edit `~/.claude.json`/`.mcp.json`).
  - codex: the adapter adds one-off `-c mcp_servers.afm_chrome.command="$AFM_BROWSER_LAUNCHER"`
    (the **resolved absolute afm path** — see below) and
    `-c mcp_servers.afm_chrome.args=["browser","mcp","--descriptor","<path>"]`
    (a **sequence** — pinned codex 0.155.1 rejects `env_vars={...}` as "invalid type:
    map, expected a sequence", and the token is in the descriptor file, so no
    `env_vars` is needed at all). No eval, no manual TOML escaping; token not in argv.
    **The command must NOT be the bare literal `afm`** — in native mode afm is often
    invoked by an absolute path and is not guaranteed to be on `PATH` as `afm`, so the
    executor exports the non-secret `AFM_BROWSER_LAUNCHER=<LauncherPath>` (the same
    resolved abs path the claude branch writes as `command`) and the adapter reads it.

- [ ] **Step 1: Write the failing policy tests — opt-out, fallback, verify, support**

```go
// pkg/orchestrator/runner_factory_test.go
func TestBrowserForStage_Policy(t *testing.T) {
	o := newOrchWithBrowser(t, &browser.Descriptor{Endpoint: "127.0.0.1:1", Epoch: "ep1"})
	if o.browserForStage(normalClaudeStage("build"), "claude") == nil {
		t.Fatal("normal supported stage must get the capability")
	}
	if o.browserForStage(stageWithBrowserFalse("build"), "claude") != nil {
		t.Fatal("browser:false stage must get nil")
	}
	if o.browserForStage(normalClaudeStage("build"), "deepseek-openai") != nil {
		t.Fatal("unsupported client (no MCP tool-loop) must get nil")
	}
}

func TestRunnerFor_InteractiveAndFallbackGetBrowser(t *testing.T) {
	o := newOrchWithBrowser(t, &browser.Descriptor{Endpoint: "127.0.0.1:1", Epoch: "ep1"})
	if !execConfigHasBrowser(o.runnerFor(interactiveClaudeStage("build"), "implementation")) {
		t.Fatal("interactive author runner should receive afm_chrome")
	}
	if !execConfigHasBrowser(o.runnerForFallback(interactiveClaudeStage("build"), "implementation")) {
		t.Fatal("interactive fallback runner must also receive afm_chrome")
	}
}

func TestRunnerForVerify_NoBrowser(t *testing.T) {
	o := newOrchWithBrowser(t, &browser.Descriptor{Endpoint: "127.0.0.1:1", Epoch: "ep1"})
	if execConfigHasBrowser(o.runnerForVerify(stageWithVerify("build"), "codex-as-claude")) {
		t.Fatal("verify runner must never receive the browser capability")
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./pkg/orchestrator/ -run 'TestBrowserForStage|TestRunnerFor' -v`
Expected: FAIL — `Options.BrowserDescriptor` / `browserForStage` / `Config.BrowserMCP` undefined.

- [ ] **Step 3: Implement the policy helper + wiring**

Add `Options.BrowserDescriptor` and `Config.BrowserMCP`. Implement `browserForStage`
(above) and call it in `runnerFor` (non-interactive and interactive branches) and
`runnerForFallback`; `runnerForVerify` and the detached memory/JSON-fix runners never
call it. This is the only place MCP eligibility is decided (consolidated policy).

- [ ] **Step 4: Run to verify pass**

Run: `go test ./pkg/orchestrator/ -run 'TestBrowserForStage|TestRunnerFor' -v`
Expected: PASS.

- [ ] **Step 5: Write executor arg + central env-strip tests, implement**

The strip is **default-deny for every child**, not gated on `VerifyMode` — JSON-fix,
memory and `RunJSONQuery` are not `VerifyMode` yet must also be clean.

```go
// pkg/executor/executor_test.go
func TestExecutor_ClaudeMCPConfigAppended(t *testing.T) {
	args := buildArgsForTest(Config{Command: "claude", BrowserMCP: &BrowserMCP{ServerName: "afm_chrome"}})
	if !hasFlagWithValue(args, "--mcp-config") {
		t.Fatal("claude must get --mcp-config for afm_chrome")
	}
}
func TestExecutor_AllChildEnvsStripBrowserTransport(t *testing.T) {
	ambient := []string{"AFM_BROWSER_TRANSPORT_TOKEN=x", "AFM_BROWSER_ENDPOINT=y",
		"AFM_BROWSER_EPOCH=ep1", "PATH=/bin"}
	for _, cfg := range []Config{
		{VerifyMode: true},               // AI verify
		{},                               // JSON-fix / memory / plain author with no BrowserMCP
	} {
		if envContainsPrefix(filterEnvForTest(cfg, ambient), "AFM_BROWSER_") {
			t.Fatalf("every child env must be stripped of AFM_BROWSER_* (cfg=%+v)", cfg)
		}
	}
}
func TestExecutor_AuthorRunnerReAddsNonSecretOnly(t *testing.T) {
	ambient := []string{"AFM_BROWSER_TRANSPORT_TOKEN=secret", "PATH=/bin"}
	env := filterEnvForTest(Config{BrowserMCP: &BrowserMCP{AppURL: "http://127.0.0.1:54321", AppPort: 18080, LauncherPath: "/usr/local/bin/afm"}}, ambient)
	if envContainsPrefix(env, "AFM_BROWSER_TRANSPORT_TOKEN") {
		t.Fatal("token must never be in the agent process env")
	}
	if !envHas(env, "AFM_BROWSER_APP_URL=http://127.0.0.1:54321") || !envHas(env, "AFM_BROWSER_APP_PORT=18080") {
		t.Fatal("author runner must re-add the non-secret app URL/port")
	}
	if !envHasPrefix(env, "AFM_BROWSER_DESCRIPTOR=") {
		t.Fatal("author runner must re-add the non-secret descriptor-file path (codex adapter reads it)")
	}
	if !envHas(env, "AFM_BROWSER_LAUNCHER=/usr/local/bin/afm") {
		t.Fatal("author runner must re-add the non-secret resolved afm launcher path (codex MCP command)")
	}
}
func TestRunJSONQuery_StripsBrowserTransport(t *testing.T) {
	env := jsonQueryEnvForTest([]string{"AFM_BROWSER_TRANSPORT_TOKEN=x", "PATH=/bin"})
	if envContainsPrefix(env, "AFM_BROWSER_") {
		t.Fatal("RunJSONQuery (separate env path) must also strip browser transport")
	}
}
```

Implement one `stripBrowserEnv(env []string) []string` used by **both** the main env
filter (`:850`+) and `RunJSONQuery`'s separate env path (`:767`+). After stripping,
when `cfg.BrowserMCP != nil`, re-add only the non-secret `AFM_BROWSER_APP_URL`,
`AFM_BROWSER_APP_PORT`, `AFM_BROWSER_DESCRIPTOR=<descriptorPath>`, and
`AFM_BROWSER_LAUNCHER=<cfg.BrowserMCP.LauncherPath>` (the resolved abs afm path the
codex adapter uses as the MCP `command`; non-secret). In the arg
builder (`:819`), when `cfg.BrowserMCP != nil`: (1) write the `0600` descriptor file
`{wsEndpoint, token, appURL, roots}`; (2) **for a claude-family command only**, write
the `0600` `.mcp.json` whose `afm_chrome` entry is `command=LauncherPath`,
`args=["browser","mcp","--descriptor","<descriptorPath>"]`, and append `--mcp-config
<.mcp.json>` (the codex adapter instead reads `AFM_BROWSER_DESCRIPTOR` from env and
builds its own `-c` overrides in Step 7). Both files use the existing `WrapperDir`/temp
discipline and are removed when the invocation ends. The token lives only in the
descriptor file (0600) — never in the agent env, the `.mcp.json`, or argv.

- [ ] **Step 6: Write the descriptor-file transport test (token/roots) + implement**

```go
func TestExecutor_DescriptorFileCarriesTokenAndRoots_NotArgvNotMCPEnv(t *testing.T) {
	descPath, mcpJSON, args := injectBrowserForTest(Config{Command: "claude",
		BrowserMCP: &BrowserMCP{LauncherPath: "/usr/local/bin/afm", Token: "tok-SECRET",
			WSEndpoint: "ws://host.docker.internal:1/session/ep1/build",
			Roots: []string{"/proj", "/afm/.afm/runs/r/build"}}})
	for _, a := range args {
		if strings.Contains(a, "tok-SECRET") { t.Fatalf("token must not be in argv: %q", a) }
	}
	if strings.Contains(readFile(t, mcpJSON), "tok-SECRET") {
		t.Fatal("token must not be in the .mcp.json")
	}
	if fileMode(t, descPath) != 0o600 { t.Fatal("descriptor file must be 0600") }
	var d struct{ Token string `json:"token"`; Roots []string `json:"roots"` }
	json.Unmarshal([]byte(readFile(t, descPath)), &d)
	if d.Token != "tok-SECRET" || len(d.Roots) != 2 {
		t.Fatalf("descriptor file must carry token + roots, got %+v", d)
	}
	// the .mcp.json references the descriptor path as a plain arg
	if !strings.Contains(readFile(t, mcpJSON), "--descriptor") {
		t.Fatal(".mcp.json server args must pass --descriptor <path>")
	}
}
```

Implement the descriptor-file write + `.mcp.json` generation as specified in Step 5.

- [ ] **Step 7: Codex adapter — sequence args + action translation + verify/shell/hook isolation**

In `scripts/codex-as-claude.sh`: if `AFM_BROWSER_DESCRIPTOR` (the per-invocation
descriptor path, set by the executor for codex author runners) is present and this is
NOT a verify pass (`CODEX_VERIFY` unset), append
`-c mcp_servers.afm_chrome.command="$AFM_BROWSER_LAUNCHER"` (the resolved absolute afm
path the executor exports — **never the bare literal `afm`**, which would fail in
native mode when afm is not on `PATH` under that name; if `AFM_BROWSER_LAUNCHER` is
unset, skip the override rather than guess) and
`-c mcp_servers.afm_chrome.args=["browser","mcp","--descriptor","$AFM_BROWSER_DESCRIPTOR"]`
(a **sequence**; NO `env_vars` map — pinned codex 0.155.1 rejects a map). The token is in
the descriptor file, not in argv/env. Extend the JSON translator (`XLATE`) so a
`mcp_tool_call` event is emitted as a visible `Bash`/tool `agent_action` (today it
surfaces mostly `command_execution`). Confirm the shell-verify env path
(`sanitizedShellVerifyEnv()` / `IsCrossAgentTransportSecret`, `pkg/orchestrator/verify.go`)
also drops `AFM_BROWSER_*`. Add a shell test asserting the overrides are present for a
normal run (with the `command` equal to the exported launcher path, exercised with a
`PATH` that contains **no** `afm` binary so a bare-`afm` command would fail) and absent
when `CODEX_VERIFY=1`.

- [ ] **Step 8: Close the lifecycle-hook leak — extend `stripTransportVars` + test**

`pkg/lifecyclehooks/runner.go`'s `stripTransportVars` currently drops only
`AFM_HOOK_SECRET_*` / `AFM_SECRET_* `/ `AFM_SYSPROMPT_*`, so an `inherit_env: true` hook
inheriting the afm process env would receive `AFM_BROWSER_TRANSPORT_TOKEN`. Add
`AFM_BROWSER_` to that prefix list.

```go
// pkg/lifecyclehooks/runner_test.go
func TestBuildEnv_InheritEnv_StripsBrowserTransport(t *testing.T) {
	t.Setenv("AFM_BROWSER_TRANSPORT_TOKEN", "tok")
	t.Setenv("AFM_BROWSER_ENDPOINT", "host.docker.internal:1")
	env := buildEnv(Hook{InheritEnv: true}, nil)
	for _, kv := range env {
		if strings.HasPrefix(kv, "AFM_BROWSER_") {
			t.Fatalf("inherit_env hook must not receive %q", kv)
		}
	}
}
```

- [ ] **Step 9: Run all affected packages**

Run: `go test ./pkg/executor/ ./pkg/orchestrator/ ./pkg/lifecyclehooks/ && bash scripts/test-codex-adapter.sh`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/runner_factory.go pkg/orchestrator/runner_factory_test.go pkg/executor/executor.go pkg/executor/executor_test.go scripts/codex-as-claude.sh pkg/lifecyclehooks/runner.go pkg/lifecyclehooks/runner_test.go
git commit -m "browser: инъекция afm_chrome (descriptor-файл), codex sequence-args, strip AFM_BROWSER_* везде"
```

---

## Task 6: Built-in live-run skill & runtime browser prompt (variant B)

**Files:**
- Create: `assets/claude/skills/live-run/SKILL.md`
- Modify: `pkg/prompts/builder.go` (+ existing install-skills asset test if present)
- Test: `pkg/prompts/builder_test.go`, asset-presence test

**Scope note (variant B):** this task ships the capability — a built-in skill plus a
minimal runtime prompt so any normal supported agent knows the managed tools exist.
It does **not** add a `live-run` stage or edit `dev-pipeline.yaml`; the user wires
the stage manually (the spec's example stanza is reproduced in `dev-spec.md` §6 for
reference).

**Interfaces:**
- Produces: a `prompts.Inputs` field `BrowserRuntime *BrowserRuntime{AppURL, AppPort,
  ServerName}` → when non-nil, `Build` appends a short `<browser_tools>` block
  explaining: use MCP `afm_chrome`; run the test server on `0.0.0.0:$AFM_BROWSER_APP_PORT`
  inside the container (or the host port natively); open `$AFM_BROWSER_APP_URL`
  (never container `localhost:app_port` nor `host.docker.internal`); save
  screenshots/console/network to `$AFM_STAGE_DIR/artifacts`. The block is emitted even
  under `--bare`.

- [ ] **Step 1: Write the failing prompt test**

```go
// pkg/prompts/builder_test.go
func TestBuild_BrowserRuntimeBlockPresent(t *testing.T) {
	out := Build(Inputs{BrowserRuntime: &BrowserRuntime{AppURL: "http://127.0.0.1:55115", ServerName: "afm_chrome"}})
	if !strings.Contains(out, "afm_chrome") || !strings.Contains(out, "http://127.0.0.1:55115") {
		t.Fatal("runtime prompt must name the MCP server and app URL")
	}
}
func TestBuild_NoBrowserBlockWhenNil(t *testing.T) {
	if strings.Contains(Build(Inputs{}), "afm_chrome") {
		t.Fatal("no browser block when capability off")
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./pkg/prompts/ -run TestBuild_Browser -v`
Expected: FAIL.

- [ ] **Step 3: Implement the prompt block + the skill file**

Add `BrowserRuntime` to `Inputs` and the conditional block in `Build`. Write
`assets/claude/skills/live-run/SKILL.md`: build the app from current sources (for afm
itself `make build`, which rebuilds the frontend before the Go embed — the pinned
image binary does not reflect new edits), wait for health, run the user scenario from
`dev-plan.md`, capture console/network + a screenshot to `$AFM_STAGE_DIR/artifacts`,
and treat an unavailable browser or a failed scenario as a failed check (not success).
Wire the runtime input where normal runners build prompts, gated by the **same**
`browserForStage` policy as the MCP injection (Task 5) — so a `browser: false` stage,
an unsupported client, or capability-off produces a nil `BrowserRuntime` and no block.
This keeps the prompt and the actual tool availability in lockstep (no "told about
tools it doesn't have").

- [ ] **Step 4: Run to verify pass**

Run: `go test ./pkg/prompts/ -v`
Expected: PASS.

- [ ] **Step 5: Assert the skill ships in the asset bundle**

Add/extend a test that `assets/claude/skills/live-run/SKILL.md` is present in the
embedded/installed skill set, so the capability does not depend on the user's personal
`~/.claude/skills/live-run`.

Run: `go test ./... -run 'Skill|Asset' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add assets/claude/skills/live-run/SKILL.md pkg/prompts/
git commit -m "browser: встроенный live-run skill + runtime-prompt о managed-браузере"
```

---

## Task 7: Integration, E2E & docs

**Files:**
- Create: `pkg/browser/smoke_mac_test.go` (opt-in, build-tagged `//go:build browser_smoke`)
- Modify: `docs/docker.md`, `docs/config-reference.md`, `README.md`, `CHANGELOG.md`
- Test: full `go test ./...`, `make lint-ci`, `make schema-check`

**Interfaces:** none new — this task verifies the assembled capability.

- [ ] **Step 1: Write the deterministic MCP-client E2E (no LLM)**

Mirror the spec experiment: a deterministic MCP client (not an LLM) drives
`initialize` → `tools/list` (no Chrome) → first `new_page` (lazy launch) →
`take_snapshot`/`fill`/`click`/`wait_for` → `evaluate_script` → `list_console_messages`
→ `list_network_requests` → `take_screenshot`, against a tiny local app. Assert:
unauthorized call never launches Chrome; the app opens via the issued app URL; two
parallel stages get separate sessions/profiles; a reconnect to the same stage id
(retry/resume) reuses its session; a `browser: false` stage and a verify runner get no
capability and no `AFM_BROWSER_*` env; cleanup leaves no owned process/profile.
Include a run where **afm-root ≠ `flow.root_dir`** and assert the MCP can
`take_screenshot` into `$AFM_STAGE_DIR/artifacts` (anchored to afm-root) AND read a
file under the separate `root_dir` — i.e. the injected filesystem roots cover both.
Run it for **both** modes (Docker on Docker Desktop; native on the host). Gate the
real-Chrome part behind `//go:build browser_smoke` so default `go test ./...` and CI
stay headless.

- [ ] **Step 2: Run the opt-in smoke on a Mac**

Run: `go test -tags browser_smoke ./pkg/browser/ -run Smoke -v`
Expected: PASS (manual, on a developer Mac with Chrome).

- [ ] **Step 3: One small paid LLM confirmation**

A single small real `claude`/codex invocation that proves the agent discovers and
calls `afm_chrome` (not the whole scenario — the deterministic client covers depth).
Record it in the live-run notes; do not add it to automated CI.

- [ ] **Step 4: Docs + CHANGELOG**

Update `docs/docker.md` (managed browser, app port/URL, loopback publish),
`docs/config-reference.md` (the `browser:` block + stage switch), `README.md`
(feature line), and remove any stale "нельзя прогнать из контейнера" claim. Add a
`CHANGELOG.md` dated section (English) under `### Improvement:`. Document: supported
hosts, native Node/MCP prerequisite, app lifetime ends with the run, and the honest
SIGKILL cleanup limitation.

- [ ] **Step 5: Full gate**

Run: `go test ./... && make lint-ci && make schema-check`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/browser/smoke_mac_test.go docs/ README.md CHANGELOG.md
git commit -m "browser: E2E deterministic MCP client, smoke-тег и документация"
```

---

## Self-Review

**Spec coverage:** §"Конфигурация" → Task 1; §"Native Go helper и CDP pipe" → Task 2;
§"Stdio launcher и упаковка MCP" → Task 3; §"Runtime и ownership" + §"Доступ Chrome к
приложению" → Task 4; §"MCP у разных агентов" (Claude/Codex/verify) → Task 5;
§"Встроенный live-run" (skill + runtime prompt; stage wiring excluded per variant B)
→ Task 6; §"Проверка интеграции и документация" → Task 7. Native host mode (added
after the spec) is folded into Tasks 1/2/3/4 as the loopback-endpoint / direct-app-URL
/ host-resolved-MCP variant of the same primitive.

Cross-cutting requirements each have an owning task now: per-stage session identity
(deterministic path) — Task 2 + Task 4 + Task 5; host-generated epoch (no run id at
reexec) — Task 4; post-start app URL impossibility (host-chosen fixed port) — Task 4;
Codex transport via `0600` descriptor file + sequence `args` (no `env_vars` map, no
token in argv/env) — Task 5; centralized default-deny browser-env isolation across
every child incl. JSON-fix/memory/RunJSONQuery/shell-verify AND lifecycle-hook
`inherit_env` (`stripTransportVars` extended) — Task 5; filesystem roots computed from
effective `RootDir`+stage dir and threaded via the descriptor file to `afm browser
mcp`, with an afm-root ≠ `root_dir` E2E — Task 5 + Task 7; stage opt-out + interactive
fallback via one policy helper — Task 5 + Task 6; no-install native MCP + enable-state
nil-safety — Tasks 1/3; stale-profile sweep — Task 2; CDP resource bounds — Task 2;
host↔image transport-protocol compatibility probe (dev-spec.md:361-362: clear error /
auto-degrade on an incompatible pinned image, never a hang) — Task 3 (`protocol-version`
subcommand + const) + Task 4 (`probeImageBrowserProtocol`); port-collision gated on the
manager actually starting (`EnableOff`/auto-degraded never fail) — Task 1 preflight.

**Type consistency:** `browser.Manager`, `browser.Descriptor` (`.StageEndpoint`),
`executor.BrowserMCP`, `Options.BrowserDescriptor`, `Orchestrator.browserForStage`,
`prompts.BrowserRuntime`, `BrowserConfig`, `BrowserConfig.EffectiveEnable`/`EnableState`,
`BrowserMode{Docker,Native}`, `browser.TransportProtocolVersion`,
`probeImageBrowserProtocol` are used with the same names across tasks.

**Review Focus:** the ten listed inputs each have an owning test (Task 2 ×5, Task 1
×2, Task 5 ×3).

## Open items for the executor (not blockers)

- `chrome-devtools-mcp`'s `--config` key for restricting filesystem access is
  `filesystemRoot`; confirm whether it accepts multiple roots or a single path during
  Task 3. If single-only, pass the lowest common ancestor of the computed roots (the
  roots list is still the source of truth in the descriptor file).
- The exact claude MCP config file shape (`.mcp.json` server entry with `command`/`args`)
  should be confirmed against the pinned Claude Code (2.1.212) `--mcp-config` loader
  during Task 5 (the stdio health check was confirmed in the experiment). The codex
  `-c mcp_servers.afm_chrome.command`/`.args` sequence form is confirmed (0.155.1
  rejects the `env_vars` map), so the token-in-descriptor-file transport is the agreed
  design, not an open question.
