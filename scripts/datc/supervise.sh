#!/bin/bash
# supervise.sh <run-script> <build-log>
#
# Keeps relaunching a DATC build until its log says the build finished
# (resume is automatic: --start comes from DatcMeta/progress, so a crash or an
# OOM kill costs at most one batch).
#
# TERM/INT to the SUPERVISOR is forwarded to the build as a graceful stop (it
# finishes the batch and cuts the spill at a frame boundary) and the supervisor
# then exits WITHOUT relaunching — one kill stops the pair. Without the trap
# the handler only ran on the normal path, so a TERM left the build running
# orphaned and every stop needed two kills in the right order.
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

while true; do
  if grep -q "DATC build done" "$LOG" 2>/dev/null && grep -q "leafseg\] done" "$LOG" 2>/dev/null; then
    echo "[supervise] finished"
    exit 0
  fi
  echo "[supervise] $(date -u +%FT%TZ) launching $RUN" >> "$LOG"
  "$RUN" >> "$LOG" 2>&1 &
  child=$!
  wait "$child"
  rc=$?
  child=""
  [ "$stopping" = 1 ] && exit 0
  echo "[supervise] $(date -u +%FT%TZ) exited rc=$rc" >> "$LOG"
  grep -q "DATC build done" "$LOG" && continue
  sleep 60
done
