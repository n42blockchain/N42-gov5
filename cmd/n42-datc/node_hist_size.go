// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// node-hist-size — feasibility measurement for a DENSE per-block node-history
// layer (spatial subtree-cluster + temporal version-adjacent). For a sample of
// blocks it counts, per trie depth, how many DISTINCT nodes changed that block
// (= one node-version to store), then extrapolates to the full chain × ~40 B
// per version (32 B hash + ~8 B path/block/nibble diff overhead). This tells us
// whether dense node-history at depth d is tens-of-GB (feasible, store it) or
// TB (infeasible — reconstruct via modified-sibling instead). Uses OUR forward
// changesets (acctcs/storcs, which carry new values), never reth's.
package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/n42blockchain/N42/internal/ethel"
)

func runNodeHistSize(args []string) {
	fs := flag.NewFlagSet("node-hist-size", flag.ExitOnError)
	csDir := fs.String("changesets", `D:/N42-eth1177/chain/freezer`, "acctcs/storcs freezer dir")
	sampleEvery := fs.Uint64("sample-every", 1000, "sample 1 in N blocks")
	maxBlock := fs.Uint64("max", 0, "last block (0 = all available)")
	_ = fs.Parse(args)

	acctTbl := openCS(*csDir, "acctcs")
	defer acctTbl.Close()
	storTbl := openCS(*csDir, "storcs")
	defer storTbl.Close()

	avail := acctTbl.Items()
	if *maxBlock == 0 || *maxBlock > avail {
		*maxBlock = avail
	}
	fmt.Printf("node-hist-size: changesets=%s avail=%d sample 1/%d (depths 1..8)\n", *csDir, avail, *sampleEvery)

	const minD, maxD = 1, 8
	// summed distinct-changed-nodes over sampled blocks, per depth
	var acctSum, storSum [maxD + 1]uint64
	var acctChgTot, storChgTot uint64 // raw change events (leaves)
	sampled := 0
	_ = context.Background

	seen := make([]map[string]struct{}, maxD+1)
	for d := minD; d <= maxD; d++ {
		seen[d] = make(map[string]struct{}, 4096)
	}
	clearSeen := func() {
		for d := minD; d <= maxD; d++ {
			for k := range seen[d] {
				delete(seen[d], k)
			}
		}
	}

	// SLOT-LEVEL storage-trie sampling (M4 sizing): node path = nibbles of
	// keccak(slot) INSIDE the changed contract's own storage trie, keyed by
	// addrHash||slotNibblePrefix. Depth 0 = one version of the contract's
	// storage ROOT per block it changed. This is what the M4 decision needs —
	// the unified-trie numbers above never leave the keccak(addr) prefix.
	const maxSD = 4
	var slotSum [maxSD + 1]uint64
	seenS := make([]map[string]struct{}, maxSD+1)
	for d := 0; d <= maxSD; d++ {
		seenS[d] = make(map[string]struct{}, 4096)
	}
	clearSeenS := func() {
		for d := 0; d <= maxSD; d++ {
			for k := range seenS[d] {
				delete(seenS[d], k)
			}
		}
	}

	t0 := time.Now()
	for n := uint64(0); n < *maxBlock; n += *sampleEvery {
		clearSeen()
		// account trie: node path = nibbles of keccak(addr)
		if ab, err := acctTbl.Retrieve(n); err == nil && len(ab) > 0 {
			acc, derr := ethel.DecodeAccountChanges(ab)
			if derr == nil {
				for i := range acc {
					h := keccak(acc[i].Address[:])
					nb := nibblesOf(h[:])
					acctChgTot++
					for d := minD; d <= maxD; d++ {
						seen[d][string(nb[:d])] = struct{}{}
					}
				}
			}
		}
		for d := minD; d <= maxD; d++ {
			acctSum[d] += uint64(len(seen[d]))
		}
		clearSeen()
		clearSeenS()
		// unified storage trie: node path = nibbles of keccak(addr)||keccak(slot)
		if sb, err := storTbl.Retrieve(n); err == nil && len(sb) > 0 {
			_ = ethel.DecodeStorageChangesFunc(sb, func(addr, slot, _, _ []byte) error {
				ah := keccak(addr)
				sh := keccak(slot)
				var key [64]byte
				copy(key[:32], ah[:])
				copy(key[32:], sh[:])
				nb := nibblesOf(key[:])
				storChgTot++
				for d := minD; d <= maxD; d++ {
					seen[d][string(nb[:d])] = struct{}{}
				}
				// slot-level: contract identity + first d nibbles of slotHash
				snb := nibblesOf(sh[:])
				for d := 0; d <= maxSD; d++ {
					seenS[d][string(ah[:])+string(snb[:d])] = struct{}{}
				}
				return nil
			})
		}
		for d := minD; d <= maxD; d++ {
			storSum[d] += uint64(len(seen[d]))
		}
		for d := 0; d <= maxSD; d++ {
			slotSum[d] += uint64(len(seenS[d]))
		}
		sampled++
	}

	scale := float64(*sampleEvery)
	const bytesPerVer = 40.0 // 32B hash + ~8B (path/block delta + changed-nibble)
	fmt.Printf("sampled %d blocks in %s | leaf-changes(sampled): acct=%d stor=%d → full≈%.2e / %.2e\n",
		sampled, time.Since(t0).Truncate(time.Millisecond), acctChgTot, storChgTot,
		float64(acctChgTot)*scale, float64(storChgTot)*scale)
	fmt.Printf("%-6s %14s %10s %14s %10s %10s\n", "depth", "acct-versions", "acctGB", "stor-versions", "storGB", "totalGB")
	var cumAcct, cumStor float64
	for d := minD; d <= maxD; d++ {
		aFull := float64(acctSum[d]) * scale
		sFull := float64(storSum[d]) * scale
		aGB := aFull * bytesPerVer / 1e9
		sGB := sFull * bytesPerVer / 1e9
		cumAcct += aGB
		cumStor += sGB
		fmt.Printf("%-6d %14.3e %10.1f %14.3e %10.1f %10.1f\n", d, aFull, aGB, sFull, sGB, aGB+sGB)
	}
	fmt.Printf("=> dense node-history depths [D..8] total GB (acct+stor), by split depth D:\n")
	// cumulative from the DEEP end: sum depths D..8
	var suffix float64
	for d := maxD; d >= minD; d-- {
		aFull := float64(acctSum[d]) * scale
		sFull := float64(storSum[d]) * scale
		suffix += (aFull + sFull) * bytesPerVer / 1e9
		fmt.Printf("   D=%d (store depths %d..8 dense): %.1f GB\n", d, d, suffix)
	}

	fmt.Printf("--- SLOT-LEVEL storage-trie node-history (per-contract tries; M4 sizing) ---\n")
	fmt.Printf("%-6s %14s %10s %12s\n", "sdepth", "versions/full", "GB", "cumGB(0..d)")
	var cumSlot float64
	for d := 0; d <= maxSD; d++ {
		full := float64(slotSum[d]) * scale
		gb := full * bytesPerVer / 1e9
		cumSlot += gb
		fmt.Printf("%-6d %14.3e %10.1f %12.1f\n", d, full, gb, cumSlot)
	}
	fmt.Printf("(sdepth 0 = one storage-ROOT version per contract per changed block — the minimum dense layer;\n cumGB(0..d) = store slot-level depths 0..d dense)\n")
}
