#!/usr/bin/env bash

set -euo pipefail

# consume-* collection is an Ethereum execution-layer check through Hive;
# project-wide Go checks are intentionally kept in the Make targets.

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
eest_dir="${EEST_DIR:-$repo_root/tests/eth-tests/execution-spec-tests}"
mode="${EEST_MODE:-fill}"
python_bin="${PYTHON_BIN:-3.13}"
test_root="${EEST_TEST_ROOT:-tests}"
input_path="${EEST_INPUT:-}"
hive_simulator="${HIVE_SIMULATOR:-}"
hive_stub="${EEST_HIVE_STUB:-0}"
hive_stub_host="${EEST_HIVE_STUB_HOST:-127.0.0.1}"
hive_stub_port="${EEST_HIVE_STUB_PORT:-3000}"
hive_stub_python="${EEST_HIVE_STUB_PYTHON:-python3}"
hive_stub_script="$repo_root/scripts/eest_hive_stub.py"
hive_stub_pid=''
hive_stub_log=''

requested_shards=("$@")

declare -a shard_rows=(
  $'paris+shanghai\tfork_Paris or fork_Shanghai\t.*fork_(Paris|Shanghai).*\t.*/.*fork_(Paris\\|Shanghai)\t~2,600\tstable@latest'
  $'cancun\tfork_Cancun\t.*fork_Cancun.*\t.*/.*fork_Cancun\t~17,250\tstable@latest'
  $'prague\tfork_Prague\t.*fork_Prague.*\t.*/.*fork_Prague\t~20,500\tstable@latest'
  $'osaka\tfork_Osaka\t.*fork_Osaka.*\t.*/.*fork_Osaka\t~21,000\tdevelop@latest'
  $'engine-access-list\teip2930_access_list\t.*eip2930_access_list.*\t.*eip2930_access_list.*\tunchanged\tstable@latest\tengine'
  $'rlp\tblockchain\t.*\t.*\tall BlockchainFixture cases\tstable@latest\trlp'
)

want_shard() {
  local shard="$1"
  if [ "${#requested_shards[@]}" -eq 0 ]; then
    return 0
  fi
  local requested
  for requested in "${requested_shards[@]}"; do
    if [ "$requested" = "$shard" ]; then
      return 0
    fi
  done
  return 1
}

resolve_input_path() {
  local shard="$1"
  local shard_default_input="$2"

  if [ -n "$input_path" ]; then
    # Stable release fixtures currently stop at Prague, so Osaka engine tests
    # need the develop artifact even when the rest of the matrix uses stable.
    if [ "$shard" = "osaka" ] && [ "$input_path" = "stable@latest" ]; then
      printf '%s\n' "${EEST_OSAKA_INPUT:-$shard_default_input}"
      return 0
    fi
    printf '%s\n' "$input_path"
    return 0
  fi

  printf '%s\n' "$shard_default_input"
}

run_collect() {
  local shard="$1"
  local fill_expr="$2"
  local sim_limit_expr="$3"
  local shard_default_input="$4"
  local runner="${5:-engine}"
  local shard_input_path=''
  case "$mode" in
    fill)
      uv run --python "$python_bin" fill "$test_root" --collect-only -q -k "$fill_expr"
      ;;
    consume-engine)
      shard_input_path="$(resolve_input_path "$shard" "$shard_default_input")"
      if [ -z "$shard_input_path" ]; then
        echo "EEST_INPUT is required when EEST_MODE=consume-engine" >&2
        return 2
      fi
      if [ -z "$hive_simulator" ]; then
        echo "HIVE_SIMULATOR is required when EEST_MODE=consume-engine" >&2
        return 2
      fi
      case "$runner" in
        engine)
          HIVE_SIMULATOR="$hive_simulator" \
            uv run --python "$python_bin" consume engine --input "$shard_input_path" --sim.limit "collectonly:$sim_limit_expr"
          ;;
        rlp)
          HIVE_SIMULATOR="$hive_simulator" \
            uv run --python "$python_bin" consume rlp --input "$shard_input_path" --sim.limit "collectonly:$sim_limit_expr"
          ;;
        *)
          echo "Unsupported EEST runner for shard $shard: $runner" >&2
          return 2
          ;;
      esac
      ;;
    *)
      echo "Unsupported EEST_MODE: $mode" >&2
      return 2
      ;;
  esac
}

cleanup_hive_stub() {
  if [ -n "$hive_stub_pid" ] && kill -0 "$hive_stub_pid" 2>/dev/null; then
    kill "$hive_stub_pid" 2>/dev/null || true
    wait "$hive_stub_pid" 2>/dev/null || true
  fi
  if [ -n "$hive_stub_log" ]; then
    rm -f "$hive_stub_log"
  fi
}

wait_for_hive_stub() {
  local url="$1"
  local attempt
  for attempt in $(seq 1 50); do
    if "$hive_stub_python" -c 'import sys, urllib.request; urllib.request.urlopen(sys.argv[1], timeout=1).read()' "$url/clients" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.1
  done
  return 1
}

start_hive_stub_if_needed() {
  if [ "$mode" != "consume-engine" ] || [ "$hive_stub" != "1" ]; then
    return 0
  fi
  if [ -z "$hive_simulator" ]; then
    hive_simulator="http://$hive_stub_host:$hive_stub_port"
  fi
  hive_stub_log="$(mktemp)"
  "$hive_stub_python" "$hive_stub_script" --host "$hive_stub_host" --port "$hive_stub_port" >"$hive_stub_log" 2>&1 &
  hive_stub_pid="$!"
  if wait_for_hive_stub "$hive_simulator"; then
    return 0
  fi
  echo "failed to start Hive stub at $hive_simulator" >&2
  cat "$hive_stub_log" >&2 || true
  cleanup_hive_stub
  return 1
}

trap cleanup_hive_stub EXIT

start_hive_stub_if_needed

printf '%s\n' '| Shard | Selector | Target ~Tests | Runner | Count | Status |'
printf '%s\n' '|-------|----------|---------------|--------|-------|--------|'

for row in "${shard_rows[@]}"; do
  IFS=$'\t' read -r shard fill_expr sim_limit_expr selector target shard_default_input runner <<<"$row"
  if ! want_shard "$shard"; then
    continue
  fi

  tmp_file="$(mktemp)"
  err_file="$(mktemp)"
  status='ok'
  (
    cd "$eest_dir"
    set +e
    run_collect "$shard" "$fill_expr" "$sim_limit_expr" "$shard_default_input" "$runner" >"$tmp_file" 2>"$err_file"
    rc=$?
    if [ "$rc" -ne 0 ]; then
      echo "$rc" >"$tmp_file.rc"
    fi
  )
  if [ -f "$tmp_file.rc" ]; then
    status="rc=$(cat "$tmp_file.rc")"
    rm -f "$tmp_file.rc"
  fi
  regex_selected_line="$(grep -E 'pytest-regex selected [0-9]+ tests to run for regex:' "$tmp_file" | tail -n 1 || true)"
  summary_line="$(grep -E '[0-9]+(/[0-9]+)? tests collected' "$tmp_file" | tail -n 1 || true)"
  if [ "$mode" = "consume-engine" ] && [ -n "$regex_selected_line" ]; then
    count="$(printf '%s\n' "$regex_selected_line" | awk '{print $3}')"
  elif [ -n "$summary_line" ]; then
    count_token="$(printf '%s\n' "$summary_line" | awk '{print $1}')"
    count="${count_token%%/*}"
  elif grep -Eq '^no tests collected ' "$tmp_file"; then
    count='0'
  else
    count="$(wc -l <"$tmp_file" | tr -d ' ')"
  fi
  if [ "$status" != 'ok' ] && [ "$count" -eq 0 ]; then
    last_error="$(tail -n 1 "$err_file" || true)"
    if [ -z "$last_error" ]; then
      last_error="$(grep -E '(no tests collected|ERROR:|INTERNALERROR:)' "$tmp_file" | tail -n 1 || true)"
    fi
    if [ -n "$last_error" ]; then
      status="$status: $last_error"
    fi
  fi
  rm -f "$tmp_file"
  rm -f "$err_file"

  runner="${runner:-engine}"
  printf '| %s | `%s` | %s | `%s` | `%s` | `%s` |\n' "$shard" "$selector" "$target" "$runner" "$count" "$status"
done
