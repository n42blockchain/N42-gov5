import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import repair_watch


class RepairWatchRetryTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="gov5-watch-test-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.attempt = self.root / "attempt"

    def result(self, name, error):
        path = self.root / (name + ".log")
        with path.open("x") as output:
            json.dump({"kind": "functional-check-not-tps", "error": error}, output)
        return path

    def test_resources_before_attempt_retry_with_unique_logs(self):
        calls, waits, notices = [], [], []
        def run(name):
            calls.append(name)
            if len(calls) == 1:
                return 1, self.result(name, "hardware blocked: active claim")
            self.attempt.mkdir()
            return 0, self.result(name, "unused")
        repair_watch.functional_when_available(run, self.attempt, lambda *args: notices.append(args), waits.append)
        self.assertEqual(calls, ["functional-run-001", "functional-run-002"])
        self.assertEqual(waits, [60])
        self.assertEqual(len(notices), 1)
        self.assertEqual(len(list(self.root.glob("*.log"))), 2)

    def test_started_attempt_and_dangling_symlink_never_retry(self):
        log = self.result("blocked", "hardware blocked: active claim")
        self.attempt.mkdir()
        self.assertFalse(repair_watch.resource_wait_only(1, log, self.attempt))
        self.attempt.rmdir()
        self.attempt.symlink_to(self.root / "missing")
        self.assertFalse(repair_watch.resource_wait_only(1, log, self.attempt))

    def test_real_functional_failure_unknown_or_signal_stop(self):
        for index, error in enumerate(("signed functional checker failed", "seven runtime API readiness timeout", "[Errno 17] File exists", "port occupied")):
            path = self.result(str(index), error)
            calls = []
            def run(name):
                calls.append(name)
                return 1, path
            with self.assertRaises(RuntimeError):
                repair_watch.functional_when_available(run, self.attempt, lambda *args: self.fail("retry notice"), lambda seconds: self.fail("retry wait"))
            self.assertEqual(len(calls), 1)
        self.assertFalse(repair_watch.resource_wait_only(-15, self.result("signal", "hardware blocked: active claim"), self.attempt))

    def test_malformed_duplicate_oversize_json_stop(self):
        for index, raw in enumerate(("not-json", "{}\n{}", '[]', '{"error":"hardware blocked: x","error":"hardware blocked: y","kind":"functional-check-not-tps"}', " " * 4097)):
            path = self.root / (str(index) + ".log")
            path.write_text(raw)
            self.assertFalse(repair_watch.resource_wait_only(1, path, self.attempt))

    def test_timeout_propagates_without_retry(self):
        calls = []
        def run(name):
            calls.append(name)
            raise subprocess.TimeoutExpired("fleet", 600)
        with self.assertRaises(subprocess.TimeoutExpired):
            repair_watch.functional_when_available(run, self.attempt, lambda *args: self.fail("notice"), lambda seconds: self.fail("wait"))
        self.assertEqual(len(calls), 1)

    def test_runner_modules_must_match_committed_bytes(self):
        watch = object.__new__(repair_watch.RepairWatch)
        watch.proof = {"runner_commit": "7774408d6cf17f74fe49aaf25badb7626204365b"}
        watch.source, watch.runner_directory = self.root, self.root
        for name in ("fleet.py", "preflight.py"):
            (self.root / name).write_bytes(name.encode())
        def committed(arguments, **kwargs):
            return arguments[2].split(":")[-1].split("/")[-1].encode()
        with patch.object(repair_watch.subprocess, "check_output", side_effect=committed):
            self.assertEqual(set(watch.verify_runner()), {"fleet.py", "preflight.py"})
            (self.root / "preflight.py").write_bytes(b"changed")
            with self.assertRaisesRegex(RuntimeError, "module changed: preflight.py"):
                watch.verify_runner()

    def test_command_timeout_allows_functional_cleanup_and_stops(self):
        watch = object.__new__(repair_watch.RepairWatch)
        watch.artifacts, watch.source = self.root, self.root
        watch.state = {"stages": []}
        watch.save = lambda: None
        watch.say = lambda message: None
        class Process:
            pid = 123456
            waits = []
            def wait(self, timeout):
                self.waits.append(timeout)
                if len(self.waits) == 1:
                    raise subprocess.TimeoutExpired("fleet", timeout)
                return 0
            def poll(self):
                return None
        process = Process()
        with patch.object(repair_watch.subprocess, "Popen", return_value=process), patch.object(repair_watch.os, "killpg") as terminate:
            with self.assertRaises(subprocess.TimeoutExpired):
                watch.command("functional-run-001", ["unused"], 600, allow_failure=True)
        self.assertEqual(process.waits, [600, 90])
        terminate.assert_called_once_with(process.pid, repair_watch.signal.SIGTERM)
        self.assertEqual(watch.state["stages"][0]["outcome"], "timeout")
        self.assertIsNone(watch.active)

    def test_explicit_claim_contention_can_wait(self):
        for index, error in enumerate(("hardware contention after claim: active claim", "hardware contention: owned BOX claim changed")):
            self.assertTrue(repair_watch.resource_wait_only(1, self.result(str(index), error), self.attempt))


if __name__ == "__main__":
    unittest.main()
