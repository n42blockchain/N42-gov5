#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
timestamp="$(date -u +%Y%m%d-%H%M%SZ)"
results_dir="$repo_root/benchmarks/results/$timestamp"

mkdir -p "$results_dir"

metadata_file="$results_dir/ENVIRONMENT.txt"
commands_file="$results_dir/COMMANDS.txt"

{
  echo "timestamp_utc=$timestamp"
  echo "repo_root=$repo_root"
  echo "git_commit=$(git -C "$repo_root" rev-parse --short HEAD)"
  echo "git_branch=$(git -C "$repo_root" rev-parse --abbrev-ref HEAD)"
  echo "go_version=$(go version)"
  echo "uname=$(uname -a)"
} >"$metadata_file"

run_bench() {
  local name="$1"
  shift
  local output_file="$results_dir/$name.txt"

  printf '$ %q' "$@" >"$output_file"
  printf '\n\n' >>"$output_file"
  printf '%q ' "$@" >>"$commands_file"
  printf '\n' >>"$commands_file"

  (
    cd "$repo_root"
    "$@"
  ) | tee -a "$output_file"
}

run_bench \
  tpsbench_subset \
  go test -run '^$' -bench 'Benchmark(AccountGeneration|TransactionCreation|StateGetBalance|SimpleTransfer|EVMTransfer|FullPipeline|BatchProcessing_1K)$' -benchmem ./tools/tpsbench

run_bench \
  rawdb_subset \
  go test -run '^$' -bench 'Benchmark(HeaderKeyGen|BlockBodyKeyGen|TxLookupKeyGen|ReceiptKeyGen|HeaderKeyParallel)$' -benchmem ./modules/rawdb

run_bench \
  vm_subset \
  go test -run '^$' -bench 'Benchmark(OpAdd|OpMul|OpDiv|OpExp|MemoryPoolGetPut)$' -benchmem ./internal/vm

cat >"$results_dir/README.txt" <<EOF
This directory contains a single reproducible performance baseline capture.

Files:
- ENVIRONMENT.txt: machine and toolchain metadata
- COMMANDS.txt: exact benchmark commands
- tpsbench_subset.txt: selected end-to-end throughput benchmarks
- rawdb_subset.txt: selected storage key-generation benchmarks
- vm_subset.txt: selected VM microbenchmarks
EOF

echo "Wrote performance baseline to: $results_dir"
