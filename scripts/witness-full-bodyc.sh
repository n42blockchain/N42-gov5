#!/bin/bash
# witness-full-bodyc.sh — full-archive Ethereum witness replay over the N42
# columnar bodyc/headerc archive, on the process-sharded + input-reservoir deep
# path.
#
# Verification stays ON: every post-Byzantium block checks GasUsed AND
# ReceiptHash. --skip-verify and --continue-on-error are deliberately absent and
# must not be added — a run that used either is not a gate result.
#
# Parameters come from the 2026-08-25 sizing sweep (see
# docs/ethel/witness-bodyc-vs-freezer-and-full-run-2026-08-25.md): 4 process
# shards x 254 total workers x 4 total readers, byte-accounted input reservoir
# 20/10 GiB, GOGC 400, 80 GiB total soft heap ceiling. The same setting won on
# both the sparse early chain and the dense 24M range, and peaked at 82.3 GiB
# summed RSS across the four children.
set -u
set -o pipefail

BIN=${BIN:-/home/n42/src/n42/N42-gov5/build/bin/witness-replay}
INPUT=${INPUT:-/data/blockchain/witness}
CODE_MDBX=${CODE_MDBX:-/data/blockchain/code-mdbx}
START=${START:-0}
END=${END:-25765566}          # exclusive; witness/senders items = 25,765,566
SHARDS=${SHARDS:-4}
WORKERS=${WORKERS:-254}
READERS=${READERS:-4}
HIGH_GB=${HIGH_GB:-20}
LOW_GB=${LOW_GB:-10}
GOGC=${GOGC:-400}
MEM_GB=${MEM_GB:-80}

RUN_ID=${RUN_ID:-full-bodyc-$(date -u +%Y%m%d-%H%M%S)}
LOGDIR=${LOGDIR:-/data/blockchain/wr-logs}
OUTDIR=${OUTDIR:-/data/blockchain/wr-out/$RUN_ID}
LOG="$LOGDIR/$RUN_ID.log"
TIME="$LOGDIR/$RUN_ID.time"
RSS="$LOGDIR/$RUN_ID.rss"

mkdir -p "$LOGDIR" "$OUTDIR"
ulimit -n 65536

{
  echo "run_id     $RUN_ID"
  echo "binary     $BIN"
  echo "sha256     $(sha256sum "$BIN" | cut -d' ' -f1)"
  echo "go_version $(go version 2>/dev/null)"
  echo "commit     $(git -C /home/n42/src/n42/N42-gov5 rev-parse HEAD 2>/dev/null)"
  git -C /home/n42/src/n42/N42-gov5 diff --quiet 2>/dev/null || echo "worktree   DIRTY (see the run's diff sidecar)"
  echo "range      $START..$END (exclusive end)"
  echo "shards     $SHARDS  workers $WORKERS  readers $READERS"
  echo "reservoir  ${HIGH_GB}/${LOW_GB} GiB   gogc $GOGC   mem_limit ${MEM_GB} GiB"
} | tee "$LOGDIR/$RUN_ID.meta"

# Peak summed RSS across the shard children — /usr/bin/time only reports the
# largest single child, which understates a 4-process run by roughly 4x.
(
  peak=0
  while pgrep -f "$(basename "$BIN")" >/dev/null; do
    s=$(ps -eo rss,args --no-headers | grep "$(basename "$BIN")" | grep -v grep | awk '{s+=$1} END{print s+0}')
    [ "$s" -gt "$peak" ] && peak=$s
    echo "$(date -u +%H:%M:%S) sum_rss_kb=$s peak_kb=$peak"
    sleep 5
  done
  echo "PEAK_SUM_RSS_KB=$peak"
) > "$RSS" 2>&1 &
SAMPLER=$!

/usr/bin/time -v -o "$TIME" "$BIN" \
  --input-headers-bodies "$INPUT" \
  --input-witness "$INPUT" \
  --senders "$INPUT" \
  --datadir "$CODE_MDBX" \
  --output "$OUTDIR" \
  --no-output \
  --start "$START" --end "$END" \
  --process-shards "$SHARDS" \
  --workers "$WORKERS" \
  --readers "$READERS" \
  --input-high-gb "$HIGH_GB" \
  --input-low-gb "$LOW_GB" \
  --gogc "$GOGC" \
  --mem-limit-gb "$MEM_GB" \
  2>&1 | tee "$LOG"
rc=${PIPESTATUS[0]}

wait $SAMPLER 2>/dev/null

echo
echo "=== acceptance ==="
echo "exit code                 $rc"
echo "reservoir enabled lines   $(grep -c 'Input reservoir enabled' "$LOG")  (want $SHARDS)"
echo "shard completions         $(grep -c 'Replay complete' "$LOG")  (want $SHARDS)"
grep -o 'failed=[0-9]*' "$LOG" | sort | uniq -c
grep 'Process-sharded verification complete' "$LOG" || echo "MISSING: parent completion line"
grep 'restored legacy EIP-7702' "$LOG" && echo "NOTE: old-format bodyc segments still in use"
tail -1 "$RSS"
exit $rc
