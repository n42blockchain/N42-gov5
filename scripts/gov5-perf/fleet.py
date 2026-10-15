#!/usr/bin/env python3
"""Validate or run the private eight-process functional fixture; never a TPS run."""

import argparse
import contextlib
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
import signal
import socket
import stat
import subprocess
import sys
import time
import uuid

import preflight


BASE = preflight.ISOLATED_ROOT / "runtime"
CLAIM = Path("/data/blockchain/.box-claim-gov5-performance-5m")
LOCK = Path("/data/blockchain/.gov5-perf-resource.lock")


def pairs_unique(pairs):
    out = {}
    for key, value in pairs:
        if key in out:
            raise ValueError("duplicate JSON field")
        out[key] = value
    return out


def load(path):
    if path.stat().st_size > 2 << 20:
        raise ValueError("configuration file too large")
    return json.loads(path.read_text(), object_pairs_hook=pairs_unique)


def private(path, directory=False):
    if not path.is_absolute() or path.resolve(strict=True) != path:
        raise ValueError("absolute paths without aliases required")
    info = path.lstat()
    if info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != (0o700 if directory else 0o600):
        raise ValueError("runtime path must be owned and private: " + str(path))
    if not (stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode)):
        raise ValueError("unexpected runtime path type")


def validate(path, base=BASE, check_ports=True):
    path = Path(path)
    private(path)
    m = load(path)
    if set(m) != {"schema_version", "status", "root", "genesis", "genesis_sha256", "identity", "owner_dir", "owner_socket", "owner_parallel_evm", "validators", "load_keys", "transactions", "gas_price"}:
        raise ValueError("unknown/missing manifest field")
    root = Path(m["root"])
    if root == base or not root.is_relative_to(base) or path != root / "run.json":
        raise ValueError("manifest outside dedicated fresh run root")
    private(root, True)
    if m["schema_version"] != 1 or m["status"] != "provisioned-not-initialized" or m["owner_parallel_evm"] is not True or m["transactions"] != "ethereum-secp256k1" or m["gas_price"] != "1":
        raise ValueError("only the small explicit parallel Ethereum fixture is supported")
    if m["genesis"] != str(root / "genesis.json") or m["owner_dir"] != str(root / "e") or m["owner_socket"] != str(root / "e/execution-rpc/execution.sock") or m["load_keys"] != str(root / "load/keys.json"):
        raise ValueError("unexpected topology path")
    for p in (root / "genesis.json", root / "load/keys.json"):
        private(p)
    private(root / "e", True)
    private(root / "load", True)
    if (root / "e/keystore").exists():
        raise ValueError("executor must not receive validator keys")
    if hashlib.sha256((root / "genesis.json").read_bytes()).hexdigest() != m["genesis_sha256"]:
        raise ValueError("full genesis file SHA differs")
    g = load(root / "genesis.json")
    cfg = g["config"]
    if cfg.get("performance") != {"version": 1, "mode": "independent", "role": "local", "transactions": "ethereum-secp256k1", "networkNamespace": "gov5-performance-5m-v1"} or cfg.get("chainId") != preflight.CHAIN_ID or cfg.get("consensus") != "hotstuff" or cfg.get("stateScheme") != "qmdb" or g.get("timestamp") != preflight.GENESIS_TIMESTAMP or g.get("extraData") != preflight.GENESIS_EXTRA:
        raise ValueError("explicit performance genesis required")
    ident = m["identity"]
    if set(ident) != {"version", "namespace", "chain_id", "genesis_hash"} or ident["version"] != 1 or ident["namespace"] != cfg["performance"]["networkNamespace"] or ident["chain_id"] != str(preflight.CHAIN_ID) or not isinstance(ident["genesis_hash"], str) or len(ident["genesis_hash"]) != 66 or int(ident["genesis_hash"], 16) == 0:
        raise ValueError("invalid chain identity (runtime also verifies actual header hash)")
    validators = m["validators"]
    if len(validators) != 7 or len(cfg["hotstuff"]["validators"]) != 7 or len({v["peer_id"] for v in validators}) != 7 or len({v["address"] for v in validators}) != 7:
        raise ValueError("exactly seven independent identities required")
    ports = [v["tcp_port"] for v in validators]
    if any(type(p) is not int for p in ports) or ports != list(range(ports[0], ports[0] + 7)) or not 25000 <= ports[0] <= 29993:
        raise ValueError("seven consecutive dedicated TCP ports required")
    for i, v in enumerate(validators):
        d = root / f"v{i}"
        if v != {"index": i, "address": cfg["hotstuff"]["validators"][i]["address"], "peer_id": v["peer_id"], "data_dir": str(d), "config": str(d / "node.json"), "api_socket": str(d / "validator-rpc/validator.sock"), "tcp_port": ports[i]}:
            raise ValueError("validator path/public identity mismatch")
        private(d, True)
        private(d / "keystore", True)
        for f in (d / "node.json", d / "p2p.key", d / "keystore" / ("bls_" + v["address"].lower() + ".key")):
            private(f)
        expected = {
            "node": {"data_dir": str(d), "chain": "private", "profile": "n42", "performance_genesis": m["genesis"], "performance_role": "validator", "performance_execution_socket": m["owner_socket"], "miner": True, "http": False, "ws": False, "auth_rpc": False, "ipc_path": ""},
            "chain": None,
            "miner": {"etherbase": v["address"], "blockintervalms": 2000},
            "p2p": {"private_key": str(d / "p2p.key"), "data_dir": str(d), "local_ip": "127.0.0.1", "host_address": "127.0.0.1", "tcp_port": ports[i], "udp_port": ports[i], "max_peers": 16, "static_peers": [f"/ip4/127.0.0.1/tcp/{w['tcp_port']}/p2p/{w['peer_id']}" for w in validators if w["index"] != i], "no_discovery": True, "quic_enabled": False, "bootstrap_node_addr": [], "discv5_bootstrap_addr": [], "relay_node_addr": "", "enable_upnp": False},
        }
        if load(d / "node.json") != expected:
            raise ValueError("node configuration differs from private functional fixture")
        if len(v["api_socket"].encode()) > 107:
            raise ValueError("Unix socket path too long")
    keys = load(Path(m["load_keys"]))
    if not 1 <= len(keys) <= 64 or any(not isinstance(k, str) or len(k) != 64 or int(k, 16) == 0 for k in keys):
        raise ValueError("invalid private fixture sender keys")
    if check_ports and set(ports) & preflight.occupied_ports():
        raise ValueError("dedicated ports already occupied")
    inherited = [k for k in os.environ if k.startswith(("N42_", "QS_", "HIVE_"))]
    if inherited:
        raise ValueError("inherited chain environment is forbidden")
    return m


@contextlib.contextmanager
def claim(claim_path=CLAIM, lock_path=LOCK, blockers=preflight.resource_blockers):
    """Serialize cooperating launchers; ambiguous existing claims fail closed."""
    fd = os.open(lock_path, os.O_CREAT | os.O_RDWR | os.O_CLOEXEC | os.O_NOFOLLOW, 0o600)
    token = uuid.uuid4().hex
    acquired = False
    try:
        info = os.fstat(fd)
        if info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o600 or not stat.S_ISREG(info.st_mode):
            raise ValueError("unsafe resource lock")
        fcntl.flock(fd, fcntl.LOCK_EX)
        errors, snapshot = blockers()
        if errors:
            raise ValueError("hardware blocked: " + "; ".join(errors))
        claim_path.mkdir(mode=0o700)
        acquired = True
        record = {"pid": os.getpid(), "token": token, "created_unix": time.time(), "purpose": "gov5 E1 functional fixture; not TPS"}
        owner = claim_path / "owner.json"
        with os.fdopen(os.open(owner, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "w") as out:
            json.dump(record, out)
        fcntl.flock(fd, fcntl.LOCK_UN)
        yield snapshot
    finally:
        if acquired:
            # Never unlink another owner or recursively remove data. A crash
            # leaves an explicit stale claim requiring human review.
            try:
                if load(claim_path / "owner.json").get("token") == token:
                    (claim_path / "owner.json").unlink()
                    claim_path.rmdir()
            except (OSError, ValueError):
                pass
        os.close(fd)


def binary(path):
    p = Path(path)
    if not p.is_absolute() or p.resolve(strict=True) != p or not p.is_file() or not os.access(p, os.X_OK) or p.stat().st_mode & 0o022:
        raise ValueError("explicit canonical non-writable executable required")
    with p.open("rb") as source:
        digest = hashlib.file_digest(source, "sha256").hexdigest()
    return {"path": str(p), "sha256": digest}


class UnixHTTP(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.host)


def status(socket_path, identity):
    c = UnixHTTP(socket_path, timeout=2)
    try:
        c.request("POST", "/v1/status", json.dumps({"identity": identity}), {"Content-Type": "application/json"})
        r = c.getresponse()
        data = r.read(4097)
        if r.status != 200 or len(data) > 4096:
            raise ValueError("validator status unavailable")
        out = json.loads(data)
        if out.get("identity") != identity:
            raise ValueError("validator returned another chain")
        return out
    finally:
        c.close()


def run(m, node, owner, duration, initialize):
    root = Path(m["root"])
    children = []
    logs = []
    env = dict(os.environ, GOMAXPROCS="2")
    def spawn(args, name):
        log = os.fdopen(os.open(root / (name + ".log"), os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "wb")
        logs.append(log)
        process = subprocess.Popen(args, cwd=root, env=env, stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
        children.append(process)
        return process
    def live():
        if any(p.poll() is not None for p in children):
            raise ValueError("owned child exited; inspect retained private logs")
    # Quiet samples are observations, never authority. The final sample and
    # claim acquisition remain under one inter-launcher lock below.
    for _ in range(2):
        errors, _ = preflight.resource_blockers()
        if errors:
            raise ValueError("hardware blocked: " + "; ".join(errors))
        time.sleep(1)
    with claim() as resources:
        # Repeat validation after the atomic claim. Uncooperative external
        # programs can still race ports; actual binds remain authoritative.
        validate(root / "run.json")
        result = {"kind": "functional-check-not-tps", "identity": m["identity"], "genesis_sha256": m["genesis_sha256"], "binaries": {"node": node, "owner": owner}, "resources": resources, "started_unix": time.time(), "owner_parallel_evm": True}
        try:
            if initialize:
                for d, metadata in [(m["owner_dir"], False)] + [(v["data_dir"], True) for v in m["validators"]]:
                    expected_files = {"keystore", "p2p.key", "node.json"} if metadata else set()
                    if {p.name for p in Path(d).iterdir()} != expected_files:
                        raise ValueError("initialization requires fresh datadirs")
                    args = [node["path"], "init", "--profile", "n42", "--chain", "private", "--data.dir", d]
                    if metadata:
                        args += ["--performance.metadata-only"]
                    p = spawn(args + [m["genesis"]], "init-" + Path(d).name)
                    if p.wait(timeout=120) != 0:
                        raise ValueError("initialization failed; retain partial data for review")
                    children.remove(p)
            for path in [m["owner_socket"]] + [v["api_socket"] for v in m["validators"]]:
                if Path(path).exists() or Path(path).is_symlink():
                    raise ValueError("existing socket is never removed by launcher")
            spawn([owner["path"], "--genesis", m["genesis"], "--data-dir", m["owner_dir"], "--parallel-evm"], "owner")
            deadline = time.monotonic() + 30
            while not Path(m["owner_socket"]).exists():
                live()
                if time.monotonic() > deadline:
                    raise ValueError("owner readiness timeout")
                time.sleep(0.1)
            for v in m["validators"]:
                spawn([node["path"], "--config", v["config"]], "v" + str(v["index"]))
            deadline = time.monotonic() + 30
            while True:
                live()
                try:
                    initial = [status(v["api_socket"], m["identity"]) for v in m["validators"]]
                    break
                except (OSError, ValueError, http.client.HTTPException):
                    if time.monotonic() > deadline:
                        raise ValueError("seven runtime API readiness timeout")
                    time.sleep(0.2)
            end = time.monotonic() + duration
            while time.monotonic() < end:
                live()
                time.sleep(min(0.2, max(0, end - time.monotonic())))
            convergence = time.monotonic() + 5
            while True:
                live()
                final = [status(v["api_socket"], m["identity"]) for v in m["validators"]]
                heads = {(s["number"], s["block_hash"], s["committed_executed_transactions"]) for s in final}
                if len(heads) == 1 and final[0]["number"] > 0:
                    break
                if time.monotonic() > convergence:
                    raise ValueError("seven processes did not converge to one nonzero canonical head/counter")
                time.sleep(0.1)
            result.update(initial=initial, final=final, finished_unix=time.time(), submitted_transactions=0, note="empty-block process/transport/finality check; no transaction TPS")
            with os.fdopen(os.open(root / "functional-result.json", os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "w") as out:
                json.dump(result, out, indent=2)
            return result
        finally:
            for p in reversed(children):
                if p.poll() is None:
                    p.send_signal(signal.SIGTERM)
            deadline = time.monotonic() + 10
            for p in reversed(children):
                try:
                    p.wait(timeout=max(0.01, deadline - time.monotonic()))
                except subprocess.TimeoutExpired:
                    p.kill()
                    p.wait(timeout=5)
            for log in logs:
                log.close()


def main():
    def interrupted(signum, frame):
        # Unwind run/claim finally blocks; no orphan fleet on normal TERM/INT.
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupted)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("--run", action="store_true", help="explicitly claim hardware and launch owned processes")
    parser.add_argument("--initialize", action="store_true", help="explicit fresh DB initialization, only with --run")
    parser.add_argument("--node-binary")
    parser.add_argument("--owner-binary")
    parser.add_argument("--duration-seconds", type=int, default=15)
    args = parser.parse_args()
    try:
        m = validate(args.manifest)
        if not args.run:
            if args.initialize:
                raise ValueError("--initialize requires --run")
            print(json.dumps({"validation": "passed", "launch_performed": False, "identity": m["identity"], "genesis_sha256": m["genesis_sha256"]}))
            return 0
        if not 5 <= args.duration_seconds <= 60 or not args.node_binary or not args.owner_binary:
            raise ValueError("bounded 5..60 second run and explicit binaries required")
        result = run(m, binary(args.node_binary), binary(args.owner_binary), args.duration_seconds, args.initialize)
        print(json.dumps({"result": str(Path(m["root"]) / "functional-result.json"), "kind": result["kind"]}))
        return 0
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError, KeyboardInterrupt) as exc:
        print(json.dumps({"error": str(exc), "kind": "functional-check-not-tps"}))
        return 1


if __name__ == "__main__":
    sys.exit(main())
