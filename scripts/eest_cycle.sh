#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"

name=""
run_cmd=""
on_fail_cmd=""
verify_cmd=""
commit_cmd=""
push_cmd=""
on_pass_cmd=""
interval="${EEST_WATCH_INTERVAL:-300}"
max_cycles="${EEST_MAX_CYCLES:-0}"
log_dir=""
state_dir=""

usage() {
  cat <<'USAGE' >&2
Usage:
  eest_cycle.sh --name <run-name> --run <command>
                [--interval <seconds>] [--max-cycles <n>]
                [--log-dir <dir>] [--state-dir <dir>]
                [--on-fail <command>] [--verify <command>]
                [--commit <command>] [--push <command>] [--on-pass <command>]

Behavior:
  - Creates a fresh log file per cycle and exports it as EEST_LOG_FILE.
  - Starts watch_eest_log.sh in parallel for each cycle.
  - Repeats until the run finishes with zero failures/errors and a pytest summary.
  - On failure, runs the optional hooks in this order:
      on-fail -> verify -> commit -> push
USAGE
  exit 1
}

while [ $# -gt 0 ]; do
  case "$1" in
    --name)
      name="${2:-}"
      shift 2
      ;;
    --run)
      run_cmd="${2:-}"
      shift 2
      ;;
    --interval)
      interval="${2:-}"
      shift 2
      ;;
    --max-cycles)
      max_cycles="${2:-}"
      shift 2
      ;;
    --log-dir)
      log_dir="${2:-}"
      shift 2
      ;;
    --state-dir)
      state_dir="${2:-}"
      shift 2
      ;;
    --on-fail)
      on_fail_cmd="${2:-}"
      shift 2
      ;;
    --verify)
      verify_cmd="${2:-}"
      shift 2
      ;;
    --commit)
      commit_cmd="${2:-}"
      shift 2
      ;;
    --push)
      push_cmd="${2:-}"
      shift 2
      ;;
    --on-pass)
      on_pass_cmd="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage
      ;;
  esac
done

[ -n "$name" ] || usage
[ -n "$run_cmd" ] || usage
printf '%s' "$interval" | grep -Eq '^[0-9]+$' || { echo "interval must be numeric" >&2; exit 1; }
printf '%s' "$max_cycles" | grep -Eq '^[0-9]+$' || { echo "max-cycles must be numeric" >&2; exit 1; }

if [ -z "$log_dir" ]; then
  log_dir="$PWD/tests/eth-tests/execution-spec-tests/logs"
fi
if [ -z "$state_dir" ]; then
  state_dir="$log_dir/cycle-$name"
fi
mkdir -p "$log_dir" "$state_dir"

summary_re='={5,} .* passed.*'
fail_re='(^|\]) FAILED( |$)|FAILED in |::.* FAILED( |$)'
error_re='(^|\]) ERROR( |$)|ERROR in |::.* ERROR( |$)'
pass_re='(^|\]) PASSED( |$)|PASSED in |::.* PASSED( |$)'

run_hook() {
  local label="$1"
  local cmd="$2"
  if [ -z "$cmd" ]; then
    return 0
  fi
  printf '[%s] %s\n' "$(date -u +'%Y-%m-%dT%H:%M:%SZ')" "$label" >>"$state_dir/cycle.log"
  bash -lc "$cmd"
}

cycle=1
while :; do
  if [ "$max_cycles" -gt 0 ] && [ "$cycle" -gt "$max_cycles" ]; then
    echo "max cycles reached: $max_cycles" >&2
    exit 1
  fi

  timestamp="$(date -u +'%Y%m%d-%H%M%SZ')"
  cycle_id="$(printf '%03d' "$cycle")"
  cycle_name="${timestamp}-${name}-c${cycle_id}"
  log_file="$log_dir/${cycle_name}.log"
  cycle_dir="$state_dir/$cycle_name"
  watch_dir="$cycle_dir/watch"
  mkdir -p "$cycle_dir" "$watch_dir"

  status_file="$watch_dir/status.md"
  history_file="$watch_dir/history.tsv"
  cycle_status_file="$cycle_dir/final-status.md"
  cycle_meta_file="$cycle_dir/result.env"

  printf '%s\n' "$log_file" >"$state_dir/current-log.txt"
  printf '%s\n' "$watch_dir" >"$state_dir/current-watch-dir.txt"

  export EEST_NAME="$name"
  export EEST_CYCLE="$cycle"
  export EEST_LOG_FILE="$log_file"
  export EEST_LOG_DIR="$log_dir"
  export EEST_STATE_DIR="$state_dir"
  export EEST_CYCLE_DIR="$cycle_dir"
  export EEST_WATCH_DIR="$watch_dir"
  export EEST_STATUS_FILE="$status_file"
  export EEST_HISTORY_FILE="$history_file"

  printf '[%s] cycle=%s log=%s\n' "$(date -u +'%Y-%m-%dT%H:%M:%SZ')" "$cycle" "$log_file" >>"$state_dir/cycle.log"
  : >"$log_file"

  "$script_dir/watch_eest_log.sh" "$log_file" --interval "$interval" --out-dir "$watch_dir" &
  watcher_pid=$!

  set +e
  bash -lc "$run_cmd"
  run_rc=$?
  set -e

  if kill -0 "$watcher_pid" 2>/dev/null; then
    kill "$watcher_pid" 2>/dev/null || true
  fi
  wait "$watcher_pid" 2>/dev/null || true

  "$script_dir/check_eest_log.sh" "$log_file" "$cycle_status_file"

  passed=$(grep -Ec "$pass_re" "$log_file" 2>/dev/null || true)
  failed=$(grep -Ec "$fail_re" "$log_file" 2>/dev/null || true)
  errors=$(grep -Ec "$error_re" "$log_file" 2>/dev/null || true)
  summary=$(grep -E "$summary_re" "$log_file" 2>/dev/null | tail -1 || true)
  first_failure=$(grep -E "$fail_re|$error_re" "$log_file" 2>/dev/null | head -1 || true)
  last_failure=$(grep -E "$fail_re|$error_re" "$log_file" 2>/dev/null | tail -1 || true)

  export EEST_PASSED="$passed"
  export EEST_FAILED="$failed"
  export EEST_ERRORS="$errors"
  export EEST_SUMMARY="$summary"
  export EEST_FIRST_FAILURE="$first_failure"
  export EEST_LAST_FAILURE="$last_failure"
  export EEST_RUN_RC="$run_rc"

  cat >"$cycle_meta_file" <<META
EEST_NAME=$(printf '%q' "$EEST_NAME")
EEST_CYCLE=$(printf '%q' "$EEST_CYCLE")
EEST_LOG_FILE=$(printf '%q' "$EEST_LOG_FILE")
EEST_LOG_DIR=$(printf '%q' "$EEST_LOG_DIR")
EEST_STATE_DIR=$(printf '%q' "$EEST_STATE_DIR")
EEST_CYCLE_DIR=$(printf '%q' "$EEST_CYCLE_DIR")
EEST_WATCH_DIR=$(printf '%q' "$EEST_WATCH_DIR")
EEST_STATUS_FILE=$(printf '%q' "$EEST_STATUS_FILE")
EEST_HISTORY_FILE=$(printf '%q' "$EEST_HISTORY_FILE")
EEST_PASSED=$(printf '%q' "$EEST_PASSED")
EEST_FAILED=$(printf '%q' "$EEST_FAILED")
EEST_ERRORS=$(printf '%q' "$EEST_ERRORS")
EEST_SUMMARY=$(printf '%q' "$EEST_SUMMARY")
EEST_FIRST_FAILURE=$(printf '%q' "$EEST_FIRST_FAILURE")
EEST_LAST_FAILURE=$(printf '%q' "$EEST_LAST_FAILURE")
EEST_RUN_RC=$(printf '%q' "$EEST_RUN_RC")
META

  if [ ! -f "$state_dir/history.tsv" ]; then
    printf 'cycle\tchecked_at\tpassed\tfailed\terrors\trun_rc\tsummary\tfirst_failure\n' >"$state_dir/history.tsv"
  fi
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$cycle" \
    "$(date -u +'%Y-%m-%dT%H:%M:%SZ')" \
    "$passed" \
    "$failed" \
    "$errors" \
    "$run_rc" \
    "$(printf '%s' "$summary" | tr '\t' ' ')" \
    "$(printf '%s' "$first_failure" | tr '\t' ' ')" \
    >>"$state_dir/history.tsv"

  cp "$cycle_status_file" "$state_dir/status.md"

  if [ "$run_rc" -eq 0 ] && [ "$failed" -eq 0 ] && [ "$errors" -eq 0 ] && [ -n "$summary" ]; then
    run_hook "on-pass cycle=$cycle" "$on_pass_cmd"
    exit 0
  fi

  run_hook "on-fail cycle=$cycle" "$on_fail_cmd"
  run_hook "verify cycle=$cycle" "$verify_cmd"
  run_hook "commit cycle=$cycle" "$commit_cmd"
  run_hook "push cycle=$cycle" "$push_cmd"

  cycle=$((cycle + 1))
done
