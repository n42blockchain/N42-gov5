#!/usr/bin/env python3
"""Finite original-frame preload, then two-block phase diagnosis and real reopen.

No TPS window. Never replay a partial attempt or infer persistence from ingress ACKs.
"""
import hashlib
import json
import re
import struct
from pathlib import Path

import bounded_fleet
import fleet
import queue_fixture
import recovery_fleet
import resume_fleet
import strict_fleet

_live_group = resume_fleet._live_group
released_claim_evidence = resume_fleet.released_claim_evidence
bounded_plan_inputs = bounded_fleet.bounded_plan_inputs
validator_binding = bounded_fleet.validator_binding
journal_evidence = bounded_fleet.journal_evidence
stable_owner_snapshot = bounded_fleet.stable_owner_snapshot
bounded_reopened_evidence = bounded_fleet.bounded_reopened_evidence

def finite_schedule_evidence(value, document, m):
    p, s = document["plan"], value["schedule"]
    frames = s["frames"]
    if s["plan_id"] != document["plan_id"] or type(s["target_new_transactions"]) is not int or s["target_new_transactions"] != 200000 or not 1 <= len(frames) <= 1810:
        raise ValueError("finite exact target/plan/frame bound differs")
    count = len(p["Senders"])
    senders, shards = [0]*count, [0]*64
    total = {"new": 0, "skip": 0, "original": 0, "bytes": 0, "retained": 0}
    digest = hashlib.sha256(b"gov5/finite-phase-schedule/v1\x00" + bytes.fromhex(document["plan_id"][2:]) + struct.pack(">QQ", 200000, len(frames)))
    journals = {}
    for position, f in enumerate(frames):
        shard, seq = position % 64, position // 64
        per_round = (count-1-shard)//64+1
        index, first = shard+(seq % per_round)*64, (seq//per_round)*500
        sender = p["Senders"][index]
        skip = min(500, max(0, sender["Nonce"]-first))
        if total["new"] >= 200000 or f["ID"] != {"CorpusID": p["CorpusID"], "Shard": shard, "Sequence": seq} or f["SenderIndex"] != index or f["Address"] != sender["Address"] or f["FirstNonce"] != first or f["Transactions"] != 500 or f["CanonicalSkipped"] != skip or f["NewlyAdmitted"] != 500-skip or first >= p["NonceEndExclusive"] or type(f["PayloadBytes"]) is not int or not 2500 <= f["PayloadBytes"] <= 53000 or not strict_fleet.valid_hash(f["PayloadHash"]):
            raise ValueError("finite round-robin original continuous prefix differs")
        digest.update(bytes.fromhex(f["ID"]["CorpusID"][2:]) + struct.pack(">HQI", shard, seq, index) + bytes.fromhex(f["Address"][2:]) + struct.pack(">QH", first, 500) + bytes.fromhex(f["PayloadHash"][2:]) + struct.pack(">IHH", f["PayloadBytes"], skip, 500-skip))
        shards[shard] += 1; senders[index] += 500-skip
        total["new"] += 500-skip; total["skip"] += skip; total["original"] += 500; total["bytes"] += 84+f["PayloadBytes"]
        if skip != 500: total["retained"] += f["PayloadBytes"]
        journals[(shard, seq)] = f
    if not 200000 <= total["new"] <= 200499 or total["original"] > p["CanonicalNonceSum"]+200499 or s["schedule_sha256"] != "0x"+digest.hexdigest() or s["frames_per_shard"] != shards or s["sender_new_transactions"] != senders or any(s[key] != total[field] for key, field in (("newly_admitted", "new"), ("canonical_skipped", "skip"), ("original_transactions", "original"), ("encoded_bytes", "bytes"), ("retained_encoded_bytes", "retained"))):
        raise ValueError("finite derived totals/shards/senders/digest differ")
    if value["sender_new_transactions"] != senders:
        raise ValueError("finite sender deltas differ from selected original schedule")
    return total, journals


def preload_evidence(value, document, m, plan_sha, runtime_sha):
    p = document["plan"]
    if value.get("kind") != "gov5-bounded-finite-preload-candidate-no-tps-v1" or value.get("passed") is not True or value.get("error") or value["identity"] != m["identity"] or value["plan_id"] != document["plan_id"] or value["plan_sha256"] != "0x"+plan_sha or value["runtime_sha256"] != "0x"+runtime_sha or value["runtime_manifest"] != str(Path(m["root"])/"run.json") or value["full_source_integrity_verified"] is not True or any(value[key] is not False for key in ("production_started", "commit_proof_included", "tps_measured", "capacity_5m_proven")):
        raise ValueError("finite candidate identity/scope differs")
    bounded_fleet.source_ready_evidence({"kind": "gov5-bounded-source-ready-before-production-v2", "plan_id": document["plan_id"], "epoch": value["owner_initial"]["Epoch"], "owner": value["owner_initial"], "full_source_integrity_verified": True, "dispatched_transactions": 0}, document, m)
    totals, _ = finite_schedule_evidence(value, document, m)
    stream, owner, initial = value["stream_final"], value["owner_final"], value["owner_initial"]
    if owner["Identity"] != initial["Identity"] or owner["ManifestHash"] != initial["ManifestHash"] or owner["Epoch"] != initial["Epoch"] or owner["CanonicalHash"] != initial["CanonicalHash"] or owner["CanonicalNumber"] != initial["CanonicalNumber"] or owner["Reconciling"] or owner["NewlyAdmitted"] != totals["new"] or owner["PendingTransactions"] != totals["new"] or owner["RetainedEncodedBytes"] != totals["retained"] or any(owner[k] != 0 for k in ("ReservedTransactions", "ReservedEncodedBytes")):
        raise ValueError("finite acknowledged preload changed canonical baseline/credits")
    if stream["acknowledged_unique_admitted"] != totals["new"] or stream["acknowledged_canonical_skipped"] != totals["skip"] or stream["acknowledged_original_frames"]*500 != totals["original"] or stream["source_read_transactions"] != totals["original"] or stream["source_read_encoded_bytes"] != totals["bytes"]:
        raise ValueError("finite stream differs from selected full-frame schedule")
    if not totals["original"] <= owner["CryptoVerified"] <= totals["original"]+500*(stream["busy_retries"]+stream["snapshot_retries"]) or value["source_files_before"] != value["source_files_after"]:
        raise ValueError("finite crypto attempts/source metadata differ")
    return p["CanonicalNonceSum"]+totals["new"]


def finite_journal_evidence(directory, value, document):
    totals = bounded_fleet.journal_evidence(directory, value, document)
    expected, frames = finite_schedule_evidence(value, document, {})
    if any(totals[k] != expected[k] for k in ("new", "skip", "original", "bytes")) or totals["frames"] != len(frames):
        raise ValueError("finite journal totals differ from schedule")
    receipts = {}
    for shard in range(64):
        for sequence, line in enumerate(bounded_fleet.read_journal(Path(directory)/f"ack-shard-{shard:02d}.jsonl").splitlines()):
            receipt = json.loads(line, object_pairs_hook=fleet.pairs_unique)
            if receipt["frame"] != frames[(shard, sequence)]:
                raise ValueError("finite journal frame differs from sealed schedule")
            receipts[(shard, sequence)] = receipt
    used = {"SourceFrames": 0, "SourceTransactions": 0, "SourceEncodedBytes": 0, "NewTransactions": 0}
    retained, initial = 0, value["owner_initial"]
    for frame in value["schedule"]["frames"]:
        ack = receipts[(frame["ID"]["Shard"], frame["ID"]["Sequence"])]["ack"]
        used["SourceFrames"] += 1; used["SourceTransactions"] += 500; used["SourceEncodedBytes"] += 84+frame["PayloadBytes"]; used["NewTransactions"] += frame["NewlyAdmitted"]
        if frame["NewlyAdmitted"]: retained += frame["PayloadBytes"]
        status, life = ack["Status"], ack["Lifetime"]
        if any(life[key] != amount for key, amount in used.items()) or any(life[key] != 0 for key in ("ReservedFrames", "ReservedTransactions", "ReservedEncodedBytes", "ReservedNew")) or status["CanonicalHash"] != initial["CanonicalHash"] or status["CanonicalNumber"] != initial["CanonicalNumber"] or status["Reconciling"] or status["NewlyAdmitted"] != used["NewTransactions"] or status["PendingTransactions"] != used["NewTransactions"] or status["RetainedEncodedBytes"] != retained or any(status[key] != 0 for key in ("ReservedTransactions", "ReservedEncodedBytes")):
            raise ValueError("finite single-inflight ACK differs from exact preproduction prefix")
    return totals


def finite_drain(m, p, candidate, expected, live, guard, deadline, initial, record):
    import time
    end = min(deadline, time.monotonic()+45)
    previous, samples, passed = initial, [], False
    try:
        while time.monotonic() < end:
            live()
            reference = fleet.status(m["validators"][0]["api_socket"], m["identity"])
            guard(True)
            owner = queue_fixture.owner_status(m)
            guard(True)
            validator_binding(reference, m, candidate["owner_initial"]["Epoch"], p["ManifestSHA256"])
            if not bounded_fleet.canonical_nonregressing(reference, previous) or reference["committed_executed_transactions"] > expected:
                raise ValueError("finite canonical reference regressed or exceeded acknowledged count")
            previous = reference
            if owner["Epoch"] != candidate["owner_initial"]["Epoch"] or owner["Identity"] != p["Identity"] or owner["ManifestHash"] != p["ManifestSHA256"] or owner["NewlyAdmitted"] != candidate["schedule"]["newly_admitted"]:
                raise ValueError("finite owner session changed during drain")
            samples.append({"observed_unix": time.time(), "reference": reference, "owner": owner})
            if time.monotonic() >= end:
                break
            if reference["committed_executed_transactions"] == expected and not owner["Reconciling"] and all(owner[k] == 0 for k in ("PendingTransactions", "ReservedTransactions", "RetainedEncodedBytes", "ReservedEncodedBytes")):
                passed = True
                return {"passed": True, "reference": reference, "owner": owner, "canonical_count": expected, "samples": samples}
            time.sleep(.1)
        raise TimeoutError("finite 45-second canonical drain unavailable; preserve attempt")
    finally:
        record("finite-drain.json", {"passed": passed, "target_count": expected, "samples": samples})


def phase_log_baselines(m):
    import os
    baselines = []
    for validator in m["validators"]:
        path = Path(validator["data_dir"])/"log/n42.log"
        if os.path.lexists(path):
            raw = bounded_fleet.read_journal(path)
            info = path.lstat()
            if raw and not raw.endswith(b"\n"): raise ValueError("phase log baseline has partial line")
            baselines.append({"path": str(path), "device": info.st_dev, "inode": info.st_ino, "size": len(raw), "sha256": hashlib.sha256(raw).hexdigest()})
        else:
            baselines.append({"path": str(path), "device": None, "inode": None, "size": 0, "sha256": hashlib.sha256(b"").hexdigest()})
    return baselines


def capture_phase_segments(m, before, directory):
    import os
    segments = []
    for index, previous in enumerate(before):
        path = Path(previous["path"])
        raw = bounded_fleet.read_journal(path)
        info = path.lstat()
        if (previous["inode"] is not None and (info.st_dev, info.st_ino) != (previous["device"], previous["inode"])) or len(raw) < previous["size"] or hashlib.sha256(raw[:previous["size"]]).hexdigest() != previous["sha256"] or not raw.endswith(b"\n"):
            raise ValueError("phase log rotated/truncated/changed or has partial final line")
        segment = raw[previous["size"]:]
        destination = directory/f"phase-v{index}.jsonl"
        with os.fdopen(os.open(destination, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600), "wb") as out:
            out.write(segment); out.flush(); os.fsync(out.fileno())
        segments.append({"validator_index": index, "source": previous, "source_end_bytes": len(raw), "path": str(destination), "sha256": hashlib.sha256(segment).hexdigest(), "bytes": len(segment)})
    fd = os.open(directory, os.O_DIRECTORY | os.O_RDONLY)
    try: os.fsync(fd)
    finally: os.close(fd)
    return segments


def phase_evidence(segments, m, baseline_number=0):
    records, counts = [], {"produce": 0, "import": 0, "commit_certificate": 0}
    for segment in segments:
        raw = bounded_fleet.read_journal(Path(segment["path"]))
        if len(raw) != segment["bytes"] or hashlib.sha256(raw).hexdigest() != segment["sha256"]:
            raise ValueError("frozen phase segment changed")
        index = segment["validator_index"]
        for line in raw.splitlines():
            message = json.loads(line, object_pairs_hook=fleet.pairs_unique)
            if message.get("msg") != "shared validator operation phases": continue
            r = message["record"]
            if r["SchemaVersion"] != 1 or r["Validator"].lower() != m["validators"][index]["address"].lower() or r["Operation"] not in counts or message.get("operation") != r["Operation"] or r["Outcome"] not in ("ok", "error", "canceled", "deadline") or r["Path"] not in {"produce": ("", "ownWAL", "appliedChild", "newBuild"), "import": ("",), "commit_certificate": ("", "advanceCanonical", "alreadyCanonical")}[r["Operation"]]:
                raise ValueError("phase schema/validator/operation differs")
            for key in ("StartedUnixMicros", "TotalNs", "AccountedNs", "UnaccountedNs", "View", "Number", "Transactions", "RawBytes"):
                if type(r[key]) is not int or not 0 <= r[key] <= (1<<63)-1: raise ValueError("phase numeric bounds invalid")
            if type(r["Durable"]) is not bool or r["Transactions"] > 163000 or r["RawBytes"] > 128<<20 or not all(isinstance(r[k], str) and re.fullmatch(r"0x[0-9a-f]{64}", r[k]) is not None for k in ("Parent", "Hash")) or not isinstance(r["PhaseNs"], dict) or any(not isinstance(k, str) or type(v) is not int or v < 0 for k, v in r["PhaseNs"].items()) or sum(r["PhaseNs"].values()) != r["AccountedNs"] or r["AccountedNs"]+r["UnaccountedNs"] != r["TotalNs"]:
                raise ValueError("phase elapsed/body accounting differs")
            counts[r["Operation"]] += 1
            records.append({"validator_index": index, "record": r})
    matches = []
    for production in records:
        r = production["record"]
        if r["Operation"] != "produce" or r["Outcome"] != "ok" or not r["Durable"] or r["Transactions"] != 163000 or r["Number"] <= baseline_number or not all(strict_fleet.valid_hash(r[k]) for k in ("Parent", "Hash")): continue
        same = lambda x: all(x["record"][k] == r[k] for k in ("Hash", "Number", "Parent"))
        imports = [x["record"] for x in records if x["validator_index"] == 0 and same(x) and x["record"]["Operation"] == "import" and x["record"]["Outcome"] == "ok" and x["record"]["Durable"] and x["record"]["Transactions"] == 163000]
        commits = [x["record"] for x in records if x["validator_index"] == 0 and same(x) and x["record"]["Operation"] == "commit_certificate" and x["record"]["Outcome"] == "ok" and x["record"]["Path"] == "advanceCanonical"]
        if imports and commits:
            matches.append({"hash": r["Hash"], "number": r["Number"], "parent": r["Parent"], "producer_validator_index": production["validator_index"], "reference_validator_index": 0, "transactions": 163000})
    if not matches or any(n == 0 for n in counts.values()):
        raise ValueError("finite diagnostic lacks correlated 163000-transaction canonical phase proof")
    return {"kind": "finite-phase-canonical-block-shape-not-capacity", "segments": segments, "operation_counts": counts, "canonical_163000_blocks": matches, "records": records, "tps_measured": False, "capacity_5m_proven": False}


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
    document, historical = bounded_plan_inputs(plan_path, plan_sha, plan_id, historical_path, m)
    p = document["plan"]
    proof_raw = strict_fleet.read_private(Path(proof_path), 16 << 20)
    if "0x" + hashlib.sha256(proof_raw).hexdigest() != p["SourceProofSHA256"]:
        raise ValueError("fixed source proof bytes differ")
    failed, failed_sha = recovery_fleet.baseline(failed_path, m)
    pins = {str(path): hashlib.sha256(strict_fleet.read_private(Path(path), 16 << 20)).hexdigest() for path in (plan_path, proof_path, historical_path, failed_path, root / "run.json", m["genesis"], Path(m["supply"]["root"]) / "manifest.json", Path(m["supply"]["root"]) / "genesis.json")}
    configs = {v["config"]: strict_fleet.read_private(Path(v["config"]), 2 << 20) for v in m["validators"]}
    reserve = 64 << 30
    if shutil.disk_usage(root).free < p["MaxSourceTransactions"] * 4096 + reserve:
        raise ValueError("bounded source transactions * 4096 plus 64GiB free disk required")
    children, logs, records = [], [], []
    log_root = None
    deadline = 0
    phase_baselines, phase_capture_attempted = None, False

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
        gomax = 36 if name == "finite-owner" else 4 if name == "finite-preload" else 2
        process = subprocess.Popen(args, cwd=root, env=dict(os.environ, GOMAXPROCS=str(gomax)), stdin=subprocess.DEVNULL, stdout=out, stderr=subprocess.STDOUT, start_new_session=True)
        data = {"name": name, "pid": process.pid, "pgid": process.pid, "command": args, "started_unix": time.time(), "gomaxprocs": gomax, "forced_kill": False}
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

    def stop_phase(name, selected=None):
        current = list(children) if selected is None else list(selected)
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
        if strict_fleet.validate(root / "run.json") != m or bounded_plan_inputs(plan_path, plan_sha, plan_id, historical_path, m) != (document, historical):
            raise ValueError("retained runtime/plan changed while waiting")
        pin_inputs()
        no_sockets()
        log_root = fleet.attempt_logs(root, attempt)
        result = {"kind": "gov5-finite-phase-diagnostic-physical-reopen-not-tps-v1", "identity": m["identity"], "resources": resources, "binaries": {"node": node, "owner": owner, "replay": replay}, "inputs_sha256": pins, "plan_id": plan_id, "no_initialization": True, "maximum_new_transactions": p["MaxNewTransactions"], "maximum_source_transactions": p["MaxSourceTransactions"], "canonical_baseline_count": p["CanonicalNonceSum"], "log_directory": str(log_root), "started_unix": time.time(), "tps_measured": False, "capacity_5m_proven": False, "execution_budget": {"production_owner_gomaxprocs": 36, "replay_gomaxprocs": 4, "each_validator_gomaxprocs": 2, "total_go_parallelism": 54, "physical_core_exclusivity_claimed": False, "readonly_owner_audit_gomaxprocs": 2}}
        record("resume-intent.json", result)
        try:
            args = [owner["path"], "--genesis", m["genesis"], "--data-dir", m["owner_dir"], "--parallel-evm", "--bounded-ingress-plan", str(plan_path), "--bounded-ingress-source-proof", str(proof_path), "--bounded-ingress-source", m["supply"]["root"], "--bounded-ingress-manifest", str(Path(m["supply"]["root"]) / "manifest.json"), "--bounded-ingress-plan-id", plan_id]
            owner_process = spawn(args, "finite-owner")
            ready(sockets[:2], 120)
            if any(os.path.lexists(path) for path in sockets[2:]):
                raise ValueError("finite validators started before preload")
            command = spawn([replay["path"], "bounded-phase-preload", "--root", m["supply"]["root"], "--runtime-manifest", str(root / "run.json"), "--attempt", attempt, "--plan", str(plan_path), "--plan-sha256", plan_sha, "--expected-plan-id", plan_id, "--baseline-result", str(historical_path), "--baseline-sha256", p["RecoverySHA256"]], "finite-preload")
            wait_child(command, 180)
            candidate_dir = root / "strict-attempts" / attempt
            candidate_path = candidate_dir / "result-candidate.json"
            candidate = strict_fleet.metadata(candidate_path, 16 << 20)
            expected = preload_evidence(candidate, document, m, plan_sha, pins[str(root / "run.json")])
            totals = finite_journal_evidence(candidate_dir, candidate, document)
            finite_schedule_evidence(candidate, document, m)
            epoch = candidate["owner_initial"]["Epoch"]
            if epoch == historical["owner_final"]["Epoch"] or any(os.path.lexists(path) for path in sockets[2:]) or queue_fixture.owner_status(m) != candidate["owner_final"]:
                raise ValueError("finite preload epoch/session changed or validator started early")
            record("fully-acknowledged-before-production.json", {"candidate_sha256": hashlib.sha256(strict_fleet.read_private(candidate_path, 16 << 20)).hexdigest(), "schedule_sha256": candidate["schedule"]["schedule_sha256"], "totals": totals, "processes": [r for _, r in records], "validator_sockets_absent": True, "owner": candidate["owner_final"]})
            phase_baselines = phase_log_baselines(m)
            producers = []
            for v in m["validators"]:
                config = json.loads(configs[v["config"]], object_pairs_hook=fleet.pairs_unique)
                config["node"]["miner"] = True
                path = log_root / f"produce-v{v['index']}.json"
                record(path.name, config)
                producers.append(spawn([node["path"], "--config", str(path)], "produce-v" + str(v["index"])))
            ready(sockets[2:], 25)
            initial = statuses()
            for status in initial:
                validator_binding(status, m, epoch, p["ManifestSHA256"])
                if status["committed_executed_transactions"] < p["CanonicalNonceSum"]:
                    raise ValueError("finite initial reference counter regressed")
            drained = finite_drain(m, p, candidate, expected, live, guard, deadline, initial[0], record)
            result.update(preload_result=str(candidate_path), preload_sha256=hashlib.sha256(strict_fleet.read_private(candidate_path, 16 << 20)).hexdigest(), schedule_sha256=candidate["schedule"]["schedule_sha256"], acknowledged_new_transactions=totals["new"], canonical_skipped=totals["skip"], target_count=expected, drained=drained)
            # Stop all producers first. The owner stays alive while a stable
            # full account/root snapshot is captured; moving seven-head equality
            # is never required while these finite blocks are being drained.
            stop_phase("production-validator-lifecycle.json", producers)
            guard(True)
            if any(os.path.lexists(path) for path in sockets[2:]):
                raise ValueError("producer socket survived clean stop")
            phase_capture_attempted = True
            phase_segments = capture_phase_segments(m, phase_baselines, log_root)
            phase_result = phase_evidence(phase_segments, m, p["Canonical"]["Number"])
            record("producer-phase-evidence.json", phase_result)
            stable_candidate = dict(candidate, owner_final=drained["owner"], reference_final=drained["reference"])
            stable = stable_owner_snapshot(m, p, historical, stable_candidate, guard)
            result["phase_evidence"] = phase_result
            current, root_read = stable["owner"], stable["canonical_roots"]
            result.update(before_stop_owner=current, before_stop_roots=root_read["roots"], stable_snapshot=stable)
            record("stable-before-owner-stop.json", stable)
            stop_phase("production-owner-lifecycle.json", [owner_process])
            record("production-process-lifecycle.json", {"cleanup_clean": True, "processes": [r for _, r in records if r["name"] == "finite-owner" or r["name"].startswith("produce-v")]})
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
            result["canonical_nonce_sum"] = bounded_reopened_evidence(reopened, historical, candidate, p, m, failed, failed_path, failed_sha, stable)
            result.update(recovery_result=str(recovery_path), recovery_sha256=hashlib.sha256(strict_fleet.read_private(recovery_path, 16 << 20)).hexdigest(), account_vector_sha256=reopened["account_vector_sha256"], finished_unix=time.time())
            stop_phase("reopen-process-lifecycle.json")
            no_sockets()
            guard(True)
            pin_inputs()
            record("finite-fleet-observation.json", result)
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
            if phase_baselines is not None and not phase_capture_attempted:
                phase_capture_attempted = True
                try:
                    segments = capture_phase_segments(m, phase_baselines, log_root)
                    record("failed-production-phase-segments.json", {"segments": segments, "tps_measured": False})
                except BaseException as error:
                    if cleanup_error is None:
                        cleanup_error = error
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
    record("finite-fleet-result.json", result)
    return result


def main():
    import argparse
    import os
    import signal
    import subprocess
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
        bounded_plan_inputs(args.plan, args.plan_sha256, args.expected_plan_id, args.historical_recovery, m)
        if args.run:
            if not args.attempt or any(path is None for path in (args.node_binary, args.owner_binary, args.replay_binary)):
                raise ValueError("actual bounded run requires explicit binaries and new attempt")
            value = run(m, fleet.binary(args.node_binary), fleet.binary(args.owner_binary), fleet.binary(args.replay_binary), args.attempt, args.plan, args.plan_sha256, args.expected_plan_id, args.source_proof, args.historical_recovery, args.failed_baseline)
            print(json.dumps({"result": str(Path(value["log_directory"]) / "finite-fleet-result.json"), "kind": value["kind"]}))
        else:
            if args.attempt or args.node_binary or args.owner_binary or args.replay_binary:
                raise ValueError("execution options require --run")
            print(json.dumps({"kind": "finite-phase-diagnostic-validation-only-not-tps", "plan_id": args.expected_plan_id}))
        return 0
    except (OSError, ValueError, TypeError, KeyError, subprocess.SubprocessError) as error:
        print(json.dumps({"error": str(error)}))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
