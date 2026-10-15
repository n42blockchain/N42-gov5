import copy
import unittest

import restart_check


class RestartConvergenceTest(unittest.TestCase):
    def setUp(self):
        self.identity = {"version": 1, "namespace": "gov5-performance-5m-v1", "chain_id": "4205001", "genesis_hash": "0x" + "11" * 32}
        self.manifest = {"identity": self.identity, "genesis_sha256": "22" * 32,
                         "validators": [{"api_socket": f"/private/v{i}.sock"} for i in range(7)]}
        self.status = {"identity": self.identity, "number": 98, "block_hash": "0x" + "33" * 32, "committed_executed_transactions": 8}
        self.baseline = {"kind": "functional-check-not-tps", "identity": self.identity, "genesis_sha256": "22" * 32,
                         "final": [copy.deepcopy(self.status) for _ in range(7)]}

    def test_retained_nonzero_count_is_not_summed_or_resubmitted(self):
        paths = []
        def read(path, identity):
            paths.append(path)
            self.assertEqual(identity, self.identity)
            return copy.deepcopy(self.status)
        result = restart_check.converge(self.manifest, self.baseline, read=read)
        self.assertEqual(len(set(paths)), 7)
        self.assertEqual(result["counter_delta"], 0)
        self.assertEqual(result["submitted_transactions"], 0)
        self.assertEqual([item["committed_executed_transactions"] for item in result["final"]], [8] * 7)
        self.assertIn("does not prove", result["limitation"])

    def test_zero_or_wrong_identity_baseline_rejected_before_network(self):
        for field, replacement in (("number", 0), ("committed_executed_transactions", 0), ("identity", {}), ("block_hash", "0x" + "00" * 32)):
            baseline = copy.deepcopy(self.baseline)
            baseline["final"][0][field] = replacement
            with self.subTest(field=field), self.assertRaises(ValueError):
                restart_check.converge(self.manifest, baseline, read=lambda *args: self.fail("untrusted baseline reached network"))
        for field in ("identity", "genesis_sha256", "kind"):
            baseline = copy.deepcopy(self.baseline)
            baseline[field] = "wrong"
            with self.subTest(field=field), self.assertRaises(ValueError):
                restart_check.baseline_head(self.manifest, baseline)

    def test_live_regression_conflict_and_count_change_rejected(self):
        for field, replacement in (("number", 97), ("number", True), ("number", 1 << 64),
                                   ("committed_executed_transactions", 0), ("committed_executed_transactions", 9),
                                   ("committed_executed_transactions", 8.0), ("block_hash", "0x" + "44" * 32), ("identity", {})):
            observation = copy.deepcopy(self.status)
            observation[field] = replacement
            with self.subTest(field=field, replacement=replacement), self.assertRaises(ValueError):
                restart_check.converge(self.manifest, self.baseline, read=lambda *args: observation)

    def test_empty_blocks_may_advance_height_but_not_count(self):
        calls = 0
        def read(*args):
            nonlocal calls
            calls += 1
            value = copy.deepcopy(self.status)
            if calls <= 7:
                value["number"] += calls % 2
                if value["number"] == 99:
                    value["block_hash"] = "0x" + "44" * 32
            else:
                value.update(number=100, block_hash="0x" + "55" * 32)
            return value
        result = restart_check.converge(self.manifest, self.baseline, read=read, wait=lambda _: None)
        self.assertEqual(calls, 14)
        self.assertEqual(result["final"][0]["number"], 100)
        self.assertEqual(result["final"][0]["committed_executed_transactions"], 8)

    def test_observation_cannot_regress_after_first_sample(self):
        calls = 0
        def read(*args):
            nonlocal calls
            calls += 1
            value = copy.deepcopy(self.status)
            # First sample disagrees (99/100); next sample tries to rewind node0.
            value.update(number=100 if calls == 1 else 99, block_hash="0x" + "44" * 32)
            return value
        with self.assertRaisesRegex(ValueError, "observations regressed"):
            restart_check.converge(self.manifest, self.baseline, read=read, wait=lambda _: None)

    def test_restart_requires_empty_block_progress_and_empty_queues(self):
        now = [0.0]
        observation = dict(self.status, queued_transactions=0)
        with self.assertRaises(TimeoutError):
            restart_check.converge(self.manifest, self.baseline, timeout=1, read=lambda *args: observation,
                                   clock=lambda: now[0], wait=lambda seconds: now.__setitem__(0, now[0] + seconds), require_progress=True)
        observation.update(number=99, block_hash="0x" + "44" * 32)
        result = restart_check.converge(self.manifest, self.baseline, read=lambda *args: observation, require_progress=True)
        self.assertTrue(result["empty_block_progress_required"])
        observation["queued_transactions"] = 1
        with self.assertRaisesRegex(ValueError, "empty transaction queues"):
            restart_check.converge(self.manifest, self.baseline, read=lambda *args: observation, require_progress=True)

    def test_unavailable_socket_times_out_without_launch_or_retrying_bad_json(self):
        now = [0.0]
        def wait(seconds):
            now[0] += seconds
        def unavailable(*args):
            raise FileNotFoundError("not ready")
        with self.assertRaises(TimeoutError):
            restart_check.converge(self.manifest, self.baseline, timeout=1, read=unavailable, clock=lambda: now[0], wait=wait)
        def bad_json(*args):
            raise ValueError("bad JSON")
        with self.assertRaisesRegex(ValueError, "bad JSON"):
            restart_check.converge(self.manifest, self.baseline, read=bad_json, wait=lambda _: self.fail("bad JSON retried"))


if __name__ == "__main__":
    unittest.main()
