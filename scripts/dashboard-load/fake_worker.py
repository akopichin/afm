#!/usr/bin/env python3
"""Fake no-LLM helper for the dashboard load fixture.

Stays "working" until gate worker-done (or stop) appears, then returns an advisory result.
"""

import json
import os
import sys
import time

GATES_DIR = os.environ.get("LOAD_GATES_DIR", "gates")
POLL_SECONDS = 0.5


def gate_exists(name):
    return os.path.exists(os.path.join(GATES_DIR, name))


def emit(line):
    print(json.dumps(line), flush=True)


def main():
    sys.stdin.read()  # the helper prompt; its text is not used
    while not (gate_exists("worker-done") or gate_exists("stop")):
        time.sleep(POLL_SECONDS)
    result = {"schema_version": 1, "kind": "result", "summary": "Fixture helper finished."}
    emit({"type": "assistant", "message": {"content": [{"type": "text", "text": json.dumps(result)}]}})
    emit({"type": "result", "subtype": "success"})


if __name__ == "__main__":
    main()
