#!/usr/bin/env python3
"""Publish the already generated cask for one release channel, without rebuilding."""
import base64
import json
import pathlib
import re
import subprocess
import sys

tag, directory = sys.argv[1:]
match = re.fullmatch(r"v([1-9][0-9]*)\.[0-9]+\.[0-9]+(?:-(alpha|beta)\.[0-9]+)?", tag)
if not match:
    sys.exit("Unsupported release tag")
major, channel = int(match[1]), match[2]
if major < 3 and channel:
    sys.exit(0)
name = f"afm@{major}" if major < 3 else ("afm-" + channel if channel else "afm")
source = pathlib.Path(directory) / "homebrew" / "Casks" / (name + ".rb")
content = source.read_bytes()
endpoint = f"repos/akopichin/homebrew-afm/contents/Casks/{name}.rb"
probe = subprocess.run(["gh", "api", endpoint], text=True, capture_output=True)
request = {"message": f"Update {name} to {tag}", "content": base64.b64encode(content).decode()}
if probe.returncode == 0:
    existing = json.loads(probe.stdout)
    if base64.b64decode(existing["content"]) == content:
        sys.exit(0)
    request["sha"] = existing["sha"]
elif "HTTP 404" not in probe.stderr:
    sys.exit("Cannot read target cask: " + probe.stderr)
subprocess.run(["gh", "api", "--method", "PUT", endpoint, "--input", "-"],
               input=json.dumps(request), text=True, check=True, stdout=subprocess.DEVNULL)
