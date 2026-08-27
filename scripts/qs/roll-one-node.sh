#!/usr/bin/env bash
# Gracefully restart ONE fleet node onto a new binary.
#
# Roll nodes one at a time and verify block-height convergence before moving to
# the next. The argument set is built by deploy-7node.sh, which this script
# re-invokes for a single index -- there is no second copy of the launch
# arguments to drift out of sync.
#
#   ./roll-one-node.sh --node 3 --bin /data/blockchain/bin/n42-v5.7.956
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./qs-env.sh

NODE=""
BIN=$QS_BIN
TXGEN_MAX=31
while (( $# )); do
  case $1 in
    --node)      NODE=$2; shift 2 ;;
    --bin)       BIN=$2; shift 2 ;;
    --txgen-max) TXGEN_MAX=$2; shift 2 ;;
    -h|--help)   sed -n '2,10p' "$0"; exit 0 ;;
    *)           echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[[ $NODE =~ ^[0-6]$ ]] || { echo "--node must be 0..6" >&2; exit 2; }
[[ -x $BIN ]] || { echo "binary not found or not executable: $BIN" >&2; exit 1; }

qs_stop_node "$NODE"
sleep 2   # let the kernel release the listen sockets

qs_load_validators
qs_place_keys "$NODE"
qs_launch_node "$NODE" "$BIN" "$TXGEN_MAX"

echo
echo "Verify convergence before rolling the next node:"
echo "  curl -s -X POST -H 'Content-Type: application/json' \\"
echo "    -d '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"eth_blockNumber\",\"params\":[]}' \\"
echo "    http://127.0.0.1:$((QS_HTTP_BASE + NODE))"
