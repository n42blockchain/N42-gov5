import contextlib
import hashlib
import json
import os
from pathlib import Path
import unittest
from unittest.mock import patch

import fleet
import preflight
import strict_fleet
import test_fleet


class StrictRuntimeValidationTest(unittest.TestCase):
    def setUp(self):
        helper = test_fleet.FleetTest()
        helper.setUp()
        self.addCleanup(helper.doCleanups)
        self.base, self.root, self.write = helper.base, helper.root, helper.write
        self.m = helper.fixture()
        self.source = self.base / "source"
        self.source.mkdir(mode=0o700)
        self.m.pop("load_keys")
        (self.root / "load/keys.json").unlink()
        (self.root / "load").rmdir()
        self.m["schema_version"] = 2
        self.m["supply"] = {"root": str(self.source), "manifest_sha256": "", "corpus_id": "0x" + "22" * 32, "genesis_file_sha256": self.m["genesis_sha256"], "block_interval_ms": 100}
        self.write(self.source / "genesis.json", fleet.load(self.root / "genesis.json"))
        for v in self.m["validators"]:
            directory = Path(v["data_dir"])
            src = self.source / f"v{v['index']}"
            src.mkdir(mode=0o700)
            (src / "keystore").mkdir(mode=0o700)
            name = "bls_" + v["address"].lower() + ".key"
            (src / "keystore" / name).write_bytes((directory / "keystore" / name).read_bytes())
            (src / "keystore" / name).chmod(0o600)
            config = fleet.load(directory / "node.json")
            config["node"].update(performance_owner_queue=True, performance_queue_max_transactions=163000, performance_queue_max_encoded_bytes=32 << 20)
            config["miner"]["blockintervalms"] = 100
            self.write(directory / "node.json", config)
        raw_shard = b"x" * (4 * 84 + 8 * 5)  # Structural bound only; Go tests validate real GCF.
        (self.source / "shard-00.gcf").write_bytes(raw_shard)
        (self.source / "shard-00.gcf").chmod(0o600)
        self.corpus = {
            "descriptor": {"version": 1, "identity": self.m["identity"], "genesis_file_sha256": "0x" + self.m["genesis_sha256"], "codec": "ethereum-signed-rlp-eip155-v1", "signature": "secp256k1-eip155-v1", "key_derivation": "gov5-supply-seed-hmac-sha256-v1", "seed_commitment": "0x" + "33" * 32, "sender_count": 4, "nonce_start": 0, "nonces_per_sender": 2, "recipient_rule": "gov5-supply-hotset-keccak-v1", "recipient_count": 4, "shard_count": 1, "frame_transactions": 2},
            "corpus_id": self.m["supply"]["corpus_id"],
            "shards": [{"index": 0, "file": "shard-00.gcf", "transactions": 8, "frames": 4, "bytes": len(raw_shard), "sha256": "0x" + hashlib.sha256(raw_shard).hexdigest()}],
        }
        self.publish()

    def publish(self):
        self.write(self.source / "manifest.json", self.corpus)
        self.m["supply"]["manifest_sha256"] = hashlib.sha256((self.source / "manifest.json").read_bytes()).hexdigest()
        self.write(self.root / "run.json", self.m)

    def validate(self):
        return strict_fleet.validate(self.root / "run.json", self.base, self.base, check_ports=False)

    def test_schema_two_validation_is_readonly_and_checks_effective_configuration(self):
        before = {str(p): (p.stat().st_ino, p.stat().st_mtime_ns, p.read_bytes()) for root in (self.source, self.root) for p in root.rglob("*") if p.is_file()}
        self.assertEqual(self.validate(), self.m)
        after = {str(p): (p.stat().st_ino, p.stat().st_mtime_ns, p.read_bytes()) for root in (self.source, self.root) for p in root.rglob("*") if p.is_file()}
        self.assertEqual(before, after)
        path = self.root / "v0/node.json"
        config = fleet.load(path)
        config["miner"]["blockintervalms"] = 2000
        self.write(path, config)
        with self.assertRaisesRegex(ValueError, "configuration/pacing"):
            self.validate()

    def test_schema_one_and_fake_load_keys_are_rejected(self):
        for field, value in (("schema_version", 1), ("load_keys", "fake")):
            m = dict(self.m)
            m[field] = value
            self.write(self.root / "run.json", m)
            with self.assertRaisesRegex(ValueError, "schema-2"):
                self.validate()

    def test_manifest_nonce_profile_and_shard_shape_are_bound(self):
        for field, value in (("nonce_start", 1), ("signature", "gateway-trusted"), ("shard_count", 65), ("frame_transactions", True)):
            previous = self.corpus["descriptor"][field]
            self.corpus["descriptor"][field] = value
            self.publish()
            with self.assertRaises(ValueError):
                self.validate()
            self.corpus["descriptor"][field] = previous
        self.publish()
        (self.source / "manifest.json").write_text("{}")
        with self.assertRaises(ValueError):
            self.validate()

    def test_go_descriptor_and_shard_allocation_limits_match(self):
        for field, value in (("sender_count", 12501), ("nonces_per_sender", 600000001), ("recipient_count", 100001), ("version", True), ("nonce_start", False)):
            previous = self.corpus["descriptor"][field]
            self.corpus["descriptor"][field] = value
            self.publish()
            with self.assertRaises(ValueError):
                self.validate()
            self.corpus["descriptor"][field] = previous
        for field, value in (("transactions", 7), ("frames", 3), ("bytes", 375), ("index", False)):
            previous = self.corpus["shards"][0][field]
            self.corpus["shards"][0][field] = value
            self.publish()
            with self.assertRaises(ValueError):
                self.validate()
            self.corpus["shards"][0][field] = previous

    def test_source_and_runtime_genesis_are_exact_bytes(self):
        path = self.root / "genesis.json"
        path.write_bytes(path.read_bytes() + b"\n")
        with self.assertRaisesRegex(ValueError, "genesis bytes"):
            self.validate()

    def test_no_owner_keys_sender_material_or_additional_validator_keys(self):
        for name, directory in (("e/keystore", True), ("sender.seed", False), ("load", True), ("v0/keystore/extra.key", False)):
            p = self.root / name
            if directory:
                p.mkdir(mode=0o700)
            else:
                p.write_text("must not be read as authorization")
                p.chmod(0o600)
            with self.assertRaises(ValueError):
                self.validate()
            if directory:
                p.rmdir()
            else:
                p.unlink()

    def test_corresponding_validator_key_and_public_order_must_match(self):
        v = self.m["validators"][0]
        path = self.root / "v0/keystore" / ("bls_" + v["address"] + ".key")
        path.write_text("changed private fixture")
        with self.assertRaisesRegex(ValueError, "source validator key"):
            self.validate()
        path.write_text("private-fixture")
        self.m["validators"][0]["address"] = self.m["validators"][1]["address"]
        self.write(self.root / "run.json", self.m)
        with self.assertRaises(ValueError):
            self.validate()

    def test_discovery_queue_cap_and_extraneous_config_do_not_silently_override(self):
        path = self.root / "v0/node.json"
        original = fleet.load(path)
        for section, field, value in (("p2p", "no_discovery", False), ("node", "performance_owner_queue", False), ("node", "performance_queue_max_transactions", 200000), ("node", "http", True)):
            changed = json.loads(json.dumps(original))
            changed[section][field] = value
            self.write(path, changed)
            with self.assertRaisesRegex(ValueError, "configuration/pacing"):
                self.validate()

    def test_metadata_alias_fifo_size_and_duplicate_keys_reject_before_io(self):
        p = self.root / "run.json"
        raw = p.read_bytes()
        p.unlink()
        os.mkfifo(p, 0o600)
        with self.assertRaises(ValueError):
            self.validate()
        p.unlink()
        p.symlink_to(self.source / "manifest.json")
        with self.assertRaises(ValueError):
            self.validate()
        p.unlink()
        p.write_bytes(raw[:-1] + b', "schema_version": 2}')
        p.chmod(0o600)
        with self.assertRaisesRegex(ValueError, "duplicate"):
            self.validate()

    def test_live_ports_and_inherited_environment_are_rejected(self):
        with patch.object(preflight, "occupied_ports", return_value={25000}):
            with self.assertRaisesRegex(ValueError, "ports occupied"):
                strict_fleet.validate(self.root / "run.json", self.base, self.base)
        with patch.dict(os.environ, {"N42_PROFILE": "other"}):
            with self.assertRaisesRegex(ValueError, "environment"):
                self.validate()


    def test_initialization_failure_preserves_attempt_and_records_cleanup(self):
        class FailedInit:
            pid = 987654
            args = ["fake-init"]
            returncode = 1
            def poll(self): return 1
            def wait(self, timeout=None): return 1
        with patch.object(fleet, "quiet_checks"), patch.object(fleet, "claim", return_value=contextlib.nullcontext({})), patch.object(fleet, "check_owned_claim"), patch.object(strict_fleet, "validate", return_value=self.m), patch.object(strict_fleet, "disk_budget", return_value={"minimum_free_bytes": 0}), patch.object(strict_fleet.subprocess, "Popen", return_value=FailedInit()), patch.object(strict_fleet.os, "sched_getaffinity", side_effect=ProcessLookupError):
            with self.assertRaisesRegex(ValueError, "initialization failed"):
                strict_fleet.run(self.m, {"path": "/fake/node"}, {}, {}, "failure", initialize=True)
        logs = self.root / "attempts/failure"
        self.assertTrue(logs.is_dir())
        lifecycle = fleet.load(logs / "process-lifecycle.json")
        self.assertEqual(lifecycle["processes"][0]["return_code"], 1)
        self.assertEqual(lifecycle["processes"][0]["affinity_sample"], "exited-before-sample")
        self.assertFalse((logs / "strict-fleet-result.json").exists())
        self.assertFalse((self.root / "e/chaindata").exists())

    def test_tail_claim_loss_does_not_return_success_and_stops_only_owned_children(self):
        manifest = self.m
        root = self.root
        processes = []
        epoch = [1] + [0] * 15
        identity = manifest["identity"]
        canonical = {"identity": identity, "number": 10, "block_hash": "0x" + "33" * 32, "committed_executed_transactions": 0}
        @contextlib.contextmanager
        def lost_at_tail():
            yield {}
            raise fleet.HardwareContention("claim replaced during cleanup")
        class FakeProcess:
            def __init__(self, args, **kwargs):
                self.args = args
                self.pid = 980000 + len(processes)
                self.returncode = None
                self.signals = []
                processes.append(self)
                if args[0] == "/fake/owner":
                    directory = root / "e/execution-rpc"
                    directory.mkdir(mode=0o700)
                    (directory / "execution.sock").touch(mode=0o600)
                    (directory / "ingress.sock").touch(mode=0o600)
                if args[0] == "/fake/replay":
                    value = json.loads((Path(__file__).parent / "testdata/strict-result.json").read_text())
                    value.update(identity=identity, manifest_sha256="0x" + manifest["supply"]["manifest_sha256"], runtime_manifest=str(root / "run.json"))
                    value["owner_final"].update(Identity=identity, ManifestHash=value["manifest_sha256"])
                    for status in value["final"]:
                        status["identity"] = identity
                    directory = root / "strict-attempts/s1"
                    directory.mkdir(mode=0o700, parents=True)
                    (directory / "result.json").write_text(json.dumps(value))
                    (directory / "result.json").chmod(0o600)
                    self.returncode = 0
            def poll(self): return self.returncode
            def send_signal(self, sig):
                self.signals.append(sig)
                self.returncode = 0
            def wait(self, timeout=None): return self.returncode
        with patch.object(fleet, "quiet_checks"), patch.object(fleet, "claim", side_effect=lost_at_tail), patch.object(fleet, "check_owned_claim"), patch.object(fleet, "status", return_value=canonical), patch.object(strict_fleet, "validate", return_value=manifest), patch.object(strict_fleet, "disk_budget", return_value={"minimum_free_bytes": 0}), patch.object(strict_fleet.subprocess, "Popen", side_effect=FakeProcess), patch.object(strict_fleet.os, "sched_getaffinity", return_value={0, 1}):
            with self.assertRaises(fleet.HardwareContention):
                strict_fleet.run(manifest, {"path": "/fake/node"}, {"path": "/fake/owner"}, {"path": "/fake/replay"}, "s1")
        self.assertEqual(len(processes), 9)
        self.assertTrue(all(p.returncode == 0 for p in processes))
        self.assertTrue(all(len(p.signals) == 1 for p in processes[:8]))
        self.assertEqual(processes[8].signals, [])
        self.assertFalse((root / "attempts/s1/strict-fleet-result.json").exists())
        self.assertTrue((root / "attempts/s1/strict-fleet-observation.json").exists())
        self.assertTrue((root / "attempts/s1/process-lifecycle.json").exists())


    def test_changed_runtime_after_claim_is_rejected_before_attempt_or_spawn(self):
        changed = json.loads(json.dumps(self.m))
        changed["supply"]["block_interval_ms"] = 200
        with patch.object(fleet, "quiet_checks"), patch.object(fleet, "claim", return_value=contextlib.nullcontext({})), patch.object(strict_fleet, "validate", return_value=changed), patch.object(strict_fleet, "disk_budget", return_value={"minimum_free_bytes": 0}), patch.object(strict_fleet.subprocess, "Popen", side_effect=AssertionError("spawned changed runtime")):
            with self.assertRaisesRegex(ValueError, "changed while waiting"):
                strict_fleet.run(self.m, {}, {}, {}, "changed")
        self.assertFalse((self.root / "attempts/changed").exists())



class StrictFleetEvidenceTest(unittest.TestCase):
    def fixture(self):
        # This exact file is also decoded by the Go strictRunResult test, so
        # invented Python field names cannot mask launcher/CLI contract drift.
        value = json.loads((Path(__file__).parent / "testdata/strict-result.json").read_text())
        m = {"root": "/dedicated/runtime", "identity": value["identity"], "supply": {"manifest_sha256": value["manifest_sha256"][2:]}}
        return m, value

    def test_one_counter_window_does_not_use_drain_or_sum_seven_counts(self):
        m, value = self.fixture()
        self.assertEqual(strict_fleet.strict_evidence(value, m), 8)
        for rate in (8 / 20, 7 * 4 / 20, float("nan"), float("inf")):
            value["canonical_committed_executed_tps"] = rate
            with self.assertRaisesRegex(ValueError, "counter/timing"):
                strict_fleet.strict_evidence(value, m)

    def test_owner_credit_epoch_identity_and_all_seven_final_heads_required(self):
        m, original = self.fixture()
        for field, value in (("PendingTransactions", 1), ("ReservedTransactions", 1), ("RetainedEncodedBytes", 1), ("ReservedEncodedBytes", 1), ("Epoch", [2] * 16), ("NewlyAdmitted", 9), ("CryptoVerified", 7), ("Reconciling", True)):
            evidence = json.loads(json.dumps(original))
            evidence["owner_final"][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                strict_fleet.strict_evidence(evidence, m)
        original["final"][6]["block_hash"] = "0x" + "44" * 32
        with self.assertRaisesRegex(ValueError, "seven canonical"):
            strict_fleet.strict_evidence(original, m)

    def test_failed_integrity_incomplete_source_or_bad_window_never_qualifies(self):
        m, original = self.fixture()
        for field, value in (("passed", False), ("error", "unknown final ACK"), ("full_source_integrity_verified", False), ("corpus_available_through_window", False), ("actual_window_seconds", 19.9), ("actual_window_seconds", 31), ("manifest_sha256", "0x" + "44" * 32)):
            evidence = json.loads(json.dumps(original))
            evidence[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                strict_fleet.strict_evidence(evidence, m)

    def test_disk_budget_includes_eight_full_bodies_and_receipt_reserve(self):
        corpus = {"descriptor": {"sender_count": 6000, "nonces_per_sender": 32000, "shard_count": 64, "frame_transactions": 500}}
        m = {"supply": {"root": "/supply"}}
        prefix = 192000000  # Full corpus is the bound under the 120-second work context.
        minimum = prefix * 4096 + (64 << 30)
        with patch.object(strict_fleet, "metadata", return_value=corpus):
            with self.assertRaisesRegex(ValueError, "disk budget"):
                strict_fleet.disk_budget(m, 6500000, minimum - 1)
            budget = strict_fleet.disk_budget(m, 6500000, minimum)
            self.assertEqual(budget["maximum_offered_prefix_transactions"], prefix)
            self.assertEqual(budget["estimated_chain_growth_bytes"], prefix * 4096)
            for invalid in (0, True, 20000001):
                with self.assertRaises(ValueError):
                    strict_fleet.disk_budget(m, invalid, minimum)

    def test_invalid_attempt_or_existing_marker_never_claims_or_starts_work(self):
        m = {"root": "/dedicated/runtime"}
        with patch.object(fleet, "quiet_checks", side_effect=AssertionError("claimed hardware")), patch.object(strict_fleet.os.path, "lexists", return_value=True):
            for name in ("../escape", "existing"):
                with self.assertRaises(ValueError):
                    strict_fleet.run(m, {}, {}, {}, name)


if __name__ == "__main__":
    unittest.main()
