#!/usr/bin/env python3
"""Deterministic stream-json agent for the AFM playground; never calls a model."""

import json
import os
from pathlib import Path
import subprocess
import shutil
import time


def emit(text):
    print(json.dumps({"type": "assistant", "message": {
        "model": "demo-simulated", "content": [{"type": "text", "text": text}]}}), flush=True)


def ask():
    result = subprocess.run(["afm", "stage", "ask", "--option", "A small feature",
                             "--option", "An edge case"], input=
                            "## Welcome to the playground\n\nThese agents are simulated. "
                            "Choose a scope to start parallel work. You can inspect the feed, "
                            "pause a stage, or stop and resume this run.\n",
                            text=True, capture_output=True, check=True)
    question = json.loads(result.stdout)["id"]
    while True:
        answer = subprocess.run(["afm", "stage", "wait-answer", question, "--timeout", "60"],
                                text=True, capture_output=True)
        if answer.returncode == 0:
            return json.loads(answer.stdout)["answer"]
        if answer.returncode != 75:
            raise RuntimeError(answer.stderr or answer.stdout)


def main():
    stage = Path(os.environ["AFM_STAGE_DIR"])
    role = stage.name
    emit(f"## {role} · simulated agent\n\nRunning a scripted demonstration, without a model.")
    if role == "scope":
        answer = ask()
        summary = f"# Demo scope\n\nSelected scope: {answer}\n\nAll findings and usage are simulated.\n"
    else:
        steps = (["Reading the selected scope", "Inspecting the example inputs",
                  "Working through the assigned task", "Checking an edge case",
                  "Preparing findings and evidence", "Writing the result"] if role != "report" else
                 ["Collecting completed stage summaries", "Checking that every worker returned a result",
                  "Preparing the final report"])
        for number, step in enumerate(steps, 1):
            emit(f"**{role} — {number}/{len(steps)}:** {step} (simulated).")
            time.sleep(float(os.environ.get("AFM_DEMO_DELAY", "5")))
        summary = f"# {role}: simulated result\n\nThe scripted task completed. No project source was changed.\n"
        if role == "report":
            summaries = sorted(stage.parent.glob("*/execution_summary.md"))
            summary += "\n## Worker results\n\n" + "\n".join(
                path.read_text() for path in summaries if path.parent != stage)
            artifacts = stage / "artifacts"
            artifacts.mkdir(exist_ok=True)
            shutil.copyfile("report-preview.png", artifacts / "report.png")
            summary += "\n## Visual report\n\nIllustrative timeline and prices; see Cost for actual recorded job times.\n\n[AFM image: report.png]\n"
            (stage / "report.md").write_text(summary)
    (stage / "execution_summary.md").write_text(summary)
    emit(summary)
    print(json.dumps({"type": "result", "subtype": "success", "model": "demo-simulated",
                      "usage": {"input_tokens": 1200, "output_tokens": 240}}), flush=True)


if __name__ == "__main__":
    main()
