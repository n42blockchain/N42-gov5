#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
results_root="$repo_root/tests/results/eest-shards"
mark_empty_abandoned=1

usage() {
  cat <<'EOF'
Usage: scripts/repair_eest_results.sh [--root DIR] [--no-mark-empty-abandoned]

Repairs historical EEST result directories by:
  - backfilling missing rc/duration_seconds metadata as explicit incomplete markers
  - generating summary.md when metadata exists but the summary is missing
  - marking empty run directories with .eest-audit-ignore by default
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --root)
      results_root="$2"
      shift 2
      ;;
    --no-mark-empty-abandoned)
      mark_empty_abandoned=0
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

meta_value() {
  local meta_path="$1"
  local key="$2"
  awk -F= -v key="$key" '$1 == key {print substr($0, index($0, "=") + 1)}' "$meta_path" | tail -n 1
}

append_meta_if_missing() {
  local meta_path="$1"
  local key="$2"
  local value="$3"
  if [ -z "$(meta_value "$meta_path" "$key")" ]; then
    printf '%s=%s\n' "$key" "$value" >>"$meta_path"
    return 0
  fi
  return 1
}

repair_summary() {
  local run_dir="$1"
  local run_name="$2"
  local summary_path="$run_dir/summary.md"
  local meta_count
  local shard_jobs
  local generated="$run_name"
  local mode='unknown'
  local python='unknown'
  local pytest_workers='unknown'
  local dry_run='unknown'
  local status='complete'
  local first_meta=''
  local row_count=0

  meta_count="$(count_matches "$run_dir" 1 1 -type f -name '*.meta')"
  if [ "$meta_count" -eq 1 ]; then
    shard_jobs='1'
  else
    shard_jobs='unknown'
  fi

  while IFS= read -r meta_path; do
    [ -n "$meta_path" ] || continue
    first_meta="$meta_path"
    break
  done < <(find "$run_dir" -maxdepth 1 -mindepth 1 -type f -name '*.meta' | sort)

  if [ -n "$first_meta" ]; then
    mode="$(meta_value "$first_meta" mode)"
    python="$(meta_value "$first_meta" python)"
    pytest_workers="$(meta_value "$first_meta" pytest_workers)"
    if [ -z "$mode" ]; then mode='unknown'; fi
    if [ -z "$python" ]; then python='unknown'; fi
    if [ -z "$pytest_workers" ]; then pytest_workers='unknown'; fi
  fi

  {
    printf '%s\n\n' '# EEST Shard Run Summary'
    printf '%s\n' "- Generated: \`$generated\`"
    printf '%s\n' "- Mode: \`$mode\`"
    printf '%s\n' "- Python: \`$python\`"
    printf '%s\n' "- Pytest workers: \`$pytest_workers\`"
    printf '%s\n' "- Shard jobs: \`$shard_jobs\`"
    printf '%s\n' "- Dry run: \`$dry_run\`"
    printf '%s\n\n' "- Status: \`__STATUS__\`"
    printf '%s\n' '| Shard | Selector | Target ~Tests | RC | Duration (s) | Log |'
    printf '%s\n' '|-------|----------|---------------|----|--------------|-----|'

    while IFS= read -r meta_path; do
      [ -n "$meta_path" ] || continue
      row_count=$((row_count + 1))
      shard_name="$(basename "$meta_path" .meta)"
      selector="$(meta_value "$meta_path" selector)"
      target="$(meta_value "$meta_path" target)"
      rc_value="$(meta_value "$meta_path" rc)"
      duration_value="$(meta_value "$meta_path" duration_seconds)"

      if [ -z "$selector" ]; then selector='unknown'; fi
      if [ -z "$target" ]; then target='unknown'; fi
      if [ -z "$rc_value" ]; then rc_value='incomplete'; fi
      if [ -z "$duration_value" ]; then duration_value='-'; fi
      if [ "$rc_value" = "incomplete" ] || [ "$duration_value" = "-" ]; then
        status='partial'
      fi

      printf '| %s | `%s` | %s | `%s` | `%s` | `%s.log` |\n' \
        "$shard_name" "$selector" "$target" "$rc_value" "$duration_value" "$shard_name"
    done < <(find "$run_dir" -maxdepth 1 -mindepth 1 -type f -name '*.meta' | sort)
  } >"$summary_path.tmp"

  sed "s/__STATUS__/$status/" "$summary_path.tmp" >"$summary_path"
  rm -f "$summary_path.tmp"

  if [ "$row_count" -eq 0 ]; then
    rm -f "$summary_path"
    return 1
  fi
  return 0
}

printf '%s\n\n' '# EEST Result Repair'
printf '%s\n' "- Root: \`$results_root\`"
printf '%s\n' "- Mark empty abandoned: \`$mark_empty_abandoned\`"
printf '%s\n' '| Run | Action | Details |'
printf '%s\n' '|-----|--------|---------|'

repairs=0
skips=0

while IFS= read -r run_dir; do
  [ -d "$run_dir" ] || continue

  run_name="$(basename "$run_dir")"
  ignore_marker="$run_dir/.eest-audit-ignore"
  meta_count="$(count_matches "$run_dir" 1 1 -type f -name '*.meta')"
  log_count="$(count_matches "$run_dir" 1 1 -type f -name '*.log')"
  summary_missing=0
  details=()

  if [ -f "$ignore_marker" ]; then
    printf '| `%s` | `%s` | %s |\n' "$run_name" "skip" "already ignored"
    skips=$((skips + 1))
    continue
  fi

  if [ ! -f "$run_dir/summary.md" ]; then
    summary_missing=1
  fi

  if [ "$meta_count" -eq 0 ] && [ "$log_count" -eq 0 ] && [ "$mark_empty_abandoned" = "1" ]; then
    printf '%s\n' 'empty historical result directory' >"$ignore_marker"
    printf '| `%s` | `%s` | %s |\n' "$run_name" "ignore" "marked empty directory as abandoned"
    repairs=$((repairs + 1))
    continue
  fi

  while IFS= read -r meta_path; do
    [ -n "$meta_path" ] || continue
    if append_meta_if_missing "$meta_path" rc incomplete; then
      details+=("$(basename "$meta_path"): rc=incomplete")
    fi
    if append_meta_if_missing "$meta_path" duration_seconds -; then
      details+=("$(basename "$meta_path"): duration_seconds=-")
    fi
  done < <(find "$run_dir" -maxdepth 1 -mindepth 1 -type f -name '*.meta' | sort)

  if [ "$summary_missing" = "1" ] && [ "$meta_count" -gt 0 ]; then
    if repair_summary "$run_dir" "$run_name"; then
      details+=("summary.md rebuilt")
    fi
  fi

  if [ "${#details[@]}" -eq 0 ]; then
    printf '| `%s` | `%s` | %s |\n' "$run_name" "noop" "nothing to repair"
    skips=$((skips + 1))
    continue
  fi

  detail_text="$(printf '%s; ' "${details[@]}")"
  detail_text="${detail_text%; }"
  printf '| `%s` | `%s` | %s |\n' "$run_name" "repair" "$detail_text"
  repairs=$((repairs + 1))
done < <(find "$results_root" -maxdepth 1 -mindepth 1 -type d | sort)

printf '\n%s\n' "- Repaired runs: \`$repairs\`"
printf '%s\n' "- Unchanged/skipped runs: \`$skips\`"
