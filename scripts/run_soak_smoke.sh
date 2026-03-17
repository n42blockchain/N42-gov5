#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
source "$repo_root/scripts/lib_node_smoke.sh"

result_root="${SOAK_RESULTS_DIR:-$repo_root/build/soak-smoke}"
cycles="${SOAK_CYCLES:-3}"
duration="${SOAK_STRESS_DURATION:-10}"
tps="${SOAK_STRESS_TPS:-5}"
http_port="${SOAK_HTTP_PORT:-38555}"
metrics_port="${SOAK_METRICS_PORT:-39071}"
pprof_port="${SOAK_PPROF_PORT:-39070}"
run_id="${SOAK_RUN_ID:-$(smoke_timestamp)}"
run_dir="$result_root/$run_id"

usage() {
  cat <<'EOF'
Usage: scripts/run_soak_smoke.sh [--result-dir DIR] [--cycles N] [--duration SEC] [--tps N]
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --result-dir)
      result_root="$2"
      run_dir="$result_root/$run_id"
      shift 2
      ;;
    --cycles)
      cycles="$2"
      shift 2
      ;;
    --duration)
      duration="$2"
      shift 2
      ;;
    --tps)
      tps="$2"
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

mkdir -p "$run_dir"
rows_file="$run_dir/rows.md"
summary_file="$run_dir/summary.md"
: >"$rows_file"

n42_bin="$run_dir/n42"
stresstest_bin="$run_dir/stresstest"
data_dir="$run_dir/data"
password_file="$run_dir/password.txt"
node_log="$run_dir/node.log"
rpc_url="http://127.0.0.1:$http_port"
metrics_url="http://127.0.0.1:$metrics_port/debug/metrics/prometheus"
pprof_url="http://127.0.0.1:$pprof_port/debug/pprof/goroutine?debug=1"
node_pid=""
etherbase=""
overall_rc=0
last_block_dec=-1

cleanup() {
  smoke_stop_process "$node_pid"
}
trap cleanup EXIT

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
  if "$@" >"$log_file" 2>&1; then
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

build_node_step() {
  smoke_build_binary "$n42_bin" ./cmd/n42
}

build_stresstest_step() {
  smoke_build_binary "$stresstest_bin" ./cmd/stresstest
}

prepare_account_step() {
  mkdir -p "$data_dir"
  : >"$password_file"
  etherbase="$(smoke_create_account "$n42_bin" "$data_dir" "$password_file")"
}

start_cycle_step() {
  node_pid="$(smoke_start_dev_node "$n42_bin" "$data_dir" "$password_file" "$etherbase" "$http_port" "$metrics_port" "$pprof_port" "$node_log")"
  smoke_wait_for_rpc "$rpc_url" "$node_pid" "$node_log" 60
  smoke_wait_for_http "$metrics_url" "$node_pid" "$node_log" 30
  smoke_wait_for_http "$pprof_url" "$node_pid" "$node_log" 30

  local response block_hex block_dec
  response="$(smoke_rpc_assert_result "$rpc_url" eth_blockNumber "[]")"
  block_hex="$(smoke_extract_hex_result "$response")"
  block_dec="$(smoke_hex_to_dec "$block_hex")"
  if [ "$last_block_dec" -ge 0 ] && [ "$block_dec" -lt "$last_block_dec" ]; then
    echo "block number regressed: $block_dec < $last_block_dec" >&2
    return 1
  fi
  last_block_dec="$block_dec"
  printf 'block=%s (%s)\n' "$block_hex" "$block_dec"
}

load_cycle_step() {
  "$stresstest_bin" --rpc "$rpc_url" --tps "$tps" --duration "$duration"
  local response block_hex
  response="$(smoke_rpc_assert_result "$rpc_url" eth_blockNumber "[]")"
  block_hex="$(smoke_extract_hex_result "$response")"
  printf 'post_load_block=%s\n' "$block_hex"
}

stop_cycle_step() {
  smoke_stop_process "$node_pid"
  node_pid=""
}

run_step build-node "$run_dir/build-node.log" build_node_step || true
run_step build-stresstest "$run_dir/build-stresstest.log" build_stresstest_step || true
run_step prepare-account "$run_dir/prepare-account.log" prepare_account_step || true

for cycle in $(seq 1 "$cycles"); do
  run_step "cycle-${cycle}-start" "$run_dir/cycle-${cycle}-start.log" start_cycle_step || true
  run_step "cycle-${cycle}-load" "$run_dir/cycle-${cycle}-load.log" load_cycle_step || true
  run_step "cycle-${cycle}-stop" "$run_dir/cycle-${cycle}-stop.log" stop_cycle_step || true
done

{
  echo "# N42 Soak Smoke"
  echo
  echo "- Generated at: \`$(date -u +"%Y-%m-%d %H:%M:%SZ")\`"
  echo "- Run dir: \`$run_dir\`"
  echo "- Cycles: \`$cycles\`"
  echo "- Stress duration per cycle: \`$duration\`"
  echo "- Stress TPS: \`$tps\`"
  echo "- Overall status: \`$( [[ $overall_rc -eq 0 ]] && echo PASS || echo FAIL )\`"
  echo
  echo "| Step | Status | Duration | Command | Log |"
  echo "|---|---|---:|---|---|"
  cat "$rows_file"
} >"$summary_file"

echo "summary=$summary_file"
echo "run_dir=$run_dir"
exit "$overall_rc"
