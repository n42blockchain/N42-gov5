#!/usr/bin/env bash
# Read-only correctness smoke for the Ethereum historical witness dataset.
# Headers/bodies, witness, and senders come from the N42 columnar freezer.
# This is not an N42 fleet transaction benchmark.
set -euo pipefail

D=${D:-/data/blockchain/witness}
HB=${HB:-/data/blockchain/witness}
CODE_DB=${CODE_DB:-/data/blockchain/code-mdbx}
BIN=${BIN:-/data/blockchain/bin/witness-replay}
START=${START:-24000022}
COUNT=${COUNT:-1}
WORKERS=${WORKERS:-1}
MEM=${MEM:-32}
GOGC=${GOGC:-300}
RUN_ID=${RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)-$$}
OUT=${OUT:-/data/blockchain/wr-smoke/$RUN_ID}
LOG=${LOG:-/data/blockchain/wr-logs/smoke-$RUN_ID.log}
END=$((START + COUNT))

mkdir -p "$OUT" "$(dirname "$LOG")"

for path in "$BIN" "$HB/headerc.cidx" "$HB/bodyc.cidx" \
  "$D/witness.cidx" "$D/senders.cidx" "$CODE_DB/mdbx.dat"; do
  if [[ ! -e "$path" ]]; then
    echo "ERROR: required input missing: $path" >&2
    exit 1
  fi
done

echo "Ethereum witness format smoke"
echo "headers-bodies=$HB witness=$D code-db=$CODE_DB range=[$START,$END) workers=$WORKERS output=$OUT"

"$BIN" --input-headers-bodies "$HB" --input-witness "$D" \
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
echo "NOTE: this single-block result is a correctness gate, not a worker-scaling benchmark."
