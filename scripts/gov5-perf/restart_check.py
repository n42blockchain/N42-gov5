#!/usr/bin/env python3
"""Read nonzero canonical convergence; never launch or submit transactions.

This checks retained counters/heads after an independently recorded restart. The
status API cannot prove ancestry of a higher head or complete WAL replay.
"""

import argparse
import http.client
import json
import os
from pathlib import Path
import re
import stat
import sys
import time

import fleet


def uint64(value):
    return type(value) is int and 0 <= value <= (1 << 64) - 1


def head(value, identity):
    if not isinstance(value, dict) or value.get("identity") != identity:
        raise ValueError("canonical status identity mismatch")
    number, count, block_hash = (value.get(key) for key in ("number", "committed_executed_transactions", "block_hash"))
    if not uint64(number) or not uint64(count) or number == 0 or count == 0:
        raise ValueError("restart convergence requires nonzero uint64 height/count")
    if not isinstance(block_hash, str) or re.fullmatch(r"0x[0-9a-f]{64}", block_hash) is None or int(block_hash, 16) == 0:
        raise ValueError("canonical status hash invalid")
    return number, block_hash, count


def baseline_head(manifest, baseline):
    if not isinstance(baseline, dict) or baseline.get("kind") != "functional-check-not-tps" or baseline.get("identity") != manifest["identity"] or baseline.get("genesis_sha256") != manifest["genesis_sha256"]:
        raise ValueError("archived functional baseline binding differs")
    final = baseline.get("final")
    if not isinstance(final, list) or len(final) != 7:
        raise ValueError("seven archived canonical observations required")
    heads = {head(item, manifest["identity"]) for item in final}
    if len(heads) != 1:
        raise ValueError("archived canonical observations disagree")
    return heads.pop()


def read_status(path, identity):
    path = Path(path)
    fleet.private(path.parent, directory=True)
    info = path.lstat()
    if path.resolve(strict=True) != path or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o600 or not stat.S_ISSOCK(info.st_mode):
        raise ValueError("private owned validator socket required")
    connection = fleet.UnixHTTP(str(path), timeout=2)
    try:
        connection.request("POST", "/v1/status", json.dumps({"identity": identity}), {"Content-Type": "application/json"})
        response = connection.getresponse()
        raw = response.read(4097)
        if response.status != 200 or len(raw) > 4096:
            raise ValueError("restart status HTTP/size error: " + str(response.status))
        return json.loads(raw, object_pairs_hook=fleet.pairs_unique)
    finally:
        connection.close()


def converge(manifest, baseline, timeout=30, read=read_status, clock=time.monotonic, wait=time.sleep, require_progress=False):
    if type(timeout) is not int or not 1 <= timeout <= 120:
        raise ValueError("convergence timeout must be 1..120 seconds")
    expected = baseline_head(manifest, baseline)
    if len(manifest["validators"]) != 7:
        raise ValueError("seven validator sockets required")
    initial = previous = None
    deadline = clock() + timeout
    while True:
        try:
            observations = [read(v["api_socket"], manifest["identity"]) for v in manifest["validators"]]
        except (OSError, http.client.HTTPException):
            # Unavailable sockets may be warming up. A decoded wrong identity,
            # count or malformed response is an error, never a readiness retry.
            observations = None
        if observations is not None:
            heads = [head(item, manifest["identity"]) for item in observations]
            for i, current in enumerate(heads):
                if require_progress and (type(observations[i].get("queued_transactions")) is not int or observations[i]["queued_transactions"] != 0):
                    raise ValueError("restart progress requires empty transaction queues")
                if current[2] != expected[2] or current[0] < expected[0] or (current[0] == expected[0] and current[1] != expected[1]):
                    raise ValueError("retained canonical evidence regressed, conflicted or changed count")
                if previous is not None and (current[0] < previous[i][0] or (current[0] == previous[i][0] and current[1] != previous[i][1])):
                    raise ValueError("canonical observations regressed or conflicted")
            if initial is None:
                initial = observations
            previous = heads
            if len(set(heads)) == 1 and (not require_progress or heads[0][0] > expected[0]):
                return {"kind": "nonzero-canonical-convergence-check-not-full-replay-proof", "identity": manifest["identity"],
                        "genesis_sha256": manifest["genesis_sha256"], "baseline": baseline["final"], "initial": initial, "final": observations,
                        "submitted_transactions": 0, "counter_delta": 0, "empty_block_progress_required": require_progress,
                        "limitation": "Read-only status convergence; does not prove higher-head ancestry, complete WAL replay or new transaction liveness; no TPS."}
        if clock() >= deadline:
            raise TimeoutError("seven nonzero canonical heads did not converge")
        wait(min(0.2, max(0, deadline - clock())))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("baseline", type=Path, help="Archived successful functional-result.json")
    parser.add_argument("output", type=Path, help="New evidence file inside an existing private run directory")
    parser.add_argument("--timeout-seconds", type=int, default=30)
    args = parser.parse_args()
    try:
        manifest = fleet.validate(args.manifest, check_ports=False)
        root = Path(manifest["root"])
        for path in (args.baseline, args.output):
            if not path.is_absolute() or not path.is_relative_to(root) or path == root:
                raise ValueError("evidence must stay inside the dedicated runtime root")
        fleet.private(args.baseline)
        fleet.private(args.output.parent, directory=True)
        if os.path.lexists(args.output):
            raise ValueError("restart evidence is never overwritten")
        baseline = fleet.load(args.baseline)
        result = converge(manifest, baseline, args.timeout_seconds)
        # Evidence publication is exclusive; no chain data or fixture files change.
        with os.fdopen(os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "w") as output:
            json.dump(result, output, indent=2)
        print(json.dumps({"result": str(args.output), "kind": result["kind"]}))
        return 0
    except (OSError, ValueError, KeyError, TypeError, http.client.HTTPException) as error:
        print(json.dumps({"kind": "nonzero-canonical-convergence-check-not-full-replay-proof", "error": str(error)}))
        return 1


if __name__ == "__main__":
    sys.exit(main())
