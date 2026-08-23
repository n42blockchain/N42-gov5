#!/usr/bin/env bash
# Build the tx-lookup index INTO the weekly seed artifact (Step 2c).
#
# Why a scratch copy instead of the seed itself: a running node also writes
# HotStuff state into its chaindata, and the seed is copied verbatim to all 7
# nodes -- seeding them with one node's consensus state is not acceptable. So
# the index is built on a throwaway copy and only txindex/ (pure derived data)
# moves into the seed.
#
# Never carry a previous generation's txindex forward by hand. replay-v2 is
# append-only, so heights already in the base keep their numbers and their
# entries stay valid -- but the week being folded in is RENUMBERED by gap fill.
# An index built against the old numbering points at the wrong blocks, which is
# worse than having none.
#
#   ./build-seed-txindex.sh [--seed DIR] [--scratch DIR] [--bin PATH] [--from N]
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./qs-env.sh

SEED=$QS_SEED
SCRATCH=$QS_ROOT/qs-seedidx
BIN=$QS_BIN
FROM=0
TIMEOUT_MIN=90
while (( $# )); do
  case $1 in
    --seed)    SEED=$2; shift 2 ;;
    --scratch) SCRATCH=$2; shift 2 ;;
    --bin)     BIN=$2; shift 2 ;;
    --from)    FROM=$2; shift 2 ;;
    --timeout-min) TIMEOUT_MIN=$2; shift 2 ;;
    -h|--help) sed -n '2,18p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

[[ -r $SEED/SEAL_DONE.json ]] || { echo "no SEAL_DONE.json in $SEED -- run the seal step first" >&2; exit 1; }
[[ -x $BIN ]] || { echo "binary not found: $BIN" >&2; exit 1; }
WATERMARK=$QS_TOOLS/txindex-watermark
[[ -x $WATERMARK ]] || { echo "txindex-watermark not found at $WATERMARK" >&2; exit 1; }

json_num() { grep -o "\"$1\"[[:space:]]*:[[:space:]]*[0-9]*" "$2" | grep -o '[0-9]*$'; }
HEAD=$(json_num head "$SEED/SEAL_DONE.json")
(( FROM == 0 )) && FROM=$(json_num sealed_end "$SEED/SEAL_DONE.json")
[[ -n ${HEAD:-} && -n ${FROM:-} ]] || { echo "could not read head/sealed_end from SEAL_DONE.json" >&2; exit 1; }
echo "seed head=$HEAD  indexing from=$FROM  ($((HEAD - FROM)) blocks)"

rm -rf "$SCRATCH"
mkdir -p "$SCRATCH"
echo "copying chaindata to scratch ..."
cp -a "$SEED/chaindata" "$SCRATCH/chaindata"

# Seed the START watermark. The indexer defaults it to head+1 on first run --
# correct for a live node (no back-scan), wrong for a seed. Nodes booting from
# the seed then take max(watermark, SealedEnd(txindex)) and rebuild only the
# short tail.
"$WATERMARK" --db "$SCRATCH/chaindata" --set "$FROM"

# No miner, no peers, no discovery: this node must index, not produce or sync.
setsid "$BIN" --chain "$QS_CHAIN" --profile n42 \
  --data.dir "$SCRATCH" \
  --p2p.no-discovery --p2p.min-sync-peers 0 \
  --p2p.local-ip 127.0.0.1 --p2p.host-ip 127.0.0.1 \
  --p2p.tcp-port 32100 --p2p.udp-port 33100 \
  --http --http.addr 127.0.0.1 --http.port 20100 --http.api eth,web3,net \
  >"$SCRATCH/idx.log" 2>"$SCRATCH/idx.err" </dev/null &
PID=$!
echo "indexer node started (pid $PID); waiting for the backlog to seal ..."

# rebuildTxTail runs synchronously inside Start(), so "txindex tail enabled" is
# logged only after it returns -- that line is the completion signal.
deadline=$(( SECONDS + TIMEOUT_MIN * 60 ))
done_flag=0
while (( SECONDS < deadline )); do
  sleep 20
  if ! kill -0 "$PID" 2>/dev/null; then
    echo "indexer node exited early -- check $SCRATCH/idx.err"
    break
  fi
  segs=$(find "$SCRATCH/txindex" -name '*.cdat' 2>/dev/null | wc -l)
  echo "  segments=$segs"
  if grep -q 'txindex tail enabled' "$SCRATCH/log/n42.log" 2>/dev/null; then done_flag=1; break; fi
done
(( done_flag )) || echo "note: completion line not seen; installing whatever was built"

kill -INT "$PID" 2>/dev/null || true
waited=0
while kill -0 "$PID" 2>/dev/null && (( waited < 180 )); do sleep 3; waited=$((waited + 3)); done

segs=$(find "$SCRATCH/txindex" -name '*.cdat' 2>/dev/null | wc -l)
(( segs > 0 )) || { echo "NO SEGMENTS BUILT -- not touching the seed" >&2; exit 1; }
echo "built $segs segments; installing into the seed"
rm -rf "$SEED/txindex"
cp -a "$SCRATCH/txindex" "$SEED/txindex"
"$WATERMARK" --db "$SEED/chaindata" --set "$FROM"
echo "SEED INDEX INSTALLED"
