"""Strictly validate the small owner queue fixture; no hardware or chain writes."""

import hashlib
from pathlib import Path

import fleet


def validate(m):
    root = Path(m["root"])
    directory = root / "load/queue"
    fleet.private(directory, True)
    manifest = directory / "manifest.json"
    fleet.private(manifest)
    raw = manifest.read_bytes()
    if len(raw) > 1 << 20:
        raise ValueError("queue manifest exceeds limit")
    corpus = fleet.load(manifest)
    if set(corpus) != {"descriptor", "corpus_id", "shards"}:
        raise ValueError("invalid queue corpus schema")
    d = corpus["descriptor"]
    if set(d) != {"version", "identity", "genesis_file_sha256", "codec", "signature", "key_derivation", "seed_commitment", "sender_count", "nonce_start", "nonces_per_sender", "recipient_rule", "recipient_count", "shard_count", "frame_transactions"}:
        raise ValueError("invalid queue descriptor schema")
    expected = {"version": 1, "identity": m["identity"], "genesis_file_sha256": "0x" + m["genesis_sha256"], "codec": "ethereum-signed-rlp-eip155-v1", "signature": "secp256k1-eip155-v1", "key_derivation": "gov5-supply-seed-hmac-sha256-v1", "sender_count": 4, "nonce_start": 0, "nonces_per_sender": 2, "recipient_rule": "gov5-supply-hotset-keccak-v1", "recipient_count": 4, "shard_count": 4, "frame_transactions": 2}
    if any(d.get(k) != v for k, v in expected.items()) or len(corpus["shards"]) != 4:
        raise ValueError("only fresh eight-transaction queue fixture supported")
    seed = root / "load/queue-seed"
    fleet.private(seed)
    if seed.stat().st_size != 32 or d["seed_commitment"] != "0x" + hashlib.sha256(seed.read_bytes()).hexdigest():
        raise ValueError("queue seed commitment differs")
    for index, shard in enumerate(corpus["shards"]):
        if set(shard) != {"index", "file", "transactions", "frames", "bytes", "sha256"} or shard["index"] != index or shard["file"] != f"shard-{index:02d}.gcf" or shard["transactions"] != 2 or shard["frames"] != 1:
            raise ValueError("invalid queue shard metadata")
        path = directory / shard["file"]
        fleet.private(path)
        if path.stat().st_size != shard["bytes"] or not 94 <= shard["bytes"] <= 8192 or "0x" + hashlib.sha256(path.read_bytes()).hexdigest() != shard["sha256"]:
            raise ValueError("queue shard size/hash differs")
    configs = root / "queue-configs"
    fleet.private(configs, True)
    for v in m["validators"]:
        path = configs / f"v{v['index']}.json"
        fleet.private(path)
        expected_config = fleet.load(Path(v["config"]))
        expected_config["node"].update(performance_owner_queue=True, performance_queue_max_transactions=163000, performance_queue_max_encoded_bytes=32 << 20)
        if fleet.load(path) != expected_config:
            raise ValueError("queue config differs from isolated baseline plus explicit queue options")
    return {"manifest": str(manifest), "manifest_hash": "0x" + hashlib.sha256(raw).hexdigest()}


def evidence(value, m, binding):
    if value.get("kind") != "owner-queue-functional-check-not-tps" or value.get("identity") != m["identity"] or value.get("genesis_sha256") != m["genesis_sha256"] or value.get("manifest_sha256") != binding["manifest_hash"] or value.get("unique_committed_executed_transactions") != 8:
        raise ValueError("queue functional result binding differs")
    owner = value["owner_final"]
    if owner["Identity"] != m["identity"] or owner["ManifestHash"] != binding["manifest_hash"] or owner["Reconciling"] or any(owner[k] != 0 for k in ("PendingTransactions", "ReservedTransactions", "RetainedEncodedBytes", "ReservedEncodedBytes")) or owner["NewlyAdmitted"] != 8 or owner["CryptoVerified"] < 8:
        raise ValueError("owner queue did not canonically release all credits")
    final = value["final"]
    if len(final) != 7 or any(s["committed_executed_transactions"] != 8 or s["identity"] != m["identity"] or s["block_hash"] != owner["CanonicalHash"] or s["number"] != owner["CanonicalNumber"] for s in final):
        raise ValueError("seven validators and owner did not converge")
    return 8


def owner_status(m):
    client = fleet.UnixHTTP(m["owner_socket"], timeout=2)
    try:
        import json
        client.request("POST", "/v1/ingress-status", json.dumps({"identity": m["identity"]}), {"Content-Type": "application/json"})
        response = client.getresponse()
        raw = response.read(8193)
        if response.status != 200 or len(raw) > 8192:
            raise ValueError("owner strict status unavailable or oversized")
        return json.loads(raw, object_pairs_hook=fleet.pairs_unique)
    finally:
        client.close()
