#!/usr/bin/env python3
"""Measure canonical-chain throughput and verify every fleet member.

Sample all time boundaries before enumerating blocks, so JSON decoding of large
blocks does not introduce gaps between windows. Never count missing RPC data as
zero transactions. Results describe canonical inclusion, not receipt success or
an independent execution of the state transition.
"""

import argparse
import json
import math
import re
import sys
import time
import urllib.request
from datetime import datetime, timezone
from pathlib import Path


class MeasurementError(Exception):
    pass


def quantity(value):
    if not isinstance(value, str) or not re.fullmatch(r"0x[0-9a-fA-F]+", value):
        raise MeasurementError(f"invalid RPC quantity: {value!r}")
    return int(value, 16)


def digest(value):
    if not isinstance(value, str) or not re.fullmatch(r"0x[0-9a-fA-F]{64}", value):
        raise MeasurementError(f"invalid RPC hash: {value!r}")
    return value.lower()


def rpc(url, method, params):
    body = json.dumps(dict(jsonrpc="2.0", id=1, method=method, params=params)).encode()
    request = urllib.request.Request(url, body, {"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            data = json.load(response)
    except (OSError, ValueError) as exc:
        raise MeasurementError(f"{url} {method}: {exc}") from exc
    if (not isinstance(data, dict) or data.get("jsonrpc") != "2.0"
            or data.get("id") != 1 or data.get("error") is not None
            or "result" not in data):
        raise MeasurementError(f"{url} {method}: invalid/error RPC response")
    return data["result"]


def block_summary(block, expected_number=None):
    if not isinstance(block, dict):
        raise MeasurementError(f"missing block at {expected_number}")
    number = quantity(block.get("number"))
    if expected_number is not None and number != expected_number:
        raise MeasurementError(f"requested block {expected_number}, received {number}")
    txs = block.get("transactions")
    if not isinstance(txs, list):
        raise MeasurementError(f"block {number}: missing transaction list")
    for tx in txs:
        digest(tx)
    used, limit = quantity(block.get("gasUsed")), quantity(block.get("gasLimit"))
    if limit == 0 or used > limit:
        raise MeasurementError(f"block {number}: invalid gas usage {used}/{limit}")
    return dict(number=number, hash=digest(block.get("hash")),
                parent_hash=digest(block.get("parentHash")),
                state_root=digest(block.get("stateRoot")),
                receipts_root=digest(block.get("receiptsRoot")),
                transactions_root=digest(block.get("transactionsRoot")),
                transactions=len(txs), gas_used=used, gas_limit=limit)


def get_block(call, url, number):
    return block_summary(call(url, "eth_getBlockByNumber", [hex(number), False]), number)


def sample_boundary(call, url, now):
    # Only the block-number RPC runs in the timed interval. Hash/root validation
    # is performed afterwards against the canonical chain at these heights.
    start = now()
    number = quantity(call(url, "eth_blockNumber", []))
    end = now()
    return dict(number=number, time=(start + end) / 2, rpc_seconds=end - start)


def audit_window(call, url, start, end, index, expected_gas_limit=0):
    elapsed = end["time"] - start["time"]
    if elapsed <= 0 or end["number"] <= start["number"]:
        raise MeasurementError(f"window {index}: chain stalled or regressed")
    anchor = get_block(call, url, start["number"])
    previous = anchor
    records = []
    for number in range(start["number"] + 1, end["number"] + 1):
        block = get_block(call, url, number)
        if block["parent_hash"] != previous["hash"]:
            raise MeasurementError(f"block {number}: canonical parent changed")
        if expected_gas_limit and block["gas_limit"] != expected_gas_limit:
            raise MeasurementError(f"block {number}: unexpected gas limit {block['gas_limit']}")
        records.append(block)
        previous = block
    txs = sum(b["transactions"] for b in records)
    if txs == 0:
        raise MeasurementError(f"window {index}: no transactions included")
    used = sum(b["gas_used"] for b in records)
    limit = sum(b["gas_limit"] for b in records)
    return dict(window=index, start=anchor, end=previous, elapsed_seconds=elapsed,
                boundary_rpc_seconds=[start["rpc_seconds"], end["rpc_seconds"]],
                blocks=len(records), transactions=txs, tps=txs / elapsed,
                occupancy_percent=100 * used / limit,
                block_time_seconds=elapsed / len(records),
                full_blocks=sum(b["gas_used"] * 100 >= b["gas_limit"] * 95 for b in records),
                block_records=records)


def verify_fleet(call, urls, anchor, timeout, now=time.monotonic, sleep=time.sleep):
    deadline = now() + timeout
    observations = []
    for url in urls:
        head = quantity(call(url, "eth_blockNumber", []))
        while head < anchor["number"]:
            if now() >= deadline:
                raise MeasurementError(f"{url}: lagging at {head}, need {anchor['number']}")
            sleep(min(1, max(0, deadline - now())))
            head = quantity(call(url, "eth_blockNumber", []))
        actual = get_block(call, url, anchor["number"])
        for field in ("hash", "state_root", "receipts_root", "transactions_root", "transactions"):
            if actual[field] != anchor[field]:
                raise MeasurementError(f"{url}: {field} mismatch at {anchor['number']}")
        observations.append(dict(url=url, observed_head=head, block=actual))
    return observations


def positive_int(value):
    result = int(value)
    if result <= 0:
        raise argparse.ArgumentTypeError("must be positive")
    return result


def positive_float(value):
    result = float(value)
    if not math.isfinite(result) or result <= 0:
        raise argparse.ArgumentTypeError("must be finite and positive")
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--windows", type=positive_int, default=3)
    parser.add_argument("--window-sec", type=positive_float, default=60)
    parser.add_argument("--port", type=positive_int, default=20012)
    parser.add_argument("--nodes", type=positive_int, default=7)
    parser.add_argument("--urls", help="comma-separated RPC URLs; overrides port/nodes")
    parser.add_argument("--convergence-sec", type=positive_float, default=60)
    parser.add_argument("--expected-gas-limit", type=positive_int)
    parser.add_argument("--min-tps", type=positive_float, help="required in EVERY window")
    parser.add_argument("--json-out", type=Path)
    args = parser.parse_args()
    urls = (args.urls.split(",") if args.urls else
            [f"http://127.0.0.1:{args.port + i}" for i in range(args.nodes)])
    if len(set(urls)) != len(urls):
        parser.error("RPC URLs must be distinct")
    report = dict(schema_version=1, status="failed", started_at=datetime.now(timezone.utc).isoformat(),
                  urls=urls, requested_window_seconds=args.window_sec,
                  min_tps=args.min_tps, expected_gas_limit=args.expected_gas_limit, windows=[])
    try:
        chain_ids = [quantity(rpc(url, "eth_chainId", [])) for url in urls]
        if len(set(chain_ids)) != 1:
            raise MeasurementError("fleet chain IDs differ")
        report["chain_id"] = chain_ids[0]
        boundaries = [sample_boundary(rpc, urls[0], time.monotonic)]
        for _ in range(args.windows):
            time.sleep(max(0, boundaries[-1]["time"] + args.window_sec - time.monotonic()))
            boundaries.append(sample_boundary(rpc, urls[0], time.monotonic))
        report["boundaries"] = boundaries
        for i in range(args.windows):
            window = audit_window(rpc, urls[0], boundaries[i], boundaries[i + 1], i + 1,
                                  args.expected_gas_limit)
            if i and window["start"] != report["windows"][-1]["end"]:
                raise MeasurementError("canonical chain changed between windows")
            report["windows"].append(window)
            window["fleet"] = verify_fleet(rpc, urls, window["end"], args.convergence_sec)
            print(f"win{i + 1}: blocks={window['blocks']} txs={window['transactions']} "
                  f"TPS={window['tps']:.0f} occupancy={window['occupancy_percent']:.1f}% "
                  f"blockTime={window['block_time_seconds']:.3f}s "
                  f"full(>=95%)={window['full_blocks']} verified_nodes={len(urls)}", flush=True)
        if args.min_tps and any(w["tps"] < args.min_tps for w in report["windows"]):
            raise MeasurementError(f"at least one window below required {args.min_tps} TPS")
        report["status"] = "passed"
    except (MeasurementError, OSError, ValueError, KeyboardInterrupt) as exc:
        report["error"] = str(exc) or "interrupted"
        print(f"measurement FAILED: {report['error']}", file=sys.stderr)
    finally:
        if args.json_out:
            args.json_out.write_text(json.dumps(report, indent=2) + "\n")
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
