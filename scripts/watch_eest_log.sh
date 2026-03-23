#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
log_file="${1:-}"
interval="${EEST_WATCH_INTERVAL:-300}"
out_dir="${EEST_WATCH_DIR:-}"

shift || true
while [ $# -gt 0 ]; do
  case "$1" in
    --interval)
      interval="$2"
      shift 2
      ;;
    --out-dir)
      out_dir="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done

if [ -z "$log_file" ]; then
  echo "Usage: $0 <log_file> [--interval seconds] [--out-dir dir]" >&2
  exit 1
fi

if [ -z "$out_dir" ]; then
  out_dir="$(dirname "$log_file")/watch-$(basename "$log_file" .log)"
fi

mkdir -p "$out_dir"
status_file="$out_dir/status.md"
history_file="$out_dir/history.tsv"

if [ ! -f "$history_file" ]; then
  printf "checked_at\tpassed\tfailed\terrors\tsummary\n" >"$history_file"
fi

while true; do
  if [ -f "$log_file" ]; then
    "$script_dir/check_eest_log.sh" "$log_file" "$status_file"

    checked_at=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
    passed=$(grep -Ec '(^|\]) PASSED( |$)|PASSED in |::.* PASSED( |$)' "$log_file" 2>/dev/null || true)
    failed=$(grep -Ec '(^|\]) FAILED( |$)|FAILED in |::.* FAILED( |$)' "$log_file" 2>/dev/null || true)
    errors=$(grep -Ec '(^|\]) ERROR( |$)|ERROR in |::.* ERROR( |$)' "$log_file" 2>/dev/null || true)
    summary=$(grep -E "={5,} .* passed.*" "$log_file" 2>/dev/null | tail -1 | tr '\t' ' ' || true)
    printf "%s\t%s\t%s\t%s\t%s\n" "$checked_at" "$passed" "$failed" "$errors" "$summary" >>"$history_file"

    if [ -n "$summary" ]; then
      exit 0
    fi
  fi

  sleep "$interval"
done
