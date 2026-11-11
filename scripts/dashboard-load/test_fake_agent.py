"""Checks that a refused afm agent call is survivable where the fixture expects it.

Run: python3 -m unittest scripts/dashboard-load/test_fake_agent.py
"""

import contextlib
import io
import json
import os
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import fake_agent  # noqa: E402
import fake_report_agent  # noqa: E402

REFUSED = subprocess.CompletedProcess([], 1, stdout="", stderr="refused")


class RefusedCallTest(unittest.TestCase):
    def test_helper_raises_one_exception_type(self):
        with mock.patch.object(subprocess, "run", return_value=REFUSED):
            with self.assertRaises(fake_agent.AfmAgentError):
                fake_agent.afm_agent("spawn", "--profile", "x")

    def test_refused_spawn_is_logged_not_fatal(self):
        fake_agent.worker_spawned = False
        with mock.patch.object(fake_agent, "gate_exists", return_value=True), \
                mock.patch.object(subprocess, "run", return_value=REFUSED):
            fake_agent.spawn_worker_on_gate()

    def test_refused_reply_is_logged_not_fatal(self):
        with mock.patch.object(subprocess, "run", return_value=REFUSED):
            fake_agent.reply_to_answer("mm-1")

    def test_json_result_goes_out_as_conversation_message(self):
        with mock.patch.object(fake_agent, "afm_agent") as send:
            fake_agent.emit_json_result()
        args = send.call_args.args
        self.assertEqual(args[:5], ("send", "--to", "user", "--kind", "message"))
        self.assertIn('"kind": "result"', args[6])


class ReportAgentTest(unittest.TestCase):
    def test_prints_150_distinct_prelude_rows_before_done(self):
        with tempfile.TemporaryDirectory() as stage_dir, \
                mock.patch.object(fake_report_agent, "STAGE_DIR", stage_dir), \
                contextlib.redirect_stdout(io.StringIO()) as out:
            fake_report_agent.main()
        texts = [json.loads(l)["message"]["content"][0]["text"] for l in out.getvalue().splitlines()]
        self.assertEqual(texts[:150], [f"report prelude line {k}" for k in range(1, 151)])
        self.assertEqual(texts[150:], ["Done; verify runs next."])


if __name__ == "__main__":
    unittest.main()
