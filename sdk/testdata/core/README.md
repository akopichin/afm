# core integration testdata

Flows and fake agents (no LLM) for `TestIntegration_Core*` in `integration_core_test.go`.

- `flows/*.yaml` hold placeholders the test replaces with absolute paths before writing `flow.yaml`:
  `__AGENT__`, `__AUTO_AGENT__`, `__ASK_AGENT__`, `__BUDGET_AGENT__`, `__GATE__`, `__ROOT_DIR__`.
- The agents call the `afm` binary named by `AFM_IT_BIN`.
- `AFM_IT_RUNS_LOG` gets one `<stage>:<phase>` line per invocation; `AFM_IT_RELEASE_DIR` holds the
  implementation until `<dir>/<stage>` exists; `AFM_IT_RECORD_DIR` receives the agent's cwd and
  `AFM_STAGE_DIR`. `script-gate.yaml` fails until its gate file exists.
- `budget_agent.sh` reports scripted token usage: invocation N takes the Nth number of `AFM_IT_BUDGET_TOKENS`
  (the last repeats) and completes the stage on invocation `AFM_IT_BUDGET_DONE_AT`.
