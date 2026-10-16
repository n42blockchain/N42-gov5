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


if __name__ == "__main__":
    unittest.main()
