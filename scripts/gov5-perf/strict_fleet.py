#!/usr/bin/env python3
"""Validate a separate schema-2 signed-corpus runtime; never changes source data."""

import argparse
import hashlib
import http.client
import math
import json
import os
from pathlib import Path
import re
import shutil
import signal
import stat
import sys
import subprocess
import time

import fleet
import preflight

SUPPLY_BASE = preflight.ISOLATED_ROOT / "supply"


def read_private(path, limit):
    fleet.private(path)
    before = path.lstat()
    if before.st_size < 1 or before.st_size > limit:
        raise ValueError("bounded nonempty metadata required")
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as source:
        after = os.fstat(source.fileno())
        if not stat.S_ISREG(after.st_mode) or (before.st_dev, before.st_ino) != (after.st_dev, after.st_ino):
            raise ValueError("metadata changed while opening")
        data = source.read(limit + 1)
    if len(data) > limit:
        raise ValueError("metadata grew beyond limit")
    return data


def metadata(path, limit=2 << 20):
    return json.loads(read_private(path, limit), object_pairs_hook=fleet.pairs_unique)


def valid_hash(value, prefix=True):
    return isinstance(value, str) and re.fullmatch(("0x" if prefix else "") + "[0-9a-f]{64}", value) is not None and int(value, 16) != 0


def validate(path, base=fleet.BASE, supply_base=SUPPLY_BASE, check_ports=True):
    path = Path(path)
    m = metadata(path)
    fields = {"schema_version", "status", "root", "genesis", "genesis_sha256", "identity", "owner_dir", "owner_socket", "owner_parallel_evm", "validators", "transactions", "gas_price", "supply"}
    if set(m) != fields or type(m["schema_version"]) is not int or m["schema_version"] != 2 or m["status"] != "provisioned-not-initialized" or m["owner_parallel_evm"] is not True or m["transactions"] != "ethereum-secp256k1" or m["gas_price"] != "1":
        raise ValueError("explicit schema-2 strict signed-corpus runtime required")
    root = Path(m["root"])
    if root == base or not root.is_relative_to(base) or path != root / "run.json":
        raise ValueError("manifest outside dedicated runtime root")
    fleet.private(root, True)
    if m["genesis"] != str(root / "genesis.json") or m["owner_dir"] != str(root / "e") or m["owner_socket"] != str(root / "e/execution-rpc/execution.sock") or not valid_hash(m["genesis_sha256"], False):
        raise ValueError("unexpected topology/genesis binding")
    fleet.private(root / "e", True)
    if os.path.lexists(root / "e/keystore") or os.path.lexists(root / "load") or os.path.lexists(root / "sender.seed"):
        raise ValueError("supply runtime must not contain owner keys or sender material")
    supply = m["supply"]
    if set(supply) != {"root", "manifest_sha256", "corpus_id", "genesis_file_sha256", "block_interval_ms"} or type(supply["block_interval_ms"]) is not int or not 0 <= supply["block_interval_ms"] <= 60000:
        raise ValueError("explicit bounded supply/pacing binding required")
    source = Path(supply["root"])
    if source == supply_base or not source.is_relative_to(supply_base):
        raise ValueError("source outside dedicated supply root")
    fleet.private(source, True)
    raw_manifest = read_private(source / "manifest.json", 1 << 20)
    corpus = json.loads(raw_manifest, object_pairs_hook=fleet.pairs_unique)
    if set(corpus) != {"descriptor", "corpus_id", "shards"} or not valid_hash(corpus["corpus_id"]) or corpus["corpus_id"] != supply["corpus_id"] or hashlib.sha256(raw_manifest).hexdigest() != supply["manifest_sha256"]:
        raise ValueError("final source manifest binding differs")
    d = corpus["descriptor"]
    descriptor_fields = {"version", "identity", "genesis_file_sha256", "codec", "signature", "key_derivation", "seed_commitment", "sender_count", "nonce_start", "nonces_per_sender", "recipient_rule", "recipient_count", "shard_count", "frame_transactions"}
    if set(d) != descriptor_fields or type(d["version"]) is not int or d["version"] != 1 or type(d["nonce_start"]) is not int or d["identity"] != m["identity"] or d["genesis_file_sha256"] != "0x" + m["genesis_sha256"] or supply["genesis_file_sha256"] != m["genesis_sha256"] or d["codec"] != "ethereum-signed-rlp-eip155-v1" or d["signature"] != "secp256k1-eip155-v1" or d["key_derivation"] != "gov5-supply-seed-hmac-sha256-v1" or d["recipient_rule"] != "gov5-supply-hotset-keccak-v1" or d["nonce_start"] != 0 or not valid_hash(d["seed_commitment"]):
        raise ValueError("strict source descriptor differs")
    for name, low, high in (("sender_count", 1, 12500), ("nonces_per_sender", 1, 600000000), ("recipient_count", 1, 100000), ("shard_count", 1, 64), ("frame_transactions", 1, 500)):
        if type(d[name]) is not int or not low <= d[name] <= high:
            raise ValueError("invalid bounded corpus shape")
    if len(corpus["shards"]) != d["shard_count"] or d["shard_count"] > d["sender_count"] or d["sender_count"] * d["nonces_per_sender"] > 600000000:
        raise ValueError("source shard count differs")
    total = 0
    for i, shard in enumerate(corpus["shards"]):
        if set(shard) != {"index", "file", "transactions", "frames", "bytes", "sha256"} or type(shard["index"]) is not int or shard["index"] != i or shard["file"] != f"shard-{i:02d}.gcf" or not valid_hash(shard["sha256"]) or any(type(shard[k]) is not int or shard[k] <= 0 for k in ("transactions", "frames", "bytes")):
            raise ValueError("invalid source shard metadata")
        senders = (d["sender_count"] - 1 - i) // d["shard_count"] + 1
        expected_tx = senders * d["nonces_per_sender"]
        expected_frames = senders * ((d["nonces_per_sender"] + d["frame_transactions"] - 1) // d["frame_transactions"])
        if shard["transactions"] != expected_tx or shard["frames"] != expected_frames or not 84 * expected_frames + 5 * expected_tx <= shard["bytes"] <= 84 * expected_frames + 2052 * expected_tx:
            raise ValueError("source shard allocation/wire bounds differ")
        file = source / shard["file"]
        fleet.private(file)
        if file.stat().st_size != shard["bytes"]:
            raise ValueError("source shard length differs")
        total += shard["transactions"]
    if total != d["sender_count"] * d["nonces_per_sender"]:
        raise ValueError("source unique transaction count differs")
    genesis_raw = read_private(root / "genesis.json", 16 << 20)
    if genesis_raw != read_private(source / "genesis.json", 16 << 20) or hashlib.sha256(genesis_raw).hexdigest() != m["genesis_sha256"]:
        raise ValueError("exact source/runtime genesis bytes differ")
    g = json.loads(genesis_raw, object_pairs_hook=fleet.pairs_unique)
    cfg = g["config"]
    if cfg.get("performance") != {"version": 1, "mode": "independent", "role": "local", "transactions": "ethereum-secp256k1", "networkNamespace": "gov5-performance-5m-v1"} or cfg.get("chainId") != preflight.CHAIN_ID or cfg.get("consensus") != "hotstuff" or cfg.get("stateScheme") != "qmdb" or g.get("timestamp") != preflight.GENESIS_TIMESTAMP or g.get("extraData") != preflight.GENESIS_EXTRA:
        raise ValueError("explicit performance genesis required")
    identity = m["identity"]
    if set(identity) != {"version", "namespace", "chain_id", "genesis_hash"} or identity["version"] != 1 or identity["namespace"] != cfg["performance"]["networkNamespace"] or identity["chain_id"] != str(preflight.CHAIN_ID) or not valid_hash(identity["genesis_hash"]):
        raise ValueError("invalid runtime identity")
    validators = m["validators"]
    genesis_validators = cfg["hotstuff"]["validators"]
    if len(validators) != 7 or len(genesis_validators) != 7 or len({v["peer_id"] for v in validators}) != 7 or len({v["address"].lower() for v in validators}) != 7:
        raise ValueError("seven independent validators required")
    ports = [v["tcp_port"] for v in validators]
    if any(type(p) is not int for p in ports) or ports != list(range(ports[0], ports[0] + 7)) or not 25000 <= ports[0] <= 29993:
        raise ValueError("seven dedicated consecutive ports required")
    for i, v in enumerate(validators):
        directory = root / f"v{i}"
        address = genesis_validators[i]["address"]
        if not isinstance(address, str) or re.fullmatch("0x[0-9a-fA-F]{40}", address) is None or v != {"index": i, "address": address, "peer_id": v["peer_id"], "data_dir": str(directory), "config": str(directory / "node.json"), "api_socket": str(directory / "validator-rpc/validator.sock"), "tcp_port": ports[i]}:
            raise ValueError("validator topology/public identity differs")
        for p in (directory, directory / "keystore"):
            fleet.private(p, True)
        name = "bls_" + address.lower() + ".key"
        if {p.name for p in (directory / "keystore").iterdir()} != {name}:
            raise ValueError("each validator must have only its corresponding key")
        # Provision checks BLS public/address/order cryptographically, and node
        # startup checks it again. Here require exact source-key correspondence.
        if read_private(directory / "keystore" / name, 256) != read_private(source / f"v{i}" / "keystore" / name, 256):
            raise ValueError("corresponding source validator key differs")
        fleet.private(directory / "p2p.key")
        expected = {
            "node": {"data_dir": str(directory), "chain": "private", "profile": "n42", "performance_genesis": m["genesis"], "performance_role": "validator", "performance_execution_socket": m["owner_socket"], "performance_owner_queue": True, "performance_queue_max_transactions": 163000, "performance_queue_max_encoded_bytes": 32 << 20, "miner": True, "http": False, "ws": False, "auth_rpc": False, "ipc_path": ""},
            "chain": None,
            "miner": {"etherbase": address, "blockintervalms": supply["block_interval_ms"]},
            "p2p": {"private_key": str(directory / "p2p.key"), "data_dir": str(directory), "local_ip": "127.0.0.1", "host_address": "127.0.0.1", "tcp_port": ports[i], "udp_port": ports[i], "max_peers": 16, "static_peers": [f"/ip4/127.0.0.1/tcp/{w['tcp_port']}/p2p/{w['peer_id']}" for w in validators if w["index"] != i], "no_discovery": True, "quic_enabled": False, "bootstrap_node_addr": [], "discv5_bootstrap_addr": [], "relay_node_addr": "", "enable_upnp": False},
        }
        if metadata(directory / "node.json") != expected:
            raise ValueError("effective strict node configuration/pacing differs")
        if len(v["api_socket"].encode()) > 107:
            raise ValueError("Unix socket path too long")
    if check_ports and set(ports) & preflight.occupied_ports():
        raise ValueError("dedicated ports occupied")
    if any(k.startswith(("N42_", "QS_", "HIVE_")) for k in os.environ):
        raise ValueError("inherited chain environment forbidden")
    return m


def disk_budget(m, offered, free):
    if type(offered) is not int or not 1 <= offered <= 20000000:
        raise ValueError("offered target must be 1..20M; it is not measured throughput")
    corpus = metadata(Path(m["supply"]["root"]) / "manifest.json", 1 << 20)
    d = corpus["descriptor"]
    unique = min(d["sender_count"] * d["nonces_per_sender"], offered * 120 + d["shard_count"] * d["frame_transactions"])
    # Conservative planning estimate, not a storage-engine hard bound: eight
    # full body copies plus owner receipts/index/state. Runtime low-water guard
    # still stops dispatch/processes rather than risking the remaining disk.
    estimate = unique * 4096
    reserve = 64 << 30
    if free < estimate + reserve:
        raise ValueError("insufficient disk budget for signed prefix, eight body copies and receipt/state reserve")
    return {"maximum_offered_prefix_transactions": unique, "estimated_bytes_per_transaction": 4096,
            "estimated_chain_growth_bytes": estimate, "minimum_free_bytes": reserve, "free_bytes_before": free,
            "limitation": "planning estimate, not proven write amplification; runtime low-water stops owned processes"}


def strict_evidence(value, m):
    if value.get("kind") != "strict-owner-queue-canonical-observation-not-capacity-promotion" or value.get("passed") is not True or value.get("error") or value.get("identity") != m["identity"] or value.get("manifest_sha256") != "0x" + m["supply"]["manifest_sha256"] or value.get("runtime_manifest") != str(Path(m["root"]) / "run.json") or value.get("full_source_integrity_verified") is not True or value.get("corpus_available_through_window") is not True:
        raise ValueError("strict replay result identity/integrity/window binding differs")
    elapsed, rate = value["actual_window_seconds"], value["canonical_committed_executed_tps"]
    before = value["window_start"]["single_validator_canonical"]["committed_executed_transactions"]
    after = value["window_end"]["single_validator_canonical"]["committed_executed_transactions"]
    if type(before) is not int or type(after) is not int or before < 0 or after < before or not isinstance(elapsed, (float, int)) or not math.isfinite(elapsed) or not 20 <= elapsed <= 30 or not isinstance(rate, (float, int)) or not math.isfinite(rate) or rate < 0 or value["canonical_window_delta"] != after - before or not math.isclose(rate, (after - before) / elapsed, rel_tol=1e-12, abs_tol=1e-9):
        raise ValueError("strict canonical window counter/timing differs")
    expected = value["stream_final"]["acknowledged_unique_admitted"]
    owner, final = value["owner_final"], value["final"]
    epoch = value["owner_initial"]["Epoch"]
    if type(expected) is not int or expected <= 0 or not isinstance(epoch, list) or len(epoch) != 16 or not any(epoch) or owner["Epoch"] != epoch or owner["Identity"] != m["identity"] or owner["ManifestHash"] != "0x" + m["supply"]["manifest_sha256"] or owner["NewlyAdmitted"] != expected or owner["CryptoVerified"] < expected or owner["Reconciling"] or any(owner[k] != 0 for k in ("PendingTransactions", "ReservedTransactions", "RetainedEncodedBytes", "ReservedEncodedBytes")):
        raise ValueError("strict owner epoch/admission/credit evidence differs")
    if len(final) != 7 or any(s["identity"] != m["identity"] or s["committed_executed_transactions"] != expected or s["block_hash"] != owner["CanonicalHash"] or s["number"] != owner["CanonicalNumber"] for s in final) or after > expected:
        raise ValueError("strict seven canonical heads and owner differ")
    return expected


def run(m, node, owner, replay, attempt, initialize=False, offered=6500000, owner_procs=36, replay_procs=36):
    if not isinstance(attempt, str) or re.fullmatch(r"[a-z][a-z0-9-]{0,31}", attempt) is None or type(owner_procs) is not int or not 1 <= owner_procs <= 256 or type(replay_procs) is not int or not 1 <= replay_procs <= 256:
        raise ValueError("explicit new attempt and bounded process budgets required")
    root = Path(m["root"])
    if os.path.lexists(root / "attempts" / attempt) or os.path.lexists(root / "strict-attempts" / attempt):
        raise ValueError("existing attempt cannot be replayed or reset automatically")
    budget = disk_budget(m, offered, shutil.disk_usage(root).free)
    children, logs, lifecycle = [], [], []
    log_root = None
    deadline = time.monotonic() + 600

    def guard(force=False):
        fleet.check_owned_claim([p.pid for p in children if p.poll() is None], force=force)
        if time.monotonic() >= deadline:
            raise TimeoutError("bounded strict fleet turn expired")
        if shutil.disk_usage(root).free < budget["minimum_free_bytes"]:
            raise ValueError("strict fleet disk low-water; preserve data and stop owned processes")

    def spawn(args, name, procs):
        guard(True)
        log = os.fdopen(os.open(log_root / (name + ".log"), os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "wb")
        logs.append(log)
        process = subprocess.Popen(args, cwd=root, env=dict(os.environ, GOMAXPROCS=str(procs)), stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
        record = {"name": name, "pid": process.pid, "executable": args[0], "gomaxprocs": procs, "started_unix": time.time()}
        children.append(process)
        lifecycle.append((process, record))
        try:
            record["actual_cpu_affinity"] = sorted(os.sched_getaffinity(process.pid))
        except ProcessLookupError:
            record["affinity_sample"] = "exited-before-sample"
        return process

    def wait_owned(process, seconds):
        end = min(deadline, time.monotonic() + seconds)
        while process.poll() is None:
            guard()
            if time.monotonic() >= end:
                raise subprocess.TimeoutExpired(process.args, seconds)
            time.sleep(0.1)
        guard(True)
        return process.returncode

    def live():
        guard()
        if any(p.poll() is not None for p in children):
            raise ValueError("owned strict fleet child exited; preserve same runtime/logs")

    fleet.quiet_checks()
    with fleet.claim() as resources:
        if validate(root / "run.json") != m:
            raise ValueError("strict runtime changed while waiting for claim")
        budget = disk_budget(m, offered, shutil.disk_usage(root).free)
        log_root = fleet.attempt_logs(root, attempt)
        result = {"kind": "strict-owner-queue-fleet-observation-not-capacity-promotion", "identity": m["identity"], "source": m["supply"],
                  "binaries": {"node": node, "owner": owner, "replay": replay}, "resources": resources, "disk_budget": budget,
                  "log_directory": str(log_root), "started_unix": time.time(), "owner_gomaxprocs": owner_procs, "validator_gomaxprocs": 2,
                  "replay_gomaxprocs": replay_procs, "cpu_limitation": "thread budgets with recorded affinity, not exclusive physical cores"}
        try:
            if initialize:
                for directory, metadata_only in [(m["owner_dir"], False)] + [(v["data_dir"], True) for v in m["validators"]]:
                    expected = {"keystore", "p2p.key", "node.json"} if metadata_only else set()
                    if {p.name for p in Path(directory).iterdir()} != expected:
                        raise ValueError("initialization requires fresh datadirs; never reset partial data")
                    args = [node["path"], "init", "--profile", "n42", "--chain", "private", "--data.dir", directory]
                    if metadata_only:
                        args += ["--performance.metadata-only"]
                    process = spawn(args + [m["genesis"]], "init-" + Path(directory).name, 2)
                    if wait_owned(process, 120) != 0:
                        raise ValueError("strict runtime initialization failed; retain partial data")
                    children.remove(process)
            sockets = [m["owner_socket"], str(root / "e/execution-rpc/ingress.sock")] + [v["api_socket"] for v in m["validators"]]
            if any(os.path.lexists(path) for path in sockets):
                raise ValueError("existing sockets are never removed by launcher")
            manifest_path = str(Path(m["supply"]["root"]) / "manifest.json")
            spawn([owner["path"], "--genesis", m["genesis"], "--data-dir", m["owner_dir"], "--parallel-evm", "--strict-ingress-manifest", manifest_path], "owner", owner_procs)
            end = time.monotonic() + 30
            while not all(Path(path).exists() for path in sockets[:2]):
                live()
                if time.monotonic() >= end:
                    raise TimeoutError("strict owner readiness timeout")
                time.sleep(0.1)
            for v in m["validators"]:
                spawn([node["path"], "--config", v["config"]], "v" + str(v["index"]), 2)
            end = time.monotonic() + 30
            while True:
                live()
                try:
                    initial = []
                    for v in m["validators"]:
                        try:
                            initial.append(fleet.status(v["api_socket"], m["identity"]))
                        finally:
                            guard(True)
                    break
                except (OSError, http.client.HTTPException):
                    if time.monotonic() >= end:
                        raise TimeoutError("strict validator API readiness timeout")
                    time.sleep(0.1)
            if any(s["committed_executed_transactions"] != 0 for s in initial):
                raise ValueError("strict prefix replay requires fresh zero canonical state; no nonce-zero recovery")
            fleet.write_record(log_root / "initial-status.json", initial)
            process = spawn([replay["path"], "strict", "--root", m["supply"]["root"], "--runtime-manifest", str(root / "run.json"), "--attempt", attempt, "--offered-tps", str(offered)], "strict-replay", replay_procs)
            if wait_owned(process, 150) != 0:
                raise ValueError("strict replay failed; preserve same runtime, prefixes and unknown-outcome evidence")
            children.remove(process)
            live()
            evidence_path = root / "strict-attempts" / attempt / "result.json"
            evidence = metadata(evidence_path, 16 << 20)
            expected = strict_evidence(evidence, m)
            result.update(unique_final_canonical_transactions=expected, measured_canonical_tps=evidence["canonical_committed_executed_tps"], measured_elapsed_seconds=evidence["actual_window_seconds"], replay_result=str(evidence_path), finished_unix=time.time())
            guard(True)
            fleet.write_record(log_root / "strict-fleet-observation.json", result)
        finally:
            for process in reversed(children):
                if process.poll() is None:
                    process.send_signal(signal.SIGTERM)
            end = time.monotonic() + 10
            for process in reversed(children):
                try:
                    process.wait(timeout=max(0.01, end - time.monotonic()))
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
            for log in logs:
                log.close()
            for process, record in lifecycle:
                record.update(stopped_unix=time.time(), return_code=process.poll())
            fleet.write_record(log_root / "process-lifecycle.json", {"kind": "owned-strict-fleet-process-lifecycle", "processes": [record for _, record in lifecycle]})
    # A staged observation is not promoted if cleanup or the claim tail fails.
    fleet.write_record(log_root / "strict-fleet-result.json", result)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("--run", action="store_true")
    parser.add_argument("--initialize", action="store_true")
    parser.add_argument("--attempt")
    parser.add_argument("--node-binary", type=Path)
    parser.add_argument("--owner-binary", type=Path)
    parser.add_argument("--replay-binary", type=Path)
    parser.add_argument("--offered-tps", type=int, default=6500000)
    parser.add_argument("--owner-gomaxprocs", type=int, default=36)
    parser.add_argument("--replay-gomaxprocs", type=int, default=36)
    args = parser.parse_args()
    def interrupted(signum, frame):
        raise KeyboardInterrupt("signal " + str(signum))
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        m = validate(args.manifest)
        if args.run:
            if not args.attempt or any(p is None for p in (args.node_binary, args.owner_binary, args.replay_binary)):
                raise ValueError("run requires explicit binaries and new attempt")
            result = run(m, fleet.binary(args.node_binary), fleet.binary(args.owner_binary), fleet.binary(args.replay_binary), args.attempt, args.initialize, args.offered_tps, args.owner_gomaxprocs, args.replay_gomaxprocs)
            print(json.dumps({"result": str(Path(result["log_directory"]) / "strict-fleet-result.json"), "kind": result["kind"]}))
            return 0
        if args.initialize or args.attempt or args.node_binary or args.owner_binary or args.replay_binary:
            raise ValueError("execution options require --run")
        print(json.dumps({"kind": "strict-runtime-validation-only", "identity": m["identity"], "source_manifest_sha256": m["supply"]["manifest_sha256"], "full_source_sha_verified": False, "chain_execution_performed": False}))
        return 0
    except (OSError, ValueError, TypeError, KeyError, subprocess.SubprocessError, http.client.HTTPException) as error:
        print(json.dumps({"error": str(error)}))
        return 1


if __name__ == "__main__":
    sys.exit(main())
