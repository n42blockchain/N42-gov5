#!/usr/bin/env bash
# mainnet_qmdb_staggered -- 7-node HotStuff live network launcher (Linux).
#
# Boots 7 validators on the era-layout seed, continues producing blocks past
# the replayed head, with built-in transaction simulation (--dev.txgen) and a
# simulated mobile-voting committee (chainspec committeePool, automatic).
#
#   ./deploy-7node.sh [--data DIR] [--bin PATH] [--txgen-max N]
#
# --data is the ERA LAYOUT dir (n42-ancient-seal output), NOT the raw replay
# base. A node dir is seeded ONLY if it does not exist, so a weekly re-seed
# means moving the old dirs aside first.
#
# ISOLATION: the mesh binds and advertises 127.0.0.1 with discovery off. Do not
# "fix" that to a LAN address. Another machine running this same chain with the
# same BLS keys would be seen as this fleet equivocating -- and this chain ships
# its own equivocation detector and slashing.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./qs-env.sh

DATA=$QS_SEED
BIN=$QS_BIN
TXGEN_MAX=31
while (( $# )); do
  case $1 in
    --data)       DATA=$2; shift 2 ;;
    --bin)        BIN=$2; shift 2 ;;
    --txgen-max)  TXGEN_MAX=$2; shift 2 ;;
    -h|--help)    sed -n '2,20p' "$0"; exit 0 ;;
    *)            echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

[[ -d $DATA/chaindata ]] || { echo "seed not found at $DATA (run the replay + seal steps first)" >&2; exit 1; }
[[ -x $BIN ]] || { echo "binary not found or not executable: $BIN" >&2; exit 1; }
qs_load_validators

# The dev faucet key drives --dev.txgen. It is kept OUT of this file so the
# script itself carries no key material and can live in version control.
# Without it, the fleet still runs -- it just produces empty blocks.
if [[ ${TXGEN_MAX:-0} -gt 0 && -z ${N42_DEV_FAUCET_KEY:-} ]]; then
  echo "note: N42_DEV_FAUCET_KEY is unset, so --dev.txgen is disabled and blocks will be empty." >&2
  echo "      export it (the dev faucet key) to enable simulated load." >&2
  TXGEN_MAX=0
fi

for i in {0..6}; do
  d="$QS_NODE_ROOT$i"
  if [[ ! -d $d ]]; then
    echo "seeding node $i from $DATA ..."
    cp -a "$DATA" "$d"
  fi
  qs_place_keys "$i"
done

for i in {0..6}; do
  qs_launch_node "$i" "$BIN" "$TXGEN_MAX"
done

cat <<EOF

7 nodes launched. Verify with:
  for p in \$(seq $QS_HTTP_BASE $((QS_HTTP_BASE + 6))); do
    curl -s -X POST -H 'Content-Type: application/json' \
      -d '{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}' http://127.0.0.1:\$p
  done
A single unique block hash per height is the acceptance bar.
Transaction simulation: --dev.txgen.max=$TXGEN_MAX per block on node 0.
EOF
