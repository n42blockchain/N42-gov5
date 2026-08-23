#!/usr/bin/env bash
# One qs throughput-benchmark round: launch the fleet, flood it, measure, stop.
#
#   ./bench-run.sh --offset 1600000 --tag pool300k --pool-slots 300000
#   ./bench-run.sh --offset 1700000 --tag bcast --broadcast --decay-sec 90
#
# Hard-won rules encoded here (docs/QS_TPS_BENCHMARK.md):
#   * --offset MUST be fresh per round. Reusing a drained/nonce-used sender set
#     produces a demote spiral that looks exactly like a node failure.
#   * The pool journal is cleared first: a restored journal replays the previous
#     round's unmined txs, and a round whose flood never launched still reported
#     ~10k TPS purely from leftovers.
#   * RPC readiness is polled, never slept on: a fixed sleep raced the MDBX open
#     and the flood died on "connection refused" with a 0-tx window.
#   * --decay-sec idles empty blocks first so a baseFee left ELEVATED by a
#     previous full-block round decays back to the floor. Without it a
#     back-to-back round inherits the prior baseFee, the fixed 10 gwei flood
#     cannot fill every block, and occupancy collapses to ~53% (alternating
#     full/empty) -- incomparable with a decayed-start round. 90 s is about 90
#     blocks at 1 s pacing, and 0.875^90 reaches the floor.
#   * Compare like-numbered windows across rounds. baseFee also climbs WITHIN a
#     round (12.5% per full block), so win3 of a full-block round measures the
#     flood's fixed price cap, not the chain.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./qs-env.sh

POOL_SLOTS=300000; POOL_QUEUE=100000; INTERVAL_MS=1000
OFFSET=900000; WINDOWS=3; SENDERS=3000; PERTX=3000
BROADCAST=0; TAG=run; PROFILING=0; DECAY_SEC=0
BIN=$QS_BIN
while (( $# )); do
  case $1 in
    --pool-slots)  POOL_SLOTS=$2; shift 2 ;;
    --pool-queue)  POOL_QUEUE=$2; shift 2 ;;
    --interval-ms) INTERVAL_MS=$2; shift 2 ;;
    --offset)      OFFSET=$2; shift 2 ;;
    --windows)     WINDOWS=$2; shift 2 ;;
    --senders)     SENDERS=$2; shift 2 ;;
    --pertx)       PERTX=$2; shift 2 ;;
    --decay-sec)   DECAY_SEC=$2; shift 2 ;;
    --tag)         TAG=$2; shift 2 ;;
    --bin)         BIN=$2; shift 2 ;;
    --broadcast)   BROADCAST=1; shift ;;
    --profiling)   PROFILING=1; shift ;;
    -h|--help)     sed -n '2,25p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

TXFLOOD=$QS_TOOLS/txflood
[[ -x $TXFLOOD ]] || { echo "txflood not found at $TXFLOOD" >&2; exit 1; }
OUT=$QS_ROOT/bench-flood-$TAG.out
ERR=$QS_ROOT/bench-flood-$TAG.err

echo "=== $TAG : bin=$(basename "$BIN") pool=$POOL_SLOTS/$POOL_QUEUE interval=${INTERVAL_MS}ms offset=$OFFSET broadcast=$BROADCAST ==="
for i in {0..6}; do rm -f "$QS_NODE_ROOT$i/txpool.journal"; done

bench_args=(--bin "$BIN" --pool-slots "$POOL_SLOTS" --pool-queue "$POOL_QUEUE" --interval-ms "$INTERVAL_MS")
if (( PROFILING )); then bench_args+=(--profiling); fi
./bench-7node.sh "${bench_args[@]}" >/dev/null

rpc() {
  curl -s -m 6 -X POST -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$2\",\"params\":[]}" "http://127.0.0.1:$1"
}

ready=0; deadline=$(( SECONDS + 240 ))
while (( SECONDS < deadline )); do
  sleep 5; ok=0
  for (( p = QS_HTTP_BASE; p <= QS_HTTP_BASE + 6; p++ )); do
    case "$(rpc "$p" eth_blockNumber)" in *'"result"'*) ok=$(( ok + 1 )) ;; esac
  done
  if (( ok == 7 )); then ready=1; break; fi
done
if (( ! ready )); then echo "RPC not ready on all 7 nodes - aborting round" >&2; exit 1; fi
echo "all 7 RPC ready"

if (( DECAY_SEC > 0 )); then
  echo "decaying baseFee for ${DECAY_SEC}s of empty blocks before the flood..."
  n0=$(rpc "$QS_HTTP_BASE" eth_blockNumber | grep -o '0x[0-9a-f]*')
  sleep "$DECAY_SEC"
  n1=$(rpc "$QS_HTTP_BASE" eth_blockNumber | grep -o '0x[0-9a-f]*')
  gp=$(rpc "$QS_HTTP_BASE" eth_gasPrice | grep -o '0x[0-9a-f]*')
  echo "decay done: $(( n1 - n0 )) empty blocks produced, gasPrice=$(( gp )) wei"
fi

rpcs=""
for (( p = QS_HTTP_BASE; p <= QS_HTTP_BASE + 6; p++ )); do rpcs="$rpcs""http://127.0.0.1:$p,"; done
flood=(-rpc "${rpcs%,}" -senders "$SENDERS" -pertx "$PERTX" -gasprice 10000000000
       -rpcbatch 100 -conc 32 -sender-offset "$OFFSET")
if (( BROADCAST )); then flood+=(-broadcast); else flood+=(-shard-senders); fi
setsid "$TXFLOOD" "${flood[@]}" >"$OUT" 2>"$ERR" </dev/null &
FLOOD_PID=$!

# Funding and pre-signing must finish before the measured windows open.
flooding=0; deadline=$(( SECONDS + 300 ))
while (( SECONDS < deadline )); do
  sleep 5
  if grep -q '^flooding ' "$OUT" 2>/dev/null; then flooding=1; break; fi
  if ! kill -0 "$FLOOD_PID" 2>/dev/null; then break; fi
done
if (( flooding )); then
  echo "flood is submitting; opening measurement windows"
  sleep 15   # let the pool reach depth
else
  echo "flood never reached the flooding stage - check $ERR" >&2
  tail -3 "$ERR" 2>/dev/null
fi

./measure-tps.sh --windows "$WINDOWS" --window-sec 60

kill -TERM "$FLOOD_PID" 2>/dev/null || true
./stop-fleet.sh --no-inspect
echo "=== $TAG done ==="
