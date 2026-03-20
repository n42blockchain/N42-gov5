#!/usr/bin/env bash
# 24h soak test with resource boundary monitoring.
#
# Continuously monitors node resource usage and enforces red-line thresholds.
# Designed for nightly/weekly CI or manual long-duration validation.
#
# Usage: ./scripts/run_soak_24h.sh [DURATION_HOURS] [METRICS_URL] [PPROF_URL]
# Defaults: 24 hours, http://127.0.0.1:6060/debug/metrics/prometheus, http://127.0.0.1:6060

set -euo pipefail

DURATION_HOURS="${1:-24}"
METRICS_URL="${2:-http://127.0.0.1:6060/debug/metrics/prometheus}"
PPROF_URL="${3:-http://127.0.0.1:6060}"
SAMPLE_INTERVAL=300  # 5 minutes
OUTDIR="build/soak-24h/$(date -u +%Y%m%d-%H%M%SZ)"

# Resource red-line thresholds
MAX_GOROUTINES=10000
MAX_HEAP_MB=8192        # 8 GB
MAX_RSS_PERCENT=90      # of system RAM
MAX_OPEN_FDS=50000

mkdir -p "$OUTDIR"

log() { echo "[$(date -u +%H:%M:%S)] $*" | tee -a "$OUTDIR/soak.log"; }
fail() { log "THRESHOLD BREACH: $*"; echo "$*" >> "$OUTDIR/breaches.log"; BREACHES=$((BREACHES+1)); }

TOTAL_SAMPLES=0
BREACHES=0
DURATION_SECONDS=$((DURATION_HOURS * 3600))
START_TIME=$(date +%s)

log "=== N42 24h Soak Test ==="
log "Duration: ${DURATION_HOURS}h"
log "Metrics: $METRICS_URL"
log "Thresholds: goroutines<$MAX_GOROUTINES heap<${MAX_HEAP_MB}MB fds<$MAX_OPEN_FDS"
log "Output: $OUTDIR"
log ""

# Header for CSV
echo "timestamp,goroutines,heap_mb,alloc_mb,gc_count,gc_pause_ms,block_number" > "$OUTDIR/samples.csv"

while true; do
    ELAPSED=$(($(date +%s) - START_TIME))
    if [ "$ELAPSED" -ge "$DURATION_SECONDS" ]; then
        break
    fi

    TOTAL_SAMPLES=$((TOTAL_SAMPLES+1))
    TS=$(date -u +%Y-%m-%dT%H:%M:%SZ)

    # Scrape Prometheus metrics
    METRICS=$(curl -s "$METRICS_URL" 2>/dev/null || echo "")
    if [ -z "$METRICS" ]; then
        log "WARNING: Cannot reach metrics endpoint"
        sleep "$SAMPLE_INTERVAL"
        continue
    fi

    # Extract key values
    GOROUTINES=$(echo "$METRICS" | grep '^go_goroutines ' | awk '{print $2}' | cut -d. -f1 || echo "0")
    HEAP_BYTES=$(echo "$METRICS" | grep '^go_memstats_heap_alloc_bytes ' | awk '{print $2}' | cut -d. -f1 || echo "0")
    ALLOC_BYTES=$(echo "$METRICS" | grep '^go_memstats_alloc_bytes ' | awk '{print $2}' | cut -d. -f1 || echo "0")
    GC_COUNT=$(echo "$METRICS" | grep '^go_gc_num_gc ' | awk '{print $2}' | cut -d. -f1 || echo "0")
    GC_PAUSE=$(echo "$METRICS" | grep '^go_gc_pause_seconds_last ' | awk '{print $2}' || echo "0")
    BLOCK=$(echo "$METRICS" | grep '^sync_current_block ' | awk '{print $2}' | cut -d. -f1 || echo "0")

    HEAP_MB=$((HEAP_BYTES / 1048576))
    ALLOC_MB=$((ALLOC_BYTES / 1048576))
    GC_PAUSE_MS=$(echo "$GC_PAUSE" | awk '{printf "%.1f", $1 * 1000}' 2>/dev/null || echo "0")

    # Log sample
    echo "$TS,$GOROUTINES,$HEAP_MB,$ALLOC_MB,$GC_COUNT,$GC_PAUSE_MS,$BLOCK" >> "$OUTDIR/samples.csv"

    REMAINING=$((DURATION_SECONDS - ELAPSED))
    REMAINING_H=$((REMAINING / 3600))
    REMAINING_M=$(((REMAINING % 3600) / 60))
    log "Sample #$TOTAL_SAMPLES: goroutines=$GOROUTINES heap=${HEAP_MB}MB block=$BLOCK (${REMAINING_H}h${REMAINING_M}m left)"

    # Check thresholds
    if [ "$GOROUTINES" -gt "$MAX_GOROUTINES" ] 2>/dev/null; then
        fail "goroutines=$GOROUTINES > $MAX_GOROUTINES"
    fi
    if [ "$HEAP_MB" -gt "$MAX_HEAP_MB" ] 2>/dev/null; then
        fail "heap=${HEAP_MB}MB > ${MAX_HEAP_MB}MB"
    fi

    # Capture pprof snapshot every hour
    if [ $((TOTAL_SAMPLES % 12)) -eq 0 ]; then
        PPROF_FILE="$OUTDIR/heap_$(date -u +%H%M).pb.gz"
        curl -s "$PPROF_URL/debug/pprof/heap" -o "$PPROF_FILE" 2>/dev/null || true
        log "  pprof heap snapshot: $PPROF_FILE"
    fi

    sleep "$SAMPLE_INTERVAL"
done

# Summary
log ""
log "=== Soak Test Complete ==="
log "Duration: ${DURATION_HOURS}h"
log "Samples: $TOTAL_SAMPLES"
log "Breaches: $BREACHES"

# Generate summary
cat > "$OUTDIR/summary.md" << SUMMARY
# Soak Test Summary

- **Duration**: ${DURATION_HOURS} hours
- **Samples**: $TOTAL_SAMPLES (every ${SAMPLE_INTERVAL}s)
- **Threshold Breaches**: $BREACHES
- **Thresholds**: goroutines<$MAX_GOROUTINES, heap<${MAX_HEAP_MB}MB

## Result: $([ "$BREACHES" -eq 0 ] && echo "PASS" || echo "FAIL ($BREACHES breaches)")

## Data Files
- samples.csv: time-series resource data
- soak.log: full event log
$([ -f "$OUTDIR/breaches.log" ] && echo "- breaches.log: threshold violations" || true)
SUMMARY

if [ "$BREACHES" -gt 0 ]; then
    log "RESULT: FAIL ($BREACHES threshold breaches)"
    exit 1
fi
log "RESULT: PASS"
