// cgo-callback-bench: measure the cost of C→Go reverse callbacks.
//
// EVMC integration relies on the C++ EVM (e.g. evmone) calling back
// into Go for every state op (SLOAD, SSTORE, BALANCE, etc.). If the
// C→Go callback latency is large compared to our current Go-side
// SLOAD path (~50-100ns: stream cursor advance + length-prefix
// decode + slice copy), the integration nets a loss on state-heavy
// workloads.
//
// This bench measures three numbers:
//   1. Pure Go→C round-trip (no callback)
//   2. C→Go callback round-trip (the relevant one for EVMC)
//   3. N42's witness-reader SLOAD path (the baseline we'd replace)
//
// Decision rule:
//   callback cost < 0.5× witness SLOAD → evmone is a clear win
//   callback cost ~  1× witness SLOAD → break-even, depends on workload
//   callback cost > 2× witness SLOAD → evmone net-loses, skip integration
//
// Build: CGO_ENABLED=1 go build -o build/bin/cgo-callback-bench.exe ./cmd/cgo-callback-bench
package main

/*
#include <stddef.h>

// callback_fn is the Go function type the C side will invoke.
extern long long goCallback(long long n);

// run_callbacks invokes goCallback exactly `n` times, returning the
// final accumulator. Loop is in C so the cost we measure is purely
// the C→Go boundary (C→Go transition, then Go→C return) per call.
static long long run_callbacks(long long n) {
    long long acc = 0;
    for (long long i = 0; i < n; i++) {
        acc = goCallback(i);
    }
    return acc;
}

// run_noop_loop is a control: pure C loop with no callback. Subtract
// from run_callbacks to isolate the callback cost.
static long long run_noop_loop(long long n) {
    long long acc = 0;
    for (long long i = 0; i < n; i++) {
        acc += i;
    }
    return acc;
}
*/
import "C"

import (
	"fmt"
	"runtime"
	"sync/atomic"
	"time"
	_ "unsafe"
)

// goCallbackCounter is atomically incremented to prevent the compiler
// from optimising the callback away; published via the //export.
var goCallbackCounter int64

//export goCallback
func goCallback(n C.longlong) C.longlong {
	atomic.AddInt64(&goCallbackCounter, 1)
	return n + 1
}

func benchPureCLoop(iters int64) time.Duration {
	t0 := time.Now()
	C.run_noop_loop(C.longlong(iters))
	return time.Since(t0)
}

func benchCGoCallback(iters int64) time.Duration {
	atomic.StoreInt64(&goCallbackCounter, 0)
	t0 := time.Now()
	C.run_callbacks(C.longlong(iters))
	return time.Since(t0)
}

// Mimic the WitnessReplayReader.ReadAccountStorage hot path: bounds
// check, length-prefix decode, slice into pre-existing stream. We
// compare this against the CGo callback cost to decide whether the
// EVMC integration breaks even.
type witnessSloadStub struct {
	stream []byte
	pos    int
}

func (w *witnessSloadStub) read() []byte {
	if w.pos >= len(w.stream) {
		return nil
	}
	length := int(w.stream[w.pos])
	w.pos++
	if length == 0 {
		return nil
	}
	val := make([]byte, length)
	copy(val, w.stream[w.pos:w.pos+length])
	w.pos += length
	return val
}

func benchWitnessSload(iters int64) time.Duration {
	// Simulate a typical witness with 32-byte slot values, length-prefix
	// = 0x20 (32). Each entry: [0x20][32 bytes] = 33 bytes. Build a
	// stream long enough for `iters` reads.
	entry := make([]byte, 33)
	entry[0] = 32
	stream := make([]byte, 0, int(iters)*33)
	for i := int64(0); i < iters; i++ {
		stream = append(stream, entry...)
	}

	w := &witnessSloadStub{stream: stream}
	t0 := time.Now()
	for i := int64(0); i < iters; i++ {
		v := w.read()
		_ = v
	}
	return time.Since(t0)
}

func main() {
	runtime.GC()

	const warmupIters = int64(1_000_000)
	const benchIters = int64(50_000_000)

	// Warmup
	_ = benchPureCLoop(warmupIters)
	_ = benchCGoCallback(warmupIters)
	_ = benchWitnessSload(warmupIters)

	// Real measurements (3 runs, take median).
	type result struct{ name string; ns float64 }
	runs := []result{}

	for i := 0; i < 3; i++ {
		d := benchPureCLoop(benchIters)
		ns := float64(d.Nanoseconds()) / float64(benchIters)
		runs = append(runs, result{"pure C loop", ns})
	}
	for i := 0; i < 3; i++ {
		d := benchCGoCallback(benchIters)
		ns := float64(d.Nanoseconds()) / float64(benchIters)
		runs = append(runs, result{"C→Go callback", ns})
	}
	// Witness SLOAD is heavier per-op; use fewer iters.
	witnessIters := benchIters / 10
	for i := 0; i < 3; i++ {
		d := benchWitnessSload(witnessIters)
		ns := float64(d.Nanoseconds()) / float64(witnessIters)
		runs = append(runs, result{"witness SLOAD", ns})
	}

	// Aggregate by name (median of 3).
	type agg struct{ low, mid, hi float64 }
	byName := map[string]*agg{}
	for _, r := range runs {
		a, ok := byName[r.name]
		if !ok {
			a = &agg{}
			byName[r.name] = a
		}
		switch {
		case a.low == 0 || r.ns < a.low:
			old := a.low
			a.low = r.ns
			if old != 0 && (a.mid == 0 || old < a.mid) {
				a.mid = old
			} else if old != 0 && a.hi == 0 {
				a.hi = old
			}
		case a.mid == 0 || r.ns < a.mid:
			old := a.mid
			a.mid = r.ns
			if old != 0 && a.hi == 0 {
				a.hi = old
			}
		default:
			if a.hi == 0 || r.ns < a.hi {
				a.hi = r.ns
			}
		}
	}

	fmt.Println("\n=== CGo callback overhead spike ===")
	fmt.Printf("%-20s  %10s  %10s  %10s\n", "test", "min ns", "med ns", "max ns")
	for _, name := range []string{"pure C loop", "C→Go callback", "witness SLOAD"} {
		a := byName[name]
		fmt.Printf("%-20s  %10.2f  %10.2f  %10.2f\n", name, a.low, a.mid, a.hi)
	}

	// Decision: callback cost vs witness SLOAD ratio.
	cb := byName["C→Go callback"].mid
	sl := byName["witness SLOAD"].mid
	pureC := byName["pure C loop"].mid
	fmt.Printf("\npure-C loop overhead    : %.2f ns/iter (control)\n", pureC)
	fmt.Printf("C→Go callback isolated  : %.2f ns/call (after control: ≈ %.2f ns)\n", cb, cb-pureC)
	fmt.Printf("witness SLOAD per-op    : %.2f ns/call\n", sl)
	fmt.Printf("ratio callback / SLOAD  : %.2fx\n", cb/sl)

	switch {
	case cb < sl*0.5:
		fmt.Println("\nDECISION: evmone integration is a clear win — callbacks are cheap")
	case cb < sl*1.0:
		fmt.Println("\nDECISION: break-even with current SLOAD cost; pursue if compute-heavy contracts dominate")
	case cb < sl*2.0:
		fmt.Println("\nDECISION: marginal; CGo overhead may eat compute gains on state-heavy txs")
	default:
		fmt.Println("\nDECISION: skip evmone integration — callback cost dominates SLOAD")
	}

	fmt.Printf("\ncallback counter (sanity): %d (should equal %d)\n",
		atomic.LoadInt64(&goCallbackCounter), benchIters)
}
