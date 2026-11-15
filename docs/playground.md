# Try AFM without model accounts

The playground runs real AFM with small Python programs acting as agents. Their
messages, findings, token counts and prices are **simulated**. Scheduling, dialog,
event persistence, timing, cost accounting and budget controls are real.
This is a guided product demo, not a benchmark or an execution of the production
catalog's workflows. It does not implement changes in your project.

[See the GIF preview](assets/afm-playground.gif) for agent costs, budget controls
and the visual report, then follow the walkthrough below.

## Start

Install [AFM](getting-started.md) 3.1.4+ and Python 3, then get this repository:

```bash
git clone https://github.com/akopichin/afm.git
cd afm
python3 examples/playground/run.py
```

After that setup, the last line is the one-command demo. It creates a fresh
temporary directory, prints its path and opens the dashboard on port 9876.
Use `--port 9877` if that port is occupied. An installed AFM binary is enough;
you do not need to build AFM from source or access its private engine module.

| Scenario | Command | What to explore |
|---|---|---|
| Development | `python3 examples/playground/run.py` | Scope → backend and frontend in parallel → report |
| Review | `python3 examples/playground/run.py --scenario review` | Five parallel perspectives → combined report |
| Refactor | `python3 examples/playground/run.py --scenario refactor` | Two architects → combined report |

The launcher disables Docker, managed browser, collaboration helpers, adding
stages and the dashboard Assistant for this fixture. All stage commands point
to the synthetic agent. Normal global AFM configuration still loads, including
any configured lifecycle hooks; the launcher does not edit it.

## A five-minute tour

1. Open **Scope** and answer its question. The answer appears in the saved scope
   summary. Free text is recorded, but these scripted agents do not interpret it.
2. Watch the worker stages progress together. Switch stages to inspect their
   feeds. Each worker takes about 30 seconds; pause and continue one to try the
   controls.
3. Open **Cost**. The stage table attributes usage to each stage. The demo enables
   dollar estimates with an illustrative rate card for `demo-simulated`:
   $3 input / $15 output / $0.30 cache read / $5 cache write per million tokens.
   Each completed job reports 1,200 input and 240 output tokens ($0.0072).
   These are educational numbers, not model prices or charges.
4. Open **Models & agents → Breakdown → By agent**. Compare each agent's time,
   tokens and estimated cost. Then open **Agent jobs** for individual durations
   and stages. All agents deliberately report the same clearly synthetic model.
5. Open **Charts**, switch between **Estimated cost**, **Agent time** and
   **Tokens**, and filter by stage. Select a bar to see its jobs. In **When agents
   worked**, select a job to find its row; overlapping workers demonstrate
   parallelism. Agent time adds across workers and can exceed elapsed run time.
6. The initial **3k-token run budget** uses **medium** policy. Scope spends
   1,440 tokens; workers may finish, but the final report waits when the limit
   is exceeded. In **Cost → Budgets**, edit the **run** limit to **20k** and choose
   **Save and continue**. Raising only the report's limit cannot lift a run hold.
7. Open the completed report stage: its feed shows a **visual report** with
   parallel workers, illustrative prices and the budget checkpoint. The image is
   also saved as `artifacts/report.png`; `report.md` includes the worker summaries.
   The picture is a bundled illustration, not a rendering of measured job times.
   Keep a dashboard tab connected while exploring the finished run.

Use **Cost → Budgets** to inspect stage limits too. **Save** only stores a change;
**Save and continue** also releases eligible held work. Dollar limits need a
complete explicit rate card for each covered command/model; see
[spending budgets](budgets.md) before experimenting with real agents.

## Stop and resume

Press Ctrl+C. Keep the printed directory, then repeat the command with it:

```bash
python3 examples/playground/run.py --dir /path/printed/by/the/launcher
```

For review/refactor, also repeat the original `--scenario`. Completed stages
remain completed; an interrupted worker replays its scripted steps. The scope
question and answer use AFM's durable dialog. A new command without `--dir`
creates a new run. To clean up, stop AFM and remove only that demo directory.

For slower exploration set `AFM_DEMO_DELAY=10` (seconds per worker step).
For your own task, continue with [pipeline packages](pipelines.md) and the
[afm-pipelines catalog](https://github.com/akopichin/afm-pipelines).
