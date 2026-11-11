#!/usr/bin/env python3
"""Fake no-LLM verifier: passes with non-blocking findings, so the report is at least 100 KB."""

import json
import sys

FINDINGS = 120
DETAIL = "Non-blocking observation with padding to make the full verify report large. " * 12


def main():
    sys.stdin.read()  # the verify prompt; its text is not used
    findings = [{"blocking": False, "title": f"Observation {i}", "evidence": f"{DETAIL}(finding {i})"}
                for i in range(1, FINDINGS + 1)]
    result = {"schema_version": 1, "verdict": "pass",
              "summary": "Fixture verifier passes with a large report.", "findings": findings}
    print(json.dumps({"type": "assistant", "message": {"content": [{"type": "text", "text": json.dumps(result)}]}}), flush=True)
    print(json.dumps({"type": "result", "subtype": "success"}), flush=True)


if __name__ == "__main__":
    main()
