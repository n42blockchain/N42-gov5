#!/usr/bin/env python3
"""Read-only validation. Does not launch, initialize, claim or stop anything."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import sys
import time


ISOLATED_ROOT = Path("/data/blockchain/gov5-performance-5m")
CHAIN_ID = 4205001
GENESIS_TIMESTAMP = 1791936001
GENESIS_EXTRA = "0x676f76352d706572666f726d616e63652d356d2d7631"
PORT_NAMES = {
    "p2p_tcp", "p2p_udp", "http", "mobileverify", "pprof", "ingest",
    "attestation", "shared_execution",
}
PROTECTED_PORTS = {8545, 8546, 8551, 30303}
for base in (32000, 33000, 20012, 21012, 6090):
    PROTECTED_PORTS.update(range(base, base + 7))


def validate_profile(profile_path, isolated_root=ISOLATED_ROOT):
    profile_path = Path(profile_path).resolve(strict=True)
    p = json.loads(profile_path.read_text())
    expected = {
        "schema_version": 1, "name": "gov5-performance-5m",
        "status": "planned-not-runnable", "node_count": 7,
        "node_profile": "n42", "chain": "private", "chain_id": CHAIN_ID,
        "listen_address": "127.0.0.1",
        "network_namespace": "gov5-performance-5m-v1",
    }
    for key, value in expected.items():
        if type(p.get(key)) is not type(value) or p.get(key) != value:
            raise ValueError(f"invalid isolated profile field: {key}")
    if p.get("execution") != {
        "baseline": "independent", "candidate": "shared-go-E1",
        "candidate_status": "prototype-implemented-not-fleet-validated",
    } or p.get("transactions") != {
        "baseline": "ethereum-secp256k1", "candidate": "performance-ed25519",
        "candidate_status": "planned-not-implemented",
    }:
        raise ValueError("execution prototype and unimplemented transaction mode must remain explicit")
    if p.get("p2p") != {
        "discovery": False, "bootnodes": [], "public_peers": False,
        "genesis_override": False,
    }:
        raise ValueError("discovery, public peers and genesis overrides are forbidden")
    known = set(expected) | {"runtime_root", "genesis", "execution", "transactions", "ports", "resources", "p2p"}
    if set(p) != known:
        raise ValueError("unknown or missing profile fields")
    # A symlink at the trusted root must not redirect the whole fleet into an
    # existing chain. Resolve absent leaf paths too; lexical checks are unsafe.
    if isolated_root.resolve() != isolated_root:
        raise ValueError("isolated root is redirected by a symlink")
    raw_root = Path(p["runtime_root"])
    if not raw_root.is_absolute():
        raise ValueError("runtime root must be absolute")
    root = raw_root.resolve()
    if root != isolated_root / "runtime":
        raise ValueError("runtime root must resolve to the dedicated runtime directory")
    datadirs = [(root / f"node{i}").resolve() for i in range(7)]
    paths = [root, *datadirs, (root / "execution").resolve()]
    for path in paths:
        if path != root and not path.is_relative_to(root):
            raise ValueError("runtime subdirectory escapes the isolated root")
        if any(part in {"mainnet", "n42data", "devtest", "keystore"} for part in path.parts):
            raise ValueError("production data path is forbidden")
        if path.exists() and (not path.is_dir() or any(path.iterdir())):
            raise ValueError("stage 0 requires empty runtime directories; no reuse or reseeding")
    if len(set(paths)) != len(paths):
        raise ValueError("runtime directories alias each other")
    if p["genesis"] != "isolated-chain.json":
        raise ValueError("use the dedicated genesis template")
    genesis_path = (profile_path.parent / p["genesis"]).resolve(strict=True)
    if genesis_path.parent != profile_path.parent:
        raise ValueError("genesis path escapes profile directory")
    genesis_bytes = genesis_path.read_bytes()
    g = json.loads(genesis_bytes)
    c = g.get("config", {})
    if c.get("performance") != {
        "version": 1, "mode": "independent", "role": "local",
        "transactions": "ethereum-secp256k1",
        "networkNamespace": "gov5-performance-5m-v1",
    }:
        raise ValueError("genesis requires explicit independent/local performance configuration")
    if (c.get("chainId") != CHAIN_ID or c.get("consensus") != "hotstuff"
            or c.get("stateScheme") != "qmdb"
            or g.get("timestamp") != GENESIS_TIMESTAMP
            or g.get("extraData") != GENESIS_EXTRA):
        raise ValueError("genesis isolation identity does not match the profile")
    if c.get("hotstuff", {}).get("validators") != [] or g.get("alloc") != {}:
        raise ValueError("validator and account provisioning belongs to the next stage")
    ports = p["ports"]
    if set(ports) != PORT_NAMES:
        raise ValueError("missing or unknown port surface")
    allocations = []
    for name, base in ports.items():
        if type(base) is not int or not 1024 <= base <= 32760:
            raise ValueError(f"port base outside the reserved low-port window: {name}")
        allocations.extend((name, base + i) for i in range(7 if name != "shared_execution" else 1))
    numbers = [port for _, port in allocations]
    if len(numbers) != len(set(numbers)) or set(numbers) & PROTECTED_PORTS:
        raise ValueError("port collision inside profile or with protected networks")
    if p["resources"] != {
        "min_available_gib": 90, "max_load1": 8,
        "claim_roots": ["/data/blockchain", "/data/blockchain/wr-logs"],
    }:
        raise ValueError("resource gates cannot be weakened in this stage")
    inherited = sorted(k for k in os.environ if k.startswith(("N42_", "QS_", "HIVE_")))
    if inherited:
        raise ValueError("inherited chain configuration must be removed: " + ", ".join(inherited))
    return {
        "profile": p["name"], "status": p["status"],
        "datadirs": [str(path) for path in datadirs],
        "genesis_file_sha256": hashlib.sha256(genesis_bytes).hexdigest(),
        "genesis_block_hash": "not-computed-stage-0",
        "chain_id": CHAIN_ID, "ports": allocations,
        "launch_supported": False,
    }


def occupied_ports(proc_root=Path("/proc")):
    occupied = set()
    for table in ("tcp", "tcp6", "udp", "udp6"):
        for line in (proc_root / "net" / table).read_text().splitlines()[1:]:
            occupied.add(int(line.split()[1].rsplit(":", 1)[1], 16))
    return occupied


def resource_blockers(proc_root=Path("/proc"), claim_roots=None, now=None):
    now = time.time() if now is None else now
    claim_roots = [Path("/data/blockchain"), Path("/data/blockchain/wr-logs")] if claim_roots is None else claim_roots
    blockers = []
    for directory in claim_roots:
        for claim in directory.glob(".box-claim*"):
            try:
                fields = claim.read_text().split()
                stamps = [int(field) for field in fields]
                if not stamps or len(stamps) > 2 or min(stamps) <= 0:
                    raise ValueError("unknown claim format")
                # Handle one timestamp and proposed immutable-claim+heartbeat
                # formats. Ambiguous/future/malformed claims all fail closed.
                if now - max(stamps) < 5400:
                    blockers.append(f"active claim: {claim.name}")
            except (OSError, ValueError):
                blockers.append(f"unreadable claim: {claim.name}")
    load1 = float((proc_root / "loadavg").read_text().split()[0])
    mem = dict(line.split(":", 1) for line in (proc_root / "meminfo").read_text().splitlines())
    available_gib = int(mem["MemAvailable"].split()[0]) / (1024 * 1024)
    if load1 >= 8:
        blockers.append("load1 >= 8")
    if available_gib < 90:
        blockers.append("MemAvailable < 90 GiB")
    busy = {"cargo", "rustc", "compile", "link", "go", "rbtcd", "reth", "geth", "lighthouse", "h2_validator", "mdbx_copy", "mdbx_chk", "overlay_audit"}
    for directory in proc_root.iterdir():
        if not directory.name.isdigit():
            continue
        try:
            comm = (directory / "comm").read_text().strip()
        except FileNotFoundError:  # Process exited during the snapshot.
            continue
        if comm in busy or comm.startswith(("n42", "txflood")):
            blockers.append(f"other workload: pid={directory.name} comm={comm}")
    return blockers, {"load1": load1, "available_gib": round(available_gib, 2)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("validate", "preflight"))
    parser.add_argument("--profile", type=Path, default=Path(__file__).with_name("profile.json"))
    args = parser.parse_args()
    try:
        result = validate_profile(args.profile)
        blockers = []
        if args.mode == "preflight":
            occupied = occupied_ports()
            blockers = [f"occupied port: {name}={port}" for name, port in result["ports"] if port in occupied]
            resource_errors, snapshot = resource_blockers()
            blockers.extend(resource_errors)
            result["resources"] = snapshot
            result["quiet_samples_required_before_future_launch"] = 3
        result["blockers"] = blockers
        result["validation"] = "blocked" if blockers else "passed"
        print(json.dumps(result, indent=2))
        return 1 if blockers else 0
    except (OSError, ValueError, KeyError, TypeError) as exc:
        print(json.dumps({"validation": "failed", "error": str(exc), "launch_supported": False}))
        return 1


if __name__ == "__main__":
    sys.exit(main())
