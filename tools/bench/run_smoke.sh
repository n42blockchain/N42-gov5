#!/bin/bash
# =============================================================================
# N42 Smoke Test Script
# =============================================================================
#
# This script runs basic smoke tests to verify node functionality.
# Usage: ./run_smoke.sh [RPC_URL]
#
# Exit codes:
#   0 - All tests passed
#   1 - One or more tests failed
#

set -e

RPC_URL="${1:-http://localhost:8545}"

# Color codes
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "==================================="
echo "N42 Smoke Test Suite"
echo "RPC URL: $RPC_URL"
echo "Time: $(date)"
echo "==================================="
echo ""

# Test counters
PASSED=0
FAILED=0
TOTAL=0

# Function to run a test
run_test() {
    local name="$1"
    local method="$2"
    local params="$3"
    local expected_field="${4:-result}"
    
    ((TOTAL++))
    echo -n "[$TOTAL] Testing $name... "
    
    local start_time=$(date +%s%N)
    local response=$(curl -s -X POST "$RPC_URL" \
        -H "Content-Type: application/json" \
        --connect-timeout 5 \
        --max-time 30 \
        --data "{\"jsonrpc\":\"2.0\",\"method\":\"$method\",\"params\":$params,\"id\":1}" 2>&1)
    local end_time=$(date +%s%N)
    
    local duration_ms=$(( (end_time - start_time) / 1000000 ))
    
    if echo "$response" | grep -q "\"$expected_field\""; then
        echo -e "${GREEN}PASS${NC} (${duration_ms}ms)"
        ((PASSED++))
        return 0
    else
        echo -e "${RED}FAIL${NC}"
        echo "  Response: ${response:0:200}"
        ((FAILED++))
        return 1
    fi
}

# Function to run a test with value check
run_test_with_check() {
    local name="$1"
    local method="$2"
    local params="$3"
    local check_script="$4"
    
    ((TOTAL++))
    echo -n "[$TOTAL] Testing $name... "
    
    local start_time=$(date +%s%N)
    local response=$(curl -s -X POST "$RPC_URL" \
        -H "Content-Type: application/json" \
        --connect-timeout 5 \
        --max-time 30 \
        --data "{\"jsonrpc\":\"2.0\",\"method\":\"$method\",\"params\":$params,\"id\":1}" 2>&1)
    local end_time=$(date +%s%N)
    
    local duration_ms=$(( (end_time - start_time) / 1000000 ))
    
    if echo "$response" | eval "$check_script" > /dev/null 2>&1; then
        echo -e "${GREEN}PASS${NC} (${duration_ms}ms)"
        ((PASSED++))
        return 0
    else
        echo -e "${RED}FAIL${NC}"
        echo "  Response: ${response:0:200}"
        ((FAILED++))
        return 1
    fi
}

echo "=== Basic Connectivity ==="
run_test "eth_chainId" "eth_chainId" "[]"
run_test "net_version" "net_version" "[]"
run_test "web3_clientVersion" "web3_clientVersion" "[]"

echo ""
echo "=== Block Operations ==="
run_test "eth_blockNumber" "eth_blockNumber" "[]"
run_test "eth_getBlockByNumber (latest)" "eth_getBlockByNumber" "[\"latest\", false]"
run_test "eth_getBlockByNumber (earliest)" "eth_getBlockByNumber" "[\"earliest\", false]"
run_test "eth_getBlockByNumber (with txs)" "eth_getBlockByNumber" "[\"latest\", true]"

echo ""
echo "=== Account Operations ==="
run_test "eth_getBalance" "eth_getBalance" "[\"0x0000000000000000000000000000000000000000\", \"latest\"]"
run_test "eth_getTransactionCount" "eth_getTransactionCount" "[\"0x0000000000000000000000000000000000000000\", \"latest\"]"
run_test "eth_getCode" "eth_getCode" "[\"0x0000000000000000000000000000000000000000\", \"latest\"]"
run_test "eth_getStorageAt" "eth_getStorageAt" "[\"0x0000000000000000000000000000000000000000\", \"0x0\", \"latest\"]"

echo ""
echo "=== Gas Operations ==="
run_test "eth_gasPrice" "eth_gasPrice" "[]"
run_test "eth_maxPriorityFeePerGas" "eth_maxPriorityFeePerGas" "[]"

echo ""
echo "=== Transaction Pool ==="
run_test "txpool_status" "txpool_status" "[]"
run_test "txpool_content" "txpool_content" "[]"

echo ""
echo "=== Call Operations ==="
# Simple call to zero address (should return empty)
run_test "eth_call (simple)" "eth_call" "[{\"to\":\"0x0000000000000000000000000000000000000000\",\"data\":\"0x\"}, \"latest\"]"

echo ""
echo "=== Estimate Gas ==="
run_test "eth_estimateGas (transfer)" "eth_estimateGas" "[{\"from\":\"0x0000000000000000000000000000000000000000\",\"to\":\"0x0000000000000000000000000000000000000001\",\"value\":\"0x0\"}]"

echo ""
echo "==================================="
echo "Test Summary"
echo "==================================="
echo -e "Passed: ${GREEN}$PASSED${NC}"
echo -e "Failed: ${RED}$FAILED${NC}"
echo "Total:  $TOTAL"
echo "==================================="

if [ $FAILED -gt 0 ]; then
    echo -e "${RED}SMOKE TEST FAILED${NC}"
    exit 1
fi

echo -e "${GREEN}ALL SMOKE TESTS PASSED${NC}"
exit 0

