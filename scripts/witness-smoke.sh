#!/usr/bin/env bash
# Read-only format smoke for the Ethereum historical witness dataset.
# This proves that the input tables open and the selected range replays with
# failed=0. Early blocks are intentionally not used as a performance result.
set -euo pipefail

D=${D:-/data/blockchain/witness}
CODE_DB=${CODE_DB:-/data/blockchain/code-mdbx}
BIN=${BIN:-/data/blockchain/bin/witness-replay}
START=${START:-0}
COUNT=${COUNT:-200000}
WORKERS=${WORKERS:-32}
MEM=${MEM:-32}
GOGC=${GOGC:-300}
RUN_ID=${RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)-$$}
OUT=${OUT:-/data/blockchain/wr-smoke/$RUN_ID}
LOG=${LOG:-/data/blockchain/wr-logs/smoke-$RUN_ID.log}
END=$((START + COUNT))

mkdir -p "$OUT" "$(dirname "$LOG")"

for path in "$BIN" "$D/headerc.cidx" "$D/bodyc.cidx" \
  "$D/witness.cidx" "$D/senders.cidx" "$CODE_DB/mdbx.dat"; do
  if [[ ! -e "$path" ]]; then
    echo "ERROR: required input missing: $path" >&2
    exit 1
  fi
done

echo "Ethereum witness format smoke"
echo "input=$D code-db=$CODE_DB range=[$START,$END) workers=$WORKERS output=$OUT"

"$BIN" --input-headers-bodies "$D" --input-witness "$D" \
  --datadir "$CODE_DB" --senders "$D" --output "$OUT" \
  --no-output \
  --start "$START" --end "$END" --workers "$WORKERS" \
  --gogc "$GOGC" --mem-limit-gb "$MEM" 2>&1 | tee "$LOG"

line=$(grep 'Replay complete' "$LOG" | tail -n 1 || true)
if [[ -z "$line" || "$line" != *"failed=0"* ]]; then
  echo "ERROR: smoke did not finish with failed=0; see $LOG" >&2
  exit 1
fi

echo "PASS: format smoke completed with failed=0"
echo "log=$LOG"
echo "NOTE: this early-block result is not a worker-scaling benchmark."
