// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// backfill.go — resume dirty-mark backfill.
//
// The node-flush pending sets (accDirty/accTouched, stoDirty) live only in
// memory and span epochs, so ANY restart (graceful or crash) that lands
// mid-epoch loses the marks accumulated since each level's epoch start. The
// next epoch-boundary flush then silently OMITS those paths' records (the 2M
// A/B measurement: 28 DatcStorNode rows lost across one hard kill). The
// reader tolerates a missing record via the leaf-history fold fallback, so
// the hole degrades query latency rather than correctness — but it compounds
// with every restart of a multi-day build.
//
// backfillDirty replays the changesets of [min_d epochStart(d), start)
// through the decode pipeline and re-applies ONLY the dirty marks:
//   - accDirty/accTouched and stoDirty get their bits back. Per-level block
//     filter: block n marks level d iff n falls inside start's d-epoch
//     (n >= start - start%e[d]); e[d]==1 levels carry no cross-restart state.
//   - chg agg events are NOT replayed: flushChgAgg drains them into the same
//     tx as every batch commit, so the rows covering [epochStart, start) are
//     already on disk — replaying would write duplicate rows.
//   - leaf rows / StoRoot / trie are untouched (already committed).
//
// accLastFull/stoLastFull stay cold: the resumed FULL-degradation and
// tombstone materialization remain (safe — row bloat only). This pass closes
// the omission hole, not the bloat.

package main

import (
	"fmt"
	"os"
	"time"
)

// backfillDirty rebuilds the in-memory dirty marks for every level whose
// current epoch started before `start`. Call once on resume, before the main
// build loop.
func (b *builder) backfillDirty(start uint64) error {
	from := start
	for d := 0; d <= maxChgDepth; d++ {
		if b.sched.e[d] <= 1 {
			continue // epoch == block: flushed every block, no cross-restart state
		}
		if es := start - start%b.sched.e[d]; es < from {
			from = es
		}
	}
	if from >= start {
		return nil // resume landed exactly on every level's epoch boundary
	}
	fmt.Printf("[datc] resume dirty-mark backfill: replaying changeset marks [%d, %d) (%d blocks)\n",
		from, start, start-from)
	t0 := time.Now()
	pipe := startDecodePipeline(b, from, start, 3)
	defer pipe.Stop()
	lastBeat := time.Now()
	for n := from; n < start; n++ {
		dec, err := pipe.Next(n)
		if err != nil {
			return fmt.Errorf("backfill block %d: %w", n, err)
		}
		for addr := range dec.dirtyA {
			ah, ok := dec.ahash[addr]
			if !ok {
				ah = b.addrHash(addr)
			}
			b.backfillAccMarks(nibblesOf(ah[:]), n, start)
		}
		for addr, slots := range dec.dirtyS {
			ah, ok := dec.ahash[addr]
			if !ok {
				ah = b.addrHash(addr)
			}
			if _, also := dec.dirtyA[addr]; !also {
				// A storage-only change still dirties the account leaf (its
				// storageRoot) — same rule as emitBlock.
				b.backfillAccMarks(nibblesOf(ah[:]), n, start)
			}
			var domain [40]byte // addrHash(32) ‖ 8 zero bytes, as recordChangeStorage builds it
			copy(domain[:], ah[:])
			for slot := range slots {
				sh, ok := dec.shash[slot]
				if !ok {
					sh = b.slotHash(slot)
				}
				b.backfillStoMarks(domain[:], nibblesOf(sh[:]), n, start)
			}
		}
		releaseDecodedBlock(dec)
		if time.Since(lastBeat) > 10*time.Second {
			fmt.Fprintf(os.Stderr, "[datc] backfill %d/%d (%.0f blk/s)\n",
				n-from, start-from, float64(n-from)/time.Since(t0).Seconds())
			lastBeat = time.Now()
		}
	}
	fmt.Printf("[datc] dirty-mark backfill done: %d blocks in %s\n",
		start-from, time.Since(t0).Round(time.Second))
	return nil
}

// backfillAccMarks re-applies recordChange's account dirty marks for block n,
// restricted to levels whose current epoch (relative to `start`) contains n.
// chg agg events are intentionally not touched.
func (b *builder) backfillAccMarks(nibs []byte, n, start uint64) {
	maxD := maxChgDepth
	if maxD > len(nibs)-1 {
		maxD = len(nibs) - 1
	}
	idx := uint32(0)
	for d := 0; d <= maxD; d++ {
		if b.sched.e[d] > 1 && n >= start-start%b.sched.e[d] {
			bit := uint16(1) << nibs[d]
			if cur := b.accDirty[d][idx]; cur == 0 {
				b.accTouched[d] = append(b.accTouched[d], idx)
				b.accDirty[d][idx] = bit
			} else if cur&bit == 0 {
				b.accDirty[d][idx] = cur | bit
			}
		}
		idx = idx*16 + uint32(nibs[d])
	}
}

// backfillStoMarks is the storage-side twin: stoDirty keys are
// domain(40) ‖ first-d-nibbles, exactly as recordChangeStorage builds them.
func (b *builder) backfillStoMarks(domain []byte, slotNibs []byte, n, start uint64) {
	maxD := maxChgDepth
	if maxD > len(slotNibs)-1 {
		maxD = len(slotNibs) - 1
	}
	kb := b.chgKeyScratch[:0]
	kb = append(kb, domain...)
	kb = append(kb, slotNibs[:maxD]...)
	b.chgKeyScratch = kb
	for d := 0; d <= maxD; d++ {
		if b.sched.e[d] <= 1 || n < start-start%b.sched.e[d] {
			continue
		}
		pk := kb[:len(domain)+d]
		bit := uint16(1) << slotNibs[d]
		if p, ok := b.stoDirty[d][string(pk)]; ok {
			*p |= bit
		} else {
			v := bit
			b.stoDirty[d][string(pk)] = &v
		}
	}
}
