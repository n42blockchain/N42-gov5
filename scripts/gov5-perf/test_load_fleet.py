import contextlib
import copy
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import load_fleet


class LoadEvidenceTest(unittest.TestCase):
    def setUp(self):
        identity = {"version": 1, "chain_id": "4205001", "namespace": "gov5-performance-5m-v1", "genesis_hash": "0x" + "11" * 32}
        self.manifest = {"identity": identity, "genesis_sha256": "22" * 32}
        initial = {"identity": identity, "number": 0, "block_hash": identity["genesis_hash"], "committed_executed_transactions": 0, "queued_transactions": 0}
        final = dict(initial, number=10, block_hash="0x" + "33" * 32, committed_executed_transactions=32768)
        self.evidence = {"kind": "finite-corpus-committed-executed-throughput-not-capacity", "passed": True, **self.manifest,
                         "corpus_transactions": 32768, "senders": 64, "nonces_per_sender": 512, "batch_size": 8192, "entry_validator": 0,
                         "initial": [copy.deepcopy(initial) for _ in range(7)], "final": [copy.deepcopy(final) for _ in range(7)],
                         "window_observed_seconds": 20.01, "window_end": dict(final, committed_executed_transactions=16384), "committed_executed_tps": 16384 / 20.01}

    def test_window_uses_one_counter_and_drain_is_separate(self):
        load_fleet.verify_result(self.manifest, self.evidence)
        self.evidence["committed_executed_tps"] *= 7
        with self.assertRaisesRegex(ValueError, "single canonical counter"):
            load_fleet.verify_result(self.manifest, self.evidence)

    def test_wrong_identity_or_nonfresh_or_incomplete_final_rejected(self):
        for field, replacement in (("passed", False), ("identity", {}), ("window_observed_seconds", 19), ("corpus_transactions", 65536)):
            value = copy.deepcopy(self.evidence)
            value[field] = replacement
            with self.subTest(field=field), self.assertRaises(ValueError):
                load_fleet.verify_result(self.manifest, value)
        for phase, field, replacement in (("initial", "committed_executed_transactions", 8), ("final", "committed_executed_transactions", 32767), ("final", "block_hash", "wrong"), ("final", "queued_transactions", 1)):
            value = copy.deepcopy(self.evidence)
            value[phase][0][field] = replacement
            with self.subTest(phase=phase, field=field), self.assertRaises(ValueError):
                load_fleet.verify_result(self.manifest, value)

    def test_existing_data_refused_before_any_spawn_or_reset(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "e").mkdir()
            (root / "e" / "retained-data").write_text("preserve")
            manifest = {"root": str(root), "owner_dir": str(root / "e"), "validators": [{"data_dir": str(root / f"v{i}")} for i in range(7)]}
            with patch.object(load_fleet.fleet, "quiet_checks"), patch.object(load_fleet.fleet, "claim", return_value=contextlib.nullcontext({})), patch.object(load_fleet.fleet, "validate"), patch.object(load_fleet.subprocess, "Popen", side_effect=AssertionError("spawned")):
                with self.assertRaisesRegex(ValueError, "existing/partial"):
                    load_fleet.run(manifest, {})
            self.assertEqual((root / "e" / "retained-data").read_text(), "preserve")
            self.assertFalse((root / "load-run").exists())


if __name__ == "__main__":
    unittest.main()
