#!/usr/bin/env python3
"""Validate a separate schema-2 signed-corpus runtime; never changes source data."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import sys

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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path)
    args = parser.parse_args()
    try:
        m = validate(args.manifest)
        print(json.dumps({"kind": "strict-runtime-validation-only", "identity": m["identity"], "source_manifest_sha256": m["supply"]["manifest_sha256"], "full_source_sha_verified": False, "chain_execution_performed": False}))
        return 0
    except (OSError, ValueError, TypeError, KeyError) as error:
        print(json.dumps({"error": str(error)}))
        return 1


if __name__ == "__main__":
    sys.exit(main())
