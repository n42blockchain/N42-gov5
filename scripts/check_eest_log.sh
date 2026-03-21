#!/usr/bin/env bash

set -euo pipefail

log_file="${1:-}"
out_file="${2:-}"

if [ -z "$log_file" ]; then
  echo "Usage: $0 <log_file> [out_file]" >&2
  exit 1
fi

if [ ! -f "$log_file" ]; then
  echo "log file not found: $log_file" >&2
  exit 1
fi

passed=$(grep -Ec '(^|\]) PASSED( |$)|PASSED in ' "$log_file" 2>/dev/null || true)
failed=$(grep -Ec '(^|\]) FAILED( |$)|FAILED in ' "$log_file" 2>/dev/null || true)
errors=$(grep -Ec '(^|\]) ERROR( |$)|ERROR in ' "$log_file" 2>/dev/null || true)
last_fail=$(grep -E '(^|\]) FAILED( |$)|FAILED in ' "$log_file" 2>/dev/null | tail -1 || true)
summary_line=$(grep -E "={5,} .* passed.*" "$log_file" 2>/dev/null | tail -1 || true)
tail_lines=$(tail -n 12 "$log_file" 2>/dev/null || true)
timestamp=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

tmp_file=""
if [ -n "$out_file" ]; then
  mkdir -p "$(dirname "$out_file")"
  tmp_file="${out_file}.tmp"
else
  tmp_file="$(mktemp)"
fi

{
  echo "# EEST Log Status"
  echo
  echo "- Checked: \`$timestamp\`"
  echo "- Log: \`$log_file\`"
  echo "- Passed: \`$passed\`"
  echo "- Failed: \`$failed\`"
  echo "- Errors: \`$errors\`"
  if [ -n "$summary_line" ]; then
    echo "- Summary: \`$summary_line\`"
  fi
  if [ -n "$last_fail" ]; then
    echo "- Last failure: \`$last_fail\`"
  fi
  echo
  echo "## Tail"
  echo
  echo '```text'
  printf '%s\n' "$tail_lines"
  echo '```'
} >"$tmp_file"

if [ -n "$out_file" ]; then
  mv "$tmp_file" "$out_file"
else
  cat "$tmp_file"
  rm -f "$tmp_file"
fi
