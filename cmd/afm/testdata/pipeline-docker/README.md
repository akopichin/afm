# pipeline-docker fixtures

Fake agent clients for pipeline tests and live runs that need no accounts.
Put them on `PATH` under the names the recipe expects (`claude`, `codex`) or
point a wrapper at them with `CODEX_BIN`.

- `fake-claude` reads the prompt, writes `$AFM_STAGE_DIR/execution_summary.md`
  and prints stream-json (assistant text, then a `success` result).
- `fake-codex` answers `exec --help` and, for `exec --json`, prints the JSONL
  that `codex-as-claude` translates. Run it through the wrapper, e.g.
  `CODEX_BIN=./fake-codex codex-as-claude -p "hi"`.

| Variable | Effect |
|---|---|
| `FAKE_AGENT_MARKER_FILE` | Appends `<client> stage=<AFM_STAGE_NAME>` per invocation, showing which client ran. |
| `FAKE_AGENT_RELEASE_FILE` | The agent waits until this file exists before finishing. |
| `FAKE_AGENT_RELEASE_TIMEOUT` | Seconds to wait for the release file (default 60). |
| `AFM_STAGE_DIR`, `AFM_STAGE_NAME` | Set by afm; the summary goes to the stage dir. |
