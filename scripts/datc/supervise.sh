#!/bin/bash
# supervise.sh <run-script> <build-log> : keep relaunching a DATC build until its log says "DATC build done".
# Resume is automatic (--start from DatcMeta/progress); an OOM kill or crash costs at most one batch.
RUN=$1; LOG=$2
while true; do
  if grep -q "DATC build done" "$LOG" 2>/dev/null && grep -q "leafseg\] done" "$LOG" 2>/dev/null; then echo "[supervise] finished"; exit 0; fi
  echo "[supervise] $(date -u +%FT%TZ) launching $RUN" >> "$LOG"
  "$RUN" >> "$LOG" 2>&1
  rc=$?
  echo "[supervise] $(date -u +%FT%TZ) exited rc=$rc" >> "$LOG"
  if grep -q "DATC build done" "$LOG"; then continue; fi
  sleep 60
done
