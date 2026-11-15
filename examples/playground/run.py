#!/usr/bin/env python3
"""Launch the synthetic playground in a separate directory with real AFM."""

import argparse
import json
import os
from pathlib import Path
import shutil
import tempfile


def prepare(root, scenario):
    root.mkdir(parents=True, exist_ok=True)
    marker = root / "playground.json"
    if marker.exists():
        if json.loads(marker.read_text()) != {"scenario": scenario}:
            raise ValueError("Choose the original scenario to resume, or a new directory.")
        return
    if list(root.iterdir()):
        raise ValueError("Use an empty directory; existing files are never overwritten.")
    shutil.copy2(Path(__file__).with_name("agent.py"), root / "agent.py")
    shutil.copy2(Path(__file__).with_name(f"report-{scenario}.png"), root / "report-preview.png")
    (root / "agent.py").chmod(0o755)
    (root / ".afm").mkdir()
    (root / ".afm/config.yaml").write_text("""docker:
  enabled: false
  autoShim: false
browser:
  enabled: false
server:
  open_browser: true
side_agent:
  enabled: false
collaboration:
  enabled: false
add_stage:
  enabled: false
client:
  command: ./agent.py
accounting:
  enabled: true
  show_money: true
pricing:
  models:
    demo-simulated: {input: 3, output: 15, cache_read: 0.3, cache_write: 5}
""")
    workers = {
        "development": ["backend", "frontend"],
        "review": ["correctness", "security", "reliability", "performance", "contracts"],
        "refactor": ["architect-a", "architect-b"],
    }[scenario]
    stages = [{"id": "scope", "name": "Choose the demo scope", "interactive": True}]
    stages += [{"id": worker, "depends_on": ["scope"]} for worker in workers]
    stages += [{"id": "report", "depends_on": workers}]
    for stage in stages:
        stage.setdefault("name", stage["id"].replace("-", " ").title())
        stage.update(agents=["auto"], command="./agent.py",
                     description="Scripted playground agent. No model calls.")
    flow = {"name": "playground-" + scenario,
            "description": "SIMULATED agents · real AFM · answer Scope to start",
            "root_dir": ".", "worktrees": False, "side_agent": False,
            "budget": {"tokens": "3k", "stop_policy": "medium"},
            "max_parallel": 5, "stages": stages}
    # JSON is also valid YAML; no third-party parser is needed for the launcher.
    (root / "flow.yaml").write_text(json.dumps(flow, indent=2) + "\n")
    marker.write_text(json.dumps({"scenario": scenario}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--scenario", choices=["development", "review", "refactor"], default="development")
    parser.add_argument("--dir", type=Path, help="Empty directory, or an existing playground to resume")
    parser.add_argument("--port", type=int, default=9876)
    parser.add_argument("--afm", default="afm", help="AFM executable")
    args = parser.parse_args()
    binary = shutil.which(args.afm)
    if not binary:
        parser.error("Install AFM first, or pass --afm /path/to/afm.")
    binary = str(Path(binary).resolve())
    root = (args.dir or Path(tempfile.mkdtemp(prefix="afm-playground-"))).resolve()
    try:
        prepare(root, args.scenario)
    except ValueError as error:
        parser.error(str(error))
    print(f"Synthetic {args.scenario} playground: {root}\n"
          f"Dashboard: http://localhost:{args.port}\n"
          "Open Scope and answer the question. Ctrl+C stops AFM.\n"
          f"Resume with the same command plus --dir {str(root)!r}.\n", flush=True)
    env = dict(os.environ, AFM_SIDEAGENT="0", AFM_COLLABORATION="0",
               AFM_ADD_STAGE="0", AFM_USE_DOCKER="0",
               AFM_ACCOUNTING="1", AFM_ACCOUNTING_SHOW_MONEY="1")
    os.chdir(root)
    os.execve(binary, [binary, "--dir", str(root), "run", "flow.yaml",
                       "--port", str(args.port)], env)


if __name__ == "__main__":
    main()
