#!/usr/bin/env python3
"""Reject newly published engine implementation, vendor trees and local workspace pins."""
import pathlib
import subprocess
import sys

root = pathlib.Path(__file__).resolve().parent.parent
files = subprocess.check_output(["git", "ls-files"], cwd=root, text=True).splitlines()
forbidden = ("vendor/", "pkg/engine/", "pkg/executor/", "pkg/orchestrator/", "pkg/state/",
             "pkg/sideagent/", "pkg/memorypipeline/", "pkg/watch/", "pkg/execprep/",
             "pkg/prompts/", "pkg/runenv/")
errors = []
for name in files:
    path = root / name
    if not path.is_file():
        continue
    if name.startswith(forbidden) or name in ("go.work", "go.work.sum"):
        errors.append(name)
    if name in ("go.mod", "contracts/go.mod") and "replace " in path.read_text():
        errors.append(name + ": committed replace directive")
    if name.endswith(".go") and not name.endswith("_test.go"):
        text = path.read_text()
        if '"github.com/akopichin/afm-engine/internal/' in text:
            errors.append(name + ": private internal import")
        if '"github.com/akopichin/afm-engine/testkit/' in text:
            errors.append(name + ": production depends on test fixtures")
        if name.startswith("contracts/") and '"github.com/akopichin/afm-engine/' in text:
            errors.append(name + ": contracts depend on engine")
if errors:
    sys.exit("Public source boundary failed:\n" + "\n".join(errors))
print("Public source boundary passed")
