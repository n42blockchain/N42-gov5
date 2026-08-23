#!/usr/bin/env bash
# Gracefully stop the 7-node fleet and report the converged consensus state.
#
# This is Step 1 of the weekly cycle. SIGINT only -- a hard kill truncates the
# MDBX spill and poisons the QMDB undo layer.
#
#   ./stop-fleet.sh [--timeout SECONDS] [--no-inspect]
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./qs-env.sh

TIMEOUT=300
INSPECT=1
while (( $# )); do
  case $1 in
    --timeout)    TIMEOUT=$2; shift 2 ;;
    --no-inspect) INSPECT=0; shift ;;
    -h|--help)    sed -n '2,8p' "$0"; exit 0 ;;
    *)            echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

# Signal all seven first, then wait: stopping them one at a time leaves the
# survivors timing out views against a shrinking quorum for no reason.
for i in {0..6}; do
  if pid=$(qs_node_pid "$i"); then
    echo "node $i: SIGINT -> $pid"
    kill -INT "$pid"
  else
    echo "node $i: not running"
  fi
done

failed=0
for i in {0..6}; do
  waited=0
  while pid=$(qs_node_pid "$i"); do
    if (( waited >= TIMEOUT )); then
      echo "node $i pid $pid did not exit in ${TIMEOUT}s -- NOT killing; investigate" >&2
      failed=1
      break
    fi
    sleep 2; waited=$((waited + 2))
  done
  (( failed )) || echo "node $i: stopped clean"
done
(( failed == 0 )) || exit 1

if (( INSPECT )) && [[ -x $QS_TOOLS/hotstuff-inspect ]]; then
  echo
  echo "=== persisted consensus state (all 7 must agree on committedQC) ==="
  for i in {0..6}; do
    "$QS_TOOLS/hotstuff-inspect" -datadir "$QS_NODE_ROOT$i/chaindata"
  done
  cat <<'EOF'

If lockedQC is AHEAD of committedQC, the fleet stopped mid-round. That is
recoverable and expected on a hard stop: the leader re-applies the locked
parent on the next start (Service.ensureParentApplied). A fleet that still
will not produce is unwedged with, per node:

  hotstuff-reset -datadir <node>/chaindata -apply -force -backup <file>
EOF
fi
