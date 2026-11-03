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
OFFSET=900000; WINDOWS=3; WINDOW_SEC=60; SENDERS=3000; PERTX=3000
GASPRICE=10000000000
RPC_MAX_GAS_PRICE=1000000000000
CHAIN_ID=94
CPU_PROFILE_SEC=0
# Supply knobs. Defaults are the historical rig settings, so a round that does
# not pass them stays comparable with every recorded round. Raise them when the
# CHAIN outruns the load generator -- the signature is occupancy falling while
# block time keeps dropping, which is the harness's ceiling, not the chain's.
CONC=32; RPCBATCH=100
# Number of txflood processes. One generator tops out well below what the chain
# can absorb once block time drops: at 0.571 s blocks a single flood at conc 96
# filled only 68% of them. Each process gets its own sender range, and each
# funds its own senders -- so the faucet cost multiplies by FLOODS too.
FLOODS=1
# Submissions per second, 0 = as fast as possible. Unpaced, a generator dumps
# its whole pre-signed set and EXITS: 12M transactions in 81 s, after which the
# pool (300k slots, ~13 blocks) drains and occupancy falls while the chain is
# still healthy. Pace it slightly above what the chain consumes and the supply
# lasts the whole round.
RATE=0
TARGET_DEPTH=0
TX_GOSSIP=1
STREAM=0
INGEST_BASE=0
BROADCAST=0; TAG=run; PROFILING=0; DECAY_SEC=0
# Spread the flood over N recipients instead of the single 0x..dEaD sink. This
# decides WHAT the round measures: at 0 a full block writes ~1,201 accounts (the
# senders plus the sink) and the state work is nearly nothing; at 22857 it writes
# one per transaction. Every cross-client number in docs/QS_TPS_BENCHMARK.md
# before 2026-09-02 was taken at 0 and is not comparable with a spread workload.
RECIPIENTS=0
# Block size is set by the gas ceiling, not by --interval-ms: at 21000 gas a
# transfer, 480M gas is 22,857 transactions and the block fills before the
# interval expires. Vary this to separate a real per-block cost from one that
# only looks large because it is divided by a small block (rule 20).
GASCEIL=480000000
# 5% over the 1.0 gwei floor: the floor itself reads back a few wei high.
DECAY_FLOOR_WEI=1050000000
BIN=$QS_BIN
while (( $# )); do
  case $1 in
    --pool-slots)  POOL_SLOTS=$2; shift 2 ;;
    --pool-queue)  POOL_QUEUE=$2; shift 2 ;;
    --interval-ms) INTERVAL_MS=$2; shift 2 ;;
    --gasceil)     GASCEIL=$2; shift 2 ;;
    --offset)      OFFSET=$2; shift 2 ;;
    --windows)     WINDOWS=$2; shift 2 ;;
    --window-sec)  WINDOW_SEC=$2; shift 2 ;;
    --gasprice)    GASPRICE=$2; shift 2 ;;
    --rpc-maxgasprice) RPC_MAX_GAS_PRICE=$2; shift 2 ;;
    --chainid)     CHAIN_ID=$2; shift 2 ;;
    --cpu-profile-sec) CPU_PROFILE_SEC=$2; shift 2 ;;
    --senders)     SENDERS=$2; shift 2 ;;
    --pertx)       PERTX=$2; shift 2 ;;
    --conc)        CONC=$2; shift 2 ;;
    --rpcbatch)    RPCBATCH=$2; shift 2 ;;
    --floods)      FLOODS=$2; shift 2 ;;
    --rate)        RATE=$2; shift 2 ;;
    --target-depth) TARGET_DEPTH=$2; shift 2 ;;
    --no-tx-gossip) TX_GOSSIP=0; shift ;;
    --stream)      STREAM=1; shift ;;
    --ingest-base) INGEST_BASE=$2; shift 2 ;;
    --recipients)  RECIPIENTS=$2; shift 2 ;;
    --decay-sec)   DECAY_SEC=$2; shift 2 ;;
    --tag)         TAG=$2; shift 2 ;;
    --bin)         BIN=$2; shift 2 ;;
    --broadcast)   BROADCAST=1; shift ;;
    --profiling)   PROFILING=1; shift ;;
    -h|--help)     sed -n '2,25p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

[[ $INGEST_BASE =~ ^(0|[1-9][0-9]*)$ && ${#INGEST_BASE} -le 5 ]] && (( INGEST_BASE == 0 || (INGEST_BASE > 1024 && INGEST_BASE <= 65529) )) || {
  echo "invalid --ingest-base (0=off, otherwise 1025..65529)" >&2; exit 2;
}
export QS_INGEST_BASE=$INGEST_BASE

for value in "$POOL_SLOTS" "$POOL_QUEUE" "$INTERVAL_MS" "$GASCEIL" "$WINDOWS" "$WINDOW_SEC" "$SENDERS" "$PERTX" "$CONC" "$RPCBATCH" "$FLOODS" "$GASPRICE" "$CHAIN_ID" "$RPC_MAX_GAS_PRICE"; do
  [[ $value =~ ^[1-9][0-9]*$ && ${#value} -le 18 ]] || { echo "invalid positive benchmark parameter: $value" >&2; exit 2; }
done
for value in "$OFFSET" "$RATE" "$TARGET_DEPTH" "$RECIPIENTS" "$DECAY_SEC" "$CPU_PROFILE_SEC"; do
  [[ $value =~ ^(0|[1-9][0-9]*)$ && ${#value} -le 12 ]] || { echo "invalid nonnegative benchmark parameter: $value" >&2; exit 2; }
done
if (( INGEST_BASE && RPCBATCH < 2 )); then echo "binary ingest requires --rpcbatch >= 2" >&2; exit 2; fi
(( RPCBATCH <= 200 )) || { echo "--rpcbatch exceeds eth_batchRawTransaction's 200-tx limit" >&2; exit 2; }
(( GASPRICE <= RPC_MAX_GAS_PRICE )) || { echo "--gasprice exceeds --rpc-maxgasprice" >&2; exit 2; }
(( CPU_PROFILE_SEC <= 60 )) || { echo "--cpu-profile-sec must be at most 60" >&2; exit 2; }
(( TARGET_DEPTH == 0 || FLOODS == 1 )) || { echo "--target-depth requires one coordinated flood process" >&2; exit 2; }
(( SENDERS <= 1000000 )) || { echo "sender ranges would overlap across flood processes" >&2; exit 2; }
[[ $TAG =~ ^[a-zA-Z0-9_-]+$ ]] || { echo "invalid --tag" >&2; exit 2; }

TXFLOOD=$QS_TOOLS/txflood
[[ -x $TXFLOOD ]] || { echo "txflood not found at $TXFLOOD" >&2; exit 1; }
JOURNAL_RESET=$QS_TOOLS/txpool-journal-reset
[[ -x $JOURNAL_RESET ]] || {
  echo "txpool-journal-reset not found at $JOURNAL_RESET" >&2
  echo "build it from ./cmd/txpool-journal-reset before benchmarking" >&2
  exit 1
}
OUT=$QS_ROOT/bench-flood-$TAG.out
ERR=$QS_ROOT/bench-flood-$TAG.err
RESULT=$QS_ROOT/bench-result-$TAG.json

# Fail before stopping a fleet or opening any journals if this execution
# environment cannot run a networked benchmark or write the target directories.
python3 ./bench-preflight.py --root "$QS_ROOT" --node-root "$QS_NODE_ROOT" --seed "$QS_SEED" --bin "$BIN"

# Record only explicit benchmark settings, never validator/faucet key material.
python3 - "$QS_ROOT/bench-config-$TAG.json" "$BIN" \
  tag "$TAG" chain "$QS_CHAIN" chain_id "$CHAIN_ID" node_root "$QS_NODE_ROOT" \
  txflood_binary "$QS_TOOLS/txflood" \
  gas_limit "$GASCEIL" gas_price "$GASPRICE" rpc_max_gas_price "$RPC_MAX_GAS_PRICE" interval_ms "$INTERVAL_MS" \
  windows "$WINDOWS" window_seconds "$WINDOW_SEC" senders "$SENDERS" \
  per_sender "$PERTX" recipients "$RECIPIENTS" offset "$OFFSET" \
  concurrency "$CONC" rpc_batch "$RPCBATCH" floods "$FLOODS" rate "$RATE" target_depth "$TARGET_DEPTH" \
  broadcast "$BROADCAST" tx_gossip "$TX_GOSSIP" stream "$STREAM" ingest_base "$INGEST_BASE" pool_slots "$POOL_SLOTS" pool_queue "$POOL_QUEUE" \
  profiling "$PROFILING" cpu_profile_seconds "$CPU_PROFILE_SEC" <<'PY'
import hashlib, json, os, platform, sys
from pathlib import Path
output, binary = Path(sys.argv[1]), Path(sys.argv[2])
pairs = iter(sys.argv[3:])
record = dict(zip(pairs, pairs))
with binary.open('rb') as stream:
    record['binary_sha256'] = hashlib.file_digest(stream, 'sha256').hexdigest()
record['binary'] = str(binary.resolve())
with Path(record['txflood_binary']).open('rb') as stream:
    record['txflood_sha256'] = hashlib.file_digest(stream, 'sha256').hexdigest()
record['harness_sha256'] = {}
for name in ('bench-run.sh', 'bench-7node.sh', 'qs-env.sh', 'stop-fleet.sh',
             'measure-tps.sh', 'measure-tps.py', 'bench-preflight.py'):
    with Path(name).open('rb') as stream:
        record['harness_sha256'][name] = hashlib.file_digest(stream, 'sha256').hexdigest()
record['cpu_affinity'] = sorted(os.sched_getaffinity(0))
record['requested_node_cpu_sets'] = {str(i): os.environ[f'QS_CPUSET_{i}'] for i in range(7)
                                     if os.environ.get(f'QS_CPUSET_{i}')}
record['requested_flood_cpu_set'] = os.environ.get('QS_FLOOD_CPUSET', '')
record['kernel'] = platform.release()
record['environment'] = {key: os.environ.get(key) for key in (
    'WRITE_MAP', 'NO_SYNC', 'N42_MDBX_SYNC', 'N42_MDBX_DIRTY_GB', 'MDBX_READAHEAD', 'N42_MDBX_COMMIT_TRACE', 'N42_MDBX_EARLY_WRITEBACK',
    'QS_MAXCPU', 'N42_MAX_GOSSIP_MB', 'N42_MDBX_MAPSIZE_GB', 'N42_TXINDEX_TAIL',
    'N42_BLOCK_CACHE_BLOCKS', 'N42_PUSH_BEFORE_WRITE', 'N42_JOURNAL_TRACE',
    'N42_SENDER_CACHE_SLOTS', 'N42_SEPARATE_VOTE_DB', 'N42_STATE_READ_QMDB', 'N42_ACCOUNT_PREFETCH_WORKERS', 'N42_QMDB_RETAIN_ENTRIES', 'GOGC', 'GOMEMLIMIT')}
output.write_text(json.dumps(record, indent=2) + '\n')
PY

FLOOD_PIDS=(); FLOOD_OUTS=(); LAUNCH_ATTEMPTED=0
kill_floods() { for pid in "${FLOOD_PIDS[@]}"; do kill -TERM "$pid" 2>/dev/null || true; done; }
cleanup() {
  local result=$?
  trap - EXIT INT TERM
  kill_floods
  if (( LAUNCH_ATTEMPTED )); then ./stop-fleet.sh --no-inspect || result=1; fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

echo "=== $TAG : bin=$(basename "$BIN") pool=$POOL_SLOTS/$POOL_QUEUE interval=${INTERVAL_MS}ms gasceil=${GASCEIL:-default} offset=$OFFSET broadcast=$BROADCAST recipients=${RECIPIENTS} supply=${FLOODS}x${SENDERS}x${PERTX}@conc${CONC}/batch${RPCBATCH}/rate${RATE} ==="

# A benchmark is a different launch profile, not an in-place mutation. If a
# normal fleet is already listening, readiness probes can accidentally measure
# those old processes after the benchmark launch loses its port race.
running=0
for i in {0..6}; do qs_node_pid "$i" >/dev/null && running=1; done
if (( running )); then
  echo "stopping existing fleet before applying benchmark parameters..."
  if ! ./stop-fleet.sh --no-inspect; then
    echo "fleet did not stop cleanly; refusing to open live MDBX journals" >&2
    exit 1
  fi
fi

# The current txpool journal lives in MDBX TxPoolJournal and is flushed during
# the graceful stop above. Removing the old file path is therefore insufficient
# (and doing it before stop is backwards): the benchmark would restore normal
# txgen traffic, spend the faucet and contaminate its first blocks. Reset only
# while every node is stopped, using the purpose-built tool. Keep the legacy
# file cleanup for node generations created before the MDBX journal migration.
for i in {0..6}; do
  d="$QS_NODE_ROOT$i"
  [[ -d $d/chaindata ]] || continue  # launcher seeds a fresh node below
  if ! "$JOURNAL_RESET" -datadir "$d/chaindata" -apply; then
    echo "failed to clear persisted txpool journal for node $i" >&2
    exit 1
  fi
  rm -f "$d/txpool.journal"
done

bench_args=(--bin "$BIN" --pool-slots "$POOL_SLOTS" --pool-queue "$POOL_QUEUE" --interval-ms "$INTERVAL_MS")
bench_args+=(--gasceil "$GASCEIL")
bench_args+=(--rpc-maxgasprice "$RPC_MAX_GAS_PRICE")
if (( TARGET_DEPTH )); then bench_args+=(--txpool-rpc); fi
if (( ! TX_GOSSIP )); then bench_args+=(--no-tx-gossip); fi
if (( INGEST_BASE )); then bench_args+=(--ingest-base "$INGEST_BASE"); fi
if (( PROFILING )); then bench_args+=(--profiling); fi
LAUNCH_ATTEMPTED=1
./bench-7node.sh "${bench_args[@]}" >/dev/null

sleep 2
launch_ok=1
for i in {0..6}; do
  pid=$(qs_node_pid "$i") || { echo "benchmark node $i failed to start" >&2; launch_ok=0; continue; }
  args=$(tr '\0' ' ' <"/proc/$pid/cmdline")
  [[ $args == *"--block-interval-ms $INTERVAL_MS"* &&
     $args == *"--miner.gasceil $GASCEIL "* &&
     $args == *"--txpool.globalslots $POOL_SLOTS"* ]] || {
    echo "node $i is not running the requested benchmark profile" >&2
    launch_ok=0
  }
done
if (( ! launch_ok )); then
  ./stop-fleet.sh --no-inspect || true
  exit 1
fi

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
if (( INGEST_BASE )); then
  python3 - "$INGEST_BASE" <<'PY'
import socket, sys
for port in range(int(sys.argv[1]), int(sys.argv[1]) + 7):
    with socket.create_connection(('127.0.0.1', port), timeout=5) as conn:
        conn.sendall(bytes(4))  # Empty frame: verify protocol without submitting a transaction.
        response = b''
        while len(response) < 4:
            chunk = conn.recv(4 - len(response))
            if not chunk:
                raise SystemExit(f'ingest port {port}: premature EOF')
            response += chunk
        if response != bytes(4):
            raise SystemExit(f'ingest port {port}: invalid empty-frame acknowledgement')
print('all 7 binary ingest endpoints ready')
PY
fi
python3 - "$QS_HTTP_BASE" "$CHAIN_ID" <<'PY'
import importlib.util, sys
spec = importlib.util.spec_from_file_location('measure', 'measure-tps.py')
measure = importlib.util.module_from_spec(spec)
spec.loader.exec_module(measure)
for port in range(int(sys.argv[1]), int(sys.argv[1]) + 7):
    chain = measure.quantity(measure.rpc(f'http://127.0.0.1:{port}', 'eth_chainId', []))
    if chain != int(sys.argv[2]):
        raise SystemExit(f'port {port}: chain ID {chain} differs from requested {sys.argv[2]}')
PY

if (( DECAY_SEC > 0 )); then
  # The decay is only real if the chain is PRODUCING. After a heavy round the
  # fleet can spend minutes re-converging (large journal clears, follower
  # alignment), and a fixed sleep then reports "0 empty blocks produced" while
  # the baseFee sits exactly where the previous round left it -- which is rule
  # 6 all over again, and the round that follows dies in FUNDING (rule 8).
  # So: sleep the requested decay, then keep waiting until the chain has both
  # produced blocks and reached the 1.0 gwei floor. Refuse the round rather
  # than measure an inherited fee market.
  echo "decaying baseFee for ${DECAY_SEC}s of empty blocks before the flood..."
  n0=$(rpc "$QS_HTTP_BASE" eth_blockNumber | grep -o '0x[0-9a-f]*')
  sleep "$DECAY_SEC"
  decayed=0; deadline=$(( SECONDS + 600 ))
  while (( SECONDS < deadline )); do
    n1=$(rpc "$QS_HTTP_BASE" eth_blockNumber | grep -o '0x[0-9a-f]*')
    gp=$(rpc "$QS_HTTP_BASE" eth_gasPrice | grep -o '0x[0-9a-f]*')
    if (( n1 > n0 && gp <= DECAY_FLOOR_WEI )); then decayed=1; break; fi
    sleep 10
  done
  echo "decay done: $(( n1 - n0 )) empty blocks produced, gasPrice=$(( gp )) wei"
  if (( ! decayed )); then
    echo "baseFee did not reach the floor (${DECAY_FLOOR_WEI} wei) and/or the chain is not producing" >&2
    echo "refusing to measure a round that inherits the previous round's fee market" >&2
    ./stop-fleet.sh --no-inspect
    exit 1
  fi
fi

rpcs=""
for (( p = QS_HTTP_BASE; p <= QS_HTTP_BASE + 6; p++ )); do rpcs="$rpcs""http://127.0.0.1:$p,"; done

# One generator per FLOODS, each on its own sender range. The ranges are spaced
# a million apart rather than by SENDERS: an overlap silently reuses accounts
# that the other generator has already advanced the nonce on, which is rule 1's
# demote spiral with a harder-to-see cause.
for (( f = 0; f < FLOODS; f++ )); do
  fout=$OUT; ferr=$ERR
  if (( FLOODS > 1 )); then fout=$OUT.$f; ferr=$ERR.$f; fi
  flood=(-rpc "${rpcs%,}" -chainid "$CHAIN_ID" -senders "$SENDERS" -pertx "$PERTX" -gasprice "$GASPRICE"
         -rpcbatch "$RPCBATCH" -conc "$CONC" -sender-offset "$(( OFFSET + f * 1000000 ))")
  if (( RATE > 0 )); then flood+=(-rate "$RATE"); fi
  if (( TARGET_DEPTH > 0 )); then flood+=(-target-depth "$TARGET_DEPTH"); fi
  if (( STREAM )); then flood+=(-stream); fi
  if (( INGEST_BASE )); then
    ingest_addrs=""
    for (( p=INGEST_BASE; p<INGEST_BASE+7; p++ )); do ingest_addrs+="127.0.0.1:$p,"; done
    flood+=(-ingest "${ingest_addrs%,}")
  fi
  if (( RECIPIENTS > 0 )); then flood+=(-recipients "$RECIPIENTS"); fi
  if (( BROADCAST )); then flood+=(-broadcast); else flood+=(-shard-senders); fi
  flood_pin=()
  if [[ -n ${QS_FLOOD_CPUSET:-} ]]; then flood_pin=(taskset -c "$QS_FLOOD_CPUSET"); fi
  setsid "${flood_pin[@]}" "$TXFLOOD" "${flood[@]}" >"$fout" 2>"$ferr" </dev/null &
  FLOOD_PIDS+=($!)
  FLOOD_OUTS+=("$fout")

  # Every generator funds its senders FROM THE SAME dev faucet, so two of them
  # funding at once race on that one account's nonce: the second gets
  # "replacement transaction underpriced" and its funding never confirms, while
  # the first floods happily and the round looks like it has half its supply.
  # Start the next one only once this one is past funding and submitting.
  if (( f + 1 < FLOODS )); then
    echo "waiting for flood $f to finish funding before starting the next..."
    deadline=$(( SECONDS + 600 ))
    while (( SECONDS < deadline )); do
      grep -q '^flooding ' "$fout" 2>/dev/null && break
      kill -0 "${FLOOD_PIDS[f]}" 2>/dev/null || break
      sleep 5
    done
  fi
done

# Funding and pre-signing must finish before the measured windows open — for
# EVERY generator. Opening the windows while one is still funding measures a
# supply ramp, not the chain.
flooding=0; deadline=$(( SECONDS + 600 ))
while (( SECONDS < deadline )); do
  sleep 5
  ready=0; alive=0
  for (( f = 0; f < FLOODS; f++ )); do
    if grep -q '^flooding ' "${FLOOD_OUTS[f]}" 2>/dev/null; then ready=$(( ready + 1 )); fi
    if kill -0 "${FLOOD_PIDS[f]}" 2>/dev/null; then alive=$(( alive + 1 )); fi
  done
  if (( ready == FLOODS )); then flooding=1; break; fi
  if (( alive == 0 )); then break; fi
done
if (( flooding )); then
  echo "all $FLOODS flood(s) submitting; opening measurement windows"
  sleep 15   # let the pool reach depth
else
  echo "a flood never reached the flooding stage - check $ERR*" >&2
  for (( f = 0; f < FLOODS; f++ )); do
    tail -2 "${FLOOD_OUTS[f]/$OUT/$ERR}" 2>/dev/null
  done
  kill_floods
  ./stop-fleet.sh --no-inspect
  exit 1
fi

./measure-tps.sh --windows "$WINDOWS" --window-sec "$WINDOW_SEC" --expected-gas-limit "$GASCEIL" --json-out "$RESULT"

if (( CPU_PROFILE_SEC > 0 )); then
  echo "measurement complete; collecting node 0 CPU profile outside measured windows"
  curl -fsS --max-time "$((CPU_PROFILE_SEC + 30))" \
    "http://127.0.0.1:$QS_PPROF_BASE/debug/pprof/profile?seconds=$CPU_PROFILE_SEC" \
    -o "$QS_ROOT/bench-cpu-$TAG.pprof"
fi

kill_floods
./stop-fleet.sh --no-inspect
LAUNCH_ATTEMPTED=0
echo "=== $TAG done ==="
