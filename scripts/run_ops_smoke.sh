#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
source "$repo_root/scripts/lib_node_smoke.sh"

result_root="${OPS_RESULTS_DIR:-$repo_root/build/ops-smoke}"
duration="${OPS_STRESS_DURATION:-15}"
tps="${OPS_STRESS_TPS:-5}"
http_port="${OPS_HTTP_PORT:-38545}"
metrics_port="${OPS_METRICS_PORT:-39061}"
pprof_port="${OPS_PPROF_PORT:-39060}"
run_id="${OPS_RUN_ID:-$(smoke_timestamp)}"
run_dir="$result_root/$run_id"

usage() {
  cat <<'EOF'
Usage: scripts/run_ops_smoke.sh [--result-dir DIR] [--duration SEC] [--tps N]
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --result-dir)
      result_root="$2"
      run_dir="$result_root/$run_id"
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

start_node_step() {
  mkdir -p "$data_dir"
  : >"$password_file"
  etherbase="$(smoke_create_account "$n42_bin" "$data_dir" "$password_file")"
  node_pid="$(smoke_start_dev_node "$n42_bin" "$data_dir" "$password_file" "$etherbase" "$http_port" "$metrics_port" "$pprof_port" "$node_log")"
  smoke_wait_for_rpc "$rpc_url" "$node_pid" "$node_log" 60
}

rpc_smoke_step() {
  smoke_rpc_assert_result "$rpc_url" eth_blockNumber "[]" >/dev/null
  smoke_rpc_assert_result "$rpc_url" eth_chainId "[]" >/dev/null
  smoke_rpc_assert_result "$rpc_url" txpool_content "[]" >/dev/null
  smoke_rpc_assert_result "$rpc_url" rpc_modules "[]" >/dev/null
}

metrics_step() {
  smoke_wait_for_http "$metrics_url" "$node_pid" "$node_log" 30
  local metrics
  metrics="$(curl -sf "$metrics_url")"
  local goroutines_count
  goroutines_count="$(printf '%s\n' "$metrics" | rg -c '^go_goroutines ')"
  if [ "$goroutines_count" -ne 1 ]; then
    echo "expected exactly one go_goroutines sample, got $goroutines_count" >&2
    return 1
  fi
  printf '%s\n' "$metrics" | rg '^rpc_duration_seconds' >/dev/null
}

pprof_step() {
  smoke_wait_for_http "$pprof_url" "$node_pid" "$node_log" 30
  curl -sf "$pprof_url" | head -n 5
}

stress_step() {
  "$stresstest_bin" --rpc "$rpc_url" --tps "$tps" --duration "$duration"
}

shutdown_step() {
  smoke_stop_process "$node_pid"
  node_pid=""
}

run_step build-node "$run_dir/build-node.log" build_node_step || true
run_step build-stresstest "$run_dir/build-stresstest.log" build_stresstest_step || true
run_step start-node "$run_dir/start-node.log" start_node_step || true
run_step rpc-smoke "$run_dir/rpc-smoke.log" rpc_smoke_step || true
run_step metrics "$run_dir/metrics.log" metrics_step || true
run_step pprof "$run_dir/pprof.log" pprof_step || true
run_step stress "$run_dir/stress.log" stress_step || true
run_step metrics-postload "$run_dir/metrics-postload.log" metrics_step || true
run_step shutdown "$run_dir/shutdown.log" shutdown_step || true

{
  echo "# N42 Ops Smoke"
  echo
  echo "- Generated at: \`$(date -u +"%Y-%m-%d %H:%M:%SZ")\`"
  echo "- Run dir: \`$run_dir\`"
  echo "- RPC URL: \`$rpc_url\`"
  echo "- Metrics URL: \`$metrics_url\`"
  echo "- Pprof URL: \`$pprof_url\`"
  echo "- Stress duration: \`$duration\`"
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
