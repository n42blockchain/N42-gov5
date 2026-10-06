#!/usr/bin/env bash
# Benchmark variant of the fleet launcher: same keys, peer IDs and ports as
# deploy-7node.sh (it shares qs_build_args), plus the throughput-tier knobs.
#
#   ./bench-7node.sh [--pool-slots N] [--pool-queue N] [--interval-ms N]
#                    [--gasceil N] [--maxcpu N] [--profiling]
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./qs-env.sh

DATA=$QS_SEED
BIN=$QS_BIN
POOL_SLOTS=60000        # executable pool capacity (>= one block of txs)
POOL_QUEUE=40000        # future-nonce capacity
INTERVAL_MS=1000        # block pacing (bench: 800-1000)
GASCEIL=480000000       # 480M gas tier
RPC_MAX_GAS_PRICE=1000000000000
TXGEN_MAX=0
PROFILING=0
TXPOOL_RPC=0
TX_GOSSIP=1
INGEST_BASE=0
# GOMAXPROCS per node. The Windows rig used 5 on a 32-thread host (7x5=35,
# deliberately just over the thread count). Scale the same way here rather than
# inheriting a number tuned for different hardware.
MAXCPU=${QS_MAXCPU:-$(( ($(nproc) + 6) / 7 ))}

while (( $# )); do
  case $1 in
    --data)        DATA=$2; shift 2 ;;
    --bin)         BIN=$2; shift 2 ;;
    --pool-slots)  POOL_SLOTS=$2; shift 2 ;;
    --pool-queue)  POOL_QUEUE=$2; shift 2 ;;
    --interval-ms) INTERVAL_MS=$2; shift 2 ;;
    --gasceil)     GASCEIL=$2; shift 2 ;;
    --rpc-maxgasprice) RPC_MAX_GAS_PRICE=$2; shift 2 ;;
    --maxcpu)      MAXCPU=$2; shift 2 ;;
    --txgen-max)   TXGEN_MAX=$2; shift 2 ;;
    --profiling)   PROFILING=1; shift ;;
    --txpool-rpc)  TXPOOL_RPC=1; shift ;;
    --no-tx-gossip) TX_GOSSIP=0; shift ;;
    --ingest-base) INGEST_BASE=$2; shift 2 ;;
    -h|--help)     sed -n '2,7p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

[[ $INGEST_BASE =~ ^(0|[1-9][0-9]*)$ && ${#INGEST_BASE} -le 5 ]] && (( INGEST_BASE == 0 || (INGEST_BASE > 1024 && INGEST_BASE <= 65529) )) || {
  echo "invalid --ingest-base" >&2; exit 2;
}
export QS_INGEST_BASE=$INGEST_BASE
export QS_INGEST_HARDCAP=$POOL_SLOTS

[[ $RPC_MAX_GAS_PRICE =~ ^[1-9][0-9]*$ && ${#RPC_MAX_GAS_PRICE} -le 18 ]] || {
  echo "invalid --rpc-maxgasprice" >&2; exit 2;
}
for value in "$POOL_SLOTS" "$POOL_QUEUE" "$INTERVAL_MS" "$GASCEIL" "$MAXCPU"; do
  [[ $value =~ ^[1-9][0-9]*$ && ${#value} -le 12 ]] || { echo "invalid positive benchmark parameter: $value" >&2; exit 2; }
done
for i in {0..6}; do
  [[ -d $QS_NODE_ROOT$i/chaindata || -d $DATA/chaindata ]] || {
    echo "node $i has no chaindata and seed not found at $DATA" >&2; exit 1;
  }
done
[[ -x $BIN ]] || { echo "binary not found: $BIN" >&2; exit 1; }
qs_load_validators

# Let gasceil jump to the target in one block instead of ramping.
export N42_STRESS_GASLIMIT=1
# Packing budget = 90% x MaxGossipSize - 32KB. The 1 MiB default caps a block at
# ~8.5k transfers (~107 B each) = 37% of the 480M tier REGARDLESS of pool depth
# or CPU: without this the rig measures the wire cap, not the chain. A full 480M
# block (22,857 txs) needs ~2.44 MB of tx payload, so >= 4 MB.
# Scale with the requested gas tier (including the 3.423G Rust reference).
# This is a transfer-workload estimate; do not use it as a general block bound.
gossip_mb=$(( (GASCEIL + 59999999) / 60000000 ))
(( gossip_mb >= 8 )) || gossip_mb=8
export N42_MAX_GOSSIP_MB=${N42_MAX_GOSSIP_MB:-$gossip_mb}
[[ $N42_MAX_GOSSIP_MB =~ ^[1-9][0-9]*$ && ${#N42_MAX_GOSSIP_MB} -le 6 ]] || {
  echo "invalid N42_MAX_GOSSIP_MB" >&2; exit 2;
}

# Contention profiling samples inside the critical path being measured, so it is
# off unless the round's purpose IS the profile.
export QS_PROFILE_CONTENTION=$PROFILING
# QS_NODE_EXTRA appends flags the harness has no option of its own for, so a
# round can turn on something like --parallel-evm without this line growing a
# parameter per experiment. Set it in the environment of the round, not here.
# NOT --mobileverify=false. It was set here and every node refused to start:
#   "mobileverify must be enabled on a mining node when MobileAnchorTime is
#    configured"
# This chainspec sets mobileAnchorTime, which makes MobileRegistryRoot mandatory
# in HotStuff headers, so a mining node without the pipeline would propose
# headers every follower rejects. The guard in node.go fails fast and is right.
#
# So the 796 MB a node (5.6 GB across seven) that PacketCache holds is NOT
# reclaimable by turning the feature off on this chain. The lever is the
# retention itself -- MobileVerifyCfg.PacketWindow, default 256 blocks -- which
# has no CLI flag today. Reducing it needs a flag or a config file, and a round
# to say what the right window is.
export QS_EXTRA_ARGS="--pprof.maxcpu $MAXCPU --block-interval-ms $INTERVAL_MS --miner.gasceil $GASCEIL --rpc.maxgasprice $RPC_MAX_GAS_PRICE --txpool.globalslots $POOL_SLOTS --txpool.globalqueue $POOL_QUEUE ${QS_NODE_EXTRA:-}"
if (( TXPOOL_RPC )); then QS_EXTRA_ARGS+=" --http.api eth,web3,net,txpool"; fi
if (( ! TX_GOSSIP )); then QS_EXTRA_ARGS+=" --p2p.tx-gossip=false"; fi

echo "bench fleet: maxcpu=$MAXCPU/node ($(nproc) threads) gasceil=$GASCEIL interval=${INTERVAL_MS}ms pool=$POOL_SLOTS/$POOL_QUEUE profiling=$PROFILING"

for i in {0..6}; do
  d="$QS_NODE_ROOT$i"
  if [[ ! -d $d/chaindata ]]; then
    # Copy the contents, even if init/preflight has already created the directory.
    echo "seeding node $i from $DATA ..."
    mkdir -p "$d"
    cp -a "$DATA/." "$d/"
  fi
  qs_place_keys "$i"
done
for i in {0..6}; do
  qs_launch_node "$i" "$BIN" "$TXGEN_MAX"
done
