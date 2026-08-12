# Multimodal `[Screenshot: <path>]` support in the OpenAI-compatible autoShim scripts

## Context

`docs/superpowers/specs/2026-08-11-clipboard-image-paste-design.md` added the
ability to paste a clipboard screenshot into any agent-facing textarea in the
dashboard. That feature uploads the image to
`<runDir>/<stageID>/attachments/paste-<id>.<ext>` and splices a plain-text
`[Screenshot: <path>]` reference into whatever textarea the user pasted into
— it never touches afm's Go code beyond persisting the file. For real
`claude` (and, by extension, `codex`, which has its own native tool-use
loop), that's sufficient: once the marker text is in front of the agent, its
own built-in `Read` tool understands it's an image file and embeds it
multimodally into its own request to the underlying model — entirely inside
the agent CLI subprocess, invisible to afm.

The two OpenAI-compatible autoShim adapter scripts added in
`docs/superpowers/specs/2026-08-12-openai-agent-tool-loop-design.md`
(`scripts/openai-as-claude.sh`, one-shot text; `scripts/openai-agent-as-claude.sh`,
a real tool loop with a single `bash` tool) have no equivalent. Both treat
the entire prompt as one opaque string when building the `chat/completions`
request body. A `[Screenshot: <path>]` marker reaching either script today is
inert text — if the model is vision-capable, it never actually sees the
picture, wasting a capability it has; if `openai-agent-as-claude.sh`'s own
`bash` tool tries to `cat` the file (e.g. while polling a dialog answer that
happens to reference one), it gets garbled raw image bytes back as "text",
which is actively confusing.

### Verified facts this design relies on

- The `[Screenshot: <path>]` marker is generated exactly once, client-side,
  in `pkg/web/dashboard/src/hooks/use-image-paste/use-image-paste.ts:79`. The
  backend has no awareness of the convention at all.
- Text containing the marker reaches an agent's stdin two structurally
  different ways:
  - **Path A — revise/note.** `Orchestrator.Revise` → `state.SaveFeedback`
    writes the raw feedback text to `<stageDir>/feedback.md`; a fresh prompt
    is built (`prompts.Build`) embedding that file's contents verbatim, and
    piped to a new agent subprocess via `cmd.Stdin`
    (`pkg/executor/executor.go:534-535`). Both adapter scripts read their
    entire prompt from stdin, so this path reaches them directly, once, at
    startup.
  - **Path B — dialog answer.** `POST /dialog/answer` atomically writes
    `<stageDir>/<phase>.<id>.answer.json`; afm's Go code never reads this
    file back into a new prompt. The *already-running* agent process picks
    it up itself, via the polling loop afm's own system prompt tells it to
    run (`pkg/prompts/builder.go:91-96`): `cat` the answer file as one more
    tool call, mid-conversation. For `openai-agent-as-claude.sh` specifically
    (the only one of the two scripts with a `bash` tool / a running loop at
    all), this means a marker can appear inside a **tool result**, not just
    the initial prompt.
- Verified live against IdeaLab's `qwen3-vl-plus` (accessible on the same
  key already configured for `idealab`/`balian`, alongside `qwen-vl-max`):
  the standard OpenAI vision message shape
  (`content: [{type:"text",...},{type:"image_url",image_url:{url:"data:<mime>;base64,<...>"}}]`)
  works correctly, **and** combines correctly with `tools`/`tool_choice` in
  both streaming and non-streaming mode — the streamed `tool_calls` chunks
  use the exact same index-keyed delta shape already handled by
  `openai-agent-as-claude.sh`'s existing SSE-reassembly filter, unchanged.
  (Note: this combination is genuinely provider/model-dependent — the same
  request shape against Balian's accessible vision model, also named
  `qwen-vl-max` but a different backend, accepted the image correctly but
  never actually emitted `tool_calls`, always answering in plain text
  instead, even with `tool_choice: "required"`. This is an operational
  constraint on which model the user points a recipe at, not something this
  design can paper over.)
- The Docker image's `grep` is GNU grep with PCRE support (`-P` works
  out of the box, no extra package), `sed` is GNU `sed` (`-E` for extended
  regex), and `base64 -w 0` (no line wrapping) is available — all verified
  directly inside `akopichin/afm:latest`.
- Both integration points (Path A's initial user message, Path B's
  synthetic follow-up message) and the core extraction/embedding logic were
  hand-built and validated end-to-end against the live IdeaLab API before
  being written into this spec — including finding and fixing a missing
  `jq -n` flag (the same class of bug documented in the tool-loop spec:
  a `jq` call built purely from `--arg`/`--argjson` without `-n` silently
  processes zero stdin documents and produces empty output).

## Goal

`[Screenshot: <path>]` references reach a vision-capable model as an actual
image in both `openai-as-claude.sh` and `openai-agent-as-claude.sh`, via both
delivery paths that already exist in the file-based dialog protocol — with
zero behavior change when no marker is present, and zero new configuration.

## Non-goals

- `cursor-as-claude.sh` / `codex-as-claude.sh` are untouched. `codex` has its
  own native tool-use loop and, plausibly, its own equivalent of claude's
  image-aware `Read` tool — orthogonal to afm, not something this design
  needs to verify or change. `cursor-as-claude.sh` creates a "no-repo" Cloud
  Agent with no filesystem access to the mounted project at all — a
  `[Screenshot: <path>]` reference is physically unreachable for it; solving
  that (inlining image bytes into the Cloud Agent creation request, if even
  possible) is a materially different, larger problem, out of scope here.
- No new `vision:`/capability flag on `AgentRecipe` or `docker.agents.<cmd>`.
  The script always attempts embedding when it finds a marker; whether the
  configured model actually understands the resulting `image_url` block is
  the same kind of user responsibility as picking a model that supports
  `tools` at all today.
- No size limit beyond what the paste endpoint already enforces (10 MiB
  per image, `pkg/server/attachments.go`). Base64 inflates that by ~33% in
  the request body; this design doesn't add a second cap on top.

## Design

### Shared logic, duplicated into both scripts (not a shared library file)

Two bash functions, added near-identically to both `openai-as-claude.sh` and
`openai-agent-as-claude.sh`:

```bash
# extract_image_blocks <text> -> JSON array of image_url content blocks for every
# readable [Screenshot: <path>] reference in text (empty "[]" if none, or if none
# were readable/recognized). Unreadable/unrecognized paths are skipped with a
# stderr warning, not a hard failure.
extract_image_blocks() {
    local text="$1"
    local blocks='[]'
    local path
    while IFS= read -r path; do
        [[ -z "$path" ]] && continue
        if [[ ! -r "$path" ]]; then
            echo "warning: [Screenshot: $path] not readable, skipping" >&2
            continue
        fi
        local mime=""
        case "$path" in
            *.png) mime="image/png" ;;
            *.jpg|*.jpeg) mime="image/jpeg" ;;
            *.webp) mime="image/webp" ;;
            *.gif) mime="image/gif" ;;
            *) echo "warning: [Screenshot: $path] unrecognized image extension, skipping" >&2; continue ;;
        esac
        local b64
        b64=$(base64 -w 0 "$path")
        blocks=$(jq -nc --argjson blocks "$blocks" --arg mime "$mime" --arg b64 "$b64" \
            '$blocks + [{type:"image_url", image_url:{url: ("data:" + $mime + ";base64," + $b64)}}]')
    done < <(printf '%s' "$text" | grep -oP '\[Screenshot: \K[^]]+' || true)
    printf '%s' "$blocks"
}

# build_user_content <text> -> plain JSON string if no image was embedded, else a
# [{type:"text",...}, image_url...] array with the [Screenshot: <path>] marker(s)
# stripped from the text portion.
build_user_content() {
    local text="$1"
    local blocks
    blocks=$(extract_image_blocks "$text")
    if [[ "$blocks" == "[]" ]]; then
        jq -nc --arg t "$text" '$t'
        return
    fi
    local cleaned
    cleaned=$(printf '%s' "$text" | sed -E 's/\[Screenshot: [^]]+\]//g')
    jq -nc --arg t "$cleaned" --argjson imgs "$blocks" '[{type:"text", text:$t}] + $imgs'
}
```

Deliberately duplicated rather than extracted into a third, sourced file:
each of the four existing adapter scripts is already fully self-contained
with no cross-script sourcing, and ~35 lines of shared logic doesn't justify
inventing a new "how do two independently-`COPY`'d Docker scripts share
code" mechanism (an extra file, an extra `COPY` line, a `source` call with
its own path-resolution question) — the simpler option (two copies) is
simpler.

### `openai-as-claude.sh`

The single line that currently builds the request body's `content` field
from the raw `$prompt` string is replaced with
`content=$(build_user_content "$prompt")`, fed into the existing
`jq -nc --argjson content "$content" ...` construction. No other change —
this script never runs a loop or a tool, so Path B doesn't apply to it; the
only place a marker can ever appear is the one prompt it receives once.

### `openai-agent-as-claude.sh`

**Path A** — identical treatment for the seed `user` message: the current
`jq -nc --arg user "$prompt" ...` becomes
`user_content=$(build_user_content "$prompt")` fed via `--argjson`.

**Path B** — after the existing per-tool-call block appends the
`role:"tool"` message (unchanged — the tool message stays the literal,
faithful command output, markers and all; only the *initial user prompt*
gets its marker stripped, on the reasoning that the tool result should
reflect exactly what the command produced), a new step:

```bash
img_blocks=$(extract_image_blocks "$tool_output")
if [[ "$img_blocks" != "[]" ]]; then
    followup_msg=$(jq -nc --argjson imgs "$img_blocks" \
        '{role:"user", content: ([{type:"text", text:"Screenshot referenced in the tool result above:"}] + $imgs)}')
    jq -c --argjson m "$followup_msg" '. + [$m]' "$messages_file" > "${messages_file}.tmp" && mv "${messages_file}.tmp" "$messages_file"
fi
```

A synthetic `user`-role message, not a multimodal `tool`-role message: OpenAI's
protocol doesn't reliably guarantee multimodal content inside a `tool`-role
message across arbitrary compatible providers, but `user`-role multimodal
content is universal — sidesteps the compatibility question entirely rather
than gambling on it. This costs one extra message in history per detected
screenshot, not an extra API turn (the `turn` counter only increments per
`chat/completions` call).

The system prompt gets one added sentence teaching the model the convention,
so it doesn't waste a `cat`/`file` command trying to inspect image bytes as
text itself: *"If a message mentions `[Screenshot: <path>]`, an image of it
is attached directly to that message — you do not need to read the file
yourself."*

### Error handling

- Missing/unreadable file, or an extension outside the four the paste
  endpoint ever produces: skip that one image (stderr warning), fall back to
  the original text unmodified for that reference. A stage shouldn't fail
  outright over one bad screenshot reference when the rest of the prompt is
  still actionable.
- No behavior change whatsoever when no marker is present: `content` stays
  exactly the plain string it is today, verified by the existing "no
  regression" test path.

## Testing

Extends the two existing test files (`pkg/executor/openai_translator_test.go`,
`pkg/executor/openai_agent_translator_test.go`), same pattern already
established there (real script execution against a stateful fake `curl`, a
real temp image file on disk, asserting on the captured request body):

- `openai-as-claude.sh`: a prompt containing `[Screenshot: <path-to-a-real-test-PNG>]`
  → the captured request's `content` field is an array with a `text` block
  (marker stripped) and an `image_url` block whose `data:image/png;base64,...`
  decodes back to the original test file's bytes. A prompt with no marker →
  `content` is unchanged, a plain string (regression check against the
  existing passing test for this script).
- `openai-agent-as-claude.sh`:
  - Initial-prompt marker (Path A): the first captured request's seed `user`
    message is a multimodal array, same shape/assertion as above.
  - Mid-loop marker (Path B): fake curl's first turn returns a `tool_calls`
    delta for `bash("cat <path-to-a-fixture-file-containing-a-[Screenshot:-marker]>")`;
    after that command executes for real, the *second* captured request must
    contain both the literal tool-role message (unmodified fixture content)
    **and** a distinct `role:"user"` message carrying the image block.
  - Missing-file marker: `[Screenshot: /nonexistent/path]` in the initial
    prompt → falls back to plain-string content, no crash, `exit 0` still
    reachable (paired with a stderr assertion if convenient, not required).
  - Regression: the existing `TestOpenAIAgentAsClaude_SingleToolCallThenFinalAnswer`,
    `_MaxTurnsReached`, and `_APIFailureExitsNonZero` tests (no marker
    anywhere in their fixtures) must keep passing unmodified — proof the
    feature is additive, not a behavior change for the common case.
