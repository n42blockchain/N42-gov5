import hashlib
import json
import os
import http.server
import socketserver
import threading
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

    def status_server(self, code=200):
        identity = {"version": 1, "namespace": "test", "chain_id": "4205001", "genesis_hash": "0x" + "11" * 32}
        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass
            def do_POST(self):
                request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                # A real HTTP exchange over a real Unix socket, with the same
                # authority requirement as Go net/http. The old path Host fails.
                good = self.headers.get("Host") == "validator" and request == {"identity": identity}
                result = code if good else 400
                self.send_response(result)
                self.end_headers()
                self.wfile.write(json.dumps({"identity": identity} if result == 200 else {"error": "invalid authority"}).encode())
        path = str(self.base / "status.sock")
        server = socketserver.UnixStreamServer(path, Handler)
        thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
        thread.start()
        def stop():
            server.shutdown()
            server.server_close()
            thread.join(timeout=1)
        self.addCleanup(stop)
        return path, identity

    def test_status_real_unix_http_uses_valid_authority(self):
        path, identity = self.status_server()
        self.assertEqual(fleet.status(path, identity), {"identity": identity})

    def test_status_retains_http_failure_reason(self):
        path, identity = self.status_server(400)
        with self.assertRaisesRegex(ValueError, "HTTP 400.*invalid authority"):
            fleet.status(path, identity)

    def test_attempt_logs_are_private_exclusive_and_confined(self):
        for invalid in ("", "../escape", "/absolute", "a/b", "A", "a" * 33, ".hidden"):
            with self.assertRaises(ValueError):
                fleet.attempt_logs(self.root, invalid)
        self.assertFalse((self.root / "attempts").exists())
        path = fleet.attempt_logs(self.root, "retry-host")
        self.assertEqual(path, self.root / "attempts/retry-host")
        self.assertEqual(path.stat().st_mode & 0o777, 0o700)
        self.write(path / "prior.json", {"preserve": True})
        with self.assertRaises(FileExistsError):
            fleet.attempt_logs(self.root, "retry-host")
        self.assertEqual(fleet.load(path / "prior.json"), {"preserve": True})
        (self.root / "attempts/alias").symlink_to(self.base, target_is_directory=True)
        with self.assertRaises(FileExistsError):
            fleet.attempt_logs(self.root, "alias")
        with self.assertRaisesRegex(ValueError, "cannot initialize"):
            fleet.run({"root": str(self.root)}, None, None, 5, True, attempt="retry")

    def test_attempt_parent_alias_and_permissions_are_rejected(self):
        parent = self.root / "attempts"
        parent.symlink_to(self.base, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "aliases"):
            fleet.attempt_logs(self.root, "retry")
        parent.unlink()
        parent.mkdir(mode=0o755)
        with self.assertRaisesRegex(ValueError, "private"):
            fleet.attempt_logs(self.root, "retry")

    def test_smoke_result_archive_never_overwrites(self):
        source = self.root / "smoke-result.json"
        destination = fleet.attempt_logs(self.root, "retry") / "smoke-result.json"
        self.write(source, {"unique": 8})
        original = source.stat()
        fleet.archive_smoke(source, destination)
        self.assertFalse(source.exists())
        self.assertEqual(destination.stat().st_ino, original.st_ino)
        self.assertEqual(fleet.load(destination), {"unique": 8})
        self.write(source, {"unique": 9})
        with self.assertRaises(FileExistsError):
            fleet.archive_smoke(source, destination)
        self.assertEqual(fleet.load(source), {"unique": 9})
        self.assertEqual(fleet.load(destination), {"unique": 8})

    def test_restart_mode_rejects_invalid_combinations_before_claim(self):
        for initialize, smoke, attempt in ((True, None, "restart"), (False, {}, "restart"), (False, None, None)):
            with self.subTest(initialize=initialize, smoke=smoke, attempt=attempt), patch.object(fleet, "quiet_checks", side_effect=AssertionError("claimed hardware")):
                with self.assertRaisesRegex(ValueError, "restart requires"):
                    fleet.run({"root": str(self.root)}, {}, {}, 5, initialize, smoke, attempt, self.root / "baseline.json")

    def test_restart_baseline_requires_same_runtime_binaries_and_nonzero_evidence(self):
        manifest = {"root": str(self.root), "identity": {"test": "identity"}, "genesis_sha256": "11" * 32}
        node, owner = {"path": "/fixed/n42", "sha256": "22" * 32}, {"path": "/fixed/owner", "sha256": "33" * 32}
        directory = fleet.attempt_logs(self.root, "baseline")
        path = directory / "functional-result.json"
        status = {"identity": manifest["identity"], "number": 98, "block_hash": "0x" + "44" * 32, "committed_executed_transactions": 8}
        baseline = {"kind": "functional-check-not-tps", "identity": manifest["identity"], "genesis_sha256": manifest["genesis_sha256"],
                    "final": [status] * 7, "binaries": {"node": node, "owner": owner}, "log_directory": str(directory)}
        self.write(path, baseline)
        self.assertEqual(fleet.restart_baseline(manifest, node, owner, False, None, "restart", path), baseline)
        with self.assertRaisesRegex(ValueError, "binary paths/SHAs"):
            fleet.restart_baseline(manifest, dict(node, sha256="changed"), owner, False, None, "restart", path)
        for status in baseline["final"]:
            status["committed_executed_transactions"] = 0
        self.write(path, baseline)
        with self.assertRaisesRegex(ValueError, "nonzero"):
            fleet.restart_baseline(manifest, node, owner, False, None, "restart", path)

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

    def claim(self, owner, lock, blockers):
        return fleet.claim(owner, lock, blockers, standard_paths=(self.base / ".box-claim-gov5", self.base / ".mirror-gov5"), wait=lambda seconds: None)

    def test_atomic_claim_rejects_competing_owner_and_only_removes_own_record(self):
        claim = self.base / ".box-claim-test"
        lock = self.base / ".resource.lock"
        quiet = lambda: ([], {"load1": 0})
        with self.claim(claim, lock, quiet):
            record = fleet.load(claim / "owner.json")
            self.assertEqual(record["pid"], os.getpid())
            self.assertEqual((self.base / ".box-claim-gov5").read_text().strip(), record["claim_epoch"])
            self.assertEqual((self.base / ".mirror-gov5").read_text().strip(), record["claim_epoch"])
            with self.assertRaises(FileExistsError):
                with self.claim(claim, lock, quiet):
                    self.fail("competing launcher entered")
        self.assertFalse(claim.exists())
        self.assertFalse((self.base / ".box-claim-gov5").exists())
        self.assertFalse((self.base / ".mirror-gov5").exists())
        with self.assertRaisesRegex(ValueError, "hardware blocked"):
            with self.claim(claim, lock, lambda: (["other workload"], {})):
                self.fail("busy machine claimed")
        self.assertFalse(claim.exists())
        with self.assertRaises(fleet.HardwareContention):
            with self.claim(claim, lock, quiet):
                record = fleet.load(claim / "owner.json")
                record["token"] = "other-owner"
                self.write(claim / "owner.json", record)
        self.assertEqual(fleet.load(claim / "owner.json")["token"], "other-owner")

    def test_standard_claim_collision_preserves_existing_epoch_and_cleans_partial(self):
        mirror = self.base / ".mirror-gov5"
        mirror.write_text("12345\n")
        with self.assertRaises(FileExistsError):
            with self.claim(self.base / ".owner", self.base / ".lock", lambda: ([], {})):
                self.fail("overwrote existing BOX claim")
        self.assertEqual(mirror.read_text(), "12345\n")
        self.assertFalse((self.base / ".box-claim-gov5").exists())
        self.assertFalse((self.base / ".owner").exists())

    def test_quiet_spacing_and_postclaim_contention_yield(self):
        calls, waits = [], []
        def quiet():
            calls.append(1)
            return [], {"load1": 0}
        self.assertEqual(len(fleet.quiet_checks(quiet, waits.append)), 3)
        self.assertEqual(waits, [30, 30])
        self.assertEqual(len(calls), 3)
        calls.clear()
        waits.clear()
        owner = self.base / ".owner"
        mirrors = (self.base / ".claim-a", self.base / ".claim-b")
        def contended():
            calls.append(1)
            return ([] if len(calls) == 1 else ["active older or ambiguous claim"]), {}
        def wait(seconds):
            waits.append(seconds)
            if seconds == 60:
                self.assertFalse(owner.exists())
                self.assertFalse(any(p.exists() for p in mirrors))
        with self.assertRaisesRegex(ValueError, "contention"):
            with fleet.claim(owner, self.base / ".lock", contended, standard_paths=mirrors, wait=wait):
                self.fail("entered fleet after claim collision")
        self.assertTrue(20 <= waits[0] <= 50)
        self.assertEqual(waits[-1], 60)

    def test_replaced_standard_claim_is_never_removed(self):
        original = self.base / ".box-claim-gov5"
        with self.assertRaises(fleet.HardwareContention):
            with self.claim(self.base / ".owner", self.base / ".lock", lambda: ([], {})):
                original.unlink()
                original.write_text("foreign-owner\n")
        self.assertEqual(original.read_text(), "foreign-owner\n")

    def test_claim_cleans_only_owned_empty_directory_after_exception(self):
        claim = self.base / ".box-claim-test"
        with self.assertRaises(RuntimeError):
            with self.claim(claim, self.base / ".lock", lambda: ([], {})):
                raise RuntimeError("child failed")
        self.assertFalse(claim.exists())
        with self.assertRaisesRegex(ValueError, "duplicate"):
            fleet.pairs_unique([("identity", 1), ("identity", 2)])

class ClaimOwnershipTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="g5-claim-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.owner = self.root / ".owner"
        self.paths = (self.root / ".box-claim-codex", self.root / ".mirror-codex", self.root / ".box-claim-gov5", self.root / ".mirror-gov5")

    def claim(self):
        return fleet.claim(self.owner, self.root / ".lock", lambda: ([], {}), standard_paths=self.paths, wait=lambda seconds: None)

    def test_default_claim_has_distinct_codex_identity_and_legacy_mirrors(self):
        self.assertEqual([p.name for p in fleet.STANDARD_CLAIMS], [".box-claim-codex", ".box-claim-codex", ".box-claim-gov5", ".box-claim-gov5"])

    def test_claim_overwrite_stops_owned_work_and_preserves_foreign_file(self):
        with self.assertRaises(fleet.HardwareContention):
            with self.claim():
                fleet.check_owned_claim()
                original = self.paths[2]
                replacement = self.root / "replacement"
                epoch = original.read_text()
                replacement.write_text(epoch)
                replacement.chmod(0o600)
                replacement.replace(original)
                with self.assertRaisesRegex(ValueError, "hardware contention.*identity changed"):
                    fleet.check_owned_claim()
        self.assertEqual(self.paths[2].read_text(), epoch)
        self.assertFalse(self.paths[0].exists())
        self.assertFalse(self.paths[1].exists())
        self.assertFalse(self.paths[3].exists())
        with self.assertRaisesRegex(ValueError, "no owned claim"):
            fleet.check_owned_claim()

    def test_runtime_process_guard_ignores_only_recorded_owned_pids(self):
        with self.claim():
            blockers = (["load1 >= 8", "MemAvailable < 90 GiB", "other workload: pid=100 comm=n42", "other workload: pid=200 comm=rustc"], {})
            with patch.object(preflight, "resource_blockers", return_value=blockers):
                with self.assertRaisesRegex(ValueError, "pid=200"):
                    fleet.check_owned_claim([100], force=True)
                fleet.check_owned_claim([100, 200], force=True)

    def test_runtime_guard_rejects_foreign_claim_before_any_process(self):
        with self.claim():
            for error in ("active claim: .box-claim-rust", "unreadable claim: .box-claim-rust", "unknown resource refusal"):
                with self.subTest(error=error), patch.object(preflight, "resource_blockers", return_value=([error], {})):
                    with self.assertRaisesRegex(ValueError, "hardware contention"):
                        fleet.check_owned_claim([], force=True)


    def test_runtime_contention_releases_only_own_files_and_yields_sixty_seconds(self):
        waits = []
        with self.assertRaises(fleet.HardwareContention):
            with fleet.claim(self.owner, self.root / ".lock", lambda: ([], {}), standard_paths=self.paths, wait=waits.append):
                with patch.object(preflight, "resource_blockers", return_value=(["active claim: .box-claim-rust"], {})):
                    fleet.check_owned_claim([], force=True)
        self.assertEqual(waits[-1], 60)
        self.assertFalse(self.owner.exists())
        self.assertFalse(any(path.exists() for path in self.paths))

    def test_status_checks_claim_after_success_and_failed_io(self):
        for failure in (None, OSError("socket not ready")):
            with self.subTest(failure=failure):
                with patch.object(fleet, "status", return_value={"number": 1}, side_effect=failure), patch.object(fleet, "check_owned_claim") as check:
                    if failure:
                        with self.assertRaises(OSError):
                            fleet.guarded_status("socket", {}, lambda: [123])
                    else:
                        self.assertEqual(fleet.guarded_status("socket", {}, lambda: [123]), {"number": 1})
                    check.assert_called_once_with([123], force=True)
        with patch.object(fleet, "status", return_value={}), patch.object(fleet, "check_owned_claim", side_effect=fleet.HardwareContention("lost claim")):
            with self.assertRaises(fleet.HardwareContention):
                fleet.guarded_status("socket", {}, lambda: [])


if __name__ == "__main__":
    unittest.main()
