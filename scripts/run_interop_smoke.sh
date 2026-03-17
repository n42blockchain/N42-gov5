#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
source "$repo_root/scripts/lib_node_smoke.sh"

result_root="${INTEROP_RESULTS_DIR:-$repo_root/build/interop-smoke}"
http_port="${INTEROP_HTTP_PORT:-38565}"
metrics_port="${INTEROP_METRICS_PORT:-39081}"
pprof_port="${INTEROP_PPROF_PORT:-39080}"
run_id="${INTEROP_RUN_ID:-$(smoke_timestamp)}"
run_dir="$result_root/$run_id"

usage() {
  cat <<'EOF'
Usage: scripts/run_interop_smoke.sh [--result-dir DIR]
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

mkdir -p "$run_dir"
rows_file="$run_dir/rows.md"
summary_file="$run_dir/summary.md"
: >"$rows_file"

n42_bin="$run_dir/n42"
data_dir="$run_dir/data"
password_file="$run_dir/password.txt"
node_log="$run_dir/node.log"
rpc_url="http://127.0.0.1:$http_port"
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
  smoke_rpc_assert_result "$rpc_url" eth_getBlockByNumber '["latest", false]' >/dev/null
  smoke_rpc_assert_result "$rpc_url" eth_getBalance '["0x0000000000000000000000000000000000000000","latest"]' >/dev/null
  smoke_rpc_assert_result "$rpc_url" eth_feeHistory '[4,"latest",[25,75]]' >/dev/null
}

blockscout_smoke_step() {
  smoke_rpc_assert_result "$rpc_url" eth_getCode '["0x0000000000000000000000000000000000000000","latest"]' >/dev/null
  smoke_rpc_assert_result "$rpc_url" eth_getLogs '[{"fromBlock":"0x0","toBlock":"latest"}]' >/dev/null
  smoke_rpc_assert_result "$rpc_url" eth_call '[{"to":"0x0000000000000000000000000000000000000000"},"latest"]' >/dev/null
  smoke_rpc_assert_result "$rpc_url" txpool_content '[]' >/dev/null
}

hive_auth_step() {
  local results_dir="$run_dir/hive-auth-results"
  local attempt
  mkdir -p "$results_dir"
  for attempt in 1 2 3; do
    echo "hive engine-auth attempt=$attempt"
    (
      cd "$repo_root"
      scripts/prepare_hive_n42_client.sh
      cd tests/eth-hive
      ./build/bin/hive -cleanup >/dev/null 2>&1 || true
      ./build/bin/hive \
        --client-file ./n42-clients.yaml \
        --client n42_local \
        --sim ethereum/engine \
        --sim.limit 'engine-auth/' \
        --sim.parallelism 1 \
        --results-root "$results_dir" \
        --docker.output
    ) && return 0
    if [ "$attempt" -lt 3 ]; then
      echo "hive engine-auth attempt=$attempt failed, retrying after cleanup" >&2
      (
        cd "$repo_root/tests/eth-hive"
        ./build/bin/hive -cleanup >/dev/null 2>&1 || true
      )
      sleep 2
    fi
  done
  return 1
}

eest_collect_step() {
  local results_dir="$run_dir/eest-collect"
  mkdir -p "$results_dir"
  (
    cd "$repo_root"
    EEST_MODE=consume-engine \
    EEST_INPUT=stable@latest \
    EEST_HIVE_STUB=1 \
    EEST_RESULTS_DIR="$results_dir" \
    EEST_PYTEST_WORKERS=1 \
    scripts/collect_eest_shards.sh paris+shanghai
  )
}

shutdown_step() {
  smoke_stop_process "$node_pid"
  node_pid=""
}

run_step build-node "$run_dir/build-node.log" build_node_step || true
run_step start-node "$run_dir/start-node.log" start_node_step || true
run_step rpc-smoke "$run_dir/rpc-smoke.log" rpc_smoke_step || true
run_step blockscout-smoke "$run_dir/blockscout-smoke.log" blockscout_smoke_step || true
run_step hive-engine-auth "$run_dir/hive-engine-auth.log" hive_auth_step || true
run_step eest-collect "$run_dir/eest-collect.log" eest_collect_step || true
run_step shutdown "$run_dir/shutdown.log" shutdown_step || true

{
  echo "# N42 Interop Smoke"
  echo
  echo "- Generated at: \`$(date -u +"%Y-%m-%d %H:%M:%SZ")\`"
  echo "- Run dir: \`$run_dir\`"
  echo "- RPC URL: \`$rpc_url\`"
  echo "- Hive auth: \`tests/eth-hive --sim ethereum/engine --sim.limit engine-auth/\`"
  echo "- EEST collect input: \`stable@latest\`"
  echo "- Overall status: \`$( [[ $overall_rc -eq 0 ]] && echo PASS || echo FAIL )\`"
  echo
  echo "| Step | Status | Duration | Command | Log |"
  echo "|---|---|---:|---|---|"
  cat "$rows_file"
} >"$summary_file"

echo "summary=$summary_file"
echo "run_dir=$run_dir"
exit "$overall_rc"
