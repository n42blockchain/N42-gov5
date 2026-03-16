#!/bin/bash
# N42 Single-Node Stress Test
# Copyright 2022-2026 The N42 Authors
#
# Starts a dev node, builds and runs the Go stress test tool (cmd/stresstest),
# monitors logs for stability.
#
# Usage:
#   ./scripts/stress_test.sh                     # Default: 5 minutes
#   ./scripts/stress_test.sh --duration 3600     # 1 hour
#   ./scripts/stress_test.sh --tps 20            # 20 TPS
#   ./scripts/stress_test.sh --skip-node         # Use running node

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# Defaults — ports chosen to avoid conflict with n42-26 (18545-18551, 30303-30309)
DURATION=300           # 5 minutes default
TPS=30
HTTP_PORT=28545
WS_PORT=28546
P2P_PORT=40303
DATA_DIR=""
SKIP_NODE=false
LOG_LEVEL="info"

NODE_PID=""
STRESS_PID=""

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --duration)    DURATION="$2"; shift 2 ;;
        --tps)         TPS="$2"; shift 2 ;;
        --http-port)   HTTP_PORT="$2"; shift 2 ;;
        --skip-node)   SKIP_NODE=true; shift ;;
        --log-level)   LOG_LEVEL="$2"; shift 2 ;;
        --help)
            echo "Usage: $0 [options]"
            echo "  --duration N       Test duration in seconds (default: 300)"
            echo "  --tps N            Target TPS (default: 30)"
            echo "  --http-port N      HTTP RPC port (default: 28545)"
            echo "  --skip-node        Skip node startup (use existing node)"
            echo "  --log-level LEVEL  debug/info/warn (default: info)"
            exit 0
            ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

if [ -z "$DATA_DIR" ]; then
    DATA_DIR=$(mktemp -d "/tmp/n42-stress-XXXXXX")
fi
NODE_LOG="$DATA_DIR/node.log"
RPC_URL="http://127.0.0.1:$HTTP_PORT"
PASSWORD_FILE="$DATA_DIR/password.txt"

# =============================================================================
# Cleanup
# =============================================================================

cleanup() {
    echo ""
    echo "=========================================="
    echo "Cleaning up..."
    echo "=========================================="

    if [ -n "$STRESS_PID" ] && kill -0 "$STRESS_PID" 2>/dev/null; then
        kill "$STRESS_PID" 2>/dev/null || true
        wait "$STRESS_PID" 2>/dev/null || true
    fi

    if [ -n "$NODE_PID" ] && kill -0 "$NODE_PID" 2>/dev/null; then
        echo "Stopping node (PID $NODE_PID)..."
        kill -INT "$NODE_PID" 2>/dev/null || true
        for i in $(seq 1 10); do
            kill -0 "$NODE_PID" 2>/dev/null || break
            sleep 1
        done
        kill -0 "$NODE_PID" 2>/dev/null && kill -9 "$NODE_PID" 2>/dev/null
    fi

    # Log analysis
    if [ -f "$NODE_LOG" ]; then
        echo ""
        echo "=== Log Analysis ==="
        ERRORS=$(grep -ci "error\|panic\|fatal" "$NODE_LOG" 2>/dev/null || echo "0")
        PANICS=$(grep -ci "panic" "$NODE_LOG" 2>/dev/null || echo "0")
        LINES=$(wc -l < "$NODE_LOG" 2>/dev/null || echo "0")
        echo "Log lines:  $LINES"
        echo "Errors:     $ERRORS"
        echo "Panics:     $PANICS"

        if [ "$PANICS" -gt 0 ]; then
            echo ""
            echo "CRITICAL: Panic detected!"
            grep -i "panic" "$NODE_LOG" | head -5
        fi
    fi

    echo ""
    echo "Data dir: $DATA_DIR"
    echo "Node log: $NODE_LOG"
}

trap cleanup EXIT

# =============================================================================
# Build
# =============================================================================

echo "=========================================="
echo "N42 Stress Test"
echo "=========================================="
echo "Duration:   ${DURATION}s ($((DURATION / 60))m)"
echo "Target TPS: $TPS"
echo "RPC:        $RPC_URL"
echo "Data dir:   $DATA_DIR"
echo "=========================================="

cd "$PROJECT_DIR"

# Build stress test tool
echo "Building stress test tool..."
go build -tags nosqlite,noboltdb -o "$DATA_DIR/stresstest" ./cmd/stresstest/
echo "Build OK"

# Build node if needed
N42_BIN="$PROJECT_DIR/build/bin/n42"
if [ "$SKIP_NODE" = false ]; then
    if [ ! -f "$N42_BIN" ]; then
        echo "Building N42 node..."
        make n42
    fi
    echo "Node binary: $N42_BIN"
fi

# =============================================================================
# Start Node
# =============================================================================

if [ "$SKIP_NODE" = false ]; then
    # Create keystore account for mining
    echo "" > "$PASSWORD_FILE"  # empty password for dev

    # Create account using the n42 binary
    echo "Creating mining account..."
    ACCOUNT_OUTPUT=$("$N42_BIN" account new \
        --data.dir "$DATA_DIR/chaindata" \
        --password "$PASSWORD_FILE" 2>&1 || true)

    # Extract address from output
    ETHERBASE=$(echo "$ACCOUNT_OUTPUT" | grep -oE '0x[0-9a-fA-F]{40}' | head -1)

    if [ -z "$ETHERBASE" ]; then
        echo "ERROR: Failed to create account"
        echo "$ACCOUNT_OUTPUT"
        exit 1
    fi
    echo "Mining address: $ETHERBASE"

    echo "Starting N42 node..."
    "$N42_BIN" \
        --dev \
        --mine \
        --etherbase "$ETHERBASE" \
        --data.dir "$DATA_DIR/chaindata" \
        --http \
        --http.port "$HTTP_PORT" \
        --http.addr "127.0.0.1" \
        --http.api "eth,net,web3,txpool,debug" \
        --http.corsdomain "*" \
        --ws \
        --ws.port "$WS_PORT" \
        --ws.addr "127.0.0.1" \
        --port "$P2P_PORT" \
        --password "$PASSWORD_FILE" \
        --log.level "$LOG_LEVEL" \
        --dev.txgen=false \
        > "$NODE_LOG" 2>&1 &

    NODE_PID=$!
    echo "Node started (PID: $NODE_PID)"

    # Wait for RPC
    echo "Waiting for RPC..."
    RPC_READY=false
    for i in $(seq 1 60); do
        if curl -s -X POST -H "Content-Type: application/json" \
            --data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
            "$RPC_URL" 2>/dev/null | grep -q '"result"'; then
            echo "RPC ready!"
            RPC_READY=true
            break
        fi

        if ! kill -0 "$NODE_PID" 2>/dev/null; then
            echo "ERROR: Node died. Last 30 lines:"
            tail -30 "$NODE_LOG"
            exit 1
        fi

        sleep 1
    done

    if [ "$RPC_READY" = false ]; then
        echo "ERROR: RPC not ready in 60s"
        tail -30 "$NODE_LOG"
        exit 1
    fi

    # Wait for first block
    echo "Waiting for block production..."
    for i in $(seq 1 30); do
        BLOCK=$(curl -s -X POST -H "Content-Type: application/json" \
            --data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
            "$RPC_URL" 2>/dev/null | grep -oE '"0x[0-9a-fA-F]+"' | head -1 | tr -d '"')

        if [ -n "$BLOCK" ] && [ "$BLOCK" != "0x0" ]; then
            echo "Block production confirmed: $BLOCK"
            break
        fi
        sleep 2
    done
else
    echo "Using existing node at $RPC_URL"
fi

# =============================================================================
# Connection Info
# =============================================================================

echo ""
echo "=== Connection Info ==="
echo "HTTP RPC:   $RPC_URL"
echo "WebSocket:  ws://127.0.0.1:$WS_PORT"
echo ""
echo "Mobile wallet: add custom network RPC=$RPC_URL, Chain ID=94"
echo ""
echo "Blockscout:"
echo "  docker run -d -p 4000:4000 \\"
echo "    -e ETHEREUM_JSONRPC_HTTP_URL=$RPC_URL \\"
echo "    -e CHAIN_ID=94 -e COIN=N42 \\"
echo "    blockscout/blockscout:latest"
echo "========================"

# =============================================================================
# Run Stress Test
# =============================================================================

echo ""
echo "Starting stress test..."
"$DATA_DIR/stresstest" \
    --rpc "$RPC_URL" \
    --tps "$TPS" \
    --duration "$DURATION" &
STRESS_PID=$!

# Monitor
wait "$STRESS_PID" 2>/dev/null || true
STRESS_PID=""

echo ""
echo "Stress test complete!"
