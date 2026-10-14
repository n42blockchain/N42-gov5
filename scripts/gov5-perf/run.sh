#!/usr/bin/env bash
# Stage 0 is check-only: intentionally no node launcher or mutation mode.
set -euo pipefail
task_script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
exec python3 "$task_script_dir/preflight.py" "$@"
