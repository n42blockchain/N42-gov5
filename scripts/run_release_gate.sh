#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
result_root="${RELEASE_RESULTS_DIR:-$repo_root/build/release-check}"
run_id="${RELEASE_RUN_ID:-$(date -u +"%Y%m%d-%H%M%SZ")}"
run_dir="$result_root/$run_id"
rows_file="$run_dir/rows.md"
summary_file="$run_dir/summary.md"
overall_rc=0

mkdir -p "$run_dir"
: >"$rows_file"

record_step() {
  local name="$1"
  local status="$2"
  local duration_seconds="$3"
  local log_file="$4"
  local command="$5"
  printf '| `%s` | `%s` | %ss | `%s` | `%s` |\n' \
    "$name" "$status" "$duration_seconds" "$command" "$(basename "$log_file")" >>"$rows_file"
}

run_step() {
  local name="$1"
  local log_file="$2"
  shift 2

  local start_ts end_ts duration_seconds status
  start_ts="$(date +%s)"
  if (
    cd "$repo_root"
    "$@"
  ) >"$log_file" 2>&1; then
    status="PASS"
  else
    status="FAIL"
    overall_rc=1
  fi
  end_ts="$(date +%s)"
  duration_seconds=$((end_ts - start_ts))
  record_step "$name" "$status" "$duration_seconds" "$log_file" "$*"
  if [ "$status" = "FAIL" ]; then
    return 1
  fi
  return 0
}

run_step maturity-baseline "$run_dir/maturity-baseline.log" bash scripts/run_maturity_baseline.sh --full
run_step ops-smoke "$run_dir/ops-smoke.log" bash scripts/run_ops_smoke.sh
run_step interop-smoke "$run_dir/interop-smoke.log" bash scripts/run_interop_smoke.sh
run_step soak-smoke "$run_dir/soak-smoke.log" bash scripts/run_soak_smoke.sh

{
  echo "# N42 Release Check"
  echo
  echo "- Generated at: \`$(date -u +"%Y-%m-%d %H:%M:%SZ")\`"
  echo "- Run dir: \`$run_dir\`"
  echo "- Interop node mode: \`--ethdev\`"
  echo "- Overall status: \`$( [[ $overall_rc -eq 0 ]] && echo PASS || echo FAIL )\`"
  echo
  echo "| Step | Status | Duration | Command | Log |"
  echo "|---|---|---:|---|---|"
  cat "$rows_file"
} >"$summary_file"

echo "summary=$summary_file"
echo "run_dir=$run_dir"
exit "$overall_rc"
