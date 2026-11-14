# core-mode testdata

Flows and fake agents (no LLM) for the dashboard/core integration tests.

- `flows/*.yaml` use placeholders the test replaces with absolute paths before writing `flow.yaml`:
  `__AGENT__` (`agents/agent.sh`), `__ASK_AGENT__` (`agents/ask_agent.sh`),
  `__AUTO_AGENT__` (`agents/auto_agent.sh`), `__BOARD_AGENT__` (`agents/board_agent.sh`), `__BUDGET_AGENT__` (`agents/budget_agent.sh`), `__PLAN__` (`plans/plan.md`).
- The agents read `AFM_STAGE_DIR`; they call the `afm` binary named by `AFM_IT_BIN` (default `afm`).
- `AFM_IT_RUNS_LOG` gets one `<stage>:<phase>` line per invocation; `AFM_IT_RELEASE_DIR` holds
  implementation until `<dir>/<stage>` exists.
- `budget_agent.sh` reports scripted token usage: invocation N takes the Nth number of `AFM_IT_BUDGET_TOKENS`
  (the last repeats) and completes the stage on invocation `AFM_IT_BUDGET_DONE_AT`.
