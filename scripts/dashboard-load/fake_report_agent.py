#!/usr/bin/env python3
"""Fake no-LLM agent that prints short prelude lines and finishes, so its verify step runs.

The prelude keeps the verify row far from the top of the feed, so scrolling can unmount it.
"""

import json
import os

STAGE_DIR = os.environ.get("AFM_STAGE_DIR", ".")
PRELUDE_LINES = 150


def emit_text(text):
    line = {"type": "assistant", "message": {"content": [{"type": "text", "text": text}]}}
    print(json.dumps(line), flush=True)


def main():
    with open(os.path.join(STAGE_DIR, "execution_summary.md"), "w", encoding="utf-8") as f:
        f.write("# Report fixture\n\nNothing to do; verify produces the large report.\n")
    for k in range(1, PRELUDE_LINES + 1):
        emit_text(f"report prelude line {k}")
    emit_text("Done; verify runs next.")


if __name__ == "__main__":
    main()
