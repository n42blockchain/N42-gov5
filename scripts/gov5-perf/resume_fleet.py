#!/usr/bin/env python3
"""Bounded original-four-frame admission, canonical execution and physical reopen.

This explicit path never initializes, resets or replays an existing attempt.
Admission evidence is provisional until the same-root nonproducing reopen audit.
"""
import hashlib
import json
from pathlib import Path

import fleet
import recovery_fleet
import strict_fleet


def plan_inputs(path, expected_sha, expected_id, recovery_path, m):
    raw = strict_fleet.read_private(Path(path), 16 << 20)
    if hashlib.sha256(raw).hexdigest() != expected_sha:
        raise ValueError("fixed four-frame plan file changed")
    value = json.loads(raw, object_pairs_hook=fleet.pairs_unique)
    if set(value) != {"kind", "plan", "plan_id"} or value["kind"] != "gov5-resume-four-frame-plan-v1" or value["plan_id"] != expected_id:
        raise ValueError("fixed four-frame plan identity differs")
    p = value["plan"]
    if p["Version"] != 1 or p["Identity"] != m["identity"] or p["ManifestSHA256"] != "0x" + m["supply"]["manifest_sha256"] or p["CorpusID"] != m["supply"]["corpus_id"] or p["GenesisFileSHA256"] != "0x" + m["genesis_sha256"]:
        raise ValueError("four-frame plan/source binding differs")
    recovery_raw = strict_fleet.read_private(Path(recovery_path), 16 << 20)
    if "0x" + hashlib.sha256(recovery_raw).hexdigest() != p["RecoverySHA256"]:
        raise ValueError("historical full nonce vector changed")
    historical = json.loads(recovery_raw, object_pairs_hook=fleet.pairs_unique)
    if historical["passed"] is not True or historical.get("error") or historical["dispatched_transactions"] != 0 or historical["identity"] != m["identity"] or historical["runtime_manifest"] != str(Path(m["root"]) / "run.json") or historical["manifest_sha256"] != p["ManifestSHA256"] or historical["canonical_nonce_sum"] != p["CanonicalNonceSum"] or historical["account_vector_sha256"] != p["AccountVectorSHA256"]:
        raise ValueError("historical recovery is not the plan's canonical baseline")
    accounts = historical["accounts"]
    if "0x" + hashlib.sha256(json.dumps(accounts, separators=(",", ":")).encode()).hexdigest() != p["AccountVectorSHA256"]:
        raise ValueError("historical account vector digest differs")
    by_address = {a["address"]: a for a in accounts}
    senders = p["Senders"]
    if len(by_address) != len(accounts) or len(senders) != len(accounts) or sum(a["nonce"] for a in accounts) != p["CanonicalNonceSum"]:
        raise ValueError("complete historical sender vector required")
    for index, sender in enumerate(senders):
        if sender["Index"] != index or sender["Address"] not in by_address or sender["Nonce"] != by_address[sender["Address"]]["nonce"]:
            raise ValueError("logical sender mapping differs from address-joined baseline")
    source_manifest = strict_fleet.metadata(Path(m["supply"]["root"]) / "manifest.json", 1 << 20)
    descriptor = source_manifest["descriptor"]
    frames = p["Frames"]
    minimum = {nonce: next((s["Index"] for s in senders if s["Nonce"] == nonce), None) for nonce in (0, 41, 42, 500)}
    if None in minimum.values() or len(frames) != 4 or [f["SenderIndex"] for f in frames] != sorted(minimum.values()):
        raise ValueError("exact four minimum-index nonce buckets required")
    for frame in frames:
        sender = senders[frame["SenderIndex"]]
        skip = min(sender["Nonce"], 500)
        if frame["CanonicalSkipped"] != skip or frame["NewlyAdmitted"] != 500 - skip or frame["ID"] != {"CorpusID": p["CorpusID"], "Shard": sender["Index"] % descriptor["shard_count"], "Sequence": sender["Index"] // descriptor["shard_count"]} or type(frame["PayloadBytes"]) is not int or not 2500 <= frame["PayloadBytes"] <= 1026000:
            raise ValueError("original four-frame range/counter binding differs")
    if sum(f["CanonicalSkipped"] for f in frames) != 583 or sum(f["NewlyAdmitted"] for f in frames) != 1417:
        raise ValueError("four-frame 583/1417 bound differs")
    return value, historical


def admission_evidence(value, document, m, plan_sha, runtime_sha):
    p, plan_id = document["plan"], document["plan_id"]
    if value.get("kind") != "gov5-resume-four-frame-admission-candidate-v1" or value.get("passed") is not True or value.get("error") or value["identity"] != m["identity"] or value["plan_id"] != plan_id or value["plan_sha256"] != "0x" + plan_sha or value["runtime_sha256"] != "0x" + runtime_sha or value["runtime_manifest"] != str(Path(m["root"]) / "run.json") or value["production_started"] is not False or value["commit_proof_included"] is not False:
        raise ValueError("four-frame provisional admission evidence differs")
    first, final = value["owner_initial"], value["owner_final"]
    epoch = first["Epoch"]
    if not isinstance(epoch, list) or len(epoch) != 16 or not any(epoch) or any(type(v) is not int or not 0 <= v <= 255 for v in epoch):
        raise ValueError("new bound resume epoch required")
    for status in (first, final):
        if status["Identity"] != m["identity"] or status["Epoch"] != epoch or status["ManifestHash"] != p["ManifestSHA256"] or status["CanonicalHash"] != p["Canonical"]["Hash"] or status["CanonicalNumber"] != p["Canonical"]["Number"] or status["Reconciling"]:
            raise ValueError("owner changed baseline/epoch during original four-frame admission")
    counters = ("NewlyAdmitted", "CryptoVerified", "PendingTransactions", "ReservedTransactions", "RetainedEncodedBytes", "ReservedEncodedBytes")
    if any(type(first[k]) is not int or first[k] != 0 for k in counters):
        raise ValueError("four-frame owner was not fresh and empty")
    retained = sum(f["PayloadBytes"] for f in p["Frames"] if f["NewlyAdmitted"])
    retries = value["known_zero_retries"]
    if type(retries) is not int or retries < 0 or final["NewlyAdmitted"] != 1417 or final["PendingTransactions"] != 1417 or final["RetainedEncodedBytes"] != retained or final["ReservedTransactions"] != 0 or final["ReservedEncodedBytes"] != 0 or not 2000 <= final["CryptoVerified"] <= 2000 + 500 * retries or value["newly_admitted"] != 1417 or value["canonical_skipped"] != 583:
        raise ValueError("four-frame exact new/skip/signature/credit totals differ")
    acks = value["acknowledgments"]
    if len(acks) != 4:
        raise ValueError("all four final ACKs required before producers may start")
    admitted, retained = 0, 0
    for receipt, frame in zip(acks, p["Frames"]):
        if set(receipt) != {"frame", "ack"} or receipt["frame"] != frame:
            raise ValueError("persisted receipt original frame/hash binding differs")
        ack = receipt["ack"]
        admitted += frame["NewlyAdmitted"]
        if frame["NewlyAdmitted"]:
            retained += frame["PayloadBytes"]
        if ack["Status"]["NewlyAdmitted"] != admitted or ack["Status"]["PendingTransactions"] != admitted or ack["Status"]["RetainedEncodedBytes"] != retained or ack["Status"]["CanonicalHash"] != p["Canonical"]["Hash"] or ack["Status"]["CanonicalNumber"] != p["Canonical"]["Number"]:
            raise ValueError("per-frame owner cumulative credit/baseline evidence differs")
        if ack["PlanID"] != plan_id or ack["Status"]["Epoch"] != epoch or ack["Status"]["Identity"] != m["identity"] or ack["Status"]["ManifestHash"] != p["ManifestSHA256"] or ack["Duplicate"] is not False or ack["NewlyAdmitted"] != frame["NewlyAdmitted"] or ack["PriorAdmission"] != frame["NewlyAdmitted"] or ack["CanonicalSkipped"] != frame["CanonicalSkipped"]:
            raise ValueError("one original frame's final ACK differs")
    return epoch


def root_evidence(value, m, owner, address):
    if value["identity"] != m["identity"] or value["epoch"] != owner["Epoch"] or value["manifest_hash"] != owner["ManifestHash"] or value["canonical_hash"] != owner["CanonicalHash"] or value["canonical_number"] != owner["CanonicalNumber"] or len(value["accounts"]) != 1 or value["accounts"][0]["address"] != address or value["accounts"][0]["exists"] is not True:
        raise ValueError("same-snapshot canonical root evidence differs")
    roots = value.get("roots")
    if not isinstance(roots, dict) or not strict_fleet.valid_hash(roots.get("root")) or roots["root"] == "0x" + "00" * 32 or roots.get("applied_root") != roots["root"]:
        raise ValueError("canonical header and loaded QMDB roots differ")
    return roots


def reopened_evidence(value, document, historical, m, failed, failed_path, failed_sha, before_stop, root_before):
    count = recovery_fleet.evidence(value, m, failed, failed_path, failed_sha)
    p = document["plan"]
    expected = p["CanonicalNonceSum"] + 1417
    if count != expected or value["owner_initial"]["Epoch"] == before_stop["Epoch"]:
        raise ValueError("reopen did not reach the exact new canonical count/fresh epoch")
    roots = value.get("canonical_roots")
    if roots is None or roots["roots"] != root_before or roots["final_read"] is not True or roots["account_batches"] != (len(p["Senders"]) + 999) // 1000:
        raise ValueError("physical reopen root/vector batch evidence differs")
    for owner in (value["owner_initial"], value["owner_final"]):
        if owner["CanonicalHash"] != before_stop["CanonicalHash"] or owner["CanonicalNumber"] != before_stop["CanonicalNumber"]:
            raise ValueError("physical reopen canonical tuple changed")
    if roots["canonical_hash"] != before_stop["CanonicalHash"] or roots["canonical_number"] != before_stop["CanonicalNumber"]:
        raise ValueError("physical reopen root tuple differs")
    old = {a["address"]: a for a in historical["accounts"]}
    increments = {p["Senders"][f["SenderIndex"]]["Address"]: f["NewlyAdmitted"] for f in p["Frames"]}
    for account in value["accounts"]:
        previous = old[account["address"]]
        extra = increments.get(account["address"], 0)
        if account["nonce"] != previous["nonce"] + extra or int(account["balance"]) != int(previous["balance"]) - 21001 * extra or account["code_hash"] != previous["code_hash"] or account["exists"] != previous["exists"]:
            raise ValueError("reopen full sender nonce/balance delta differs from sealed suffixes")
    return count


class SnapshotRetry(ValueError):
    pass


def root_request(m, owner, address):
    client = fleet.UnixHTTP(m["owner_socket"], timeout=3)
    try:
        request = {"identity": m["identity"], "epoch": owner["Epoch"], "manifest_hash": owner["ManifestHash"], "canonical_hash": owner["CanonicalHash"], "canonical_number": owner["CanonicalNumber"], "addresses": [address], "include_roots": True}
        client.request("POST", "/v1/ingress-accounts", json.dumps(request), {"Content-Type": "application/json"})
        response = client.getresponse()
        raw = response.read(8193)
        if response.status == 409:
            raise SnapshotRetry("exact canonical root snapshot not yet applied")
        if response.status != 200 or len(raw) > 8192:
            raise ValueError("exact canonical root query unavailable")
        value = json.loads(raw, object_pairs_hook=fleet.pairs_unique)
        root_evidence(value, m, owner, address)
        return value
    finally:
        client.close()


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


def run(m, node, owner, replay, attempt, plan_path, plan_sha, plan_id, proof_path, historical_path, failed_path):
    import http.client
    import os
    import re
    import shutil
    import signal
    import subprocess
    import sys
    import time
    import queue_fixture

    root = Path(m["root"])
    if not isinstance(attempt, str) or re.fullmatch(r"[a-z][a-z0-9-]{0,23}", attempt) is None:
        raise ValueError("new resume basename of at most 24 characters required")
    reopen_attempt = attempt + "-reopen"
    for name in (attempt, reopen_attempt):
        if any(os.path.lexists(root / parent / name) for parent in ("attempts", "strict-attempts")):
            raise ValueError("existing resume/reopen attempt is never replayed")
    document, historical = plan_inputs(plan_path, plan_sha, plan_id, historical_path, m)
    p = document["plan"]
    proof_raw = strict_fleet.read_private(Path(proof_path), 16 << 20)
    if "0x" + hashlib.sha256(proof_raw).hexdigest() != p["SourceProofSHA256"]:
        raise ValueError("fixed source proof bytes differ")
    failed, failed_sha = recovery_fleet.baseline(failed_path, m)
    pins = {str(path): hashlib.sha256(strict_fleet.read_private(Path(path), 16 << 20)).hexdigest() for path in (plan_path, proof_path, historical_path, failed_path, root / "run.json", m["genesis"], Path(m["supply"]["root"]) / "manifest.json", Path(m["supply"]["root"]) / "genesis.json")}
    configs = {v["config"]: strict_fleet.read_private(Path(v["config"]), 2 << 20) for v in m["validators"]}
    reserve = 64 << 30
    if shutil.disk_usage(root).free < reserve:
        raise ValueError("64GiB retained-data functional disk reserve required")
    children, logs, records = [], [], []
    log_root = None
    deadline = 0

    def pin_inputs():
        for path, digest in pins.items():
            if hashlib.sha256(strict_fleet.read_private(Path(path), 16 << 20)).hexdigest() != digest:
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

    def statuses():
        result = []
        for v in m["validators"]:
            try:
                result.append(fleet.status(v["api_socket"], m["identity"]))
            finally:
                guard(True)
        return result

    fleet.quiet_checks()
    with fleet.claim() as resources:
        deadline = time.monotonic() + 600
        active_claim = fleet._active_claim
        claim_info = active_claim["path"].lstat()
        captured_claim = {"path": str(active_claim["path"]), "device": claim_info.st_dev, "inode": claim_info.st_ino, "token": active_claim["token"], "files": [(str(path), dev, ino) for path, dev, ino in active_claim["files"]]}
        if strict_fleet.validate(root / "run.json") != m or plan_inputs(plan_path, plan_sha, plan_id, historical_path, m) != (document, historical):
            raise ValueError("retained runtime/plan changed while waiting")
        pin_inputs()
        no_sockets()
        log_root = fleet.attempt_logs(root, attempt)
        result = {"kind": "gov5-original-four-frame-canonical-physical-reopen-v1", "identity": m["identity"], "resources": resources, "binaries": {"node": node, "owner": owner, "replay": replay}, "inputs_sha256": pins, "plan_id": plan_id, "no_initialization": True, "expected_new_transactions": 1417, "expected_canonical_skipped": 583, "canonical_baseline_count": p["CanonicalNonceSum"], "target_count": p["CanonicalNonceSum"] + 1417, "log_directory": str(log_root), "started_unix": time.time(), "tps_measured": False}
        record("resume-intent.json", result)
        try:
            args = [owner["path"], "--genesis", m["genesis"], "--data-dir", m["owner_dir"], "--parallel-evm", "--resume-ingress-plan", str(plan_path), "--resume-ingress-source-proof", str(proof_path), "--resume-ingress-source", m["supply"]["root"], "--resume-ingress-manifest", str(Path(m["supply"]["root"]) / "manifest.json"), "--resume-ingress-plan-id", plan_id, "--ingress-max-transactions", "1417", "--ingress-max-encoded-bytes", str(sum(f["PayloadBytes"] for f in p["Frames"]))]
            spawn(args, "resume-owner")
            ready(sockets[:2], 120)
            # This process list is the startup-order proof: no validator has
            # been launched before every original frame is acknowledged.
            record("owner-only-before-dispatch.json", {"processes": [r for _, r in records], "validator_sockets_absent": all(not os.path.lexists(path) for path in sockets[2:])})
            if any(os.path.lexists(path) for path in sockets[2:]):
                raise ValueError("validator appeared before sealed four-frame admission")
            process = spawn([replay["path"], "resume-four-frame", "--root", m["supply"]["root"], "--runtime-manifest", str(root / "run.json"), "--attempt", attempt, "--plan", str(plan_path), "--plan-sha256", plan_sha, "--expected-plan-id", plan_id], "four-frame-admission")
            wait_child(process, 60)
            candidate_path = root / "strict-attempts" / attempt / "result-candidate.json"
            candidate = strict_fleet.metadata(candidate_path, 16 << 20)
            epoch = admission_evidence(candidate, document, m, plan_sha, pins[str(root / "run.json")])
            if epoch == historical["owner_final"]["Epoch"]:
                raise ValueError("resume session reused historical owner epoch")
            for i, ack in enumerate(candidate["acknowledgments"]):
                if strict_fleet.metadata(candidate_path.parent / f"ack-{i}.json") != ack:
                    raise ValueError("persisted final ACK differs from candidate")
            record("four-frame-admission-accepted.json", {"candidate": str(candidate_path), "candidate_sha256": hashlib.sha256(strict_fleet.read_private(candidate_path, 16 << 20)).hexdigest(), "epoch": epoch, "producers_started": False, "unique_new": 1417, "canonical_skipped": 583})
            for v in m["validators"]:
                config = json.loads(configs[v["config"]], object_pairs_hook=fleet.pairs_unique)
                config["node"]["miner"] = True
                path = log_root / f"produce-v{v['index']}.json"
                record(path.name, config)
                spawn([node["path"], "--config", str(path)], "produce-v" + str(v["index"]))
            ready(sockets[2:], 45)
            end = min(deadline, time.monotonic() + 120)
            while True:
                live()
                final = statuses()
                current = queue_fixture.owner_status(m)
                guard(True)
                if current["Epoch"] != epoch or current["Identity"] != m["identity"] or current["ManifestHash"] != p["ManifestSHA256"] or current["NewlyAdmitted"] != 1417 or any(s["committed_executed_transactions"] > result["target_count"] for s in final):
                    raise ValueError("sealed resume session/count exceeded expected bound")
                drained = not current["Reconciling"] and all(current[k] == 0 for k in ("PendingTransactions", "ReservedTransactions", "RetainedEncodedBytes", "ReservedEncodedBytes"))
                same = all(s["block_hash"] == current["CanonicalHash"] and s["number"] == current["CanonicalNumber"] and s["committed_executed_transactions"] == result["target_count"] and s["owner_queue"]["epoch"] == epoch for s in final)
                if drained and same:
                    try:
                        root_read = root_request(m, current, p["Senders"][0]["Address"])
                    except SnapshotRetry:
                        root_read = None
                    guard(True)
                    if root_read is not None and queue_fixture.owner_status(m) == current:
                        break
                if time.monotonic() >= end:
                    raise TimeoutError("bounded four-frame canonical drain failed")
                time.sleep(.1)
            result.update(admission_result=str(candidate_path), before_stop_owner=current, before_stop_validators=final, before_stop_roots=root_read["roots"])
            record("canonical-before-stop.json", {"owner": current, "validators": final, "root_read": root_read})
            stop_phase("production-process-lifecycle.json")
            guard(True)
            no_sockets()
            # The owner now reopens the same physical data using ordinary strict
            # mode and a fresh empty epoch. No resume plan or frame is resent.
            spawn([owner["path"], "--genesis", m["genesis"], "--data-dir", m["owner_dir"], "--parallel-evm", "--strict-ingress-manifest", str(Path(m["supply"]["root"]) / "manifest.json")], "reopen-owner")
            ready(sockets[:2], 45)
            for v in m["validators"]:
                config = json.loads(configs[v["config"]], object_pairs_hook=fleet.pairs_unique)
                config["node"]["miner"] = False
                path = log_root / f"reopen-v{v['index']}.json"
                record(path.name, config)
                spawn([node["path"], "--config", str(path)], "reopen-v" + str(v["index"]))
            ready(sockets[2:], 45)
            audit = spawn([replay["path"], "recover", "--root", m["supply"]["root"], "--runtime-manifest", str(root / "run.json"), "--attempt", reopen_attempt, "--baseline-result", str(failed_path), "--require-canonical-roots"], "physical-reopen-audit")
            wait_child(audit, 60)
            recovery_path = root / "strict-attempts" / reopen_attempt / "recovery.json"
            reopened = strict_fleet.metadata(recovery_path, 16 << 20)
            result["canonical_nonce_sum"] = reopened_evidence(reopened, document, historical, m, failed, failed_path, failed_sha, current, root_read["roots"])
            result.update(recovery_result=str(recovery_path), recovery_sha256=hashlib.sha256(strict_fleet.read_private(recovery_path, 16 << 20)).hexdigest(), account_vector_sha256=reopened["account_vector_sha256"], finished_unix=time.time())
            stop_phase("reopen-process-lifecycle.json")
            no_sockets()
            guard(True)
            pin_inputs()
            record("resume-fleet-observation.json", result)
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
    record("resume-fleet-result.json", result)
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
    parser.add_argument("--source-proof", type=Path, required=True)
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
        plan_inputs(args.plan, args.plan_sha256, args.expected_plan_id, args.historical_recovery, m)
        if args.run:
            if not args.attempt or any(path is None for path in (args.node_binary, args.owner_binary, args.replay_binary)):
                raise ValueError("actual resume requires explicit binaries and fresh attempt")
            value = run(m, fleet.binary(args.node_binary), fleet.binary(args.owner_binary), fleet.binary(args.replay_binary), args.attempt, args.plan, args.plan_sha256, args.expected_plan_id, args.source_proof, args.historical_recovery, args.failed_baseline)
            print(json.dumps({"result": str(Path(value["log_directory"]) / "resume-fleet-result.json"), "kind": value["kind"]}))
        else:
            if args.attempt or args.node_binary or args.owner_binary or args.replay_binary:
                raise ValueError("execution options require --run")
            print(json.dumps({"kind": "four-frame-resume-validation-only", "plan_id": args.expected_plan_id}))
        return 0
    except (OSError, ValueError, TypeError, KeyError, subprocess.SubprocessError) as error:
        print(json.dumps({"error": str(error)}))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
