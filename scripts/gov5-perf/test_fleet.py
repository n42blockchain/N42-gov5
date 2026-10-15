import hashlib
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import fleet
import preflight


class FleetTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="g5-")
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.root = self.base / "r"
        self.root.mkdir(mode=0o700)
        self.env = patch.dict(os.environ, {}, clear=True)
        self.env.start()
        self.addCleanup(self.env.stop)

    def write(self, path, value):
        path.write_text(json.dumps(value))
        path.chmod(0o600)

    def fixture(self):
        r = self.root
        (r / "e").mkdir(mode=0o700)
        (r / "load").mkdir(mode=0o700)
        self.write(r / "load/keys.json", ["01" * 32])
        g = json.loads(Path(__file__).with_name("isolated-chain.json").read_text())
        validators = []
        for i in range(7):
            d = r / f"v{i}"
            d.mkdir(mode=0o700)
            (d / "keystore").mkdir(mode=0o700)
            address = "0x" + f"{i + 16:040x}"
            g["config"]["hotstuff"]["validators"].append({"address": address, "blsKey": "public-fixture"})
            v = {"index": i, "address": address, "peer_id": "test" + str(i), "data_dir": str(d), "config": str(d / "node.json"), "api_socket": str(d / "validator-rpc/validator.sock"), "tcp_port": 25000 + i}
            validators.append(v)
            self.write(d / "p2p.key", "not-used-by-readonly-validation")
            self.write(d / "keystore" / ("bls_" + address + ".key"), "private-fixture")
        m = {"schema_version": 1, "status": "provisioned-not-initialized", "root": str(r), "genesis": str(r / "genesis.json"), "identity": {"version": 1, "namespace": "gov5-performance-5m-v1", "chain_id": "4205001", "genesis_hash": "0x" + "11" * 32}, "owner_dir": str(r / "e"), "owner_socket": str(r / "e/execution-rpc/execution.sock"), "owner_parallel_evm": True, "validators": validators, "load_keys": str(r / "load/keys.json"), "transactions": "ethereum-secp256k1", "gas_price": "1"}
        self.write(r / "genesis.json", g)
        m["genesis_sha256"] = hashlib.sha256((r / "genesis.json").read_bytes()).hexdigest()
        for v in validators:
            d = Path(v["data_dir"])
            self.write(d / "node.json", {
                "node": {"data_dir": str(d), "chain": "private", "profile": "n42", "performance_genesis": m["genesis"], "performance_role": "validator", "performance_execution_socket": m["owner_socket"], "miner": True, "http": False, "ws": False, "auth_rpc": False, "ipc_path": ""},
                "chain": None,
                "miner": {"etherbase": v["address"], "blockintervalms": 2000},
                "p2p": {"private_key": str(d / "p2p.key"), "data_dir": str(d), "local_ip": "127.0.0.1", "host_address": "127.0.0.1", "tcp_port": v["tcp_port"], "udp_port": v["tcp_port"], "max_peers": 16, "static_peers": [f"/ip4/127.0.0.1/tcp/{w['tcp_port']}/p2p/{w['peer_id']}" for w in validators if w["index"] != v["index"]], "no_discovery": True, "quic_enabled": False, "bootstrap_node_addr": [], "discv5_bootstrap_addr": [], "relay_node_addr": "", "enable_upnp": False},
            })
        self.write(r / "run.json", m)
        return m

    def validate(self):
        with patch.object(preflight, "occupied_ports", return_value=set()):
            return fleet.validate(self.root / "run.json", self.base)

    def test_validation_is_readonly_and_refuses_genesis_or_public_rpc_changes(self):
        m = self.fixture()
        before = {p: p.read_bytes() for p in self.root.rglob("*") if p.is_file()}
        self.assertEqual(self.validate(), m)
        self.assertEqual(before, {p: p.read_bytes() for p in self.root.rglob("*") if p.is_file()})
        config = self.root / "v0/node.json"
        original = config.read_text()
        c = json.loads(original)
        c["node"]["http"] = True
        self.write(config, c)
        with self.assertRaisesRegex(ValueError, "configuration differs"):
            self.validate()
        config.write_text(original)
        with (self.root / "genesis.json").open("a") as out:
            out.write(" ")
        with self.assertRaisesRegex(ValueError, "SHA differs"):
            self.validate()

    def test_rejects_alias_permissions_unknown_fields_and_occupied_port(self):
        m = self.fixture()
        path = self.root / "v0/p2p.key"
        path.chmod(0o644)
        with self.assertRaisesRegex(ValueError, "private"):
            self.validate()
        path.chmod(0o600)
        saved = path.with_suffix(".saved")
        path.rename(saved)
        path.symlink_to(saved)
        with self.assertRaisesRegex(ValueError, "aliases"):
            self.validate()
        path.unlink()
        saved.rename(path)
        with patch.object(preflight, "occupied_ports", return_value={25003}):
            with self.assertRaisesRegex(ValueError, "occupied"):
                fleet.validate(self.root / "run.json", self.base)
        m["owner_capability"] = "must-never-be-provisioned-to-validators"
        self.write(self.root / "run.json", m)
        with self.assertRaisesRegex(ValueError, "manifest field"):
            self.validate()

    def test_atomic_claim_rejects_competing_owner_and_only_removes_own_record(self):
        claim = self.base / ".box-claim-test"
        lock = self.base / ".resource.lock"
        quiet = lambda: ([], {"load1": 0})
        with fleet.claim(claim, lock, quiet):
            record = fleet.load(claim / "owner.json")
            self.assertEqual(record["pid"], os.getpid())
            with self.assertRaises(FileExistsError):
                with fleet.claim(claim, lock, quiet):
                    self.fail("competing launcher entered")
        self.assertFalse(claim.exists())
        with self.assertRaisesRegex(ValueError, "hardware blocked"):
            with fleet.claim(claim, lock, lambda: (["other workload"], {})):
                self.fail("busy machine claimed")
        self.assertFalse(claim.exists())
        with fleet.claim(claim, lock, quiet):
            record = fleet.load(claim / "owner.json")
            record["token"] = "other-owner"
            self.write(claim / "owner.json", record)
        self.assertEqual(fleet.load(claim / "owner.json")["token"], "other-owner")

    def test_claim_cleans_only_owned_empty_directory_after_exception(self):
        claim = self.base / ".box-claim-test"
        with self.assertRaises(RuntimeError):
            with fleet.claim(claim, self.base / ".lock", lambda: ([], {})):
                raise RuntimeError("child failed")
        self.assertFalse(claim.exists())
        with self.assertRaisesRegex(ValueError, "duplicate"):
            fleet.pairs_unique([("identity", 1), ("identity", 2)])


if __name__ == "__main__":
    unittest.main()
