#!/usr/bin/env python3
"""Check out engine tests from the exact module revision selected by AFM."""
import json
import pathlib
import re
import subprocess
import sys

MODULE = "github.com/akopichin/afm-engine"


def output(*args):
    return subprocess.check_output(args, text=True).strip()


def pinned_revision():
    module = json.loads(output("go", "list", "-m", "-json", MODULE))
    if module.get("Replace") or module.get("Main"):
        raise ValueError("Engine checks require a pinned module with GOWORK=off")
    downloaded = json.loads(output("go", "mod", "download", "-json", MODULE + "@" + module["Version"]))
    info = json.loads(pathlib.Path(downloaded["Info"]).read_text())
    revision = (downloaded.get("Origin") or info.get("Origin", {})).get("Hash", "")
    if not re.fullmatch(r"[0-9a-f]{40}", revision):
        raise ValueError("Missing exact engine Git revision")
    return revision


def checkout(destination):
    revision = pinned_revision()
    destination.mkdir(parents=True, exist_ok=False)
    subprocess.run(["git", "init", "--quiet", str(destination)], check=True)
    subprocess.run(["git", "-C", str(destination), "fetch", "--quiet", "--depth=1",
                    "https://github.com/akopichin/afm-engine.git", revision], check=True)
    subprocess.run(["git", "-C", str(destination), "checkout", "--quiet", "--detach", "FETCH_HEAD"], check=True)
    if output("git", "-C", str(destination), "rev-parse", "HEAD") != revision:
        raise ValueError("Engine checkout differs from the pinned module revision")
    print("Engine revision: " + revision)


if __name__ == "__main__":
    checkout(pathlib.Path(sys.argv[1]))
