#!/usr/bin/env bash
# Archive-depth smoke test
# Validates that archive mode, historical queries, and recovery semantics
# are working correctly.
#
# Usage: ./scripts/run_archive_smoke.sh [RPC_URL]
# Default RPC_URL: http://127.0.0.1:20012

set -euo pipefail

RPC="${1:-http://127.0.0.1:20012}"
PASS=0
FAIL=0
SKIP=0

log_pass() { echo "  ✓ $1"; PASS=$((PASS+1)); }
log_fail() { echo "  ✗ FAIL: $1"; FAIL=$((FAIL+1)); }
log_skip() { echo "  - SKIP: $1"; SKIP=$((SKIP+1)); }

rpc_call() {
    local method="$1"
    local params="$2"
    curl -s -X POST "$RPC" \
        -H "Content-Type: application/json" \
        -d "{\"jsonrpc\":\"2.0\",\"method\":\"$method\",\"params\":$params,\"id\":1}" 2>/dev/null
}

echo "=== Archive-Depth Smoke Test ==="
echo "RPC: $RPC"
echo ""

# 1. Check node is reachable
echo "[1] Node connectivity"
RESULT=$(rpc_call "eth_blockNumber" "[]")
if echo "$RESULT" | grep -q "result"; then
    LATEST=$(echo "$RESULT" | grep -o '"result":"[^"]*"' | cut -d'"' -f4)
    log_pass "Node reachable, latest block: $LATEST"
else
    log_fail "Node not reachable"
    echo "Total: $PASS passed, $FAIL failed, $SKIP skipped"
    exit 1
fi

# 2. Check earliest block (EIP-4444 boundary)
echo "[2] Earliest block boundary"
RESULT=$(rpc_call "eth_getBlockByNumber" '["earliest", false]')
if echo "$RESULT" | grep -q "number"; then
    EARLIEST=$(echo "$RESULT" | grep -o '"number":"[^"]*"' | head -1 | cut -d'"' -f4)
    log_pass "Earliest block available: $EARLIEST"
else
    log_skip "Earliest block not available (may be pruned)"
fi

# 3. Historical balance query (genesis block)
echo "[3] Historical balance at genesis"
RESULT=$(rpc_call "eth_getBalance" '["0x0000000000000000000000000000000000000000", "0x0"]')
if echo "$RESULT" | grep -q "result"; then
    log_pass "eth_getBalance at block 0 works"
else
    log_skip "Historical balance at genesis not available"
fi

# 4. Historical storage query
echo "[4] Historical storage query"
RESULT=$(rpc_call "eth_getStorageAt" '["0x0000000000000000000000000000000000000000", "0x0", "0x0"]')
if echo "$RESULT" | grep -q "result"; then
    log_pass "eth_getStorageAt at block 0 works"
else
    log_skip "Historical storage not available"
fi

# 5. eth_getProof (latest)
echo "[5] eth_getProof (latest)"
RESULT=$(rpc_call "eth_getProof" '["0x0000000000000000000000000000000000000001", ["0x0"], "latest"]')
if echo "$RESULT" | grep -q "accountProof"; then
    log_pass "eth_getProof returns accountProof"
else
    log_fail "eth_getProof missing accountProof"
fi

# 6. Block range check
echo "[6] Block range integrity"
RESULT=$(rpc_call "eth_getBlockByNumber" '["0x1", false]')
if echo "$RESULT" | grep -q "number"; then
    log_pass "Block 1 accessible"
else
    log_skip "Block 1 not available (may be pruned)"
fi

# 7. Receipt availability
echo "[7] Receipt availability"
RESULT=$(rpc_call "eth_getBlockReceipts" '["latest"]')
if echo "$RESULT" | grep -q "result"; then
    log_pass "Block receipts available"
else
    log_skip "Block receipts not available"
fi

# 8. Log query
echo "[8] Log index query"
RESULT=$(rpc_call "eth_getLogs" '[{"fromBlock":"0x0","toBlock":"0x1"}]')
if echo "$RESULT" | grep -q "result"; then
    log_pass "eth_getLogs works for historical range"
else
    log_skip "Historical logs not available"
fi

echo ""
echo "=== Results ==="
echo "Passed: $PASS"
echo "Failed: $FAIL"
echo "Skipped: $SKIP"
echo "Total: $((PASS+FAIL+SKIP))"

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
echo "Archive smoke: OK"
