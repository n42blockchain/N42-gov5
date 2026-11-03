#!/usr/bin/env python3
"""Check benchmark execution permissions before touching existing node state."""
import argparse
import json
import os
import socket
import sys
import tempfile
from pathlib import Path


def check(args):
    errors = []
    for name, kind in (("TCP", socket.SOCK_STREAM), ("UDP", socket.SOCK_DGRAM)):
        try:
            with socket.socket(socket.AF_INET, kind) as probe:
                probe.bind(("127.0.0.1", 0))
                if kind == socket.SOCK_STREAM:
                    probe.listen(1)
        except OSError as exc:
            errors.append(f"{name} loopback unavailable: {exc}")
    if not args.bin.is_file() or not os.access(args.bin, os.X_OK):
        errors.append(f"node executable unavailable: {args.bin}")
    directories = {args.root}
    for i in range(7):
        node = Path(f"{args.node_root}{i}")
        if (node / "chaindata").is_dir():
            directories.update((node, node / "chaindata"))
        else:
            if not (args.seed / "chaindata").is_dir():
                errors.append(f"node {i}: no existing chaindata or seed {args.seed}")
            directories.add(node if node.is_dir() else node.parent)
    for directory in sorted(directories):
        try:
            with tempfile.TemporaryFile(dir=directory):
                pass
        except OSError as exc:
            errors.append(f"benchmark directory not writable: {directory}: {exc}")
    return dict(status="failed" if errors else "passed", errors=errors,
                root=str(args.root), node_root=str(args.node_root), binary=str(args.bin),
                cpus=len(os.sched_getaffinity(0)))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--node-root", type=Path, required=True)
    parser.add_argument("--seed", type=Path, required=True)
    parser.add_argument("--bin", type=Path, required=True)
    args = parser.parse_args()
    report = check(args)
    print(json.dumps(report, indent=2))
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
