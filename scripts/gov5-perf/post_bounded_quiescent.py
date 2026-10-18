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
import post_bounded_audit

failed_window_inputs = post_bounded_audit.failed_window_inputs
first_audit_evidence = post_bounded_audit.first_audit_evidence
second_audit_evidence = post_bounded_audit.second_audit_evidence

def recovery_binding(owner, m, manifest, epoch):
    if owner["Identity"] != m["identity"] or owner["Epoch"] != epoch or owner["ManifestHash"] != manifest or owner["Reconciling"] or any(owner[k] != 0 for k in ("NewlyAdmitted", "CryptoVerified", "PendingTransactions", "ReservedTransactions", "RetainedEncodedBytes", "ReservedEncodedBytes")):
        raise ValueError("quiescent owner is not a new empty canonical session")

def recovery_validator(value, m, epoch, manifest, retained, previous=None, converged=False):
    finite_fleet.validator_binding(value,m,epoch,manifest)
    if value.get("recovery_only") is not True or value.get("drain_acknowledged") is not True or type(value.get("fresh_builds",0)) is not int or value.get("fresh_builds",0)!=0:
        raise ValueError("recovery-only ACK/fresh-build fence differs")
    current=(value["number"],value["block_hash"],value["committed_executed_transactions"])
    floor=retained["floor"];target=retained["target"]
    allowed=[(floor["number"],floor["hash"],floor["count"]),(target["number"],target["hash"],retained["expected_count"])]
    if current not in allowed or (converged and current!=allowed[1]):
        raise ValueError("recovery-only canonical tuple outside exact retained transition")
    if previous is not None and not bounded_fleet.canonical_nonregressing(value,previous):
        raise ValueError("recovery-only peer regressed")


def retained_inputs(path,digest,admission,m):
    import re
    raw=strict_fleet.read_private(Path(path),16<<20)
    if hashlib.sha256(raw).hexdigest()!=digest:
        raise ValueError("retained evidence changed")
    evidence=json.loads(raw,object_pairs_hook=fleet.pairs_unique)
    if evidence.get("kind")!="gov5-post-bounded-retained-child-evidence-v1":
        raise ValueError("explicit retained-child evidence required")
    failure_path=Path(evidence["ordinary_failure_path"])
    raw=strict_fleet.read_private(failure_path,16<<20)
    if hashlib.sha256(raw).hexdigest()!=evidence["ordinary_failure_sha256"]:
        raise ValueError("ordinary failure seal changed")
    failure=json.loads(raw,object_pairs_hook=fleet.pairs_unique)
    reference=admission["reference_final"]
    floor=dict(number=reference["number"],hash=reference["block_hash"],count=reference["committed_executed_transactions"])
    if failure.get("kind")!="gov5-post-bounded-readonly-recovery-failure-independent-sol-acceptance-v1" or failure.get("failure_verified") is not True or failure.get("recovery_converged") is not False or failure.get("full_canonical_account_snapshot_obtained") is not False or failure.get("dispatched_transactions")!=0 or failure.get("second_reopen_performed") is not False or failure["canonical_last_metadata"]!={"number":floor["number"],"block_hash":floor["hash"],"committed_executed_transactions":floor["count"]}:
        raise ValueError("retained evidence detached from ordinary failed recovery")
    pins={str(failure_path):evidence["ordinary_failure_sha256"],**failure["evidence_sha256"]}
    for source,expected in pins.items():
        if hashlib.sha256(strict_fleet.read_private(Path(source),16<<20)).hexdigest()!=expected:
            raise ValueError("sealed ordinary recovery evidence changed")
    owners=[Path(p)for p in failure["evidence_sha256"]if Path(p).name=="first-owner.log"]
    if len(owners)!=1:
        raise ValueError("unique sealed owner log required")
    children=[]
    for line in owners[0].read_text().splitlines():
        match=re.search(r' record=("(?:\\.|[^"\\])*")',line)
        if match is None:continue
        record=json.loads(json.loads(match.group(1)))
        if record.get("Operation")=="applied_child" and record.get("Outcome")=="ok" and record.get("Number")==floor["number"]+1 and record.get("Parent")==floor["hash"]:
            if record.get("Identity")!=m["identity"] or type(record.get("Transactions")) is not int or record["Transactions"]<=0 or record.get("PhaseNs",{}).get("durable_evidence",0)<=0:
                raise ValueError("retained child lacks durable execution evidence")
            children.append({k:record[k]for k in ("Hash","Parent","Number","Transactions")})
    if len(children)!=len(m["validators"]) or any(v!=children[0]for v in children):
        raise ValueError("seven matching durable retained-child probes required")
    child=children[0];target=dict(number=child["Number"],hash=child["Hash"],parent=child["Parent"],transactions=child["Transactions"])
    if not strict_fleet.valid_hash(target["hash"]) or evidence["target"]!=target:
        raise ValueError("explicit retained target differs from actual probes")
    for node in m["validators"]:
        log=failure_path.parent/'metadata-logs'/('v'+str(node["index"])+'.n42.log')
        if str(log)not in failure["evidence_sha256"]:
            raise ValueError("sealed validator startup log missing")
        rows=[]
        for line in log.read_text().splitlines():
            try:rows.append(json.loads(line,object_pairs_hook=fleet.pairs_unique))
            except ValueError:continue
        states=[v for v in rows if v.get("msg")=="hotstuff: recovering persisted state"]
        heads=[v for v in rows if v.get("msg")=="hotstuff: persisted consensus state agrees with the applied chain"]
        if not states or not heads:
            raise ValueError("persisted consensus startup evidence missing")
        state,head=states[-1],heads[-1]
        short=floor["hash"][2:8]+'…'+floor["hash"][-6:]
        if state["lockedQCView"]!=state["committedQCView"] or state["view"]!=state["committedQCView"]+1 or state["lastVotedView"]>state["view"] or head["height"]!=floor["number"] or head["localHead"]!=floor["number"] or head["committedQCBlock"]!=short:
            raise ValueError("persisted lock/committed evidence outside retained-child branch")
    expected_count=floor["count"]+target["transactions"]
    return dict(floor=floor,target=target,expected_count=expected_count),pins


def bounded_account_evidence(accounts,historical,p,admission,count):
    old={a["address"]:a for a in historical["accounts"]}
    if len(accounts)!=len(old) or [a["address"]for a in accounts]!=sorted(old):
        raise ValueError("complete ordered canonical account vector required")
    actual=[0]*len(p["Senders"])
    indices={s["Address"]:s["Index"]for s in p["Senders"]}
    for account in accounts:
        previous=old[account["address"]];index=indices[account["address"]]
        if type(account["nonce"])is not int:
            raise ValueError("canonical nonce must be an integer")
        delta=account["nonce"]-previous["nonce"]
        if not 0<=delta<=admission["sender_new_transactions"][index] or account["exists"]!=previous["exists"] or account["code_hash"]!=previous["code_hash"] or account["balance"]!=str(int(previous["balance"])-21001*delta):
            raise ValueError("retained canonical account outside known ACK bound")
        actual[index]=delta
    if sum(a["nonce"]for a in accounts)!=count:
        raise ValueError("retained canonical nonce sum differs")
    return bounded_fleet.sender_delta_evidence(accounts,historical,p,actual)


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

def capture_snapshot(m,p,historical,admission,epoch,retained,guard,require_validators):
    import time
    count=retained["expected_count"];target_number=retained["target"]["number"];target_hash=retained["target"]["hash"]
    previous=[None]*len(m["validators"])
    addresses=sorted(a["address"] for a in historical["accounts"])
    end=time.monotonic()+45
    while time.monotonic()<end:
        guard(True)
        peers=[]
        if require_validators:
            for v in m["validators"]:
                item=fleet.status(v["api_socket"],m["identity"]);guard(True)
                recovery_validator(item,m,epoch,p["ManifestSHA256"],retained,previous[v["index"]])
                previous[v["index"]]=item
                peers.append(item)
            if any(v["number"]!=target_number or v["block_hash"]!=target_hash for v in peers):
                time.sleep(.1);continue
        owner=queue_fixture.owner_status(m);guard(True)
        if owner["Identity"]!=m["identity"] or owner["Epoch"]!=epoch or owner["ManifestHash"]!=p["ManifestSHA256"] or any(owner[k]!=0 for k in ("NewlyAdmitted","CryptoVerified","PendingTransactions","ReservedTransactions","RetainedEncodedBytes","ReservedEncodedBytes")):
            raise ValueError("quiescent owner identity/empty-session changed")
        floor=retained["floor"]
        if (owner["CanonicalNumber"],owner["CanonicalHash"]) not in ((floor["number"],floor["hash"]),(target_number,target_hash)):
            raise ValueError("owner outside exact retained-child transition")
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
                    recovery_validator(item,m,epoch,p["ManifestSHA256"],retained,previous[v["index"]],True)
                    previous[v["index"]]=item
                    peers.append(item)
            digest=bounded_account_evidence(accounts,historical,p,admission,count)
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

def run(m, node, owner, replay, attempt, plan_path, plan_sha, plan_id, historical_path, failed_path, admission_path, admission_sha, failure_path, failure_sha, supplement_path, supplement_sha, retained_path, retained_sha):
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
    retained,retained_pins=retained_inputs(retained_path,retained_sha,admission,m)
    target_number,target_hash=retained["target"]["number"],retained["target"]["hash"]
    if retained["expected_count"]>p["CanonicalNonceSum"]+totals["new"]:
        raise ValueError("retained child exceeds known ACK upper bound")
    if type(target_number) is not int or target_number!=admission["reference_final"]["number"]+1 or not strict_fleet.valid_hash(target_hash):
        raise ValueError("explicit next retained-child target required")
    failed, failed_sha = recovery_fleet.baseline(failed_path, m)
    input_paths = (plan_path, admission_path, failure_path, supplement_path, retained_path, historical_path, failed_path, root / "run.json", m["genesis"], Path(m["supply"]["root"]) / "manifest.json", Path(m["supply"]["root"]) / "genesis.json")
    pins = {str(path): hashlib.sha256(strict_fleet.read_private(Path(path), 16 << 20)).hexdigest() for path in input_paths}
    for shard in range(64):
        ack_path = Path(admission_path).parent / f"ack-shard-{shard:02d}.jsonl"
        pins[str(ack_path)] = hashlib.sha256(bounded_fleet.read_journal(ack_path)).hexdigest()
    pins.update(retained_pins)
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
        result = {"kind": "gov5-post-bounded-retained-child-recovery-only-double-reopen-v1", "identity": m["identity"], "resources": resources, "binaries": {"node": node, "owner": owner, "replay": replay}, "inputs_sha256": pins, "plan_id": plan_id, "no_initialization": True, "producer_recovery_only_then_disabled_configuration_copies": True, "retained_evidence":retained,"retained_target_number":target_number, "retained_target_hash":target_hash, "dispatched_transactions": 0, "known_prior_new_transactions": totals["new"], "known_prior_canonical_skipped": totals["skip"], "minimum_canonical_count": admission["reference_final"]["committed_executed_transactions"], "maximum_canonical_count": p["CanonicalNonceSum"] + totals["new"], "canonical_baseline_count": p["CanonicalNonceSum"], "log_directory": str(log_root), "started_unix": time.time(), "tps_measured": False, "capacity_5m_proven": False, "production_snapshot_reconstructed": False}
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
            converged=capture_snapshot(m,p,historical,admission,epoch,retained,guard,True)
            record("live-converged-snapshot.json",converged)
            owner_process=children[0]
            # Stop only the seven validators. The live owner remains available
            # for the post-stop snapshot which excludes late durable imports.
            children.remove(owner_process)
            try:
                stop_phase("recovery-validator-lifecycle.json")
            finally:
                children.insert(0,owner_process)
            stopped=capture_snapshot(m,p,historical,admission,epoch,retained,guard,False)
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
                if observed["canonical_nonce_sum"]!=retained["expected_count"] or observed["accounts"]!=converged["accounts"] or observed["account_vector_sha256"]!=converged["account_vector_sha256"] or observed["canonical_roots"]!=converged["canonical_roots"] or observed["owner_final"]["CanonicalHash"]!=target_hash or observed["owner_final"]["CanonicalNumber"]!=target_number or observed["owner_initial"]["Epoch"]==epoch:
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
    parser.add_argument("--reference-supplement", type=Path, required=True)
    parser.add_argument("--reference-supplement-sha256",required=True)
    parser.add_argument("--retained-evidence",type=Path,required=True)
    parser.add_argument("--retained-evidence-sha256",required=True)
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
            value = run(m, fleet.binary(args.node_binary), fleet.binary(args.owner_binary), fleet.binary(args.replay_binary), args.attempt, args.plan, args.plan_sha256, args.expected_plan_id, args.historical_recovery, args.failed_baseline, args.admission_result, args.admission_sha256, args.failure_acceptance, args.failure_acceptance_sha256, args.reference_supplement, args.reference_supplement_sha256, args.retained_evidence, args.retained_evidence_sha256)
            print(json.dumps({"result": str(Path(value["log_directory"]) / "quiescent-recovery-result.json"), "kind": value["kind"]}))
        else:
            if args.attempt or args.node_binary or args.owner_binary or args.replay_binary:
                raise ValueError("execution options require --run")
            print(json.dumps({"kind": "post-bounded-retained-child-validation-only", "plan_id": args.expected_plan_id}))
        return 0
    except (OSError, ValueError, TypeError, KeyError, subprocess.SubprocessError) as error:
        print(json.dumps({"error": str(error)}))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
