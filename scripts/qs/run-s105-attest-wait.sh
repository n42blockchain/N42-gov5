#!/bin/bash
set -u
export TZ=America/New_York
L=/data/blockchain/wr-logs/r35zzzce.log
SP=/data/blockchain/gov5-work
N42_BIN=n42-r142
S97_DEPTH=2   # S97: depth 2 in every leg (no A/B): legs restart the nodes on the same chain without reseed, so a depth change would break the header rule (header N carries the root of N-k) for stored blocks
BIN=$SP/$N42_BIN
say(){ echo "$(date +%H:%M:%S) $*" | tee -a $L; }
cd /data/blockchain/scripts-qs

others_busy(){
  local n=0 p now f age
  for p in $(pgrep -f 'rust-fleet7(-bench)?/node[0-9]*/(e[l]|consensu[s])|gov5-perf7-[0-9a-z-]*/nod[e]|n42-datc-[0-9a-z]*\.bi[n]' 2>/dev/null); do
    case "$(cat /proc/$p/comm 2>/dev/null)" in bash|sh|dash|zsh|pgrep|grep|python3|"") continue ;; esac
    n=$((n+1))
  done
  [ "$n" -gt 0 ] && { echo "fleet processes: $n"; return 0; }
  local l1; l1=$(cut -d' ' -f1 /proc/loadavg | cut -d. -f1)
  [ "$l1" -ge 8 ] && { echo "load1 $l1"; return 0; }
  local ma; ma=$(awk '/MemAvailable/{print int($2/1048576)}' /proc/meminfo)
  [ "$ma" -lt 80 ] && { echo "MemAvailable ${ma}G"; return 0; }
  now=$(date -u +%s)
  for f in /data/blockchain/.box-claim-* /data/blockchain/wr-logs/.box-claim-*; do
    [ -f "$f" ] || continue
    case "$f" in *-gov5) continue ;; esac
    age=$(( now - $(cat "$f" 2>/dev/null || echo 0) ))
    [ "$age" -lt 5400 ] && { echo "claim $f age ${age}s"; return 0; }
  done
  return 1
}
older_claim_exists(){ # any foreign claim OLDER than ours
  local mine theirs f
  mine=$(cat /data/blockchain/.box-claim-gov5 2>/dev/null || echo 0)
  for f in /data/blockchain/.box-claim-* /data/blockchain/wr-logs/.box-claim-*; do
    [ -f "$f" ] || continue
    case "$f" in *-gov5) continue ;; esac
    theirs=$(cat "$f" 2>/dev/null || echo 0)
    case "$theirs" in ''|*[!0-9]*) continue ;; esac
    [ "$theirs" -lt "$mine" ] && [ $(( $(date -u +%s) - theirs )) -lt 5400 ] && return 0
  done
  return 1
}
write_claim(){ date -u +%s > /data/blockchain/.box-claim-gov5; date -u +%s > /data/blockchain/wr-logs/.box-claim-gov5; }
drop_claim(){  rm -f /data/blockchain/.box-claim-gov5 /data/blockchain/wr-logs/.box-claim-gov5; }

say "waiting for a quiet box (3 checks, 30 s apart)"
while :; do
  quiet=0
  for i in 1 2 3; do
    if why=$(others_busy); then say "busy: $why"; quiet=0; break; fi
    quiet=$((quiet+1)); [ $i -lt 3 ] && sleep 30
  done
  [ $quiet -eq 3 ] || { sleep 60; continue; }
  write_claim
  sleep $((20 + RANDOM % 31))
  if older_claim_exists; then say "yielding: an older claim exists"; drop_claim; sleep 90; continue; fi
  break
done
say "claimed the box"

for p in $(pgrep -x txflood-r34 2>/dev/null) $(pgrep -x txflood-r35 2>/dev/null) $(pgrep -x txflood-r36 2>/dev/null) \
         $(pgrep -x txflood-r37 2>/dev/null) $(pgrep -x txflood-r38 2>/dev/null) $(pgrep -x txflood-r39 2>/dev/null) $(pgrep -x txflood-r45 2>/dev/null) $(pgrep -x txflood-r46 2>/dev/null) $(pgrep -x txflood-r47 2>/dev/null) $(pgrep -x txflood-r48 2>/dev/null) $(pgrep -x txflood-r49 2>/dev/null); do
  case "$(readlink /proc/$p/exe 2>/dev/null)" in
    "$SP"/*) say "killing orphaned generator $p"; kill -TERM "$p" 2>/dev/null ;;
  esac
done
for i in 0 1 2 3 4 5 6; do
  N42_MDBX_MAPSIZE_GB=320 /data/blockchain/bin/hotstuff-reset -datadir /data/blockchain/qs-node$i/chaindata -apply -force \
    -backup /data/blockchain/wr-logs/hsreset-r35zzzce-node$i.json >>$L 2>&1 || say "hotstuff-reset failed on node $i"
done
say "preflight done: orphan generators cleared, hotstuff journals reset"
( while :; do sleep 600; [ -f /data/blockchain/.box-claim-gov5 ] || exit 0; write_claim; done ) & RFPID=$!
trap 'kill $RFPID 2>/dev/null; drop_claim' EXIT

MARK='QMDB-only account persistence'
mark_logs(){
  date +%Y-%m-%d\ %H:%M:%S > /data/blockchain/wr-logs/r35zzzce.legstart
  : > /data/blockchain/wr-logs/r35zzzce.logmark
  local i f
  for i in 0 1 2 3 4 5 6; do
    f=/data/blockchain/qs-node$i/log/n42.log
    echo "$i $(wc -c < "$f" 2>/dev/null || echo 0)" >> /data/blockchain/wr-logs/r35zzzce.logmark
  done
}
check_mode(){ # $1 = expect hits (0 for A, 7 for B)  $2 = leg
  local hits=0 i f since
  since=$(cat /data/blockchain/wr-logs/r35zzzce.legstart)
  for i in 0 1 2 3 4 5 6; do
    local found=0
    for f in /data/blockchain/qs-node$i/log/n42.log $(find /data/blockchain/qs-node$i/log -name 'n42-*.log*' -newer /data/blockchain/wr-logs/r35zzzce.legstart 2>/dev/null); do
      [ -f "$f" ] || continue
      case "$f" in *.gz) zcat "$f" 2>/dev/null;; *) cat "$f";; esac | grep -F "$MARK" | grep -o '"time":"[^"]*"' | awk -v s="$since" -F'"' '$4 >= s {found=1} END{exit !found}' && { found=1; break; }
    done
    hits=$((hits+found))
  done
  if [ "$hits" -eq "$1" ]; then say "MODE CHECK $2: freeze warning on $hits/7 nodes (expected $1)"
  else say "MODE INTERLOCK FAILED in $2: expected $1/7 nodes with the freeze warning, saw $hits/7"
       touch /data/blockchain/wr-logs/r35zzzce-MODE-FAILED; fi
}

run_leg(){ # $1 name  $2 gasceil (header)  $3 offset  $4 fill gas (builder budget, half of $2)  $5 N42_LEADER_WRITE_AFTER_JOURNAL (1 in every leg now, adopted)  $6 GOMEMLIMIT (fixed 10GiB every leg this round)  $7 N42_SENDER_CACHE_SLOTS (fixed 4194304 every leg)  $8 POOL slots/queue, "N/M" split by ${8%%/*}/${8##*/} (fixed 600000/200000 every leg this round)  $9 N42_BLOCK_CACHE_BLOCKS (fixed 2 every leg -- S36b adopted)  ${10} N42_HOTSTUFF_LEADER_TENURE (fixed 4 every leg this round)  ${11} N42_DEFERRED_CHECK_CONCURRENT (fixed 1 every leg -- S42 adopted, 6e4)  ${12} generator pacing triple "rate/depth/pertx": warm-up/A1/B1 "16000/0/9000" (35zzzx's own B2, the best known supply on record, NOT 35zzzz's -- rate only, depth throttle off) vs B2/A2 "0/0/9000" (flat-out, S49 PART 2(iii))  ${13} N42_FLOOD_DEPTH_EXACT 0/1 (fixed 0 every leg this round -- target-depth is 0 in every leg now, so this flag is inert either way)  ${14} N42_TX_INGEST_HIGH_WATER (S49 rerun, 6f2, prediction 109: THIS round's one mechanism, A/B by leg -- unset/0 warm-up/A1/B1 = today's generator-side throttle; 500000 B2/A2 = n42-rs's own local pool-gate, 5/6 of the 600000-slot pool, PAIRED with QS_FLOOD_BARRIER fixed on every leg so the gate cannot starve a later generator's own funding)  ${19} --floods generator process count (fixed 8 every leg this round -- undoes S61's 16-generator B2, S61 FALSIFIED: 16x500 delivered less than 8x1000)  ${20} N42_HOTSTUFF_CATCHUP_GRACE_MS (S63, 6fc: 0/unset warm-up/A1/B1 = today's immediate lag-1 catch-up; 2000 B2/A2 = withhold a lag-exactly-1 request for 2s so the normal proposal/commit path can deliver the block first; a lag >= 2 and the equal-height fork-recovery path are unaffected except for the fork-recovery path's own delay when its lag is exactly 1)  ${21} N42_BLOCK_CACHE_PRIME (S64, 6fc: 0/unset warm-up/A1/B1 = today's behaviour, no fill at commit; 1 B2/A2 = prime bc.blockCache with the block object already decoded at writeBlockWithState/CommitToCanonicalWith, so GetBlock(hash) readers -- sync body-range serving, api getBlockByNumber, miner ancestor walk -- hit the cache instead of re-decoding from MDBX)  ${22} N42_NO_HISTORY_INDEX (0 every leg)  ${23} N42_QMDB_PARALLEL_APPLY (0 every leg, S72 closed)  ${24} N42_QMDB_EVICT_LAG_BLOCKS (S74: 0 warm-up/A1/B1, 64 B2/A2)  ${25} --interval-ms, bench-run.sh's own flag (-> bench-7node.sh --block-interval-ms -> conf.Miner.BlockIntervalMs -> worker.go paceBlock's absolute-grid sleep, docs/QS_180MS.md): S77, A/B by leg -- 250 warm-up/A1/B1 (today's standing value) vs 125 B2/A2 (today/2); verified by bench-run.sh's own existing launch check (scripts-qs/bench-run.sh:138, greps each node's /proc/<pid>/cmdline for "--block-interval-ms $INTERVAL_MS" and aborts the leg if absent)
  local leg=$1
  local pace_rate pace_depth pace_pertx
  IFS='/' read -r pace_rate pace_depth pace_pertx <<<"${12}"
  local pace_exact=${13:-0}
  say "LEG $1: gasceil=$2 fillgas=$4 tenure=${10} (fixed 4 every leg, not an A/B this round) N42_LEADER_WRITE_AFTER_JOURNAL=$5 GOMEMLIMIT=$6 N42_SENDER_CACHE_SLOTS=$7 POOL=$8 (fixed 600000/200000 every leg) N42_BLOCK_CACHE_BLOCKS=$9 (fixed 4 every leg, S96) N42_DEFERRED_CHECK_CONCURRENT=${11} (fixed 1, S42 adopted) pace=${12}=rate:$pace_rate/depth:$pace_depth/pertx:$pace_pertx high-water=${14:-0} barrier=/data/blockchain/wr-logs/r35zzzce-barrier-$1 (S49 rerun, prediction 109: the one mechanism -- unset/0 today's generator throttle, 500000 n42-rs's own local pool-gate + flat-out generators, funding barrier fixed on every leg) (READ_QMDB=1 + WRITE_QMDB_ONLY=1 all legs; write probe on; 8 floods x 1000 x 9000, pool 600000/200000, block cache 2, PUSH+PROPOSE_BEFORE_WRITE, PARALLEL_EVM x32, gossip 24 MB, recipients 22857 (HOT set)) floods=${19:-8} catchup-grace-ms=${20:-0} (S63, adopted) block-cache-prime=${21:-0} (S64) no-history-index=${22:-0} (S70 rerun) qmdb-parallel-apply=${23:-0} (S72, closed) qmdb-evict-lag-blocks=${24:-0} (S74) interval-ms=${25:-250} (S78: 125 fixed in every leg, S77's A/B retired) workers=${26:-32} (S78, A/B: 32 today vs 64 today x2) offset=$3 deferred-depth=$S97_DEPTH"
  mark_logs
  local PRESIGN_DIR=/data/blockchain/wr-logs/r35zzzce-floods/$1
  local freegb; freegb=$(df -BG --output=avail /data/blockchain | tail -1 | tr -dc '0-9')
  if [ "${freegb:-0}" -lt 40 ]; then
    say "ABORT $1: only ${freegb}G free on /data/blockchain (need 40G for the presign corpora)"
    say "ROUND ABORTED: disk before leg $1"; exit 1
  fi
  [ ! -e "$PRESIGN_DIR" ] || { say "ROUND ABORTED: existing presign directory $PRESIGN_DIR"; exit 1; }; mkdir -p "$PRESIGN_DIR"
  local flood_marker=/data/blockchain/wr-logs/r35zzzce-legstart-$1
  : > "$flood_marker"
  ( source ./env-linux.sh
    export N42_SLOW_BLOCK_MS=1 QS_METRICS=1
    export N42_MDBX_MAPSIZE_GB=320
    export N42_MINER_FILL_GAS=$4
    export N42_TXPOOL_NOLOCALS=1
    export QS_FLOOD_GASPRICE=1000000000
    export QS_BIN=$BIN
    export QS_TXFLOOD=$SP/txflood-r49
    export N42_HISTORY_INDEX_DEFERRED=1
    export N42_STATE_READ_QMDB=1
    export N42_STATE_WRITE_QMDB_ONLY=1
    export QS_EXTRA_ARGS="--mobileverify.packet-window 8"
    unset N42_FINALIZE_TRACE   # S103: P10 says the trace costs ~4 ms per root
    export N42_WRITE_PROBE=1
    export QS_FLOOD_BARRIER=/data/blockchain/wr-logs/r35zzzce-barrier-$1
    export QS_FLOOD_PRESIGN_DIR=$PRESIGN_DIR   # S98: bench-run.sh adds -sign-workers 4 -presign DIR/corpus-F.bin per generator
    QS_FLOOD_EXTRA="-target-depth $pace_depth -depth-by-nonce -lazy-sign -interleave"
    if [ "$pace_exact" = 1 ]; then
      QS_FLOOD_EXTRA="$QS_FLOOD_EXTRA -depth-by-nonce-exact"
    fi
    export QS_FLOOD_EXTRA
    export N42_MAX_GOSSIP_MB=24
    export GOMEMLIMIT=$6
    export N42_BLOCK_DECODE_REUSE_POOL=0
    export GOGC=100
    export N42_FRAME_DB=1
    export N42_LAG_GATE_APPLIED=1
    case "$1" in B1|B2) export N42_FRAME_ATTEST_WAIT_MS=50 ;; *) export N42_FRAME_ATTEST_WAIT_MS=0 ;; esac
    export N42_FRAME_ATTEST=1 N42_FRAME_ATTEST_COMMITTEE=3   # S102: attestation ON in every leg (A/B retired)
    export QS_MAXCPU=36   # S101: 7 x GOMAXPROCS 37 = 259 threads on a 256-thread box
    export N42_POOL_PENDING_SNAPSHOT=1
    export N42_SENDER_CACHE_SLOTS=$7
    export N42_MINER_PARALLEL_FILL=1
    export N42_HOTSTUFF_LEADER_TENURE=${10}
    export N42_BLOCK_CACHE_BLOCKS=$9
    export N42_DEFERRED_CHECK_CONCURRENT=${11}
    if [ -n "${14:-}" ] && [ "${14}" != 0 ]; then
      export N42_TX_INGEST_HIGH_WATER=${14}
    else
      unset N42_TX_INGEST_HIGH_WATER
    fi
    if [ -n "${16:-}" ] && [ "${16}" != 0 ]; then export N42_INGEST_WORKERS=${16}; else unset N42_INGEST_WORKERS; fi   # S53: parallel decode+recover per RPC batch
    if [ -n "${17:-}" ] && [ "${17}" != 0 ]; then export N42_HINT_RECOVERY_POOL=${17}; else unset N42_HINT_RECOVERY_POOL; fi   # S59: sender recovery once, on its own pool
    export N42_QMDB_SINGLE_FOLD=1   # S58 v1: verification counters only, every leg
    if [ -n "${18:-}" ] && [ "${18}" != 0 ]; then export N42_TXINDEX_KEEP_BLOCKS=${18}; else unset N42_TXINDEX_KEEP_BLOCKS; fi   # S60: tail index keep window (unset = 64)
    if [ -n "${20:-}" ] && [ "${20}" != 0 ]; then export N42_HOTSTUFF_CATCHUP_GRACE_MS=${20}; else unset N42_HOTSTUFF_CATCHUP_GRACE_MS; fi
    if [ -n "${21:-}" ] && [ "${21}" != 0 ]; then export N42_BLOCK_CACHE_PRIME=${21}; else unset N42_BLOCK_CACHE_PRIME; fi
    if [ -n "${22:-}" ] && [ "${22}" != 0 ]; then export N42_NO_HISTORY_INDEX=${22}; else unset N42_NO_HISTORY_INDEX; fi
    if [ -n "${23:-}" ] && [ "${23}" != 0 ]; then export N42_QMDB_PARALLEL_APPLY=${23}; else unset N42_QMDB_PARALLEL_APPLY; fi   # S72: QMDB apply pre-pass workers
    if [ -n "${24:-}" ] && [ "${24}" != 0 ]; then export N42_QMDB_EVICT_LAG_BLOCKS=${24}; else unset N42_QMDB_EVICT_LAG_BLOCKS; fi   # S74: QMDB entry-eviction lag (blocks)
    export N42_PUSH_BEFORE_WRITE=1
    export N42_PROPOSE_BEFORE_WRITE=1
    export QS_NODE_EXTRA="--parallel-evm --txpool.accountslots 4096 --txpool.accountqueue 8192 --mobileverify.packet-window 8"
    export N42_PARALLEL_WORKERS=${26:-32}
    export N42_HISTORY_INDEX_INTERVAL=20s
    export N42_TXROOT_BLAKE3_TIME=1788393864
    export N42_DEFERRED_EXECUTION_TIME=1788393864
    export N42_MINER_ADOPT_APPENDS=1
    export N42_BUILD_STALL_DIAG=1
    unset N42_CONTENTION_DIAG
    export N42_BLOCK_GOSSIP_FALLBACK=0
    export N42_WAVE_LPT=1
    GW_ADDR=$(tr -d '[:space:]' < $SP/gateway-35zzzbm.addr); GW_KEY=$(tr -d '[:space:]' < $SP/gateway-35zzzbm.key)
    FRAME_PEERS=""; for _p in 0 1 2 3 4 5 6; do FRAME_PEERS="$FRAME_PEERS""127.0.0.1:$((${QS_INGEST_BASE:-9100} + _p)),"; done; FRAME_PEERS=${FRAME_PEERS%,}
    export N42_FRAME_GATEWAYS=$GW_ADDR,0xe487609834cC18e67aC6C7bDCE5a7661ECBDF266,0x00ab16A16DAbE8E7676d211a93068c92523685F1,0x9869B72870C9B9d0D8d7ac94AfB324D2340E1dF3,0xA6b4C79fbDA0c392436edC058D5bee526d6796F7,0xe303459fcb71dfadc8FC9C2B8b2ee6f26f4Bd494,0x0a3315603A47480CF77Cf20E64D59E84c3170EB4,0x44f9EC27Db1B02a8f4ceA43FAE4c39346DE2d3D1 N42_FRAME_BLOCKS=1 N42_FRAME_PULL_PEERS=$FRAME_PEERS
    export N42_FRAME_STORE_BYTES=1073741824   # 1 GiB per node (S96; 2 GiB filled in S95)
    export N42_FRAME_KEEP_BLOCKS=16   # S89: evict a frame only 64 blocks after a committed block references it
    unset N42_FRAME_PULL_TIMEOUT_MS; export N42_FRAME_BLOCK_MAX_FRAMES=1024   # explicit 1024 (S91: ~620 frames per 310k-tx block)
    export N42_VOTE_ON_AVAILABILITY=1 N42_DEFERRED_DEPTH=$S97_DEPTH N42_FRAME_RECOVER_WORKERS=16   # S91 P2a
    QS_FLOOD_EXTRA="$QS_FLOOD_EXTRA -frames -gateway-key $GW_KEY -frame-peers $FRAME_PEERS -frame-broadcast"
    export QS_FLOOD_EXTRA
    export N42_MINER_SENDER_ROTATE=1
    export N42_LEADER_WRITE_AFTER_JOURNAL=$5
    export QS_INGEST_HINT=1
    export QS_MEASURE_PORT=20018   # S101: measure-tps fetches win blocks via node6 (follower), not the leader node0
    ./bench-run.sh --tag "r35-$1" --offset "$3" --decay-sec "$([ "$1" = warmup ] && echo 1900 || echo 400)" --windows 2 --gasceil "$2" \
      --floods ${19:-8} --senders $(( 8000 / ${19:-8} )) --pertx "$pace_pertx" --conc ${15:-96} --rpcbatch 200 --rate "$pace_rate" \
      --pool-slots ${8%%/*} --pool-queue ${8##*/} --interval-ms ${25:-250} --recipients 22857 ) >>$L 2>&1 &
  local benchpid=$!
  local expect=7
  for envnode in 0 3; do
    envpid=""
    for _t in 1 2 3 4 5 6 7 8 9 10; do
      envpid=$(ps -eo pid,args | awk -v n="qs-node${envnode}" '$0 ~ ("data\\.dir /data/blockchain/" n "([^0-9]|$)"){print $1; exit}')
      [ -n "$envpid" ] && break
      sleep 2
    done
    if [ -n "$envpid" ] && [ -r "/proc/$envpid/environ" ]; then
      envdump=$(tr '\0' '\n' < "/proc/$envpid/environ")
      ev(){ echo "$envdump" | awk -F= -v k="$1" '$1==k{print substr($0,length(k)+2); f=1} END{if(!f) print "unset"}'; }
      case "$1" in B1|B2) expected_wait=50 ;; *) expected_wait=0 ;; esac
      if [ "$(ev N42_FRAME_ATTEST_WAIT_MS)" != "$expected_wait" ] || [ "$(ev N42_FRAME_DB)" != 1 ] || [ "$(ev N42_LAG_GATE_APPLIED)" != 1 ]; then
        say "ROUND ABORTED: node${envnode} A/B environment interlock failed"
        kill -TERM "$benchpid" 2>/dev/null
        wait "$benchpid" 2>/dev/null
        ( source ./env-linux.sh; ./stop-fleet.sh ) >>"$L" 2>&1
        exit 1
      fi
      say "ENV $1 node${envnode}: N42_FRAME_ATTEST_WAIT_MS=$(ev N42_FRAME_ATTEST_WAIT_MS) N42_LAG_GATE_APPLIED=$(ev N42_LAG_GATE_APPLIED) N42_FRAME_DB=$(ev N42_FRAME_DB) N42_NO_HISTORY_INDEX=$(ev N42_NO_HISTORY_INDEX) N42_QMDB_PARALLEL_APPLY=$(ev N42_QMDB_PARALLEL_APPLY) N42_QMDB_EVICT_LAG_BLOCKS=$(ev N42_QMDB_EVICT_LAG_BLOCKS) N42_HOTSTUFF_CATCHUP_GRACE_MS=$(ev N42_HOTSTUFF_CATCHUP_GRACE_MS) N42_BLOCK_CACHE_PRIME=$(ev N42_BLOCK_CACHE_PRIME) N42_TXINDEX_KEEP_BLOCKS=$(ev N42_TXINDEX_KEEP_BLOCKS) N42_SENDER_CACHE_SLOTS=$(ev N42_SENDER_CACHE_SLOTS) N42_PARALLEL_WORKERS=$(ev N42_PARALLEL_WORKERS) N42_CONTENTION_DIAG=$(ev N42_CONTENTION_DIAG) N42_WAVE_LPT=$(ev N42_WAVE_LPT) N42_MINER_SENDER_ROTATE=$(ev N42_MINER_SENDER_ROTATE) N42_FRAME_GATEWAYS=$(ev N42_FRAME_GATEWAYS) N42_FRAME_BLOCKS=$(ev N42_FRAME_BLOCKS) N42_FRAME_STORE_BYTES=$(ev N42_FRAME_STORE_BYTES) N42_FRAME_PULL_PEERS=$(ev N42_FRAME_PULL_PEERS) N42_MINER_PARALLEL_FILL=$(ev N42_MINER_PARALLEL_FILL) N42_BUILD_STALL_DIAG=$(ev N42_BUILD_STALL_DIAG) GOMEMLIMIT=$(ev GOMEMLIMIT)"
    else
      say "ENV $1 node${envnode}: pid not found within 20s, could not verify"
    fi
  done
  say "FLOOD FLAGS $1: QS_FLOOD_EXTRA=${QS_FLOOD_EXTRA:-unset} presign=-sign-workers ${QS_FLOOD_SIGN_WORKERS:-4} -presign ${QS_FLOOD_PRESIGN_DIR:-unset}/corpus-<f>.bin"
  ( while kill -0 $benchpid 2>/dev/null; do
      sleep 10
      ma=$(awk '/MemAvailable/{print int($2/1048576)}' /proc/meminfo)
      nodes=""
      for np in $(ps -eo pid,args | awk '/[/]n42-r35 /{print $1}'); do
        nodes="$nodes $(awk '/^RssAnon/{a=$2} /^RssFile/{f=$2} END{printf "%d+%d", a/1024, f/1024}' /proc/$np/status 2>/dev/null)"
      done
      echo "$(date +%H:%M:%S) avail=${ma}G nodesAnon+FileMB=$nodes floodsMB=$(ps -eo rss,args | awk '/[t]xflood.*-rpc/{printf "%d ", $1/1024}') $(awk '/^(Shmem|Dirty|Cached|AnonPages):/{printf "%s%dM ", $1, $2/1024}' /proc/meminfo)" >> /data/blockchain/wr-logs/r35zzzce-mem.log
      if [ "$ma" -lt 20 ]; then
        say "ABORT $1: MemAvailable ${ma}G; stopping the round"
        touch /data/blockchain/wr-logs/r35zzzce-MODE-FAILED
        kill -TERM $benchpid 2>/dev/null; exit 0
      fi
    done ) &
  ( declare -A prev_pgmajfault prev_pgscan_kswapd prev_pgscan_direct prev_pgsteal_kswapd prev_refault
    first=1
    while kill -0 $benchpid 2>/dev/null; do
      sleep 10
      ts=$(date +%H:%M:%S)
      nodes=""
      for np in $(ps -eo pid,args | awk '/[/]n42-r35 /{print $1}'); do
        read -r minflt majflt utime stime < <(awk '{print $10, $12, $14, $15}' /proc/$np/stat 2>/dev/null)
        rss=$(awk '/^RssAnon/{a=$2} /^RssFile/{f=$2} /^RssShmem/{s=$2} END{printf "%d+%d+%d", a/1024, f/1024, s/1024}' /proc/$np/status 2>/dev/null)
        cpuSec=$(( (${utime:-0} + ${stime:-0}) / 100 ))
        nodes="$nodes pid$np:minflt=${minflt:-0}:majflt=${majflt:-0}:cpuSec=$cpuSec:rssAnon+File+ShmemMB=${rss:-0+0+0}"
      done
      gens=""
      for gp in $(ps -eo pid,args | awk '/[t]xflood.*-rpc/{print $1}'); do
        read -r utime stime < <(awk '{print $14, $15}' /proc/$gp/stat 2>/dev/null)
        gcpuSec=$(( (${utime:-0} + ${stime:-0}) / 100 ))
        granon=$(awk '/^RssAnon/{print int($2/1024)}' /proc/$gp/status 2>/dev/null)
        gens="$gens pid$gp:cpuSec=$gcpuSec:rssAnonMB=${granon:-0}"
      done
      declare -A cur
      while read -r key val; do cur[$key]=$val; done < <(awk '/^(pgmajfault|pgscan_kswapd|pgscan_direct|pgsteal_kswapd|workingset_refault_file) /{print $1, $2}' /proc/vmstat 2>/dev/null)
      if [ "$first" = 1 ]; then
        d_pgmajfault=0; d_pgscan_kswapd=0; d_pgscan_direct=0; d_pgsteal_kswapd=0; d_refault=0
        first=0
      else
        d_pgmajfault=$(( ${cur[pgmajfault]:-0} - ${prev_pgmajfault[v]:-0} ))
        d_pgscan_kswapd=$(( ${cur[pgscan_kswapd]:-0} - ${prev_pgscan_kswapd[v]:-0} ))
        d_pgscan_direct=$(( ${cur[pgscan_direct]:-0} - ${prev_pgscan_direct[v]:-0} ))
        d_pgsteal_kswapd=$(( ${cur[pgsteal_kswapd]:-0} - ${prev_pgsteal_kswapd[v]:-0} ))
        d_refault=$(( ${cur[workingset_refault_file]:-0} - ${prev_refault[v]:-0} ))
      fi
      prev_pgmajfault[v]=${cur[pgmajfault]:-0}
      prev_pgscan_kswapd[v]=${cur[pgscan_kswapd]:-0}
      prev_pgscan_direct[v]=${cur[pgscan_direct]:-0}
      prev_pgsteal_kswapd[v]=${cur[pgsteal_kswapd]:-0}
      prev_refault[v]=${cur[workingset_refault_file]:-0}
      poolstats=""
      for pn in 0 3; do
        pbody=$(curl -s -m 3 "http://127.0.0.1:$((6070+pn))/debug/metrics/prometheus" 2>/dev/null)
        ppending=$(echo "$pbody" | awk '/^txpool_pending /{print $2}')
        pqueued=$(echo "$pbody" | awk '/^txpool_queued /{print $2}')
        poolstats="$poolstats pool node$pn pending=${ppending:-} queued=${pqueued:-}"
      done
      echo "$ts leg=$1 vmstat10s pgmajfaultD=$d_pgmajfault pgscanKswapdD=$d_pgscan_kswapd pgscanDirectD=$d_pgscan_direct pgstealKswapdD=$d_pgsteal_kswapd refaultFileD=$d_refault$nodes gens:$gens$poolstats" >> /data/blockchain/wr-logs/r35zzzce-vm.log
    done ) &
  ( while kill -0 $benchpid 2>/dev/null; do
      sleep 30
      ts=$(date +%H:%M:%S)
      for i in 0 1; do
        body=$(curl -s -m 10 -w '\nHTTPSTATUS:%{http_code}' "http://127.0.0.1:$((6090+i))/debug/pprof/heap?debug=1" 2>/dev/null)
        status=$(printf '%s' "$body" | grep -o 'HTTPSTATUS:[0-9]*' | grep -o '[0-9]*$')
        if [ "$status" != "200" ]; then
          echo "$ts leg=$1 node$i memstats: HTTP ${status:-no-response} (tolerated)" >> /data/blockchain/wr-logs/r35zzzce-memstats.log
          continue
        fi
        {
          echo "=== $ts leg=$1 node$i ==="
          printf '%s' "$body" | sed '/^HTTPSTATUS:/d' | tail -n 40
        } >> /data/blockchain/wr-logs/r35zzzce-memstats.log
      done
    done ) &
  local mark; mark=$(grep -c 'all 8 flood' $L 2>/dev/null | tail -1)
  local rmark; rmark=$(grep -c 'funding barrier: all .* funded; releasing' $L 2>/dev/null | tail -1)   # S101: capture anchor = barrier release
  ( while [ "$(grep -c 'all 8 flood' $L 2>/dev/null | tail -1)" -le "$mark" ]; do sleep 5; kill -0 $benchpid 2>/dev/null || exit 0; done
    sleep 20
    check_mode $expect "$1" ) &
  ( sleep 480
    for i in 0 1 2 3 4 5 6; do
      if ! curl -s -m 3 -X POST -H 'content-type: application/json' --data '{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}' http://127.0.0.1:$((20012+i)) | grep -q result; then
        curl -s -m 20 "http://127.0.0.1:$((6090+i))/debug/pprof/goroutine?debug=2" -o /data/blockchain/wr-pprof/r35zzzce-$1-node$i-stall-goroutines.txt
        cp -f /data/blockchain/qs-node$i/run.log /data/blockchain/wr-logs/r35zzzce-$1-node$i-stall-run.log 2>/dev/null
        cp -f /data/blockchain/qs-node$i/run.err /data/blockchain/wr-logs/r35zzzce-$1-node$i-stall-run.err 2>/dev/null
        alive=$(pgrep -f "qs-node$i/" | wc -l)
        say "STALL WATCH $1: node $i not serving RPC at +150 s; processes=$alive; goroutine dump $(wc -c < /data/blockchain/wr-pprof/r35zzzce-$1-node$i-stall-goroutines.txt 2>/dev/null || echo 0) bytes; run.log tail: $(tail -1 /data/blockchain/qs-node$i/run.log 2>/dev/null | tr -d '\033' | cut -c1-160)"
      fi
    done ) &
  if [ "$1" = "warmup" ] || [ "$1" = "B1" ]; then
    export S95_RMARK=$rmark S95_L=$L S95_MARK=$mark S95_BP=$benchpid S95_LEG=$1
    setsid bash -c '
      L=$S95_L; mark=$S95_MARK; benchpid=$S95_BP; hleg=$S95_LEG; hp=6090; rmark=$S95_RMARK
      say() { printf "%s %s\n" "$(date +%T)" "$*" >> "$L"; }
      sw=$SECONDS; while [ $((SECONDS - sw)) -lt 2700 ]; do
        [ "$(grep -c "funding barrier: all .* funded; releasing" $L 2>/dev/null | tail -1)" -gt "$rmark" ] && break
        kill -0 $benchpid 2>/dev/null || { say "S95 capture $hleg: bench ended before barrier release"; exit 0; }
        sleep 1
      done
      [ "$(grep -c "funding barrier: all .* funded; releasing" $L 2>/dev/null | tail -1)" -gt "$rmark" ] || { say "S95 capture $hleg: barrier release not seen in 2700 s"; exit 0; }
      t0=$SECONDS; say "S101 capture $hleg: barrier release seen (anchor); heap captures at +20/+60/+90/+150 s (cap 400 s)"
      for t in 20 60 90 150 180 240; do
        while [ $((SECONDS - t0)) -lt $t ]; do sleep 1; done
        [ $((SECONDS - t0)) -gt 400 ] && break
        if [ $t -ge 180 ]; then
          f=/data/blockchain/wr-logs/r35zzzce-goroutine2-$hleg-${t}s.txt
          curl -s -m 30 "http://127.0.0.1:$hp/debug/pprof/goroutine?debug=2" > $f 2>/dev/null
          say "S96 capture $hleg +${t}s: goroutine debug=2 -> $f ($(wc -c < $f 2>/dev/null || echo 0) bytes)"
          continue
        fi
        f=/data/blockchain/wr-logs/r35zzzce-heap-$hleg-${t}s.pb.gz
        curl -s -m 20 http://127.0.0.1:$hp/debug/pprof/heap > $f 2>/dev/null
        say "S95 capture $hleg +${t}s: heap -> $f ($(wc -c < $f 2>/dev/null || echo 0) bytes)"
        if [ $t = 90 ]; then
          f=/data/blockchain/wr-logs/r35zzzce-allocs-$hleg-90s.pb.gz
          curl -s -m 20 http://127.0.0.1:$hp/debug/pprof/allocs > $f 2>/dev/null
          say "S95 capture $hleg +90s: allocs -> $f ($(wc -c < $f 2>/dev/null || echo 0) bytes)"
          f=/data/blockchain/wr-logs/r35zzzce-goroutine-$hleg-90s.txt
          curl -s -m 20 "http://127.0.0.1:$hp/debug/pprof/goroutine?debug=1" > $f 2>/dev/null
          say "S95 capture $hleg +90s: goroutine -> $f ($(wc -c < $f 2>/dev/null || echo 0) bytes)"
        fi
      done' >/dev/null 2>&1 </dev/null &
  fi

  if [ "$1" = "B1" ] || [ "$1" = "B2" ]; then
    ( win1_start=0
      deadline=$((SECONDS + 900))
      while [ "$win1_start" = 0 ] && [ $SECONDS -lt $deadline ]; do
        kill -0 $benchpid 2>/dev/null || break
        sleep 1
        if [ "$(grep -c 'funding barrier: all .* funded; releasing' $L 2>/dev/null | tail -1)" -gt "$rmark" ]; then
          win1_start=$((SECONDS + 15))
          say "S101 capture $1: barrier release seen; win1 start = now+15s (anchored on the release, not the 'all 8 flood' line)"
        fi
      done
      if [ "$win1_start" = 0 ]; then
        say "S25 capture $1: flood-announcement signal not seen within 900s; falling back to first-full-block polling"
        deadline=$((SECONDS + 300))
        while [ "$win1_start" = 0 ] && [ $SECONDS -lt $deadline ]; do
          kill -0 $benchpid 2>/dev/null || break
          sleep 3
          hexnum=$(curl -s -m 3 -X POST -H 'content-type: application/json' --data '{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}' http://127.0.0.1:20012 2>/dev/null | grep -o '"result":"0x[0-9a-f]*"' | grep -o '0x[0-9a-f]*')
          [ -z "$hexnum" ] && continue
          blk=$(curl -s -m 3 -X POST -H 'content-type: application/json' --data "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"eth_getBlockByNumber\",\"params\":[\"$hexnum\",false]}" http://127.0.0.1:20018 2>/dev/null)
          gu=$(printf '%s' "$blk" | grep -o '"gasUsed":"0x[0-9a-f]*"' | grep -o '0x[0-9a-f]*')
          gl=$(printf '%s' "$blk" | grep -o '"gasLimit":"0x[0-9a-f]*"' | grep -o '0x[0-9a-f]*')
          [ -z "$gu" ] && continue
          [ -z "$gl" ] && continue
          if [ $((gl)) -gt 0 ] && [ $(( 100 * (gu) / (gl) )) -ge 45 ]; then
            win1_start=$SECONDS
            say "S25 capture $1: first full block $hexnum (gasUsed=$((gu)) gasLimit=$((gl))); win1 start (fallback path)"
          fi
        done
      fi
      if [ "$win1_start" = 0 ]; then
        say "S25 capture $1: neither signal fired; skipping win1/win2 captures"
        exit 0
      fi
      capture_win() {
        local win=$1 offset=$2
        local target=$((win1_start + offset)) now=$SECONDS remain
        remain=$((target - now))
        [ $remain -gt 0 ] && sleep $remain
        kill -0 $benchpid 2>/dev/null || { say "S22 capture $leg $win: bench already ended; skipping"; return; }
        declare -A propose_ts
        local i
        for i in 0 1 2 3 4 5 6; do
          propose_ts[$i]=$(grep '"msg":"miner: propose phases"' /data/blockchain/qs-node$i/log/n42.log 2>/dev/null | tail -1 | grep -o '"time":"[^"]*"')
        done
        local leader=0 follower=-1
        for i in 1 2 3 4 5 6; do
          if [ -n "${propose_ts[$i]}" ] && { [ -z "${propose_ts[$leader]}" ] || [[ "${propose_ts[$i]}" > "${propose_ts[$leader]}" ]]; }; then
            leader=$i
          fi
        done
        for i in 0 1 2 3 4 5 6; do
          [ "$i" = "$leader" ] && continue
          if [ "$follower" = -1 ] || [ -z "${propose_ts[$i]}" ] || { [ -n "${propose_ts[$follower]}" ] && [[ "${propose_ts[$i]}" < "${propose_ts[$follower]}" ]]; }; then
            follower=$i
          fi
        done
        say "S22 capture $leg $win: leader=node$leader follower=node$follower (win1_start+${offset}s)"
        local role_i
        for role_i in "$leader" "$follower"; do
          (
            curl -s -m 40 "http://127.0.0.1:$((6090+role_i))/debug/pprof/profile?seconds=20" -o /data/blockchain/wr-pprof/r35zzzce-$leg-$win-node$role_i-cpu.pb.gz \
              || say "S22 capture $leg $win: node$role_i cpu profile failed"
          ) &
          (
            curl -s -m 40 "http://127.0.0.1:$((6090+role_i))/debug/pprof/mutex?seconds=20" -o /data/blockchain/wr-pprof/r35zzzce-$leg-$win-node$role_i-mutex.pb.gz \
              || say "S22 capture $leg $win: node$role_i mutex profile failed"
          ) &
          (
            curl -s -m 40 "http://127.0.0.1:$((6090+role_i))/debug/pprof/block?seconds=20" -o /data/blockchain/wr-pprof/r35zzzce-$leg-$win-node$role_i-block.pb.gz \
              || say "S22 capture $leg $win: node$role_i block profile failed"
          ) &
          (
            curl -s -m 40 "http://127.0.0.1:$((6090+role_i))/debug/pprof/heap" -o /data/blockchain/wr-pprof/r35zzzce-$leg-$win-node$role_i-heap.pb.gz \
              || say "S22 capture $leg $win: node$role_i heap profile failed"
          ) &
          (
            curl -s -m 40 "http://127.0.0.1:$((6090+role_i))/debug/pprof/allocs?seconds=20" -o /data/blockchain/wr-pprof/r35zzzce-$leg-$win-node$role_i-allocs.pb.gz \
              || say "S25 capture $leg $win: node$role_i allocs profile failed"
          ) &
          (
            curl -s -m 40 "http://127.0.0.1:$((6090+role_i))/debug/pprof/goroutine?debug=1" -o /data/blockchain/wr-pprof/r35zzzce-$leg-$win-node$role_i-goroutines.txt \
              || say "S22 capture $leg $win: node$role_i goroutine dump failed"
          ) &
        done
        wait
        say "S22 capture $leg $win: done (leader=node$leader follower=node$follower)"
      }
      capture_win win1 15
      capture_win win2 75
    ) &
  fi
  local drained0; drained0=$(grep -c '^drained:' "$L" 2>/dev/null || true)
  ( while kill -0 $benchpid 2>/dev/null; do
      sleep 15
      [ "$(grep -c '^drained:' "$L" 2>/dev/null)" -gt "$drained0" ] && exit 0
      while read -r i off; do
        f=/data/blockchain/qs-node$i/log/n42.log
        if tail -c "+$((off+1))" "$f" 2>/dev/null | grep -F 'BAD BLOCK' | grep -v -E 'db closed|context canceled' \
             | grep -o 'Hash: 0x[0-9a-f]*' \
             | grep -v -E '0x2c4de4832128a4ba|0xb7323b47b7f3a93e|0x7946f2380733c835' | grep -q .; then
          say "ABORT $1: node $i logged a BAD BLOCK; stopping the round"
          touch /data/blockchain/wr-logs/r35zzzce-MODE-FAILED
          kill -TERM $benchpid 2>/dev/null
          exit 0
        fi
        if tail -c "+$((off+1))" "$f" 2>/dev/null | grep -v 'context canceled' | grep -q -E 'does not reproduce sealed root|QMDB tree/marker discontinuity before execution|deferred execution: header .* carries|deferred execution: .* not stored on this node|transaction root hash mismatch'; then
          say "ABORT $1: node $i logged a deferred-execution or tx-root failure; stopping the round"
          touch /data/blockchain/wr-logs/r35zzzce-MODE-FAILED
          kill -TERM $benchpid 2>/dev/null
          exit 0
        fi
      done < /data/blockchain/wr-logs/r35zzzce.logmark
    done ) &
  local badblockcheckpid=$!
  wait $benchpid
  kill "$badblockcheckpid" 2>/dev/null
  wait "$badblockcheckpid" 2>/dev/null
  for _w in $(seq 1 30); do pgrep -f "$PRESIGN_DIR/corpus-" >/dev/null 2>&1 || break; sleep 1; done
  rm -f "$PRESIGN_DIR"/corpus-*.bin
  say "LEG $1: presign corpora removed from $PRESIGN_DIR; $(df -h /data/blockchain | tail -1 | awk '{print $4}') free"
  say "LEG $1 done"
  fd=/data/blockchain/wr-logs/r35zzzce-floods/$1
  mkdir -p "$fd"
  find /data/blockchain -maxdepth 1 -name "bench-flood-r35-$1.out*" -newer "$flood_marker" -exec cp -f {} "$fd/" \; 2>/dev/null
  find /data/blockchain -maxdepth 1 -name "bench-flood-r35-$1.err*" -newer "$flood_marker" -exec cp -f {} "$fd/" \; 2>/dev/null
  rm -f "$flood_marker"
  if [ -f /data/blockchain/wr-logs/r35zzzce-MODE-FAILED ]; then
    ( source ./env-linux.sh; ./stop-fleet.sh ) >>$L 2>&1
    say "ROUND ABORTED"; exit 1
  fi
}

rm -f /data/blockchain/wr-logs/r35zzzce-MODE-FAILED
run_leg warmup 6846000000 2520000000 3423000000 1 7GiB 16777216 600000/200000 4 8 1 0/0/15000 0 0 4 0 0 0 8 2000 0 0 0 4 125 32
run_leg A1 6846000000 2528000000 3423000000 1 7GiB 16777216 600000/200000 4 8 1 0/0/15000 0 0 4 0 0 0 8 2000 0 0 0 4 125 32
run_leg B1 6846000000 2536000000 3423000000 1 7GiB 16777216 600000/200000 4 8 1 0/0/15000 0 0 4 0 0 0 8 2000 0 0 0 4 125 32
run_leg B2 6846000000 2544000000 3423000000 1 7GiB 16777216 600000/200000 4 8 1 0/0/15000 0 0 4 0 0 0 8 2000 0 0 0 4 125 32
run_leg A2 6846000000 2552000000 3423000000 1 7GiB 16777216 600000/200000 4 8 1 0/0/15000 0 0 4 0 0 0 8 2000 0 0 0 4 125 32

check_conflicting_commits() {
  local logs=(/data/blockchain/qs-node{0..6}/log/n42.log)
  local found
  found=$(grep -h '^{' "${logs[@]}" 2>/dev/null | jq -s -r '
    ( [ .[] | select(.msg=="🔨 Successfully sealed new block" or .msg=="add future block" or .msg=="block push: received")
            | select(.hash != null and (.number|type)=="number")
            | {(.hash): .number} ] | add // {} ) as $h2n
    | ( [ .[] | select(.msg=="block committed!") | .blockHash ] | unique ) as $committed
    | $committed
    | map({hash: ., number: ($h2n[.] // null)})
    | group_by(.number)
    | map(select((.[0].number != null) and (length>1)))
    | .[]
    | "\(.[0].number) \([.[].hash] | join(","))"
  ' 2>/dev/null)
  if [ -n "$found" ]; then
    while IFS=' ' read -r height hashes; do
      say "ROUND ABORTED: conflicting commits at height $height ($hashes)"
    done <<< "$found"
    return 1
  fi
  return 0
}

check_legs_produced() {
  local bad
  bad=$(grep -nE "chain is not producing|refusing to measure" "$L" 2>/dev/null)
  if [ -n "$bad" ]; then
    local legs=""
    while IFS=: read -r lineno _; do
      local leg
      leg=$(grep -nE "^[0-9]{2}:[0-9]{2}:[0-9]{2} LEG [A-Za-z0-9]+:" "$L" 2>/dev/null \
            | awk -F: -v n="$lineno" '$1<n{l=$0} END{print l}' \
            | sed -E 's/^[0-9]+:[0-9]{2}:[0-9]{2}:[0-9]{2} LEG ([A-Za-z0-9]+):.*/\1/')
      leg="${leg:-unknown}"
      case " $legs " in *" $leg "*) continue ;; esac
      legs="$legs $leg"
      say "ROUND ABORTED: leg $leg did not produce"
    done <<< "$bad"
    return 1
  fi
  return 0
}

round_ok=1
check_conflicting_commits || round_ok=0
check_legs_produced || round_ok=0

if [ -f /data/blockchain/wr-logs/r35zzzce-MODE-FAILED ]; then
  say "ROUND DONE -- BUT THE MODE INTERLOCK FAILED; the legs are not what they claim to be, publish nothing"
elif [ "$round_ok" != 1 ]; then
  : # a check above already printed its own ROUND ABORTED line(s)
else
  say "ROUND DONE"
fi

say "S105 finished; all previous replay snapshots retained"
