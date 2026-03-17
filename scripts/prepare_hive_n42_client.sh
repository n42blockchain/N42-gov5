#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
client_dir="$repo_root/tests/eth-hive/clients/n42"
local_src="$client_dir/n42-local"
client_file="$repo_root/tests/eth-hive/n42-clients.yaml"

mkdir -p "$client_dir"
rm -rf "$local_src"
mkdir -p "$local_src"

rsync -a --delete \
  --exclude '.git/' \
  --exclude '.codex-cache/' \
  --exclude 'AGENTS.md' \
  --exclude 'benchmarks/results/' \
  --exclude 'build/' \
  --exclude 'devtest/' \
  --exclude 'mainnet/' \
  --exclude 'n42data/' \
  --exclude 'tests/' \
  --exclude 'docs/' \
  --exclude 'tmp/' \
  --exclude 'tests/eth-hive/clients/n42/n42-local/' \
  "$repo_root/" "$local_src/"

cat >"$client_file" <<'EOF'
- client: n42
  nametag: local
  dockerfile: local
  build_args:
    local_path: n42-local
EOF

printf 'local_path=%s\n' "$local_src"
printf 'client_file=%s\n' "$client_file"
