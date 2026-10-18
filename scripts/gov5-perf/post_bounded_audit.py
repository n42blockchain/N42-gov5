#!/usr/bin/env python3
"""Two nonproducing same-root reopens after a failed bounded drain.

No initialization, plan arming, frame dispatch, production or TPS measurement.
The first snapshot is new evidence, never a reconstructed production snapshot.
"""
import hashlib
import json
from pathlib import Path

import fleet
import bounded_fleet
import recovery_fleet
import resume_fleet
import strict_fleet

def failed_window_inputs(path, digest, failure_path, failure_sha, supplement_path, supplement_sha, document, historical, m):
    def pinned_json(path, digest):
        raw = strict_fleet.read_private(Path(path), 16 << 20)
        if hashlib.sha256(raw).hexdigest() != digest:
            raise ValueError("fixed failed-window evidence changed")
        return json.loads(raw, object_pairs_hook=fleet.pairs_unique)
    value = pinned_json(path, digest)
    accepted = pinned_json(failure_path, failure_sha)
    supplement = pinned_json(supplement_path, supplement_sha)
    if value.get("kind") != "gov5-bounded-resume-20s-window-and-drain-candidate-not-capacity-v2" or value.get("passed") is not False or value.get("error") not in ("bounded acknowledged prefix did not drain before deadline", 'Post "http://unix/v1/status": context deadline exceeded') or value["identity"] != m["identity"] or value["plan_id"] != document["plan_id"]:
        raise ValueError("actual failed bounded drain required")
    if accepted.get("failure_verified") is not True or accepted.get("functional_gate_passed") is not False or accepted.get("capacity_5m_proven") is not False or accepted["candidate_sha256"] != digest or accepted.get("all_owned_processes_gone") is not True or accepted.get("owned_claims_released") is not True or accepted.get("physical_reopen_performed") is not False:
        raise ValueError("independent failed-window acceptance required")
    if supplement.get("kind") != "gov5-bounded-window-failure-reference-correction-v2" or supplement["failure_acceptance_sha256"] != failure_sha or supplement["candidate_sha256"] != digest or supplement["last_drain_reference"] != value["reference_final"]:
        raise ValueError("later drain reference supplement differs")
    p = document["plan"]
    epoch = value["owner_initial"]["Epoch"]
    bounded_fleet.source_ready_evidence({"kind":"gov5-bounded-source-ready-before-production-v2", "plan_id":document["plan_id"], "full_source_integrity_verified":True, "dispatched_transactions":0, "owner":value["owner_initial"], "epoch":epoch}, document, m)
    totals = bounded_fleet.journal_evidence(Path(path).parent, value, document)
    if accepted["journal_totals"] != totals or totals["new"] != value["owner_final"]["NewlyAdmitted"]:
        raise ValueError("known journal/owner new totals differ")
    reference = value["reference_final"]
    bounded_fleet.validator_binding(reference, m, epoch, p["ManifestSHA256"])
    end = value["window_end"]["single_validator_canonical"]
    if not bounded_fleet.canonical_nonregressing(reference, end):
        raise ValueError("late drain reference regressed")
    low, high = reference["committed_executed_transactions"], p["CanonicalNonceSum"] + totals["new"]
    if low > high or supplement["recovery_canonical_count_lower_bound"] != low or supplement["recovery_canonical_count_upper_bound"] != high:
        raise ValueError("recovery bounds differ from known ACKs/reference")
    if len(value["sender_new_transactions"]) != len(p["Senders"]):
        raise ValueError("complete logical sender admission vector required")
    return value, totals


def first_audit_evidence(value, document, historical, m, failed, failed_path, failed_sha, candidate):
    count = recovery_fleet.evidence(value, m, failed, failed_path, failed_sha)
    p = document["plan"]
    reference = candidate["reference_final"]
    if not reference["committed_executed_transactions"] <= count <= p["CanonicalNonceSum"] + sum(candidate["sender_new_transactions"]) or value["owner_initial"]["Epoch"] in (candidate["owner_initial"]["Epoch"], historical["owner_final"]["Epoch"]):
        raise ValueError("post-bounded snapshot outside known canonical/ACK bounds or reused epoch")
    owner, roots = value["owner_final"], value.get("canonical_roots")
    for status in (value["owner_initial"], owner):
        if status["CanonicalNumber"] < reference["number"] or (status["CanonicalNumber"] == reference["number"] and status["CanonicalHash"] != reference["block_hash"]):
            raise ValueError("post-bounded canonical head regressed below late drain reference")
    if roots is None or roots["final_read"] is not True or roots["account_batches"] != (len(p["Senders"]) + 999) // 1000 or roots["canonical_hash"] != owner["CanonicalHash"] or roots["canonical_number"] != owner["CanonicalNumber"]:
        raise ValueError("post-bounded complete canonical root tuple missing")
    pair = roots["roots"]
    if not strict_fleet.valid_hash(pair["root"]) or pair["root"] == "0x" + "00" * 32 or pair["applied_root"] != pair["root"]:
        raise ValueError("post-bounded header and loaded QMDB roots differ")
    old = {a["address"]: a for a in historical["accounts"]}
    increments = {s["Address"]: candidate["sender_new_transactions"][s["Index"]] for s in p["Senders"]}
    actual = []
    for account in value["accounts"]:
        previous = old[account["address"]]
        delta = account["nonce"] - previous["nonce"]
        if not 0 <= delta <= increments[account["address"]] or account["balance"] != str(int(previous["balance"]) - 21001 * delta) or account["code_hash"] != previous["code_hash"] or account["exists"] != previous["exists"]:
            raise ValueError("post-bounded sender state outside acknowledged suffix bound")
        actual.append(delta)
    if sum(actual) != count - p["CanonicalNonceSum"]:
        raise ValueError("post-bounded account deltas differ from canonical count")
    return count


def second_audit_evidence(value, first, document, historical, m, failed, failed_path, failed_sha, candidate):
    count = first_audit_evidence(value, document, historical, m, failed, failed_path, failed_sha, candidate)
    if value["owner_initial"]["Epoch"] == first["owner_initial"]["Epoch"] or value["accounts"] != first["accounts"] or value["account_vector_sha256"] != first["account_vector_sha256"] or value["canonical_roots"] != first["canonical_roots"] or any(value["owner_final"][k] != first["owner_final"][k] for k in ("CanonicalHash", "CanonicalNumber")):
        raise ValueError("second physical reopen changed complete head/root/vector or reused epoch")
    return count


def _live_group(pgid):
    import os
    live = []
    for entry in Path("/proc").iterdir():
        if not entry.name.isdigit():
            continue
        try:
            state = (entry / "stat").read_text().rsplit(")", 1)[1].split()[0]
            if state != "Z" and os.getpgid(int(entry.name)) == pgid:
                live.append(int(entry.name))
        except (FileNotFoundError, ProcessLookupError):
            pass
    return live


def released_claim_evidence(captured):
    import os
    checks = []
    paths = [(captured["path"], captured["device"], captured["inode"]), *captured["files"]]
    for path, device, inode in paths:
        path = Path(path)
        if os.path.lexists(path):
            info = path.lstat()
            if (info.st_dev, info.st_ino) == (device, inode):
                raise ValueError("owned claim survived cleanup; no promotion")
            checks.append({"path": str(path), "owned_identity_absent": True, "different_claim_present": True})
        else:
            checks.append({"path": str(path), "owned_identity_absent": True, "different_claim_present": False})
    return {"owner_token": captured["token"], "checks": checks, "all_owned_claims_absent": True}

def run(m, node, owner, replay, attempt, plan_path, plan_sha, plan_id, historical_path, failed_path, admission_path, admission_sha, failure_path, failure_sha, supplement_path, supplement_sha):
    import os
    import re
    import shutil
    import signal
    import subprocess
    import sys
    import time

    root = Path(m["root"])
    if not isinstance(attempt, str) or re.fullmatch(r"[a-z][a-z0-9-]{0,23}", attempt) is None:
        raise ValueError("new resume basename of at most 24 characters required")
    reopen_attempt = attempt + "-reopen"
    for name in (attempt, reopen_attempt):
        if any(os.path.lexists(root / parent / name) for parent in ("attempts", "strict-attempts")):
            raise ValueError("existing resume/reopen attempt is never replayed")
    document, historical = bounded_fleet.bounded_plan_inputs(plan_path, plan_sha, plan_id, historical_path, m)
    p = document["plan"]
    admission, totals = failed_window_inputs(admission_path, admission_sha, failure_path, failure_sha, supplement_path, supplement_sha, document, historical, m)
    failed, failed_sha = recovery_fleet.baseline(failed_path, m)
    input_paths = (plan_path, admission_path, failure_path, supplement_path, historical_path, failed_path, root / "run.json", m["genesis"], Path(m["supply"]["root"]) / "manifest.json", Path(m["supply"]["root"]) / "genesis.json")
    pins = {str(path): hashlib.sha256(strict_fleet.read_private(Path(path), 16 << 20)).hexdigest() for path in input_paths}
    for shard in range(64):
        ack_path = Path(admission_path).parent / f"ack-shard-{shard:02d}.jsonl"
        pins[str(ack_path)] = hashlib.sha256(bounded_fleet.read_journal(ack_path)).hexdigest()
    configs = {v["config"]: strict_fleet.read_private(Path(v["config"]), 2 << 20) for v in m["validators"]}
    reserve = 64 << 30
    if shutil.disk_usage(root).free < reserve:
        raise ValueError("64GiB retained-data functional disk reserve required")
    children, logs, records = [], [], []
    log_root = None
    deadline = 0

    def pin_inputs():
        for path, digest in pins.items():
            if hashlib.sha256((bounded_fleet.read_journal(Path(path)) if Path(path).suffix == ".jsonl" else strict_fleet.read_private(Path(path), 16 << 20))).hexdigest() != digest:
                raise ValueError("resume fixed input changed")
        for path, raw in configs.items():
            if strict_fleet.read_private(Path(path), 2 << 20) != raw:
                raise ValueError("original validator configuration changed")
        for binary in (node, owner, replay):
            if fleet.binary(Path(binary["path"])) != binary:
                raise ValueError("resume binary changed")

    def guard(force=False):
        fleet.check_owned_claim([c.pid for c in children if c.poll() is None], force=force)
        if time.monotonic() >= deadline or shutil.disk_usage(root).free < reserve:
            raise ValueError("bounded resume turn/disk reserve exceeded")

    def live():
        guard()
        if any(c.poll() is not None for c in children):
            raise ValueError("owned resume runtime exited; preserve same data and intent")

    def record(name, value):
        # Durable intent/phase evidence is never overwritten on retry.
        path = log_root / name
        with os.fdopen(os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600), "w") as out:
            json.dump(value, out, indent=2)
            out.flush()
            os.fsync(out.fileno())
        fd = os.open(log_root, os.O_DIRECTORY | os.O_RDONLY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)

    def spawn(args, name):
        guard(True)
        pin_inputs()
        out = os.fdopen(os.open(log_root / (name + ".log"), os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600), "wb")
        logs.append(out)
        process = subprocess.Popen(args, cwd=root, env=dict(os.environ, GOMAXPROCS="2"), stdin=subprocess.DEVNULL, stdout=out, stderr=subprocess.STDOUT, start_new_session=True)
        data = {"name": name, "pid": process.pid, "pgid": process.pid, "command": args, "started_unix": time.time(), "gomaxprocs": 2, "forced_kill": False}
        children.append(process)
        records.append((process, data))
        try:
            data["actual_cpu_affinity"] = sorted(os.sched_getaffinity(process.pid))
        except ProcessLookupError:
            data["affinity_sample"] = "exited-before-sample"
        return process

    def wait_child(process, seconds):
        end = min(deadline, time.monotonic() + seconds)
        while process.poll() is None:
            guard()
            if any(c is not process and c.poll() is not None for c in children):
                raise ValueError("runtime exited during bounded resume command")
            if time.monotonic() >= end:
                raise TimeoutError("bounded resume command expired")
            time.sleep(.1)
        guard(True)
        if process.returncode != 0 or _live_group(process.pid):
            raise ValueError("actual resume command failed or left children; never replay automatically")
        children.remove(process)
        next(r for c, r in records if c is process).update(return_code=0, stopped_unix=time.time())

    def stop_phase(name):
        current = list(children)
        for c in reversed(current):
            if c.poll() is None or _live_group(c.pid):
                try:
                    os.killpg(c.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
        end = time.monotonic() + 30
        cleanup_errors = []
        for c in reversed(current):
            while _live_group(c.pid) and time.monotonic() < end:
                time.sleep(.05)
            if _live_group(c.pid):
                next(r for proc, r in records if proc is c)["forced_kill"] = True
                try:
                    os.killpg(c.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
            try:
                c.wait(timeout=5)
            except subprocess.TimeoutExpired:
                cleanup_errors.append("owned leader did not exit")
            if _live_group(c.pid):
                cleanup_errors.append("owned process group did not drain")
            else:
                children.remove(c)
            next(r for proc, r in records if proc is c).update(return_code=c.poll(), stopped_unix=time.time())
        selected = [r for c, r in records if c in current]
        clean = all(r["return_code"] == 0 and not r["forced_kill"] for r in selected)
        record(name, {"cleanup_clean": clean, "processes": selected})
        if cleanup_errors or not clean:
            raise ValueError("resume runtime did not exit cleanly; no promotion")

    sockets = [m["owner_socket"], str(root / "e/execution-rpc/ingress.sock")] + [v["api_socket"] for v in m["validators"]]

    def no_sockets():
        if any(os.path.lexists(path) for path in sockets):
            raise ValueError("existing socket is never deleted by resume launcher")

    def ready(paths, seconds):
        end = min(deadline, time.monotonic() + seconds)
        while not all(Path(path).exists() for path in paths):
            live()
            if time.monotonic() >= end:
                raise TimeoutError("resume runtime readiness timeout")
            time.sleep(.1)
        guard(True)

    fleet.quiet_checks()
    with fleet.claim() as resources:
        deadline = time.monotonic() + 600
        active_claim = fleet._active_claim
        claim_info = active_claim["path"].lstat()
        captured_claim = {"path": str(active_claim["path"]), "device": claim_info.st_dev, "inode": claim_info.st_ino, "token": active_claim["token"], "files": [(str(path), dev, ino) for path, dev, ino in active_claim["files"]]}
        if strict_fleet.validate(root / "run.json") != m or bounded_fleet.bounded_plan_inputs(plan_path, plan_sha, plan_id, historical_path, m) != (document, historical):
            raise ValueError("retained runtime/plan changed while waiting")
        pin_inputs()
        no_sockets()
        log_root = fleet.attempt_logs(root, attempt)
        result = {"kind": "gov5-post-bounded-nonproducing-double-reopen-v1", "identity": m["identity"], "resources": resources, "binaries": {"node": node, "owner": owner, "replay": replay}, "inputs_sha256": pins, "plan_id": plan_id, "no_initialization": True, "producer_disabled_configuration_copies": True, "dispatched_transactions": 0, "known_prior_new_transactions": totals["new"], "known_prior_canonical_skipped": totals["skip"], "minimum_canonical_count": admission["reference_final"]["committed_executed_transactions"], "maximum_canonical_count": p["CanonicalNonceSum"] + totals["new"], "canonical_baseline_count": p["CanonicalNonceSum"], "log_directory": str(log_root), "started_unix": time.time(), "tps_measured": False, "production_snapshot_reconstructed": False}
        record("post-bounded-audit-intent.json", result)
        try:
            for phase, audit_attempt in (("first", attempt), ("second", reopen_attempt)):
                spawn([owner["path"], "--genesis", m["genesis"], "--data-dir", m["owner_dir"], "--parallel-evm", "--strict-ingress-manifest", str(Path(m["supply"]["root"]) / "manifest.json")], phase + "-owner")
                ready(sockets[:2], 45)
                for validator in m["validators"]:
                    config = json.loads(configs[validator["config"]], object_pairs_hook=fleet.pairs_unique)
                    config["node"]["miner"] = False
                    path = log_root / (phase + "-v" + str(validator["index"]) + ".json")
                    record(path.name, config)
                    spawn([node["path"], "--config", str(path)], phase + "-v" + str(validator["index"]))
                ready(sockets[2:], 45)
                audit = spawn([replay["path"], "recover", "--root", m["supply"]["root"], "--runtime-manifest", str(root / "run.json"), "--attempt", audit_attempt, "--baseline-result", str(failed_path), "--require-canonical-roots"], phase + "-audit")
                wait_child(audit, 60)
                recovery_path = root / "strict-attempts" / audit_attempt / "recovery.json"
                observed = strict_fleet.metadata(recovery_path, 16 << 20)
                if phase == "first":
                    first = observed
                    first_audit_evidence(first, document, historical, m, failed, failed_path, failed_sha, admission)
                    result["first_recovery_result"] = str(recovery_path)
                    result["first_recovery_sha256"] = hashlib.sha256(strict_fleet.read_private(recovery_path, 16 << 20)).hexdigest()
                    result["first_canonical_owner"] = first["owner_final"]
                    result["first_canonical_roots"] = first["canonical_roots"]
                else:
                    count = second_audit_evidence(observed, first, document, historical, m, failed, failed_path, failed_sha, admission)
                    if observed["accounts"] != first["accounts"] or observed["account_vector_sha256"] != first["account_vector_sha256"]:
                        raise ValueError("second physical reopen changed the complete first account vector")
                    result.update(canonical_nonce_sum=count, actual_prior_new_persisted=count-p["CanonicalNonceSum"], known_ack_new_not_canonical=totals["new"]-(count-p["CanonicalNonceSum"]), recovery_result=str(recovery_path), recovery_sha256=hashlib.sha256(strict_fleet.read_private(recovery_path, 16 << 20)).hexdigest(), account_vector_sha256=observed["account_vector_sha256"], second_canonical_owner=observed["owner_final"], second_canonical_roots=observed["canonical_roots"], finished_unix=time.time())
                record(phase + "-canonical-audit-accepted.json", {"recovery_result": str(recovery_path), "recovery_sha256": hashlib.sha256(strict_fleet.read_private(recovery_path, 16 << 20)).hexdigest(), "canonical_owner": observed["owner_final"], "canonical_roots": observed["canonical_roots"], "account_vector_sha256": observed["account_vector_sha256"], "canonical_nonce_sum": observed["canonical_nonce_sum"], "production_running": False, "dispatched_transactions": 0})
                stop_phase(phase + "-process-lifecycle.json")
                no_sockets()
                guard(True)
            pin_inputs()
            record("post-bounded-audit-observation.json", result)
        finally:
            primary_failure = sys.exc_info()[0] is not None
            cleanup_error = None
            if children:
                try:
                    stop_phase("failed-process-lifecycle.json")
                except BaseException as error:
                    cleanup_error = error
            for log in logs:
                log.close()
            cleanup_clean = all(c.poll() == 0 and not r["forced_kill"] and not _live_group(c.pid) for c, r in records)
            record("all-process-lifecycle.json", {"processes": [r for _, r in records], "cleanup_clean": cleanup_clean})
            if not primary_failure:
                if cleanup_error is not None:
                    raise cleanup_error
                if not cleanup_clean:
                    raise ValueError("resume cleanup failed; no result promotion")
    # A normal contextmanager return alone is insufficient: its best-effort
    # unlink may fail. Prove this owner's captured claim identities disappeared.
    result["claim_cleanup"] = released_claim_evidence(captured_claim)
    pin_inputs()
    record("post-bounded-audit-result.json", result)
    return result

def main():
    import argparse
    import os
    import signal
    import subprocess
    import sys
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("--run", action="store_true")
    parser.add_argument("--attempt")
    parser.add_argument("--node-binary", type=Path)
    parser.add_argument("--owner-binary", type=Path)
    parser.add_argument("--replay-binary", type=Path)
    parser.add_argument("--plan", type=Path, required=True)
    parser.add_argument("--plan-sha256", required=True)
    parser.add_argument("--expected-plan-id", required=True)
    parser.add_argument("--admission-result", type=Path, required=True)
    parser.add_argument("--admission-sha256", required=True)
    parser.add_argument("--failure-acceptance", type=Path, required=True)
    parser.add_argument("--failure-acceptance-sha256", required=True)
    parser.add_argument("--reference-supplement", type=Path, required=True)
    parser.add_argument("--reference-supplement-sha256", required=True)
    parser.add_argument("--historical-recovery", type=Path, required=True)
    parser.add_argument("--failed-baseline", type=Path, required=True)
    args = parser.parse_args()
    def interrupted(signum, frame):
        raise KeyboardInterrupt("signal " + str(signum))
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    os.umask(0o077)
    try:
        m = strict_fleet.validate(args.manifest)
        bounded_fleet.bounded_plan_inputs(args.plan, args.plan_sha256, args.expected_plan_id, args.historical_recovery, m)
        if args.run:
            if not args.attempt or any(path is None for path in (args.node_binary, args.owner_binary, args.replay_binary)):
                raise ValueError("actual resume requires explicit binaries and fresh attempt")
            value = run(m, fleet.binary(args.node_binary), fleet.binary(args.owner_binary), fleet.binary(args.replay_binary), args.attempt, args.plan, args.plan_sha256, args.expected_plan_id, args.historical_recovery, args.failed_baseline, args.admission_result, args.admission_sha256, args.failure_acceptance, args.failure_acceptance_sha256, args.reference_supplement, args.reference_supplement_sha256)
            print(json.dumps({"result": str(Path(value["log_directory"]) / "post-bounded-audit-result.json"), "kind": value["kind"]}))
        else:
            if args.attempt or args.node_binary or args.owner_binary or args.replay_binary:
                raise ValueError("execution options require --run")
            print(json.dumps({"kind": "post-bounded-double-reopen-validation-only", "plan_id": args.expected_plan_id}))
        return 0
    except (OSError, ValueError, TypeError, KeyError, subprocess.SubprocessError) as error:
        print(json.dumps({"error": str(error)}))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
