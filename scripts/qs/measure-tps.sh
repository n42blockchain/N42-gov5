#!/usr/bin/env bash
# TPS measurement for the qs benchmark fleet.
#
# Samples N windows; per window reports blocks, txs, TPS, mean block time and
# gas occupancy. Occupancy is what separates "fast empty blocks" from real
# throughput -- a window under ~95% means the supply pipe, not the chain, is
# the limiter, and the TPS number is not a chain result.
#
#   ./measure-tps.sh [--windows 3] [--window-sec 60] [--port 20012]
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./qs-env.sh

WINDOWS=3; WINDOW_SEC=60; PORT=$QS_HTTP_BASE
while (( $# )); do
  case $1 in
    --windows)    WINDOWS=$2; shift 2 ;;
    --window-sec) WINDOW_SEC=$2; shift 2 ;;
    --port)       PORT=$2; shift 2 ;;
    -h|--help)    sed -n '2,10p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

rpc() {
  curl -s -m "${RPC_MAX:-10}" -X POST -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}" \
    "http://127.0.0.1:$PORT"
}
head_num() { rpc eth_blockNumber '[]' | grep -o '"result":"0x[0-9a-f]*"' | grep -o '0x[0-9a-f]*'; }

for (( w = 1; w <= WINDOWS; w++ )); do
  h0=$(head_num); t0=$(date +%s)
  sleep "$WINDOW_SEC"
  h1=$(head_num); t1=$(date +%s)
  n0=$((h0)); n1=$((h1))          # bash parses 0x... directly
  elapsed=$((t1 - t0))

  # One pass over the window's blocks; awk does the arithmetic so gas totals
  # stay exact (they exceed what bash integers handle comfortably).
  # S90: occupancy = header gasUsed / header gasLimit, summed over every block
  # of the window (a block with no parsable header, e.g. a timed-out fetch of a
  # 160k-tx frame block, is counted in unparsed= instead of silently as 0).
  # full(>=95%) = share of blocks whose own gasUsed/gasLimit >= 0.95.
  { for (( n = n0 + 1; n <= n1; n++ )); do
      RPC_MAX=90 rpc eth_getBlockByNumber "[\"$(printf '0x%x' "$n")\",false]"
    done
  } | awk -v elapsed="$elapsed" -v blocks="$((n1 - n0))" -v w="$w" '
    NF == 0 { next }   # the RPC reply ends in a newline; never treat blank records as blocks
    {
      # transactions is an array of hashes; count the commas + 1 when non-empty
      tx = 0
      if (match($0, /"transactions":\[[^]]*\]/)) {
        seg = substr($0, RSTART + 18, RLENGTH - 19)
        if (length(seg) > 0) { tx = gsub(/,/, ",", seg) + 1 }
      }
      txs += tx
      gu = -1; gl = -1
      if (match($0, /"gasUsed":"0x[0-9a-f]*"/))  { gu = hex(substr($0, RSTART + 12, RLENGTH - 13)) }
      if (match($0, /"gasLimit":"0x[0-9a-f]*"/)) { gl = hex(substr($0, RSTART + 13, RLENGTH - 14)) }
      if (gu < 0 || gl <= 0) { unparsed++; next }
      used += gu; lim += gl; parsed++
      if (gu / gl >= 0.95) full++
    }
    function hex(s,   i, c, v, d) {
      v = 0
      for (i = 1; i <= length(s); i++) {
        c = tolower(substr(s, i, 1)); d = index("0123456789abcdef", c) - 1
        if (d >= 0) v = v * 16 + d
      }
      return v
    }
    END {
      tps = elapsed > 0 ? txs / elapsed : 0
      occ = lim  > 0 ? 100 * used / lim : 0
      fullpct = parsed > 0 ? 100 * full / parsed : 0
      etps = elapsed > 0 ? used / 21000 / elapsed : 0   # execTPS: valid for pure 21000-gas transfers
      bt  = blocks > 0 ? elapsed / blocks : 0
      printf "win%d: blocks=%d txs=%d TPS=%.0f occupancy=%.1f%% blockTime=%.3fs full(>=95%%)=%.1f%% (%d of %d blocks) unparsed=%d execTPS=%.0f\n",
             w, blocks, txs, tps, occ, bt, fullpct, full, parsed, unparsed, etps
    }'
done
