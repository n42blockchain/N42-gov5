#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
result_root="${RELEASE_RESULTS_DIR:-$repo_root/build/release-check}"
run_id="${RELEASE_RUN_ID:-$(date -u +"%Y%m%d-%H%M%SZ")}"
stub_mode="${RELEASE_GATE_STUB:-0}"
stub_fail_step="${RELEASE_GATE_STUB_FAIL_STEP:-}"
run_dir="$result_root/$run_id"
overall_rc=0

usage() {
  cat <<'EOF'
Usage: scripts/run_release_gate.sh [--result-dir DIR]
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --result-dir)
      result_root="$2"
      run_dir="$result_root/$run_id"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

rows_file="$run_dir/rows.md"
summary_file="$run_dir/summary.md"

mkdir -p "$run_dir"
: >"$rows_file"

run_stub_step() {
  local step="$1"

  if [[ "$stub_mode" != "1" ]]; then
    return 2
  fi

  case "$step" in
    maturity-baseline|eest-audit|ops-smoke|interop-smoke|soak-smoke)
      echo "stubbed $step"
      ;;
    *)
      echo "unknown stub step: $step" >&2
      return 1
      ;;
  esac

  if [[ -n "$stub_fail_step" && "$stub_fail_step" == "$step" ]]; then
    echo "stubbed failure at $step" >&2
    return 1
  fi
  return 0
}

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
  if [[ "$stub_mode" == "1" ]]; then
    if run_stub_step "$name" >"$log_file" 2>&1; then
      status="PASS"
    else
      status="FAIL"
      overall_rc=1
    fi
  elif (
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

run_step maturity-baseline "$run_dir/maturity-baseline.log" bash scripts/run_maturity_baseline.sh --full || true
run_step eest-audit "$run_dir/eest-audit.log" bash scripts/audit_eest_results.sh || true
run_step ops-smoke "$run_dir/ops-smoke.log" bash scripts/run_ops_smoke.sh || true
run_step interop-smoke "$run_dir/interop-smoke.log" bash scripts/run_interop_smoke.sh || true
run_step soak-smoke "$run_dir/soak-smoke.log" bash scripts/run_soak_smoke.sh || true

{
  echo "# N42 Release Check"
  echo
  echo "- Generated at: \`$(date -u +"%Y-%m-%d %H:%M:%SZ")\`"
  echo "- Run dir: \`$run_dir\`"
  echo "- Interop node mode: \`--ethdev\`"
  echo "- EEST result audit: \`scripts/audit_eest_results.sh\`"
  echo "- Overall status: \`$( [[ $overall_rc -eq 0 ]] && echo PASS || echo FAIL )\`"
  echo
  echo "| Step | Status | Duration | Command | Log |"
  echo "|---|---|---:|---|---|"
  cat "$rows_file"
} >"$summary_file"

echo "summary=$summary_file"
echo "run_dir=$run_dir"
exit "$overall_rc"
