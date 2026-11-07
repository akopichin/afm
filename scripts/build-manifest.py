#!/usr/bin/env python3
"""Record exact product/module revisions and hashes of distribution artifacts."""
import hashlib
import json
import pathlib
import re
import subprocess
import sys

output = pathlib.Path(sys.argv[1])

def command(*args):
    return subprocess.check_output(args, text=True).strip()

if command("git", "status", "--porcelain", "--untracked-files=no"):
    sys.exit("Release manifest requires a clean tracked product tree")

modules = {}
for name in ("github.com/akopichin/afm-engine", "github.com/akopichin/afm/contracts"):
    module = json.loads(command("go", "list", "-m", "-json", name))
    if module.get("Replace") or module.get("Main"):
        sys.exit("Release manifest requires pinned modules with GOWORK=off: " + name)
    downloaded = json.loads(command("go", "mod", "download", "-json", name + "@" + module["Version"]))
    info = json.loads(pathlib.Path(downloaded["Info"]).read_text())
    revision = (downloaded.get("Origin") or info.get("Origin", {})).get("Hash", "")
    if not re.fullmatch(r"[0-9a-f]{40}", revision):
        sys.exit("Missing exact Git revision for module: " + name)
    modules[name] = {key: downloaded[key] for key in ("Version", "Sum", "GoModSum")}
    modules[name]["commit"] = revision

artifacts = {}
for path in sorted(output.parent.iterdir()):
    if path.name.endswith((".tar.gz", ".zip")):
        artifacts[path.name] = hashlib.sha256(path.read_bytes()).hexdigest()
if not artifacts:
    sys.exit("No release archives found")
manifest = {
    "afm_commit": command("git", "rev-parse", "HEAD"),
    "go_toolchain": command("go", "version"),
    "runtime_protocol_version": int(re.search(
        r"runtimeProtocolVersion\s*=\s*(\d+)", pathlib.Path("pkg/server/runtime.go").read_text()
    ).group(1)),
    "modules": modules,
    "artifacts_sha256": artifacts,
}
output.write_text(json.dumps(manifest, indent=2) + "\n")
