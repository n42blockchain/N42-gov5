#!/usr/bin/env python3
"""Recover a retained schema-2 run without initialization or transaction dispatch."""
import argparse
import hashlib
import json
import os
import re
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import time

import fleet
import strict_fleet

EMPTY_CODE_HASH = "0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"


def baseline(path, m):
    path = Path(path)
    if path.name != "result.json" or not path.is_relative_to(Path(m["root"]) / "strict-attempts"):
        raise ValueError("failed baseline must belong to this retained runtime")
    raw = strict_fleet.read_private(path, 16 << 20)
    b = json.loads(raw, object_pairs_hook=fleet.pairs_unique)
    if b.get("kind") != "strict-owner-queue-canonical-observation-not-capacity-promotion" or b.get("passed") is not False or not b.get("error") or b.get("full_source_integrity_verified") is not True or b.get("identity") != m["identity"] or b.get("manifest_sha256") != "0x" + m["supply"]["manifest_sha256"] or b.get("runtime_manifest") != str(Path(m["root"]) / "run.json") or len(b.get("final", [])) != 7:
        raise ValueError("retained strict failure binding differs")
    if any(s["identity"] != m["identity"] or type(s["number"]) is not int or s["number"] < 1 or type(s["committed_executed_transactions"]) is not int or s["committed_executed_transactions"] < 1 for s in b["final"]):
        raise ValueError("nonzero retained canonical evidence required")
    return b, hashlib.sha256(raw).hexdigest()


def evidence(value, m, b, baseline_path, baseline_sha):
    if value.get("kind") != "strict-owner-queue-recovery-no-dispatch-not-tps" or value.get("passed") is not True or value.get("error") or type(value.get("dispatched_transactions")) is not int or value.get("dispatched_transactions") != 0 or value.get("identity") != m["identity"] or value.get("manifest_sha256") != "0x" + m["supply"]["manifest_sha256"] or value.get("runtime_manifest") != str(Path(m["root"]) / "run.json") or value.get("baseline_result") != str(baseline_path) or value.get("baseline_sha256") != "0x" + baseline_sha:
        raise ValueError("read-only recovery result binding differs")
    first, owner, final = value["owner_initial"], value["owner_final"], value["final"]
    epoch = first["Epoch"]
    if not isinstance(epoch, list) or len(epoch) != 16 or not any(epoch) or any(type(v) is not int or not 0 <= v <= 255 for v in epoch) or epoch == b["owner_final"]["Epoch"]:
        raise ValueError("new empty owner epoch required")
    for status in (first, owner):
        if status["Identity"] != m["identity"] or status["Epoch"] != epoch or status["ManifestHash"] != "0x" + m["supply"]["manifest_sha256"] or any(type(status[k]) is not int or status[k] != 0 for k in ("NewlyAdmitted", "CryptoVerified", "PendingTransactions", "ReservedTransactions", "RetainedEncodedBytes", "ReservedEncodedBytes")):
            raise ValueError("recovery contains transaction activity or a new binding")
    for current, previous in ((first, b["owner_final"]), (owner, first)):
        if current["CanonicalNumber"] < previous["CanonicalNumber"] or (current["CanonicalNumber"] == previous["CanonicalNumber"] and current["CanonicalHash"] != previous["CanonicalHash"]):
            raise ValueError("owner canonical evidence regressed")
    if owner["Reconciling"] or len(final) != 7:
        raise ValueError("recovery still reconciling")
    initial = value["initial"]
    if len(initial) != 7:
        raise ValueError("seven initial recovery observations required")
    for i, status in enumerate(initial):
        old = b["final"][i]
        if status["identity"] != m["identity"] or status["producer_running"] is not False or status["owner_queue"]["epoch"] != epoch or status["owner_queue"]["manifest_hash"] != owner["ManifestHash"] or status["number"] < old["number"] or status["committed_executed_transactions"] < old["committed_executed_transactions"] or (status["number"] == old["number"] and (status["block_hash"] != old["block_hash"] or status["committed_executed_transactions"] != old["committed_executed_transactions"])):
            raise ValueError("initial recovery evidence regressed or changed session")
    count = value["canonical_nonce_sum"]
    if type(count) is not int or count < 1:
        raise ValueError("nonzero canonical nonce total required")
    for i, status in enumerate(final):
        old = initial[i]
        if status["identity"] != m["identity"] or status["producer_running"] is not False or status["block_hash"] != owner["CanonicalHash"] or status["number"] != owner["CanonicalNumber"] or status["committed_executed_transactions"] != count or status["number"] < old["number"] or count < old["committed_executed_transactions"] or (status["number"] == old["number"] and (status["block_hash"] != old["block_hash"] or count != old["committed_executed_transactions"])):
            raise ValueError("recovery canonical evidence regressed or producers remain active")
        queue = status["owner_queue"]
        if queue["epoch"] != epoch or queue["manifest_hash"] != owner["ManifestHash"]:
            raise ValueError("validator recovery epoch differs")
    source = strict_fleet.metadata(Path(m["supply"]["root"]) / "manifest.json", 1 << 20)["descriptor"]
    alloc = strict_fleet.metadata(Path(m["genesis"]), 16 << 20)["alloc"]
    addresses = sorted(a.lower() for a in alloc)
    accounts = value["accounts"]
    if len(accounts) != source["sender_count"] or len(alloc) != source["sender_count"]:
        raise ValueError("complete public sender vector required")
    total = 0
    for i, account in enumerate(accounts):
        if set(account) != {"address", "exists", "nonce", "balance", "code_hash"} or account["address"] != addresses[i] or account["exists"] is not True or type(account["nonce"]) is not int or not 0 <= account["nonce"] <= source["nonces_per_sender"] or account["code_hash"] != EMPTY_CODE_HASH or not isinstance(account["balance"], str) or not account["balance"].isascii() or not account["balance"].isdigit():
            raise ValueError("invalid funded EOA nonce vector")
        balance = int(account["balance"])
        if str(balance) != account["balance"] or not 0 < balance < 1 << 256 or balance < (source["nonces_per_sender"] - account["nonce"]) * 21001:
            raise ValueError("recovery sender balance cannot fund remaining range")
        total += account["nonce"]
    encoded = json.dumps(accounts, separators=(",", ":")).encode()
    if total != count or value["account_vector_sha256"] != "0x" + hashlib.sha256(encoded).hexdigest():
        raise ValueError("canonical count/vector digest differs")
    return count


def run(m, failed_path, node, owner, audit, attempt):
    # Shared helper provides basename/ownership/O_EXCL validation within claim.
    root = Path(m["root"])
    if not isinstance(attempt, str) or re.fullmatch(r"[a-z][a-z0-9-]{0,31}", attempt) is None:
        raise ValueError("new lowercase recovery attempt basename required")
    if os.path.lexists(root / "attempts" / attempt) or os.path.lexists(root / "strict-attempts" / attempt):
        raise ValueError("existing recovery attempt is never retried")
    b, digest = baseline(failed_path, m)
    children, logs, records = [], [], []
    log_root = None
    deadline = time.monotonic() + 180
    reserve = 64 << 30

    def guard(force=False):
        fleet.check_owned_claim([p.pid for p in children if p.poll() is None], force=force)
        if time.monotonic() >= deadline or shutil.disk_usage(root).free < reserve:
            raise ValueError("bounded recovery turn/disk reserve exceeded")

    def spawn(args, name):
        guard(True)
        log = os.fdopen(os.open(log_root / (name + ".log"), os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "wb")
        logs.append(log)
        process = subprocess.Popen(args, cwd=root, env=dict(os.environ, GOMAXPROCS="2"), stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
        record = {"name": name, "pid": process.pid, "executable": args[0], "gomaxprocs": 2, "started_unix": time.time()}
        children.append(process)
        records.append((process, record))
        try:
            record["actual_cpu_affinity"] = sorted(os.sched_getaffinity(process.pid))
        except ProcessLookupError:
            record["affinity_sample"] = "exited-before-sample"
        return process

    def live():
        guard()
        if any(p.poll() is not None for p in children):
            raise ValueError("recovery child exited; retain same databases and logs")

    if shutil.disk_usage(root).free < reserve:
        raise ValueError("64GiB existing-data recovery disk reserve required")
    fleet.quiet_checks()
    with fleet.claim() as resources:
        deadline = time.monotonic() + 180
        if strict_fleet.validate(root / "run.json") != m or baseline(failed_path, m) != (b, digest):
            raise ValueError("retained runtime or failure changed while waiting")
        log_root = fleet.attempt_logs(root, attempt)
        result = {"kind": "strict-owner-queue-recovery-fleet-no-dispatch-not-tps", "identity": m["identity"], "baseline_result": str(failed_path), "baseline_sha256": digest, "binaries": {"node": node, "owner": owner, "audit": audit}, "resources": resources, "log_directory": str(log_root), "started_unix": time.time(), "no_initialization": True, "producer_disabled_configuration_copies": True, "dispatched_transactions": 0, "disk_reserve_bytes": reserve}
        try:
            sockets = [m["owner_socket"], str(root / "e/execution-rpc/ingress.sock")] + [v["api_socket"] for v in m["validators"]]
            if any(os.path.lexists(path) for path in sockets):
                raise ValueError("existing sockets are not removed by recovery")
            spawn([owner["path"], "--genesis", m["genesis"], "--data-dir", m["owner_dir"], "--parallel-evm", "--strict-ingress-manifest", str(Path(m["supply"]["root"]) / "manifest.json")], "owner")
            end = time.monotonic() + 30
            while not all(Path(path).exists() for path in sockets[:2]):
                live()
                if time.monotonic() >= end:
                    raise TimeoutError("recovery owner readiness timeout")
                time.sleep(.1)
            for validator in m["validators"]:
                config = strict_fleet.metadata(Path(validator["config"]))
                config["node"]["miner"] = False
                path = log_root / ("recovery-v" + str(validator["index"]) + ".json")
                fleet.write_record(path, config)
                spawn([node["path"], "--config", str(path)], "v" + str(validator["index"]))
            end = time.monotonic() + 30
            while not all(Path(path).exists() for path in sockets[2:]):
                live()
                if time.monotonic() >= end:
                    raise TimeoutError("recovery validator readiness timeout")
                time.sleep(.1)
            audit_process = spawn([audit["path"], "recover", "--root", m["supply"]["root"], "--runtime-manifest", str(root / "run.json"), "--attempt", attempt, "--baseline-result", str(failed_path)], "recovery-audit")
            while audit_process.poll() is None:
                guard()
                if any(p.poll() is not None for p in children if p is not audit_process):
                    raise ValueError("recovery runtime exited during audit")
                time.sleep(.1)
            guard(True)
            if audit_process.returncode != 0:
                raise ValueError("actual same-data recovery audit failed; preserve evidence")
            children.remove(audit_process)
            live()
            path = root / "strict-attempts" / attempt / "recovery.json"
            count = evidence(strict_fleet.metadata(path, 16 << 20), m, b, failed_path, digest)
            result.update(canonical_nonce_sum=count, recovery_result=str(path), finished_unix=time.time())
            guard(True)
            fleet.write_record(log_root / "recovery-fleet-observation.json", result)
        finally:
            for p in reversed(children):
                if p.poll() is None:
                    p.send_signal(signal.SIGTERM)
            end = time.monotonic() + 10
            forced = set()
            for p in reversed(children):
                try:
                    p.wait(timeout=max(.01, end - time.monotonic()))
                except subprocess.TimeoutExpired:
                    forced.add(p.pid)
                    p.kill()
                    p.wait(timeout=5)
            for log in logs:
                log.close()
            for p, record in records:
                record.update(stopped_unix=time.time(), return_code=p.poll(), forced_kill=p.pid in forced)
            clean = all(p.poll() == 0 and p.pid not in forced for p, _ in records)
            fleet.write_record(log_root / "process-lifecycle.json", {"kind": "owned-recovery-process-lifecycle", "cleanup_clean": clean, "processes": [r for _, r in records]})
            if not clean and sys.exc_info()[0] is None:
                raise ValueError("recovery cleanup did not exit cleanly; no success promotion")
    fleet.write_record(log_root / "recovery-fleet-result.json", result)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("--baseline-result", type=Path, required=True)
    parser.add_argument("--run", action="store_true")
    parser.add_argument("--attempt")
    parser.add_argument("--node-binary", type=Path)
    parser.add_argument("--owner-binary", type=Path)
    parser.add_argument("--audit-binary", type=Path)
    args = parser.parse_args()
    def interrupted(signum, frame):
        raise KeyboardInterrupt("signal " + str(signum))
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        m = strict_fleet.validate(args.manifest)
        baseline(args.baseline_result, m)
        if args.run:
            if not args.attempt or any(p is None for p in (args.node_binary, args.owner_binary, args.audit_binary)):
                raise ValueError("recovery run requires new attempt and explicit binaries")
            value = run(m, args.baseline_result, fleet.binary(args.node_binary), fleet.binary(args.owner_binary), fleet.binary(args.audit_binary), args.attempt)
            print(json.dumps({"result": str(Path(value["log_directory"]) / "recovery-fleet-result.json"), "kind": value["kind"]}))
            return 0
        if args.attempt or args.node_binary or args.owner_binary or args.audit_binary:
            raise ValueError("execution options require --run")
        print(json.dumps({"kind": "retained-recovery-validation-only", "identity": m["identity"]}))
        return 0
    except (OSError, ValueError, TypeError, KeyError, subprocess.SubprocessError) as error:
        print(json.dumps({"error": str(error)}))
        return 1

if __name__ == "__main__":
    sys.exit(main())
