import copy
import hashlib
import json
from pathlib import Path
import unittest

import fleet
import queue_fixture
import test_fleet


class QueueFixtureTest(unittest.TestCase):
    def setUp(self):
        self.helper = test_fleet.FleetTest()
        self.helper.setUp()
        self.addCleanup(self.helper.doCleanups)
        self.m = self.helper.fixture()
        self.root = self.helper.root
        self.write = self.helper.write
        directory = self.root / "load/queue"
        directory.mkdir(mode=0o700)
        seed = b"x" * 32
        (self.root / "load/queue-seed").write_bytes(seed)
        (self.root / "load/queue-seed").chmod(0o600)
        d = {"version": 1, "identity": self.m["identity"], "genesis_file_sha256": "0x" + self.m["genesis_sha256"], "codec": "ethereum-signed-rlp-eip155-v1", "signature": "secp256k1-eip155-v1", "key_derivation": "gov5-supply-seed-hmac-sha256-v1", "seed_commitment": "0x" + hashlib.sha256(seed).hexdigest(), "sender_count": 4, "nonce_start": 0, "nonces_per_sender": 2, "recipient_rule": "gov5-supply-hotset-keccak-v1", "recipient_count": 4, "shard_count": 4, "frame_transactions": 2}
        shards = []
        # These bytes exercise read-only launcher validation, not signatures.
        # The Go fixture tests independently decode/recover every real tx.
        for index in range(4):
            raw = bytes([index]) * 100
            name = f"shard-{index:02d}.gcf"
            (directory / name).write_bytes(raw)
            (directory / name).chmod(0o600)
            shards.append({"index": index, "file": name, "transactions": 2, "frames": 1, "bytes": len(raw), "sha256": "0x" + hashlib.sha256(raw).hexdigest()})
        self.write(directory / "manifest.json", {"descriptor": d, "corpus_id": "0x" + "77" * 32, "shards": shards})
        (self.root / "queue-configs").mkdir(mode=0o700)
        for v in self.m["validators"]:
            cfg = fleet.load(Path(v["config"]))
            cfg["node"].update(performance_owner_queue=True, performance_queue_max_transactions=163000, performance_queue_max_encoded_bytes=32 << 20)
            self.write(self.root / "queue-configs" / f"v{v['index']}.json", cfg)

    def test_queue_validation_preserves_default_fixture_and_pins_files(self):
        before = {p: p.read_bytes() for p in self.root.rglob("*") if p.is_file()}
        self.helper.validate()
        binding = queue_fixture.validate(self.m)
        self.assertEqual(binding["manifest_hash"], "0x" + hashlib.sha256(Path(binding["manifest"]).read_bytes()).hexdigest())
        self.assertEqual(before, {p: p.read_bytes() for p in self.root.rglob("*") if p.is_file()})
        (self.root / "load/queue/shard-00.gcf").write_bytes(b"z" * 100)
        with self.assertRaisesRegex(ValueError, "size/hash"):
            queue_fixture.validate(self.m)

    def test_queue_validation_rejects_alias_and_config_drift(self):
        path = self.root / "queue-configs/v0.json"
        saved = path.with_suffix(".saved")
        path.rename(saved)
        path.symlink_to(saved)
        with self.assertRaisesRegex(ValueError, "aliases"):
            queue_fixture.validate(self.m)
        path.unlink()
        saved.rename(path)
        cfg = fleet.load(path)
        cfg["p2p"]["no_discovery"] = False
        self.write(path, cfg)
        with self.assertRaisesRegex(ValueError, "config differs"):
            queue_fixture.validate(self.m)

    def test_evidence_requires_owner_credit_release_not_only_local_zeros(self):
        binding = queue_fixture.validate(self.m)
        owner = {"Identity": self.m["identity"], "ManifestHash": binding["manifest_hash"], "Epoch": [1] * 16, "Reconciling": False, "PendingTransactions": 0, "ReservedTransactions": 0, "RetainedEncodedBytes": 0, "ReservedEncodedBytes": 0, "NewlyAdmitted": 8, "CryptoVerified": 8, "CanonicalHash": "0x" + "88" * 32, "CanonicalNumber": 3}
        status = {"identity": self.m["identity"], "committed_executed_transactions": 8, "queued_transactions": 0, "block_hash": owner["CanonicalHash"], "number": 3}
        result = {"kind": "owner-queue-functional-check-not-tps", "identity": self.m["identity"], "genesis_sha256": self.m["genesis_sha256"], "manifest_sha256": binding["manifest_hash"], "unique_committed_executed_transactions": 8, "owner_final": owner, "final": [copy.deepcopy(status) for _ in range(7)]}
        self.assertEqual(queue_fixture.evidence(result, self.m, binding), 8)
        retried = copy.deepcopy(result)
        retried["owner_final"]["CryptoVerified"] = 12
        self.assertEqual(queue_fixture.evidence(retried, self.m, binding), 8)
        incomplete = copy.deepcopy(result)
        incomplete["owner_final"]["CryptoVerified"] = 7
        with self.assertRaisesRegex(ValueError, "release"):
            queue_fixture.evidence(incomplete, self.m, binding)
        for key in ("PendingTransactions", "ReservedTransactions", "RetainedEncodedBytes", "ReservedEncodedBytes"):
            bad = copy.deepcopy(result)
            bad["owner_final"][key] = 1
            with self.assertRaisesRegex(ValueError, "release"):
                queue_fixture.evidence(bad, self.m, binding)
        bad = copy.deepcopy(result)
        bad["final"][6]["block_hash"] = "0x" + "99" * 32
        with self.assertRaisesRegex(ValueError, "converge"):
            queue_fixture.evidence(bad, self.m, binding)

    def test_mode_rejects_incompatible_options_before_claim(self):
        for smoke, restart in ((None, None), ({"path": "x"}, "old")):
            with self.assertRaisesRegex(ValueError, "requires its checker"):
                fleet.run(self.m, None, None, 5, False, smoke=smoke, restart=restart, owner_queue=True)


if __name__ == "__main__":
    unittest.main()
