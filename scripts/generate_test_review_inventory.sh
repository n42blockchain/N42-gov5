#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
output_path="${1:-$repo_root/docs/engineering/TEST_REVIEW_FILE_INVENTORY.md}"

python3 - "$repo_root" "$output_path" <<'PY'
from __future__ import annotations

import datetime as dt
import subprocess
import sys
from collections import Counter, defaultdict
from pathlib import Path

repo = Path(sys.argv[1])
output = Path(sys.argv[2])

raw_files = sorted(
    f for f in subprocess.check_output(
        ["rg", "--files", "-g", "*_test.go"], cwd=repo, text=True
    ).splitlines() if f
)
tracked_files = sorted(
    f for f in subprocess.check_output(
        ["git", "ls-files", "*_test.go"], cwd=repo, text=True
    ).splitlines() if f
)
tracked_set = set(tracked_files)
workspace_only = sorted(f for f in raw_files if f not in tracked_set)

top_counter = Counter(path.split("/", 1)[0] if "/" in path else "." for path in tracked_files)
module_counter = Counter(
    "/".join(path.split("/")[:2]) if "/" in path else path
    for path in tracked_files
)

section_order = [
    "Phase 1 Entry / Config / Tooling",
    "Phase 2 Execution / State / Storage",
    "Phase 3 Consensus / Network / RPC / Node",
    "Phase 4 Crypto / ZK / Contracts",
    "Phase 5 Base Libraries",
    "Phase 6 Repo-Owned Integration / Harness",
    "Unclassified",
]


def section_for(path: str) -> str:
    if path.startswith(("cmd/", "accounts/", "api/protocol/", "conf/", "log/", "params/", "pkg/", "tools/", "utils/")):
        return "Phase 1 Entry / Config / Tooling"

    if path.startswith("contracts/"):
        return "Phase 4 Crypto / ZK / Contracts"

    if path.startswith("tests/"):
        return "Phase 6 Repo-Owned Integration / Harness"

    if path.startswith("common/crypto/") or path.startswith("lib/crypto/"):
        return "Phase 4 Crypto / ZK / Contracts"

    if path.startswith(("modules/state/", "modules/rawdb/", "modules/ethdb/")):
        return "Phase 2 Execution / State / Storage"
    if path.startswith(("modules/rpc/", "modules/event/")):
        return "Phase 3 Consensus / Network / RPC / Node"
    if path.startswith("modules/"):
        return "Phase 2 Execution / State / Storage"

    if path.startswith("common/"):
        return "Phase 2 Execution / State / Storage"

    if path.startswith("internal/zkprover/") or path.startswith("internal/zkverifier/"):
        return "Phase 4 Crypto / ZK / Contracts"
    if path.startswith((
        "internal/api/",
        "internal/consensus/",
        "internal/p2p/",
        "internal/sync/",
        "internal/node/",
        "internal/network/",
        "internal/miner/",
        "internal/tracing/",
        "internal/tracers/",
        "internal/download/",
        "internal/metrics/",
        "internal/mcp/",
        "internal/bundler/",
        "internal/peerdas/",
        "internal/parallel/",
        "internal/exex/",
        "internal/core/",
        "internal/amcdb/",
    )):
        return "Phase 3 Consensus / Network / RPC / Node"
    if path.startswith("internal/"):
        return "Phase 2 Execution / State / Storage"

    if path.startswith(("lib/state/", "lib/kv/", "lib/commitment/", "lib/jmt/")):
        return "Phase 2 Execution / State / Storage"
    if path.startswith("lib/"):
        return "Phase 5 Base Libraries"

    return "Unclassified"


sectioned: dict[str, list[str]] = defaultdict(list)
for path in tracked_files:
    sectioned[section_for(path)].append(path)

external_assets = []
for rel in [
    "tests/eth-tests",
    "tests/eth-hive",
    "tests/eth-execution-apis",
    "tests/eth-devp2p",
]:
    if (repo / rel).exists():
        external_assets.append(rel)

lines: list[str] = []
lines.append("# Test Review File Inventory")
lines.append("")
lines.append(f"Generated: `{dt.date.today().isoformat()}`")
lines.append("")
lines.append("## Summary")
lines.append("")
lines.append(f"- Raw workspace `*_test.go`: `{len(raw_files)}`")
lines.append(f"- Formal tracked `*_test.go`: `{len(tracked_files)}`")
lines.append(f"- Workspace-only `*_test.go`: `{len(workspace_only)}`")
lines.append("")
lines.append("## Top-Level Distribution")
lines.append("")
for key, value in sorted(top_counter.items()):
    lines.append(f"- `{key}`: `{value}`")
lines.append("")
lines.append("## High-Density Modules")
lines.append("")
for key, value in module_counter.most_common(30):
    lines.append(f"- `{key}`: `{value}`")
lines.append("")

if workspace_only:
    lines.append("## Workspace-Only Tests")
    lines.append("")
    for path in workspace_only:
        lines.append(f"- `{path}`")
    lines.append("")

lines.append("## Ordered Review Inventory")
lines.append("")
for section in section_order:
    files = sectioned.get(section, [])
    if not files:
        continue
    lines.append(f"### {section}")
    lines.append("")
    module_groups: dict[str, list[str]] = defaultdict(list)
    for path in files:
        module = "/".join(path.split("/")[:2]) if "/" in path else path
        module_groups[module].append(path)
    for module in sorted(module_groups):
        module_files = sorted(module_groups[module])
        lines.append(f"- `{module}`: `{len(module_files)}`")
        for path in module_files:
            lines.append(f"  - `{path}`")
    lines.append("")

lines.append("## External Mirrored Test Assets")
lines.append("")
if external_assets:
    lines.append("These directories exist locally but are treated as execution assets, not line-by-line primary test code review targets:")
    lines.append("")
    for rel in external_assets:
        lines.append(f"- `{rel}`")
else:
    lines.append("No mirrored external test asset directories detected.")
lines.append("")

output.parent.mkdir(parents=True, exist_ok=True)
output.write_text("\n".join(lines), encoding="utf-8")
PY
