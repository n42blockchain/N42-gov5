#!/usr/bin/env python3
"""Finish one retained sealed child without fresh builds, then prove two reopens.

No transaction dispatch, plan arming, marker repair, rollback or TPS measurement.
"""
import hashlib
import json
from pathlib import Path

import fleet
import bounded_fleet
import finite_fleet
import recovery_fleet
import resume_fleet
import strict_fleet
import queue_fixture
import post_finite_audit

failed_finite_inputs = post_finite_audit.failed_finite_inputs
first_audit_evidence = post_finite_audit.first_audit_evidence
second_audit_evidence = post_finite_audit.second_audit_evidence

def recovery_binding(owner, m, manifest, epoch):
    if owner["Identity"] != m["identity"] or owner["Epoch"] != epoch or owner["ManifestHash"] != manifest or owner["Reconciling"] or any(owner[k] != 0 for k in ("NewlyAdmitted", "CryptoVerified", "PendingTransactions", "ReservedTransactions", "RetainedEncodedBytes", "ReservedEncodedBytes")):
        raise ValueError("quiescent owner is not a new empty canonical session")

def recovery_validator(value, m, epoch, manifest, target_number, target_hash, count, converged=False):
    finite_fleet.validator_binding(value,m,epoch,manifest)
    if value.get("recovery_only") is not True or value.get("drain_acknowledged") is not True or type(value.get("fresh_builds",0)) is not int or value.get("fresh_builds",0)!=0 or value["committed_executed_transactions"]!=count:
        raise ValueError("recovery-only ACK/fresh-build/count fence differs")
    if converged and (value["number"]!=target_number or value["block_hash"]!=target_hash):
        raise ValueError("retained child has not canonically converged")

def acknowledge_recovery(m, epoch, manifest, guard):
    requests=[]
    for v in m["validators"]:
        guard(True)
        client=fleet.UnixHTTP(v["api_socket"],timeout=33)
        try:
            request={"identity":m["identity"],"epoch":epoch,"manifest_hash":manifest}
            client.request("POST","/v1/recovery-only",json.dumps(request),{"Content-Type":"application/json"})
            response=client.getresponse();raw=response.read(4097)
            if response.status!=200 or len(raw)>4096:
                raise ValueError("recovery-only validator did not ACK")
            value=json.loads(raw,object_pairs_hook=fleet.pairs_unique)
            if any(value.get(k)!=x for k,x in request.items()) or value.get("recovery_only") is not True or value.get("drain_acknowledged") is not True or type(value.get("fresh_builds")) is not int or value["fresh_builds"]!=0:
                raise ValueError("recovery-only acknowledgement binding/counter differs")
            requests.append({"validator":v["index"],"ack":value})
        finally:
            client.close();guard(True)
    return requests

def capture_snapshot(m,p,historical,admission,epoch,target_number,target_hash,guard,require_validators):
    import time
    count=p["CanonicalNonceSum"]+sum(admission["sender_new_transactions"])
    addresses=sorted(a["address"] for a in historical["accounts"])
    end=time.monotonic()+45
    while time.monotonic()<end:
        guard(True)
        peers=[]
        if require_validators:
            for v in m["validators"]:
                item=fleet.status(v["api_socket"],m["identity"]);guard(True)
                recovery_validator(item,m,epoch,p["ManifestSHA256"],target_number,target_hash,count)
                peers.append(item)
            if any(v["number"]!=target_number or v["block_hash"]!=target_hash for v in peers):
                time.sleep(.1);continue
        owner=queue_fixture.owner_status(m);guard(True)
        if owner["Identity"]!=m["identity"] or owner["Epoch"]!=epoch or owner["ManifestHash"]!=p["ManifestSHA256"] or any(owner[k]!=0 for k in ("NewlyAdmitted","CryptoVerified","PendingTransactions","ReservedTransactions","RetainedEncodedBytes","ReservedEncodedBytes")):
            raise ValueError("quiescent owner identity/empty-session changed")
        if owner["Reconciling"] or owner["CanonicalNumber"]!=target_number or owner["CanonicalHash"]!=target_hash:
            time.sleep(.1);continue
        accounts=[];roots=None
        try:
            for start in range(0,len(addresses),1000):
                response=bounded_fleet.canonical_account_request(m,owner,addresses[start:start+1000]);guard(True)
                if roots is not None and roots!=response["roots"]:
                    raise ValueError("quiescent root changed across account batches")
                roots=response["roots"];accounts.extend(response["accounts"])
            final=bounded_fleet.canonical_account_request(m,owner,addresses[:1]);guard(True)
            if final["roots"]!=roots:
                raise ValueError("quiescent final root changed")
            if queue_fixture.owner_status(m)!=owner:
                guard(True);time.sleep(.1);continue
            if require_validators:
                peers=[]
                for v in m["validators"]:
                    item=fleet.status(v["api_socket"],m["identity"]);guard(True)
                    recovery_validator(item,m,epoch,p["ManifestSHA256"],target_number,target_hash,count,True)
                    peers.append(item)
            digest=bounded_fleet.sender_delta_evidence(accounts,historical,p,admission["sender_new_transactions"])
            if sum(a["nonce"] for a in accounts)!=count:
                raise ValueError("quiescent complete nonce sum differs")
            recovery_binding(owner,m,p["ManifestSHA256"],epoch)
            return {"owner":owner,"accounts":accounts,"canonical_nonce_sum":count,"account_vector_sha256":digest,"canonical_roots":{"canonical_hash":target_hash,"canonical_number":target_number,"roots":roots,"account_batches":(len(accounts)+999)//1000,"final_read":True},"validators":peers}
        except bounded_fleet.SnapshotRetry:
            time.sleep(.1)
    raise TimeoutError("bounded recovery-only canonical/applied convergence unavailable")

def exact_snapshot(after,before):
    for key in ("owner","accounts","canonical_nonce_sum","account_vector_sha256","canonical_roots"):
        if after[key]!=before[key]:
            raise ValueError("late import or stop changed quiescent full snapshot")


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

def run(m, node, owner, replay, attempt, plan_path, plan_sha, plan_id, historical_path, failed_path, admission_path, admission_sha, failure_path, failure_sha, drain_path, drain_sha, phase_path, phase_sha, target_number, target_hash):
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
    admission, totals = failed_finite_inputs(admission_path, admission_sha, failure_path, failure_sha, drain_path, drain_sha, phase_path, phase_sha, document, historical, m, plan_sha)
    if type(target_number) is not int or target_number!=admission["reference_final"]["number"]+1 or not strict_fleet.valid_hash(target_hash):
        raise ValueError("explicit next retained-child target required")
    failed, failed_sha = recovery_fleet.baseline(failed_path, m)
    input_paths = (plan_path, admission_path, failure_path, drain_path, phase_path, historical_path, failed_path, root / "run.json", m["genesis"], Path(m["supply"]["root"]) / "manifest.json", Path(m["supply"]["root"]) / "genesis.json")
    pins = {str(path): hashlib.sha256(strict_fleet.read_private(Path(path), 16 << 20)).hexdigest() for path in input_paths}
    for shard in range(64):
        ack_path = Path(admission_path).parent / f"ack-shard-{shard:02d}.jsonl"
        pins[str(ack_path)] = hashlib.sha256(bounded_fleet.read_journal(ack_path)).hexdigest()
    for segment in strict_fleet.metadata(Path(phase_path), 16 << 20)["segments"]:
        path = Path(segment["path"])
        pins[str(path)] = hashlib.sha256(bounded_fleet.read_journal(path)).hexdigest()
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
        result = {"kind": "gov5-retained-child-recovery-only-double-reopen-v1", "identity": m["identity"], "resources": resources, "binaries": {"node": node, "owner": owner, "replay": replay}, "inputs_sha256": pins, "plan_id": plan_id, "no_initialization": True, "producer_recovery_only_then_disabled_configuration_copies": True, "retained_target_number":target_number, "retained_target_hash":target_hash, "dispatched_transactions": 0, "known_prior_new_transactions": totals["new"], "known_prior_canonical_skipped": totals["skip"], "minimum_canonical_count": admission["reference_final"]["committed_executed_transactions"], "maximum_canonical_count": p["CanonicalNonceSum"] + totals["new"], "canonical_baseline_count": p["CanonicalNonceSum"], "log_directory": str(log_root), "started_unix": time.time(), "tps_measured": False, "capacity_5m_proven": False, "production_snapshot_reconstructed": False}
        record("quiescent-recovery-intent.json", result)
        try:
            spawn([owner["path"],"--genesis",m["genesis"],"--data-dir",m["owner_dir"],"--parallel-evm","--strict-ingress-manifest",str(Path(m["supply"]["root"])/"manifest.json")],"recovery-owner")
            ready(sockets[:2],45)
            owner_initial=queue_fixture.owner_status(m);guard(True)
            epoch=owner_initial["Epoch"]
            if owner_initial["Identity"]!=m["identity"] or owner_initial["ManifestHash"]!=p["ManifestSHA256"] or epoch==admission["owner_initial"]["Epoch"] or not any(epoch) or any(owner_initial[k]!=0 for k in ("NewlyAdmitted","CryptoVerified","PendingTransactions","ReservedTransactions","RetainedEncodedBytes","ReservedEncodedBytes")):
                raise ValueError("recovery startup session binding differs")
            record("recovery-owner-initial.json",owner_initial)
            for validator in m["validators"]:
                config=json.loads(configs[validator["config"]],object_pairs_hook=fleet.pairs_unique)
                config["node"]["miner"]=True
                config["node"]["performance_recovery_only"]=True
                path=log_root/("recovery-v"+str(validator["index"])+".json")
                record(path.name,config)
                spawn([node["path"],"--config",str(path)],"recovery-v"+str(validator["index"]))
            ready(sockets[2:],45)
            acks=acknowledge_recovery(m,epoch,p["ManifestSHA256"],guard)
            record("recovery-only-acknowledgements.json",{"passed":True,"acknowledgements":acks,"fresh_builds":0})
            converged=capture_snapshot(m,p,historical,admission,epoch,target_number,target_hash,guard,True)
            record("live-converged-snapshot.json",converged)
            owner_process=children[0]
            # Stop only the seven validators. The live owner remains available
            # for the post-stop snapshot which excludes late durable imports.
            children.remove(owner_process)
            try:
                stop_phase("recovery-validator-lifecycle.json")
            finally:
                children.insert(0,owner_process)
            stopped=capture_snapshot(m,p,historical,admission,epoch,target_number,target_hash,guard,False)
            exact_snapshot(stopped,converged)
            record("post-validator-stop-snapshot.json",stopped)
            stop_phase("recovery-owner-lifecycle.json")
            no_sockets();guard(True)
            for phase, audit_attempt in (("first", attempt), ("second", reopen_attempt)):
                spawn([owner["path"], "--genesis", m["genesis"], "--data-dir", m["owner_dir"], "--parallel-evm", "--strict-ingress-manifest", str(Path(m["supply"]["root"]) / "manifest.json")], phase + "-owner")
                ready(sockets[:2], 45)
                for validator in m["validators"]:
                    config = json.loads(configs[validator["config"]], object_pairs_hook=fleet.pairs_unique)
                    config["node"]["miner"] = False
                    config["node"].pop("performance_recovery_only",None)
                    path = log_root / (phase + "-v" + str(validator["index"]) + ".json")
                    record(path.name, config)
                    spawn([node["path"], "--config", str(path)], phase + "-v" + str(validator["index"]))
                ready(sockets[2:], 45)
                audit = spawn([replay["path"], "recover", "--root", m["supply"]["root"], "--runtime-manifest", str(root / "run.json"), "--attempt", audit_attempt, "--baseline-result", str(failed_path), "--require-canonical-roots"], phase + "-audit")
                wait_child(audit, 60)
                recovery_path = root / "strict-attempts" / audit_attempt / "recovery.json"
                observed = strict_fleet.metadata(recovery_path, 16 << 20)
                if observed["accounts"]!=converged["accounts"] or observed["account_vector_sha256"]!=converged["account_vector_sha256"] or observed["canonical_roots"]!=converged["canonical_roots"] or observed["owner_final"]["CanonicalHash"]!=target_hash or observed["owner_final"]["CanonicalNumber"]!=target_number or observed["owner_initial"]["Epoch"]==epoch:
                    raise ValueError("physical reopen changed live converged snapshot or reused epoch")
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
            record("quiescent-recovery-observation.json", result)
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
    record("quiescent-recovery-result.json", result)
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
    parser.add_argument("--drain-result", type=Path, required=True)
    parser.add_argument("--drain-sha256", required=True)
    parser.add_argument("--phase-result", type=Path, required=True)
    parser.add_argument("--phase-sha256", required=True)
    parser.add_argument("--historical-recovery", type=Path, required=True)
    parser.add_argument("--failed-baseline", type=Path, required=True)
    parser.add_argument("--retained-target-number",type=int,required=True)
    parser.add_argument("--retained-target-hash",required=True)
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
            value = run(m, fleet.binary(args.node_binary), fleet.binary(args.owner_binary), fleet.binary(args.replay_binary), args.attempt, args.plan, args.plan_sha256, args.expected_plan_id, args.historical_recovery, args.failed_baseline, args.admission_result, args.admission_sha256, args.failure_acceptance, args.failure_acceptance_sha256, args.drain_result, args.drain_sha256, args.phase_result, args.phase_sha256, args.retained_target_number, args.retained_target_hash)
            print(json.dumps({"result": str(Path(value["log_directory"]) / "quiescent-recovery-result.json"), "kind": value["kind"]}))
        else:
            if args.attempt or args.node_binary or args.owner_binary or args.replay_binary:
                raise ValueError("execution options require --run")
            print(json.dumps({"kind": "post-finite-double-reopen-validation-only", "plan_id": args.expected_plan_id}))
        return 0
    except (OSError, ValueError, TypeError, KeyError, subprocess.SubprocessError) as error:
        print(json.dumps({"error": str(error)}))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
