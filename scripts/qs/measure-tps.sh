#!/usr/bin/env bash
# Strict TPS measurement; defaults to checking all seven qs nodes.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./qs-env.sh
exec python3 ./measure-tps.py --port "$QS_HTTP_BASE" "$@"
