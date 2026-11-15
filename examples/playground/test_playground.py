import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import tempfile
import time
import unittest
import urllib.error
import urllib.request

from run import prepare


class PlaygroundTest(unittest.TestCase):
    def test_resume_preserves_runtime_changes(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            prepare(root, "development")
            (root / "flow.yaml").write_text("user's saved budget")
            prepare(root, "development")
            self.assertEqual((root / "flow.yaml").read_text(), "user's saved budget")
            with self.assertRaises(ValueError):
                prepare(root, "review")

    def test_existing_files_are_preserved(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / "important.txt").write_text("keep")
            with self.assertRaises(ValueError):
                prepare(root, "development")
            self.assertEqual((root / "important.txt").read_text(), "keep")

    def test_worker_reports_usage_and_summary(self):
        with tempfile.TemporaryDirectory() as folder:
            stage = Path(folder) / "backend"
            stage.mkdir()
            result = subprocess.run([str(Path(__file__).with_name("agent.py"))],
                                    env=dict(os.environ, AFM_STAGE_DIR=str(stage), AFM_DEMO_DELAY="0"),
                                    text=True, capture_output=True, check=True)
            events = [json.loads(line) for line in result.stdout.splitlines()]
            self.assertEqual(events[-1]["usage"], {"input_tokens": 1200, "output_tokens": 240})
            self.assertIn("simulated", (stage / "execution_summary.md").read_text())

    @unittest.skipUnless(os.environ.get("AFM_PLAYGROUND_TEST_BIN"), "Set AFM_PLAYGROUND_TEST_BIN for integration")
    def test_budget_hold_and_resume_all_scenarios(self):
        binary = shutil.which(os.environ["AFM_PLAYGROUND_TEST_BIN"])
        for scenario in ("development", "review", "refactor"):
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as folder:
                root = Path(folder)
                prepare(root, scenario)
                config = root / ".afm/config.yaml"
                config.write_text(config.read_text().replace("open_browser: true", "open_browser: false"))
                with socket.socket() as listener:
                    listener.bind(("127.0.0.1", 0))
                    port = listener.getsockname()[1]
                base = f"http://127.0.0.1:{port}"

                def request(path, body=None):
                    req = urllib.request.Request(base + path,
                        data=None if body is None else json.dumps(body).encode(),
                        headers={"Content-Type": "application/json", "Origin": base})
                    with urllib.request.urlopen(req, timeout=3) as response:
                        return json.load(response)

                def until(callback):
                    deadline = time.monotonic() + 40
                    while time.monotonic() < deadline:
                        try:
                            value = callback()
                            if value:
                                return value
                        except (urllib.error.URLError, ConnectionError):
                            pass
                        time.sleep(0.1)
                    self.fail("Timed out waiting for the real AFM runtime")

                with (root / "process.log").open("w") as log:
                    process = subprocess.Popen([binary, "--dir", str(root), "run", "flow.yaml", "--port", str(port)],
                        cwd=root, stdout=log, stderr=log, env=dict(os.environ,
                            AFM_DEMO_DELAY="0.1", AFM_SIDEAGENT="0", AFM_COLLABORATION="0"))
                    try:
                        until(lambda: any(s["status"] == "awaiting_user_input" for s in request("/api/status")["stages"]))
                        request("/api/stages/scope/dialog/answer", {"id": "q1", "phase": "autonomous_execution",
                            "answer": "Regression test", "from_options": False})
                        view = until(lambda: (v if v.get("hold") else None)
                                     if (v := request("/api/budgets/view?scope=run")["view"]) else None)
                        stages = request("/api/status")["stages"]
                        self.assertNotEqual(next(s for s in stages if s["id"] == "report")["status"], "done")
                        request("/api/budgets/update", {"scope": "run", "expected_revision": view["revision"],
                            "override": {"kind": "limit", "spec": {"tokens": 20000, "stop_policy": "medium"}}, "resume": True})
                        until(lambda: all(s["status"] == "done" for s in request("/api/status")["stages"]))
                        report = next(root.glob(".afm/runs/*/report/report.md"))
                        self.assertIn("Regression test", report.read_text())
                        self.assertIn("[AFM image: report.png]", report.read_text())
                        with urllib.request.urlopen(base + "/api/stages/report/artifacts/report.png") as response:
                            self.assertEqual(response.headers["Content-Type"], "image/png")
                            self.assertTrue(response.read().startswith(b"\x89PNG\r\n\x1a\n"))
                        usage = next(root.glob(".afm/runs/*/usage.jsonl"))
                        records = [json.loads(line) for line in usage.read_text().splitlines()]
                        self.assertTrue(records)
                        self.assertTrue(all(record["priced"] for record in records))
                        self.assertTrue(all(record["estimated_cost_usd"] == 0.0072 for record in records))
                        self.assertIn("demo-simulated", usage.read_text())
                    finally:
                        process.send_signal(signal.SIGINT)
                        try:
                            process.wait(timeout=15)
                        except subprocess.TimeoutExpired:
                            process.kill()
                            process.wait()


if __name__ == "__main__":
    unittest.main()
