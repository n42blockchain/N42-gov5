#!/bin/bash
# N42 Node Health Check Script
# Usage: ./scripts/check_node.sh [rpc_url]

RPC=${1:-http://localhost:20012}

echo "========================================="
echo "N42 Node Health Check"
echo "RPC: $RPC"
echo "========================================="

# 1. Block Height
echo ""
echo "--- Block Height ---"
BLOCK=$(curl -s "$RPC" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | grep -o '"result":"[^"]*"' | cut -d'"' -f4)
echo "Block: $BLOCK ($(printf '%d' $BLOCK 2>/dev/null || echo 'parse error'))"

# 2. Chain ID
echo ""
echo "--- Chain ID ---"
CHAIN=$(curl -s "$RPC" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":2}' | grep -o '"result":"[^"]*"' | cut -d'"' -f4)
echo "Chain ID: $CHAIN ($(printf '%d' $CHAIN 2>/dev/null || echo 'parse error'))"

# 3. Latest Block Header
echo ""
echo "--- Latest Block Header ---"
curl -s "$RPC" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest",false],"id":3}' | python3 -m json.tool 2>/dev/null || \
curl -s "$RPC" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest",false],"id":3}'

# 4. Peer Count
echo ""
echo "--- Peer Count ---"
PEERS=$(curl -s "$RPC" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"net_peerCount","params":[],"id":4}' | grep -o '"result":"[^"]*"' | cut -d'"' -f4)
echo "Peers: $PEERS ($(printf '%d' $PEERS 2>/dev/null || echo 'none'))"

# 5. Syncing Status
echo ""
echo "--- Syncing ---"
curl -s "$RPC" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_syncing","params":[],"id":5}' | grep -o '"result":[^,}]*'

# 6. Mining Status
echo ""
echo "--- Mining ---"
curl -s "$RPC" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_mining","params":[],"id":6}' | grep -o '"result":[^,}]*'

# 7. Gas Price
echo ""
echo "--- Gas Price ---"
GAS=$(curl -s "$RPC" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_gasPrice","params":[],"id":7}' | grep -o '"result":"[^"]*"' | cut -d'"' -f4)
echo "Gas Price: $GAS"

# 8. TX Pool Status
echo ""
echo "--- TX Pool ---"
curl -s "$RPC" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"txpool_status","params":[],"id":8}' | grep -o '"result":{[^}]*}'

# 9. Check Header Fields (21 fields for ETH Pectra compat)
echo ""
echo "--- Header Field Check ---"
HEADER=$(curl -s "$RPC" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest",false],"id":9}')

for field in parentHash sha3Uncles miner stateRoot transactionsRoot receiptsRoot logsBloom difficulty number gasLimit gasUsed timestamp extraData mixHash nonce baseFeePerGas withdrawalsRoot blobGasUsed excessBlobGas parentBeaconBlockRoot requestsRoot; do
  if echo "$HEADER" | grep -q "\"$field\""; then
    echo "  ✓ $field"
  else
    echo "  ✗ $field (MISSING)"
  fi
done

# 10. Block production rate (check 2 blocks 10s apart)
echo ""
echo "--- Block Production ---"
B1=$(curl -s "$RPC" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":10}' | grep -o '"result":"[^"]*"' | cut -d'"' -f4)
echo "Block at t=0: $B1"
sleep 10
B2=$(curl -s "$RPC" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":11}' | grep -o '"result":"[^"]*"' | cut -d'"' -f4)
echo "Block at t=10s: $B2"
if [ "$B1" != "$B2" ]; then
  echo "✓ Blocks are being produced"
else
  echo "✗ No new blocks in 10 seconds"
fi

echo ""
echo "========================================="
echo "Check complete"
echo "========================================="
