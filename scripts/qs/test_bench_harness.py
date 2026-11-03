"""Exercise launcher parameter handling without starting nodes or using sockets."""
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPTS = Path(__file__).parent


class HarnessTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for name in ("bench-7node.sh", "bench-run.sh"):
            shutil.copyfile(SCRIPTS / name, self.root / name)
        (self.root / "qs-env.sh").write_text('''set -euo pipefail
QS_ROOT=$PWD
QS_NODE_ROOT=$PWD/node
QS_SEED=$PWD/missing-seed
QS_BIN=/bin/true
QS_TOOLS=$PWD
QS_CHAIN=private
qs_load_validators() { :; }
qs_place_keys() { :; }
qs_launch_node() { echo "node=$1 gossip=$N42_MAX_GOSSIP_MB $QS_EXTRA_ARGS"; }
qs_node_pid() { echo unwanted-mutation >> "$PWD/mutations"; return 1; }
''')
        self.env = {k: v for k, v in os.environ.items() if not k.startswith(("QS_", "N42_"))}
        for i in range(7):
            (self.root / f"node{i}" / "chaindata").mkdir(parents=True)

    def run_script(self, script, *args):
        return subprocess.run(["bash", str(self.root / script), *args], env=self.env,
                              text=True, capture_output=True)

    def test_existing_seven_nodes_do_not_require_seed(self):
        result = self.run_script("bench-7node.sh")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.count("gossip=8 "), 7)

    def test_flagship_gas_tier_scales_wire_budget(self):
        result = self.run_script("bench-7node.sh", "--gasceil", "3423000000")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.count("gossip=58 "), 7)
        self.assertEqual(result.stdout.count("--miner.gasceil 3423000000"), 7)

    def test_explicit_wire_budget_is_preserved(self):
        self.env["N42_MAX_GOSSIP_MB"] = "64"
        result = self.run_script("bench-7node.sh", "--gasceil", "3423000000")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.count("gossip=64 "), 7)

    def test_invalid_gas_tier_does_not_launch(self):
        result = self.run_script("bench-7node.sh", "--gasceil", "0")
        self.assertEqual(result.returncode, 2)
        self.assertNotIn("node=", result.stdout)

    def test_rpc_batch_above_server_limit_fails_before_mutation(self):
        result = self.run_script("bench-run.sh", "--rpcbatch", "500")
        self.assertEqual(result.returncode, 2)
        self.assertIn("200-tx limit", result.stderr)
        self.assertFalse((self.root / "mutations").exists())

    def test_ingest_invalid_port_range_fails_before_mutation(self):
        for port in ("1024", "65530", "034000", "-1", "abc"):
            for script in ("bench-run.sh", "bench-7node.sh"):
                with self.subTest(port=port, script=script):
                    result = self.run_script(script, "--ingest-base", port)
                    self.assertEqual(result.returncode, 2, result.stderr)
                    self.assertIn("invalid --ingest-base", result.stderr)
                    self.assertNotIn("node=", result.stdout)
                    self.assertFalse((self.root / "mutations").exists())

    def test_ingest_requires_batch_mode_before_mutation(self):
        result = self.run_script("bench-run.sh", "--ingest-base", "34000", "--rpcbatch", "1")
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn("binary ingest requires", result.stderr)
        self.assertFalse((self.root / "mutations").exists())

    def test_preflight_failure_does_not_touch_existing_fleet(self):
        for tool in ("txflood", "txpool-journal-reset"):
            (self.root / tool).symlink_to("/bin/true")
        (self.root / "bench-preflight.py").write_text("raise SystemExit(42)\n")
        result = self.run_script("bench-run.sh")
        self.assertEqual(result.returncode, 42, result.stderr)
        self.assertFalse((self.root / "mutations").exists())


if __name__ == "__main__":
    unittest.main()
