import copy
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import preflight


class PreflightTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.isolated = self.base / "isolated"
        self.profile = json.loads(Path(__file__).with_name("profile.json").read_text())
        self.profile["runtime_root"] = str(self.isolated / "runtime")
        self.profile_path = self.base / "profile.json"
        self.genesis = json.loads(Path(__file__).with_name("isolated-chain.json").read_text())
        self.env = patch.dict(os.environ, {}, clear=True)
        self.env.start()
        self.addCleanup(self.env.stop)

    def validate(self, profile=None, genesis=None):
        self.profile_path.write_text(json.dumps(self.profile if profile is None else profile))
        (self.base / "isolated-chain.json").write_text(json.dumps(self.genesis if genesis is None else genesis))
        return preflight.validate_profile(self.profile_path, self.isolated)

    def test_default_contract_has_seven_distinct_nodes_and_no_launch(self):
        result = self.validate()
        self.assertEqual(len(set(result["datadirs"])), 7)
        self.assertEqual(len(result["ports"]), 50)
        self.assertFalse(result["launch_supported"])

    def test_rejects_production_and_relative_roots(self):
        for root in ("/data/blockchain/qs-node0", "/home/n42/src/n42/N42-gov5/mainnet", "runtime", "/data/blockchain"):
            with self.subTest(root=root):
                p = copy.deepcopy(self.profile)
                p["runtime_root"] = root
                with self.assertRaises(ValueError):
                    self.validate(p)

    def test_rejects_root_and_node_symlink_escape(self):
        other = self.base / "other"
        other.mkdir()
        self.isolated.symlink_to(other, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "symlink"):
            self.validate()
        self.isolated.unlink()
        runtime = self.isolated / "runtime"
        runtime.mkdir(parents=True)
        (runtime / "node0").symlink_to(other, target_is_directory=True)
        with self.assertRaises(ValueError):
            self.validate()

    def test_rejects_existing_runtime_without_touching_it(self):
        runtime = self.isolated / "runtime"
        runtime.mkdir(parents=True)
        marker = runtime / "do-not-touch"
        marker.write_text("preserved")
        with self.assertRaises(ValueError):
            self.validate()
        self.assertEqual(marker.read_text(), "preserved")

    def test_rejects_production_identity_and_unimplemented_modes(self):
        for key, value in (("chain_id", 94), ("chain", "eth-mainnet"), ("node_profile", "gov5-perf"), ("status", "ready")):
            with self.subTest(key=key):
                p = copy.deepcopy(self.profile)
                p[key] = value
                with self.assertRaises(ValueError):
                    self.validate(p)
        g = copy.deepcopy(self.genesis)
        g["timestamp"] = 1678174066
        with self.assertRaisesRegex(ValueError, "identity"):
            self.validate(genesis=g)
        p = copy.deepcopy(self.profile)
        p["transactions"]["candidate_status"] = "implemented"
        with self.assertRaises(ValueError):
            self.validate(p)

    def test_rejects_protected_and_overlapping_ports_and_discovery(self):
        for port in (32000, 8545, 25200, 33000):
            p = copy.deepcopy(self.profile)
            p["ports"]["p2p_tcp"] = port
            with self.subTest(port=port), self.assertRaises(ValueError):
                self.validate(p)
        p = copy.deepcopy(self.profile)
        p["p2p"]["discovery"] = True
        with self.assertRaises(ValueError):
            self.validate(p)

    def test_inherited_chain_environment_is_not_silently_applied(self):
        os.environ["N42_FRAME_BLOCKS"] = "secret-placeholder"
        with self.assertRaisesRegex(ValueError, "N42_FRAME_BLOCKS") as error:
            self.validate()
        self.assertNotIn("secret-placeholder", str(error.exception))

    def proc(self):
        proc = self.base / "proc"
        proc.mkdir()
        (proc / "loadavg").write_text("0.1 0.1 0.1 1/1 1\n")
        (proc / "meminfo").write_text(f"MemAvailable: {100 * 1024 * 1024} kB\n")
        return proc

    def test_live_claim_malformed_claim_and_unclaimed_build_block(self):
        proc = self.proc()
        claim = self.base / ".box-claim-rust"
        for content in ("9990", "9000 9990", "future-garbage", "999999", "0"):
            claim.write_text(content)
            with self.subTest(content=content):
                blockers, _ = preflight.resource_blockers(proc, [self.base], now=10000)
                self.assertTrue(blockers)
        claim.write_text("1")
        blockers, _ = preflight.resource_blockers(proc, [self.base], now=10000)
        self.assertEqual(blockers, [])
        (proc / "42").mkdir()
        (proc / "42" / "comm").write_text("cargo\n")
        blockers, _ = preflight.resource_blockers(proc, [self.base], now=10000)
        self.assertEqual(blockers, ["other workload: pid=42 comm=cargo"])

    def test_low_memory_and_high_load_block(self):
        proc = self.proc()
        (proc / "loadavg").write_text("8.0 0.1 0.1 1/1 1\n")
        (proc / "meminfo").write_text("MemAvailable: 1000 kB\n")
        blockers, _ = preflight.resource_blockers(proc, [], now=10000)
        self.assertEqual(len(blockers), 2)

    def test_bare_claim_and_directory_claim_fail_closed(self):
        proc = self.proc()
        (self.base / ".box-claim").write_text("9990")
        (self.base / ".box-claim-directory").mkdir()
        blockers, _ = preflight.resource_blockers(proc, [self.base], now=10000)
        self.assertEqual(len(blockers), 2)

    def test_occupied_port_detection_includes_ipv6_and_udp(self):
        proc = self.proc()
        (proc / "net").mkdir()
        for table, port in (("tcp", 25000), ("tcp6", 25200), ("udp", 25100), ("udp6", 25101)):
            (proc / "net" / table).write_text(f"header\n0: 00000000:{port:04X} 00000000:0000 0A\n")
        self.assertEqual(preflight.occupied_ports(proc), {25000, 25200, 25100, 25101})


if __name__ == "__main__":
    unittest.main()
