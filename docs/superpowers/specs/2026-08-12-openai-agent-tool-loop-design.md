# openai-agent: real tool-loop autoShim recipe for OpenAI-compatible function-calling providers

## Context

`docker.agents.<cmd>` autoShim already supports `type: openai` (`scripts/openai-as-claude.sh`):
one chat-completion call, aggregate the streamed text, print one claude-compatible
`assistant` envelope. That's sufficient for planning/review-style stages where afm
just takes the raw text (`RunPlanning` writes it to `plan.md`, review ignores it).

It is **not** sufficient for `agents: [auto]` (autonomous) or `interactive: true`
stages. Those require the agent process to actually act on the filesystem:

- Autonomous stages must write `execution_summary.md` in the stage directory
  themselves (`pkg/orchestrator/stagefiles/completion.go`'s `CheckAutonomousCompletion`
  checks the file exists and is non-empty — purely filesystem-based, not
  event-log-based).
- Interactive stages must write `<phase>.<id>.question.json` into `$AFM_STAGE_DIR`
  and then block until `<phase>.<id>.answer.json` appears
  (`pkg/orchestrator/orchestrator.go`'s `pollQuestions`, filesystem-poll based, not
  event-log-based either).
- Stages with `skills: [...]` expect the agent to look up and follow
  `.claude/skills/<name>/SKILL.md` — a convention built into Claude Code's own
  Skill tool, not into the OpenAI chat-completions protocol.

`openai-as-claude.sh` does none of this — it has no tools, so it can never
produce these side effects. Concretely, this blocks a real project
(`/Users/alexander.kopichin/work/lt-flow/flow.yaml`) whose stages are entirely
`agents: [auto]` / `interactive: true`, from running through the `idealab`
autoShim agent added for it (`type: openai`, IdeaLab's `qwen3-max`, an
Alibaba-internal OpenAI-compatible `/chat/completions` gateway).

### Verified facts this design relies on

- IdeaLab's `qwen3-max` supports OpenAI-style function calling: a request with a
  `tools` array + `tool_choice: "auto"` returns a standard `tool_calls` array
  (verified with a live curl call against
  `https://idealab.alibaba-inc.com/api/openai/v1/chat/completions`).
- Streamed (`stream: true`) tool calls use the standard OpenAI delta shape:
  each chunk's `choices[0].delta.tool_calls[]` entry carries an `index`; `id`
  and `function.name` are set on the first chunk for that index, and
  `function.arguments` arrives as string fragments to be concatenated per
  index (verified with a live curl call, chunks captured and inspected).
- afm's executor (`pkg/executor/executor.go`) resets its 30-minute idle-timeout
  timer on **every line** read from the agent process's stdout, regardless of
  content (`lineReader` callback calls `idleTimer.Reset` unconditionally).
- afm's dashboard action feed recognizes lines shaped like
  `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"..."}}]}}`
  (`pkg/executor/executor.go`'s `contentToAction`, `toolNameBash = "Bash"`,
  `toolInput.Command` mapped from `json:"command"`) — this is the exact shape a
  real Claude `Bash` tool call produces, and also the exact shape our adapter's
  own tool-call JSON naturally becomes once translated.
- `pkg/prompts/builder.go` only ever emits `<skills>name, name</skills>` as a
  hint in the prompt text; it does not embed skill file contents. Claude Code's
  own Skill tool is what resolves that hint into file contents. A bash-only
  loop has no equivalent built in and needs to be told the convention
  explicitly.

## Goal

A new autoShim recipe type, **not IdeaLab-specific**: any OpenAI-compatible
provider with function calling gets a real single-tool ReAct loop instead of
one-shot text. Wired up for IdeaLab as the first (and currently only) user.

## Non-goals

- No new sandboxing beyond the existing Docker container isolation — the
  container already runs `claude`/`codex` with unrestricted tool access, and
  this reuses the same trust model.
- No tool surface beyond one generic `bash` command-execution tool. Read/Write/
  Skill-loading/dialog-polling are all just shell commands run through it —
  deliberately not reimplementing Claude's individual tool types.
- No change to the existing `openai`/`cursor`/`codex` recipe types or their
  scripts.

## Design

### 1. New recipe type: `openai-agent`

`pkg/config/config.go`:
- New constant `RecipeTypeOpenAIAgent = "openai-agent"`.
- `AgentRecipe` gets a new optional field `MaxTurns int `yaml:"max_turns"`` —
  caps tool-call iterations per stage invocation. Zero/unset means "use the
  script's own default" (40).
- `Validate()`: `openai-agent` is added everywhere `openai`/`cursor` are
  currently accepted — model and url required, `auth.to` is any `env:VAR`
  (not restricted to `ClaudeAuthEnvVars`, same reasoning as `openai`/`cursor`:
  it's an external gateway with its own env var convention, not a
  claude-compatible auth channel).

### 2. Wrapper generation

`pkg/docker/wrapper.go`'s `generateWrapper`: new branch parallel to the
existing `RecipeTypeOpenAI` one. Exports `OPENAI_API_KEY` (from the recipe's
transient secret, same pattern as today), `OPENAI_BASE_URL`, `OPENAI_MODEL`,
and `OPENAI_AGENT_MAX_TURNS` (from `AgentRecipe.MaxTurns`, only exported when
non-zero — the script defaults to 40 when the var is absent). Execs
`/usr/local/bin/openai-agent-as-claude "$@"`.

### 3. The adapter script — `scripts/openai-agent-as-claude.sh`

Environment variables (mirrors `openai-as-claude.sh`'s naming):
- `OPENAI_API_KEY` (required)
- `OPENAI_BASE_URL` (default `https://api.openai.com/v1`)
- `OPENAI_MODEL` (default `gpt-4o`)
- `OPENAI_AGENT_MAX_TURNS` (default `40`)

Behavior:
1. Read the prompt from stdin (ignore all claude CLI flags), same convention
   as the other adapters.
2. Build the initial message history: one `system` message with generic,
   provider-agnostic instructions —
   > "You have exactly one tool, `bash`, which runs a shell command in the
   > current working directory and returns its combined stdout+stderr and
   > exit code. Use it to read and write files, run scripts, and wait for
   > external input (a blocking command is fine — run it, it will return
   > when ready). If the task mentions a skill by name, first read its
   > instructions with `bash` (e.g. `cat .claude/skills/<name>/SKILL.md` or
   > `~/.claude/skills/<name>/SKILL.md`) before proceeding. When the task is
   > fully complete, respond with your final answer as plain text and do not
   > call any tool."

   and one `user` message containing the piped-in prompt as-is.
3. Tool schema: one function, `bash(command: string)`.
4. Loop, bounded by `OPENAI_AGENT_MAX_TURNS`:
   1. `curl` the chat-completions endpoint with `stream: true`,
      `tool_choice: "auto"`, the tool schema, and the full message history.
   2. Reassemble the SSE stream with `jq` into one object:
      `content` (concatenation of all `delta.content` fragments),
      `tool_calls` (all `delta.tool_calls[]` entries across chunks, grouped by
      `.index`; per group, first non-empty `id`/`function.name` wins,
      `function.arguments` fragments are concatenated in order), and
      `finish_reason` (last non-empty value seen).
   3. If `tool_calls` is non-empty: for each call (in index order) —
      - Immediately print
        `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"<command>"}}]}}`
        to stdout. This is a real Claude-shaped tool-use envelope (drives the
        dashboard action feed) and, incidentally, is what keeps the
        idle-timeout timer alive between turns.
      - Run `bash -c "<command>"`, capturing combined stdout+stderr and the
        exit code. Truncate captured output to ~15000 characters (append a
        `[...truncated]` marker) to keep the growing history bounded.
      - Append the assistant's message (as returned, including its
        `tool_calls` array) and one `role: "tool"` message per call
        (`tool_call_id` + the captured, possibly-truncated output) to the
        history.
      - Continue the loop (next iteration re-sends the full history).
   4. Else: `content` is the final answer. Break out of the loop.
5. If the loop exits because `OPENAI_AGENT_MAX_TURNS` was reached without a
   final answer: treat the last-seen `content` (likely empty) plus an
   appended `\n[openai-agent: max turns reached, stopping]` as the final text.
   This is **not** treated as a script failure — afm's existing incomplete-work
   handling (missing/empty `execution_summary.md` triggers the standard
   autonomous-stage retry path) already covers a stage that didn't finish in
   time. No new error path needed.
6. Network/API failure (non-2xx response, curl error) on any turn: print a
   diagnostic to stderr and exit 1 — afm marks the stage failed, same as
   `codex-as-claude.sh`'s behavior on a codex failure. (Unlike
   `openai-as-claude.sh`, which swallows curl failures into an empty
   success — that's fine for a single best-effort text completion, but wrong
   here: silently "succeeding" with an empty tool-loop would let afm believe
   an autonomous stage's absence of `execution_summary.md` is a normal
   in-progress state rather than a hard failure worth surfacing immediately.)
7. On a normal finish: print the final
   `{"type":"assistant","message":{"content":[{"type":"text","text":"<content>"}]}}`
   envelope, then `{"type":"result","subtype":"success"}`.

### 4. Docker image

`Dockerfile.runtime`: add
`COPY scripts/openai-agent-as-claude.sh /usr/local/bin/openai-agent-as-claude`
+ `chmod +x`, in the same place as the other three adapter `COPY` blocks.

### 5. Config change (not part of this repo)

`~/.afm/config.yaml`'s `docker.agents.idealab.type`: `openai` → `openai-agent`.

### 6. `lt-flow/flow.yaml` (not part of this repo)

Add `command: idealab` to the five stages that invoke an agent: `get-uri`,
`logs`, `categorize`, `add-auth-rule`, `fill-rule-params`. The four `script:`-only
stages (`prepare-rule`, `add-mandatory-rules`, `strip-mixer-prefix`,
`load-collector-data`) are untouched — `command:` doesn't apply to them.

### 7. Docs

`CLAUDE.md`: new `#### Тип `openai-agent`: полноценный tool-loop` subsection
next to the existing `openai`/`cursor`/`codex` ones, covering: the single-`bash`-tool
design, the skill-loading convention, `max_turns`, and the pre-existing
(not new) constraint that a stage silent on stdout for the whole
`idle_timeout` (e.g. an interactive stage's blocking poll, if the human takes
longer than that to answer) still gets killed — same constraint the
file-based dialog protocol already documents for real `claude`.

## Testing

Follows the existing precedent in `pkg/executor/openai_translator_test.go`:
real script execution with a fake `curl` on `PATH`, no network. New file
`pkg/executor/openai_agent_translator_test.go`, same package/pattern, extended
because our fake `curl` must behave differently across successive
invocations within one script run (multi-turn loop):

- A stateful fake `curl` (a small shell script using a counter file under
  `t.TempDir()` to track invocation count) returns turn-specific canned SSE
  bodies: turn 1 → a `tool_calls` chunk sequence (in the real multi-chunk,
  index-keyed shape captured from IdeaLab) for `bash("echo hello-from-tool")`;
  turn 2 → a final-answer chunk sequence with no tool calls.
- Assertions: the script actually executes the real shell command (its output
  is observable — either by asserting the second `curl` invocation's request
  body, captured to a file by the fake `curl`, contains the executed
  command's stdout, or by having the fake tool command write a marker file
  and asserting it exists); a `tool_use`/`Bash` envelope is printed for the
  tool call and parses via `contentToAction`; the final `assistant` text
  envelope and `result` success line are both present and well-formed.
- A second test: fake `curl` always returns another `tool_calls` chunk
  (never finishes); with `OPENAI_AGENT_MAX_TURNS` set low (e.g. 2) for a fast
  test, assert the script stops after that many turns, still exits 0, and
  the final text contains the max-turns note.
- A third test: fake `curl` exits non-zero on the first call; assert the
  script exits non-zero too (unlike `openai-as-claude.sh`'s swallow-on-failure
  behavior).
- `pkg/config/config_test.go`: `Validate()` cases for `openai-agent` (missing
  model/url rejected, valid recipe accepted, `max_turns` accepted as a plain
  int with no extra constraint).
- `pkg/docker/wrapper_test.go`: wrapper generation for a `RecipeTypeOpenAIAgent`
  recipe exports the right env vars and execs the right binary; `max_turns`
  omitted vs. present.

End-to-end (manual, after the unit-test-driven implementation is green):
rebuild the Docker image, run `lt-flow` for real with `command: idealab`,
answer the `get-uri` interactive question through the dialog API, and confirm
an autonomous stage (`logs`, which uses the `lt-logs` skill) actually executes
`bash` tool calls and produces `execution_summary.md` plus the expected
`./result/*` files.
