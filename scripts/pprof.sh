#!/usr/bin/env bash
# pprof helper for running ethexec. Usage: ./scripts/pprof.sh <mode> [duration]
#
# Modes:
#   cpu [secs]    — CPU profile (default 60s) + top 30 functions
#   heap          — heap snapshot + top 20 allocators by in-use bytes
#   alloc         — cumulative allocations (helps spot GC pressure)
#   mutex         — mutex contention (needs --pprof on ethexec)
#   block         — block profile / channel waits (needs --pprof)
#   goroutine     — goroutine count + top 20 stacks (use when stuck)
#   web-cpu [secs]— CPU profile + open in browser flame graph
#   inuse         — aggregated heap inuse_space (5s live sample)
#   gc            — GC trace summary
#   all [secs]    — run cpu + heap + alloc + mutex and save profiles/
#
# Raw profile capture requires curl. Top/flame-graph rendering additionally
# requires a Go distribution that ships `go tool pprof`; some Ubuntu/custom Go
# packages omit it, in which case this helper still preserves every raw profile.

set -euo pipefail

HOST="${PPROF_HOST:-localhost:6060}"
OUT="${PPROF_OUT:-profiles}"
mkdir -p "$OUT"

have_pprof=1
if ! go tool pprof -h >/dev/null 2>&1; then
    have_pprof=0
    echo "(note) go tool pprof is unavailable; capture is enabled, local top rendering is disabled" >&2
fi

pprof_top() {
    if [[ "$have_pprof" == 0 ]]; then
        echo "(note) go tool pprof is unavailable; raw profile was preserved for offline analysis" >&2
        return 0
    fi
    go tool pprof "$@"
}

mode="${1:-help}"
secs="${2:-60}"

ts() { date +%Y%m%d-%H%M%S; }

need_pprof_flag() {
    echo "(note) needs ethexec --pprof to collect meaningful data" >&2
}

case "$mode" in
    cpu)
        f="$OUT/cpu-$(ts).prof"
        echo "CPU profile ${secs}s → $f"
        curl -sS "http://${HOST}/debug/pprof/profile?seconds=${secs}" > "$f"
        echo
        echo "=== top 30 by flat ==="
        pprof_top -top -nodecount=30 "$f" 2>/dev/null | head -45
        echo
        echo "=== top 30 by cum ==="
        pprof_top -top -cum -nodecount=30 "$f" 2>/dev/null | head -45
        echo
        echo "Open flame graph: go tool pprof -http=:0 $f"
        ;;
    heap)
        f="$OUT/heap-$(ts).prof"
        echo "Heap snapshot → $f"
        curl -sS "http://${HOST}/debug/pprof/heap" > "$f"
        echo
        echo "=== top 20 inuse_space ==="
        pprof_top -top -nodecount=20 -inuse_space "$f" 2>/dev/null | head -32
        ;;
    alloc)
        f="$OUT/alloc-$(ts).prof"
        echo "Alloc profile (cumulative) → $f"
        curl -sS "http://${HOST}/debug/pprof/allocs" > "$f"
        echo
        echo "=== top 20 alloc_space ==="
        pprof_top -top -nodecount=20 -alloc_space "$f" 2>/dev/null | head -32
        echo
        echo "=== top 20 alloc_objects ==="
        pprof_top -top -nodecount=20 -alloc_objects "$f" 2>/dev/null | head -32
        ;;
    mutex)
        need_pprof_flag
        f="$OUT/mutex-$(ts).prof"
        echo "Mutex profile → $f"
        curl -sS "http://${HOST}/debug/pprof/mutex" > "$f"
        echo
        pprof_top -top -nodecount=20 "$f" 2>/dev/null | head -32
        ;;
    block)
        need_pprof_flag
        f="$OUT/block-$(ts).prof"
        echo "Block profile → $f"
        curl -sS "http://${HOST}/debug/pprof/block" > "$f"
        echo
        pprof_top -top -nodecount=20 "$f" 2>/dev/null | head -32
        ;;
    goroutine)
        f="$OUT/goroutine-$(ts).txt"
        echo "Goroutine dump → $f"
        curl -sS "http://${HOST}/debug/pprof/goroutine?debug=2" > "$f"
        echo
        echo "=== goroutine count: $(grep -c "^goroutine " "$f") ==="
        echo
        echo "=== top stack patterns ==="
        grep -A1 "^goroutine" "$f" | grep -v "^goroutine\|^--$" | sort | uniq -c | sort -rn | head -20
        ;;
    web-cpu)
        f="$OUT/cpu-$(ts).prof"
        echo "CPU profile ${secs}s → $f, opening flame graph..."
        curl -sS "http://${HOST}/debug/pprof/profile?seconds=${secs}" > "$f"
        pprof_top -http=:0 "$f"
        ;;
    inuse)
        if [[ "$have_pprof" == 0 ]]; then
            f="$OUT/heap-$(ts).prof"
            curl -sS "http://${HOST}/debug/pprof/heap" > "$f"
            echo "Heap snapshot → $f (render offline)"
        else
            curl -sS "http://${HOST}/debug/pprof/heap" | \
                pprof_top -top -nodecount=15 -inuse_space - 2>/dev/null | head -25
        fi
        ;;
    gc)
        # Read runtime GC stats via expvar or trace. Quick alternative:
        # just hit heap endpoint and print the goroutine section of profile.
        curl -sS "http://${HOST}/debug/vars" 2>/dev/null | \
            python3 -c "import json,sys;d=json.load(sys.stdin);m=d.get('memstats',{});print(f\"GC: NumGC={m.get('NumGC')} PauseTotalNs={m.get('PauseTotalNs')/1e9:.1f}s HeapAlloc={m.get('HeapAlloc')/1e9:.2f}GB HeapIdle={m.get('HeapIdle')/1e9:.2f}GB GCCPUFraction={m.get('GCCPUFraction'):.4f}\")" 2>/dev/null \
            || echo "/debug/vars not exposed; use cpu/heap modes instead"
        ;;
    all)
        secs="${2:-30}"
        stamp=$(ts)
        echo "Collecting all profiles (cpu=${secs}s) → $OUT/*-${stamp}.prof"
        curl -sS "http://${HOST}/debug/pprof/profile?seconds=${secs}" > "$OUT/cpu-${stamp}.prof" &
        curl -sS "http://${HOST}/debug/pprof/heap"                    > "$OUT/heap-${stamp}.prof"
        curl -sS "http://${HOST}/debug/pprof/allocs"                  > "$OUT/alloc-${stamp}.prof"
        curl -sS "http://${HOST}/debug/pprof/mutex"                   > "$OUT/mutex-${stamp}.prof"
        curl -sS "http://${HOST}/debug/pprof/block"                   > "$OUT/block-${stamp}.prof"
        curl -sS "http://${HOST}/debug/pprof/goroutine?debug=2"       > "$OUT/goroutine-${stamp}.txt"
        wait
        echo "Done. Files in $OUT/"
        echo
        echo "=== CPU top 20 flat ==="
        pprof_top -top -nodecount=20 "$OUT/cpu-${stamp}.prof" 2>/dev/null | head -32
        echo
        echo "=== Heap top 15 inuse ==="
        pprof_top -top -nodecount=15 -inuse_space "$OUT/heap-${stamp}.prof" 2>/dev/null | head -25
        ;;
    help|*)
        cat <<EOF
pprof helper. ethexec must be running (pprof server on ${HOST}).

Usage: $0 <mode> [args]

Modes:
  cpu [secs=60]   — CPU profile + top functions (flat + cum)
  heap            — heap snapshot + top inuse allocators
  alloc           — cumulative allocations (GC pressure diagnosis)
  mutex           — mutex contention (needs ethexec --pprof)
  block           — block / channel waits (needs ethexec --pprof)
  goroutine       — goroutine dump + stack histogram
  web-cpu [secs]  — CPU profile + interactive flame graph (opens browser)
  inuse           — quick inuse_space top
  gc              — runtime GC stats from /debug/vars
  all [secs=30]   — collect everything

Env:
  PPROF_HOST      — pprof host:port (default localhost:6060)
  PPROF_OUT       — output dir (default ./profiles)

Examples:
  $0 cpu 60           # 60s CPU profile + top
  $0 web-cpu 30       # flame graph
  $0 all 30           # snapshot everything for later analysis
EOF
        ;;
esac
