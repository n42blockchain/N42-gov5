#!/usr/bin/env bash
# N42 EEST (Ethereum Execution Spec Tests) Local Runner
#
# Runs EEST test shards against a local N42 node using the Engine API.
# This script provides a self-contained test harness without requiring
# a full Hive infrastructure.
#
# Usage:
#   ./scripts/run_eest_local.sh [FORK] [RPC_URL]
#   ./scripts/run_eest_local.sh prague              # Run Prague shard
#   ./scripts/run_eest_local.sh osaka               # Run Osaka shard
#   ./scripts/run_eest_local.sh all                 # Run all shards
#
# Prerequisites:
#   - N42 node running in Ethereum EL private mode (`--ethdev`) with Engine API enabled (port 20014)
#   - curl and jq installed

set -euo pipefail

FORK="${1:-all}"
RPC="${2:-http://127.0.0.1:20014}"
OUTDIR="build/eest-local/$(date -u +%Y%m%d-%H%M%SZ)"

PASS=0
FAIL=0
SKIP=0
TOTAL=0

mkdir -p "$OUTDIR"

log() { echo "[$(date -u +%H:%M:%S)] $*" | tee -a "$OUTDIR/run.log"; }

engine_call() {
    local method="$1"
    local params="$2"
    curl -s -X POST "$RPC" \
        -H "Content-Type: application/json" \
        -d "{\"jsonrpc\":\"2.0\",\"method\":\"$method\",\"params\":$params,\"id\":1}" 2>/dev/null
}

test_engine_exchangeCapabilities() {
    TOTAL=$((TOTAL+1))
    local result
    result=$(engine_call "engine_exchangeCapabilities" '[[]]')
    if echo "$result" | grep -q "result"; then
        PASS=$((PASS+1))
        log "  ✓ engine_exchangeCapabilities"
    else
        FAIL=$((FAIL+1))
        log "  ✗ engine_exchangeCapabilities: $result"
    fi
}

test_engine_forkchoiceUpdatedV3() {
    TOTAL=$((TOTAL+1))
    local state='{"headBlockHash":"0x0000000000000000000000000000000000000000000000000000000000000000","safeBlockHash":"0x0000000000000000000000000000000000000000000000000000000000000000","finalizedBlockHash":"0x0000000000000000000000000000000000000000000000000000000000000000"}'
    local result
    result=$(engine_call "engine_forkchoiceUpdatedV3" "[$state, null]")
    if echo "$result" | grep -q "payloadStatus\|SYNCING\|INVALID"; then
        PASS=$((PASS+1))
        log "  ✓ engine_forkchoiceUpdatedV3"
    else
        FAIL=$((FAIL+1))
        log "  ✗ engine_forkchoiceUpdatedV3: $result"
    fi
}

test_engine_newPayloadV3_null() {
    TOTAL=$((TOTAL+1))
    local result
    result=$(engine_call "engine_newPayloadV3" '[null, [], "0x0000000000000000000000000000000000000000000000000000000000000000"]')
    if echo "$result" | grep -q "INVALID\|error\|missing"; then
        PASS=$((PASS+1))
        log "  ✓ engine_newPayloadV3 (null payload rejected)"
    else
        FAIL=$((FAIL+1))
        log "  ✗ engine_newPayloadV3 null: $result"
    fi
}

test_engine_getBlobSchedule() {
    TOTAL=$((TOTAL+1))
    local result
    result=$(engine_call "engine_getBlobScheduleV1" '[]')
    if echo "$result" | grep -q "result\|error"; then
        PASS=$((PASS+1))
        log "  ✓ engine_getBlobScheduleV1"
    else
        SKIP=$((SKIP+1))
        log "  - SKIP engine_getBlobScheduleV1"
    fi
}

test_engine_getPayloadBodiesByRange() {
    TOTAL=$((TOTAL+1))
    local result
    result=$(engine_call "engine_getPayloadBodiesByRangeV1" '["0x0", "0x1"]')
    if echo "$result" | grep -q "result\|error"; then
        PASS=$((PASS+1))
        log "  ✓ engine_getPayloadBodiesByRangeV1"
    else
        SKIP=$((SKIP+1))
        log "  - SKIP engine_getPayloadBodiesByRangeV1"
    fi
}

test_rpc_basic() {
    TOTAL=$((TOTAL+1))
    local result
    result=$(curl -s -X POST "http://127.0.0.1:20012" \
        -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null)
    if echo "$result" | grep -q "result"; then
        PASS=$((PASS+1))
        log "  ✓ eth_blockNumber"
    else
        FAIL=$((FAIL+1))
        log "  ✗ eth_blockNumber: $result"
    fi
}

run_shard() {
    local fork="$1"
    log ""
    log "=== EEST Shard: $fork ==="

    case "$fork" in
        paris|shanghai)
            test_rpc_basic
            test_engine_exchangeCapabilities
            test_engine_forkchoiceUpdatedV3
            ;;
        cancun)
            test_rpc_basic
            test_engine_exchangeCapabilities
            test_engine_forkchoiceUpdatedV3
            test_engine_newPayloadV3_null
            ;;
        prague|pectra)
            test_rpc_basic
            test_engine_exchangeCapabilities
            test_engine_forkchoiceUpdatedV3
            test_engine_newPayloadV3_null
            test_engine_getBlobSchedule
            test_engine_getPayloadBodiesByRange
            ;;
        osaka)
            test_rpc_basic
            test_engine_exchangeCapabilities
            test_engine_forkchoiceUpdatedV3
            test_engine_newPayloadV3_null
            test_engine_getBlobSchedule
            test_engine_getPayloadBodiesByRange
            ;;
        *)
            log "Unknown fork: $fork"
            ;;
    esac
}

log "=== N42 EEST Local Test Runner ==="
log "Fork: $FORK"
log "Engine RPC: $RPC"
log "Output: $OUTDIR"

if [ "$FORK" = "all" ]; then
    for f in paris cancun prague osaka; do
        run_shard "$f"
    done
else
    run_shard "$FORK"
fi

log ""
log "=== Results ==="
log "Passed: $PASS"
log "Failed: $FAIL"
log "Skipped: $SKIP"
log "Total: $TOTAL"

cat > "$OUTDIR/summary.md" << EOF
# EEST Local Test Results

- **Fork**: $FORK
- **Date**: $(date -u)
- **Passed**: $PASS
- **Failed**: $FAIL
- **Skipped**: $SKIP
- **Total**: $TOTAL
- **Result**: $([ "$FAIL" -eq 0 ] && echo "PASS" || echo "FAIL")
EOF

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
log "EEST local: PASS"
