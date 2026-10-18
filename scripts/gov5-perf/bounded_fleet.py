#!/usr/bin/env python3
"""Explicit bounded GSR2 20-second canonical observation and same-data reopen.

The 60M original prefix cannot establish 5M sustained TPS for 20 seconds. Source
exhaustion, unknown ACK or any runtime failure preserves this attempt and stops.
"""
import hashlib
import json
import math
from pathlib import Path

import fleet
import queue_fixture
import recovery_fleet
import resume_fleet
import strict_fleet

_live_group = resume_fleet._live_group
released_claim_evidence = resume_fleet.released_claim_evidence
SnapshotRetry = resume_fleet.SnapshotRetry


def bounded_plan_inputs(path, expected_sha, expected_id, recovery_path, m):
    raw = strict_fleet.read_private(Path(path), 16 << 20)
    if hashlib.sha256(raw).hexdigest() != expected_sha:
        raise ValueError("fixed bounded plan bytes changed")
    doc = json.loads(raw, object_pairs_hook=fleet.pairs_unique)
    if set(doc) != {"kind", "plan", "plan_id"} or doc["kind"] != "gov5-bounded-resume-prefix-plan-v2" or doc["plan_id"] != expected_id:
        raise ValueError("explicit bounded plan v2 required")
    p = doc["plan"]
    if p["Version"] != 2 or p["Identity"] != m["identity"] or p["ManifestSHA256"] != "0x" + m["supply"]["manifest_sha256"] or p["CorpusID"] != m["supply"]["corpus_id"] or p["GenesisFileSHA256"] != "0x" + m["genesis_sha256"]:
        raise ValueError("bounded plan source identity differs")
    if p["NonceEndExclusive"] != 10000 or p["MaxSourceFrames"] != 120000 or p["MaxSourceTransactions"] != 60000000 or p["MaxSourceEncodedBytes"] != 6370080000 or len(p["Senders"]) != 6000:
        raise ValueError("bounded production requires exact 6000-sender original 60M prefix")
    source = strict_fleet.metadata(Path(m["supply"]["root"]) / "manifest.json", 1 << 20)["descriptor"]
    if (source["sender_count"], source["nonces_per_sender"], source["shard_count"], source["frame_transactions"], source["nonce_start"]) != (6000, 32000, 64, 500, 0):
        raise ValueError("original production source geometry differs")
    raw = strict_fleet.read_private(Path(recovery_path), 16 << 20)
    if "0x" + hashlib.sha256(raw).hexdigest() != p["RecoverySHA256"]:
        raise ValueError("bounded historical vector report changed")
    historical = json.loads(raw, object_pairs_hook=fleet.pairs_unique)
    accounts = historical["accounts"]
    if historical["passed"] is not True or historical.get("error") or historical["dispatched_transactions"] != 0 or historical["identity"] != p["Identity"] or historical["canonical_nonce_sum"] != p["CanonicalNonceSum"] or historical["account_vector_sha256"] != p["AccountVectorSHA256"]:
        raise ValueError("bounded historical audit differs from plan")
    if "0x" + hashlib.sha256(json.dumps(accounts, separators=(",", ":")).encode()).hexdigest() != p["AccountVectorSHA256"]:
        raise ValueError("bounded historical vector digest differs")
    by_address = {a["address"]: a for a in accounts}
    addresses = [a["address"] for a in accounts]
    if len(by_address) != 6000 or addresses != sorted(addresses) or sum(a["nonce"] for a in accounts) != p["CanonicalNonceSum"] or p["MaxNewTransactions"] != 60000000-p["CanonicalNonceSum"]:
        raise ValueError("bounded complete historical nonce vector/cap differs")
    for index, sender in enumerate(p["Senders"]):
        if sender["Index"] != index or sender["Address"] not in by_address or type(sender["Nonce"]) is not int or not 0 <= sender["Nonce"] <= 10000 or sender["Nonce"] != by_address[sender["Address"]]["nonce"]:
            raise ValueError("bounded logical sender mapping differs from account vector")
    if len({s["Address"] for s in p["Senders"]}) != 6000 or type(p["SourceEncodedBytes"]) is not int or not 0 < p["SourceEncodedBytes"] <= p["MaxSourceEncodedBytes"]:
        raise ValueError("bounded sender bijection or encoded cap invalid")
    roots = historical.get("canonical_roots")
    if roots is None or roots["roots"] != {"root": p["Canonical"]["Root"], "applied_root": p["Canonical"]["Root"]} or roots["canonical_hash"] != p["Canonical"]["Hash"] or roots["canonical_number"] != p["Canonical"]["Number"] or roots["final_read"] is not True or roots["account_batches"] != 6:
        raise ValueError("bounded historical complete canonical root proof differs")
    return doc, historical


def source_ready_evidence(value, document, m):
    p = document["plan"]
    if value.get("kind") != "gov5-bounded-source-ready-before-production-v2" or value.get("plan_id") != document["plan_id"] or value.get("full_source_integrity_verified") is not True or type(value.get("dispatched_transactions")) is not int or value["dispatched_transactions"] != 0:
        raise ValueError("bounded source-ready evidence differs")
    owner, epoch = value["owner"], value["epoch"]
    if not isinstance(epoch, list) or len(epoch) != 16 or not any(epoch) or any(type(v) is not int or not 0 <= v <= 255 for v in epoch):
        raise ValueError("bounded new nonzero epoch required")
    if owner["Identity"] != m["identity"] or owner["Epoch"] != epoch or owner["ManifestHash"] != p["ManifestSHA256"] or owner["CanonicalHash"] != p["Canonical"]["Hash"] or owner["CanonicalNumber"] != p["Canonical"]["Number"] or owner["Reconciling"] or any(type(owner[k]) is not int or owner[k] != 0 for k in ("NewlyAdmitted", "CryptoVerified", "PendingTransactions", "ReservedTransactions", "RetainedEncodedBytes", "ReservedEncodedBytes")):
        raise ValueError("bounded source-ready owner not empty at exact baseline")
    return epoch


def canonical_nonregressing(current, previous):
    return current["identity"] == previous["identity"] and current["number"] >= previous["number"] and current["committed_executed_transactions"] >= previous["committed_executed_transactions"] and (current["number"] != previous["number"] or (current["block_hash"] == previous["block_hash"] and current["committed_executed_transactions"] == previous["committed_executed_transactions"]))


def validator_binding(status, m, epoch, manifest):
    queue = status.get("owner_queue")
    if status.get("identity") != m["identity"] or status.get("producer_running") is not True or not isinstance(queue, dict) or queue.get("epoch") != epoch or queue.get("manifest_hash") != manifest or queue.get("max_transactions") != 163000 or queue.get("max_encoded_bytes") != 32 << 20:
        raise ValueError("bounded canonical observation validator/session/block limits differ")


def window_evidence(value, document, m, plan_sha, runtime_sha, offered):
    p = document["plan"]
    if value.get("kind") != "gov5-bounded-resume-20s-window-and-drain-candidate-not-capacity-v2" or value.get("passed") is not True or value.get("error") or value["identity"] != m["identity"] or value["plan_id"] != document["plan_id"] or value["plan_sha256"] != "0x" + plan_sha or value["runtime_sha256"] != "0x" + runtime_sha or value["runtime_manifest"] != str(Path(m["root"]) / "run.json") or value["offered_tps"] != offered or value["target_warmup_seconds"] != 4 or value["actual_warmup_seconds"] < 4 or value["full_source_integrity_verified"] is not True or value["physical_reopen_included"] is not False or value["capacity_5m_proven"] is not False or value["corpus_available_through_window"] is not True:
        raise ValueError("bounded window candidate identity/boundary differs")
    begin, end = value["window_start"], value["window_end"]
    epoch = value["owner_initial"]["Epoch"]
    if len(value.get("initial", [])) != 7:
        raise ValueError("bounded seven initial validator observations required")
    for status in value["initial"]:
        validator_binding(status, m, epoch, p["ManifestSHA256"])
        if status["committed_executed_transactions"] != p["CanonicalNonceSum"] or status["number"] < p["Canonical"]["Number"] or (status["number"] == p["Canonical"]["Number"] and status["block_hash"] != p["Canonical"]["Hash"]):
            raise ValueError("bounded initial canonical baseline differs")
    validator_binding(value["reference_final"], m, epoch, p["ManifestSHA256"])
    ordered = sorted([begin, end, *value["samples"]], key=lambda o: o["request_start_monotonic_ns"])
    previous = value["initial"][0]
    previous_end = 0
    for observation in ordered:
        validator_binding(observation["single_validator_canonical"], m, epoch, p["ManifestSHA256"])
        if observation["request_start_monotonic_ns"] < previous_end or not canonical_nonregressing(observation["single_validator_canonical"], previous):
            raise ValueError("bounded reference samples overlap or regress")
        previous, previous_end = observation["single_validator_canonical"], observation["response_end_monotonic_ns"]
    if value["actual_warmup_seconds"] != begin["request_start_monotonic_ns"]/1e9:
        raise ValueError("bounded actual warmup differs from first endpoint")
    for observation in (begin, end, *value["samples"]):
        if type(observation["request_start_monotonic_ns"]) is not int or type(observation["response_end_monotonic_ns"]) is not int or not 0 <= observation["request_start_monotonic_ns"] <= observation["response_end_monotonic_ns"]:
            raise ValueError("bounded RPC monotonic bracket invalid")
    if end["request_start_monotonic_ns"]-begin["response_end_monotonic_ns"] < 20000000000 or not canonical_nonregressing(end["single_validator_canonical"], begin["single_validator_canonical"]):
        raise ValueError("bounded complete 20-second canonical bracket required")
    elapsed = (end["response_end_monotonic_ns"]-begin["request_start_monotonic_ns"])/1e9
    delta = end["single_validator_canonical"]["committed_executed_transactions"]-begin["single_validator_canonical"]["committed_executed_transactions"]
    if value["canonical_window_delta"] != delta or value["actual_window_seconds"] != elapsed or not math.isfinite(value["canonical_committed_executed_tps"]) or not math.isclose(value["canonical_committed_executed_tps"], delta/elapsed, rel_tol=1e-12):
        raise ValueError("bounded TPS not from frozen conservative canonical endpoints")
    stream = value["stream_final"]
    first_exhausted = stream["first_observed_shard_exhaustion_seconds"]
    if first_exhausted != 0 and first_exhausted*1e9 <= end["response_end_monotonic_ns"]:
        raise ValueError("bounded source exhausted before end response")
    owner = value["owner_final"]
    if owner["Epoch"] != value["owner_initial"]["Epoch"] or owner["Identity"] != m["identity"] or owner["ManifestHash"] != p["ManifestSHA256"] or owner["NewlyAdmitted"] != stream["acknowledged_unique_admitted"] or owner["Reconciling"] or any(owner[k] != 0 for k in ("PendingTransactions", "ReservedTransactions", "RetainedEncodedBytes", "ReservedEncodedBytes")):
        raise ValueError("bounded final owner credits/session differ")
    minimum_crypto = stream["acknowledged_original_frames"] * 500
    if type(owner["CryptoVerified"]) is not int or not minimum_crypto <= owner["CryptoVerified"] <= minimum_crypto + 500*(stream["busy_retries"]+stream["snapshot_retries"]):
        raise ValueError("bounded online signature attempts differ from known frames/retries")
    expected = p["CanonicalNonceSum"]+stream["acknowledged_unique_admitted"]
    if value["drained_canonical_count"] != expected or value["reference_final"]["committed_executed_transactions"] != expected or not canonical_nonregressing(value["reference_final"], end["single_validator_canonical"]):
        raise ValueError("bounded drain count differs from known new admissions")
    if len(value["sender_new_transactions"]) != len(p["Senders"]) or any(type(n) is not int or n < 0 for n in value["sender_new_transactions"]) or sum(value["sender_new_transactions"]) != stream["acknowledged_unique_admitted"]:
        raise ValueError("bounded per-sender known admission total differs")
    if value["source_files_before"] != value["source_files_after"]:
        raise ValueError("bounded original source metadata changed")
    return expected


def read_journal(path):
    import os
    import stat
    fleet.private(path)
    before = path.lstat()
    identity = lambda v: (v.st_dev, v.st_ino, v.st_mode, v.st_uid, v.st_nlink, v.st_size, v.st_mtime_ns, v.st_ctime_ns)
    if before.st_size > 32 << 20 or before.st_nlink != 1:
        raise ValueError("bounded receipt journal exceeded size/link limit")
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as source:
        opened = os.fstat(source.fileno())
        if identity(opened) != identity(before) or not stat.S_ISREG(opened.st_mode):
            raise ValueError("bounded receipt journal changed at open")
        raw = source.read((32 << 20)+1)
        after = os.fstat(source.fileno())
    if identity(after) != identity(before) or identity(path.lstat()) != identity(before) or len(raw) != before.st_size:
        raise ValueError("bounded receipt journal changed during read")
    return raw


def journal_evidence(directory, value, document):
    p = document["plan"]
    totals = {"frames": 0, "original": 0, "bytes": 0, "new": 0, "skip": 0}
    sender_new = [0] * len(p["Senders"])
    progress = value["consumed_prefixes"]
    if len(progress) != 64:
        raise ValueError("bounded complete shard progress required")
    for shard in range(64):
        raw = read_journal(Path(directory) / f"ack-shard-{shard:02d}.jsonl")
        lines = raw.splitlines()
        per_round = (len(sender_new)-1-shard)//64+1
        admitted = 0
        previous_life = None
        for seq, line in enumerate(lines):
            receipt = json.loads(line, object_pairs_hook=fleet.pairs_unique)
            frame, ack = receipt["frame"], receipt["ack"]
            index, first = shard+(seq % per_round)*64, (seq//per_round)*500
            sender = p["Senders"][index]
            skip = min(500, max(0, sender["Nonce"]-first))
            if frame["ID"] != {"CorpusID": p["CorpusID"], "Shard": shard, "Sequence": seq} or frame["SenderIndex"] != index or frame["Address"] != sender["Address"] or frame["FirstNonce"] != first or first >= p["NonceEndExclusive"] or frame["Transactions"] != 500 or frame["CanonicalSkipped"] != skip or frame["NewlyAdmitted"] != 500-skip or type(frame["PayloadBytes"]) is not int or not 2500 <= frame["PayloadBytes"] <= 53000 or not strict_fleet.valid_hash(frame["PayloadHash"]):
                raise ValueError("bounded journal original whitelist/range differs")
            if ack["PlanID"] != document["plan_id"] or ack["Status"]["Epoch"] != value["owner_initial"]["Epoch"] or ack["Status"]["Identity"] != p["Identity"] or ack["Status"]["ManifestHash"] != p["ManifestSHA256"] or ack["Duplicate"] is not False or ack["NewlyAdmitted"] != 500-skip or ack["PriorAdmission"] != 500-skip or ack["CanonicalSkipped"] != skip:
                raise ValueError("bounded final journal ACK differs")
            life = ack["Lifetime"]
            if any(life[key] != cap for key, cap in (("MaxSourceFrames", p["MaxSourceFrames"]), ("MaxSourceTransactions", p["MaxSourceTransactions"]), ("MaxSourceEncodedBytes", p["SourceEncodedBytes"]), ("MaxNewTransactions", p["MaxNewTransactions"]))):
                raise ValueError("bounded ACK lifetime cap identity differs")
            if life["SourceTransactions"] != life["SourceFrames"]*500 or life["ReservedTransactions"] != life["ReservedFrames"]*500 or life["NewTransactions"] != ack["Status"]["NewlyAdmitted"] or life["ReservedNew"] != ack["Status"]["ReservedTransactions"]:
                raise ValueError("bounded ACK queue/lifetime accounting differs")
            if previous_life is not None and any(life[key] < previous_life[key] for key in ("SourceFrames", "SourceTransactions", "SourceEncodedBytes", "NewTransactions")):
                raise ValueError("bounded same-shard ACK lifetime regressed")
            previous_life = life
            for field, cap in (("SourceFrames", p["MaxSourceFrames"]), ("SourceTransactions", p["MaxSourceTransactions"]), ("SourceEncodedBytes", p["SourceEncodedBytes"]), ("NewTransactions", p["MaxNewTransactions"])):
                reserved = {"SourceFrames": "ReservedFrames", "SourceTransactions": "ReservedTransactions", "SourceEncodedBytes": "ReservedEncodedBytes", "NewTransactions": "ReservedNew"}[field]
                if type(life[field]) is not int or type(life[reserved]) is not int or not 0 <= life[field] <= cap or not 0 <= life[reserved] <= cap-life[field]:
                    raise ValueError("bounded owner lifetime cap exceeded")
            totals["frames"] += 1; totals["original"] += 500; totals["bytes"] += 84+frame["PayloadBytes"]; totals["new"] += 500-skip; totals["skip"] += skip
            sender_new[index] += 500-skip; admitted += 500-skip
        if progress[shard]["next_sequence"] != len(lines) or progress[shard]["admitted_transactions"] != admitted:
            raise ValueError("bounded journal length differs from consumed prefix")
    stream, budget = value["stream_final"], value["sender_lifetime_budget"]
    if totals["frames"] != stream["acknowledged_original_frames"] or totals["new"] != stream["acknowledged_unique_admitted"] or totals["skip"] != stream["acknowledged_canonical_skipped"] or sender_new != value["sender_new_transactions"] or budget != {"charged_original_frames": totals["frames"], "charged_original_transactions": totals["original"], "charged_original_encoded_bytes": totals["bytes"], "charged_new_transactions": totals["new"]}:
        raise ValueError("bounded shared sender lifetime differs from all known receipts")
    if totals["frames"] > p["MaxSourceFrames"] or totals["original"] > p["MaxSourceTransactions"] or totals["bytes"] > p["SourceEncodedBytes"] or totals["new"] > p["MaxNewTransactions"]:
        raise ValueError("bounded receipts exceeded sealed source/new caps")
    return totals


def sender_delta_evidence(accounts, historical, p, increments):
    old = {a["address"]: a for a in historical["accounts"]}
    by_address = {s["Address"]: increments[s["Index"]] for s in p["Senders"]}
    if len(accounts) != len(old) or [a["address"] for a in accounts] != sorted(old):
        raise ValueError("bounded stable complete address-ordered vector required")
    for account in accounts:
        previous, extra = old[account["address"]], by_address[account["address"]]
        if account["exists"] is not True or account["code_hash"] != previous["code_hash"] or type(account["nonce"]) is not int or account["nonce"] != previous["nonce"]+extra or account["nonce"] > p["NonceEndExclusive"] or account["balance"] != str(int(previous["balance"])-21001*extra):
            raise ValueError("bounded stable sender nonce/balance differs from known receipts")
    raw = json.dumps(accounts, separators=(",", ":")).encode()
    return "0x" + hashlib.sha256(raw).hexdigest()


def stable_owner_snapshot(m, p, historical, candidate, guard):
    import time
    addresses = sorted(a["address"] for a in historical["accounts"])
    deadline = time.monotonic()+15
    while time.monotonic() < deadline:
        guard(True)
        owner = queue_fixture.owner_status(m)
        guard(True)
        epoch = candidate["owner_initial"]["Epoch"]
        if owner["Identity"] != p["Identity"] or owner["Epoch"] != epoch or owner["ManifestHash"] != p["ManifestSHA256"] or owner["NewlyAdmitted"] != candidate["stream_final"]["acknowledged_unique_admitted"]:
            raise ValueError("bounded stable owner binding changed")
        for previous in (candidate["owner_final"], {"CanonicalHash": candidate["reference_final"]["block_hash"], "CanonicalNumber": candidate["reference_final"]["number"]}):
            if owner["CanonicalNumber"] < previous["CanonicalNumber"] or (owner["CanonicalNumber"] == previous["CanonicalNumber"] and owner["CanonicalHash"] != previous["CanonicalHash"]):
                raise ValueError("bounded stable canonical head regressed after producers stopped")
        if owner["Reconciling"] or any(owner[k] != 0 for k in ("PendingTransactions", "ReservedTransactions", "RetainedEncodedBytes", "ReservedEncodedBytes")):
            time.sleep(.1); continue
        accounts, roots = [], None
        try:
            for start in range(0, len(addresses), 1000):
                response = canonical_account_request(m, owner, addresses[start:start+1000])
                guard(True)
                if roots is not None and roots != response["roots"]:
                    raise ValueError("bounded stable root changed across batches")
                roots = response["roots"]; accounts.extend(response["accounts"])
            final = canonical_account_request(m, owner, addresses[:1]); guard(True)
            if final["roots"] != roots:
                raise ValueError("bounded final stable root changed")
            if queue_fixture.owner_status(m) != owner:
                guard(True); time.sleep(.1); continue
            digest = sender_delta_evidence(accounts, historical, p, candidate["sender_new_transactions"])
            expected = p["CanonicalNonceSum"]+candidate["stream_final"]["acknowledged_unique_admitted"]
            if sum(a["nonce"] for a in accounts) != expected:
                raise ValueError("bounded stable nonce sum differs from reference canonical count")
            return {"owner": owner, "accounts": accounts, "canonical_nonce_sum": expected, "account_vector_sha256": digest, "canonical_roots": {"canonical_hash": owner["CanonicalHash"], "canonical_number": owner["CanonicalNumber"], "roots": roots, "account_batches": (len(accounts)+999)//1000, "final_read": True}}
        except SnapshotRetry:
            time.sleep(.1)
    raise TimeoutError("bounded stable canonical/applied account snapshot unavailable after producers stopped")


def canonical_account_request(m, owner, addresses):
    client = fleet.UnixHTTP(m["owner_socket"], timeout=3)
    try:
        request = {"identity": m["identity"], "epoch": owner["Epoch"], "manifest_hash": owner["ManifestHash"], "canonical_hash": owner["CanonicalHash"], "canonical_number": owner["CanonicalNumber"], "addresses": addresses, "include_roots": True}
        client.request("POST", "/v1/ingress-accounts", json.dumps(request), {"Content-Type": "application/json"})
        response = client.getresponse(); raw = response.read((384 << 10)+1)
        if response.status == 409:
            raise SnapshotRetry("bounded canonical/applied snapshot changed")
        if response.status != 200 or len(raw) > 384 << 10:
            raise ValueError("bounded canonical account query unavailable")
        value = json.loads(raw, object_pairs_hook=fleet.pairs_unique)
        if value["identity"] != m["identity"] or value["epoch"] != owner["Epoch"] or value["manifest_hash"] != owner["ManifestHash"] or value["canonical_hash"] != owner["CanonicalHash"] or value["canonical_number"] != owner["CanonicalNumber"] or [a["address"] for a in value["accounts"]] != addresses:
            raise ValueError("bounded canonical account response binding differs")
        roots = value.get("roots")
        if roots is None or not strict_fleet.valid_hash(roots["root"]) or roots["root"] == "0x"+"00"*32 or roots["applied_root"] != roots["root"]:
            raise ValueError("bounded header and loaded QMDB roots differ")
        return value
    finally:
        client.close()


def bounded_reopened_evidence(value, historical, candidate, p, m, failed, failed_path, failed_sha, stable):
    count = recovery_fleet.evidence(value, m, failed, failed_path, failed_sha)
    if count != stable["canonical_nonce_sum"] or value["accounts"] != stable["accounts"] or value["account_vector_sha256"] != stable["account_vector_sha256"] or value["canonical_roots"] != stable["canonical_roots"]:
        raise ValueError("bounded physical reopen changed stable full vector/root")
    for owner in (value["owner_initial"], value["owner_final"]):
        if owner["Epoch"] == stable["owner"]["Epoch"] or owner["CanonicalHash"] != stable["owner"]["CanonicalHash"] or owner["CanonicalNumber"] != stable["owner"]["CanonicalNumber"]:
            raise ValueError("bounded physical reopen exact canonical tuple/new epoch differs")
    sender_delta_evidence(value["accounts"], historical, p, candidate["sender_new_transactions"])
    return count


def publish_handshake(path, value):
    import os
    with os.fdopen(os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600), "w") as out:
        json.dump(value, out); out.flush(); os.fsync(out.fileno())
    fd = os.open(path.parent, os.O_DIRECTORY | os.O_RDONLY)
    try: os.fsync(fd)
    finally: os.close(fd)
    fd = os.open(str(path)+".complete", os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
    try: os.fsync(fd)
    finally: os.close(fd)
    fd = os.open(path.parent, os.O_DIRECTORY | os.O_RDONLY)
    try: os.fsync(fd)
    finally: os.close(fd)


def ready_complete(path):
    fleet.private(path)
    info = path.lstat()
    if info.st_size != 0 or info.st_nlink != 1:
        raise ValueError("bounded ready completion marker invalid")


def run(m, node, owner, replay, attempt, plan_path, plan_sha, plan_id, proof_path, historical_path, failed_path, offered=6500000):
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
        gomax = 36 if name == "bounded-owner" else 4 if name == "bounded-window" else 2
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
        result = {"kind": "gov5-bounded-20s-canonical-window-physical-reopen-v2", "identity": m["identity"], "resources": resources, "binaries": {"node": node, "owner": owner, "replay": replay}, "inputs_sha256": pins, "plan_id": plan_id, "no_initialization": True, "maximum_new_transactions": p["MaxNewTransactions"], "maximum_source_transactions": p["MaxSourceTransactions"], "offered_tps": offered, "canonical_baseline_count": p["CanonicalNonceSum"], "log_directory": str(log_root), "started_unix": time.time(), "tps_measured": False, "capacity_5m_proven": False, "execution_budget": {"production_owner_gomaxprocs": 36, "replay_gomaxprocs": 4, "each_validator_gomaxprocs": 2, "total_go_parallelism": 54, "physical_core_exclusivity_claimed": False, "readonly_owner_audit_gomaxprocs": 2}}
        record("resume-intent.json", result)
        try:
            args = [owner["path"], "--genesis", m["genesis"], "--data-dir", m["owner_dir"], "--parallel-evm", "--bounded-ingress-plan", str(plan_path), "--bounded-ingress-source-proof", str(proof_path), "--bounded-ingress-source", m["supply"]["root"], "--bounded-ingress-manifest", str(Path(m["supply"]["root"]) / "manifest.json"), "--bounded-ingress-plan-id", plan_id]
            owner_process = spawn(args, "bounded-owner")
            ready(sockets[:2], 120)
            command = spawn([replay["path"], "bounded-window", "--root", m["supply"]["root"], "--runtime-manifest", str(root / "run.json"), "--attempt", attempt, "--plan", str(plan_path), "--plan-sha256", plan_sha, "--expected-plan-id", plan_id, "--baseline-result", str(historical_path), "--baseline-sha256", p["RecoverySHA256"], "--offered-tps", str(offered)], "bounded-window")
            candidate_dir = root / "strict-attempts" / attempt
            ready([candidate_dir / "source-ready.json.complete"], 120)
            ready_complete(candidate_dir / "source-ready.json.complete")
            source_ready = strict_fleet.metadata(candidate_dir / "source-ready.json")
            epoch = source_ready_evidence(source_ready, document, m)
            if epoch == historical["owner_final"]["Epoch"] or any(os.path.lexists(path) for path in sockets[2:]):
                raise ValueError("bounded source-ready epoch reused or validators started too early")
            record("source-ready-before-production.json", {"source_ready": source_ready, "processes": [r for _, r in records], "validator_sockets_absent": True})
            producers = []
            for v in m["validators"]:
                config = json.loads(configs[v["config"]], object_pairs_hook=fleet.pairs_unique)
                config["node"]["miner"] = True
                path = log_root / f"produce-v{v['index']}.json"
                record(path.name, config)
                producers.append(spawn([node["path"], "--config", str(path)], "produce-v" + str(v["index"])))
            ready(sockets[2:], 25)
            for status in statuses():
                if status["identity"] != m["identity"] or status["producer_running"] is not True or status["owner_queue"]["epoch"] != epoch or status["owner_queue"]["manifest_hash"] != p["ManifestSHA256"]:
                    raise ValueError("bounded validator production binding differs")
            marker = {"plan_id": plan_id, "epoch": epoch, "validators_ready": True}
            marker_path = candidate_dir / "production-ready.json"
            publish_handshake(marker_path, marker)
            wait_child(command, 120)
            candidate_path = candidate_dir / "result-candidate.json"
            candidate = strict_fleet.metadata(candidate_path, 16 << 20)
            window_evidence(candidate, document, m, plan_sha, pins[str(root / "run.json")], offered)
            journal_evidence(candidate_dir, candidate, document)
            if candidate["owner_initial"] != source_ready["owner"]:
                raise ValueError("bounded measurement changed initial owner baseline")
            result.update(tps_measured=True, window_result=str(candidate_path), window_sha256=hashlib.sha256(strict_fleet.read_private(candidate_path, 16 << 20)).hexdigest(), canonical_window_delta=candidate["canonical_window_delta"], actual_window_seconds=candidate["actual_window_seconds"], canonical_committed_executed_tps=candidate["canonical_committed_executed_tps"], target_count=p["CanonicalNonceSum"] + candidate["stream_final"]["acknowledged_unique_admitted"])
            # Stop all producers first. The owner stays alive while a stable
            # full account/root snapshot is captured; moving seven-head equality
            # is never required by the 20-second window or its drain.
            stop_phase("production-validator-lifecycle.json", producers)
            guard(True)
            if any(os.path.lexists(path) for path in sockets[2:]):
                raise ValueError("producer socket survived clean stop")
            stable = stable_owner_snapshot(m, p, historical, candidate, guard)
            current, root_read = stable["owner"], stable["canonical_roots"]
            result.update(before_stop_owner=current, before_stop_roots=root_read["roots"], stable_snapshot=stable)
            record("stable-before-owner-stop.json", stable)
            stop_phase("production-owner-lifecycle.json", [owner_process])
            record("production-process-lifecycle.json", {"cleanup_clean": True, "processes": [r for _, r in records if r["name"] == "bounded-owner" or r["name"].startswith("produce-v")]})
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
            record("bounded-fleet-observation.json", result)
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
    record("bounded-fleet-result.json", result)
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
    parser.add_argument("--offered-tps", type=int, default=6500000)
    args = parser.parse_args()
    def interrupted(signum, frame):
        raise KeyboardInterrupt("signal " + str(signum))
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    os.umask(0o077)
    try:
        if not 1 <= args.offered_tps <= 20000000:
            raise ValueError("explicit bounded offered rate 1..20M required")
        m = strict_fleet.validate(args.manifest)
        bounded_plan_inputs(args.plan, args.plan_sha256, args.expected_plan_id, args.historical_recovery, m)
        if args.run:
            if not args.attempt or any(path is None for path in (args.node_binary, args.owner_binary, args.replay_binary)):
                raise ValueError("actual bounded run requires explicit binaries and new attempt")
            value = run(m, fleet.binary(args.node_binary), fleet.binary(args.owner_binary), fleet.binary(args.replay_binary), args.attempt, args.plan, args.plan_sha256, args.expected_plan_id, args.source_proof, args.historical_recovery, args.failed_baseline, args.offered_tps)
            print(json.dumps({"result": str(Path(value["log_directory"]) / "bounded-fleet-result.json"), "kind": value["kind"]}))
        else:
            if args.attempt or args.node_binary or args.owner_binary or args.replay_binary:
                raise ValueError("execution options require --run")
            print(json.dumps({"kind": "bounded-resume-prefix-validation-only-not-tps", "plan_id": args.expected_plan_id}))
        return 0
    except (OSError, ValueError, TypeError, KeyError, subprocess.SubprocessError) as error:
        print(json.dumps({"error": str(error)}))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
