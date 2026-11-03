import copy
import importlib.util
import io
import json
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("measure_tps", Path(__file__).with_name("measure-tps.py"))
measure = importlib.util.module_from_spec(spec)
spec.loader.exec_module(measure)


def h(number):
    return f"0x{number:064x}"


def block(number):
    return dict(number=hex(number), hash=h(number), parentHash=h(number - 1),
                stateRoot=h(100 + number), receiptsRoot=h(200 + number),
                transactionsRoot=h(300 + number), transactions=[h(400 + number)],
                gasUsed=hex(21000), gasLimit=hex(42000))


class MeasurementTests(unittest.TestCase):
    def setUp(self):
        self.blocks = {n: block(n) for n in range(1, 4)}
        self.start = dict(number=1, time=10.0, rpc_seconds=0.01)
        self.end = dict(number=3, time=12.0, rpc_seconds=0.02)

    def rpc(self, url, method, params):
        if method == "eth_blockNumber":
            return "0x3"
        return copy.deepcopy(self.blocks.get(int(params[0], 16)))

    def audit(self):
        return measure.audit_window(self.rpc, "node0", self.start, self.end, 1, 42000)

    def test_complete_window_and_seven_nodes(self):
        window = self.audit()
        self.assertEqual(window["tps"], 1)
        self.assertEqual(window["transactions"], 2)
        self.assertEqual(window["occupancy_percent"], 50)
        observations = measure.verify_fleet(self.rpc, [f"node{i}" for i in range(7)], window["end"], 1)
        self.assertEqual(len(observations), 7)

    def test_missing_block_fails_instead_of_counting_zero(self):
        del self.blocks[2]
        with self.assertRaisesRegex(measure.MeasurementError, "missing block at 2"):
            self.audit()

    def test_stalled_or_regressed_chain_fails(self):
        for height in (0, 1):
            self.end["number"] = height
            with self.assertRaisesRegex(measure.MeasurementError, "stalled or regressed"):
                self.audit()

    def test_parent_change_fails(self):
        self.blocks[3]["parentHash"] = h(99)
        with self.assertRaisesRegex(measure.MeasurementError, "parent changed"):
            self.audit()

    def test_gas_tier_mismatch_fails(self):
        self.blocks[2]["gasLimit"] = hex(480000000)
        with self.assertRaisesRegex(measure.MeasurementError, "unexpected gas limit"):
            self.audit()

    def test_empty_workload_fails(self):
        for b in self.blocks.values():
            b["transactions"] = []
        with self.assertRaisesRegex(measure.MeasurementError, "no transactions"):
            self.audit()

    def test_invalid_block_fields_fail(self):
        for field, value in (("transactions", None), ("transactions", [None]),
                             ("gasUsed", hex(50000)), ("gasLimit", "0x0"),
                             ("number", "0x9"), ("stateRoot", "garbage")):
            with self.subTest(field=field, value=value):
                self.blocks[2] = block(2)
                self.blocks[2][field] = value
                with self.assertRaises(measure.MeasurementError):
                    self.audit()

    def test_one_divergent_node_fails(self):
        anchor = self.audit()["end"]
        for field in ("hash", "stateRoot", "receiptsRoot", "transactionsRoot"):
            def diverge(url, method, params):
                result = self.rpc(url, method, params)
                if url == "node6" and method == "eth_getBlockByNumber":
                    result[field] = h(999)
                return result
            with self.subTest(field=field), self.assertRaisesRegex(measure.MeasurementError, "mismatch"):
                measure.verify_fleet(diverge, [f"node{i}" for i in range(7)], anchor, 1)

    def test_lag_timeout_fails(self):
        ticks = iter([0, 2])
        with self.assertRaisesRegex(measure.MeasurementError, "lagging"):
            measure.verify_fleet(lambda *_: "0x1", ["node6"], self.audit()["end"], 1,
                                 now=lambda: next(ticks), sleep=lambda _: None)

    def test_boundary_records_rpc_latency(self):
        ticks = iter([10, 12])
        boundary = measure.sample_boundary(self.rpc, "node0", lambda: next(ticks))
        self.assertEqual(boundary, dict(number=3, time=11, rpc_seconds=2))

    def test_rpc_error_or_missing_result_fails(self):
        for data in ({"jsonrpc": "2.0", "id": 1, "error": {"code": -1}},
                     {"jsonrpc": "2.0", "id": 1}, [], {"result": "0x3"}):
            with self.subTest(data=data):
                response = io.BytesIO(json.dumps(data).encode())
                with patch.object(measure.urllib.request, "urlopen", return_value=response):
                    with self.assertRaises(measure.MeasurementError):
                        measure.rpc("http://localhost", "eth_blockNumber", [])

    def test_cli_failure_retains_report_and_nonzero_exit(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "result.json"
            with patch("sys.argv", ["measure-tps.py", "--json-out", str(path)]), \
                    patch.object(measure, "rpc", side_effect=measure.MeasurementError("unavailable")), \
                    redirect_stderr(io.StringIO()):
                self.assertEqual(measure.main(), 1)
            result = json.loads(path.read_text())
            self.assertEqual(result["status"], "failed")
            self.assertEqual(result["error"], "unavailable")

    def test_cli_threshold_checks_every_window(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "result.json"
            boundaries = [dict(number=n, time=float(n), rpc_seconds=0) for n in (1, 2, 3)]
            with patch("sys.argv", ["measure-tps.py", "--windows", "2", "--min-tps", "2", "--json-out", str(path)]), \
                    patch.object(measure, "rpc", side_effect=self.rpc), \
                    patch.object(measure, "sample_boundary", side_effect=boundaries), \
                    patch.object(measure.time, "sleep"), redirect_stderr(io.StringIO()), redirect_stdout(io.StringIO()):
                # Supply the chain-ID query separately from the block fixture.
                original = self.rpc
                measure.rpc.side_effect = lambda u, m, p: "0x1" if m == "eth_chainId" else original(u, m, p)
                self.assertEqual(measure.main(), 1)
            result = json.loads(path.read_text())
            self.assertEqual(len(result["windows"]), 2)
            self.assertIn("below required", result["error"])
            self.assertEqual(len(result["windows"][1]["fleet"]), 7)


if __name__ == "__main__":
    unittest.main()
