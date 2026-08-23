#!/usr/bin/env bash
# Single source of truth for the qs fleet's environment and layout.
#
# Every other script in this directory sources this file. That is deliberate:
# the Windows original declared its environment inside the deploy script only,
# and a rolling restart driven by a *different* script silently dropped
# N42_TXINDEX_TAIL -- which put every tx-lookup row back inside the consensus
# commit AND made eth_getTransactionByHash return null across the whole range
# that had been indexed into the tail. A lever declared outside the thing that
# launches the process is a lever that gets dropped.
#
# Override any value by exporting it before sourcing, or by editing a local
# copy. No secrets live here; see QS_VALIDATORS / N42_DEV_FAUCET_KEY below.

set -euo pipefail

# ---------------------------------------------------------------- layout ----
: "${QS_ROOT:=/data/blockchain}"
: "${QS_NODE_ROOT:=$QS_ROOT/qs-node}"      # per-node datadir prefix: qs-node0..6
: "${QS_SEED:=$QS_ROOT/qs-era-out}"        # era-layout seed (hot MDBX + ancient-era)
: "${QS_BASE:=$QS_ROOT/qs-replay-v5}"      # canonical replay-v2 base
: "${QS_SOURCE:=$QS_ROOT/qs-source}"       # live-chain source for the weekly fold
: "${QS_BIN:=$QS_ROOT/bin/n42}"            # the node binary; same one drives replay-v2
: "${QS_TOOLS:=$QS_ROOT/bin}"              # n42-ancient-seal, txindex-watermark, txflood...
: "${QS_CHAIN:=mainnet_qmdb_staggered}"

# BLS private keys, one validator per line: "<idx>,<addr>,<secp>,<bls>".
# NEVER commit this file and never place it inside a node datadir -- the seed
# is copied verbatim to all seven nodes.
: "${QS_VALIDATORS:=$HOME/qs-validators.md}"

# ------------------------------------------------------------- node levers --
# TxLookup tail tier. With it off, EVERY transaction writes a random-key row
# into MDBX; at high tx/block that scatters thousands of dirty pages and makes
# mdbx_txn_commit dominate the block cycle -- measured 1.9 s/block at 22.8k txs
# against 0.5 ms with the tier on (docs/QS_TPS_BENCHMARK.md). The index lives
# in the node datadir and travels with the seed.
export N42_TXINDEX_TAIL="${N42_TXINDEX_TAIL:-1}"

# Bound each MDBX map so seven instances fit. This is address space, not
# resident memory: a MAP_SHARED file mapping does not count against overcommit.
export N42_MDBX_MAPSIZE_GB="${N42_MDBX_MAPSIZE_GB:-128}"

# ------------------------------------------------------------------ ports ---
# TCP 32000+i, UDP 33000+i, HTTP 20012+i, mobileverify 21012+i, pprof 6090+i.
#
# LINUX HAZARD, absent on Windows: UDP 33000-33006 falls INSIDE the default
# ephemeral range (net.ipv4.ip_local_port_range = 32768 60999), so any outbound
# connection from any process on the host can claim a node's listen port and
# that node fails to bind. The Windows fleet lost a node on two consecutive
# starts to exactly this, with 62000/63000 inside Windows' own ephemeral range.
# Reserve them once per boot (needs root):
#
#   sysctl -w net.ipv4.ip_local_reserved_ports=33000-33006
#
# TCP 32000-32006 sits below the range and needs nothing.
: "${QS_TCP_BASE:=32000}"
: "${QS_UDP_BASE:=33000}"
: "${QS_HTTP_BASE:=20012}"
: "${QS_MOBILE_BASE:=21012}"
: "${QS_PPROF_BASE:=6090}"

# Fixed network keys and their peer IDs (from cmd/peerid). Not secrets: they
# are libp2p host identities for a loopback-only mesh, fixed so every node can
# be given a static peer list without discovery.
QS_NETKEYS=(
  "$(printf '11%.0s' {1..32})" "$(printf '22%.0s' {1..32})"
  "$(printf '33%.0s' {1..32})" "$(printf '44%.0s' {1..32})"
  "$(printf '55%.0s' {1..32})" "$(printf '66%.0s' {1..32})"
  "$(printf '77%.0s' {1..32})"
)
QS_PEERIDS=(
  "16Uiu2HAmHzBkRq62mG95vsjKMuYQBezZCtjPXYWUoyVxMxi71aB3"
  "16Uiu2HAkzAbMrvCbnbeGML8nXZ1XCbVjyphcMGMGQL4vwpUHbxVc"
  "16Uiu2HAkyVds6zuDgExN2UUKQmgte5eNLm3RVz6gexFWAdHiBawr"
  "16Uiu2HAmFcvT2V4i4wtEn1Szrd2K8j14xNsNF7UCoajk9utjdkZe"
  "16Uiu2HAm5qnMKTecNm8SGzYzRhtQAYsLzYp9GN61KpWDNPE6n1Sz"
  "16Uiu2HAmJm4bd8d8Bfs7EbpTiYWdG5YxeUhk298XqCCPpnP7qsDH"
  "16Uiu2HAmLpq62D7sSoUBUE8GxykmR6kuyZxMWymZSLfsCUxSKPN1"
)

# ------------------------------------------------------------- validators ---
# qs_load_validators populates QS_ADDRS[] and QS_BLS[] from QS_VALIDATORS.
# Key material stays in shell arrays and is written only into each node's own
# keystore -- never echoed, never logged.
qs_load_validators() {
  [[ -r $QS_VALIDATORS ]] || { echo "validators file not readable: $QS_VALIDATORS" >&2; return 1; }
  QS_ADDRS=(); QS_BLS=()
  local line
  while IFS= read -r line; do
    QS_ADDRS+=("$(cut -d, -f2 <<<"$line")")
    QS_BLS+=("$(cut -d, -f4 <<<"$line")")
  done < <(grep -E '^[0-6],0x' "$QS_VALIDATORS" | head -7)
  (( ${#QS_ADDRS[@]} == 7 )) || {
    echo "expected 7 validators in $QS_VALIDATORS, got ${#QS_ADDRS[@]}" >&2; return 1; }
}

# ------------------------------------------------------------ process ops ---
# qs_node_pid echoes the recorded pid of node $1 only if that pid is alive AND
# is an n42 process. Never trust the pidfile alone: a stale pid can belong to
# something else entirely after a reboot, and a fleet script that "stopped"
# such a pid would go on to kill an unrelated process.
qs_node_pid() {
  local d="$QS_NODE_ROOT$1" pid comm
  [[ -r $d/n42.pid ]] || return 1
  pid=$(awk '{print $1; exit}' "$d/n42.pid" 2>/dev/null) || return 1
  [[ -n ${pid:-} ]] || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  comm=$(cat "/proc/$pid/comm" 2>/dev/null) || return 1
  [[ $comm == n42* ]] || return 1
  echo "$pid"
}

# qs_stop_node sends SIGINT (cmd/n42/app.go handles SIGINT and SIGTERM through
# the same graceful path) and waits. NEVER SIGKILL: a hard kill truncates the
# MDBX spill and poisons the QMDB undo layer.
qs_stop_node() {
  local i=$1 timeout=${2:-300} pid waited=0
  pid=$(qs_node_pid "$i") || { echo "node $i: not running"; return 0; }
  echo "node $i: SIGINT -> $pid"
  kill -INT "$pid"
  while kill -0 "$pid" 2>/dev/null; do
    (( waited >= timeout )) && { echo "node $i pid $pid did not exit in ${timeout}s -- NOT killing; investigate" >&2; return 1; }
    sleep 2; waited=$((waited + 2))
  done
  echo "node $i: stopped clean"
}

# qs_rotate_logs preserves crash evidence across restarts.
qs_rotate_logs() {
  local d=$1 stamp f
  stamp=$(date +%Y%m%d-%H%M%S)
  for f in run.log run.err; do
    [[ -s $d/$f ]] && mv "$d/$f" "$d/$f.$stamp"
  done
  return 0
}

# ------------------------------------------------------------ launch args ---
# qs_build_args and qs_launch_node live here, not in the deploy script, so that
# the deploy path and the rolling-restart path cannot drift apart. The Windows
# originals kept two copies of the argument set ("mirrors deploy BuildArgs
# exactly", said the comment) and a roll that silently dropped one lever is
# exactly the failure that motivated this file.
#
# qs_build_args <index> [txgen_max] -> fills QS_ARGS[]
qs_build_args() {
  local i=$1 txgen=${2:-0} j d="$QS_NODE_ROOT$1"
  QS_ARGS=(--chain "$QS_CHAIN" --profile n42
        --data.dir "$d"
        --engine.miner --engine.etherbase "${QS_ADDRS[$i]}"
        --p2p.no-discovery
        # Same-host mesh: advertise loopback only. Otherwise the node advertises
        # its PUBLIC ip, peers dial it at both 127.0.0.1 and that address, and
        # the duplicate connections churn libp2p.
        --p2p.local-ip 127.0.0.1 --p2p.host-ip 127.0.0.1
        --p2p.tcp-port "$((QS_TCP_BASE + i))" --p2p.udp-port "$((QS_UDP_BASE + i))"
        --p2p.min-sync-peers 0
        --http --http.addr 127.0.0.1 --http.port "$((QS_HTTP_BASE + i))"
        --http.api eth,web3,net
        # The mobileverify cohort is auto-enabled by --profile n42; only the
        # phone-facing HTTP surface needs an explicit address.
        --mobileverify.http "127.0.0.1:$((QS_MOBILE_BASE + i))"
        --pprof --pprof.port "$((QS_PPROF_BASE + i))")
  [[ -n ${QS_EXTRA_ARGS:-} ]] && { local extra; read -ra extra <<<"$QS_EXTRA_ARGS"; QS_ARGS+=("${extra[@]}"); }
  for j in {0..6}; do
    (( j == i )) && continue
    QS_ARGS+=(--p2p.peer "/ip4/127.0.0.1/tcp/$((QS_TCP_BASE + j))/p2p/${QS_PEERIDS[$j]}")
  done
  # Simulated transactions on node 0 ONLY: one faucet key, and parallel senders
  # would race its nonce.
  if (( txgen > 0 && i == 0 )) && [[ -n ${N42_DEV_FAUCET_KEY:-} ]]; then
    QS_ARGS+=(--dev.txgen --dev.txgen.max "$txgen" --dev.txgen.key "$N42_DEV_FAUCET_KEY")
  fi
}

# qs_place_keys <index> -- writes this node's BLS key and libp2p identity.
# The seed is copied verbatim to all seven nodes, so keys are placed AFTER the
# copy, never inside the seed itself.
qs_place_keys() {
  local i=$1 d="$QS_NODE_ROOT$1"
  mkdir -p "$d/keystore"
  ( umask 077; printf '%s' "${QS_BLS[$i]}" > "$d/keystore/bls_${QS_ADDRS[$i]}.key" )
  printf '%s' "${QS_NETKEYS[$i]}" > "$d/network-keys"
}

# qs_launch_node <index> <binary> [txgen_max]
# setsid detaches the node from this shell's session: when the launching shell
# is reclaimed it must not take the fleet down with it.
qs_launch_node() {
  local i=$1 bin=$2 txgen=${3:-0} d="$QS_NODE_ROOT$1"
  qs_rotate_logs "$d"
  qs_build_args "$i" "$txgen"
  setsid "$bin" "${QS_ARGS[@]}" >"$d/run.log" 2>"$d/run.err" </dev/null &
  echo "node $i started: pid $! etherbase ${QS_ADDRS[$i]} http :$((QS_HTTP_BASE + i))"
}
