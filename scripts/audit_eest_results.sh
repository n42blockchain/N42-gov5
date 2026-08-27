#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
results_root="$repo_root/tests/results/eest-shards"
fail_on_skip=0

usage() {
  cat <<'EOF'
Usage: scripts/audit_eest_results.sh [--root DIR] [--fail-on-skip]

Audits EEST shard result directories for missing summaries, missing logs,
and incomplete shard metadata.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --root)
      results_root="$2"
      shift 2
      ;;
    --fail-on-skip)
      fail_on_skip=1
      shift
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

if [ ! -d "$results_root" ]; then
  echo "results root not found: $results_root" >&2
  exit 2
fi

count_matches() {
  find "$1" -maxdepth "$2" -mindepth "$3" "${@:4}" | wc -l | tr -d ' '
}

append_issue() {
  local message="$1"
  if [ -z "$issue_text" ]; then
    issue_text="$message"
  else
    issue_text="$issue_text; $message"
  fi
}

runs_scanned=0
runs_pass=0
runs_fail=0
runs_skip=0
run_count="$(count_matches "$results_root" 1 1 -type d)"

printf '%s\n\n' '# EEST Result Audit'
printf '%s\n' "- Root: \`$results_root\`"
printf '%s\n' "- Fail on skip: \`$fail_on_skip\`"

if [ "$run_count" -eq 0 ]; then
  printf '%s\n' "- Runs scanned: \`0\`"
  printf '%s\n' "- Status: \`PASS\`"
  exit 0
fi

printf '%s\n' '| Run | Status | Shards | Issues |'
printf '%s\n' '|-----|--------|--------|--------|'

while IFS= read -r run_dir; do
  [ -d "$run_dir" ] || continue
  runs_scanned=$((runs_scanned + 1))

  run_name="$(basename "$run_dir")"
  ignore_marker="$run_dir/.eest-audit-ignore"
  issue_text=''
  shard_count=0
  log_count="$(count_matches "$run_dir" 1 1 -type f -name '*.log')"

  if [ -f "$ignore_marker" ]; then
    issue_text="$(tr '\n' ' ' <"$ignore_marker" | sed 's/[[:space:]]\+/ /g; s/^ //; s/ $//')"
    if [ -z "$issue_text" ]; then
      issue_text="ignored"
    fi
    runs_skip=$((runs_skip + 1))
    printf '| `%s` | `%s` | `%s` | %s |\n' \
      "$run_name" "SKIP" "$shard_count" "$issue_text"
    continue
  fi

  if [ ! -f "$run_dir/summary.md" ]; then
    append_issue "missing summary.md"
  elif ! grep -Fqx -- '- Status: `complete`' "$run_dir/summary.md"; then
    append_issue "summary.md is not complete"
  fi

  while IFS= read -r meta_path; do
    [ -n "$meta_path" ] || continue
    shard_count=$((shard_count + 1))
    shard_name="$(basename "$meta_path" .meta)"
    log_path="$run_dir/$shard_name.log"
    rc_value="$(awk -F= '/^rc=/{print $2}' "$meta_path" | tail -n 1)"
    duration_value="$(awk -F= '/^duration_seconds=/{print $2}' "$meta_path" | tail -n 1)"

    if [ ! -f "$log_path" ]; then
      append_issue "missing ${shard_name}.log"
    fi
    if [ -z "$rc_value" ]; then
      append_issue "${shard_name}.meta missing rc"
    elif [ "$rc_value" != "0" ]; then
      append_issue "${shard_name}.meta has rc=${rc_value}"
    fi
    if [ -z "$duration_value" ]; then
      append_issue "${shard_name}.meta missing duration_seconds"
    elif [[ ! "$duration_value" =~ ^[0-9]+$ ]]; then
      append_issue "${shard_name}.meta has invalid duration_seconds=${duration_value}"
    fi
  done < <(find "$run_dir" -maxdepth 1 -mindepth 1 -type f -name '*.meta' | sort)

  if [ "$shard_count" -eq 0 ]; then
    append_issue "no .meta files"
  fi
  if [ "$log_count" -eq 0 ]; then
    append_issue "no .log files"
  fi

  if [ -z "$issue_text" ]; then
    status="PASS"
    issue_text="none"
    runs_pass=$((runs_pass + 1))
  else
    status="FAIL"
    runs_fail=$((runs_fail + 1))
  fi

  printf '| `%s` | `%s` | `%s` | %s |\n' \
    "$run_name" "$status" "$shard_count" "$issue_text"
done < <(find "$results_root" -maxdepth 1 -mindepth 1 -type d | sort)

overall_status="PASS"
if [ "$runs_fail" -gt 0 ] || { [ "$fail_on_skip" = "1" ] && [ "$runs_skip" -gt 0 ]; }; then
  overall_status="FAIL"
fi

printf '\n%s\n' "- Runs scanned: \`$runs_scanned\`"
printf '%s\n' "- Passing runs: \`$runs_pass\`"
printf '%s\n' "- Failing runs: \`$runs_fail\`"
printf '%s\n' "- Skipped runs: \`$runs_skip\`"
printf '%s\n' "- Status: \`$overall_status\`"

if [ "$runs_fail" -gt 0 ] || { [ "$fail_on_skip" = "1" ] && [ "$runs_skip" -gt 0 ]; }; then
  exit 1
fi
