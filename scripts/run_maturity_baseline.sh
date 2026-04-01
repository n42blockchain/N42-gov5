#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
result_root="${MATURITY_RESULTS_DIR:-$repo_root/build/maturity-baseline}"
mode="smoke"
execution_model="package-test baseline (no ephemeral node boot)"

usage() {
  cat <<'EOF'
Usage: scripts/run_maturity_baseline.sh [--full] [--result-dir DIR]

This script records package-level smoke/core gates only. It does not boot a node.

Options:
  --full            Run the focused smoke suite plus core baseline gates.
  --result-dir DIR  Override the output directory root.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --full)
      mode="full"
      shift
      ;;
    --result-dir)
      if [[ $# -lt 2 ]]; then
        echo "--result-dir requires a path" >&2
        exit 2
      fi
      result_root="$2"
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

run_id="${MATURITY_RUN_ID:-$(date -u +"%Y%m%d-%H%M%SZ")}"
run_dir="$result_root/$run_id"
mkdir -p "$run_dir"

summary_file="$run_dir/summary.md"
surface_rows="$run_dir/surface.rows"
recovery_rows="$run_dir/recovery.rows"
core_rows="$run_dir/core.rows"
: >"$surface_rows"
: >"$recovery_rows"
: >"$core_rows"

overall_rc=0

rows_file_for_section() {
  case "$1" in
    surface) echo "$surface_rows" ;;
    recovery) echo "$recovery_rows" ;;
    core) echo "$core_rows" ;;
    *)
      echo "unknown section: $1" >&2
      exit 2
      ;;
  esac
}

run_step() {
  local section="$1"
  local name="$2"
  local log_file="$3"
  shift 3

  local start_ts end_ts duration status cmd_text rows_file
  rows_file="$(rows_file_for_section "$section")"
  start_ts="$(date +%s)"
  printf '[%s] %s\n' "$section" "$name"
  if (
    cd "$repo_root"
    "$@"
  ) >"$log_file" 2>&1; then
    status="PASS"
  else
    status="FAIL"
    overall_rc=1
  fi
  end_ts="$(date +%s)"
  duration=$((end_ts - start_ts))
  cmd_text="$(printf '%q ' "$@")"
  printf '| `%s` | `%s` | %ss | `%s` | `%s` |\n' \
    "$name" "$status" "$duration" "$cmd_text" "$(basename "$log_file")" >>"$rows_file"
}

branch="$(git -C "$repo_root" rev-parse --abbrev-ref HEAD)"
commit="$(git -C "$repo_root" rev-parse HEAD)"
go_version="$(go version)"
generated_at="$(date -u +"%Y-%m-%d %H:%M:%SZ")"

run_step surface "engine-api" "$run_dir/engine-api.log" \
  go test -count=1 ./internal/api -run \
  'TestEngineAPIV1BuildsAndImportsMinimalPayload|TestEngineAPIBlobBuildsAndImportsMinimalPayloadV3|TestEngineAPIv4BuildsAndImportsMinimalPayloadV4|TestEngineAPIV1InputValidationAndExchangeCapabilities|TestEngineAPIBlobInputValidation|TestEngineAPIv4InputValidation'
run_step surface "graphql" "$run_dir/graphql.log" \
  go test -count=1 ./internal/api/graphql
run_step surface "clef" "$run_dir/clef.log" \
  go test -count=1 ./cmd/clef
run_step surface "external-signer" "$run_dir/external-signer.log" \
  go test -count=1 ./accounts/external
run_step surface "node-auth-and-genesis" "$run_dir/node-auth-and-genesis.log" \
  go test -count=1 ./internal/node -run \
  'TestAuthenticatedModulesSkipsOpenNamespaces|TestAuthenticatedModulesDeduplicatesInOrder|TestWriteGenesisBlockPreservesExplicitHeaderFields'

run_step recovery "keystore-reload" "$run_dir/keystore-reload.log" \
  go test -count=1 ./accounts/keystore -run \
  'TestUpdatedKeyfileContents|TestAccountsReloadWhenWatcherMissesEvent'
run_step recovery "genesis-config" "$run_dir/genesis-config.log" \
  go test -count=1 ./conf -run \
  'TestGenesisUnmarshalHiveEngineFixture|TestGenesisUnmarshalPreservesExplicitHeaderFields|TestGenesisUnmarshalInfersConsensusFromExplicitEngine|TestApplyHiveGenesisEnvCreatesFakerConfig|TestApplyHiveGenesisEnvSupportsClique'
run_step recovery "checkpoint-recovery" "$run_dir/checkpoint-recovery.log" \
  go test -count=1 ./internal/sync/checkpoint -run \
  'TestCheckpointService_AlreadyHaveBlock|TestCheckpointService_CheckExistingBlockRejectsIncompleteBlock|TestCheckpointService_HashMismatch'
run_step recovery "snapshot-journal" "$run_dir/snapshot-journal.log" \
  go test -count=1 ./modules/state/snapshot -run \
  'TestJournalSaveAndLoad|TestJournalSaveAndLoad_MultipleBlocks|TestJournalDeserialize_TruncatedData|TestJournalDeserialize_CorruptAccountCount|TestGenerator_ContextCancelled'
run_step recovery "freezer-recovery" "$run_dir/freezer-recovery.log" \
  go test -count=1 ./modules/rawdb/freezer -run \
  'TestFreezerReopenPrefersTableCountWhenMetadataStale|TestFreezerReopenTruncatesToShortestTable'
run_step recovery "history-expiry-recovery" "$run_dir/history-expiry-recovery.log" \
  go test -count=1 ./internal/api ./internal/node ./turbo/rpchelper -run \
  'TestBlockByNumberUsesEarliestAvailableAfterHistoryExpiry|TestResolveBlockRangeUsesEarliestAvailableBlock|TestResolveBlockRangeClampsToEarliestAvailableHistory|TestHistoryExpiry_RestartResumesFromPersistedEarliestBlock|TestGetCanonicalBlockNumberUsesEarliestAvailableHistory'
run_step recovery "txpool-journal" "$run_dir/txpool-journal.log" \
  go test -count=1 ./internal/txspool -run \
  'TestFlushToDBPersistsLocalAndPendingTransactions|TestLoadPersistedTransactionsDeduplicatesCurrentAndLegacyEntries|TestLoadPersistedTransactionsClearsUnreadableJournalEntries|TestLoadPersistedTransactionsMigratesLegacyEntriesWithoutClearingForeignData'

if [[ "$mode" == "full" ]]; then
  run_step core "build" "$run_dir/build.log" \
    go build ./...
  run_step core "vet" "$run_dir/vet.log" \
    go vet ./...
  run_step core "test" "$run_dir/test.log" \
    go test -count=1 ./...
  run_step core "lint" "$run_dir/lint.log" \
    make lint
  run_step core "race-core" "$run_dir/race-core.log" \
    make race-core
fi

{
  echo "# N42 Maturity Baseline"
  echo
  echo "- Generated at: \`$generated_at\`"
  echo "- Mode: \`$mode\`"
  echo "- Branch: \`$branch\`"
  echo "- Commit: \`$commit\`"
  echo "- Go: \`$go_version\`"
  echo "- Repo: \`$repo_root\`"
  echo "- Run dir: \`$run_dir\`"
  echo "- Execution model: \`$execution_model\`"
  echo "- Overall status: \`$( [[ $overall_rc -eq 0 ]] && echo PASS || echo FAIL )\`"
  echo
  echo "## Surface Smoke"
  echo
  echo "| Step | Status | Duration | Command | Log |"
  echo "|---|---|---:|---|---|"
  cat "$surface_rows"
  echo
  echo "## Recovery Smoke"
  echo
  echo "| Step | Status | Duration | Command | Log |"
  echo "|---|---|---:|---|---|"
  cat "$recovery_rows"
  if [[ "$mode" == "full" ]]; then
    echo
    echo "## Core Baseline"
    echo
    echo "| Step | Status | Duration | Command | Log |"
    echo "|---|---|---:|---|---|"
    cat "$core_rows"
  fi
} >"$summary_file"

rm -f "$surface_rows" "$recovery_rows" "$core_rows"

echo "summary=$summary_file"
echo "run_dir=$run_dir"

exit "$overall_rc"
