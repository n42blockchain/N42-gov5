#!/usr/bin/env python3
"""One fresh, finite 32k transaction load; never resets data or bypasses BOX."""

import argparse
import http.client
import json
import math
import os
from pathlib import Path
import signal
import subprocess
import sys
import time

import fleet


def verify_result(manifest, evidence):
    if evidence.get("kind") != "finite-corpus-committed-executed-throughput-not-capacity" or evidence.get("passed") is not True or evidence.get("identity") != manifest["identity"] or evidence.get("genesis_sha256") != manifest["genesis_sha256"]:
        raise ValueError("load evidence identity or outcome differs")
    if evidence.get("corpus_transactions") != 32768 or evidence.get("senders") != 64 or evidence.get("nonces_per_sender") != 512 or evidence.get("batch_size") != 8192 or evidence.get("entry_validator") != 0:
        raise ValueError("finite workload parameters differ")
    initial, final = evidence["initial"], evidence["final"]
    if len(initial) != 7 or len(final) != 7:
        raise ValueError("seven initial/final observations required")
    for item in initial:
        if item["identity"] != manifest["identity"] or item["committed_executed_transactions"] != 0:
            raise ValueError("load must start from zero transactions")
    for item in final:
        if item["identity"] != manifest["identity"] or item["committed_executed_transactions"] != 32768 or item["queued_transactions"] != 0 or (item["number"], item["block_hash"]) != (final[0]["number"], final[0]["block_hash"]):
            raise ValueError("seven canonical committed/executed heads did not converge")
    elapsed = evidence["window_observed_seconds"]
    end = evidence["window_end"]
    if type(elapsed) not in (int, float) or not math.isfinite(elapsed) or not 20 <= elapsed <= 26 or end["identity"] != manifest["identity"] or not 0 <= end["committed_executed_transactions"] <= 32768:
        raise ValueError("bounded monotonic measurement window invalid")
    expected = end["committed_executed_transactions"] / elapsed
    if not math.isclose(evidence["committed_executed_tps"], expected, rel_tol=1e-10):
        raise ValueError("TPS is not the single canonical counter delta / observed time")


def run(manifest, binaries):
    root = Path(manifest["root"])
    fleet.quiet_checks()
    with fleet.claim() as resources:
        fleet.validate(root / "run.json")
        # This driver is deliberately fresh-only; a failed run is preserved and
        # requires explicit recovery analysis rather than reinitialization.
        for directory, metadata in [(manifest["owner_dir"], False)] + [(v["data_dir"], True) for v in manifest["validators"]]:
            expected = {"keystore", "p2p.key", "node.json"} if metadata else set()
            if {p.name for p in Path(directory).iterdir()} != expected:
                raise ValueError("fresh load driver refuses existing/partial chain data")
        logs = root / "load-run"
        logs.mkdir(mode=0o700)
        processes, streams, lifecycle = [], [], []
        def spawn(arguments, name, procs, role):
            stream = os.fdopen(os.open(logs / (name + ".log"), os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "wb")
            streams.append(stream)
            process = subprocess.Popen(arguments, cwd=root, env=dict(os.environ, GOMAXPROCS=str(procs)), stdin=subprocess.DEVNULL,
                                       stdout=stream, stderr=subprocess.STDOUT, start_new_session=True)
            processes.append(process)
            record = {"name": name, "role": role, "pid": process.pid, "executable": arguments[0], "gomaxprocs": procs, "started_unix": time.time()}
            try:
                record["affinity_logical_cpu_ids"] = sorted(os.sched_getaffinity(process.pid))
                record["affinity_sample"] = "observed"
            except ProcessLookupError:
                record["affinity_sample"] = "exited-before-sample"
            lifecycle.append((process, record))
            return process
        def live():
            if any(p.poll() is not None for p in processes):
                raise ValueError("owned runtime child exited; retain logs/data")
        started = time.time()
        try:
            for directory, metadata in [(manifest["owner_dir"], False)] + [(v["data_dir"], True) for v in manifest["validators"]]:
                arguments = [binaries["node"]["path"], "init", "--profile", "n42", "--chain", "private", "--data.dir", directory]
                if metadata:
                    arguments += ["--performance.metadata-only"]
                process = spawn(arguments + [manifest["genesis"]], "init-" + Path(directory).name, 2, "init")
                if process.wait(timeout=120) != 0:
                    raise ValueError("fresh initialization failed; partial data retained")
                processes.remove(process)
            spawn([binaries["owner"]["path"], "--genesis", manifest["genesis"], "--data-dir", manifest["owner_dir"], "--parallel-evm"], "owner", 36, "owner")
            deadline = time.monotonic() + 30
            while not Path(manifest["owner_socket"]).exists():
                live()
                if time.monotonic() >= deadline:
                    raise ValueError("owner readiness timeout")
                time.sleep(0.1)
            for v in manifest["validators"]:
                spawn([binaries["node"]["path"], "--config", v["config"]], "v" + str(v["index"]), 2, "validator")
            deadline = time.monotonic() + 30
            while True:
                live()
                try:
                    initial = [fleet.status(v["api_socket"], manifest["identity"]) for v in manifest["validators"]]
                    break
                except (OSError, ValueError, http.client.HTTPException) as error:
                    if time.monotonic() >= deadline:
                        raise ValueError("seven API readiness timeout: " + str(error)) from error
                    time.sleep(0.2)
            if any(item.get("committed_executed_transactions") != 0 or item.get("queued_transactions") != 0 for item in initial):
                raise ValueError("fresh load initial counters/queues must be zero")
            fleet.write_record(logs / "initial-status.json", initial)
            process = spawn([binaries["load"]["path"], "--manifest", str(root / "run.json")], "load", 2, "load-client")
            if process.wait(timeout=110) != 0:
                raise ValueError("finite load client failed; inspect load-result.json and retained logs")
            processes.remove(process)
            live()
            evidence = fleet.load(root / "load-result.json")
            verify_result(manifest, evidence)
            result = {"kind": "fresh-finite-corpus-load-not-capacity", "identity": manifest["identity"], "genesis_sha256": manifest["genesis_sha256"],
                      "binaries": binaries, "resources": resources, "started_unix": started, "load_finished_unix": time.time(),
                      "owner_gomaxprocs": 36, "validator_gomaxprocs": 2, "load_gomaxprocs": 2,
                      "budget_note": "GOMAXPROCS thread budgets, not exclusive physical cores; affinity recorded per child.",
                      "evidence": str(root / "load-result.json"), "committed_executed_tps": evidence["committed_executed_tps"],
                      "supply_exhausted": evidence["feed"]["supply_exhausted"], "limitation": evidence["limitation"]}
        finally:
            for process in reversed(processes):
                if process.poll() is None:
                    process.send_signal(signal.SIGTERM)
            deadline = time.monotonic() + 10
            for process in reversed(processes):
                try:
                    process.wait(timeout=max(0.01, deadline - time.monotonic()))
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
            for stream in streams:
                stream.close()
            for process, record in lifecycle:
                record.update(stopped_unix=time.time(), return_code=process.poll())
            fleet.write_record(logs / "process-lifecycle.json", {"processes": [record for _, record in lifecycle]})
        if any(record["return_code"] != 0 for _, record in lifecycle):
            raise ValueError("owned child shutdown was not clean; lifecycle retained")
        result["finished_unix"] = time.time()
        fleet.write_record(logs / "load-fleet-result.json", result)
        return result


def main():
    def interrupted(signum, frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupted)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("--node-binary", required=True)
    parser.add_argument("--owner-binary", required=True)
    parser.add_argument("--load-binary", required=True)
    args = parser.parse_args()
    try:
        manifest = fleet.validate(args.manifest)
        binaries = {"node": fleet.binary(args.node_binary), "owner": fleet.binary(args.owner_binary), "load": fleet.binary(args.load_binary)}
        result = run(manifest, binaries)
        print(json.dumps(result))
        return 0
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError, KeyboardInterrupt) as error:
        print(json.dumps({"kind": "fresh-finite-corpus-load-not-capacity", "error": str(error)}))
        return 1


if __name__ == "__main__":
    sys.exit(main())
