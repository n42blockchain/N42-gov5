#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
hive_dir="$repo_root/tests/eth-hive"
client_file="$repo_root/tests/eth-hive/n42-clients.yaml"
dev_addr="${HIVE_DEV_ADDR:-127.0.0.1:3000}"
client_name="${HIVE_CLIENT_NAME:-n42_local}"

"$repo_root/scripts/prepare_hive_n42_client.sh"

(
  cd "$hive_dir"
  go build -o build/bin/hive .
)

cd "$hive_dir"
exec ./build/bin/hive --dev --dev.addr "$dev_addr" --client-file "$client_file" --client "$client_name" --docker.output
