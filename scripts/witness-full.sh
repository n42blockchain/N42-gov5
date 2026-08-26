#!/bin/bash
# witness-full.sh — full-archive Ethereum witness replay, single process.
#
# Body/header source defaults to the geth ancient freezer. Do NOT point it at
# the columnar bodyc archive for a full run: a bodyc reader pins a whole decoded
# 8192-block segment, and over the full history that collapses dense bands to
# 165-630 blk/s (see docs/ethel/witness-bodyc-vs-freezer-and-full-run-2026-08-25.md
# §5.6). Same binary and parameters on the freezer: 1h13m23s, 446,152 CPU-s.
#
# Verification stays ON (GasUsed + ReceiptHash). --skip-verify and
# --continue-on-error are deliberately absent; a run that used either is not a
# gate result. Rank runs by CPU-seconds first, wall clock second.
#
# Production configuration (3/3 passes, wall 54m10s-54m35s, 395-398k CPU-s):
# WORKERS=128 READERS=6 GOGC=300 MEM_GB=56 with the tier-1 code (2026-08-26).
# Baseline history: base binary 128w/gc100 = 1h06m29s, 458k CPU-s;
# 104w/gc100 = 1h09m20s, 417k CPU-s. GOGC 300 is required by the tier-1 code:
# its smaller live heap makes GOGC 100 collect too often and starve the readers.
set -u
set -o pipefail
BIN=${BIN:-/home/n42/src/n42/N42-gov5/build/bin/witness-replay}
HB=${HB:-/data/blockchain/witness-geth}
READERS=${READERS:-6}
# The kernel truncates comm to 15 chars, so pgrep -x / ps comm must be matched
# on the truncated name. Matching the full basename silently never matches.
PROC=$(basename "$BIN"); PROC=${PROC:0:15}
RUN_ID=${RUN_ID:?RUN_ID required}
WORKERS=${WORKERS:-128}
GOGC=${GOGC:-300}
MEM_GB=${MEM_GB:-56}
HIGH_GB=${HIGH_GB:-0}
LOW_GB=${LOW_GB:-0}
LOGDIR=/data/blockchain/wr-logs
OUTDIR=/data/blockchain/wr-out/$RUN_ID
mkdir -p "$LOGDIR" "$OUTDIR"
ulimit -n 65536

RES=()
[ "$HIGH_GB" != "0" ] && RES=(--input-high-gb "$HIGH_GB" --input-low-gb "$LOW_GB")

{ echo "run_id  $RUN_ID"
  echo "binary  $BIN"
  echo "sha256  $(sha256sum "$BIN" | cut -d' ' -f1)"
  echo "config  single process, source $HB, workers $WORKERS, readers $READERS, gogc $GOGC, mem-limit ${MEM_GB} GiB, reservoir ${HIGH_GB}/${LOW_GB} GiB"
  echo "range   0..25765566"
} | tee "$LOGDIR/$RUN_ID.meta"

( peak=0; waited=0
  while ! pgrep -x "$PROC" >/dev/null; do sleep 1; waited=$((waited+1)); [ $waited -gt 180 ] && break; done
  while pgrep -x "$PROC" >/dev/null; do
    s=$(ps -eo rss,comm --no-headers | awk -v p="$PROC" '$2==p {s+=$1} END{print s+0}')
    [ "$s" -gt "$peak" ] && peak=$s
    echo "$(date -u +%H:%M:%S) sum_rss_kb=$s peak_kb=$peak"
    sleep 10
  done
  echo "PEAK_SUM_RSS_KB=$peak" ) > "$LOGDIR/$RUN_ID.rss" 2>&1 &
SAMPLER=$!

/usr/bin/time -v -o "$LOGDIR/$RUN_ID.time" "$BIN" \
  --input-headers-bodies "$HB" \
  --input-witness /data/blockchain/witness \
  --senders /data/blockchain/witness \
  --datadir /data/blockchain/code-mdbx \
  --output "$OUTDIR" \
  --no-output \
  --start 0 --end 25765566 \
  --workers "$WORKERS" --readers "$READERS" --gogc "$GOGC" --mem-limit-gb "$MEM_GB" "${RES[@]}" \
  2>&1 | tee "$LOGDIR/$RUN_ID.log"
rc=${PIPESTATUS[0]}
wait $SAMPLER 2>/dev/null
echo "=== $RUN_ID exit $rc ==="
grep -o 'failed=[0-9]*' "$LOGDIR/$RUN_ID.log" | sort | uniq -c
grep 'Replay complete' "$LOGDIR/$RUN_ID.log" | tail -1
tail -1 "$LOGDIR/$RUN_ID.rss"
exit $rc
