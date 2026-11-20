#!/usr/bin/env python3
"""Publish the already generated cask for one release channel, without rebuilding."""
import base64
import json
import pathlib
import re
import subprocess
import sys

tag, directory = sys.argv[1:]
directory = pathlib.Path(directory)
desktop = re.fullmatch(r"ds-v((?:0|[1-9][0-9]*)(?:\.(?:0|[1-9][0-9]*)){2})", tag)
if desktop:
    name = "afm-desktop"
    version = desktop[1]
    template = pathlib.Path(__file__).resolve().parents[1] / "desktop/homebrew/afm-desktop.rb.in"
    cask = template.read_text().replace("@VERSION@", version)
    checksums = {}
    for line in (directory / "SHA256SUMS").read_text().splitlines():
        digest, filename = line.split()
        if not re.fullmatch("[0-9a-f]{64}", digest) or filename in checksums:
            sys.exit("Invalid desktop checksums")
        checksums[filename] = digest
    for arch in ("arm64", "x64"):
        cask = cask.replace("@" + arch.upper() + "@", checksums[f"afm-desktop-{version}-darwin-{arch}.zip"])
    content = cask.encode()
else:
    match = re.fullmatch(r"v([1-9][0-9]*)\.[0-9]+\.[0-9]+(?:-(alpha|beta)\.[0-9]+)?", tag)
    if not match:
        sys.exit("Unsupported release tag")
    major, channel = int(match[1]), match[2]
    if major < 3 and channel:
        sys.exit(0)
    name = f"afm@{major}" if major < 3 else ("afm-" + channel if channel else "afm")
    content = (directory / "homebrew" / "Casks" / (name + ".rb")).read_bytes()
endpoint = f"repos/akopichin/homebrew-afm/contents/Casks/{name}.rb"
probe = subprocess.run(["gh", "api", endpoint], text=True, capture_output=True)
request = {"message": f"Update {name} to {tag}", "content": base64.b64encode(content).decode()}
if probe.returncode == 0:
    existing = json.loads(probe.stdout)
    current = base64.b64decode(existing["content"])
    if desktop:
        previous = re.search(rb'^  version "(\d+\.\d+\.\d+)"', current, re.MULTILINE)
        if not previous:
            sys.exit("Unexpected afm-desktop cask version")
        if tuple(map(int, previous[1].split(b"."))) > tuple(map(int, version.split("."))):
            sys.exit(0)
    if current == content:
        sys.exit(0)
    request["sha"] = existing["sha"]
elif "HTTP 404" not in probe.stderr:
    sys.exit("Cannot read target cask: " + probe.stderr)
subprocess.run(["gh", "api", "--method", "PUT", endpoint, "--input", "-"],
               input=json.dumps(request), text=True, check=True, stdout=subprocess.DEVNULL)
