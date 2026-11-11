#!/usr/bin/env python3
"""Fake no-LLM agent that streams claude stream-json events to load the dashboard feed.

Phases are gated by files in $LOAD_GATES_DIR (default ./gates); see README.md.
"""

import json
import os
import struct
import subprocess
import sys
import time
import zlib

TOOL_SERIES = int(os.environ.get("LOAD_TOOL_SERIES", "2000"))
CONVERSATION = int(os.environ.get("LOAD_CONVERSATION", "300"))
JSON_RESULT = int(os.environ.get("LOAD_JSON_RESULT", "1"))
IMAGE = int(os.environ.get("LOAD_IMAGE", "1"))
PHASE1 = int(os.environ.get("LOAD_PHASE1", "10000"))
PHASE2 = int(os.environ.get("LOAD_PHASE2", "20000"))
BOARD = int(os.environ.get("LOAD_BOARD", "400"))
PHASE3 = int(os.environ.get("LOAD_PHASE3", "1000"))
RATE = float(os.environ.get("LOAD_RATE", "50"))
ANSWER_TIMEOUT = float(os.environ.get("LOAD_ANSWER_TIMEOUT", "1800"))
GATES_DIR = os.environ.get("LOAD_GATES_DIR", "gates")
STAGE_DIR = os.environ.get("AFM_STAGE_DIR", ".")

MARKDOWN_EVERY = 10
BIG_MESSAGE_EVERY = 1500  # markdown events are numbered 1, 11, 21..., so 1501, 3001... are big
POLL_SECONDS = 0.5
IMAGE_NAME = "delayed.png"
IMAGE_DELAY_SECONDS = 1.5
IMAGE_WIDTH, IMAGE_HEIGHT = 1000, 700

counter = 0
worker_spawned = False


class Stop(Exception):
    """Raised when the stop gate appears, wherever the agent is."""


def gate_exists(name):
    return os.path.exists(os.path.join(GATES_DIR, name))


def check_stop():
    if gate_exists("stop"):
        raise Stop()


def sleep_checking_stop(seconds):
    check_stop()
    time.sleep(seconds)


def wait_gate(name):
    print(f"waiting for gate {name}", file=sys.stderr, flush=True)
    while not gate_exists(name):
        spawn_worker_on_gate()
        sleep_checking_stop(POLL_SECONDS)


def emit(content):
    line = {"type": "assistant", "message": {"content": content}}
    print(json.dumps(line), flush=True)


def markdown_text(n, big):
    filler = "Paragraph with **bold**, `inline code` and a [link](https://example.com/load). " * 12
    body = (
        f"load #{n}\n\n"
        "## Section heading\n\n"
        "- first item\n- second item\n- third item\n\n"
        "```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```\n\n"
        f"{filler}\n"
    )
    if big:
        body += filler * 30
    return body


def emit_markdown(n):
    emit([{"type": "text", "text": markdown_text(n, big=n % BIG_MESSAGE_EVERY == 1)}])


def emit_tool(n):
    if n % 2 == 0:
        tool = {"name": "Bash", "input": {"command": f"echo 'load #{n}' && ls -la"}}
    else:
        tool = {"name": "Read", "input": {"file_path": f"/tmp/load/load #{n}.md"}}
    emit([{"type": "tool_use", "id": f"toolu_{n}", **tool}])


def run_phase(count):
    """Streams count numbered events: a markdown message every few events, otherwise tool-call series."""
    global counter
    delay = 1.0 / RATE
    series_left = 0
    for emitted in range(count):
        counter += 1
        if emitted % MARKDOWN_EVERY == 0:
            emit_markdown(counter)
            series_left = 3 + counter % 10
        else:
            emit_tool(counter)
            series_left -= 1
        sleep_checking_stop(delay)


def emit_series_call(k):
    if k % 2 == 0:
        tool = {"name": "Bash", "input": {"command": f"echo 'series #{k}' && ls -la"}}
    else:
        tool = {"name": "Read", "input": {"file_path": f"/tmp/load/series #{k}.md"}}
    emit([{"type": "tool_use", "id": f"toolu_series_{k}", **tool}])


def emit_tool_series():
    """Consecutive tool calls with no text between them: one long collapsed series."""
    delay = 1.0 / RATE
    for k in range(1, TOOL_SERIES + 1):
        emit_series_call(k)
        sleep_checking_stop(delay)


# One mesh thread holds at most 200 messages and a new thread needs a new recipient set, so the
# second half of the conversation goes to user + witness. The ask_human request lives in the first thread.
CONVERSATION_THREAD_SIZE = 150
CONVERSATION_RECIPIENT_SETS = ["user", "user,witness"]


def send_conversation():
    if CONVERSATION > CONVERSATION_THREAD_SIZE * len(CONVERSATION_RECIPIENT_SETS):
        sys.exit(f"LOAD_CONVERSATION above {CONVERSATION_THREAD_SIZE * len(CONVERSATION_RECIPIENT_SETS)} does not fit the mesh thread limit")
    for k in range(1, CONVERSATION + 1):
        check_stop()
        recipients = CONVERSATION_RECIPIENT_SETS[(k - 1) // CONVERSATION_THREAD_SIZE]
        afm_agent("send", "--to", recipients, "--kind", "message", "--body", f"conv #{k}: status note from the agent.")


def emit_json_result():
    """A v1 result envelope as a conversation message: only those bodies offer Raw in the feed."""
    result = {"schema_version": 1, "kind": "result", "verdict": "pass",
              "summary": "Fixture result shown through Raw."}
    afm_agent("send", "--to", CONVERSATION_RECIPIENT_SETS[0], "--kind", "message", "--body", json.dumps(result))


def png_bytes(width, height):
    def chunk(kind, data):
        body = kind + data
        return struct.pack(">I", len(data)) + body + struct.pack(">I", zlib.crc32(body))

    rows = b"".join(
        b"\x00" + b"".join(bytes((x * 255 // width, y * 255 // height, 128)) for x in range(width))
        for y in range(height)
    )  # RGB gradient: filter byte 0 + one row of pixels
    header = struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", header) + chunk(b"IDAT", zlib.compress(rows)) + chunk(b"IEND", b"")


def emit_delayed_image():
    """The marker comes first; the file appears later, so the feed image must retry."""
    png = png_bytes(IMAGE_WIDTH, IMAGE_HEIGHT)
    emit([{"type": "text", "text": f"[AFM image: {IMAGE_NAME}]"}])
    sleep_checking_stop(IMAGE_DELAY_SECONDS)
    artifacts = os.path.join(STAGE_DIR, "artifacts")
    os.makedirs(artifacts, exist_ok=True)
    tmp_path = os.path.join(artifacts, IMAGE_NAME + ".tmp")
    with open(tmp_path, "wb") as f:
        f.write(png)
    os.replace(tmp_path, os.path.join(artifacts, IMAGE_NAME))  # rename: never a half-written PNG


def run_shapes():
    """History shapes in a fixed order; a knob set to 0 turns its shape off."""
    if TOOL_SERIES > 0:
        emit_tool_series()
    if CONVERSATION > 0:
        send_conversation()
    if JSON_RESULT > 0:
        emit_json_result()
    if IMAGE > 0:
        emit_delayed_image()


def spawn_worker_on_gate():
    """Starts the controllable helper once per process when gate worker appears."""
    global worker_spawned
    if worker_spawned or not gate_exists("worker"):
        return
    worker_spawned = True
    try:
        afm_agent("spawn", "--profile", "load-worker", "--title", "Load worker",
                  "--instruction", "Wait for the fixture signal, then answer.")
    except AfmAgentError as err:
        print(f"worker spawn failed: {err}", file=sys.stderr, flush=True)


class AfmAgentError(Exception):
    """An afm agent command exited non-zero."""


def afm_agent(*args):
    result = subprocess.run(["afm", "agent", *args], capture_output=True, text=True)
    if result.returncode != 0:
        raise AfmAgentError(f"afm agent {' '.join(args[:3])} failed: {result.stdout.strip()} {result.stderr.strip()}")
    return result.stdout


def find_message_ids(value):
    """Yields every message id in parsed afm agent JSON, depth first."""
    if isinstance(value, dict):
        for key, item in value.items():
            if key in ("id", "message_id") and isinstance(item, str):
                yield item
            yield from find_message_ids(item)
    elif isinstance(value, list):
        for item in value:
            yield from find_message_ids(item)


def ask_human():
    """Sends a mesh request to the user and returns the id of the message to reply to."""
    out = afm_agent("send", "--to", "user", "--kind", "request",
                    "--body", "Load fixture is ready. Answer to start phase 1.")
    request_id = next(find_message_ids(json.loads(out)), "")
    emit([{"type": "text", "text": f"Asked the human, request {request_id}; waiting for an answer."}])
    deadline = time.time() + ANSWER_TIMEOUT
    while time.time() < deadline and not gate_exists("answered"):
        messages = json.loads(afm_agent("inbox") or "{}")
        ids = list(find_message_ids(messages))
        if ids:
            return ids[0]
        sleep_checking_stop(2)
    return request_id


def publish_board():
    for i in range(1, BOARD + 1):
        check_stop()
        afm_agent("board", "publish", "--key", f"load/{i}", "--status", f"load board item {i}")


def reply_to_answer(answer_id):
    """The reply is rejected when gate answered made the id our own request; that must not end the run."""
    try:
        afm_agent("reply", "--in-reply-to", answer_id, "--body", "Board published; this answers your earlier message.")
    except AfmAgentError as err:
        print(f"reply failed: {err}", file=sys.stderr, flush=True)


def write_summary():
    with open(os.path.join(STAGE_DIR, "execution_summary.md"), "w", encoding="utf-8") as f:
        f.write(f"# Load fixture\n\nEmitted {counter} numbered load events.\n")


def main():
    answer_id = ask_human()
    run_shapes()
    wait_gate("phase1")
    run_phase(PHASE1)
    wait_gate("phase2")
    run_phase(PHASE2)
    wait_gate("finale")
    publish_board()
    if answer_id:
        reply_to_answer(answer_id)
    wait_gate("phase3")
    run_phase(PHASE3)
    write_summary()
    wait_gate("stop")


if __name__ == "__main__":
    try:
        main()
    except Stop:
        write_summary()
    except AfmAgentError as err:
        sys.exit(str(err))
