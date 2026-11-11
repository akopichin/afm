#!/usr/bin/env python3
"""Fake no-LLM agent that stays alive until the stop gate, so the load agent has a second mesh recipient."""

import json
import os
import time

GATES_DIR = os.environ.get("LOAD_GATES_DIR", "gates")
STAGE_DIR = os.environ.get("AFM_STAGE_DIR", ".")


def main():
    with open(os.path.join(STAGE_DIR, "execution_summary.md"), "w", encoding="utf-8") as f:
        f.write("# Witness fixture\n\nIdle recipient for conversation threads.\n")
    line = {"type": "assistant", "message": {"content": [{"type": "text", "text": "Witness is waiting for the stop gate."}]}}
    print(json.dumps(line), flush=True)
    while not os.path.exists(os.path.join(GATES_DIR, "stop")):
        time.sleep(0.5)


if __name__ == "__main__":
    main()
