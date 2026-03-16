#!/bin/bash
# N42 Log Monitor
# Copyright 2022-2026 The N42 Authors
#
# Monitors N42 node log files for errors, warnings, and anomalies.
# Can run standalone or as part of stress_test.sh.
#
# Usage:
#   ./scripts/log_monitor.sh <log_file> [--duration 3600] [--interval 30]

set -euo pipefail

LOG_FILE="${1:-}"
DURATION="${2:-3600}"
INTERVAL=30
REPORT_FILE=""

# Parse optional args
shift 1 2>/dev/null || true
while [[ $# -gt 0 ]]; do
    case $1 in
        --duration)  DURATION="$2"; shift 2 ;;
        --interval)  INTERVAL="$2"; shift 2 ;;
        --report)    REPORT_FILE="$2"; shift 2 ;;
        *) shift ;;
    esac
done

if [ -z "$LOG_FILE" ]; then
    echo "Usage: $0 <log_file> [--duration N] [--interval N] [--report file]"
    exit 1
fi

if [ ! -f "$LOG_FILE" ]; then
    echo "Log file not found: $LOG_FILE"
    echo "Waiting for log file..."
    for i in $(seq 1 30); do
        if [ -f "$LOG_FILE" ]; then
            break
        fi
        sleep 1
    done
    if [ ! -f "$LOG_FILE" ]; then
        echo "ERROR: Log file never appeared: $LOG_FILE"
        exit 1
    fi
fi

echo "=========================================="
echo "N42 Log Monitor"
echo "=========================================="
echo "Log file:  $LOG_FILE"
echo "Duration:  ${DURATION}s"
echo "Interval:  ${INTERVAL}s"
echo "=========================================="

START_TIME=$(date +%s)
END_TIME=$((START_TIME + DURATION))

# Counters
TOTAL_ERRORS=0
TOTAL_WARNS=0
TOTAL_PANICS=0
LAST_LINE_COUNT=0
CHECKS_DONE=0

# Known error patterns to flag
CRITICAL_PATTERNS=(
    "panic"
    "fatal"
    "out of memory"
    "OOM"
    "segfault"
    "SIGSEGV"
    "deadlock"
    "database.*corrupt"
    "mdbx.*error"
)

# Warning patterns (informational)
WARN_PATTERNS=(
    "peer.*disconnect"
    "block.*timeout"
    "tx.*rejected"
    "nonce.*too.*low"
    "insufficient.*funds"
    "gas.*limit"
)

check_patterns() {
    local FILE=$1
    local FROM_LINE=$2

    # Count new lines
    local CURRENT_LINES
    CURRENT_LINES=$(wc -l < "$FILE" 2>/dev/null || echo "0")
    local NEW_LINES=$((CURRENT_LINES - FROM_LINE))

    if [ "$NEW_LINES" -le 0 ]; then
        return 0
    fi

    # Check critical patterns
    for pattern in "${CRITICAL_PATTERNS[@]}"; do
        local COUNT
        COUNT=$(tail -n "$NEW_LINES" "$FILE" | grep -ci "$pattern" 2>/dev/null || echo "0")
        if [ "$COUNT" -gt 0 ]; then
            echo "[$(date '+%H:%M:%S')] CRITICAL: Found $COUNT occurrences of '$pattern'"
            tail -n "$NEW_LINES" "$FILE" | grep -i "$pattern" | head -3
            TOTAL_PANICS=$((TOTAL_PANICS + COUNT))
        fi
    done

    # Count errors and warnings
    local NEW_ERRORS
    NEW_ERRORS=$(tail -n "$NEW_LINES" "$FILE" | grep -ci "error" 2>/dev/null || echo "0")
    local NEW_WARNS
    NEW_WARNS=$(tail -n "$NEW_LINES" "$FILE" | grep -ci "warn" 2>/dev/null || echo "0")

    TOTAL_ERRORS=$((TOTAL_ERRORS + NEW_ERRORS))
    TOTAL_WARNS=$((TOTAL_WARNS + NEW_WARNS))

    LAST_LINE_COUNT=$CURRENT_LINES
}

# Main monitor loop
while [ "$(date +%s)" -lt "$END_TIME" ]; do
    sleep "$INTERVAL"
    CHECKS_DONE=$((CHECKS_DONE + 1))

    check_patterns "$LOG_FILE" "$LAST_LINE_COUNT"

    ELAPSED=$(( $(date +%s) - START_TIME ))
    REMAINING=$(( END_TIME - $(date +%s) ))

    printf "[%s] Check #%d | Lines: %d | Errors: %d | Warnings: %d | Critical: %d | Elapsed: %ds | Left: %ds\n" \
        "$(date '+%H:%M:%S')" "$CHECKS_DONE" "$LAST_LINE_COUNT" \
        "$TOTAL_ERRORS" "$TOTAL_WARNS" "$TOTAL_PANICS" \
        "$ELAPSED" "$REMAINING"
done

# Final summary
echo ""
echo "=========================================="
echo "Log Monitor Summary"
echo "=========================================="
echo "Total checks:    $CHECKS_DONE"
echo "Total log lines: $LAST_LINE_COUNT"
echo "Total errors:    $TOTAL_ERRORS"
echo "Total warnings:  $TOTAL_WARNS"
echo "Critical issues: $TOTAL_PANICS"
echo ""

if [ "$TOTAL_PANICS" -gt 0 ]; then
    echo "RESULT: FAIL - Critical issues detected"
    echo "Critical log entries:"
    for pattern in "${CRITICAL_PATTERNS[@]}"; do
        grep -i "$pattern" "$LOG_FILE" 2>/dev/null | head -5
    done
    EXIT_CODE=1
elif [ "$TOTAL_ERRORS" -gt 100 ]; then
    echo "RESULT: WARNING - High error count ($TOTAL_ERRORS)"
    EXIT_CODE=0
else
    echo "RESULT: PASS - No critical issues"
    EXIT_CODE=0
fi

# Write report if requested
if [ -n "$REPORT_FILE" ]; then
    {
        echo "Log Monitor Report"
        echo "Date: $(date)"
        echo "Log file: $LOG_FILE"
        echo "Checks: $CHECKS_DONE"
        echo "Errors: $TOTAL_ERRORS"
        echo "Warnings: $TOTAL_WARNS"
        echo "Critical: $TOTAL_PANICS"
    } > "$REPORT_FILE"
fi

exit $EXIT_CODE
