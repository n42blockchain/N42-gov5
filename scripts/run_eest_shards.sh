#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
eest_dir="${EEST_DIR:-$repo_root/tests/eth-tests/execution-spec-tests}"
mode="${EEST_MODE:-consume-engine}"
python_bin="${PYTHON_BIN:-3.13}"
pytest_workers="${EEST_PYTEST_WORKERS:-auto}"
shard_jobs="${EEST_SHARD_JOBS:-1}"
test_root="${EEST_TEST_ROOT:-tests}"
input_path="${EEST_INPUT:-}"
hive_simulator="${HIVE_SIMULATOR:-}"
extra_args_raw="${EEST_EXTRA_ARGS:-}"
dry_run="${EEST_DRY_RUN:-0}"
timestamp="$(date -u +%Y%m%d-%H%M%SZ)"
results_dir="${EEST_RESULTS_DIR:-$repo_root/tests/results/eest-shards/$timestamp}"

requested_shards=("$@")

declare -a shard_rows=(
  $'paris+shanghai\tfork_Paris or fork_Shanghai\t.*fork_(Paris|Shanghai).*\t.*/.*fork_(Paris\\|Shanghai)\t~2,600'
  $'cancun\tfork_Cancun\t.*fork_Cancun.*\t.*/.*fork_Cancun\t~17,250'
  $'prague\tfork_Prague\t.*fork_Prague.*\t.*/.*fork_Prague\t~20,500'
  $'osaka\tfork_Osaka\t.*fork_Osaka.*\t.*/.*fork_Osaka\t~21,000'
  $'rlp\teip2930_access_list\t.*eip2930_access_list.*\t.*eip2930_access_list.*\tunchanged'
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

run_shard() {
  local shard="$1"
  local fill_expr="$2"
  local sim_limit_expr="$3"
  local selector="$4"
  local target="$5"
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
      if [ -z "$input_path" ]; then
        echo "EEST_INPUT is required when EEST_MODE=consume-engine" >&2
        return 2
      fi
      if [ "$dry_run" != "1" ] && [ -z "$hive_simulator" ]; then
        echo "HIVE_SIMULATOR is required when EEST_MODE=consume-engine" >&2
        return 2
      fi
      cmd=(uv run --python "$python_bin" consume engine --input "$input_path" --sim.limit "$sim_limit_expr")
      ;;
    *)
      echo "Unsupported EEST_MODE: $mode" >&2
      return 2
      ;;
  esac

  if [ -n "$pytest_workers" ]; then
    cmd+=(-n "$pytest_workers")
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
    printf 'python=%s\n' "$python_bin"
    printf 'pytest_workers=%s\n' "$pytest_workers"
    printf 'command='
    printf '%q ' "${cmd[@]}"
    printf '\n'
  } >"$meta_path"

  if [ "$dry_run" = "1" ]; then
    printf '%q ' "${cmd[@]}" >"$log_path"
    printf '\n' >>"$log_path"
  else
    (
      cd "$eest_dir"
      if [ "$mode" = "consume-engine" ]; then
        HIVE_SIMULATOR="$hive_simulator" "${cmd[@]}"
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
  {
    printf '%s\n\n' '# EEST Shard Run Summary'
    printf '%s\n' "- Generated: \`$timestamp\`"
    printf '%s\n' "- Mode: \`$mode\`"
    printf '%s\n' "- Python: \`$python_bin\`"
    printf '%s\n' "- Pytest workers: \`$pytest_workers\`"
    printf '%s\n' "- Shard jobs: \`$shard_jobs\`"
    printf '%s\n\n' "- Dry run: \`$dry_run\`"
    printf '%s\n' '| Shard | Selector | Target ~Tests | RC | Duration (s) | Log |'
    printf '%s\n' '|-------|----------|---------------|----|--------------|-----|'

    local row shard expr selector target meta_path rc duration
    for row in "${shard_rows[@]}"; do
      IFS=$'\t' read -r shard fill_expr sim_limit_expr selector target <<<"$row"
      if ! want_shard "$shard"; then
        continue
      fi
      meta_path="$results_dir/$shard.meta"
      rc="$(awk -F= '/^rc=/{print $2}' "$meta_path")"
      duration="$(awk -F= '/^duration_seconds=/{print $2}' "$meta_path")"
      printf '| %s | `%s` | %s | `%s` | `%s` | `%s.log` |\n' "$shard" "$selector" "$target" "$rc" "$duration" "$shard"
    done
  } >"$summary_path"
}

mkdir -p "$results_dir"

declare -a pids=()
declare -a active_shards=()

for row in "${shard_rows[@]}"; do
  IFS=$'\t' read -r shard fill_expr sim_limit_expr selector target <<<"$row"
  if ! want_shard "$shard"; then
    continue
  fi

  while [ "${#pids[@]}" -ge "$shard_jobs" ]; do
    wait "${pids[0]}" || true
    pids=("${pids[@]:1}")
    active_shards=("${active_shards[@]:1}")
  done

  run_shard "$shard" "$fill_expr" "$sim_limit_expr" "$selector" "$target" &
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
