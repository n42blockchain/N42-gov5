#!/usr/bin/env bash
# Offline follower-path replay on the reflink copy of qs-node0 (QS_REPLAN 4.0).
# Usage: replay-run.sh <tag> <bin> [extra n42 flags...]   e.g. --replay.scan
# Copy of the datadir: cp -a --reflink=always /data/blockchain/qs-node0 /data/blockchain/qs-replay-node0 (xfs, ~11 s for 200 GB)
set -euo pipefail
export TZ=America/New_York
TAG=$1; BIN=$2; shift 2
DD=/data/blockchain/qs-replay-node0
L=/data/blockchain/wr-logs/replay-$TAG.log
# Same node environment as run-r35za.sh (the fleet's follower), minus the pool/miner/p2p levers.
export N42_HISTORY_INDEX_DEFERRED=1 N42_STATE_READ_QMDB=1 N42_STATE_WRITE_QMDB_ONLY=1 N42_WRITE_PROBE=1
export GOMEMLIMIT=10GiB GOGC=200 N42_SENDER_CACHE_SLOTS=4194304 N42_BLOCK_CACHE_BLOCKS=4
export N42_PARALLEL_WORKERS=${REPLAY_WORKERS:-32} N42_MDBX_MAPSIZE_GB=256
export N42_SLOW_BLOCK_MS=0   # "blockimport phases" for every block
echo "[$(date +%H:%M:%S)] replay $TAG bin=$BIN workers=$N42_PARALLEL_WORKERS args=$*" | tee -a $L
exec nice -n 10 "$BIN" --parallel-evm qs-replay --chain mainnet_qmdb_staggered --profile n42 --data.dir "$DD" \
  --pprof.maxcpu "${REPLAY_MAXCPU:-32}" "$@" 2>&1 | tee -a $L
