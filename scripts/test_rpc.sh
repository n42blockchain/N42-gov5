#!/bin/bash
# RPC Compatibility Test Script for N42
# Usage: ./test_rpc.sh [RPC_URL]

RPC_URL="${1:-http://localhost:8545}"

echo "==================================="
echo "N42 RPC Compatibility Test"
echo "RPC URL: $RPC_URL"
echo "==================================="
echo ""

# Color codes
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Test counter
PASSED=0
FAILED=0

# Function to run a test
run_test() {
    local name="$1"
    local method="$2"
    local params="$3"
    
    echo -n "Testing $name... "
    
    local response=$(curl -s -X POST "$RPC_URL" \
        -H "Content-Type: application/json" \
        --data "{\"jsonrpc\":\"2.0\",\"method\":\"$method\",\"params\":$params,\"id\":1}")
    
    if echo "$response" | grep -q '"result"'; then
        echo -e "${GREEN}PASS${NC}"
        ((PASSED++))
        return 0
    else
        echo -e "${RED}FAIL${NC}"
        echo "  Response: $response"
        ((FAILED++))
        return 1
    fi
}

# Test eth_blockNumber
run_test "eth_blockNumber" "eth_blockNumber" "[]"

# Test eth_chainId
run_test "eth_chainId" "eth_chainId" "[]"

# Test eth_getBlockByNumber (latest)
run_test "eth_getBlockByNumber" "eth_getBlockByNumber" "[\"latest\", false]"

# Test eth_gasPrice
run_test "eth_gasPrice" "eth_gasPrice" "[]"

# Test eth_getBalance (zero address)
run_test "eth_getBalance" "eth_getBalance" "[\"0x0000000000000000000000000000000000000000\", \"latest\"]"

# Test eth_getCode (zero address)
run_test "eth_getCode" "eth_getCode" "[\"0x0000000000000000000000000000000000000000\", \"latest\"]"

# Test eth_getTransactionCount (zero address)
run_test "eth_getTransactionCount" "eth_getTransactionCount" "[\"0x0000000000000000000000000000000000000000\", \"latest\"]"

# Test net_version
run_test "net_version" "net_version" "[]"

# Test web3_clientVersion
run_test "web3_clientVersion" "web3_clientVersion" "[]"

# Test txpool_status
run_test "txpool_status" "txpool_status" "[]"

echo ""
echo "==================================="
echo "Test Summary"
echo "==================================="
echo -e "Passed: ${GREEN}$PASSED${NC}"
echo -e "Failed: ${RED}$FAILED${NC}"
echo "Total:  $((PASSED + FAILED))"
echo "==================================="

if [ $FAILED -gt 0 ]; then
    exit 1
fi
exit 0

