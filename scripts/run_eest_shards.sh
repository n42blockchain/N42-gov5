#!/usr/bin/env bash

set -euo pipefail

# All consume-* rows below exercise an Ethereum execution-layer client through
# Hive. They are intentionally reported separately from the project-wide Go
# test/build/lint gates.

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
eest_dir="${EEST_DIR:-$repo_root/tests/eth-tests/execution-spec-tests}"
mode="${EEST_MODE:-consume-engine}"
python_bin="${PYTHON_BIN:-3.13}"
pytest_workers="${EEST_PYTEST_WORKERS:-auto}"
shard_jobs="${EEST_SHARD_JOBS:-1}"
test_root="${EEST_TEST_ROOT:-tests}"
input_path="${EEST_INPUT:-}"
hive_simulator="${HIVE_SIMULATOR:-}"
rpc_timeout="${EEST_RPC_TIMEOUT:-90}"
infra_reruns="${EEST_INFRA_RERUNS:-2}"
infra_rerun_delay="${EEST_INFRA_RERUN_DELAY:-1}"
infra_rerun_match="${EEST_INFRA_RERUN_MATCH:-ConnectionError|ConnectionRefusedError|RemoteDisconnected|ReadTimeout}"
extra_args_raw="${EEST_EXTRA_ARGS:-}"
dry_run="${EEST_DRY_RUN:-0}"
test_run_shard_delay="${EEST_TEST_RUN_SHARD_DELAY:-0}"
timestamp="$(date -u +%Y%m%d-%H%M%SZ)"
results_dir="${EEST_RESULTS_DIR:-$repo_root/tests/results/eest-shards/$timestamp}"

git_revision() {
  git -C "$1" rev-parse HEAD 2>/dev/null || printf '%s\n' unavailable
}

git_dirty() {
  if ! git -C "$1" rev-parse --git-dir >/dev/null 2>&1; then
    printf '%s\n' unavailable
  elif [ -n "$(git -C "$1" status --porcelain --untracked-files=no)" ]; then
    printf '%s\n' true
  else
    printf '%s\n' false
  fi
}

n42_revision="$(git_revision "$repo_root")"
n42_dirty="$(git_dirty "$repo_root")"
hive_revision="$(git_revision "$repo_root/tests/eth-hive")"
hive_dirty="$(git_dirty "$repo_root/tests/eth-hive")"
eest_revision="$(git_revision "$eest_dir")"
eest_dirty="$(git_dirty "$eest_dir")"

requested_shards=("$@")

declare -a shard_rows=(
  $'paris+shanghai\tfork_Paris or fork_Shanghai\t.*fork_(Paris|Shanghai).*\t.*/.*fork_(Paris\\|Shanghai)\t~3,573\tstable@latest'
  $'cancun\tfork_Cancun\t.*fork_Cancun.*\t.*/.*fork_Cancun\t~17,783\tstable@latest'
  $'prague\tfork_Prague\t.*fork_Prague.*\t.*/.*fork_Prague\t~20,878\tstable@latest'
  $'osaka\tfork_Osaka\t.*fork_Osaka.*\t.*/.*fork_Osaka\t~21,583\tdevelop@latest'
  $'engine-access-list\teip2930_access_list\t.*eip2930_access_list.*\t.*eip2930_access_list.*\t~2,132\tstable@latest\tengine'
  $'rlp\tblockchain\t.*\t.*\t~47,589\tstable@latest\trlp'
)

declare -a extra_args=()
if [ -n "$extra_args_raw" ]; then
  # shellcheck disable=SC2206
  extra_args=($extra_args_raw)
fi

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

run_shard() {
  local shard="$1"
  local fill_expr="$2"
  local sim_limit_expr="$3"
  local selector="$4"
  local target="$5"
  local shard_default_input="$6"
  local runner="${7:-engine}"
  local shard_input_path=''
  local log_path="$results_dir/$shard.log"
  local meta_path="$results_dir/$shard.meta"
  local cmd=()
  local rc=0
  local start_ts
  local end_ts
  local duration

  case "$mode" in
    fill)
      cmd=(uv run --python "$python_bin" fill "$test_root" -k "$fill_expr")
      ;;
    consume-engine)
      shard_input_path="$(resolve_input_path "$shard" "$shard_default_input")"
      if [ -z "$shard_input_path" ]; then
        echo "EEST_INPUT is required when EEST_MODE=consume-engine" >&2
        return 2
      fi
      if [ "$dry_run" != "1" ] && [ -z "$hive_simulator" ]; then
        echo "HIVE_SIMULATOR is required when EEST_MODE=consume-engine" >&2
        return 2
      fi
      case "$runner" in
        engine)
          cmd=(uv run --python "$python_bin" consume engine --input "$shard_input_path" --sim.limit "$sim_limit_expr")
          ;;
        rlp)
          cmd=(uv run --python "$python_bin" consume rlp --input "$shard_input_path" --sim.limit "$sim_limit_expr")
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

  if [ -n "$pytest_workers" ]; then
    cmd+=(-n "$pytest_workers")
  fi
  if [ "$mode" = "consume-engine" ] && [ "$infra_reruns" != "0" ]; then
    # Docker Desktop can briefly drop a published client port while Hive is
    # rapidly replacing short-lived containers. Retry only connection-level
    # failures; protocol assertions and execution mismatches remain failures.
    cmd+=(--reruns "$infra_reruns" --reruns-delay "$infra_rerun_delay" --only-rerun "$infra_rerun_match")
  fi
  if [ "${#extra_args[@]}" -gt 0 ]; then
    cmd+=("${extra_args[@]}")
  fi

  start_ts="$(date +%s)"
  {
    printf 'shard=%s\n' "$shard"
    printf 'selector=%s\n' "$selector"
    printf 'target=%s\n' "$target"
    printf 'mode=%s\n' "$mode"
    printf 'runner=%s\n' "$runner"
    printf 'python=%s\n' "$python_bin"
    printf 'pytest_workers=%s\n' "$pytest_workers"
    printf 'rpc_timeout_seconds=%s\n' "$rpc_timeout"
    printf 'infra_reruns=%s\n' "$infra_reruns"
    printf 'infra_rerun_delay_seconds=%s\n' "$infra_rerun_delay"
    printf 'infra_rerun_match=%s\n' "$infra_rerun_match"
    printf 'n42_revision=%s\n' "$n42_revision"
    printf 'n42_dirty=%s\n' "$n42_dirty"
    printf 'hive_revision=%s\n' "$hive_revision"
    printf 'hive_dirty=%s\n' "$hive_dirty"
    printf 'eest_revision=%s\n' "$eest_revision"
    printf 'eest_dirty=%s\n' "$eest_dirty"
    if [ "$mode" = "consume-engine" ]; then
      printf 'input=%s\n' "$shard_input_path"
    fi
    printf 'command='
    printf '%q ' "${cmd[@]}"
    printf '\n'
  } >"$meta_path"

  if [ "$test_run_shard_delay" != "0" ]; then
    sleep "$test_run_shard_delay"
  fi

  if [ "$dry_run" = "1" ]; then
    printf '%q ' "${cmd[@]}" >"$log_path"
    printf '\n' >>"$log_path"
  else
    (
      cd "$eest_dir"
      if [ "$mode" = "consume-engine" ]; then
        HIVE_SIMULATOR="$hive_simulator" EEST_RPC_TIMEOUT="$rpc_timeout" "${cmd[@]}"
      else
        "${cmd[@]}"
      fi
    ) >"$log_path" 2>&1 || rc=$?
  fi

  end_ts="$(date +%s)"
  duration="$((end_ts - start_ts))"

  {
    printf 'rc=%s\n' "$rc"
    printf 'duration_seconds=%s\n' "$duration"
  } >>"$meta_path"

  return "$rc"
}

write_summary() {
  local summary_path="$results_dir/summary.md"
  local summary_state='complete'
  {
    printf '%s\n\n' '# EEST Shard Run Summary'
    printf '%s\n' "- Generated: \`$timestamp\`"
    printf '%s\n' "- Mode: \`$mode\`"
    printf '%s\n' "- Python: \`$python_bin\`"
    printf '%s\n' "- Pytest workers: \`$pytest_workers\`"
    printf '%s\n' "- Shard jobs: \`$shard_jobs\`"
    printf '%s\n' "- Dry run: \`$dry_run\`"
    printf '%s\n' "- N42 revision: \`$n42_revision\` (dirty: \`$n42_dirty\`)"
    printf '%s\n' "- Hive revision: \`$hive_revision\` (dirty: \`$hive_dirty\`)"
    printf '%s\n' "- EEST revision: \`$eest_revision\` (dirty: \`$eest_dirty\`)"
    printf '%s\n' '| Shard | Selector | Target ~Tests | Runner | RC | Duration (s) | Log |'
    printf '%s\n' '|-------|----------|---------------|--------|----|--------------|-----|'

    local row shard expr selector target meta_path rc duration runner
    for row in "${shard_rows[@]}"; do
      IFS=$'\t' read -r shard fill_expr sim_limit_expr selector target shard_default_input runner <<<"$row"
      if ! want_shard "$shard"; then
        continue
      fi
      meta_path="$results_dir/$shard.meta"
      rc=''
      duration=''
      if [ -f "$meta_path" ]; then
        rc="$(awk -F= '/^rc=/{print $2}' "$meta_path" | tail -n 1)"
        duration="$(awk -F= '/^duration_seconds=/{print $2}' "$meta_path" | tail -n 1)"
      fi
      if [ -z "$rc" ]; then
        rc='incomplete'
        summary_state='partial'
      fi
      if [ -z "$duration" ]; then
        duration='-'
      fi
      runner="${runner:-engine}"
      printf '| %s | `%s` | %s | `%s` | `%s` | `%s` | `%s.log` |\n' "$shard" "$selector" "$target" "$runner" "$rc" "$duration" "$shard"
    done
  } >"$summary_path.tmp"

  {
    sed -n '1,11p' "$summary_path.tmp"
    printf '%s\n\n' "- Status: \`$summary_state\`"
    sed -n '12,$p' "$summary_path.tmp"
  } >"$summary_path"
  rm -f "$summary_path.tmp"
}

mkdir -p "$results_dir"

declare -a pids=()
declare -a active_shards=()
cleanup_done=0

cleanup_and_exit() {
  local code="$1"
  local reason="${2:-exit}"
  local pid

  if [ "$cleanup_done" = "1" ]; then
    return
  fi
  cleanup_done=1
  trap - EXIT INT TERM

  if [ "$reason" != "exit" ]; then
    overall_rc=1
  fi

  for pid in "${pids[@]}"; do
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
    fi
  done
  for pid in "${pids[@]}"; do
    wait "$pid" 2>/dev/null || true
  done

  write_summary

  if [ "$reason" != "exit" ]; then
    exit "$code"
  fi
}

trap 'cleanup_and_exit $?' EXIT
trap 'cleanup_and_exit 130 INT' INT
trap 'cleanup_and_exit 143 TERM' TERM

for row in "${shard_rows[@]}"; do
  IFS=$'\t' read -r shard fill_expr sim_limit_expr selector target shard_default_input runner <<<"$row"
  if ! want_shard "$shard"; then
    continue
  fi

  while [ "${#pids[@]}" -ge "$shard_jobs" ]; do
    wait "${pids[0]}" || true
    pids=("${pids[@]:1}")
    active_shards=("${active_shards[@]:1}")
  done

  run_shard "$shard" "$fill_expr" "$sim_limit_expr" "$selector" "$target" "$shard_default_input" "$runner" &
  pids+=("$!")
  active_shards+=("$shard")
done

overall_rc=0
for pid in "${pids[@]}"; do
  if ! wait "$pid"; then
    overall_rc=1
  fi
done

write_summary
printf 'results_dir=%s\n' "$results_dir"

exit "$overall_rc"
