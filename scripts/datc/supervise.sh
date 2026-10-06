#!/bin/bash
# supervise.sh <run-script> <build-log>
#
# Keeps relaunching a DATC build until it finishes (resume is automatic:
# --start comes from DatcMeta/progress, so a crash or an OOM kill costs at most
# one batch).
#
# TERM/INT to the SUPERVISOR is forwarded to the build as a graceful stop (it
# finishes the batch and cuts the spill at a frame boundary) and the supervisor
# then exits WITHOUT relaunching — one kill stops the pair. Without the trap
# the handler only ran on the normal path, so a TERM left the build running
# orphaned and every stop needed two kills in the right order.
#
# The leaf finalize is never retried. Once a build reaches its end it prints
# "[leafseg] finalizing"; if it dies before "[leafseg] done", buckets whose
# spill was retained (kill-tail frames) would be merged a second time into
# their finished segments on a relaunch (duplicate rows, 2026-09-15). The
# supervisor then stops, and refuses to start while the log shows such an
# unfinished finalize. Recover by hand: quarantine every spill that already
# has a .seg, run `finalize-leaves` standalone, then append "[leafseg] done
# (standalone)" to the log.
RUN=$1
LOG=$2
child=""
stopping=0

graceful() {
  stopping=1
  if [ -n "$child" ] && kill -0 "$child" 2>/dev/null; then
    echo "[supervise] $(date -u +%FT%TZ) forwarding TERM to $child (graceful stop)" >> "$LOG"
    kill -TERM "$child"
    wait "$child" 2>/dev/null
  fi
  exit 0
}
trap graceful TERM INT

# finalize_pending: the last "[leafseg] finalizing" in the log has no later "[leafseg] done".
finalize_pending() {
  [ -f "$LOG" ] && [ "$(awk '/^\[leafseg\] finalizing/{p=1} /^\[leafseg\] done/{p=0} END{print p+0}' "$LOG")" = 1 ]
}

refuse() {
  echo "[supervise] $(date -u +%FT%TZ) $1; NOT launching. Quarantine spills that already have a .seg," \
    "run finalize-leaves standalone, then append '[leafseg] done (standalone)' to $LOG." | tee -a "$LOG"
  exit 1
}

finalize_pending && refuse "log shows an unfinished leaf finalize"

while true; do
  launched_at=$(( $( [ -f "$LOG" ] && wc -l < "$LOG" || echo 0) + 1 ))
  echo "[supervise] $(date -u +%FT%TZ) launching $RUN" >> "$LOG"
  "$RUN" >> "$LOG" 2>&1 &
  child=$!
  wait "$child"
  rc=$?
  child=""
  [ "$stopping" = 1 ] && exit 0
  echo "[supervise] $(date -u +%FT%TZ) exited rc=$rc" >> "$LOG"
  if tail -n +"$launched_at" "$LOG" | grep -q "DATC build done"; then
    if [ "$rc" = 0 ] && ! finalize_pending; then
      echo "[supervise] finished"
      exit 0
    fi
    refuse "build reached its end but exited rc=$rc during the leaf finalize"
  fi
  sleep 60
done
