// parallel-evm-demo: synthetic workload demo for the Block-STM
// parallel executor. Runs N transactions against an MVHashMap with
// configurable conflict ratio, then calls FinalizeBlock + Apply to
// produce the final state. Verifies the parallel result matches a
// sequential reference and reports throughput.
//
// Usage:
//   build/bin/parallel-evm-demo --txs 100 --workers 8 --conflict 0.3 --runs 5
//
// Output (per run):
//   parallel: <duration>   seq: <duration>   speedup: Nx   verified=true/false
//
// Worker tuning:
//   --workers 4 or 8 is the sweet spot for the current PoC scheduler.
//   --workers >= 16 on contended workloads (low hot-account count, high
//   conflict %) can stall: the simplified estimate-retry loop spins
//   when many workers contend for a small set of hot keys. Phase 4's
//   condvar-based dependency blocking will fix this; for now stay at
//   <= 8 workers in heavy contention.
package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

func main() {
	numTxs := flag.Int("txs", 100, "number of transactions per block")
	workers := flag.Int("workers", runtime.NumCPU(), "parallel worker goroutines")
	conflictPct := flag.Float64("conflict", 0.30, "fraction of txs that touch a shared hot account (0.0=disjoint, 1.0=all-conflict)")
	runs := flag.Int("runs", 5, "repetitions for timing")
	hotAccts := flag.Int("hot", 4, "number of hot accounts (lower => more contention)")
	seed := flag.Int64("seed", 42, "RNG seed for reproducible workload")
	flag.Parse()

	if *numTxs < 1 {
		fmt.Fprintln(os.Stderr, "--txs must be >= 1")
		os.Exit(1)
	}
	rng := rand.New(rand.NewSource(*seed))

	fmt.Printf("Block-STM parallel-evm demo\n")
	fmt.Printf("  txs=%d  workers=%d  conflict=%.0f%%  hotAccts=%d  runs=%d  seed=%d\n",
		*numTxs, *workers, *conflictPct*100, *hotAccts, *runs, *seed)

	for run := 0; run < *runs; run++ {
		// Build workload: list of (txIdx, target_addr, action).
		// Conflicting txs touch one of `hotAccts` shared addresses.
		// Disjoint txs touch a per-tx unique address.
		txTargets := make([]types.Address, *numTxs)
		for i := 0; i < *numTxs; i++ {
			if rng.Float64() < *conflictPct {
				h := rng.Intn(*hotAccts)
				copy(txTargets[i][:], []byte{0xff, byte(h)})
			} else {
				// per-tx disjoint addr (high bytes seeded by i)
				txTargets[i][0] = byte(i >> 8)
				txTargets[i][1] = byte(i)
				txTargets[i][2] = 0xa0
			}
		}

		// === Parallel run ===
		base := state.NewMapBaseReader(nil)
		executor := func(txIdx int) state.TxExecutor {
			return func(v *state.MVStateView) error {
				ev := state.NewEVMStateView(v)
				addr := txTargets[txIdx]
				// Read+write account counter slot 0 (simulates ERC20 balance).
				slot := types.Hash{}
				cur, err := ev.ReadStorage(addr, slot)
				if err != nil {
					return err
				}
				if v.AbortPending() {
					return nil
				}
				ev.WriteStorage(addr, slot, cur.AddUint64(cur, 1))
				return nil
			}
		}
		t0 := time.Now()
		_, mv, err := state.ExecuteBlockParallel(*numTxs, *workers, base, executor)
		if err != nil {
			fmt.Fprintln(os.Stderr, "parallel exec:", err)
			os.Exit(1)
		}
		// Build outputs (gas/logs/coinbase are dummy).
		outs := make([]state.TxOutput, *numTxs)
		for i := range outs {
			outs[i] = state.TxOutput{TxIdx: i, GasUsed: 21000, Status: 1}
		}
		bc, err := state.FinalizeBlock(mv, outs, types.Address{})
		if err != nil {
			fmt.Fprintln(os.Stderr, "finalize:", err)
			os.Exit(1)
		}
		parTarget := state.NewMapApplyTarget()
		if err := bc.Apply(parTarget); err != nil {
			fmt.Fprintln(os.Stderr, "apply:", err)
			os.Exit(1)
		}
		parDur := time.Since(t0)

		// === Sequential reference ===
		seqState := make(map[string]uint64)
		t1 := time.Now()
		for i := 0; i < *numTxs; i++ {
			k := txTargets[i].Hex()
			seqState[k] = seqState[k] + 1
		}
		seqDur := time.Since(t1)

		// === Verify ===
		matched := true
		mismatch := ""
		for addr, want := range seqState {
			var addrBytes types.Address
			// reverse Hex (no 0x for our internal map; but Hex returns 0x... — use it as-is)
			_ = addrBytes
			// Find in parTarget.Storage by walking; simpler: re-decode by addr lookup
			// We stored via ev.WriteStorage(addr, slot, cur+1). cur is the running counter.
			// For verification, just count how many times each addr appears as txTarget.
			// Scan parTarget.Storage[addr][slot].
			_ = addr
			_ = want
		}
		// Simpler verification: compare per-target hit counts.
		seqHits := make(map[types.Address]uint64, *numTxs)
		for _, t := range txTargets {
			seqHits[t]++
		}
		for addr, want := range seqHits {
			slot := types.Hash{}
			slots := parTarget.Storage[addr]
			var got uint64
			if slots != nil {
				if v := slots[slot]; len(v) > 0 {
					for _, b := range v {
						got = (got << 8) | uint64(b)
					}
				}
			}
			if got != want {
				matched = false
				mismatch = fmt.Sprintf(" addr=%x got=%d want=%d", addr, got, want)
				break
			}
		}

		// Speedup (note: seq reference is a trivial map ops baseline,
		// not full EVM — gives a HARD ceiling for what parallel can
		// match here. Real EVM workloads would show better scaling.)
		var speedup float64
		if seqDur > 0 {
			speedup = float64(parDur) / float64(seqDur)
		}
		fmt.Printf("  run %d: parallel=%-12v  seqRef=%-12v  par/seqRef=%.2fx  verified=%t%s\n",
			run+1, parDur.Truncate(time.Microsecond),
			seqDur.Truncate(time.Microsecond), speedup, matched, mismatch)

		if !matched {
			os.Exit(2)
		}
	}

	// Print MV write key distribution (sanity).
	fmt.Printf("\nNote: seqRef baseline is a plain Go map (NO EVM); the ratio measures\n")
	fmt.Printf("      parallel's overhead vs the cheapest-possible reference. With a real\n")
	fmt.Printf("      EVM (Phase 3 Part 6), parallel should beat sequential EVM substantially\n")
	fmt.Printf("      on conflict<70%% workloads.\n")

	// Demo unique-key counts under different conflict ratios for context.
	addrSet := make(map[types.Address]bool, *numTxs)
	for i := 0; i < *numTxs; i++ {
		var a types.Address
		if rngVal := i; rngVal%10 < int(*conflictPct*10) {
			copy(a[:], []byte{0xff, byte(rngVal % *hotAccts)})
		} else {
			a[0] = byte(rngVal >> 8)
			a[1] = byte(rngVal)
			a[2] = 0xa0
		}
		addrSet[a] = true
	}
	keys := make([]types.Address, 0, len(addrSet))
	for k := range addrSet {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	fmt.Printf("\nUnique target addrs in workload: %d (of %d txs)\n", len(addrSet), *numTxs)
}
