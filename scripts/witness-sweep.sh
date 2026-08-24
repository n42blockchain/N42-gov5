#!/usr/bin/env bash
# Ethereum witness-replay block-worker sweep.
#
# The first run is always a workers=1 correctness gate over the same dense
# block interval. Any replay failure aborts the sweep. No N42 fleet sender,
# nonce, faucet, txpool, or reward mechanism is involved here.
#
#   ./witness-sweep.sh
#   START=24000000 COUNT=10000 ./witness-sweep.sh
#   WORKERS="8 16 32 64" ./witness-sweep.sh
set -euo pipefail

D=${D:-/data/blockchain/witness}
HB=${HB:-/data/blockchain/witness-geth}
CODE_DB=${CODE_DB:-/data/blockchain/code-mdbx}
BIN=${BIN:-/data/blockchain/bin/witness-replay}
HEADS_BIN=${HEADS_BIN:-/data/blockchain/bin/freezer-heads}
OUTROOT=${OUTROOT:-/data/blockchain/wr-out}
LOGROOT=${LOGROOT:-/data/blockchain/wr-logs}
START=${START:-24000000}
COUNT=${COUNT:-10000}
WORKERS=${WORKERS:-"8 16 32 64 128"}
MEM=${MEM:-96}
GOGC=${GOGC:-300}
RUN_ID=${RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)-$$}
END=$((START + COUNT))
RUNDIR="$OUTROOT/$RUN_ID"
LOGDIR="$LOGROOT/$RUN_ID"

mkdir -p "$RUNDIR" "$LOGDIR"

for path in "$BIN" "$HB/headers.cidx" "$HB/bodies.cidx" \
  "$D/witness.cidx" "$D/senders.cidx" "$CODE_DB/mdbx.dat"; do
  if [[ ! -e "$path" ]]; then
    echo "ERROR: required input missing: $path" >&2
    exit 1
  fi
done

echo "Ethereum witness block-worker sweep"
echo "host: $(nproc) threads, $(free -g | awk '/^Mem:/{print $2}') GiB RAM"
echo "headers-bodies=$HB witness=$D code-db=$CODE_DB range=[$START,$END) mem-limit=${MEM}g gogc=$GOGC"
echo "run=$RUN_ID"

if [[ -x "$HEADS_BIN" ]]; then
  "$HEADS_BIN" "$HB" "$D" 2>&1 | tee "$LOGDIR/input-heads.log"
  bodies_items=$(awk '$1 == "bodies" {for (i=1; i<=NF; i++) if ($i ~ /^items=/) {sub(/^items=/, "", $i); print $i}}' "$LOGDIR/input-heads.log")
  headers_items=$(awk '$1 == "headers" {for (i=1; i<=NF; i++) if ($i ~ /^items=/) {sub(/^items=/, "", $i); print $i}}' "$LOGDIR/input-heads.log")
  witness_items=$(awk '$1 == "witness" {for (i=1; i<=NF; i++) if ($i ~ /^items=/) {sub(/^items=/, "", $i); print $i}}' "$LOGDIR/input-heads.log")
  senders_items=$(awk '$1 == "senders" {for (i=1; i<=NF; i++) if ($i ~ /^items=/) {sub(/^items=/, "", $i); print $i}}' "$LOGDIR/input-heads.log")
  if [[ -z "$bodies_items" || "$bodies_items" -lt "$END" ]]; then
    echo "ERROR: geth bodies coverage ${bodies_items:-unknown} does not reach $END" >&2
    exit 1
  fi
  if [[ -z "$headers_items" || "$headers_items" -lt "$END" ]]; then
    echo "ERROR: geth headers coverage ${headers_items:-unknown} does not reach $END" >&2
    exit 1
  fi
  if [[ -z "$witness_items" || "$witness_items" -lt "$END" ]]; then
    echo "ERROR: witness coverage ${witness_items:-unknown} does not reach $END" >&2
    exit 1
  fi
  if [[ -z "$senders_items" || "$senders_items" -lt "$END" ]]; then
    echo "ERROR: senders coverage ${senders_items:-unknown} does not reach $END" >&2
    exit 1
  fi
fi

extract_field() {
  local line=$1 key=$2 field
  for field in $line; do
    if [[ "$field" == "$key="* ]]; then
      printf '%s\n' "${field#*=}"
      return 0
    fi
  done
  return 1
}

printf '\n%-8s %12s %12s %12s %10s %8s\n' workers block/s tx/s Mgas/s wall_s failed

run_one() {
  local workers=$1 phase=$2
  local out="$RUNDIR/${phase}-w${workers}"
  local log="$LOGDIR/${phase}-w${workers}.log"
  local t0 t1 wall line txs blk gas failed txps

  mkdir -p "$out"
  t0=$(date +%s)
  if ! "$BIN" --input-headers-bodies "$HB" --input-witness "$D" \
      --datadir "$CODE_DB" --senders "$D" --output "$out" \
      --no-output \
      --start "$START" --end "$END" --workers "$workers" \
      --gogc "$GOGC" --mem-limit-gb "$MEM" 2>&1 | tee "$log"; then
    echo "ERROR: $phase workers=$workers failed; see $log" >&2
    exit 1
  fi
  t1=$(date +%s)
  wall=$((t1 - t0))
  line=$(grep 'Replay complete' "$log" | tail -n 1 || true)
  if [[ -z "$line" ]]; then
    echo "ERROR: missing Replay complete line; see $log" >&2
    exit 1
  fi

  failed=$(extract_field "$line" failed || true)
  if [[ "$failed" != "0" ]]; then
    echo "ERROR: $phase workers=$workers finished with failed=${failed:-unknown}" >&2
    exit 1
  fi

  blk=$(extract_field "$line" blk/s || true)
  gas=$(extract_field "$line" mgas/s || true)
  txs=$(extract_field "$line" txs || true)
  txps="?"
  if (( wall > 0 )) && [[ "$txs" =~ ^[0-9]+$ ]]; then
    txps=$((txs / wall))
  fi
  printf '%-8s %12s %12s %12s %10s %8s\n' \
    "$workers" "${blk:-?}" "$txps" "${gas:-?}" "$wall" "$failed"
}

echo "gate: workers=1, fail-fast, verification enabled"
run_one 1 gate

for workers in $WORKERS; do
  if [[ "$workers" == "1" ]]; then
    continue
  fi
  run_one "$workers" sweep
done

echo
echo "PASS: all worker counts completed the identical dense range with failed=0"
echo "logs=$LOGDIR"
echo "outputs=$RUNDIR (no-output mode; directories contain no replay payload)"
