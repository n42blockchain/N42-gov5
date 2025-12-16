#!/bin/bash
# Blockscout RPC 兼容性测试脚本
# Copyright 2022-2026 The N42 Authors
#
# 使用方法: ./test_blockscout.sh [RPC_URL]
# 默认 RPC 地址: http://localhost:8545

set -e

RPC_URL="${1:-http://localhost:8545}"
LOG_FILE="blockscout_test.log"
PASS_COUNT=0
FAIL_COUNT=0

# 清空日志
> "$LOG_FILE"

echo "=============================================="
echo "Blockscout RPC 兼容性测试"
echo "=============================================="
echo "RPC URL: $RPC_URL"
echo "日志文件: $LOG_FILE"
echo "=============================================="

# 测试函数
test_rpc() {
    local METHOD=$1
    local PARAMS=$2
    local DESCRIPTION=$3
    
    printf "%-45s" "Testing $METHOD..."
    
    RESPONSE=$(curl -s -X POST -H "Content-Type: application/json" \
        --data "{\"jsonrpc\":\"2.0\",\"method\":\"$METHOD\",\"params\":[$PARAMS],\"id\":1}" \
        "$RPC_URL" 2>&1)
    
    echo "=== $METHOD ===" >> "$LOG_FILE"
    echo "Request: {\"method\":\"$METHOD\",\"params\":[$PARAMS]}" >> "$LOG_FILE"
    echo "Response: $RESPONSE" >> "$LOG_FILE"
    echo "" >> "$LOG_FILE"
    
    if echo "$RESPONSE" | grep -q '"result"'; then
        echo "✓ PASS"
        ((PASS_COUNT++))
    elif echo "$RESPONSE" | grep -q '"error"'; then
        ERROR_MSG=$(echo "$RESPONSE" | grep -o '"message":"[^"]*"' | cut -d'"' -f4)
        echo "✗ FAIL: $ERROR_MSG"
        ((FAIL_COUNT++))
    else
        echo "? UNKNOWN"
        ((FAIL_COUNT++))
    fi
}

echo ""
echo "=== 基础方法 (Basic Methods) ==="
test_rpc "eth_blockNumber" "" "获取当前区块号"
test_rpc "eth_chainId" "" "获取链 ID"
test_rpc "eth_gasPrice" "" "获取 Gas 价格"
test_rpc "eth_syncing" "" "获取同步状态"
test_rpc "eth_coinbase" "" "获取挖矿地址"
test_rpc "eth_mining" "" "获取挖矿状态"
test_rpc "eth_hashrate" "" "获取算力"
test_rpc "eth_accounts" "" "获取账户列表"

echo ""
echo "=== 区块方法 (Block Methods) ==="
test_rpc "eth_getBlockByNumber" '"latest", false' "获取最新区块(无交易详情)"
test_rpc "eth_getBlockByNumber" '"latest", true' "获取最新区块(含交易详情)"
test_rpc "eth_getBlockByNumber" '"0x0", false' "获取创世区块"
test_rpc "eth_getBlockByHash" '"0x0000000000000000000000000000000000000000000000000000000000000000", false' "按哈希获取区块"
test_rpc "eth_getBlockTransactionCountByNumber" '"latest"' "获取区块交易数量(按号)"
test_rpc "eth_getBlockTransactionCountByHash" '"0x0000000000000000000000000000000000000000000000000000000000000000"' "获取区块交易数量(按哈希)"
test_rpc "eth_getUncleCountByBlockNumber" '"latest"' "获取叔块数量(按号)"
test_rpc "eth_getUncleCountByBlockHash" '"0x0000000000000000000000000000000000000000000000000000000000000000"' "获取叔块数量(按哈希)"
test_rpc "eth_getBlockReceipts" '{"blockNumber": "latest"}' "获取区块所有收据"

echo ""
echo "=== 交易方法 (Transaction Methods) ==="
test_rpc "eth_getTransactionCount" '"0x0000000000000000000000000000000000000000", "latest"' "获取交易计数"
test_rpc "eth_getTransactionByBlockNumberAndIndex" '"latest", "0x0"' "按区块号和索引获取交易"
test_rpc "eth_getTransactionByBlockHashAndIndex" '"0x0000000000000000000000000000000000000000000000000000000000000000", "0x0"' "按哈希和索引获取交易"

echo ""
echo "=== 状态方法 (State Methods) ==="
test_rpc "eth_getBalance" '"0x0000000000000000000000000000000000000000", "latest"' "获取余额"
test_rpc "eth_getCode" '"0x0000000000000000000000000000000000000000", "latest"' "获取代码"
test_rpc "eth_getStorageAt" '"0x0000000000000000000000000000000000000000", "0x0", "latest"' "获取存储"

echo ""
echo "=== 调用方法 (Call Methods) ==="
test_rpc "eth_call" '{"to": "0x0000000000000000000000000000000000000000"}, "latest"' "执行调用"
test_rpc "eth_estimateGas" '{"to": "0x0000000000000000000000000000000000000000"}' "估算 Gas"

echo ""
echo "=== 过滤器方法 (Filter Methods) ==="
test_rpc "eth_getLogs" '{"fromBlock": "0x0", "toBlock": "latest"}' "获取日志"
test_rpc "eth_newBlockFilter" "" "创建区块过滤器"
test_rpc "eth_newPendingTransactionFilter" "" "创建待处理交易过滤器"

echo ""
echo "=== Gas 费用方法 (Gas Fee Methods) ==="
test_rpc "eth_maxPriorityFeePerGas" "" "获取最大优先费"
test_rpc "eth_feeHistory" '4, "latest", [25, 75]' "获取费用历史"

echo ""
echo "=== 网络方法 (Network Methods) ==="
test_rpc "net_version" "" "获取网络版本"
test_rpc "net_listening" "" "获取监听状态"
test_rpc "net_peerCount" "" "获取节点数量"

echo ""
echo "=== Web3 方法 (Web3 Methods) ==="
test_rpc "web3_clientVersion" "" "获取客户端版本"
test_rpc "web3_sha3" '"0x68656c6c6f"' "计算 SHA3"

echo ""
echo "=== 交易池方法 (TxPool Methods) ==="
test_rpc "txpool_status" "" "获取交易池状态"
test_rpc "txpool_content" "" "获取交易池内容"

echo ""
echo "=============================================="
echo "测试完成!"
echo "=============================================="
echo "通过: $PASS_COUNT"
echo "失败: $FAIL_COUNT"
echo "总计: $((PASS_COUNT + FAIL_COUNT))"
echo "=============================================="
echo "详细日志: $LOG_FILE"

if [ $FAIL_COUNT -gt 0 ]; then
    exit 1
else
    exit 0
fi

