# Synthetic playground

From the repository root, with AFM 3.1.4+ and Python 3 installed:

```bash
python3 examples/playground/run.py
```

The [walkthrough](../../docs/playground.md) covers the three scenarios, Cost
charts, budget hold and continuation, restart and cleanup. The launcher uses a
separate directory and never runs the demo against this checkout.

Run regression checks with `python3 -m unittest discover -s examples/playground`.

The report PNGs are bundled, so running the demo needs no imaging dependencies.
To regenerate them, install Pillow in a development environment and run:

```bash
python3 examples/playground/render_reports.py --font /path/to/font.ttf
```

With AFM installed, exercise all scenarios against the real runtime (dialog,
budget hold/continue, priced usage and the served image):

```bash
AFM_PLAYGROUND_TEST_BIN=afm python3 -m unittest discover -s examples/playground -v
```
