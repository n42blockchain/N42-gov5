#!/usr/bin/env bash
# S105: quiet-box gates, preserve the prior fleet generation, then run A-B-B-A.
set -u
export TZ=America/New_York
W=/data/blockchain/gov5-work
L=/data/blockchain/wr-logs/chain-35zzzce.log
say(){ echo "$(date +%H:%M:%S) $*" | tee -a "$L"; }
sleep 5
say "S105 (35zzzce) chain started; waiting for the MemAvailable and quiet gates"
for attempt in 1; do
  say "attempt $attempt"
  avail_gb(){ awk '/^MemAvailable:/{printf "%d", $2/1048576}' /proc/meminfo; }
  n=0
  while [ "$(avail_gb)" -lt 90 ]; do
    n=$((n+1)); sleep 60
  done
  say "memory ok: MemAvailable $(avail_gb) GB"
  rs_waiting(){ pgrep -f "n42-n42-rs/.*/run-[a-z0-9]*[.]sh" >/dev/null || [ -e /data/blockchain/.box-claim-rust ] || [ -e /data/blockchain/wr-logs/.box-claim-rust ]; }
  if rs_waiting; then
    say "taking turns: n42-rs is waiting; up to 20 min for .box-claim-rust"
    for i in $(seq 1 40); do
      if [ -e /data/blockchain/.box-claim-rust ] || [ -e /data/blockchain/wr-logs/.box-claim-rust ]; then say "n42-rs holds the box; waiting for it to finish"; break; fi
      sleep 30
    done
  fi
  while pgrep -f "cold-replay-20260914/run_replay.py" >/dev/null || pgrep -x rbtcd >/dev/null \
     || pgrep -x overlay_audit >/dev/null || pgrep -x mdbx_copy >/dev/null || pgrep -x mdbx_chk >/dev/null; do
    sleep 60
  done
  say "waiting for the box: no foreign claim, load1 < 8, three checks 60 s apart"
  quiet=0
  while [ $quiet -lt 3 ]; do
    claims=0
    now=$(date -u +%s)
    for cf in /data/blockchain/.box-claim-* /data/blockchain/wr-logs/.box-claim-*; do
      [ -f "$cf" ] || continue
      case "$cf" in *-gov5) continue ;; esac
      ts=$(tr -dc '0-9' < "$cf" | cut -c1-10)
      [ -n "$ts" ] || ts=0
      [ $(( now - ts )) -lt 5400 ] && claims=$((claims+1))
    done
    l1=$(awk '{printf "%d", $1}' /proc/loadavg)
    if [ "$claims" = 0 ] && [ "$l1" -lt 8 ]; then quiet=$((quiet+1)); else quiet=0; fi  # rbtc soaks run unclaimed and can idle for hours: the load gate covers them at start
    [ $quiet -lt 3 ] && sleep 60
  done
  say "box quiet (claims=0, load1=$(awk '{print $1}' /proc/loadavg))"
  date -u +%s > /data/blockchain/.box-claim-gov5; date -u +%s > /data/blockchain/wr-logs/.box-claim-gov5
  trap 'rm -f /data/blockchain/.box-claim-gov5 /data/blockchain/wr-logs/.box-claim-gov5' EXIT
  left=$(ps -eo args | awk '($1=="bash" && $2 ~ /run-r35zz/) || $1 ~ /\/n42-r35$/ || $1 ~ /txflood-r[34][0-9]$/' | wc -l)
  if [ "$left" != 0 ]; then say "not launching yet: leftover=$left (the previous round is still shutting down)"; sleep 120; continue; fi
  [ -s "$W/n42-r142.sha256" ] && sha256sum -c "$W/n42-r142.sha256" >>"$L" 2>&1 || { say "BINARY CHECK FAILED"; exit 1; }
  available_bytes=$(df -B1 --output=avail /data | tail -1)
  seed_bytes=$(du -sb /data/blockchain/qs-era-linux | awk '{print $1}')
  [ "$available_bytes" -gt $((7*seed_bytes + 200*1024*1024*1024)) ] || { say "DISK CHECK FAILED: seed plus growth reserve"; exit 1; }
  sleep $((20 + RANDOM % 31))
  mine=$(cat /data/blockchain/.box-claim-gov5)
  for foreign in /data/blockchain/.box-claim-* /data/blockchain/wr-logs/.box-claim-*; do
    [ -f "$foreign" ] || continue
    case "$foreign" in *-gov5) continue ;; esac
    theirs=$(cat "$foreign")
    case "$theirs" in ''|*[!0-9]*) say "CLAIM CHECK FAILED: unreadable foreign claim"; exit 1 ;; esac
    if [ "$theirs" -lt "$mine" ] && [ $(( $(date -u +%s)-theirs )) -lt 5400 ]; then
      say "yielding to older foreign claim"; exit 1
    fi
  done
  STAMP=$(date +%Y%m%d)
  for i in 0 1 2 3 4 5 6; do
    [ ! -e "/data/blockchain/qs-node$i.pre-$STAMP" ] || { say "RESEED BLOCKED: rollback path already exists"; exit 1; }
  done
  say "reseeding the fleet (old dirs -> qs-node*.pre-$STAMP)"
  if ! bash /data/blockchain/seed-7node-nolaunch.sh >>"$L" 2>&1; then say "RESEED FAILED"; exit 1; fi
  for i in 0 1 2 3 4 5 6; do
    [ -d /data/blockchain/qs-node$i/chaindata ] || { say "qs-node$i missing after reseed"; exit 1; }
  done
  say "reseed ok: $(du -sh /data/blockchain/qs-node0 | cut -f1) per node; rollback generation retained"
  say "previous fleet generation retained at qs-node*.pre-$STAMP"
  say "disk now: $(df -h /data | tail -1 | awk '{print $4" free"}')"
  [ -s $W/n42-r142 ] || { say "no n42-r142"; exit 1; }
  cmp -s $W/n42-r142 $W/n42-r35 || cp -f $W/n42-r142 $W/n42-r35
  printf '%s gov5: fleet reseeded; round 35zzzce (S105, n42-r142; all measured legs 163k/125ms; attestation wait A1/A2=0ms B1/B2=50ms; fresh dirs) queued via the claim protocol.\n' "$(date '+%Y-%m-%d %H:%M %Z')" >> /data/blockchain/wr-logs/BOX-NOTE-gov5.txt
  rm -f /data/blockchain/wr-logs/watch-r35zzzce-warmup.out
  trap - EXIT
  setsid nohup bash $W/run-r35zzzce.sh >/data/blockchain/wr-logs/r35zzzce-runner.out 2>&1 </dev/null &
  setsid nohup bash $W/watch-funding.sh r35zzzce warmup >/dev/null 2>&1 </dev/null &
  sleep 3
  say "launched: $(ps -eo args | awk '$1=="bash" && $2 ~ /run-r35zzzce/' | wc -l) runner"

  L35=/data/blockchain/wr-logs/r35zzzce.log
  until grep -qE "^[0-9:]{8} ROUND (DONE|.*ABORTED)" $L35 2>/dev/null; do sleep 30; done
  if grep -qE "^[0-9:]{8} ROUND DONE" $L35; then say "round done"; break; fi
  if grep -q "MemAvailable" $L35; then
    say "round ABORTED on memory; NOT retrying (S32: box is shared, no silent re-run)"
  else
    say "round aborted for another reason; not retrying"
  fi
  break
done
